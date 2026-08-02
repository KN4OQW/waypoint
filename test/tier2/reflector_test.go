//go:build tier2

package tier2

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KN4OQW/waypoint/internal/config"
)

// Live-reflector link-state checks — issue #17's last Tier 2 item. Unlike every
// other test here they leave the loopback and touch a third-party service, so
// they are opt-in via WAYPOINT_TIER2_NET=1 and never run by default.
//
// A reflector fans traffic out to other stations and does not echo the
// originator, so there is no round-trip to observe: the checkable claim is that
// a gateway configured entirely by our renderers reaches a real reflector and
// the reflector answers. That is a link-state check, and it is the *last* thing
// the loopback tests cannot cover — everything below the link is already proven.
//
// No audio is ever transmitted. These link, observe the link state, and unlink.
const (
	// A US reflector that exists to be tested against, so a node appearing and
	// leaving is exactly what it is for.
	//
	// The name carries the country prefix the daemon builds when it loads the
	// hostlist ("US-" + name, or "XX-" + name when use_xx_prefix), because
	// findByName matches against that constructed form. The settings picker
	// stores the bare name instead, which is why no picked startup reflector
	// links today — see #146.
	ysfLinkTarget     = "US-UTAHDRN-TEST"
	ysfLinkTargetName = "UTAHDRN-TEST" // as the hostlist and the picker carry it

	// The FCS side of the same claim: DGIdGateway resolves an FCS room by name
	// (fcs002.xreflector.net for FCS00290) with no hostlist involved, and prints
	// it back in the FCS002-90 form.
	fcsLinkTarget = "FCS00290"
	fcsLinkPrint  = "FCS002-90"

	// The shipped hostlist, as used by the device.
	seedHostsPath = "../../internal/hostsrc/seed/YSFHosts.json"
)

// requireLiveNetwork gates the tests that leave the machine.
func requireLiveNetwork(t *testing.T) {
	t.Helper()
	if os.Getenv("WAYPOINT_TIER2_NET") != "1" {
		t.Skip("live-reflector test: set WAYPOINT_TIER2_NET=1 to link to a real reflector")
	}
}

// localHostlist rewrites the managed hostlist path in a rendered config to a copy
// of the shipped seed file. That absolute path (/var/lib/waypoint/etc) only
// exists on a device; the file's CONTENT is the same one that ships, and nothing
// else in the rendered config is touched — the routing and link behaviour under
// test are unaffected.
func localHostlist(t *testing.T, ini string) string {
	t.Helper()
	seed, err := os.ReadFile(seedHostsPath)
	if err != nil {
		t.Fatalf("read shipped hostlist: %v", err)
	}
	local := filepath.Join(t.TempDir(), "YSFHosts.json")
	if err := os.WriteFile(local, seed, 0o600); err != nil {
		t.Fatal(err)
	}
	return regexp.MustCompile(`(?m)^Hosts=.*$`).ReplaceAllString(ini, "Hosts="+local)
}

// mqttTail collects everything a daemon logs. The rendered configs set
// DisplayLevel=0 / MQTTLevel=1 — that is the point of the MQTT-native pipeline —
// so link messages go to <Name>/log on the broker, not to stdout.
type mqttTail struct {
	client mqtt.Client
	mu     sync.Mutex
	lines  []string
}

func newMQTTTail(t *testing.T, name string) *mqttTail {
	t.Helper()
	tail := &mqttTail{}
	co := mqtt.NewClientOptions().
		AddBroker("tcp://127.0.0.1:1883").
		SetClientID("tier2-tail-" + name).
		SetCleanSession(true).
		SetKeepAlive(10 * time.Second)
	co.SetOnConnectHandler(func(c mqtt.Client) {
		c.Subscribe(name+"/log", 0, func(_ mqtt.Client, m mqtt.Message) {
			tail.mu.Lock()
			tail.lines = append(tail.lines, string(m.Payload()))
			tail.mu.Unlock()
		})
	})
	tail.client = mqtt.NewClient(co)
	if tok := tail.client.Connect(); tok.WaitTimeout(5*time.Second) && tok.Error() != nil {
		t.Skipf("no MQTT broker on 127.0.0.1:1883 (the daemons need one): %v", tok.Error())
	}
	t.Cleanup(func() { tail.client.Disconnect(250) })
	return tail
}

func (m *mqttTail) text() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return strings.Join(m.lines, "\n")
}

