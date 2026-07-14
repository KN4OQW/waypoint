package lcd

import (
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// fakeDevice records every call so tests can assert exact writes (design §7).
type fakeDevice struct {
	rows, cols    int
	inits, clears int
	writes        []fakeWrite
	closed        bool
}

type fakeWrite struct {
	row  int
	text string
}

func (f *fakeDevice) Init(r, c int) error { f.rows, f.cols, f.inits = r, c, f.inits+1; return nil }
func (f *fakeDevice) WriteLine(row int, text string) error {
	f.writes = append(f.writes, fakeWrite{row, text})
	return nil
}
func (f *fakeDevice) Clear() error { f.clears++; return nil }
func (f *fakeDevice) Close() error { f.closed = true; return nil }

// row returns the most recent text written to row n ("" if never written).
func (f *fakeDevice) row(n int) string {
	s := ""
	for _, w := range f.writes {
		if w.row == n {
			s = w.text
		}
	}
	return s
}

func page(name string, enabled bool, dur string, lines ...string) config.LCDPage {
	return config.LCDPage{Enabled: enabled, Name: name, Duration: dur, Lines: lines}
}

func testInfo() Info {
	return Info{Callsign: "KN4OQW", Modes: []string{"DMR"}, Version: "1.0"}
}

func TestWindow(t *testing.T) {
	const wide = "0123456789ABCDEFGHIJKLMN" // 24 runes
	cases := []struct {
		name         string
		line         string
		cols, offset int
		want         string
	}{
		{"short-padded-20", "ABC", 20, 0, "ABC" + strings.Repeat(" ", 17)},
		{"exact-20", "0123456789ABCDEFGHIJ", 20, 0, "0123456789ABCDEFGHIJ"},
		{"wide-20-off0", wide, 20, 0, "0123456789ABCDEFGHIJ"},
		{"wide-20-off1", wide, 20, 1, "123456789ABCDEFGHIJK"},
		{"short-padded-16", "ABC", 16, 0, "ABC" + strings.Repeat(" ", 13)},
		{"exact-16", "0123456789ABCDEF", 16, 0, "0123456789ABCDEF"},
		{"wide-16-off0", wide, 16, 0, "0123456789ABCDEF"},
		{"wide-16-off2", wide, 16, 2, "23456789ABCDEFGH"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := window(c.line, c.cols, c.offset)
			if len([]rune(got)) != c.cols {
				t.Fatalf("width = %d, want %d (%q)", len([]rune(got)), c.cols, got)
			}
			if got != c.want {
				t.Errorf("window(%q,%d,%d) = %q, want %q", c.line, c.cols, c.offset, got, c.want)
			}
		})
	}
}

// A wide line's scroll wraps through the gap and back to the start.
func TestWindowWraps(t *testing.T) {
	line := "ABCDE"    // 5 runes, cols 4 → over-wide (full = "ABCDE   ", n=8)
	full := "ABCDE   " // with scrollGap
	for off := 0; off < 2*len(full); off++ {
		got := window(line, 4, off)
		want := ""
		for i := 0; i < 4; i++ {
			want += string(full[(off+i)%len(full)])
		}
		if got != want {
			t.Fatalf("off %d: window = %q, want %q", off, got, want)
		}
	}
}

func TestRotation(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	cfg := config.LCD{
		Rows: "1", Cols: "20", ScrollSpeed: "0",
		Pages: []config.LCDPage{
			page("P0", true, "8", "zero"),
			page("P1", true, "5", "one"),
		},
	}
	r := NewRenderer(cfg, testInfo(), &fakeDevice{}, nil)
	if err := r.Tick(base); err != nil { // initializes pageStart=base
		t.Fatal(err)
	}
	steps := []struct {
		at   time.Duration
		want string
	}{
		{0, "P0"},
		{7 * time.Second, "P0"},
		{8 * time.Second, "P1"},  // P0's 8s elapsed
		{12 * time.Second, "P1"}, // 4s into P1 (<5)
		{13 * time.Second, "P0"}, // P1's 5s elapsed → wrap
	}
	for _, s := range steps {
		p, _ := r.currentPage(base.Add(s.at))
		if p.Name != s.want {
			t.Errorf("at +%v: page %q, want %q", s.at, p.Name, s.want)
		}
	}
}

