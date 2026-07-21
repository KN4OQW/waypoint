package main

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
)

// playout_test.go drives the play-out scheduler on a fake clock (no goroutine, no
// sockets): jittered arrivals emerge on the destination mode's frame cadence (YSF
// = 100 ms here), a frame past the deadline is dropped rather than played late, and
// a buffer underrun emits nothing and recovers (RFC-0016 §5, P2-3).

var base = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(ms int) time.Time   { return base.Add(time.Duration(ms) * time.Millisecond) }
func offMs(t time.Time) int { return int(t.Sub(base) / time.Millisecond) }

// ysfEP is a member endpoint for the YSF destination (5 codewords ⇒ 100 ms cadence).
func ysfEP() *memberEndpoint {
	return &memberEndpoint{mode: config.ModeYSF, fmode: frames.ModeYSF}
}

func drainSchedule(p *playoutScheduler) (offsets []int, ids []byte) {
	for {
		next, ok := p.nextPlayAt()
		if !ok {
			return
		}
		for _, f := range p.emitDue(next) {
			offsets = append(offsets, offMs(next))
			ids = append(ids, f.out[0])
		}
	}
}

// TestPlayoutJitteredArrivalsEmergeOnCadence: five YSF frames (100 ms apart at the
// source) arrive jittered within the buffer; they play out at exactly 100 ms
// spacing, in order — the buffer absorbs the jitter.
func TestPlayoutJitteredArrivalsEmergeOnCadence(t *testing.T) {
	p := newPlayoutScheduler(40, 60) // 40 ms buffer, 60 ms deadline
	me := ysfEP()
	// expected slots 0,100,200,300,400; arrivals jittered around them, within buffer.
	arrivals := []int{5, 95, 210, 305, 380}
	for i, a := range arrivals {
		if flush := p.schedule(1, me, []byte{byte(i)}, at(a)); len(flush) != 0 {
			t.Fatalf("single stream should never flush stragglers, got %d", len(flush))
		}
	}
	offsets, ids := drainSchedule(p)
	if string(ids) != string([]byte{0, 1, 2, 3, 4}) {
		t.Fatalf("play-out order = %v, want in-order (jitter must not reorder)", ids)
	}
	if offsets[0] != 45 { // anchors on first arrival(5) + buffer(40)
		t.Fatalf("first frame played at %d ms, want 45", offsets[0])
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i]-offsets[i-1] != 100 {
			t.Fatalf("inter-frame interval %d = %d ms, want 100 (jitter not smoothed): %v", i, offsets[i]-offsets[i-1], offsets)
		}
	}
	if p.Dropped() != 0 {
		t.Fatalf("no frame should drop within the buffer, dropped %d", p.Dropped())
	}
}

// TestPlayoutLateFrameDropped: a frame arriving later than the deadline is dropped
// and counted — never emitted late.
func TestPlayoutLateFrameDropped(t *testing.T) {
	p := newPlayoutScheduler(40, 60)
	me := ysfEP()
	// frame 2 (expected 200) arrives at 280 — 80 ms late, past the 60 ms deadline.
	arr := map[byte]int{0: 0, 1: 100, 2: 280, 3: 300}
	for _, id := range []byte{0, 1, 2, 3} {
		p.schedule(1, me, []byte{id}, at(arr[id]))
	}
	_, ids := drainSchedule(p)
	for _, id := range ids {
		if id == 2 {
			t.Fatal("the late frame (id 2) must be dropped, not played late")
		}
	}
	if string(ids) != string([]byte{0, 1, 3}) {
		t.Fatalf("emitted %v, want [0 1 3]", ids)
	}
	if p.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", p.Dropped())
	}
}

// TestPlayoutUnderrunEmitsNothingAndRecovers: after a stall the play timer finds
// nothing due (silence, not a replayed frame) and recovers when the next in-time
// frame arrives.
func TestPlayoutUnderrunEmitsNothingAndRecovers(t *testing.T) {
	p := newPlayoutScheduler(40, 60)
	me := ysfEP()
	p.schedule(1, me, []byte{0}, at(0))   // slot 40
	p.schedule(1, me, []byte{1}, at(100)) // slot 140
	if got := p.emitDue(at(40)); len(got) != 1 || got[0].out[0] != 0 {
		t.Fatalf("frame 0 at 40 ms, got %v", got)
	}
	if got := p.emitDue(at(140)); len(got) != 1 || got[0].out[0] != 1 {
		t.Fatalf("frame 1 at 140 ms, got %v", got)
	}
	// Underrun between 140 and the next frame's slot: nothing due.
	if got := p.emitDue(at(180)); len(got) != 0 {
		t.Fatalf("underrun must emit nothing, got %v", got)
	}
	if got := p.emitDue(at(220)); len(got) != 0 {
		t.Fatalf("underrun must emit nothing, got %v", got)
	}
	// Recovery: frame 2 (expected 200) arrives at 250 — 50 ms late but within deadline.
	p.schedule(1, me, []byte{2}, at(250))
	if got := p.emitDue(at(250)); len(got) != 1 || got[0].out[0] != 2 {
		t.Fatalf("stream should recover and play frame 2 at 250 ms, got %v", got)
	}
	if p.Dropped() != 0 {
		t.Fatalf("recovered frame was within deadline; dropped %d", p.Dropped())
	}
}

// TestPlayoutNewStreamResetsBuffer: a second transmission (new Stream.ID) starts a
// fresh buffer so its first frame is not judged against the first stream's clock.
func TestPlayoutNewStreamResetsBuffer(t *testing.T) {
	p := newPlayoutScheduler(40, 60)
	me := ysfEP()
	p.schedule(7, me, []byte{0}, at(0))
	drainSchedule(p)
	if flush := p.schedule(8, me, []byte{0}, at(30000)); len(flush) != 0 {
		_ = flush
	}
	_, ids := drainSchedule(p)
	if len(ids) != 1 || ids[0] != 0 {
		t.Fatalf("new stream's first frame should play, got %v", ids)
	}
	if p.Dropped() != 0 {
		t.Fatalf("new stream must not inherit lateness; dropped %d", p.Dropped())
	}
}
