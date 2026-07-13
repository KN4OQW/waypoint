package config

import (
	"encoding/json"
	"fmt"
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
		// Display fully populated with non-default values so the round-trip cannot be
		// masked by a rendered default filling an empty field. This node drives an
		// HD44780 over I2C (address 0x22), 4 rows × 20 cols.
		Display: Display{Type: "HD44780", OLEDType: "6", Port: "/dev/ttyUSB0", NextionLayout: "3", HD44780Rows: "4", HD44780Cols: "20", HD44780I2CAddr: "0x22"},
		DMR:     DMR{ColorCode: "1", ID: "3180202", EmbeddedLCOnly: true, SelfOnly: true, DumpTAData: true, Beacons: true},
		DMRNet:  DMRNet{LocalPort: "62032", GatewayAddress: "127.0.0.1", GatewayPort: "62031", Slot1: true, Slot2: true, Jitter: "360"},
		Modes:   Modes{DStar: false, DMR: true, YSF: true, P25: false, NXDN: true, M17: false, POCSAG: false, FM: false},
		// Routing round-trips through generated type templates, not verbatim
		// rewrites: a primary BM (catch-all/PassAll, the TG9990 Parrot rides it),
		// prefixed SystemX (prefix 4) and TGIF (prefix 5) alternates, and an XLX
		// section network. SystemX and TGIF are first-class WPSD networks (D1), each
		// exercised here with its secret (SystemX master password, TGIF security
		// key) so the round-trip and password preservation cover them. Options
		// carries a verbatim BM subscription string (with '=' in the value).
		Networks: []Network{
			{Name: "BM_3102_United_States", Type: NetBrandmeister, Primary: true, Address: "3102.master.brandmeister.network", Port: "62031", Password: "s3cr3t", Options: "StartRef=4000;RelinkTime=15;UserLink=1;", ESSID: "01", Enabled: true},
			{Name: "TGIF_Network", Type: NetTGIF, Address: "tgif.network", Port: "62031", Password: "hunter2", ESSID: "05", Enabled: false},
			{Name: "SystemX", Type: NetSystemX, Address: "systemx.pistar.uk", Port: "62031", Password: "sysx-key", ESSID: "02", Enabled: true},
			{Name: "XLX", Type: NetXLX, Port: "62030", Password: "xlxpw", XLXStartup: "950", XLXModule: "E", XLXSlot: "2", Enabled: true},
		},
		YSF:    YSF{LowDeviation: true, SelfOnly: false, TXHang: "6", RemoteGateway: false, ModeHang: "20"},
		YSFGW:  YSFGateway{Suffix: "RPT", WiresXPassthrough: true, Startup: "FCS00290", Revert: true, InactivityTimeout: "30", YSFNetwork: true, FCSNetwork: true, APRS: false},
		P25:    P25{NAC: "293", SelfOnly: true, OverrideUIDCheck: false, RemoteGateway: false, TXHang: "5"},
		P25GW:  P25Gateway{Static: "10100,10200", Voice: true, RFHangTime: "120", NetHangTime: "60"},
		NXDN:   NXDN{RAN: "1", SelfOnly: true, RemoteGateway: false, TXHang: "5"},
		NXDNGW: NXDNGateway{Static: "10200,65000", Voice: true, RFHangTime: "120", NetHangTime: "60"},
		DStar:  DStar{Module: "B", SelfOnly: true, RemoteGateway: false},
		DStarGW: DStarGateway{
			Reflector: "REF001 C", ReflectorReconnect: "Never",
			IRCDDBHostname: "ircv4.openquad.net", IRCDDBUsername: "KN4OQW", IRCDDBPassword: "irc-s3cret",
			Dextra: true, DPlus: true, DPlusLogin: "KN4OQW", DCS: true, XLX: false,
		},
		M17:   M17{CAN: "0", SelfOnly: true, AllowEncryption: false, TXHang: "5"},
		M17GW: M17Gateway{Suffix: "H", Startup: "M17-M17 C", Revert: true, HangTime: "240", Voice: true},
	}
}

