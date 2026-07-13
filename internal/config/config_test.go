package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

func memStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fixture is a fully-populated model. Every field is non-empty so the
// round-trip cannot be masked by a rendered default filling an empty field.
func fixture() *Model {
	return &Model{
		General: General{Callsign: "KN4OQW", ID: "3180202", Duplex: true, Timeout: "240", RFModeHang: "300", NetModeHang: "300", Power: "1", Location: "Milton, EM60", URL: "https://waypoint.kn4oqw.com"},
		Modem:   Modem{Port: "/dev/ttyAMA0", UARTSpeed: "115200", RXFreqHz: "433900000", TXFreqHz: "438900000", RXOffset: "75", TXOffset: "-40", TXInvert: true, RXInvert: false, PTTInvert: false, RXLevel: "50", TXLevel: "50"},
		DMR:     DMR{ColorCode: "1", ID: "3180202", EmbeddedLCOnly: true, SelfOnly: false, DumpTAData: true},
		DMRNet:  DMRNet{LocalPort: "62032", GatewayAddress: "127.0.0.1", GatewayPort: "62031", Slot1: true, Slot2: true, Jitter: "360"},
		Modes:   Modes{DStar: false, DMR: true, YSF: true, P25: false, NXDN: false, M17: false, POCSAG: false, FM: false},
		Networks: []Network{
			{Name: "BM_3102_United_States", Address: "3102.master.brandmeister.network", Port: "62031", Password: "s3cr3t", Enabled: true, Rewrites: []string{"PCRewrite0=2,94000,2,4000,1001", "TGRewrite0=2,9,2,9,1"}},
			{Name: "TGIF_Network", Address: "tgif.network", Port: "62031", Password: "hunter2", Enabled: false, Rewrites: nil},
		},
		YSF:   YSF{LowDeviation: true, SelfOnly: false, TXHang: "6", RemoteGateway: false, ModeHang: "20"},
		YSFGW: YSFGateway{Suffix: "RPT", WiresXPassthrough: true, WiresXMakeUpper: true, Startup: "FCS00290", Reconnect: true, Revert: true, InactivityTimeout: "30", YSFNetwork: true, FCSNetwork: true, APRS: false},
		P25:   P25{NAC: "293", SelfOnly: true, OverrideUIDCheck: false, RemoteGateway: false, TXHang: "5"},
		P25GW: P25Gateway{Static: "10100,10200", Voice: true, RFHangTime: "120", NetHangTime: "60"},
	}
}

// Property 1 — Round-trip: render → parse → model with no semantic loss.
func TestLosslessRoundTrip(t *testing.T) {
	m := fixture()
	mm, err := ParseINI(strings.NewReader(m.RenderMMDVM()))
	if err != nil {
		t.Fatal(err)
	}
	dg, err := ParseINI(strings.NewReader(m.RenderDMRGateway()))
	if err != nil {
		t.Fatal(err)
	}
	yg, err := ParseINI(strings.NewReader(m.RenderYSFGateway()))
	if err != nil {
		t.Fatal(err)
	}
	pg, err := ParseINI(strings.NewReader(m.RenderP25Gateway()))
	if err != nil {
		t.Fatal(err)
	}
	got := fromINI(mm, dg, yg, pg)
	if !reflect.DeepEqual(m, got) {
		t.Fatalf("round-trip lost data:\n want %+v\n  got %+v", m, got)
	}
}

// Rendering is a pure function: same model ⇒ byte-identical output.
func TestRenderDeterministic(t *testing.T) {
	m := fixture()
	if m.RenderMMDVM() != m.RenderMMDVM() || m.RenderDMRGateway() != m.RenderDMRGateway() {
		t.Fatal("render is not deterministic")
	}
}

// Property 2 — Isolation: changing one section never alters another section's
// rendered output.
func TestIsolation(t *testing.T) {
	m := fixture()
	before := section(m.RenderMMDVM(), "Modem")

	m.General.Callsign = "W1AW" // change an unrelated section
	after := section(m.RenderMMDVM(), "Modem")

	if before != after {
		t.Fatalf("changing [General] altered [Modem]:\n before %q\n after %q", before, after)
	}
}

