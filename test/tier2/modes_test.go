//go:build tier2

package tier2

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/paths"
)

// Per-mode gateway acceptance, and the point where Tier 1's readiness checks are
// held to account.
//
// The DMR and YSF paths already had Tier 2 coverage (dmr_test.go, ysf_test.go);
// P25, NXDN, M17 and D-Star had none, so nothing in this project had ever
// established that the pinned daemons will so much as parse the files it writes
// for them. TestTier2_GatewaysAcceptRenderedConfigs is that claim for all of them.
//
// The second test is the one that matters more. internal/config/mode_readiness.go
// asserts that certain configurations are broken, and two of those assertions say
// something a loopback can actually check: that the daemon does not start at all.
// An assertion like that is worth exactly as much as its evidence, and reading
// upstream's source is not evidence — upstream changes, and a pinned-version bump
// that removes a guard would leave Waypoint refusing to start a daemon that works
// fine. So the two daemon-fatal claims are executed against the real binaries here.
//
// The other readiness errors are deliberately NOT tested here, and cannot be: a
// four-bit colour code set to 20 produces a daemon that starts, stays up, binds
// its socket and reports itself healthy. That is the whole reason those checks
// exist, and a loopback that cannot key a radio has nothing to say about them.
// Their evidence is the protocol field width cited at each check.

const (
	// Where each gateway binds MMDVM-Host's side of the loopback. These mirror the
	// unexported constants in internal/config/render.go — spelled out rather than
	// exported for a test, the same way ysf_test.go carries 3200/4200 — so a
	// renderer that silently moved a port fails here rather than on a bench.
	p25GwPort   = 42020 // P25Gateway [General] LocalPort
	nxdnGwPort  = 14020 // NXDNGateway [General] LocalPort
	m17GwPort   = 17010 // M17Gateway [General] LocalPort
	dstarGwPort = 20010 // dstargateway [General] HBPort

	// How long a daemon is given to decide it dislikes its configuration. The
	// pinned daemons that reject a config do it in well under a second (they exit
	// before opening a socket); this is generous so a loaded bench Pi does not
	// produce a false pass.
	startupGrace = 4 * time.Second
)

// seedDir is the shipped-hostlist directory, relative to this package.
const seedDir = "../../internal/hostsrc/seed"

// benchModel is a node that Tier 1 considers fully ready: a callsign, a radio ID,
// real frequencies, and in-range parameters for every mode. Every positive test
// below starts from this, and asserts Tier 1 agrees before it starts a daemon —
// so "the daemon accepted our config" is always a statement about a configuration
// the product would actually have shipped to the device.
func benchModel() *config.Model {
	return &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202", Power: "1", Location: "Milton, EM60"},
		// Explicit frequencies are not decoration: YSFGateway aborts without them
		// (#145) and M17Gateway renders them into [Info]. A bench node is a 70cm
		// simplex hotspot.
		Modem:  config.Modem{RXFreqHz: "438800000", TXFreqHz: "438800000"},
		Modes:  config.Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true},
		DMR:    config.DMR{ColorCode: "1", ID: "3180202"},
		DMRNet: config.DMRNet{LocalPort: "62032", GatewayAddress: "127.0.0.1", GatewayPort: "62031", Slot1: true, Slot2: true},
		Networks: []config.Network{
			{Name: "BM_3102_United_States", Type: config.NetBrandmeister, Primary: true,
				Address: "127.0.0.1", Port: "62031", Password: "passw0rd", Enabled: true},
		},
		YSF:     config.YSF{},
		YSFGW:   config.YSFGateway{Suffix: "ND", YSFNetwork: true, InactivityTimeout: "30"},
		P25:     config.P25{NAC: "293"},
		P25GW:   config.P25Gateway{Voice: false, RFHangTime: "120", NetHangTime: "60"},
		NXDN:    config.NXDN{RAN: "1"},
		NXDNGW:  config.NXDNGateway{Voice: false, RFHangTime: "120", NetHangTime: "60"},
		DStar:   config.DStar{Module: "B"},
		DStarGW: config.DStarGateway{IRCDDBHostname: "ircv4.openquad.net", IRCDDBUsername: "KN4OQW", ReflectorReconnect: "Never"},
		M17:     config.M17{CAN: "0"},
		M17GW:   config.M17Gateway{Suffix: "H", HangTime: "240"},
		// A syntactically valid AuthKey. It is not a real DAPNET credential and no
		// test below logs in with it — see TestTier2_ReadinessErrorsPredictDaemonFailure
		// for why the POCSAG positive case is a negative one.
		POCSAG: config.POCSAG{Frequency: "439987500", Server: "dapnet.afu.rwth-aachen.de", AuthKey: "not-a-real-key"},
		FM:     config.FM{CTCSS: "127.3", AccessMode: "0", Timeout: "180"},
	}
}

