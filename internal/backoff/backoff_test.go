package backoff

import (
	"testing"
	"time"
)

// The schedule doubles from Initial and stops at Max — the behaviour the peering
// dialer relied on before this package existed.
func TestSchedule(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: 30 * time.Second}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got := b.Next(); got != w*time.Second {
			t.Errorf("attempt %d: got %v, want %v", i, got, w*time.Second)
		}
	}
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Errorf("after Reset: got %v, want 1s", got)
	}
}

// A zero or absurd Initial must not produce a zero or negative delay — that would
// be a hot loop, which is the one outcome a backoff exists to prevent.
func TestScheduleDegenerateInitial(t *testing.T) {
	b := Backoff{Initial: 0, Max: 30 * time.Second}
	for i := 0; i < 5; i++ {
		if got := b.Next(); got != 30*time.Second {
			t.Fatalf("zero Initial must fall back to Max, got %v", got)
		}
	}
	// A shift that overflows must also land on Max rather than wrapping negative.
	b = Backoff{Initial: time.Hour, Max: 24 * time.Hour}
	for i := 0; i < 80; i++ {
		if got := b.Next(); got <= 0 || got > 24*time.Hour {
			t.Fatalf("attempt %d produced %v, outside (0, Max]", i, got)
		}
	}
}

// Jitter keeps every delay inside [d/2, d) and leaves the schedule's progression
// alone, so backing off still means backing off.
func TestJitterBounds(t *testing.T) {
	for _, r := range []float64{0, 0.25, 0.5, 0.999} {
		b := Backoff{Initial: time.Second, Max: 32 * time.Second, Rand: func() float64 { return r }}
		plain := Backoff{Initial: time.Second, Max: 32 * time.Second}
		for i := 0; i < 8; i++ {
			d := plain.Next()
			got := b.Next()
			if got < d/2 || got >= d {
				t.Errorf("rand=%v attempt %d: %v outside [%v, %v)", r, i, got, d/2, d)
			}
			if b.Attempt() != plain.Attempt() {
				t.Fatalf("jitter changed the schedule's progression: %d vs %d", b.Attempt(), plain.Attempt())
			}
		}
	}
}

// Two schedules that failed at the same instant must not retry at the same
// instant — the whole point of jitter.
func TestJitterSpreads(t *testing.T) {
	seen := map[time.Duration]bool{}
	for i := 0; i < 10; i++ {
		r := float64(i) / 10
		b := Backoff{Initial: time.Second, Max: time.Minute, Rand: func() float64 { return r }}
		b.Next()
		b.Next()
		seen[b.Next()] = true
	}
	if len(seen) < 5 {
		t.Errorf("ten nodes produced only %d distinct delays; the herd is not spread", len(seen))
	}
}

func TestAttemptTracks(t *testing.T) {
	b := Backoff{Initial: time.Second, Max: 8 * time.Second}
	if b.Attempt() != 0 {
		t.Fatal("a fresh schedule has made no attempts")
	}
	b.Next()
	b.Next()
	if b.Attempt() != 2 {
		t.Errorf("Attempt = %d, want 2", b.Attempt())
	}
	b.Reset()
	if b.Attempt() != 0 {
		t.Errorf("Reset should zero the attempt count, got %d", b.Attempt())
	}
}
