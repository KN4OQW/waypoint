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
		Modes:   Modes{DStar: false, DMR: true, YSF: true, P25: false, NXDN: true, M17: false, POCSAG: true, FM: true},
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
		// POCSAG carries the DAPNET AuthKey secret (redacted in the view, preserved on
		// blank) plus the paging channel and RIC whitelist/blacklist; every field is
		// non-empty so the round-trip cannot be masked by a rendered default. FM is the
		// analog surface (no gateway daemon) with non-default operator values.
		POCSAG: POCSAG{Frequency: "439987500", Server: "dapnet.afu.rwth-aachen.de", Callsign: "KN4OQW", AuthKey: "dapnet-s3cret", Whitelist: "1234567,7654321", Blacklist: "9999999"},
		FM:     FM{CTCSS: "127.3", Timeout: "180", KerchunkTime: "3", RFAudioBoost: "2", ExtAudioBoost: "3", AccessMode: "2"},
		// Cross-mode bridges: every bridge enabled with non-empty fields so the
		// round-trip cannot be masked by a rendered default filling an empty field.
		// The two DMR-master bridges (YSF2DMR, NXDN2DMR) each carry their own master
		// password so the round-trip and secret redaction/preservation cover them;
		// YSF2DMR also carries a DMR Options line (the WPSD addition) with an '=' in
		// the value, like a DMR network's options.
		YSF2DMR:  YSF2DMR{Enable: true, DMRId: "3180202", Master: "3102.master.brandmeister.network", Password: "y2d-s3cret", Options: "TS1_1=3100;TS2_1=31665;", TG: "31665"},
		DMR2YSF:  DMR2YSF{Enable: true, DMRId: "3180202", DefaultTG: "9"},
		YSF2NXDN: YSF2NXDN{Enable: true, NXDNId: "31802", TG: "1200"},
		DMR2NXDN: DMR2NXDN{Enable: true, DMRId: "3180202", NXDNId: "65519"},
		NXDN2DMR: NXDN2DMR{Enable: true, DMRId: "3180202", Master: "3102.master.brandmeister.network", Password: "n2d-s3cret", Options: "TS2_1=31665;", TG: "31665", NXDNTG: "20"},
		// LCD drives no INI, so it does not participate in the INI round-trip — it is
		// set to DefaultLCD() (the value fromINI assigns) so the render→parse→fromINI
		// comparison stays balanced. Its own store round-trip is covered by
		// TestLCDStoreRoundTrip.
		LCD: DefaultLCD(),
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
	// DAPNETGateway is an always-on gateway (like YSF/P25/NXDN), so its rendered INI
	// is always parsed and passed — the POCSAG gateway fields (server, callsign,
	// AuthKey, whitelist/blacklist) round-trip through it, while the mode enable and
	// paging Frequency round-trip through MMDVM-Host's [POCSAG] section.
	dpg, err := ParseINI(strings.NewReader(m.RenderDAPNETGateway()))
	if err != nil {
		t.Fatal(err)
	}
	// Cross-mode bridges: the fixture enables all five, so each renders and parses
	// back. A bridge INI has no Enable key — its presence in fromINI IS its Enable
	// (see fromINI), so passing every rendered bridge recovers Enable=true plus its
	// fields, exactly as a running node's files would.
	y2d, err := ParseINI(strings.NewReader(m.RenderYSF2DMR()))
	if err != nil {
		t.Fatal(err)
	}
	d2y, err := ParseINI(strings.NewReader(m.RenderDMR2YSF()))
	if err != nil {
		t.Fatal(err)
	}
	y2n, err := ParseINI(strings.NewReader(m.RenderYSF2NXDN()))
	if err != nil {
		t.Fatal(err)
	}
	d2n, err := ParseINI(strings.NewReader(m.RenderDMR2NXDN()))
	if err != nil {
		t.Fatal(err)
	}
	n2d, err := ParseINI(strings.NewReader(m.RenderNXDN2DMR()))
	if err != nil {
		t.Fatal(err)
	}
	got := fromINI(mm, dg, yg, nil, pg, ng, xg, mg, dpg, y2d, d2y, y2n, d2n, n2d) // dgid nil: fixture runs the classic YSFGateway
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