// waitFor polls the collected log for a fragment, returning as soon as it shows.
func (m *mqttTail) waitFor(fragment string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if strings.Contains(m.text(), fragment) {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

// startDaemon runs a pinned gateway and stops it POLITELY on cleanup: SIGTERM
// gives it the chance to send its unlink, so we leave a real reflector's node
// list the way a shutting-down node would, not by vanishing.
func startDaemon(t *testing.T, name, confPath string) *strings.Builder {
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
	t.Cleanup(func() {
		cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
		}
		if s := out.String(); strings.TrimSpace(s) != "" {
			t.Logf("--- %s stdout ---\n%s", name, s)
		}
	})
	return out
}

// TestTier2_YSFGatewayLinksReflector is issue #17 Tier 2's YSF reflector claim:
// a YSFGateway configured entirely by RenderYSFGateway, reading the hostlist we
// ship, must reach a real YSF reflector and get an answer.
//
// Reflectors fan out to other stations rather than echoing the originator, so
// this asserts on link state, not on returned audio. Nothing is transmitted.
func TestTier2_YSFGatewayLinksReflector(t *testing.T) {
	requireLiveNetwork(t)

	// Frequencies are not optional here: YSFGateway builds Wires-X unconditionally
	// and CWiresX::setInfo asserts txFrequency > 0, so a profile with no modem
	// frequencies aborts the daemon at startup (see #145).
	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		Modem:   config.Modem{RXFreqHz: "438800000", TXFreqHz: "438800000"},
		YSFGW: config.YSFGateway{
			Suffix:            "ND",
			Startup:           ysfLinkTarget,
			YSFNetwork:        true,
			InactivityTimeout: "30",
		},
	}
	confPath := filepath.Join(t.TempDir(), "YSFGateway.ini")
	if err := os.WriteFile(confPath, []byte(localHostlist(t, m.RenderYSFGateway())), 0o600); err != nil {
		t.Fatal(err)
	}

	tail := newMQTTTail(t, "ysf-gateway")
	startDaemon(t, "YSFGateway", confPath)

	// The gateway must find the reflector in the hostlist we ship before it can
	// link at all — an unknown name is reported instead of attempted.
	// The daemon logs the attempt as: Automatic (re-)connection to 13149 - "US-UTAHDRN-TEST "
	if !tail.waitFor(`Automatic (re-)connection to`, 20*time.Second) || !strings.Contains(tail.text(), ysfLinkTarget) {
		t.Fatalf("YSFGateway never attempted the startup reflector %s (unknown name in the shipped hostlist?)\n%s",
			ysfLinkTarget, tail.text())
	}
	t.Logf("PASS: %s resolved from the shipped hostlist and dialled", ysfLinkTarget)

	// And the reflector must answer: the link only reaches LINKED when a poll
	// comes back from the far end.
	if !tail.waitFor("Linked to "+ysfLinkTarget, 45*time.Second) {
		t.Fatalf("no link established to %s within 45s — the reflector never answered\n%s",
			ysfLinkTarget, tail.text())
	}
	t.Logf("PASS: linked to the live YSF reflector %s from a generated config", ysfLinkTarget)
}

// TestTier2_DGIdGatewayLinksFCSRoom is the same claim for the other rendered YSF
// path and the other protocol: the [DGId=N] static startup block RenderDGIdGateway
// emits for YCSNetwork, linking an FCS room. DGIdGateway resolves FCS rooms by
// name (fcs002.xreflector.net), so this exercises name resolution the hostlist is
// not involved in. Nothing is transmitted.
func TestTier2_DGIdGatewayLinksFCSRoom(t *testing.T) {
	requireLiveNetwork(t)

	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		YSFGW: config.YSFGateway{
			EnableDGId: true,
			Suffix:     "ND",
			Startup:    fcsLinkTarget,
			YCSNetwork: true,
		},
	}
	ini := m.RenderDGIdGateway()
	if !strings.Contains(ini, "Name="+fcsLinkTarget) {
		t.Fatalf("renderer did not emit the startup room as a DG-ID block:\n%s", ini)
	}
	confPath := filepath.Join(t.TempDir(), "DGIdGateway.ini")
	if err := os.WriteFile(confPath, []byte(localHostlist(t, ini)), 0o600); err != nil {
		t.Fatal(err)
	}

	tail := newMQTTTail(t, "dgid-gateway")
	startDaemon(t, "DGIdGateway", confPath)

	if !tail.waitFor("Added FCS:"+fcsLinkTarget, 20*time.Second) {
		t.Fatalf("DGIdGateway never took the generated FCS DG-ID block\n%s", tail.text())
	}
	t.Logf("PASS: the generated [DGId] startup block became an FCS network for %s", fcsLinkTarget)

	if !tail.waitFor("Linked to "+fcsLinkPrint, 45*time.Second) {
		t.Fatalf("no link established to %s within 45s — the room never answered\n%s",
			fcsLinkPrint, tail.text())
	}
	t.Logf("PASS: linked to the live FCS room %s from a generated config", fcsLinkPrint)
}