// Property 3 — Disable/re-enable: a disabled mode's settings survive unrelated
// changes and come back intact. Modelled at the store: toggling a mode off is a
// value flip, never a row delete.
func TestDisableReEnablePreservesSettings(t *testing.T) {
	s := memStore(t)
	m := fixture()
	m.DMR.ColorCode = "7" // a DMR-specific setting we must not lose
	if err := m.Save(s, "test"); err != nil {
		t.Fatal(err)
	}

	// Disable DMR, change something unrelated, save.
	m2, _ := Load(s)
	m2.Modes.DMR = false
	m2.General.Location = "elsewhere"
	if err := m2.Save(s, "test"); err != nil {
		t.Fatal(err)
	}

	// Re-enable DMR — its color code must still be 7.
	m3, _ := Load(s)
	m3.Modes.DMR = true
	if err := m3.Save(s, "test"); err != nil {
		t.Fatal(err)
	}
	final, _ := Load(s)
	if final.DMR.ColorCode != "7" {
		t.Fatalf("disabled mode's ColorCode was lost: got %q", final.DMR.ColorCode)
	}
}

// A partial section write merges — unspecified fields survive (the guarantee
// that lets the UI PUT only the fields it manages).
func TestSetSectionMergePreserves(t *testing.T) {
	s := memStore(t)
	if err := fixture().Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	known, err := SetSection(s, "general", []byte(`{"callsign":"W1AW"}`), "test")
	if !known || err != nil {
		t.Fatalf("known=%v err=%v", known, err)
	}
	m, _ := Load(s)
	if m.General.Callsign != "W1AW" {
		t.Fatalf("callsign not updated: %q", m.General.Callsign)
	}
	if m.General.Timeout != "240" || m.General.ID != "3180202" {
		t.Fatalf("unspecified fields lost on merge: %+v", m.General)
	}
}

func TestSetSectionRejectsUnknownField(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed")
	if _, err := SetSection(s, "general", []byte(`{"bogus":true}`), "test"); err == nil {
		t.Fatal("unknown field should be rejected")
	}
	if known, _ := SetSection(s, "nosuch", []byte(`{}`), "test"); known {
		t.Fatal("unknown section should report known=false")
	}
}

// Editing networks without resupplying passwords keeps the stored ones; a new
// password replaces; a dropped network is removed.
func TestSetNetworksPreservesPasswords(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // BM (pw s3cr3t) + TGIF (pw hunter2)

	// UI edits BM's port, supplies no password, and drops TGIF entirely.
	body := `[{"name":"BM_3102_United_States","address":"3102.master.brandmeister.network","port":"62035","password":"","enabled":true,"rewrites":["TGRewrite0=2,9,2,9,1"]}]`
	if err := SetNetworks(s, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(s)
	if len(m.Networks) != 1 {
		t.Fatalf("want 1 network after drop, got %d", len(m.Networks))
	}
	n := m.Networks[0]
	if n.Port != "62035" {
		t.Fatalf("port not updated: %q", n.Port)
	}
	if n.Password != "s3cr3t" {
		t.Fatalf("blank password should have kept the stored one, got %q", n.Password)
	}

	// Now supply a new password — it replaces.
	body2 := `[{"name":"BM_3102_United_States","address":"a","port":"1","password":"newpw","enabled":true,"rewrites":[]}]`
	_ = SetNetworks(s, []byte(body2), "test")
	m2, _ := Load(s)
	if m2.Networks[0].Password != "newpw" {
		t.Fatalf("new password should replace, got %q", m2.Networks[0].Password)
	}
}

func TestViewRedactsPasswords(t *testing.T) {
	v := fixture().View("/tmp/config.db")
	blob := ""
	for _, n := range v.Networks {
		blob += n.Name + n.Address + n.Port
		if !n.HasPassword {
			t.Fatalf("network %s should report has_password", n.Name)
		}
	}
	if strings.Contains(blob, "s3cr3t") || strings.Contains(blob, "hunter2") {
		t.Fatal("password leaked into the view")
	}
}

func TestGeneratedHeaderPresent(t *testing.T) {
	if !strings.HasPrefix(fixture().RenderMMDVM(), "; Generated by waypointd") {
		t.Fatal("rendered MMDVM-Host.ini missing the generated-by header")
	}
}

// section extracts the lines of one [Section] (excluding the header line) from
// rendered INI text, for isolation assertions.
func section(ini, name string) string {
	lines := strings.Split(ini, "\n")
	var out []string
	in := false
	for _, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			in = t == "["+name+"]"
			continue
		}
		if in && t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n")
}
