package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

// iniKey pulls "Key=Value" out of a named section of a rendered INI, so a test
// asserts the key the daemon's parser will actually see rather than a substring
// that could match in a neighbouring block.
func iniKey(t *testing.T, text, section, key string) (string, bool) {
	t.Helper()
	inSection := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inSection = strings.EqualFold(line, "["+section+"]")
			continue
		}
		if !inSection {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}

func mustKey(t *testing.T, text, section, key string) string {
	t.Helper()
	v, ok := iniKey(t, text, section, key)
	if !ok {
		t.Fatalf("[%s] %s missing from render:\n%s", section, key, text)
	}
	return v
}

// TestRenderMQTTKeysMatchPinnedSources is the ground-truth test for D2: every
// [MQTT] and [Log] key Waypoint emits must be one the PINNED daemon actually
// parses, spelled the way that daemon spells it. The expectations below are
// transcribed from the sources named in pins.env — see system.go for the file and
// line of each. The two drifts this pins down are the ones a sample INI would have
// hidden: DStarGateway takes Address/Authenticate where everything else takes
// Host/Auth, and M17Gateway is pre-MQTT so it must get NO [MQTT] section at all.
func TestRenderMQTTKeysMatchPinnedSources(t *testing.T) {
	m := &Model{}
	m.Modes.POCSAG = true
	m.MQTT = MQTT{Host: "10.1.2.3", Port: "1884", Name: "node7"}

	cases := []struct {
		name     string
		render   func() string
		hostKey  string // "Host" or "Address"; "" ⇒ expect no [MQTT] section
		authKey  string // "Auth" or "Authenticate"
		mqttName string
	}{
		{"mmdvm", m.RenderMMDVM, "Host", "Auth", "node7"},
		{"dmrgateway", m.RenderDMRGateway, "Address", "Auth", "dmr-gateway"},
		{"ysfgateway", m.RenderYSFGateway, "Address", "Auth", "ysf-gateway"},
		{"dgidgateway", m.RenderDGIdGateway, "Address", "Auth", "dgid-gateway"},
		{"p25gateway", m.RenderP25Gateway, "Address", "Auth", "p25-gateway"},
		{"nxdngateway", m.RenderNXDNGateway, "Address", "Auth", "nxdn-gateway"},
		{"dstargateway", m.RenderDStarGateway, "Address", "Authenticate", "dstar-gateway"},
		{"dapnetgateway", m.RenderDAPNETGateway, "Address", "Auth", "dapnet-gateway"},
		{"m17gateway", m.RenderM17Gateway, "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.render()
			if tc.hostKey == "" {
				// Pre-MQTT: the pinned M17Gateway has no libmosquitto, so an [MQTT]
				// section would be a block it cannot read.
				if _, ok := iniKey(t, out, "MQTT", "Address"); ok {
					t.Errorf("M17Gateway rendered an [MQTT] section; the pinned build is pre-MQTT")
				}
				if strings.Contains(out, "[MQTT]") {
					t.Errorf("M17Gateway rendered an [MQTT] header:\n%s", out)
				}
				// It logs to a file instead. FileLevel is the key its Conf.cpp parses.
				if got := mustKey(t, out, "Log", "DisplayLevel"); got != "1" {
					t.Errorf("m17 DisplayLevel = %q, want 1", got)
				}
				if got := mustKey(t, out, "Log", "FileLevel"); got != "0" {
					t.Errorf("m17 FileLevel = %q, want 0", got)
				}
				if _, ok := iniKey(t, out, "Log", "MQTTLevel"); ok {
					t.Errorf("m17 rendered MQTTLevel; the pinned build has no MQTT sink")
				}
				return
			}
			if got := mustKey(t, out, "MQTT", tc.hostKey); got != "10.1.2.3" {
				t.Errorf("[MQTT] %s = %q, want the store host", tc.hostKey, got)
			}
			if got := mustKey(t, out, "MQTT", "Port"); got != "1884" {
				t.Errorf("[MQTT] Port = %q, want the store port", got)
			}
			if got := mustKey(t, out, "MQTT", tc.authKey); got != "0" {
				t.Errorf("[MQTT] %s = %q, want 0", tc.authKey, got)
			}
			if got := mustKey(t, out, "MQTT", "Name"); got != tc.mqttName {
				t.Errorf("[MQTT] Name = %q, want %q", got, tc.mqttName)
			}
			// The spelling this daemon does NOT use must be absent, or the daemon
			// would silently fall back to its compiled default and connect nowhere.
			other := map[string]string{"Host": "Address", "Address": "Host"}[tc.hostKey]
			if _, ok := iniKey(t, out, "MQTT", other); ok {
				t.Errorf("[MQTT] carries %s as well as %s; only one is parsed", other, tc.hostKey)
			}
			otherAuth := map[string]string{"Auth": "Authenticate", "Authenticate": "Auth"}[tc.authKey]
			if _, ok := iniKey(t, out, "MQTT", otherAuth); ok {
				t.Errorf("[MQTT] carries %s as well as %s", otherAuth, tc.authKey)
			}
			// [Log] on every MQTT-era daemon: both keys, defaulted.
			if got := mustKey(t, out, "Log", "MQTTLevel"); got != "1" {
				t.Errorf("MQTTLevel = %q, want the default 1", got)
			}
			if got := mustKey(t, out, "Log", "DisplayLevel"); got != "0" {
				t.Errorf("DisplayLevel = %q, want the default 0", got)
			}
		})
	}
}