// TestRenderTargetsRegistry: the target registry leads with MMDVM-Host and
// DMRGateway, wires each path/unit through, and each target's Render matches the
// standalone renderer byte-for-byte. This is the pattern a new mode copies to
// join the apply loop (issue #21 gateway-plugin seam) — no apply-code edits.
func TestRenderTargetsRegistry(t *testing.T) {
	m := fixture() // DMR enabled
	paths := Paths{
		MMDVM: "/etc/MMDVM-Host.ini", DMRGateway: "/etc/DMRGateway.ini",
		YSFGateway: "/etc/YSFGateway.ini", P25Gateway: "/etc/P25Gateway.ini",
		NXDNGateway: "/etc/NXDNGateway.ini", DStarGateway: "/etc/dstargateway.cfg",
		M17Gateway: "/etc/M17Gateway.ini",
	}
	targets := m.RenderTargets(paths)
	if len(targets) < 2 {
		t.Fatalf("want at least MMDVM + DMRGateway targets, got %d", len(targets))
	}

	want := []struct {
		path, unit string
		render     func(*Model) string
	}{
		{paths.MMDVM, "waypoint-mmdvm.service", (*Model).RenderMMDVM},
		{paths.DMRGateway, "waypoint-dmrgateway.service", (*Model).RenderDMRGateway},
	}
	for i, w := range want {
		got := targets[i]
		if got.Path != w.path {
			t.Errorf("target %d path = %q, want %q", i, got.Path, w.path)
		}
		if got.Unit != w.unit {
			t.Errorf("target %d unit = %q, want %q", i, got.Unit, w.unit)
		}
		if got.Render(m) != w.render(m) {
			t.Errorf("target %d Render does not match the standalone renderer", i)
		}
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
	ng, err := ParseINI(strings.NewReader(m.RenderNXDNGateway()))
	if err != nil {
		t.Fatal(err)
	}
	xg, err := ParseINI(strings.NewReader(m.RenderDStarGateway()))
	if err != nil {
		t.Fatal(err)
	}
	mg, err := ParseINI(strings.NewReader(m.RenderM17Gateway()))
	if err != nil {
		t.Fatal(err)
	}
	got := fromINI(mm, dg, yg, nil, pg, ng, xg, mg) // dgid nil: fixture runs the classic YSFGateway
	if !reflect.DeepEqual(m, got) {
		t.Fatalf("round-trip lost data:\n want %+v\n  got %+v", m, got)
	}
}

// TestDGIdGatewaySwap: enabling DG-ID swaps the System Fusion render target from
// YSFGateway to DGIdGateway (they share MMDVM-Host's 3200/4200 loopback and
// cannot co-run), and the generated DGIdGateway.ini carries the DG-ID table the
// daemon needs — DG-ID 0 the local Wires-X gateway, DG-ID 1 the Parrot, and the
// startup reflector as a static DG-ID network (YCS). Every asserted key is one
// the pinned DGIdGateway Conf.cpp parses.
func TestDGIdGatewaySwap(t *testing.T) {
	m := fixture()
	m.YSFGW.EnableDGId = true
	m.YSFGW.YCSNetwork = true
	m.YSFGW.Startup = "FCS00290"

	paths := Paths{YSFGateway: "/etc/YSFGateway.ini", DGIdGateway: "/etc/DGIdGateway.ini"}
	ysf := m.RenderTargets(paths)[2] // the System Fusion slot
	if ysf.Path != paths.DGIdGateway || ysf.Unit != "waypoint-dgidgateway.service" {
		t.Fatalf("DG-ID slot not swapped: path=%q unit=%q", ysf.Path, ysf.Unit)
	}
	// The classic YSFGateway target must NOT also be present (mutually exclusive).
	for _, tg := range m.RenderTargets(paths) {
		if tg.Path == paths.YSFGateway {
			t.Fatalf("YSFGateway target still present alongside DGIdGateway")
		}
	}

	ini := m.RenderDGIdGateway()
	for sec, wants := range map[string][]string{
		"General":     {"RptPort=3200", "LocalPort=4200", "Suffix=RPT"},
		"MQTT":        {"Name=dgid-gateway"},
		"YSF Network": {"Hosts=" + ysfHostsPath},
		"DGId=0":      {"Type=Gateway", "Static=1"},
		"DGId=1":      {"Type=Parrot"},
		"DGId=5":      {"Type=FCS", "Name=FCS00290", "Static=1"},
	} {
		got := section(ini, sec)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("[%s] missing %q\n%s", sec, w, got)
			}
		}
	}

	// Round-trip: a node running DGIdGateway imports back as DG-ID enabled with
	// its startup reflector and YCS flag recovered from the DG-ID network block.
	dgid, err := ParseINI(strings.NewReader(ini))
	if err != nil {
		t.Fatal(err)
	}
	back := ysfGatewayFromINI(nil, dgid)
	if !back.EnableDGId || !back.YCSNetwork || back.Startup != "FCS00290" {
		t.Fatalf("DGIdGateway.ini did not round-trip: %+v", back)
	}
}

