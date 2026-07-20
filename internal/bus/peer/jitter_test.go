package peer

import (
	"testing"
	"time"
)

// TestJitterBufferArrivalPatterns drives the RFC-0016 §5 defaults (40 ms buffer,
// 60 ms deadline, 20 ms cadence) against synthetic arrival patterns using a fake
// clock. Late-past-deadline frames are dropped; everything within the buffer is
// delivered.
func TestJitterBufferArrivalPatterns(t *testing.T) {
	cad, buf, dl := FrameCadence, DefaultJitterBuffer, DefaultDeadline

	type arrival struct {
		seq  int64
		atMs int // arrival time in ms from stream start
		drop bool
	}
	cases := []struct {
		name     string
		arrivals []arrival
	}{
		{
			name: "in-order, on cadence, all delivered",
			arrivals: []arrival{
				{0, 0, false}, {1, 20, false}, {2, 40, false}, {3, 60, false}, {4, 80, false},
			},
		},
		{
			name: "jittered within the buffer, all delivered",
			arrivals: []arrival{
				{0, 0, false}, {1, 28, false}, {2, 35, false}, {3, 66, false}, {4, 79, false},
			},
		},
		{
			name: "bursty (several arrive early together), all delivered",
			arrivals: []arrival{
				{0, 0, false}, {1, 5, false}, {2, 6, false}, {3, 7, false}, {4, 80, false},
			},
		},
		{
			name: "one frame 80ms late past its deadline is dropped",
			arrivals: []arrival{
				// seq 2 is expected at 40ms; arriving at 120ms is 80ms late (> 60ms deadline)
				{0, 0, false}, {1, 20, false}, {2, 120, true}, {3, 60, false},
			},
		},
	}

	base := time.Unix(1_700_000_000, 0)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			jb := NewJitterBuffer(cad, buf, dl)
			for _, a := range c.arrivals {
				_, drop := jb.Accept(a.seq, base.Add(time.Duration(a.atMs)*time.Millisecond))
				if drop != a.drop {
					t.Fatalf("seq %d @%dms: drop=%v, want %v", a.seq, a.atMs, drop, a.drop)
				}
			}
		})
	}
}

func TestJitterBufferPlayoutIsBuffered(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	jb := NewJitterBuffer(FrameCadence, DefaultJitterBuffer, DefaultDeadline)
	// first frame arrives at t=0; its play-out slot is delayed by the buffer depth
	playAt, drop := jb.Accept(0, base)
	if drop {
		t.Fatal("first frame should not drop")
	}
	if got := playAt.Sub(base); got != DefaultJitterBuffer {
		t.Fatalf("first frame play-out should be delayed by the buffer (%v), got %v", DefaultJitterBuffer, got)
	}
}

func TestJitterBufferCounts(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	jb := NewJitterBuffer(FrameCadence, DefaultJitterBuffer, DefaultDeadline)
	jb.Accept(0, base)
	jb.Accept(1, base.Add(200*time.Millisecond)) // hugely late -> drop
	jb.Accept(2, base.Add(40*time.Millisecond))
	if jb.Delivered() != 2 || jb.Dropped() != 1 {
		t.Fatalf("counts wrong: delivered=%d dropped=%d", jb.Delivered(), jb.Dropped())
	}
}