// TestRenderLogLevelsFromStore proves each daemon reads its OWN levels: setting
// one daemon's pair must not move another's. This is the RFC-0001 isolation
// property applied to the new section — a shared helper makes cross-wiring easy.
func TestRenderLogLevelsFromStore(t *testing.T) {
	m := &Model{}
	m.Logging.MMDVM = LogLevels{Display: "5", MQTT: "4"}
	m.Logging.DStarGateway = LogLevels{Display: "6", MQTT: "3"}
	m.Logging.M17Gateway = FileLogLevels{Display: "2", File: "6"}

	if got := mustKey(t, m.RenderMMDVM(), "Log", "DisplayLevel"); got != "5" {
		t.Errorf("mmdvm DisplayLevel = %q, want 5", got)
	}
	if got := mustKey(t, m.RenderMMDVM(), "Log", "MQTTLevel"); got != "4" {
		t.Errorf("mmdvm MQTTLevel = %q, want 4", got)
	}
	if got := mustKey(t, m.RenderDStarGateway(), "Log", "DisplayLevel"); got != "6" {
		t.Errorf("dstar DisplayLevel = %q, want 6", got)
	}
	if got := mustKey(t, m.RenderM17Gateway(), "Log", "FileLevel"); got != "6" {
		t.Errorf("m17 FileLevel = %q, want 6", got)
	}
	// Untouched daemons keep the compiled defaults.
	if got := mustKey(t, m.RenderP25Gateway(), "Log", "MQTTLevel"); got != "1" {
		t.Errorf("p25 MQTTLevel = %q, want the untouched default 1", got)
	}
	if got := mustKey(t, m.RenderP25Gateway(), "Log", "DisplayLevel"); got != "0" {
		t.Errorf("p25 DisplayLevel = %q, want the untouched default 0", got)
	}
}

// TestRenderMQTTCredentialsOnlyWhenAuth pins the rule that keeps the broker
// password out of seven world-readable-to-root files for no benefit: the daemons
// send credentials only when authentication is on, so the renderer emits them
// only then.
func TestRenderMQTTCredentialsOnlyWhenAuth(t *testing.T) {
	m := &Model{}
	m.MQTT = MQTT{Username: "wp", Password: "s3cret"}
	out := m.RenderMMDVM()
	if strings.Contains(out, "s3cret") {
		t.Errorf("auth off but the password was rendered:\n%s", out)
	}
	if _, ok := iniKey(t, out, "MQTT", "Username"); ok {
		t.Errorf("auth off but Username was rendered")
	}

	m.MQTT.Auth = true
	out = m.RenderMMDVM()
	if got := mustKey(t, out, "MQTT", "Auth"); got != "1" {
		t.Errorf("Auth = %q, want 1", got)
	}
	if got := mustKey(t, out, "MQTT", "Username"); got != "wp" {
		t.Errorf("Username = %q, want wp", got)
	}
	if got := mustKey(t, out, "MQTT", "Password"); got != "s3cret" {
		t.Errorf("Password = %q, want the stored secret", got)
	}
}