// DefaultLCD carries the documented panel defaults and two starter pages, so a
// first-time operator who enables the driver sees something.
func TestDefaultLCD(t *testing.T) {
	d := DefaultLCD()
	if d.Enabled {
		t.Error("DefaultLCD must be disabled")
	}
	if d.I2CBus != "/dev/i2c-1" || d.I2CAddress != "0x27" || d.Rows != "4" || d.Cols != "20" {
		t.Errorf("unexpected panel defaults: %+v", d)
	}
	if d.ScrollSpeed != "300" || !d.ActivityInterrupt {
		t.Errorf("unexpected behaviour defaults: %+v", d)
	}
	if len(d.Pages) != 2 || d.Pages[0].Name != "Idle" || d.Pages[1].Name != "Last Heard" {
		t.Fatalf("want two starter pages Idle+Last Heard: %+v", d.Pages)
	}
	for _, p := range d.Pages {
		if !p.Enabled || p.Duration == "" || len(p.Lines) == 0 {
			t.Errorf("starter page not usable: %+v", p)
		}
	}
}

// Property 1 for a store-only section — the LCD section round-trips through the
// store (Save → Load) with no loss, including ragged per-page line counts. LCD
// drives no INI, so this is its round-trip guarantee (it is deliberately absent
// from the INI round-trip in TestLosslessRoundTrip).
func TestLCDStoreRoundTrip(t *testing.T) {
	s := memStore(t)
	m := fixture()
	m.LCD = LCD{
		Enabled: true, I2CBus: "/dev/i2c-3", I2CAddress: "0x3f",
		Rows: "2", Cols: "16", ScrollSpeed: "250", ActivityInterrupt: false,
		Pages: []LCDPage{
			{Enabled: true, Name: "Status", Duration: "10", Lines: []string{"{callsign} {mode}", "{status}"}},
			{Enabled: false, Name: "Clock", Duration: "4", Lines: []string{"{time}"}},                        // ragged: one line
			{Enabled: true, Name: "Net", Duration: "6", Lines: []string{"{ip}", "{lh_call}", "{lh_tg}", ""}}, // ragged: four incl. blank
		},
	}
	if err := m.Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.LCD, got.LCD) {
		t.Fatalf("LCD store round-trip lost data:\n want %+v\n  got %+v", m.LCD, got.LCD)
	}
}

// Isolation: changing the LCD section never alters another section's rendered
// output (LCD drives no INI, so RenderMMDVM must be byte-identical), and changing
// another section never alters the stored LCD.
func TestLCDIsolation(t *testing.T) {
	s := memStore(t)
	m := fixture()
	if err := m.Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	before := m.RenderMMDVM()

	if _, err := SetSection(s, "lcd", []byte(`{"enabled":true,"i2c_address":"0x3f"}`), "test"); err != nil {
		t.Fatal(err)
	}
	after, _ := Load(s)
	if got := after.RenderMMDVM(); got != before {
		t.Error("changing LCD altered the rendered MMDVM-Host.ini")
	}

	// Changing an unrelated section leaves the stored LCD untouched.
	wantLCD := after.LCD
	if _, err := SetSection(s, "general", []byte(`{"callsign":"W1AW"}`), "test"); err != nil {
		t.Fatal(err)
	}
	final, _ := Load(s)
	if !reflect.DeepEqual(final.LCD, wantLCD) {
		t.Fatalf("changing [general] altered stored LCD:\n want %+v\n  got %+v", wantLCD, final.LCD)
	}
}

