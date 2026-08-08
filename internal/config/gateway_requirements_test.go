package config

import (
	"slices"
	"strings"
	"testing"
)

// A gateway daemon that reads a required value at startup, finds it unset, and
// exits is a restart loop, not a running gateway. These tests pin the rule that
// apply withholds such a daemon rather than starting it: no render target, no
// restart, stopped if it was already running, and disabled for boot.

// The POCSAG gate: enabled + no AuthKey is blocked, enabled + a key is not, and a
// disabled mode is never reported (nothing is being withheld from a mode the
// operator turned off).
func TestUnmetGatewayRequirementsPOCSAG(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		authKey string
		blocked bool
	}{
		{"enabled, no key", true, "", true},
		{"enabled, whitespace key", true, "   ", true},
		// Upstream's own guard rejects the literal placeholder from its stock ini as
		// well as the empty string, so a node that pasted the sample is blocked here
		// instead of being left to crash-loop.
		{"enabled, upstream placeholder", true, "TOPSECRET", true},
		{"enabled, real key", true, "abc123", false},
		{"disabled, no key", false, "", false},
		{"disabled, real key", false, "abc123", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			m.Modes.POCSAG = tc.enabled
			m.POCSAG.AuthKey = tc.authKey

			reqs := m.UnmetGatewayRequirements()
			if got := len(reqs) > 0; got != tc.blocked {
				t.Fatalf("UnmetGatewayRequirements() = %+v, want blocked=%v", reqs, tc.blocked)
			}
			if !tc.blocked {
				return
			}
			r := reqs[0]
			if r.Mode != ModePOCSAG {
				t.Errorf("Mode = %q, want %q", r.Mode, ModePOCSAG)
			}
			if r.Unit != unitDAPNETGateway {
				t.Errorf("Unit = %q, want %q", r.Unit, unitDAPNETGateway)
			}
			if !slices.Contains(r.Missing, "pocsag.auth_key") {
				t.Errorf("Missing = %v, want it to name pocsag.auth_key", r.Missing)
			}
		})
	}
}

// Only DAPNETGateway and MMDVM-Host have a locally-knowable guard.
// Every other gateway's early exit is a runtime condition (unresolvable host,
// bound port), not missing config, so none of them may be blocked — inventing a
// requirement here would refuse to start daemons that work today.
func TestNoOtherGatewayIsBlocked(t *testing.T) {
	m := fixture()
	m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
	// Wipe every gateway's optional settings: startup reflectors, suffixes, the
	// ircDDB login, passwords. None of these gate a daemon.
	m.YSFGW.Startup, m.YSFGW.Suffix = "", ""
	m.P25GW.Static, m.NXDNGW.Static, m.M17GW.Startup = "", "", ""
	m.DStarGW.Reflector, m.DStarGW.IRCDDBUsername, m.DStarGW.IRCDDBPassword = "", "", ""
	m.POCSAG.AuthKey = "a-real-key" // the ones that ARE gated, satisfied
	m.Modem.Port, m.General.Callsign, m.General.ID = "/dev/ttyAMA0", "KN4OQW", "3180202"

	if reqs := m.UnmetGatewayRequirements(); len(reqs) != 0 {
		t.Fatalf("a gateway with no locally-knowable requirement was blocked: %+v", reqs)
	}
}

// MMDVM-Host's own requirements. createModem() failing is a RUNTIME condition and
// deliberately not gated; what is knowable locally is that it cannot open a port
// that was never set, and that the station identity every mode transmits under
// must exist.
func TestModemHostRequirements(t *testing.T) {
	for _, tc := range []struct {
		name        string
		mutate      func(*Model)
		wantMissing string
	}{
		{"no modem port", func(m *Model) { m.Modem.Port = "" }, "modem.port"},
		{"no callsign", func(m *Model) { m.General.Callsign = "" }, "general.callsign"},
		{"no id", func(m *Model) { m.General.ID = "" }, "general.id"},
		{"whitespace port", func(m *Model) { m.Modem.Port = "  " }, "modem.port"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			tc.mutate(m)
			if m.ModemHostRuns() {
				t.Fatal("MMDVM-Host must not run when a value it needs is unset")
			}
			var found bool
			for _, r := range m.UnmetGatewayRequirements() {
				if r.Mode == ModeModem && slices.Contains(r.Missing, tc.wantMissing) {
					found = true
					if r.Unit != unitMMDVM {
						t.Errorf("Unit = %q, want %q", r.Unit, unitMMDVM)
					}
				}
			}
			if !found {
				t.Errorf("no ModeModem requirement naming %q: %+v", tc.wantMissing, m.UnmetGatewayRequirements())
			}
		})
	}
}

