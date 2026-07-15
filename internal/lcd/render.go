package lcd

import (
	"context"
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

// defaultLinger holds an activity page briefly after key-up before rotation
// resumes, so a quick over-and-out is still readable. It is the fallback when
// LCD.LingerSecs is blank or malformed.
const defaultLinger = 3 * time.Second

// fallbackActivityPage is the synthesized "who's talking" screen shown during an
// activity interrupt when the operator has defined no page of their own with
// interrupt=true. It renders through the same token engine, so it adapts to the
// panel width and available data. Once the operator marks one of their pages
// interrupt=true, that page is used instead (interruptPage).
var fallbackActivityPage = config.LCDPage{
	Name: "Activity",
	Lines: []string{
		"{status}",
		"{source}  {tg}",
		"{mode}",
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
	linger := defaultLinger
	if v, err := strconv.Atoi(strings.TrimSpace(cfg.LingerSecs)); err == nil && v >= 0 {
		linger = time.Duration(v) * time.Second
	}
	return &Renderer{
		cfg: cfg, info: info, dev: dev, ip: ip,
		rows: rows, cols: cols,
		scroll: time.Duration(atoiDef(cfg.ScrollSpeed, 300)) * time.Millisecond,
		linger: linger,
		last:   make([]string, rows),
	}
}

// Run drives the renderer until ctx is canceled: it folds events as they arrive
// and paints a frame on every tick, closing the device on return. Before each
// tick it drains any already-queued events, so a burst of events immediately
// before a frame is all reflected in it. A transient device write error is not
// fatal — the next tick retries — so a flaky panel never stops the daemon. A
// wiring stage supplies the hub event channel and a ticker's channel.
func (r *Renderer) Run(ctx context.Context, events <-chan hub.Event, ticks <-chan time.Time) error {
	defer r.dev.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			r.Handle(e)
		case t, ok := <-ticks:
			if !ok {
				ticks = nil
				continue
			}
			r.drain(events)
			_ = r.Tick(t) // a transient device error is not fatal; the next tick retries
		}
	}
}

// drain folds every event already queued on the channel, without blocking, so
// the frame about to render reflects them.
func (r *Renderer) drain(events <-chan hub.Event) {
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return
			}
			r.Handle(e)
		default:
			return
		}
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

// frame renders the current page's rows for instant now, each exactly cols wide,
// applying the scroll marquee to any over-wide row (anchored at the page's start).
func (r *Renderer) frame(now time.Time) []string {
	page, anchor := r.currentPage(now)
	rc := renderCtx{st: &r.st, info: r.info, now: now, ip: r.ip}
	out := make([]string, r.rows)
	for i := 0; i < r.rows; i++ {
		text := expandRow(page, i, rc)
		off := 0
		if r.scroll > 0 && len([]rune(text)) > r.cols {
			off = int(now.Sub(anchor) / r.scroll)
		}
		out[i] = window(text, r.cols, off)
	}
	return out
}

// expandRow expands the template for row i of a page (or the empty string past the
// page's declared lines) against the render context. A blank or whitespace-only
// template expands to blanks, which window pads to a blank row.
func expandRow(page config.LCDPage, i int, rc renderCtx) string {
	tmpl := ""
	if i < len(page.Lines) {
		tmpl = page.Lines[i]
	}
	return expand(tmpl, rc)
}

// renderPage is the pure render contract (design §6): it expands one page against
// a live state snapshot into exactly rows lines of cols columns — token expansion
// plus truncation/padding, no scroll and no hardware. This is the geometry-
// agnostic core: a page with fewer lines than rows pads the remainder blank, a
// line wider than cols is truncated (the renderer's marquee is a timed view of the
// same truncation). It takes the instant now so time tokens are deterministic.
func renderPage(rows, cols int, page config.LCDPage, st *state, info Info, ip func() string, now time.Time) []string {
	rc := renderCtx{st: st, info: info, now: now, ip: ip}
	out := make([]string, rows)
	for i := 0; i < rows; i++ {
		out[i] = window(expandRow(page, i, rc), cols, 0)
	}
	return out
}

// currentPage resolves which page to show at instant now, advancing rotation and
// entering/leaving the activity interrupt as needed. It returns the page and the
// scroll anchor (the instant that page began showing).
func (r *Renderer) currentPage(now time.Time) (config.LCDPage, time.Time) {
	// Activity interrupt: hold the interrupt page while keyed and through the
	// linger after key-up, then resume rotation where it paused.
	if r.interrupt {
		if !r.st.active && now.Sub(r.endedAt) >= r.linger {
			r.interrupt = false
			r.pageStart = now // give the resumed page a fresh hold
		} else {
			return r.interruptPage(), r.interruptStart
		}
	}
	pages := r.rotationPages()
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

// rotationPages are the pages the normal cycle steps through: enabled and not
// marked interrupt. An interrupt page is excluded here because it shows only
// during activity (interruptPage), not in the idle rotation.
func (r *Renderer) rotationPages() []config.LCDPage {
	var ps []config.LCDPage
	for _, p := range r.cfg.Pages {
		if p.Enabled && !p.Interrupt {
			ps = append(ps, p)
		}
	}
	return ps
}

// interruptPage is the page shown during an activity interrupt: the first enabled
// page the operator marked interrupt=true, or the synthesized fallback when they
// defined none (so activity still surfaces on a stock or minimal config).
func (r *Renderer) interruptPage() config.LCDPage {
	for _, p := range r.cfg.Pages {
		if p.Enabled && p.Interrupt {
			return p
		}
	}
	return fallbackActivityPage
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

// NoopDevice is a headless LCDDevice: it accepts every call and does nothing. It
// runs when no physical panel is wired — or, in a later stage, when the I2C bus
// can't be opened — so the renderer runs harmlessly and the rest of the daemon
// is unaffected (design §7 failure posture).
type NoopDevice struct{}

func (NoopDevice) Init(rows, cols int) error            { return nil }
func (NoopDevice) WriteLine(row int, text string) error { return nil }
func (NoopDevice) Clear() error                         { return nil }
func (NoopDevice) Close() error                         { return nil }