// The generic SetSection merge applies to LCD: a partial body updates only the
// named fields (pages survive), a body with a pages array replaces the pages
// wholesale, and an unknown field is rejected.
func TestSetSectionLCDMerge(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // LCD = DefaultLCD(), two pages

	// Partial: flip enabled only — the starter pages must survive the merge.
	if _, err := SetSection(s, "lcd", []byte(`{"enabled":true}`), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(s)
	if !m.LCD.Enabled || len(m.LCD.Pages) != 2 {
		t.Fatalf("partial merge dropped pages or missed enable: %+v", m.LCD)
	}

	// Full pages array replaces (json decodes an array over the slice, truncating).
	if _, err := SetSection(s, "lcd", []byte(`{"pages":[{"enabled":true,"name":"One","duration":"5","lines":["{callsign}"]}]}`), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ = Load(s)
	if len(m.LCD.Pages) != 1 || m.LCD.Pages[0].Name != "One" {
		t.Fatalf("pages array did not replace: %+v", m.LCD.Pages)
	}

	// Unknown field rejected.
	if _, err := SetSection(s, "lcd", []byte(`{"bogus":true}`), "test"); err == nil {
		t.Fatal("unknown LCD field should be rejected")
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

// TestPOCSAGFMRendered: the paging channel renders into MMDVM-Host's [POCSAG] +
// [POCSAG Network] (the 3800/4800 loopback to DAPNETGateway), the DAPNET login /
// filters render into DAPNETGateway.ini, and the analog [FM] operator keys render
// into MMDVM-Host. Every asserted key is one the pinned g4klx MMDVM-Host.ini /
// DAPNETGateway.ini exposes.
func TestPOCSAGFMRendered(t *testing.T) {
	m := fixture() // POCSAG + FM enabled, AuthKey dapnet-s3cret, CTCSS 127.3
	mm := m.RenderMMDVM()
	for sec, wants := range map[string][]string{
		"POCSAG":         {"Enable=1", "Frequency=439987500"},
		"POCSAG Network": {"Enable=1", "LocalPort=3800", "GatewayPort=4800"},
		"FM": {
			"Enable=1", "CTCSSFrequency=127.3", "Timeout=180",
			"KerchunkTime=3", "AccessMode=2", "RFAudioBoost=2", "ExtAudioBoost=3",
		},
	} {
		got := section(mm, sec)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("MMDVM [%s] missing %q\n%s", sec, w, got)
			}
		}
	}

	dpg := m.RenderDAPNETGateway()
	for sec, wants := range map[string][]string{
		"General": {"Callsign=KN4OQW", "RptPort=3800", "LocalPort=4800", "WhiteList=1234567,7654321", "BlackList=9999999"},
		"MQTT":    {"Name=dapnet-gateway"},
		"DAPNET":  {"Address=dapnet.afu.rwth-aachen.de", "AuthKey=dapnet-s3cret", "Port=43434"},
	} {
		got := section(dpg, sec)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("DAPNETGateway [%s] missing %q\n%s", sec, w, got)
			}
		}
	}

	// A blank whitelist/blacklist must be omitted (an empty value would filter
	// everything), not rendered as WhiteList=.
	m.POCSAG.Whitelist = ""
	m.POCSAG.Blacklist = ""
	if g := section(m.RenderDAPNETGateway(), "General"); strings.Contains(g, "WhiteList=") || strings.Contains(g, "BlackList=") {
		t.Errorf("blank whitelist/blacklist should be omitted from [General]\n%s", g)
	}
}

// TestDAPNETTargetRegistered: the POCSAG gateway is a render target wired to the
// dapnetgateway unit *when POCSAG mode is enabled*, and its target's Render
// matches the standalone renderer byte-for-byte. It is NOT always-on: unlike the
// digital-mode gateways it exits without a DAPNET AuthKey, so rendering it with
// POCSAG off would crash-loop the unit and stall every Apply.
func dapnetTarget(m *Model, paths Paths) *RenderTarget {
	for i := range m.RenderTargets(paths) {
		tg := m.RenderTargets(paths)[i]
		if tg.Unit == unitDAPNETGateway {
			return &tg
		}
	}
	return nil
}

func TestDAPNETTargetRegistered(t *testing.T) {
	m := fixture() // POCSAG enabled
	paths := Paths{DAPNETGateway: "/etc/DAPNETGateway.ini"}
	found := dapnetTarget(m, paths)
	if found == nil {
		t.Fatal("DAPNETGateway target not registered in RenderTargets when POCSAG is enabled")
	}
	if found.Path != paths.DAPNETGateway {
		t.Errorf("DAPNETGateway path = %q, want %q", found.Path, paths.DAPNETGateway)
	}
	if found.Render(m) != m.RenderDAPNETGateway() {
		t.Error("DAPNETGateway target Render does not match the standalone renderer")
	}
}