// A node with every mode off is not misconfigured — it has asked for nothing, so
// nothing is being withheld and the operator must not be shown a fault.
func TestModemHostNotReportedWhenNoModeWantsTheAir(t *testing.T) {
	m := fixture()
	m.Modes = Modes{}
	m.Modem.Port, m.General.Callsign, m.General.ID = "", "", ""

	if m.ModemHostRuns() {
		t.Error("MMDVM-Host must not run when no mode is enabled")
	}
	for _, r := range m.UnmetGatewayRequirements() {
		if r.Mode == ModeModem {
			t.Errorf("an all-modes-off node must report no modem requirement, got %+v", r)
		}
	}
}

// testPaths is a full Paths so every render target has somewhere to go; a blank
// path would drop a target for the wrong reason and mask what these tests assert.
func testPaths() Paths {
	return Paths{
		MMDVM: "/etc/MMDVM-Host.ini", DMRGateway: "/etc/DMRGateway.ini",
		YSFGateway: "/etc/YSFGateway.ini", DGIdGateway: "/etc/DGIdGateway.ini",
		P25Gateway: "/etc/P25Gateway.ini", NXDNGateway: "/etc/NXDNGateway.ini",
		DStarGateway: "/etc/dstargateway.cfg", M17Gateway: "/etc/M17Gateway.ini",
		DAPNETGateway: "/etc/DAPNETGateway.ini",
	}
}

// restartUnitsOf is the unit set apply would (re)start — the rendered targets.
func restartUnitsOf(m *Model, paths Paths) []string {
	var out []string
	for _, t := range m.RenderTargets(paths) {
		if t.Unit != "" {
			out = append(out, t.Unit)
		}
	}
	return out
}

// A blocked gateway contributes no render target, so apply writes no config file
// and restarts no unit for it. Satisfying the requirement brings both back.
func TestBlockedGatewayHasNoRenderTarget(t *testing.T) {
	paths := testPaths()
	hasDAPNET := func(m *Model) bool {
		for _, tgt := range m.RenderTargets(paths) {
			if tgt.Unit == unitDAPNETGateway {
				return true
			}
		}
		return false
	}

	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""
	if hasDAPNET(m) {
		t.Error("POCSAG enabled with no AuthKey still rendered a DAPNETGateway target; the daemon would crash-loop")
	}
	// Every target for an ENABLED mode must survive — the gate is narrow, not a
	// general opt-out. The fixture enables DMR, YSF, NXDN and POCSAG, so blocking
	// DAPNET must still leave the modem host and the other three gateways.
	for _, want := range []string{unitMMDVM, unitDMRGateway, unitYSFGateway, unitNXDNGateway} {
		if !slices.Contains(restartUnitsOf(m, paths), want) {
			t.Errorf("blocking DAPNET dropped %q too: %v", want, restartUnitsOf(m, paths))
		}
	}

	m.POCSAG.AuthKey = "abc123"
	if !hasDAPNET(m) {
		t.Error("POCSAG enabled with an AuthKey did not render a DAPNETGateway target")
	}

	// And the mode enable is still the outer gate, key or not.
	m.Modes.POCSAG = false
	if hasDAPNET(m) {
		t.Error("POCSAG disabled still rendered a DAPNETGateway target")
	}
}