// TestBusPrefixRerendersBusConfigs is D5: the bus topic prefix is one setting, so
// changing it must move the bus configs too. Before the store owned it, the
// gateway INIs would follow the edit while the bus configs kept the value
// waypointd was started with — a node half on each prefix.
func TestBusPrefixRerendersBusConfigs(t *testing.T) {
	m := &Model{
		Buses:       []Bus{{ID: "hub", Name: "Hub", Enabled: true}},
		Attachments: []Attachment{{BusID: "hub", Mode: ModeDMR, Slot: "2"}, {BusID: "hub", Mode: ModeYSF}},
	}
	dir := t.TempDir()
	paths := Paths{BusConfigDir: dir, MQTTBroker: "127.0.0.1:1883", BusTopicPrefix: DefaultBusTopicPrefix}

	before := renderBus(t, m, paths)
	if before.MQTT == nil || before.MQTT.Prefix != DefaultBusTopicPrefix {
		t.Fatalf("baseline prefix = %+v, want %s", before.MQTT, DefaultBusTopicPrefix)
	}

	// The operator renames the bus root in the System tab.
	m.MQTT.BusPrefix = "site7/bus"
	after := renderBus(t, m, paths)
	if after.MQTT == nil || after.MQTT.Prefix != "site7/bus" {
		t.Fatalf("prefix after the edit = %+v, want site7/bus", after.MQTT)
	}

	// The broker follows the store the same way.
	m.MQTT.Host, m.MQTT.Port = "10.9.9.9", "1885"
	if got := renderBus(t, m, paths); got.MQTT.Broker != "10.9.9.9:1885" {
		t.Errorf("broker = %q, want the store's 10.9.9.9:1885", got.MQTT.Broker)
	}

	// A node with no broker at all (demo, and renderer tests) still emits no block.
	if got := renderBus(t, m, Paths{BusConfigDir: dir}); got.MQTT != nil {
		t.Errorf("demo render carried an MQTT block: %+v", got.MQTT)
	}
}

func renderBus(t *testing.T, m *Model, paths Paths) BusConfig {
	t.Helper()
	var found *RenderTarget
	for _, tg := range m.RenderTargets(paths) {
		if filepath.Base(tg.Path) == busConfigFile("hub") {
			target := tg
			found = &target
			break
		}
	}
	if found == nil {
		t.Fatal("no render target for bus hub")
	}
	var bc BusConfig
	if err := json.Unmarshal([]byte(found.Render(m)), &bc); err != nil {
		t.Fatalf("bus config did not parse: %v", err)
	}
	return bc
}