// TestDisplayRendered: the [General] Display selector and every driver
// subsection render from the model with the confirmed pre-MQTT MMDVM-Host key
// names. All five subsections are always present (like the stock ini) regardless
// of which type is selected, so a clone carries them for any driver.
func TestDisplayRendered(t *testing.T) {
	m := fixture() // Display: HD44780, 4x20, I2C 0x22, OLED type 6, Nextion layout 3, port /dev/ttyUSB0
	ini := m.RenderMMDVM()
	for sec, wants := range map[string][]string{
		"General":    {"Display=HD44780"},
		"HD44780":    {"Rows=4", "Columns=20", "I2CAddress=0x22", "Pins=11,10,0,1,2,3"},
		"OLED":       {"Type=6"},
		"Nextion":    {"Port=/dev/ttyUSB0", "ScreenLayout=3"},
		"TFT Serial": {"Port=/dev/ttyUSB0"},
		"LCDproc":    {"Address=localhost"},
	} {
		got := section(ini, sec)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("[%s] missing %q\n%s", sec, w, got)
			}
		}
	}
}

// TestDisplayIsolation: changing Display settings must not alter the [DMR] or
// [Modem] sections of MMDVM-Host.ini.
func TestDisplayIsolation(t *testing.T) {
	m := fixture()
	beforeDMR, beforeModem := section(m.RenderMMDVM(), "DMR"), section(m.RenderMMDVM(), "Modem")

	m.Display.Type = "OLED"
	m.Display.HD44780I2CAddr = "0x3c"

	if got := section(m.RenderMMDVM(), "DMR"); got != beforeDMR {
		t.Errorf("changing Display altered [DMR]:\n before %q\n after %q", beforeDMR, got)
	}
	if got := section(m.RenderMMDVM(), "Modem"); got != beforeModem {
		t.Errorf("changing Display altered [Modem]:\n before %q\n after %q", beforeModem, got)
	}
}