// The apply path must stop and boot-disable a blocked unit. Without this, a daemon
// already looping from an earlier apply that had the key set would keep looping
// (nothing in the target set stops it), and a reboot would start it again only to
// have it exit.
func TestBlockedGatewayIsStoppedAndBootDisabled(t *testing.T) {
	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""

	if got := m.BlockedGatewayUnits(); !slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BlockedGatewayUnits() = %v, want it to name %q", got, unitDAPNETGateway)
	}
	paths := testPaths()
	if got := m.BootDisableUnits(paths); !slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v, want it to name %q", got, unitDAPNETGateway)
	}

	m.POCSAG.AuthKey = "abc123"
	if got := m.BlockedGatewayUnits(); len(got) != 0 {
		t.Errorf("BlockedGatewayUnits() = %v with the key set, want empty", got)
	}
	if got := m.BootDisableUnits(paths); slices.Contains(got, unitDAPNETGateway) {
		t.Errorf("BootDisableUnits() = %v still disables DAPNET with the key set", got)
	}
	// Boot-disabling must not have displaced the existing entries it appends to.
	m.Buses = []Bus{{ID: "b1", Name: "b1", Enabled: false}}
	if got := m.BootDisableUnits(paths); len(got) == 0 {
		t.Error("BootDisableUnits() dropped the disabled-bus units")
	}
}

// The view tells the operator which control to fill in — otherwise the only
// evidence is a daemon that is not running for no visible reason. It carries field
// NAMES only, so reporting that a secret is unset never exposes a secret.
func TestViewSurfacesBlockedGateways(t *testing.T) {
	m := fixture()
	m.Modes.POCSAG, m.POCSAG.AuthKey = true, ""
	v := m.View(Sources{})
	if len(v.BlockedGateways) != 1 || v.BlockedGateways[0].Mode != ModePOCSAG {
		t.Fatalf("View().BlockedGateways = %+v, want one POCSAG entry", v.BlockedGateways)
	}
	if v.POCSAG.HasAuthKey {
		t.Error("has_auth_key true with a blank key")
	}
	for _, f := range v.BlockedGateways[0].Missing {
		if strings.Contains(f, "TOPSECRET") || f == "" {
			t.Errorf("Missing carried something other than a field name: %q", f)
		}
	}

	m.POCSAG.AuthKey = "abc123"
	if got := m.View(Sources{}).BlockedGateways; len(got) != 0 {
		t.Errorf("View().BlockedGateways = %+v with the key set, want empty", got)
	}
}

// --- the boot picture is derived, not listed (the waypoint-mmdvm regression) ---

// TestBootPictureMatchesRenderSet is the invariant the whole mechanism exists for:
// what apply starts and what systemd starts at boot are the same set. Before this,
// they were two hand-maintained lists and they disagreed — waypoint-mmdvm.service
// was in neither, so the modem host died at every reboot.
func TestBootPictureMatchesRenderSet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*Model)
	}{
		{"fixture as-is", func(*Model) {}},
		{"every mode on", func(m *Model) {
			m.Modes = Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, M17: true, POCSAG: true, FM: true}
			m.POCSAG.AuthKey = "k"
		}},
		{"every mode off", func(m *Model) { m.Modes = Modes{} }},
		{"only FM", func(m *Model) { m.Modes = Modes{FM: true} }},
		{"POCSAG blocked", func(m *Model) { m.Modes = Modes{POCSAG: true}; m.POCSAG.AuthKey = "" }},
		{"DG-ID swap", func(m *Model) { m.Modes = Modes{YSF: true}; m.YSFGW.EnableDGId = true }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			tc.mutate(m)
			paths := testPaths()

			rendered := restartUnitsOf(m, paths)
			boot := m.BootEnableUnits(paths)
			if !slices.Equal(rendered, boot) {
				t.Fatalf("boot set diverged from the render set:\n  render = %v\n  boot   = %v", rendered, boot)
			}
			// And the disable set must be the exact complement: every managed unit is
			// accounted for exactly once, so none keeps a stale boot state.
			disabled := m.BootDisableUnits(paths)
			for _, u := range ManagedUnits() {
				inBoot, inDisabled := slices.Contains(boot, u), slices.Contains(disabled, u)
				if inBoot == inDisabled {
					t.Errorf("%q is %s — every managed unit must be enabled xor disabled",
						u, map[bool]string{true: "in BOTH sets", false: "in NEITHER set"}[inBoot])
				}
			}
		})
	}
}

