package lcd

import (
	"strconv"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// LCDDevice is the only hardware seam (design §7). The real PCF8574/HD44780
// implementation lands in a later stage; the pure layer and its tests drive a
// fake. WriteLine always receives text already sized to cols and ASCII-safe.
type LCDDevice interface {
	Init(rows, cols int) error
	WriteLine(row int, text string) error
	Clear() error
	Close() error
}

// scrollGap separates the end and the wrapped start of an over-wide line so a
// marquee reads clearly.
const scrollGap = "   "

// defaultLinger holds the activity page briefly after key-up before rotation
// resumes, so a quick over-and-out is still readable.
const defaultLinger = 3 * time.Second

// activityPage is the synthesized "who's talking" screen shown during the
// activity interrupt. It is not operator-configurable; it renders through the
// same token engine, so it adapts to the panel width and available data.
var activityPage = config.LCDPage{
	Name: "Activity",
	Lines: []string{
		"{status}",
		"{lh_call}  {lh_tg}",
		"{lh_mode}",
		"",
	},
}

// Renderer folds the event stream into state and paints the device on each Tick.
// It is deterministic: given the same events and Tick times it writes identical
// bytes, so its scheduling is fully unit-testable without real time — Tick takes
// the current instant rather than reading a clock. A wiring stage drives Tick
// from a ticker and Handle from the hub feed.
type Renderer struct {
	cfg  config.LCD
	info Info
	dev  LCDDevice
	ip   func() string

	rows, cols int
	scroll     time.Duration
	linger     time.Duration

	st state

	started        bool
	pageIdx        int       // index into the enabled-pages slice
	pageStart      time.Time // when the current rotation page began showing
	interrupt      bool      // showing the synthesized activity page
	interruptStart time.Time // when the interrupt began (scroll anchor)
	endedAt        time.Time // when the interrupting call ended (for linger)

	last []string // last text written per row (diffed writes)
}

// NewRenderer builds a renderer for a panel + page set. rows/cols/scroll are
// parsed from the config with sane fallbacks so a malformed value never panics.
func NewRenderer(cfg config.LCD, info Info, dev LCDDevice, ip func() string) *Renderer {
	rows := clampAtLeast(atoiDef(cfg.Rows, 4), 1)
	cols := clampAtLeast(atoiDef(cfg.Cols, 20), 1)
	return &Renderer{
		cfg: cfg, info: info, dev: dev, ip: ip,
		rows: rows, cols: cols,
		scroll: time.Duration(atoiDef(cfg.ScrollSpeed, 300)) * time.Millisecond,
		linger: defaultLinger,
		last:   make([]string, rows),
	}
}

// Handle folds one event into the derived state and manages the activity-
// interrupt edges (enter on key-down, note key-up for the linger).
func (r *Renderer) Handle(e hub.Event) {
	prevActive := r.st.active
	r.st.handle(e)
	if !r.cfg.ActivityInterrupt {
		return
	}
	if !prevActive && r.st.active {
		r.interrupt = true
		r.interruptStart = e.Time
	}
	if prevActive && !r.st.active {
		r.endedAt = e.Time
	}
}

// Tick renders the current frame for instant now and writes only the rows that
// changed since the last frame. The first Tick initializes and clears the panel.
func (r *Renderer) Tick(now time.Time) error {
	if !r.started {
		if err := r.dev.Init(r.rows, r.cols); err != nil {
			return err
		}
		if err := r.dev.Clear(); err != nil {
			return err
		}
		blank := strings.Repeat(" ", r.cols)
		for i := range r.last {
			r.last[i] = blank // Clear left the panel full of spaces
		}
		r.started = true
		r.pageStart = now
	}
	rows := r.frame(now)
	for i := 0; i < r.rows; i++ {
		if rows[i] == r.last[i] {
			continue
		}
		if err := r.dev.WriteLine(i, rows[i]); err != nil {
			return err
		}
		r.last[i] = rows[i]
	}
	return nil
}

// frame renders the current page's rows for instant now, each exactly cols wide.
func (r *Renderer) frame(now time.Time) []string {
	page, anchor := r.currentPage(now)
	rc := renderCtx{st: &r.st, info: r.info, now: now, ip: r.ip}
	out := make([]string, r.rows)
	for i := 0; i < r.rows; i++ {
		tmpl := ""
		if i < len(page.Lines) {
			tmpl = page.Lines[i]
		}
		text := expand(tmpl, rc)
		off := 0
		if r.scroll > 0 && len([]rune(text)) > r.cols {
			off = int(now.Sub(anchor) / r.scroll)
		}
		out[i] = window(text, r.cols, off)
	}
	return out
}

// currentPage resolves which page to show at instant now, advancing rotation and
// entering/leaving the activity interrupt as needed. It returns the page and the
// scroll anchor (the instant that page began showing).
func (r *Renderer) currentPage(now time.Time) (config.LCDPage, time.Time) {
	// Activity interrupt: hold the caller page while keyed and through the linger
	// after key-up, then resume rotation where it paused.
	if r.interrupt {
		if !r.st.active && now.Sub(r.endedAt) >= r.linger {
			r.interrupt = false
			r.pageStart = now // give the resumed page a fresh hold
		} else {
			return activityPage, r.interruptStart
		}
	}
	pages := r.enabledPages()
	if len(pages) == 0 {
		return config.LCDPage{}, r.pageStart
	}
	if r.pageIdx >= len(pages) {
		r.pageIdx = 0
	}
	// A single enabled page just stays put (no rotation).
	if len(pages) > 1 {
		dur := time.Duration(atoiDef(pages[r.pageIdx].Duration, 5)) * time.Second
		if now.Sub(r.pageStart) >= dur {
			r.pageIdx = (r.pageIdx + 1) % len(pages)
			r.pageStart = now
		}
	}
	return pages[r.pageIdx], r.pageStart
}

func (r *Renderer) enabledPages() []config.LCDPage {
	var ps []config.LCDPage
	for _, p := range r.cfg.Pages {
		if p.Enabled {
			ps = append(ps, p)
		}
	}
	return ps
}

// window fits a line to exactly cols runes: as-is when it already fits, padded
// with spaces when short, or a wrapping marquee window at the given offset when
// over-wide (design §6). Padding to full width overwrites stale characters on
// the physical panel, which does not auto-clear.
func window(line string, cols, offset int) string {
	r := []rune(line)
	switch {
	case len(r) == cols:
		return line
	case len(r) < cols:
		return line + strings.Repeat(" ", cols-len(r))
	}
	full := append(r, []rune(scrollGap)...)
	n := len(full)
	off := ((offset % n) + n) % n
	out := make([]rune, cols)
	for i := 0; i < cols; i++ {
		out[i] = full[(off+i)%n]
	}
	return string(out)
}

func atoiDef(s string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
		return v
	}
	return def
}

func clampAtLeast(v, min int) int {
	if v < min {
		return min
	}
	return v
}