// TestYSFIsolation: changing System Fusion settings (mode params, gateway, or the
// DG-ID swap) must not alter the [DMR] or [Modem] sections of MMDVM-Host.ini.
func TestYSFIsolation(t *testing.T) {
	m := fixture()
	beforeDMR, beforeModem := section(m.RenderMMDVM(), "DMR"), section(m.RenderMMDVM(), "Modem")

	m.YSF.TXHang = "99"
	m.YSFGW.Startup = "FCS00123"
	m.YSFGW.EnableDGId = true // affects the render target, never MMDVM's DMR/Modem

	if got := section(m.RenderMMDVM(), "DMR"); got != beforeDMR {
		t.Errorf("changing YSF altered [DMR]:\n before %q\n after %q", beforeDMR, got)
	}
	if got := section(m.RenderMMDVM(), "Modem"); got != beforeModem {
		t.Errorf("changing YSF altered [Modem]:\n before %q\n after %q", beforeModem, got)
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

// SystemX and TGIF are first-class WPSD DMR networks (D1), each carrying its own
// secret (SystemX master password, TGIF security key). Editing them without
// resupplying the secret must keep the stored one, exactly like BrandMeister —
// the write-only-secret rule has to hold for every typed network, not just BM.
func TestSetNetworksPreservesSystemXTGIFSecrets(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // TGIF (key hunter2) + SystemX (key sysx-key)

	// UI toggles enables and edits ESSIDs but supplies no secrets (blank = keep).
	// The redacted view never carried the passwords, so the merge must restore them.
	body := `[
		{"name":"BM_3102_United_States","type":"brandmeister","primary":true,"address":"3102.master.brandmeister.network","port":"62031","password":"","options":"StartRef=4000;RelinkTime=15;UserLink=1;","essid":"01","enabled":true},
		{"name":"TGIF_Network","type":"tgif","address":"tgif.network","port":"62031","password":"","essid":"05","enabled":true},
		{"name":"SystemX","type":"systemx","address":"systemx.pistar.uk","port":"62031","password":"","essid":"07","enabled":true}
	]`
	if err := SetNetworks(s, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(s)
	byName := map[string]Network{}
	for _, n := range m.Networks {
		byName[n.Name] = n
	}
	if got := byName["TGIF_Network"]; got.Password != "hunter2" || !got.Enabled || got.ESSID != "05" {
		t.Fatalf("TGIF: blank secret should keep stored key + apply edits, got %+v", got)
	}
	if got := byName["SystemX"]; got.Password != "sysx-key" || got.ESSID != "07" {
		t.Fatalf("SystemX: blank secret should keep stored key + apply edits, got %+v", got)
	}

	// A supplied secret replaces (proves the preservation is scoped to blank).
	body2 := `[{"name":"SystemX","type":"systemx","address":"systemx.pistar.uk","port":"62031","password":"rotated","essid":"07","enabled":true}]`
	_ = SetNetworks(s, []byte(body2), "test")
	m2, _ := Load(s)
	if m2.Networks[0].Password != "rotated" {
		t.Fatalf("SystemX: new secret should replace, got %q", m2.Networks[0].Password)
	}
}

// Editing the D-Star gateway without resupplying the ircDDB password keeps the
// stored one; a non-blank password replaces it. Mirrors the DMR-networks rule
// (TestSetNetworksPreservesPasswords) for the other write-only secret.
func TestSetDStarGatewayPreservesPassword(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // ircDDB password irc-s3cret

	// UI edits the startup reflector, supplies no password (blank = keep stored),
	// and sends only the fields the panel manages — the merge must keep the rest.
	body := `{"reflector":"DCS006 B","ircddb_username":"KN4OQW","ircddb_password":""}`
	if err := SetDStarGateway(s, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(s)
	if m.DStarGW.Reflector != "DCS006 B" {
		t.Fatalf("reflector not updated: %q", m.DStarGW.Reflector)
	}
	if m.DStarGW.IRCDDBPassword != "irc-s3cret" {
		t.Fatalf("blank password should have kept the stored one, got %q", m.DStarGW.IRCDDBPassword)
	}
	// An unspecified field (Hostname) survives the merge.
	if m.DStarGW.IRCDDBHostname != "ircv4.openquad.net" {
		t.Fatalf("unspecified field lost on merge: %q", m.DStarGW.IRCDDBHostname)
	}

	// Now supply a new password — it replaces.
	if err := SetDStarGateway(s, []byte(`{"ircddb_password":"newirc"}`), "test"); err != nil {
		t.Fatal(err)
	}
	m2, _ := Load(s)
	if m2.DStarGW.IRCDDBPassword != "newirc" {
		t.Fatalf("new password should replace, got %q", m2.DStarGW.IRCDDBPassword)
	}
}

// TestDStarIsolation: changing D-Star settings (mode params or the gateway
// secret/reflector) must not alter the [DMR] or [Modem] sections of
// MMDVM-Host.ini — the gateway fields render into dstargateway.cfg only.
func TestDStarIsolation(t *testing.T) {
	m := fixture()
	beforeDMR, beforeModem := section(m.RenderMMDVM(), "DMR"), section(m.RenderMMDVM(), "Modem")

	m.DStar.SelfOnly = !m.DStar.SelfOnly
	m.DStarGW.Reflector = "DCS006 B"
	m.DStarGW.IRCDDBPassword = "changed" // gateway-only; never touches MMDVM

	if got := section(m.RenderMMDVM(), "DMR"); got != beforeDMR {
		t.Errorf("changing D-Star altered [DMR]:\n before %q\n after %q", beforeDMR, got)
	}
	if got := section(m.RenderMMDVM(), "Modem"); got != beforeModem {
		t.Errorf("changing D-Star altered [Modem]:\n before %q\n after %q", beforeModem, got)
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
	if strings.Contains(blob, "s3cr3t") || strings.Contains(blob, "hunter2") || strings.Contains(blob, "sysx-key") {
		t.Fatal("password leaked into the view")
	}
	// The D-Star ircDDB password is a secret too: the view reports only whether
	// one is set, never the value.
	if !v.DStar.HasIRCDDBPassword {
		t.Fatal("D-Star view should report has_ircddb_password")
	}
	dv := fmt.Sprintf("%+v", v.DStar)
	if strings.Contains(dv, "irc-s3cret") {
		t.Fatal("ircDDB password leaked into the view")
	}
}

// TestViewSurfacesNodeLock: the DMR view carries the Node Lock bit (WPSD's
// SelfOnly moved into the DMR panel) so the UI can read/write it, and it
// serializes under the "self_only" key the settings page binds to.
func TestViewSurfacesNodeLock(t *testing.T) {
	m := fixture() // DMR.SelfOnly = true (Node Lock = Private)
	v := m.View("/tmp/config.db")
	if !v.DMR.SelfOnly {
		t.Fatal("DMR view should surface SelfOnly (Node Lock) from the model")
	}
	blob, err := json.Marshal(v.DMR)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), `"self_only":true`) {
		t.Fatalf("DMR view JSON missing self_only: %s", blob)
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

// TestParrotRoutingGenerated is the regression for the original bug: a primary
// network must emit the TG9990 group→private TypeRewrite and PassAll on both
// slots, so the Parrot echoes without any hand-written routing.
func TestParrotRoutingGenerated(t *testing.T) {
	m := &Model{
		DMR: DMR{ID: "3180202"},
		Networks: []Network{
			{Name: "BM", Type: NetBrandmeister, Primary: true, Address: "bm.example", Enabled: true},
		},
	}
	got := section(m.RenderDMRGateway(), "DMR Network 1")
	for _, want := range []string{
		"TypeRewrite0=2,9990,2,9990", // group→private Parrot conversion
		"PassAllTG0=1", "PassAllTG1=2", "PassAllPC0=1", "PassAllPC1=2",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("primary network missing %q\n%s", want, got)
		}
	}
}

// TestPrefixRoutingGenerated checks a non-primary network is reached by its
// WPSD dial prefix (DMR+ = 8): dialing 8000321 on TS2 strips to TG 321.
func TestPrefixRoutingGenerated(t *testing.T) {
	m := &Model{
		DMR:      DMR{ID: "1"},
		Networks: []Network{{Name: "DMRPlus", Type: NetDMRPlus, Address: "dmrplus.example", Enabled: true}},
	}
	got := section(m.RenderDMRGateway(), "DMR Network 1")
	for _, want := range []string{
		"TGRewrite0=2,8,2,9,1",            // single-digit local shortcut
		"TGRewrite2=2,8000001,2,1,999999", // prefix strip on TS2
		"PCRewrite0=2,84000,2,4000,1001",  // reflector-control range
		"TypeRewrite2=2,8009990,2,9990",   // Parrot on the alternate
	} {
		if !strings.Contains(got, want) {
			t.Errorf("DMR+ network missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "PassAll") {
		t.Errorf("non-primary network must not emit PassAll\n%s", got)
	}
}

// TestDMRRouteOverride: a route ties a dialed TG to a specific gateway, rendered
// as a direct TGRewrite indexed past the template so it beats the primary.
func TestDMRRouteOverride(t *testing.T) {
	m := &Model{
		DMR: DMR{ID: "1"},
		Networks: []Network{
			{Name: "BM", Type: NetBrandmeister, Primary: true, Address: "bm.example", Enabled: true},
			{Name: "TGIF", Type: NetTGIF, Address: "tgif.network", Enabled: true},
		},
		Routes: []DMRRoute{{Slot: "2", TG: "31665", Network: "TGIF"}},
	}
	tgif := section(m.RenderDMRGateway(), "DMR Network 2")
	if !strings.Contains(tgif, "TGRewrite3=2,31665,2,31665,1") {
		t.Errorf("route override not appended to TGIF (expected TGRewrite3)\n%s", tgif)
	}
	// The route must not have leaked onto the primary.
	if bm := section(m.RenderDMRGateway(), "DMR Network 1"); strings.Contains(bm, "31665") {
		t.Errorf("route leaked onto primary\n%s", bm)
	}
}

// TestImportClassifies: a standard generated block imports as its clean type
// (no raw rewrites); a hand-tuned block is preserved verbatim as custom.
func TestImportClassifies(t *testing.T) {
	clean := "[DMR Network 1]\nName=BM_Master\nAddress=bm.example\n" +
		strings.Join(primaryRewrites(), "\n") + "\n"
	dg, err := ParseINI(strings.NewReader(clean))
	if err != nil {
		t.Fatal(err)
	}
	nets := importNetworks(dg, "3180202")
	if len(nets) != 1 || nets[0].Type != NetBrandmeister || !nets[0].Primary || len(nets[0].Rewrites) != 0 {
		t.Fatalf("standard BM block should import as primary brandmeister w/ no raw rewrites: %+v", nets)
	}

	tweaked := "[DMR Network 1]\nName=BM_Master\nAddress=bm.example\nTGRewrite0=2,1234,2,5678,1\n"
	dg2, _ := ParseINI(strings.NewReader(tweaked))
	nets2 := importNetworks(dg2, "3180202")
	if len(nets2) != 1 || nets2[0].Type != NetCustom || len(nets2[0].Rewrites) != 1 {
		t.Fatalf("hand-tuned block should be preserved as custom: %+v", nets2)
	}
}

// TestLegacyUntypedNetworkPreserved: a network from a store predating typed
// routing (Type == "") must render its stored rewrites verbatim, so upgrading
// the binary never wipes DMR routing before the operator re-picks a type.
func TestLegacyUntypedNetworkPreserved(t *testing.T) {
	m := &Model{
		DMR: DMR{ID: "1"},
		Networks: []Network{{
			Name: "BM", Address: "bm.example", Enabled: true,
			Rewrites: []string{"TGRewrite0=2,9,2,9,1", "PassAllTG=2", "PassAllPC=2"},
		}},
	}
	got := section(m.RenderDMRGateway(), "DMR Network 1")
	for _, want := range []string{"TGRewrite0=2,9,2,9,1", "PassAllTG=2", "PassAllPC=2"} {
		if !strings.Contains(got, want) {
			t.Errorf("legacy untyped network dropped %q\n%s", want, got)
		}
	}
}