// TestModemHostIsBootEnabled is the #221-adjacent regression proper. The modem
// host is the one unit the stack updater's health gate always requires, and it was
// never boot-enabled — so a node that rebooted failed every subsequent stack
// update until someone applied config by hand.
func TestModemHostIsBootEnabled(t *testing.T) {
	m := fixture() // DMR/YSF/NXDN/POCSAG on, modem port and identity set
	paths := testPaths()

	if !m.ModemHostRuns() {
		t.Fatal("a configured node with modes enabled must run MMDVM-Host")
	}
	if got := m.BootEnableUnits(paths); !slices.Contains(got, unitMMDVM) {
		t.Fatalf("waypoint-mmdvm.service must be enabled for boot; got %v", got)
	}
	if got := m.BootDisableUnits(paths); slices.Contains(got, unitMMDVM) {
		t.Errorf("waypoint-mmdvm.service must not be boot-disabled on a configured node; got %v", got)
	}
}

// The inverse half of the same defect: YSF and NXDN were boot-enabled
// unconditionally, so a node started two daemons whose modes were switched off.
func TestDisabledModeIsNotBootEnabled(t *testing.T) {
	m := fixture()
	m.Modes = Modes{DMR: true} // YSF and NXDN explicitly off
	paths := testPaths()

	boot := m.BootEnableUnits(paths)
	for _, off := range []string{unitYSFGateway, unitDGIdGateway, unitNXDNGateway, unitP25Gateway, unitM17Gateway, unitDStarGateway, unitDAPNETGateway} {
		if slices.Contains(boot, off) {
			t.Errorf("%q is boot-enabled with its mode off; got %v", off, boot)
		}
		if !slices.Contains(m.BootDisableUnits(paths), off) {
			t.Errorf("%q must be boot-DISABLED with its mode off", off)
		}
	}
	// The one enabled mode, and the modem host it needs, still come up.
	for _, on := range []string{unitMMDVM, unitDMRGateway} {
		if !slices.Contains(boot, on) {
			t.Errorf("%q must be boot-enabled; got %v", on, boot)
		}
	}
}

// A node with nothing switched on runs nothing — including the modem host. This is
// the "unclaimed node" the image ships, and it must not be given a crash-looping
// MMDVM-Host just because the unit exists.
func TestNoModesRunsNothing(t *testing.T) {
	m := fixture()
	m.Modes = Modes{}
	paths := testPaths()

	if got := m.BootEnableUnits(paths); len(got) != 0 {
		t.Errorf("a node with every mode off must boot-enable nothing; got %v", got)
	}
	if got := len(m.BootDisableUnits(paths)); got != len(ManagedUnits()) {
		t.Errorf("every managed unit should be boot-disabled; got %d of %d", got, len(ManagedUnits()))
	}
}

// A displacing bus must keep the stock gateway out of the boot set, or a reboot
// races it for the mode's loopback — the original reason this code exists.
func TestDisplacedGatewayStaysOutOfTheBootSet(t *testing.T) {
	m := fixture()
	m.Modes = Modes{YSF: true}
	m.Buses = []Bus{{ID: "b1", Name: "b1", Enabled: true}}
	m.Attachments = []Attachment{{BusID: "b1", Mode: ModeYSF}}
	paths := testPaths()

	if !m.modeDisplacesGateway(ModeYSF) {
		t.Skip("fixture did not produce a displacing YSF bus")
	}
	if got := m.BootEnableUnits(paths); slices.Contains(got, unitYSFGateway) {
		t.Errorf("a displaced YSF gateway must not be boot-enabled; got %v", got)
	}
	if got := m.BootDisableUnits(paths); !slices.Contains(got, unitYSFGateway) {
		t.Errorf("a displaced YSF gateway must be boot-disabled; got %v", got)
	}
}

