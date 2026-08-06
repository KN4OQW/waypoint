package supervisor

import (
	"testing"
	"time"
)

var ct0 = time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)

const testUnit = "waypoint-dmrgateway.service"

// The threshold, from below it to past it, and out the far side as the window
// expires. Table-driven over one watcher so each step sees the state the previous
// one left, which is the only way the window's edges are actually exercised.
func TestRestartWatchThreshold(t *testing.T) {
	w := &RestartWatch{Threshold: 5, Window: 10 * time.Minute}

	for _, tc := range []struct {
		name      string
		at        time.Duration
		nrestarts int
		looping   bool
		count     int
	}{
		// The first sample is a baseline: waypointd may have started long after
		// these restarts happened, and reporting history it did not witness would
		// fire on every daemon start.
		{"first sample is a baseline", 0, 100, false, 0},
		{"one restart", 1 * time.Minute, 101, false, 0},
		{"two", 2 * time.Minute, 102, false, 0},
		{"three", 3 * time.Minute, 103, false, 0},
		{"four is still below", 4 * time.Minute, 104, false, 0},
		{"five trips it", 5 * time.Minute, 105, true, 5},
		{"and stays tripped while they are in the window", 6 * time.Minute, 106, true, 6},
		// The first restart was at +1m and the window is 10m, so at +11m30s it has
		// aged out and the count drops back below the threshold.
		{"the oldest ages out", 11*time.Minute + 30*time.Second, 106, true, 5},
		{"and the rest follow", 15 * time.Minute, 106, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, looping := w.Observe(testUnit, tc.nrestarts, ct0.Add(tc.at))
			if looping != tc.looping {
				t.Fatalf("looping = %v, want %v (restarts=%d)", looping, tc.looping, got.Restarts)
			}
			if looping && got.Restarts != tc.count {
				t.Errorf("restarts = %d, want %d", got.Restarts, tc.count)
			}
		})
	}
}

// The bench incident: systemd restarting a daemon roughly every ten seconds for
// hours, while the unit read active and waypointd's own restart budget was never
// touched because waypointd had not restarted anything.
func TestRestartWatch4810RestartsAllGreen(t *testing.T) {
	w := &RestartWatch{}
	// Baseline, then a probe every 30s watching the counter climb by ~3 each time.
	w.Observe(testUnit, 0, ct0)
	var loop CrashLoop
	var looping bool
	for i := 1; i <= 6; i++ {
		loop, looping = w.Observe(testUnit, i*3, ct0.Add(time.Duration(i)*30*time.Second))
	}
	if !looping {
		t.Fatal("a daemon restarting every ten seconds was not reported as looping")
	}
	if loop.Unit != testUnit {
		t.Errorf("unit = %q, want %q", loop.Unit, testUnit)
	}
	if loop.Restarts < DefaultCrashLoopThreshold {
		t.Errorf("restarts = %d, want at least the threshold %d", loop.Restarts, DefaultCrashLoopThreshold)
	}
	if loop.Since.IsZero() {
		t.Error("Since is zero; a report should say when the thrashing started")
	}
	// It must trip well inside the incident's duration, not after hours of it.
	if el := loop.Since.Sub(ct0); el > 5*time.Minute {
		t.Errorf("took %s of history to notice; the loop was visible within a minute", el)
	}
}

func TestRestartWatchEdges(t *testing.T) {
	// A counter that goes down means somebody restarted the unit by hand, which
	// resets systemd's count. That is an intervention, and treating it as more
	// thrashing would make the operator's own fix look like the fault.
	t.Run("a manual restart resets rather than accumulates", func(t *testing.T) {
		w := &RestartWatch{Threshold: 3, Window: time.Hour}
		w.Observe(testUnit, 10, ct0)
		w.Observe(testUnit, 12, ct0.Add(time.Minute))
		if _, looping := w.Observe(testUnit, 0, ct0.Add(2*time.Minute)); looping {
			t.Error("a counter reset was reported as a loop")
		}
		// And the baseline restarts from there: two more restarts is still below 3.
		if _, looping := w.Observe(testUnit, 2, ct0.Add(3*time.Minute)); looping {
			t.Error("history survived a manual restart")
		}
	})

	// A probe that missed cycles must count what happened between samples, not
	// collapse a jump of twenty into a single restart.
	t.Run("a jump counts every restart in it", func(t *testing.T) {
		w := &RestartWatch{Threshold: 5, Window: time.Hour}
		w.Observe(testUnit, 0, ct0)
		got, looping := w.Observe(testUnit, 20, ct0.Add(time.Minute))
		if !looping {
			t.Fatal("a jump of twenty restarts was not reported")
		}
		if got.Restarts != 20 {
			t.Errorf("restarts = %d, want 20", got.Restarts)
		}
	})

	// A steady unit never reports, however long it runs.
	t.Run("a healthy unit is silent", func(t *testing.T) {
		w := &RestartWatch{}
		for i := 0; i < 200; i++ {
			if _, looping := w.Observe(testUnit, 7, ct0.Add(time.Duration(i)*time.Minute)); looping {
				t.Fatalf("a unit at a constant restart count reported a loop at sample %d", i)
			}
		}
	})

	t.Run("units are tracked independently", func(t *testing.T) {
		w := &RestartWatch{Threshold: 3, Window: time.Hour}
		other := "waypoint-ysfgateway.service"
		w.Observe(testUnit, 0, ct0)
		w.Observe(other, 0, ct0)
		for i := 1; i <= 3; i++ {
			w.Observe(testUnit, i, ct0.Add(time.Duration(i)*time.Minute))
		}
		if _, looping := w.Observe(other, 0, ct0.Add(4*time.Minute)); looping {
			t.Error("a quiet unit was reported because a different one was looping")
		}
		all := w.Looping(ct0.Add(4 * time.Minute))
		if len(all) != 1 || all[0].Unit != testUnit {
			t.Errorf("Looping() = %v, want only %s", all, testUnit)
		}
	})

	t.Run("Forget drops a removed unit", func(t *testing.T) {
		w := &RestartWatch{Threshold: 2, Window: time.Hour}
		w.Observe(testUnit, 0, ct0)
		w.Observe(testUnit, 5, ct0.Add(time.Minute))
		if len(w.Looping(ct0.Add(time.Minute))) != 1 {
			t.Fatal("setup: expected a looping unit")
		}
		w.Forget(testUnit)
		if got := w.Looping(ct0.Add(time.Minute)); len(got) != 0 {
			t.Errorf("Looping() = %v after Forget, want none", got)
		}
	})
}
