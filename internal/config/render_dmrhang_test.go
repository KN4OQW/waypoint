package config

import (
	"strings"
	"testing"
)

// The DMR hang timers are the one place in [DMR] where "no value" and "the
// upstream default" mean different things. MMDVM-Host's [General] RFModeHang /
// NetModeHang fan out to every per-mode hang variable (Conf.cpp:575-578) and the
// per-section keys are parsed afterwards, so any rendered per-section key wins —
// including one that merely restates the default. Rendering CallHang/TXHang/
// ModeHang unconditionally would therefore quietly sever the General fan-out on
// every existing node. These tests pin the omit-when-blank contract in both
// directions so that cannot regress into a def() fallback.

// A model with all four fields blank must render no hang keys at all, in either
// section — the operator's global mode hang keeps governing.
func TestDMRHangTimersOmittedWhenBlank(t *testing.T) {
	m := fixture()
	m.DMR.CallHang, m.DMR.TXHang, m.DMR.ModeHang = "", "", ""
	m.DMRNet.ModeHang = ""

	ini := m.RenderMMDVM()
	for _, tc := range []struct{ sec, key string }{
		{"DMR", "CallHang"},
		{"DMR", "TXHang"},
		{"DMR", "ModeHang"},
		{"DMR Network", "ModeHang"},
	} {
		got := section(ini, tc.sec)
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, tc.key+"=") {
				t.Errorf("[%s] rendered %q with a blank model field; blank must omit the key so [General] still governs\n--- [%s] ---\n%s",
					tc.sec, line, tc.sec, got)
			}
		}
	}
	// Whitespace is not a value either: the renderer trims before deciding.
	m.DMR.TXHang, m.DMRNet.ModeHang = "  ", "\t"
	if got := section(m.RenderMMDVM(), "DMR"); strings.Contains(got, "TXHang") {
		t.Errorf("[DMR] rendered TXHang from a whitespace-only field:\n%s", got)
	}
	if got := section(m.RenderMMDVM(), "DMR Network"); strings.Contains(got, "ModeHang") {
		t.Errorf("[DMR Network] rendered ModeHang from a whitespace-only field:\n%s", got)
	}
}

// Set fields render exactly the given values, in the right section, with no
// clamping or normalisation on Waypoint's side — the host does its own clamping
// (MMDVM-Host.cpp:684-693) and we must not second-guess it.
func TestDMRHangTimersRenderedWhenSet(t *testing.T) {
	m := fixture()
	m.DMR.CallHang, m.DMR.TXHang, m.DMR.ModeHang = "8", "6", "30"
	m.DMRNet.ModeHang = "25"

	ini := m.RenderMMDVM()
	dmr := section(ini, "DMR")
	for _, want := range []string{"CallHang=8", "TXHang=6", "ModeHang=30"} {
		if !hasLine(dmr, want) {
			t.Errorf("[DMR] missing %q:\n%s", want, dmr)
		}
	}
	// The net-side timer belongs to [DMR Network] only; [DMR] must carry the RF one.
	dmrNet := section(ini, "DMR Network")
	if !hasLine(dmrNet, "ModeHang=25") {
		t.Errorf("[DMR Network] missing ModeHang=25:\n%s", dmrNet)
	}
	if hasLine(dmrNet, "ModeHang=30") {
		t.Errorf("[DMR Network] took the RF mode hang:\n%s", dmrNet)
	}
	if hasLine(dmr, "ModeHang=25") {
		t.Errorf("[DMR] took the net mode hang:\n%s", dmr)
	}
	// A deliberately oversized call hold renders verbatim: reducing it here would
	// hide the host's clamp from the operator instead of letting the log show it.
	m.DMR.CallHang = "600"
	if !hasLine(section(m.RenderMMDVM(), "DMR"), "CallHang=600") {
		t.Error("[DMR] did not render an oversized CallHang verbatim; clamping is the host's job")
	}
}