// TestDAPNETTargetGatedOnPOCSAG: with POCSAG disabled the DAPNETGateway target is
// absent, so apply neither writes DAPNETGateway.ini nor restarts (and crash-loops)
// its unit. Guards the fix for the ~45s Apply stall observed on hardware.
func TestDAPNETTargetGatedOnPOCSAG(t *testing.T) {
	m := fixture()
	m.Modes.POCSAG = false
	paths := Paths{DAPNETGateway: "/etc/DAPNETGateway.ini"}
	if found := dapnetTarget(m, paths); found != nil {
		t.Fatal("DAPNETGateway target present with POCSAG disabled; want gated out")
	}
}

// TestSetDAPNETPreservesAuthKey: editing POCSAG without resupplying the DAPNET
// AuthKey keeps the stored one; a non-blank one replaces it. Mirrors the DMR /
// ircDDB write-only-secret rule for the paging secret.
func TestSetDAPNETPreservesAuthKey(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // AuthKey dapnet-s3cret

	// UI edits the paging frequency, supplies no AuthKey (blank = keep stored), and
	// sends only the fields the panel manages — the merge must keep the rest.
	body := `{"frequency":"433000000","server":"dapnet.afu.rwth-aachen.de","auth_key":""}`
	if err := SetDAPNET(s, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
	m, _ := Load(s)
	if m.POCSAG.Frequency != "433000000" {
		t.Fatalf("frequency not updated: %q", m.POCSAG.Frequency)
	}
	if m.POCSAG.AuthKey != "dapnet-s3cret" {
		t.Fatalf("blank AuthKey should have kept the stored one, got %q", m.POCSAG.AuthKey)
	}
	// An unspecified field (Callsign) survives the merge.
	if m.POCSAG.Callsign != "KN4OQW" {
		t.Fatalf("unspecified field lost on merge: %q", m.POCSAG.Callsign)
	}

	// Now supply a new AuthKey — it replaces.
	if err := SetDAPNET(s, []byte(`{"auth_key":"rotated-key"}`), "test"); err != nil {
		t.Fatal(err)
	}
	m2, _ := Load(s)
	if m2.POCSAG.AuthKey != "rotated-key" {
		t.Fatalf("new AuthKey should replace, got %q", m2.POCSAG.AuthKey)
	}
}

// TestViewRedactsDAPNETAuthKey: the DAPNET AuthKey never reaches the view — it
// reports only has_auth_key. FM (no secret) surfaces its fields plainly.
func TestViewRedactsDAPNETAuthKey(t *testing.T) {
	v := fixture().View("/tmp/config.db")
	pv := fmt.Sprintf("%+v", v.POCSAG)
	if strings.Contains(pv, "dapnet-s3cret") {
		t.Fatal("DAPNET AuthKey leaked into the view")
	}
	if !v.POCSAG.HasAuthKey {
		t.Fatal("POCSAG view should report has_auth_key when a key is set")
	}
	if v.POCSAG.Frequency != "439987500" || v.POCSAG.Whitelist != "1234567,7654321" {
		t.Fatalf("POCSAG view should carry non-secret fields, got %+v", v.POCSAG)
	}
	if v.FM.CTCSS != "127.3" || !v.FM.Enable {
		t.Fatalf("FM view should surface its params, got %+v", v.FM)
	}
}

// TestPOCSAGFMIsolation: changing POCSAG or FM settings must not alter the [DMR]
// or [Modem] sections of MMDVM-Host.ini — the paging gateway fields render into
// DAPNETGateway.ini only.
func TestPOCSAGFMIsolation(t *testing.T) {
	m := fixture()
	beforeDMR, beforeModem := section(m.RenderMMDVM(), "DMR"), section(m.RenderMMDVM(), "Modem")

	m.POCSAG.Server = "changed.dapnet.example"
	m.POCSAG.AuthKey = "rotated" // gateway-only; never touches MMDVM
	m.FM.CTCSS = "100.0"

	if got := section(m.RenderMMDVM(), "DMR"); got != beforeDMR {
		t.Errorf("changing POCSAG/FM altered [DMR]:\n before %q\n after %q", beforeDMR, got)
	}
	if got := section(m.RenderMMDVM(), "Modem"); got != beforeModem {
		t.Errorf("changing POCSAG/FM altered [Modem]:\n before %q\n after %q", beforeModem, got)
	}
}

// TestCrossModeBridgeRendered: the fat YSF2DMR bridge renders its DMR master,
// password, target TG and the WPSD Options line into [DMR Network], and reads the
// YSF side from YSFGateway's [YSF Network] Port. Every asserted key is one the
// MMDVM_CM YSF2DMR daemon parses.
func TestCrossModeBridgeRendered(t *testing.T) {
	m := fixture()
	ini := m.RenderYSF2DMR()
	for sec, wants := range map[string][]string{
		"YSF Network": {"DstPort=42000", "Callsign=KN4OQW"},
		"DMR Network": {
			"Id=3180202",
			"Address=3102.master.brandmeister.network",
			"Password=y2d-s3cret",
			"StartupDstId=31665",
			"Options=TS1_1=3100;TS2_1=31665;",
		},
	} {
		got := section(ini, sec)
		for _, w := range wants {
			if !strings.Contains(got, w) {
				t.Errorf("[%s] missing %q\n%s", sec, w, got)
			}
		}
	}
	// A blank Options must be omitted (like a DMR network's), not rendered empty.
	m.YSF2DMR.Options = ""
	if strings.Contains(section(m.RenderYSF2DMR(), "DMR Network"), "Options=") {
		t.Error("blank Options should be omitted from [DMR Network]")
	}
}

// TestCrossModeTargetsGated: an enabled bridge contributes a render target (INI +
// unit); a disabled one contributes none, so apply neither writes its file nor
// restarts its unit. The always-on MMDVM/DMRGateway targets keep the lead.
func TestCrossModeTargetsGated(t *testing.T) {
	paths := Paths{
		MMDVM: "/etc/MMDVM-Host.ini", DMRGateway: "/etc/DMRGateway.ini",
		YSFGateway: "/etc/YSFGateway.ini", P25Gateway: "/etc/P25Gateway.ini",
		NXDNGateway: "/etc/NXDNGateway.ini", DStarGateway: "/etc/dstargateway.cfg",
		M17Gateway: "/etc/M17Gateway.ini", YSF2DMR: "/etc/YSF2DMR.ini",
	}
	m := fixture()
	m.DMR2YSF.Enable = false
	m.YSF2NXDN.Enable = false
	m.DMR2NXDN.Enable = false
	m.NXDN2DMR.Enable = false // leave only YSF2DMR enabled

	has := func(targets []RenderTarget, unit string) bool {
		for _, tg := range targets {
			if tg.Unit == unit {
				return true
			}
		}
		return false
	}
	targets := m.RenderTargets(paths)
	if targets[0].Unit != unitMMDVM || targets[1].Unit != unitDMRGateway {
		t.Fatalf("bridges must append after the always-on gateways; got %q, %q", targets[0].Unit, targets[1].Unit)
	}
	if !has(targets, unitYSF2DMR) {
		t.Error("enabled YSF2DMR should contribute a render target")
	}
	if has(targets, unitDMR2YSF) {
		t.Error("disabled DMR2YSF must not contribute a render target")
	}

	// Enable them all: all five units appear.
	m2 := fixture()
	all := m2.RenderTargets(paths)
	for _, u := range []string{unitYSF2DMR, unitDMR2YSF, unitYSF2NXDN, unitDMR2NXDN, unitNXDN2DMR} {
		if !has(all, u) {
			t.Errorf("enabled bridge target %q missing", u)
		}
	}
}

// TestCrossModeIsolation: changing a bridge section renders into that bridge's own
// INI only — never the MMDVM-Host [DMR]/[Modem] sections, and never another
// bridge's file.
func TestCrossModeIsolation(t *testing.T) {
	m := fixture()
	beforeDMR, beforeModem := section(m.RenderMMDVM(), "DMR"), section(m.RenderMMDVM(), "Modem")
	beforeDMR2YSF := m.RenderDMR2YSF()

	m.YSF2DMR.Master = "changed.master.example"
	m.YSF2DMR.Password = "rotated"
	m.YSF2DMR.TG = "91"

	if got := section(m.RenderMMDVM(), "DMR"); got != beforeDMR {
		t.Errorf("changing YSF2DMR altered MMDVM [DMR]:\n before %q\n after %q", beforeDMR, got)
	}
	if got := section(m.RenderMMDVM(), "Modem"); got != beforeModem {
		t.Errorf("changing YSF2DMR altered MMDVM [Modem]:\n before %q\n after %q", beforeModem, got)
	}
	if got := m.RenderDMR2YSF(); got != beforeDMR2YSF {
		t.Errorf("changing YSF2DMR altered DMR2YSF.ini:\n before %q\n after %q", beforeDMR2YSF, got)
	}
}

// TestViewRedactsCrossModeSecrets: the two DMR-master bridges' passwords never
// reach the view — it reports only has_password.
func TestViewRedactsCrossModeSecrets(t *testing.T) {
	v := fixture().View("/tmp/config.db")
	cm := fmt.Sprintf("%+v", v.CrossMode)
	if strings.Contains(cm, "y2d-s3cret") || strings.Contains(cm, "n2d-s3cret") {
		t.Fatal("cross-mode DMR-master password leaked into the view")
	}
	if !v.CrossMode.YSF2DMR.HasPassword || !v.CrossMode.NXDN2DMR.HasPassword {
		t.Fatal("cross-mode bridges with a master password should report has_password")
	}
	// The non-secret bridge still surfaces its fields.
	if v.CrossMode.DMR2YSF.DefaultTG != "9" {
		t.Fatalf("DMR2YSF view should carry DefaultTG, got %q", v.CrossMode.DMR2YSF.DefaultTG)
	}
}

// TestSetCrossBridgePreservesPassword: editing a DMR-master bridge without
// resupplying its password keeps the stored one; a non-blank one replaces it.
// Mirrors the DMR-networks / ircDDB write-only-secret rule for the bridges.
func TestSetCrossBridgePreservesPassword(t *testing.T) {
	s := memStore(t)
	_ = fixture().Save(s, "seed") // YSF2DMR password y2d-s3cret

	// UI edits the target TG, supplies no password (blank = keep stored), and sends
	// only the fields the card manages — the merge must keep the rest.
	body := `{"enable":true,"master":"3102.master.brandmeister.network","tg":"91","password":""}`
	known, err := SetCrossBridge(s, "ysf2dmr", []byte(body), "test")
	if !known || err != nil {
		t.Fatalf("known=%v err=%v", known, err)
	}
	m, _ := Load(s)
	if m.YSF2DMR.TG != "91" {
		t.Fatalf("TG not updated: %q", m.YSF2DMR.TG)
	}
	if m.YSF2DMR.Password != "y2d-s3cret" {
		t.Fatalf("blank password should have kept the stored one, got %q", m.YSF2DMR.Password)
	}
	// An unspecified field (DMRId) survives the merge.
	if m.YSF2DMR.DMRId != "3180202" {
		t.Fatalf("unspecified field lost on merge: %q", m.YSF2DMR.DMRId)
	}

	// A supplied password replaces.
	if _, err := SetCrossBridge(s, "ysf2dmr", []byte(`{"password":"rotated"}`), "test"); err != nil {
		t.Fatal(err)
	}
	m2, _ := Load(s)
	if m2.YSF2DMR.Password != "rotated" {
		t.Fatalf("new password should replace, got %q", m2.YSF2DMR.Password)
	}

	// A no-secret bridge writes through unchanged, and an unknown section reports
	// known=false (mirrors SetSection).
	if _, err := SetCrossBridge(s, "dmr2ysf", []byte(`{"enable":true,"default_tg":"8"}`), "test"); err != nil {
		t.Fatal(err)
	}
	if known, _ := SetCrossBridge(s, "nosuch", []byte(`{}`), "test"); known {
		t.Fatal("unknown section should report known=false")
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