// --- save-time validation -------------------------------------------------

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSetMQTTRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown field", `{"host":"127.0.0.1","broker":"nope"}`},
		{"port not a number", `{"port":"eighteen-eighty-three"}`},
		{"port out of range", `{"port":"70000"}`},
		{"port zero", `{"port":"0"}`},
		{"wildcard in name", `{"name":"mmdvm/#"}`},
		{"wildcard in status prefix", `{"status_prefix":"waypoint/+/status"}`},
		{"trailing slash on bus prefix", `{"bus_prefix":"waypoint/bus/"}`},
		{"whitespace in host", `{"host":"127.0.0.1 :1883"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := SetMQTT(s, []byte(tc.body), "test"); err == nil {
				t.Fatalf("SetMQTT accepted %s", tc.body)
			}
			// A refused save must leave the row untouched, not half-written.
			var got MQTT
			if _, err := s.GetInto("mqtt", &got); err != nil {
				t.Fatalf("GetInto: %v", err)
			}
			if got != (MQTT{}) {
				t.Errorf("refused save still wrote the row: %+v", got)
			}
		})
	}
}

// TestSetMQTTBlankPasswordKeepsStored is D4's write half: the View never returns
// the password, so the UI has nothing to send back, and a blank field must mean
// "keep it" rather than "clear it".
func TestSetMQTTBlankPasswordKeepsStored(t *testing.T) {
	s := newTestStore(t)
	if err := SetMQTT(s, []byte(`{"auth":true,"username":"wp","password":"s3cret"}`), "test"); err != nil {
		t.Fatalf("initial save: %v", err)
	}
	// A later save of an unrelated field, with the password field blank.
	if err := SetMQTT(s, []byte(`{"host":"10.0.0.2","password":""}`), "test"); err != nil {
		t.Fatalf("second save: %v", err)
	}
	var got MQTT
	if _, err := s.GetInto("mqtt", &got); err != nil {
		t.Fatalf("GetInto: %v", err)
	}
	if got.Password != "s3cret" {
		t.Errorf("password = %q, want the stored secret preserved", got.Password)
	}
	if got.Host != "10.0.0.2" {
		t.Errorf("host = %q, want the new value", got.Host)
	}
	if got.Username != "wp" {
		t.Errorf("username = %q, want the merge to keep it", got.Username)
	}
	// A non-blank password replaces.
	if err := SetMQTT(s, []byte(`{"password":"newer"}`), "test"); err != nil {
		t.Fatalf("third save: %v", err)
	}
	if _, err := s.GetInto("mqtt", &got); err != nil {
		t.Fatalf("GetInto: %v", err)
	}
	if got.Password != "newer" {
		t.Errorf("password = %q, want the replacement", got.Password)
	}
}

// TestViewNeverReturnsBrokerPassword is D4's read half, asserted against the
// SERIALIZED response rather than the struct: the guarantee is about the bytes
// that reach the browser, so a field added later without a json:"-" tag fails
// here rather than in production.
func TestViewNeverReturnsBrokerPassword(t *testing.T) {
	m := &Model{}
	m.MQTT = MQTT{Auth: true, Username: "wp", Password: "s3cret-do-not-leak", Host: "10.0.0.2"}
	raw, err := json.Marshal(m.View(Sources{Store: "/tmp/config.db", Listen: "127.0.0.1:8073"}))
	if err != nil {
		t.Fatalf("marshal view: %v", err)
	}
	if strings.Contains(string(raw), "s3cret-do-not-leak") {
		t.Fatalf("GET /api/config leaked the broker password:\n%s", raw)
	}
	v := m.View(Sources{Store: "/tmp/config.db", Listen: "127.0.0.1:8073"})
	if !v.MQTT.HasPassword {
		t.Errorf("has_password = false with a password stored")
	}
	if v.MQTT.Username != "wp" || v.MQTT.Host != "10.0.0.2" {
		t.Errorf("view dropped non-secret fields: %+v", v.MQTT)
	}
	if v.Sources.Listen != "127.0.0.1:8073" {
		t.Errorf("listen address = %q, want it surfaced read-only", v.Sources.Listen)
	}
	// An empty store still projects usable defaults, so the page is never blank.
	empty := (&Model{}).View(Sources{})
	if empty.MQTT.Host != DefaultMQTTHost || empty.MQTT.Port != DefaultMQTTPort {
		t.Errorf("empty store projected %+v, want the compiled defaults", empty.MQTT)
	}
	if empty.MQTT.HasPassword {
		t.Errorf("has_password = true with no password stored")
	}
	if empty.Logging.MMDVM.MQTT != "1" || empty.Logging.M17Gateway.File != "0" {
		t.Errorf("empty store projected %+v, want the render defaults", empty.Logging)
	}
}

func TestSetLoggingRejectsBadInput(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"unknown daemon", `{"telnetgateway":{"display":"1","mqtt":"1"}}`},
		{"unknown field", `{"mmdvm":{"display":"1","syslog":"1"}}`},
		{"level above the ladder", `{"mmdvm":{"display":"7"}}`},
		{"negative level", `{"mmdvm":{"mqtt":"-1"}}`},
		{"non-numeric level", `{"p25gateway":{"display":"debug"}}`},
		{"m17 has no mqtt sink", `{"m17gateway":{"mqtt":"1"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestStore(t)
			if err := SetLogging(s, []byte(tc.body), "test"); err == nil {
				t.Fatalf("SetLogging accepted %s", tc.body)
			}
		})
	}
	// The whole ladder, including the boundaries, is accepted.
	for _, lvl := range []string{"0", "1", "6", ""} {
		s := newTestStore(t)
		body := `{"mmdvm":{"display":"` + lvl + `","mqtt":"` + lvl + `"}}`
		if err := SetLogging(s, []byte(body), "test"); err != nil {
			t.Errorf("SetLogging rejected level %q: %v", lvl, err)
		}
	}
}

// TestMQTTSectionRoundTrips is the RFC-0001 store contract for the two new
// sections: what is saved is what loads back, including a disabled-but-populated
// auth block (disable preserves data).
func TestMQTTSectionRoundTrips(t *testing.T) {
	s := newTestStore(t)
	want := &Model{}
	want.MQTT = MQTT{Host: "10.0.0.2", Port: "1884", Auth: false, Username: "wp", Password: "kept", Name: "node7", StatusPrefix: "site/status", BusPrefix: "site/bus"}
	want.Logging.MMDVM = LogLevels{Display: "3", MQTT: "2"}
	want.Logging.M17Gateway = FileLogLevels{Display: "4", File: "5"}
	if err := want.Save(s, "test"); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(s)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.MQTT != want.MQTT {
		t.Errorf("mqtt round-trip: got %+v want %+v", got.MQTT, want.MQTT)
	}
	if got.Logging != want.Logging {
		t.Errorf("logging round-trip: got %+v want %+v", got.Logging, want.Logging)
	}
	// Auth off must not have dropped the credentials — re-enabling restores them.
	if got.MQTT.Username != "wp" || got.MQTT.Password != "kept" {
		t.Errorf("disabling auth lost the credentials: %+v", got.MQTT)
	}
}