// Store round-trip: Save/Load preserves all four, and a partial SetSection write
// merges without dropping them (the guarantee the settings UI relies on when it
// PUTs only the fields its panel manages).
func TestDMRHangTimersStoreRoundTrip(t *testing.T) {
	s := memStore(t)
	m := fixture()
	m.DMR.CallHang, m.DMR.TXHang, m.DMR.ModeHang = "8", "6", "30"
	m.DMRNet.ModeHang = "25"
	if err := m.Save(s, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.DMR.CallHang != "8" || got.DMR.TXHang != "6" || got.DMR.ModeHang != "30" || got.DMRNet.ModeHang != "25" {
		t.Fatalf("hang timers lost in Save/Load: dmr=%+v dmrnet.mode_hang=%q", got.DMR, got.DMRNet.ModeHang)
	}

	// A write that names only ColorCode must leave the timers alone.
	if known, err := SetSection(s, "dmr", []byte(`{"color_code":"3"}`), "test"); err != nil || !known {
		t.Fatalf("SetSection(dmr): known=%v err=%v", known, err)
	}
	if known, err := SetSection(s, "dmrnet", []byte(`{"jitter":"400"}`), "test"); err != nil || !known {
		t.Fatalf("SetSection(dmrnet): known=%v err=%v", known, err)
	}
	got, err = Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.DMR.ColorCode != "3" || got.DMR.CallHang != "8" || got.DMR.TXHang != "6" || got.DMR.ModeHang != "30" {
		t.Errorf("partial [dmr] write dropped hang timers: %+v", got.DMR)
	}
	if got.DMRNet.Jitter != "400" || got.DMRNet.ModeHang != "25" {
		t.Errorf("partial [dmrnet] write dropped the net mode hang: %+v", got.DMRNet)
	}

	// The new JSON names are accepted by the DisallowUnknownFields decoder, and
	// blanking a timer through the API is a real value, not a no-op merge — that is
	// how the UI hands control back to [General].
	if known, err := SetSection(s, "dmr", []byte(`{"call_hang":"","tx_hang":"","mode_hang":""}`), "test"); err != nil || !known {
		t.Fatalf("SetSection(dmr) blanking: known=%v err=%v", known, err)
	}
	if known, err := SetSection(s, "dmrnet", []byte(`{"mode_hang":""}`), "test"); err != nil || !known {
		t.Fatalf("SetSection(dmrnet) blanking: known=%v err=%v", known, err)
	}
	got, err = Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if got.DMR.CallHang != "" || got.DMR.TXHang != "" || got.DMR.ModeHang != "" || got.DMRNet.ModeHang != "" {
		t.Errorf("blanking the timers through SetSection did not clear them: dmr=%+v dmrnet=%+v", got.DMR, got.DMRNet)
	}
	if got.DMR.ColorCode != "3" {
		t.Errorf("blanking the timers clobbered ColorCode: %+v", got.DMR)
	}
	if ini := m.RenderMMDVM(); ini == "" {
		t.Fatal("render returned empty")
	}
}

// The DMR tab's read model surfaces all four on one card, including the
// [DMR Network] timer, which lives on the dmrnet store section but belongs beside
// the RF timer it is clamped against.
func TestViewDMRSurfacesHangTimers(t *testing.T) {
	m := fixture()
	m.DMR.CallHang, m.DMR.TXHang, m.DMR.ModeHang = "8", "6", "30"
	m.DMRNet.ModeHang = "25"
	v := m.View(Sources{})
	if v.DMR.CallHang != "8" || v.DMR.TXHang != "6" || v.DMR.ModeHang != "30" || v.DMR.NetModeHang != "25" {
		t.Errorf("ViewDMR hang timers wrong: %+v", v.DMR)
	}
}

// Importing a Pi-Star ini carries explicit values through and leaves absent keys
// blank — an imported node that never set a per-section hang must keep deferring
// to [General] afterwards, exactly as it did before the import.
func TestImportDMRHangTimers(t *testing.T) {
	const withHangs = `[General]
Callsign=W1ABC
Id=3161234
RFModeHang=300
NetModeHang=300
[DMR]
Enable=1
ColorCode=1
CallHang=7
TXHang=5
ModeHang=45
[DMR Network]
Enable=1
ModeHang=20
`
	mm, err := ParseINI(strings.NewReader(withHangs))
	if err != nil {
		t.Fatal(err)
	}
	m := fromINI(mm, nil, nil, nil, nil, nil, nil, nil, nil)
	if m.DMR.CallHang != "7" || m.DMR.TXHang != "5" || m.DMR.ModeHang != "45" {
		t.Errorf("import dropped [DMR] hang timers: %+v", m.DMR)
	}
	if m.DMRNet.ModeHang != "20" {
		t.Errorf("import dropped [DMR Network] ModeHang: %q", m.DMRNet.ModeHang)
	}

	const withoutHangs = `[General]
Callsign=W1ABC
Id=3161234
RFModeHang=300
NetModeHang=300
[DMR]
Enable=1
ColorCode=1
[DMR Network]
Enable=1
`
	mm, err = ParseINI(strings.NewReader(withoutHangs))
	if err != nil {
		t.Fatal(err)
	}
	m = fromINI(mm, nil, nil, nil, nil, nil, nil, nil, nil)
	if m.DMR.CallHang != "" || m.DMR.TXHang != "" || m.DMR.ModeHang != "" || m.DMRNet.ModeHang != "" {
		t.Errorf("import invented hang timers for keys the source ini never set: dmr=%+v dmrnet=%+v", m.DMR, m.DMRNet)
	}
	// And the re-render is clean: importing a node without hangs must not add them.
	ini := m.RenderMMDVM()
	if strings.Contains(section(ini, "DMR"), "Hang") || strings.Contains(section(ini, "DMR Network"), "ModeHang") {
		t.Errorf("re-render after a hang-free import emitted hang keys:\n--- [DMR] ---\n%s\n--- [DMR Network] ---\n%s",
			section(ini, "DMR"), section(ini, "DMR Network"))
	}
}

// hasLine reports whether a rendered section contains exactly this key=value
// line, so "ModeHang=3" cannot satisfy an assertion for "ModeHang=30".
func hasLine(sec, want string) bool {
	for _, l := range strings.Split(sec, "\n") {
		if strings.TrimSpace(l) == want {
			return true
		}
	}
	return false
}