// localise rewrites the managed device paths in a rendered config to a temporary
// directory holding the SHIPPED hostlists, and returns the config.
//
// The renderers emit absolute paths under /var/lib/waypoint that exist on a device
// and not on a build host, which is the only thing standing between a rendered
// config and a daemon here. Nothing but the directory changes: the hostlist
// content is the file that ships, and the routing, ports and link behaviour under
// test are untouched. It is the same substitution reflector_test.go's
// localHostlist makes, generalised to the whole directory because P25, NXDN and
// D-Star each reference several paths rather than one.
func localise(t *testing.T, ini string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range []string{"P25Hosts.json", "NXDNHosts.json", "M17Hosts.txt", "DStar_Hosts.json"} {
		b, err := os.ReadFile(filepath.Join(seedDir, f))
		if err != nil {
			t.Fatalf("read shipped %s: %v", f, err)
		}
		if err := os.WriteFile(filepath.Join(dir, f), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// The audio and custom-hosts directories a device has. A missing one is not
	// fatal to any of these daemons (it only disables the spoken announcements),
	// but creating them keeps the run's logs free of errors that are the test
	// environment's fault rather than the config's.
	for _, d := range []string{"P25Audio", "NXDNAudio", "M17Audio", "dstar", "dstar-hostsfiles.d"} {
		if err := os.MkdirAll(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return strings.ReplaceAll(ini, paths.EtcDir, dir)
}

// writeConf writes a rendered config to a temp file and returns its path.
func writeConf(t *testing.T, name, ini string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(ini), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// daemonRun is a started gateway under observation.
type daemonRun struct {
	cmd *exec.Cmd
	log *strings.Builder
}

// start runs a pinned daemon against a config file, skipping the test if the
// binary was not built. The process is killed and its log dumped on cleanup —
// unlike reflector_test.go's startDaemon these daemons never link anything real,
// so there is no unlink worth waiting for.
func start(t *testing.T, name, confPath string) *daemonRun {
	t.Helper()
	bin := gwBin(name)
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("%s binary not built: %v", name, err)
	}
	cmd := exec.Command(bin, confPath)
	out := &strings.Builder{}
	cmd.Stdout, cmd.Stderr = out, out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	r := &daemonRun{cmd: cmd, log: out}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
		if s := out.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- %s output ---\n%s", name, s)
		}
	})
	return r
}

// exitedWithin waits up to d for the process to exit, and reports whether it did
// along with its exit code. A daemon that rejects its configuration exits well
// inside the window; one that accepted it never does, so the wait costs the full
// grace period exactly once per positive case.
func (r *daemonRun) exitedWithin(d time.Duration) (bool, int) {
	done := make(chan error, 1)
	go func() { done <- r.cmd.Wait() }()
	select {
	case <-done:
		return true, r.cmd.ProcessState.ExitCode()
	case <-time.After(d):
		return false, 0
	}
}

// ownsUDP reports whether something already holds a UDP port — i.e. whether the
// daemon bound the loopback its config told it to.
func ownsUDP(port int) bool {
	c, err := net.ListenPacket("udp4", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return true
	}
	c.Close()
	return false
}

// The gateway-startup claim, for every mode that has a daemon and did not already
// have one: the config Waypoint generates must be a config the pinned daemon
// accepts — it has to parse, bind MMDVM-Host's loopback for that mode, and stay
// up.
//
// Tier 1's render tests prove only that the bytes are the bytes we meant to write.
// Whether the daemon agrees they describe a valid node is a different claim and
// this is the only thing that makes it.
//
// DMR and YSF are absent because they are already covered — TestTier2_ParrotEcho
// and TestTier2_DGIdGatewayAcceptsRenderedConfig make the same claim and go
// further, carrying traffic over the loopback rather than only binding it.
func TestTier2_GatewaysAcceptRenderedConfigs(t *testing.T) {
	m := benchModel()
	if problems := m.ModeProblems(); m.HasModeErrors() {
		t.Fatalf("the bench model is not a ready node, so nothing below tests what it claims to:\n%+v", problems)
	}

	for _, tc := range []struct {
		mode     config.Mode
		daemon   string // binary name, as build.sh drops it
		conf     string // rendered file name, as the device carries it
		render   func(*config.Model) string
		bindPort int
	}{
		{config.ModeP25, "P25Gateway", "P25Gateway.ini", (*config.Model).RenderP25Gateway, p25GwPort},
		{config.ModeNXDN, "NXDNGateway", "NXDNGateway.ini", (*config.Model).RenderNXDNGateway, nxdnGwPort},
		{config.ModeM17, "M17Gateway", "M17Gateway.ini", (*config.Model).RenderM17Gateway, m17GwPort},
		{config.ModeDStar, "dstargateway", "dstargateway.cfg", (*config.Model).RenderDStarGateway, dstarGwPort},
	} {
		t.Run(string(tc.mode), func(t *testing.T) {
			// Nothing else may already hold the port, or "the daemon bound it" is
			// not a claim this test can make.
			if ownsUDP(tc.bindPort) {
				t.Fatalf("UDP :%d is already in use before %s started", tc.bindPort, tc.daemon)
			}

			conf := writeConf(t, tc.conf, localise(t, tc.render(m)))
			run := start(t, tc.daemon, conf)

			if exited, code := run.exitedWithin(startupGrace); exited {
				t.Fatalf("%s exited with %d after reading the generated %s:\n%s",
					tc.daemon, code, tc.conf, run.log.String())
			}
			if !ownsUDP(tc.bindPort) {
				t.Fatalf("%s stayed up but never bound MMDVM-Host's %s loopback :%d\n%s",
					tc.daemon, tc.mode, tc.bindPort, run.log.String())
			}
			t.Logf("PASS: pinned %s accepted the generated %s and owns :%d", tc.daemon, tc.conf, tc.bindPort)
		})
	}
}

// The frequency survey: which daemons actually die when the node has no RF
// frequency set, established against the pinned binaries rather than read off
// upstream's source.
//
// This exists because a narrower version of the same question was answered wrongly
// once already. gateway_requirements.go concluded DAPNETGateway was the only daemon
// that exits before opening anything, having searched upstream for `return 1` and
// not for `assert` — and a bench node then turned up YSFGateway crash-looping on
// exactly that (#215, restart counter 2,127). The correction is not to read the
// sources more carefully; it is to stop reading them for this class of claim.
//
// Four rendered configs carry [Info] frequencies (render.go): MMDVM-Host.ini,
// YSFGateway.ini, DGIdGateway.ini and M17Gateway.ini. Three of them are here.
// MMDVM-HOST IS NOT, and that gap is the honest limit of this test: build.sh does
// not build it (nothing in Tier 2 drives a modem), so the claim that it dies on an
// unset frequency rests on the bench journal in #216 — `Received a NAK to the
// SET_FREQ command from the modem`, exit 1 — and not on anything here. If Tier 2
// ever gains a modem stand-in, this is where that claim belongs.
//
// The daemons that render no frequency at all are in the table too, as controls. A
// survey that only visits the daemons it suspects is how the last one went wrong,
// and "P25Gateway does not care" is worth pinning: it is what makes withholding
// YSFGateway a statement about YSFGateway rather than about frequencies generally.
func TestTier2_UnsetFrequencyDaemonSurvey(t *testing.T) {
	const freq = "438800000" // the bench node's 70cm simplex pair, as benchModel sets it

	for _, tc := range []struct {
		name     string
		daemon   string      // binary name, as build.sh drops it
		conf     string      // rendered file name, as the device carries it
		mode     config.Mode // whose gateway this is, for the registry assertions
		render   func(*config.Model) string
		bindPort int
		dgid     bool     // render the DG-ID variant of the YSF slot
		rx, tx   string   // what the modem section carries for this case
		dies     bool     // measured against the pinned binary, not read off upstream
		wants    []string // store paths the daemon's death makes required
	}{
		// YSFGateway, three ways. Both #145 and #215 describe this as a TRANSMIT
		// frequency requirement, and both are incomplete: CWiresX::setInfo asserts
		// on line 103 AND line 104, so either frequency alone kills it. The
		// requirement therefore names both fields, and these two half-set cases are
		// why — without them the registry would let a node set TX, watch the daemon
		// come up blocked-free, and crash-loop on RX.
		{name: "neither frequency", daemon: "YSFGateway", conf: "YSFGateway.ini", mode: config.ModeYSF, render: (*config.Model).RenderYSFGateway,
			bindPort: ysfGwPort, rx: "", tx: "", dies: true, wants: []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{name: "transmit set, receive unset", daemon: "YSFGateway", conf: "YSFGateway.ini", mode: config.ModeYSF, render: (*config.Model).RenderYSFGateway,
			bindPort: ysfGwPort, rx: "", tx: freq, dies: true, wants: []string{"modem.rx_freq_hz"}},
		{name: "receive set, transmit unset", daemon: "YSFGateway", conf: "YSFGateway.ini", mode: config.ModeYSF, render: (*config.Model).RenderYSFGateway,
			bindPort: ysfGwPort, rx: freq, tx: "", dies: true, wants: []string{"modem.tx_freq_hz"}},

		// The other file that carries [Info] frequencies and is buildable here. It is
		// the YSF mode's OTHER gateway, so this row is also what grounds the
		// requirement's DG-ID exemption: same mode, same [Info] section, no Wires-X.
		{name: "neither frequency", daemon: "DGIdGateway", conf: "DGIdGateway.ini", mode: config.ModeYSF, render: (*config.Model).RenderDGIdGateway,
			bindPort: ysfGwPort, dgid: true, rx: "", tx: "", dies: false},
		{name: "neither frequency", daemon: "M17Gateway", conf: "M17Gateway.ini", mode: config.ModeM17, render: (*config.Model).RenderM17Gateway,
			bindPort: m17GwPort, rx: "", tx: "", dies: false},

		// Controls: no frequency reaches these files at all.
		{name: "neither frequency", daemon: "P25Gateway", conf: "P25Gateway.ini", mode: config.ModeP25, render: (*config.Model).RenderP25Gateway,
			bindPort: p25GwPort, rx: "", tx: "", dies: false},
		{name: "neither frequency", daemon: "NXDNGateway", conf: "NXDNGateway.ini", mode: config.ModeNXDN, render: (*config.Model).RenderNXDNGateway,
			bindPort: nxdnGwPort, rx: "", tx: "", dies: false},
		{name: "neither frequency", daemon: "dstargateway", conf: "dstargateway.cfg", mode: config.ModeDStar, render: (*config.Model).RenderDStarGateway,
			bindPort: dstarGwPort, rx: "", tx: "", dies: false},
	} {
		t.Run(tc.daemon+"/"+tc.name, func(t *testing.T) {
			m := benchModel()
			m.Modem.RXFreqHz, m.Modem.TXFreqHz = tc.rx, tc.tx
			m.YSFGW.EnableDGId = tc.dgid

			if ownsUDP(tc.bindPort) {
				t.Fatalf("UDP :%d is already in use before %s started", tc.bindPort, tc.daemon)
			}
			conf := writeConf(t, tc.conf, localise(t, localHostlist(t, tc.render(m))))
			run := start(t, tc.daemon, conf)
			exited, code := run.exitedWithin(startupGrace)

			switch {
			case tc.dies && !exited:
				t.Fatalf("%s is still running with no frequency — it no longer dies on one, and any requirement Waypoint registers for it now withholds a daemon that would work:\n%s",
					tc.daemon, run.log.String())
			case !tc.dies && exited:
				t.Fatalf("%s exited (%d) with no frequency — it has GAINED a frequency requirement Waypoint does not register, so it will crash-loop on a node that has not set one:\n%s",
					tc.daemon, code, run.log.String())
			case tc.dies:
				// A daemon that cannot start must be one Waypoint does not start, and
				// the block must name the fields that would fix it — otherwise the
				// operator gets a daemon that is silently absent instead of one that
				// silently crash-loops, which is no better.
				missing := blockedFields(m, tc.mode)
				if missing == nil {
					t.Fatalf("%s died (exit %d) and Waypoint does not withhold it, so a node in this state crash-loops:\n%s",
						tc.daemon, code, run.log.String())
				}
				for _, want := range tc.wants {
					if !slices.Contains(missing, want) {
						t.Errorf("%s died for want of %s, but the block names only %v", tc.daemon, want, missing)
					}
				}
				t.Logf("PASS: %s died (exit %d), and Waypoint withholds it naming %v", tc.daemon, code, missing)
				return
			}

			// A survivor must NOT be withheld — the direction that keeps the registry
			// from growing invented requirements. Every daemon here runs with no
			// frequency at all, so blocking one would take a working gateway off a
			// working node. Scoped to this mode: MMDVM-Host is legitimately blocked in
			// all of these models (the node has no frequency for the modem to tune),
			// and that is a different claim, made in the modem case below.
			if missing := blockedFields(m, tc.mode); missing != nil {
				t.Fatalf("%s runs fine with no frequency, but Waypoint withholds it for %v", tc.daemon, missing)
			}

			// A survivor has to have got far enough to bind its loopback. "Did not
			// exit within four seconds" on its own would also be true of a daemon
			// wedged before it opened anything, and that is not the claim.
			if !ownsUDP(tc.bindPort) {
				t.Fatalf("%s stayed up with no frequency but never bound :%d, so it did not really start:\n%s",
					tc.daemon, tc.bindPort, run.log.String())
			}
			t.Logf("PASS: %s started and bound :%d with no frequency set", tc.daemon, tc.bindPort)
		})
	}

	// MMDVM-Host, which no run above covers and none can: build.sh does not build
	// it, so what follows asserts only that Waypoint withholds it and names the
	// frequencies. That the host actually dies without them is #216's bench
	// finding — `Received a NAK to the SET_FREQ command from the modem`, exit 1,
	// restart counter in the hundreds, with DisplayLevel=0 hiding the cause — and
	// it is the one claim in this file resting on a journal rather than a run.
	// Anyone adding a modem stand-in to Tier 2 should promote it.
	t.Run("MMDVM-Host/withheld on the bench finding alone", func(t *testing.T) {
		// A modem port on both models: benchModel carries none (nothing in Tier 2
		// opens a modem), and without one MMDVM-Host is withheld for that instead,
		// which would make the frequency claim below vacuous in one direction and
		// wrong in the other.
		withPort := func(rx, tx string) *config.Model {
			m := benchModel()
			m.Modem.Port, m.Modem.RXFreqHz, m.Modem.TXFreqHz = "/dev/ttyACM0", rx, tx
			return m
		}

		missing := blockedFields(withPort("", ""), config.ModeModem)
		for _, want := range []string{"modem.rx_freq_hz", "modem.tx_freq_hz"} {
			if !slices.Contains(missing, want) {
				t.Errorf("MMDVM-Host block names %v, want it to include %s", missing, want)
			}
		}
		if got := blockedFields(withPort("438800000", "438800000"), config.ModeModem); got != nil {
			t.Errorf("MMDVM-Host is withheld from a node that has both frequencies: %v", got)
		}
		t.Logf("PASS: MMDVM-Host is withheld naming %v, and runs with the frequencies set (daemon behaviour: #216, not measured here)", missing)
	})
}

// blockedFields returns the store paths Waypoint says are missing for one mode's
// gateway, or nil when that gateway is not withheld at all. Scoped per mode
// because most models here legitimately block MMDVM-Host as well, and a test
// asking "is this gateway withheld" must not be answered by a different one.
func blockedFields(m *config.Model, mode config.Mode) []string {
	for _, r := range m.UnmetGatewayRequirements() {
		if r.Mode == mode {
			return r.Missing
		}
	}
	return nil
}

// The accountability test: where internal/config/mode_readiness.go claims a
// configuration stops a daemon from starting, the real daemon must actually fail
// to start.
//
// Two claims are checkable this way, and both are checked in both directions —
// the bad config fails AND the good one does not — because a check that fires on
// everything proves nothing. A pinned-version bump that removes either guard
// fails here, which is the point: the alternative is Waypoint quietly refusing to
// start a daemon that works.
func TestTier2_ReadinessErrorsPredictDaemonFailure(t *testing.T) {
	// #145: YSFGateway builds Wires-X unconditionally and CWiresX::setInfo asserts
	// the transmit frequency is non-zero, so a node with no frequency does not
	// misbehave — the daemon aborts and systemd restart-loops it.
	t.Run("YSFGateway aborts without a transmit frequency", func(t *testing.T) {
		m := benchModel()
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""

		// Tier 1 must be making this claim, or this test is checking something the
		// product does not say.
		var claimed bool
		for _, p := range m.ProblemsFor(config.ModeYSF) {
			if p.Mode == config.ModeYSF && p.Field == "modem.tx_freq_hz" && p.Severity == config.SeverityError {
				claimed = true
			}
		}
		if !claimed {
			t.Fatal("mode_readiness no longer claims YSFGateway aborts without a transmit frequency; either the claim or this test is stale")
		}

		conf := writeConf(t, "YSFGateway.ini", localHostlist(t, m.RenderYSFGateway()))
		run := start(t, "YSFGateway", conf)
		exited, code := run.exitedWithin(startupGrace)
		if !exited {
			t.Fatalf("YSFGateway is still running with no transmit frequency — #145's assert is gone, and mode_readiness now refuses a config that works:\n%s",
				run.log.String())
		}
		t.Logf("PASS: YSFGateway died (exit %d) with no transmit frequency, as mode_readiness predicts", code)

		// The other direction: with frequencies it stays up, so the finding is about
		// the frequency and not about everything else in the file.
		m2 := benchModel()
		conf2 := writeConf(t, "YSFGateway.ini", localHostlist(t, m2.RenderYSFGateway()))
		run2 := start(t, "YSFGateway", conf2)
		if exited, code := run2.exitedWithin(startupGrace); exited {
			t.Fatalf("YSFGateway also died WITH frequencies (exit %d), so the finding does not isolate them:\n%s",
				code, run2.log.String())
		}
		t.Log("PASS: the same config with frequencies stays up")
	})

	// DAPNETGateway.cpp:283 rejects an empty or placeholder AuthKey and returns 1
	// before constructing the network object. This is the one requirement that
	// WITHHOLDS a daemon (gateway_requirements.go) rather than merely reporting,
	// and until now the evidence for withholding it was a reading of upstream.
	t.Run("DAPNETGateway exits without a usable AuthKey", func(t *testing.T) {
		for _, tc := range []struct{ name, key string }{
			{"empty", ""},
			{"upstream placeholder", "TOPSECRET"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				m := benchModel()
				m.POCSAG.AuthKey = tc.key

				// Waypoint must be withholding the daemon for this key.
				if units := m.BlockedGatewayUnits(); len(units) == 0 {
					t.Fatalf("gateway_requirements no longer blocks DAPNETGateway for key %q; either the rule or this test is stale", tc.key)
				}

				// Waypoint would not start it at all — this runs it anyway, which is
				// the only way to find out whether not starting it was right.
				exited, code, _ := runDAPNET(t, m)
				if !exited {
					t.Fatalf("DAPNETGateway is still running with AuthKey %q — its guard is gone, and Waypoint is withholding a daemon that would work", tc.key)
				}
				if code == 0 {
					t.Errorf("DAPNETGateway exited cleanly (0) rather than failing; systemd Restart=on-failure would not retry it")
				}
				t.Logf("PASS: DAPNETGateway exited %d with AuthKey %q, as gateway_requirements predicts", code, tc.key)
			})
		}

		// And the guard must be about the KEY, not about the rest of the file — a
		// guard that rejects everything would make the block above vacuously right
		// while withholding daemons that would run.
		//
		// This is not a login test and carries no credential. DAPNETGateway also
		// exits on "Cannot login to the DAPNET network", so a made-up key dies
		// either way; what separates the two is WHERE, and the guard names itself
		// in the log. Comparing the two runs' logs is therefore the check, and the
		// empty-key run is its positive control: if the guard's own message never
		// appears there either, the log plane is not observable in this environment
		// and the comparison is skipped rather than passed.
		t.Run("the guard is about the key alone", func(t *testing.T) {
			const guardMsg = "AuthKey not set or invalid"

			blocked := benchModel()
			blocked.POCSAG.AuthKey = ""
			_, _, blockedLog := runDAPNET(t, blocked)
			if !strings.Contains(blockedLog, guardMsg) {
				t.Skipf("the AuthKey guard's message never reached stdout or the MQTT log plane, so a valid key cannot be distinguished from a rejected one here:\n%s", blockedLog)
			}

			ok := benchModel() // a made-up but non-placeholder key
			if units := ok.BlockedGatewayUnits(); len(units) != 0 {
				t.Fatalf("Waypoint blocks the daemon for a syntactically valid key: %v", units)
			}
			if _, _, okLog := runDAPNET(t, ok); strings.Contains(okLog, guardMsg) {
				t.Errorf("the AuthKey guard rejected a syntactically valid key, so gateway_requirements is blocking more than missing keys:\n%s", okLog)
			}
			t.Log("PASS: a syntactically valid key clears the AuthKey guard (the run then fails at the DAPNET login, as it must without a real credential)")
		})
	})
}

// runDAPNET renders and runs DAPNETGateway for one model, returning whether it
// exited within the grace period, its exit code, and everything it said.
//
// The log has two possible homes and this collects both. The rendered config sets
// DisplayLevel=0 / MQTTLevel=1, so a running daemon publishes to <name>/log rather
// than stdout — but the AuthKey guard runs before the network object is built, and
// whether the MQTT plane is up that early is upstream's business, not something
// this test should assume either way.
func runDAPNET(t *testing.T, m *config.Model) (exited bool, code int, log string) {
	t.Helper()
	tail := newMQTTTail(t, "dapnet-gateway")
	conf := writeConf(t, "DAPNETGateway.ini", localise(t, m.RenderDAPNETGateway()))
	run := start(t, "DAPNETGateway", conf)
	exited, code = run.exitedWithin(startupGrace)
	return exited, code, run.log.String() + "\n" + tail.text()
}
