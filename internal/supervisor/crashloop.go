package supervisor

import (
	"sort"
	"sync"
	"time"
)

// crashloop.go notices a daemon that systemd is restarting over and over.
//
// This is a different question from the one the rest of this package answers, and
// it needs a different source. MaxRestarts/RestartWindow bound the restarts
// WAYPOINT performs; systemd's own Restart= loop is invisible to them. On the
// bench a DMRGateway that exited on every DNS failure was restarted 4,810 times
// by systemd while the unit read active, the MQTT gateway topic read running, and
// waypointd's restart budget was never touched — because waypointd had not
// restarted anything.
//
// The evidence is systemd's NRestarts counter, sampled by the liveness probe that
// already walks these units. It counts automatic restarts and resets whenever the
// unit is restarted outright, so a decrease means "something restarted it", not
// "time went backwards" — and since that is itself a restart, it is counted rather
// than treated as a clean slate. See Observe for why that matters here
// specifically.
//
// Why a rate and not a total: a node that has been up for a year has legitimately
// restarted a gateway a few times. Nine restarts spread over that year is health;
// nine in ten minutes is a loop. Only the second one is worth waking anybody for.

const (
	// DefaultCrashLoopThreshold is how many automatic restarts inside the window
	// constitute a loop. Five is comfortably above the ordinary — a gateway
	// bouncing on a config apply, a master dropping and being chased — and far
	// below the bench incident, which ran at roughly six restarts a minute.
	DefaultCrashLoopThreshold = 5
	// DefaultCrashLoopWindow is how far back the count reaches. Ten minutes is
	// long enough that a slow loop (one restart a minute) still trips it, and
	// short enough that a fixed daemon stops being reported within one window.
	DefaultCrashLoopWindow = 10 * time.Minute
)

// CrashLoop is what a watcher reports about one unit.
type CrashLoop struct {
	// Unit is the systemd unit, as the caller named it.
	Unit string
	// Restarts is how many automatic restarts landed inside Window.
	Restarts int
	// Window is the span Restarts was counted over.
	Window time.Duration
	// Since is the timestamp of the oldest restart still counted, so a message can
	// say when the thrashing started rather than only how fast it is going.
	Since time.Time
}

// RestartWatch turns a sequence of NRestarts samples into a crash-loop verdict.
// The zero value is usable and uses the defaults.
type RestartWatch struct {
	// Threshold and Window override the defaults when non-zero.
	Threshold int
	Window    time.Duration

	mu    sync.Mutex
	last  map[string]int         // unit → last NRestarts seen
	times map[string][]time.Time // unit → when each counted restart was observed
}

func (w *RestartWatch) threshold() int {
	if w.Threshold > 0 {
		return w.Threshold
	}
	return DefaultCrashLoopThreshold
}

func (w *RestartWatch) window() time.Duration {
	if w.Window > 0 {
		return w.Window
	}
	return DefaultCrashLoopWindow
}

// Observe records one NRestarts reading for a unit and reports whether that unit
// is currently looping.
//
// The first sample for a unit establishes a baseline and never reports a loop:
// waypointd may have started long after the restarts it is reading about, and
// counting history it did not witness would fire on every daemon start.
//
// A counter that has gone DOWN means the unit was restarted outright, which resets
// systemd's count — and that restart is itself evidence, so the history is KEPT and
// the reset counts as one more. The alternative was tried and is worse: this
// supervisor restarts a gateway that has lost its link, and on the bench that reset
// NRestarts from 23 to 1 and wiped the loop verdict. A daemon thrashing badly
// enough for waypointd to intervene would have laundered its own evidence through
// the intervention, every time, and reported healthy throughout.
//
// The cost is that a genuine operator fix leaves the finding standing until the
// window rolls off. That is the right way round: it was thrashing a minute ago,
// and saying so for another few minutes is honest where forgetting is not.
func (w *RestartWatch) Observe(unit string, nrestarts int, now time.Time) (CrashLoop, bool) {
	if unit == "" || nrestarts < 0 {
		return CrashLoop{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.last == nil {
		w.last, w.times = map[string]int{}, map[string][]time.Time{}
	}

	prev, seen := w.last[unit]
	w.last[unit] = nrestarts
	switch {
	case !seen:
		return CrashLoop{}, false // baseline only
	case nrestarts < prev:
		// The counter reset, so the unit was restarted: count that restart, plus
		// however many it has already racked up since.
		for i := 0; i <= nrestarts; i++ {
			w.times[unit] = append(w.times[unit], now)
		}
	case nrestarts > prev:
		// One timestamp per restart, so a probe that missed a few cycles still
		// counts what happened between the samples rather than collapsing it to one.
		for i := 0; i < nrestarts-prev; i++ {
			w.times[unit] = append(w.times[unit], now)
		}
	}

	cutoff := now.Add(-w.window())
	kept := w.times[unit][:0]
	for _, t := range w.times[unit] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(w.times, unit)
		return CrashLoop{}, false
	}
	w.times[unit] = kept

	if len(kept) < w.threshold() {
		return CrashLoop{}, false
	}
	return CrashLoop{
		Unit:     unit,
		Restarts: len(kept),
		Window:   w.window(),
		Since:    kept[0],
	}, true
}

// Forget drops a unit's history — for a unit the config no longer renders, so a
// removed gateway does not keep a stale verdict alive.
func (w *RestartWatch) Forget(unit string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.last, unit)
	delete(w.times, unit)
}

// Looping lists every unit currently over the threshold, oldest thrash first, so
// a caller reporting several has a stable order.
func (w *RestartWatch) Looping(now time.Time) []CrashLoop {
	w.mu.Lock()
	defer w.mu.Unlock()
	cutoff := now.Add(-w.window())
	var out []CrashLoop
	for unit, ts := range w.times {
		n := 0
		var since time.Time
		for _, t := range ts {
			if t.After(cutoff) {
				if n == 0 {
					since = t
				}
				n++
			}
		}
		if n >= w.threshold() {
			out = append(out, CrashLoop{Unit: unit, Restarts: n, Window: w.window(), Since: since})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Since.Equal(out[j].Since) {
			return out[i].Unit < out[j].Unit
		}
		return out[i].Since.Before(out[j].Since)
	})
	return out
}