// YSFGateway's RF frequency requirement (#145 / #215). CWiresX::setInfo asserts on
// WiresX.cpp:103 and :104, so EITHER frequency alone aborts the daemon — the
// half-set cases are the ones a transmit-only reading of the issues would miss.
//
// The values come from measurements against the pinned binary, recorded at
// frequencyStops and executed by test/tier2's TestTier2_UnsetFrequencyDaemonSurvey.
// What is asserted here is that this package draws the line in the same place.
func TestUnmetGatewayRequirementsYSFFrequency(t *testing.T) {
	ysf := func(m *Model) *GatewayRequirement {
		for i, r := range m.UnmetGatewayRequirements() {
			if r.Mode == ModeYSF {
				return &m.UnmetGatewayRequirements()[i]
			}
		}
		return nil
	}

	for _, tc := range []struct {
		name        string
		rx, tx      string
		wantMissing []string // nil ⇒ the gateway must NOT be withheld
	}{
		{"neither set", "", "", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{"receive unset", "", "438800000", []string{"modem.rx_freq_hz"}},
		{"transmit unset", "438800000", "", []string{"modem.tx_freq_hz"}},
		{"whitespace", "  ", "  ", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{"literal zero", "0", "0", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{"padded zero", "00", "00", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{"not a number", "abc", "abc", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},
		{"hex, which atoi reads as 0", "0x10", "0x10", []string{"modem.rx_freq_hz", "modem.tx_freq_hz"}},

		// The clean cases, and they are the ones that matter most: a rule that
		// fires on a working node is worse than no rule. Everything below produces
		// a non-zero atoi, which is the whole of what the daemon checks.
		{"both set", "438800000", "438800000", nil},
		{"megahertz typed into a hertz field", "438", "438", nil},
		{"a decimal point, which atoi truncates", "438.8", "438.8", nil},
		{"trailing rubbish after the digits", "438800000abc", "438800000abc", nil},
		{"leading whitespace", " 438800000", " 438800000", nil},
		{"explicitly signed", "+438800000", "+438800000", nil},
		// Negative is the uncomfortable one: atoi yields -1, the daemon stores it
		// in an unsigned and the assert passes on a garbage frequency. It starts,
		// so it is not withheld. mode_readiness reports it.
		{"negative", "-1", "-1", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := fixture()
			m.Modes.YSF, m.YSFGW.EnableDGId = true, false
			m.Modem.RXFreqHz, m.Modem.TXFreqHz = tc.rx, tc.tx

			got := ysf(m)
			if tc.wantMissing == nil {
				if got != nil {
					t.Fatalf("YSFGateway withheld from a node it would start on: %+v", got)
				}
				if !m.GatewayRuns(ModeYSF) {
					t.Error("GatewayRuns(ysf) is false for a configuration the daemon accepts")
				}
				return
			}
			if got == nil {
				t.Fatal("YSFGateway not withheld, so this configuration crash-loops")
			}
			if got.Unit != unitYSFGateway {
				t.Errorf("Unit = %q, want %q", got.Unit, unitYSFGateway)
			}
			if !slices.Equal(got.Missing, tc.wantMissing) {
				t.Errorf("Missing = %v, want %v", got.Missing, tc.wantMissing)
			}
			if m.GatewayRuns(ModeYSF) {
				t.Error("GatewayRuns(ysf) is true for a configuration that aborts the daemon")
			}
		})
	}

	// DGIdGateway has no Wires-X and starts with no frequency at all (measured in
	// Tier 2), so the DG-ID slot lifts the block. This is the same swap that moves
	// the render target, which is why it is expressed as one condition and not two.
	t.Run("the DG-ID slot is exempt", func(t *testing.T) {
		m := fixture()
		m.Modes.YSF, m.YSFGW.EnableDGId = true, true
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""
		if got := ysf(m); got != nil {
			t.Fatalf("DGIdGateway withheld for a frequency it does not read: %+v", got)
		}
		if !m.GatewayRuns(ModeYSF) {
			t.Error("the DG-ID gateway must run without frequencies")
		}
	})

	// A mode that is off is withholding nothing — the same rule the rest of the
	// registry follows, restated here because YSF's block keys on two sections.
	t.Run("YSF off reports nothing", func(t *testing.T) {
		m := fixture()
		m.Modes.YSF = false
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""
		if got := ysf(m); got != nil {
			t.Fatalf("a disabled mode was reported: %+v", got)
		}
	})
}

// The apply-path consequences for YSF, mirroring the DAPNET case: no render
// target, stopped, and out of the boot set — with the other enabled modes left
// alone, because a withheld YSF gateway is not an excuse to take a node off air.
func TestBlockedYSFGatewayIsDroppedStoppedAndBootDisabled(t *testing.T) {
	paths := testPaths()
	m := fixture()
	m.Modes.YSF, m.YSFGW.EnableDGId = true, false
	m.Modem.RXFreqHz, m.Modem.TXFreqHz = "", ""

	if slices.Contains(restartUnitsOf(m, paths), unitYSFGateway) {
		t.Error("a YSF gateway that cannot start still rendered a target")
	}
	if got := m.BlockedGatewayUnits(); !slices.Contains(got, unitYSFGateway) {
		t.Errorf("BlockedGatewayUnits() = %v, want it to name %q", got, unitYSFGateway)
	}
	if got := m.BootDisableUnits(paths); !slices.Contains(got, unitYSFGateway) {
		t.Errorf("BootDisableUnits() = %v, want it to name %q", got, unitYSFGateway)
	}
	if got := m.BootEnableUnits(paths); slices.Contains(got, unitYSFGateway) {
		t.Errorf("BootEnableUnits() = %v still enables a withheld gateway", got)
	}

	// With the frequencies set it comes back, which is what makes the above a
	// statement about the frequencies rather than about the fixture.
	m.Modem.RXFreqHz, m.Modem.TXFreqHz = "438800000", "438800000"
	if !slices.Contains(restartUnitsOf(m, paths), unitYSFGateway) {
		t.Errorf("the YSF gateway did not come back with frequencies set: %v", restartUnitsOf(m, paths))
	}
}

// The invariant that replaces a renderer-level error: if YSFGateway.ini is
// rendered at all, the frequencies in it are ones the daemon starts on.
//
// Issue #145 proposed making the renderer refuse instead. That was not done, and
// this is why it did not need to be: renderers are pure func(*Model) string and
// that purity is load-bearing (RenderTarget holds one, WriteFiles calls them
// blind), so an error return would change every call site to catch a case the
// registry makes unreachable — a withheld gateway is never rendered at all.
//
// What that argument needs is a test, or the two mechanisms drift and a future
// change to one of them reopens the bug quietly. This is that test: it walks the
// same frequency values the registry table uses, and asserts the implication
// directly rather than trusting the two to stay in step.
// It deliberately does NOT call frequencyStops to judge the rendered value. Doing
// so would make the test self-referential — a frequencyStops that returned false
// for everything would satisfy both the gate and the assertion — so the fatal
// values are listed literally, exactly as the pinned YSFGateway was measured to
// abort on them.
func TestRenderedYSFConfigNeverCarriesAStoppingFrequency(t *testing.T) {
	// Measured against YSFClients @ 2b480aa: each of these reaches CWiresX::setInfo
	// as 0 and trips the assert on WiresX.cpp:103/:104.
	fatal := []string{"", " ", "  ", "0", "00", "abc", "0x10"}

	paths := testPaths()
	var everRendered, everWithheld bool
	for _, v := range []string{"", "  ", "0", "00", "abc", "0x10", "438", "438.8", "438800000", "438800000abc", " 438800000", "+438800000", "-1"} {
		m := fixture()
		m.Modes.YSF, m.YSFGW.EnableDGId = true, false
		m.Modem.RXFreqHz, m.Modem.TXFreqHz = v, v

		var rendered bool
		for _, tgt := range m.RenderTargets(paths) {
			if tgt.Unit == unitYSFGateway {
				rendered = true
			}
		}
		if !rendered {
			everWithheld = true
			continue // withheld, which is the other half of the invariant
		}
		everRendered = true

		ini := m.RenderYSFGateway()
		for _, key := range []string{"RXFrequency", "TXFrequency"} {
			if got := iniValue(t, ini, key); slices.Contains(fatal, got) {
				t.Errorf("YSFGateway.ini was rendered with %s=%q, which aborts the daemon", key, got)
			}
		}
	}
	// Both branches must have been taken, or the loop proved nothing: all-withheld
	// would make the assertion unreachable, all-rendered would mean the gate never
	// fired.
	if !everRendered || !everWithheld {
		t.Errorf("the table exercised only one branch (rendered=%v withheld=%v)", everRendered, everWithheld)
	}
}

// iniValue pulls one key's value out of a rendered INI. Deliberately dumb: it is
// checking what a daemon's line-oriented parser sees, not re-implementing one.
func iniValue(t *testing.T, ini, key string) string {
	t.Helper()
	for _, line := range strings.Split(ini, "\n") {
		if name, val, ok := strings.Cut(line, "="); ok && strings.TrimSpace(name) == key {
			return val
		}
	}
	t.Fatalf("rendered config carries no %s key", key)
	return ""
}
