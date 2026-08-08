package config

import (
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

func panelTestStore(t *testing.T, l LCD) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Model{LCD: l}
	if err := m.Save(st, "test"); err != nil {
		t.Fatal(err)
	}
	return st
}

func lcdOf(t *testing.T, st *store.Store) LCD {
	t.Helper()
	var l LCD
	if _, err := st.GetInto("lcd", &l); err != nil {
		t.Fatal(err)
	}
	return l
}

// A node that has never probed reports the never-looked state rather than an
// error — every node upgraded into this feature has no record at all.
func TestPanelStateIsEmptyBeforeAnyProbe(t *testing.T) {
	st := panelTestStore(t, DefaultLCD())
	got, err := GetPanelState(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Found != nil || got.CheckedAt != "" {
		t.Fatalf("unprobed node reports %+v, want the zero state", got)
	}
}

// The "we looked and there was nothing" record survives a round trip. It is a
// different state from never having looked, and the UI says different things
// about them.
func TestPanelStateRecordsAnEmptyAnswer(t *testing.T) {
	st := panelTestStore(t, DefaultLCD())
	if err := SetPanelState(st, PanelState{CheckedAt: "2026-08-07T12:00:00Z"}, "test"); err != nil {
		t.Fatal(err)
	}
	got, err := GetPanelState(st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Found != nil {
		t.Fatalf("found = %+v, want nil", got.Found)
	}
	if got.CheckedAt != "2026-08-07T12:00:00Z" {
		t.Fatalf("checked_at = %q", got.CheckedAt)
	}
}

// Adoption is the crossing #136 is about: bus, address AND the enable, because a
// configured-but-disabled panel is still a dark panel.
func TestAdoptPanelWritesTheBusAddressAndEnablesTheDriver(t *testing.T) {
	l := DefaultLCD()
	l.Enabled = false
	l.I2CBus, l.I2CAddress = "", ""
	st := panelTestStore(t, l)

	a, err := AdoptPanel(st, PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	got := lcdOf(t, st)
	if !got.Enabled || got.I2CBus != "/dev/i2c-1" || got.I2CAddress != "0x27" {
		t.Fatalf("lcd after adopt = %+v", got)
	}
	if len(a.Changed) != 3 {
		t.Fatalf("changed = %v, want bus, address and enabled", a.Changed)
	}
	// The operator is shown what moved, so enabling a display is never something
	// that merely happened to their config.
	joined := strings.Join(a.Changed, "; ")
	for _, want := range []string{"i2c_bus", "i2c_address", "enabled"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("changed %q does not name %s", joined, want)
		}
	}
}

// The pages are the operator's content and geometry is not something an address
// can reveal, so neither is touched.
func TestAdoptPanelLeavesPagesAndGeometryAlone(t *testing.T) {
	l := DefaultLCD()
	l.Rows, l.Cols = "2", "16"
	st := panelTestStore(t, l)

	if _, err := AdoptPanel(st, PanelFound{Bus: "/dev/i2c-0", Addr: "0x3f"}, "test"); err != nil {
		t.Fatal(err)
	}
	got := lcdOf(t, st)
	if got.Rows != "2" || got.Cols != "16" {
		t.Fatalf("geometry = %sx%s, want 16x2", got.Cols, got.Rows)
	}
	if len(got.Pages) != len(l.Pages) {
		t.Fatalf("pages = %d, want the %d it started with", len(got.Pages), len(l.Pages))
	}
}

// Adopting the panel already configured changes nothing and says so — the UI
// turns an empty change list into "already configured for this panel".
func TestAdoptPanelIsIdempotent(t *testing.T) {
	l := DefaultLCD()
	l.Enabled, l.I2CBus, l.I2CAddress = true, "/dev/i2c-1", "0x27"
	st := panelTestStore(t, l)

	a, err := AdoptPanel(st, PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"}, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Changed) != 0 {
		t.Fatalf("changed = %v, want nothing", a.Changed)
	}
}

// A record with no bus or address is not adoptable. It should never reach here —
// the routes check for a detection first — but writing a half-empty device path
// into the config would produce a renderer that fails to open and an operator
// with no idea why.
func TestAdoptPanelRefusesAnEmptyDetection(t *testing.T) {
	st := panelTestStore(t, DefaultLCD())
	for _, f := range []PanelFound{{}, {Bus: "/dev/i2c-1"}, {Addr: "0x27"}} {
		if _, err := AdoptPanel(st, f, "test"); err == nil {
			t.Fatalf("AdoptPanel(%+v) was accepted", f)
		}
	}
	if lcdOf(t, st).Enabled {
		t.Fatal("a refused adopt still enabled the driver")
	}
}

// Turning the driver on is the moment an invalid page set stops being harmless,
// so adoption passes the same geometry rule every other write to this section does.
func TestAdoptPanelRefusesAConfigItsPagesDoNotFit(t *testing.T) {
	l := DefaultLCD()
	l.Rows = "2"
	l.Pages = []LCDPage{{Name: "too tall", Lines: []string{"a", "b", "c"}}}
	st := panelTestStore(t, l)

	if _, err := AdoptPanel(st, PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"}, "test"); err == nil {
		t.Fatal("adopted a panel into a config whose pages do not fit it")
	}
	if lcdOf(t, st).Enabled {
		t.Fatal("a refused adopt still enabled the driver")
	}
}