func TestRotationSkipsDisabledAndHoldsSingle(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	// Middle page disabled: rotation must cycle P0 ↔ P2 only.
	cfg := config.LCD{Rows: "1", Cols: "20", ScrollSpeed: "0", Pages: []config.LCDPage{
		page("P0", true, "5", "a"), page("P1", false, "5", "b"), page("P2", true, "5", "c"),
	}}
	r := NewRenderer(cfg, testInfo(), &fakeDevice{}, nil)
	_ = r.Tick(base)
	for i, want := range []string{"P0", "P2", "P0"} {
		p, _ := r.currentPage(base.Add(time.Duration(i) * 5 * time.Second))
		if p.Name != want {
			t.Errorf("step %d: %q, want %q", i, p.Name, want)
		}
	}

	// A single enabled page never advances, however much time passes.
	single := config.LCD{Rows: "1", Cols: "20", ScrollSpeed: "0", Pages: []config.LCDPage{page("Solo", true, "3", "x")}}
	rs := NewRenderer(single, testInfo(), &fakeDevice{}, nil)
	_ = rs.Tick(base)
	if p, _ := rs.currentPage(base.Add(1000 * time.Second)); p.Name != "Solo" {
		t.Errorf("single page did not hold: %q", p.Name)
	}
}

func TestActivityInterrupt(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	cfg := config.LCD{
		Rows: "2", Cols: "20", ScrollSpeed: "0", ActivityInterrupt: true,
		Pages: []config.LCDPage{page("Idle", true, "8", "{callsign}"), page("Two", true, "5", "two")},
	}
	dev := &fakeDevice{}
	r := NewRenderer(cfg, testInfo(), dev, nil)
	_ = r.Tick(base)
	if p, _ := r.currentPage(base.Add(1 * time.Second)); p.Name != "Idle" {
		t.Fatalf("pre-key page = %q, want Idle", p.Name)
	}

	// Key-down enters the interrupt; the caller page shows the live status.
	r.Handle(hub.Event{Type: "rf_voice_start", Mode: "DMR", Source: "W1ABC", Dest: "TG91", Time: base.Add(1 * time.Second)})
	if err := r.Tick(base.Add(1 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(dev.row(0), "RX DMR TG91 W1ABC") {
		t.Errorf("activity row0 = %q, want the live status", dev.row(0))
	}

	// Key-up: the caller page lingers, then rotation resumes at the paused page.
	r.Handle(hub.Event{Type: "rf_voice_end", Mode: "DMR", Source: "W1ABC", Dest: "TG91", Time: base.Add(5 * time.Second)})
	if p, _ := r.currentPage(base.Add(6 * time.Second)); p.Name != "Activity" {
		t.Errorf("within linger: page %q, want Activity", p.Name) // 1s < 3s linger
	}
	if p, _ := r.currentPage(base.Add(8 * time.Second)); p.Name != "Idle" {
		t.Errorf("after linger: page %q, want the resumed Idle", p.Name) // 3s ≥ linger
	}
}

// Re-rendering an unchanged frame writes nothing: only changed rows hit the bus.
func TestDiffedWrites(t *testing.T) {
	base := time.Date(2026, 7, 13, 0, 0, 0, 0, time.UTC)
	cfg := config.LCD{Rows: "4", Cols: "20", ScrollSpeed: "0", Pages: []config.LCDPage{
		page("Idle", true, "8", "{callsign}"), // one non-blank row; rows 1..3 blank
	}}
	dev := &fakeDevice{}
	r := NewRenderer(cfg, testInfo(), dev, nil)

	if err := r.Tick(base); err != nil {
		t.Fatal(err)
	}
	if dev.inits != 1 || dev.clears != 1 {
		t.Fatalf("want one Init+Clear, got init=%d clear=%d", dev.inits, dev.clears)
	}
	firstWrites := len(dev.writes)
	if firstWrites != 1 { // only row0 differs from the cleared (blank) panel
		t.Fatalf("first frame writes = %d, want 1 (row0 only)", firstWrites)
	}
	if got := strings.TrimRight(dev.row(0), " "); got != "KN4OQW" {
		t.Fatalf("row0 = %q", dev.row(0))
	}

	// Same instant, no events → identical frame → no further writes.
	if err := r.Tick(base); err != nil {
		t.Fatal(err)
	}
	if len(dev.writes) != firstWrites {
		t.Errorf("unchanged frame wrote %d extra rows", len(dev.writes)-firstWrites)
	}
}
