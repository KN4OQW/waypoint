package main

import (
	"sort"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
)

// playout.go schedules owner→member voice through peer.JitterBuffer (RFC-0016 §5,
// closing P2-3): a frame enqueues on arrival and emerges at its play-out slot on
// the 20 ms cadence, smoothing LAN jitter instead of emitting on arrival; a frame
// later than the deadline is dropped and counted, never played late. The scheduler
// is a pure function of the clock — schedule() and emitDue() both take `now`, and
// it holds no goroutine — so it is fake-clock testable; the session loop wires the
// real play timer and the real loopback emit around it.

// scheduledFrame is one constructed wire frame waiting for its play-out slot.
type scheduledFrame struct {
	playAt time.Time
	me     *memberEndpoint
	out    []byte
}

// playoutScheduler holds the current stream's jitter buffer and the queue of frames
// scheduled but not yet due. It keeps the per-peer configured depth so a future
// VPN-peer follow-up can widen the buffer without touching this code.
type playoutScheduler struct {
	buffer, deadline time.Duration

	jb         *peer.JitterBuffer
	haveStream bool
	streamID   uint32
	seq        int64
	queue      []scheduledFrame // ascending by playAt

	delivered, dropped int64 // cumulative across the session's streams
}

func newPlayoutScheduler(bufferMs, deadlineMs int) *playoutScheduler {
	buffer := time.Duration(bufferMs) * time.Millisecond
	if buffer <= 0 {
		buffer = peer.DefaultJitterBuffer
	}
	deadline := time.Duration(deadlineMs) * time.Millisecond
	if deadline <= 0 {
		deadline = peer.DefaultDeadline
	}
	return &playoutScheduler{buffer: buffer, deadline: deadline}
}

// schedule buffers a constructed frame for play-out at time `now`. A new stream
// (new Stream.ID) starts a fresh buffer, returning any stragglers from the previous
// stream to emit immediately in order (a transmission's tail is normally drained
// before the next starts; this keeps ordering correct if it is not). The frame is
// dropped here — never queued — when the buffer rules it too late (deadline).
func (p *playoutScheduler) schedule(streamID uint32, me *memberEndpoint, out []byte, now time.Time) (flush []scheduledFrame) {
	if !p.haveStream || p.streamID != streamID {
		flush = p.drain()
		p.rollStreamStats()
		// The play-out cadence is the DESTINATION mode's frame period — a YSF DN
		// frame carries 5 codewords (100 ms), DMR 3 (60 ms), NXDN 4 (80 ms) — not a
		// flat 20 ms, so the buffer's expected-arrival schedule matches the rate the
		// owner actually emits reframed frames at.
		p.jb = peer.NewJitterBuffer(frameCadence(me.fmode), p.buffer, p.deadline)
		p.haveStream, p.streamID, p.seq = true, streamID, 0
	} else {
		p.seq++
	}
	playAt, drop := p.jb.Accept(p.seq, now)
	if drop {
		return flush // late past the deadline: dropped + counted, never emitted late
	}
	p.insert(scheduledFrame{playAt: playAt, me: me, out: out})
	return flush
}

// emitDue returns the frames whose play-out slot has arrived by `now`, in order,
// removing them from the queue. Empty when nothing is due — a buffer underrun
// (arrival gap) emits nothing and simply recovers when the next frame's slot comes.
func (p *playoutScheduler) emitDue(now time.Time) []scheduledFrame {
	i := 0
	for i < len(p.queue) && !p.queue[i].playAt.After(now) {
		i++
	}
	due := append([]scheduledFrame(nil), p.queue[:i]...)
	p.queue = append([]scheduledFrame(nil), p.queue[i:]...)
	return due
}

// nextPlayAt is the earliest pending play-out time, to arm the session's play timer.
func (p *playoutScheduler) nextPlayAt() (time.Time, bool) {
	if len(p.queue) == 0 {
		return time.Time{}, false
	}
	return p.queue[0].playAt, true
}

func (p *playoutScheduler) insert(f scheduledFrame) {
	i := sort.Search(len(p.queue), func(i int) bool { return p.queue[i].playAt.After(f.playAt) })
	p.queue = append(p.queue, scheduledFrame{})
	copy(p.queue[i+1:], p.queue[i:])
	p.queue[i] = f
}

func (p *playoutScheduler) drain() []scheduledFrame {
	out := p.queue
	p.queue = nil
	return out
}

func (p *playoutScheduler) rollStreamStats() {
	if p.jb != nil {
		p.delivered += p.jb.Delivered()
		p.dropped += p.jb.Dropped()
	}
}

// Delivered / Dropped are the session-cumulative play-out counts (current stream
// included), for the session-end quality log.
func (p *playoutScheduler) Delivered() int64 {
	if p.jb != nil {
		return p.delivered + p.jb.Delivered()
	}
	return p.delivered
}
func (p *playoutScheduler) Dropped() int64 {
	if p.jb != nil {
		return p.dropped + p.jb.Dropped()
	}
	return p.dropped
}

// frameCadence is the play-out period for a destination mode: its codewords-per-
// frame times the 20 ms AMBE+2 codeword period (YSF 100 ms, DMR 60 ms, NXDN 80 ms).
func frameCadence(fm frames.Mode) time.Duration {
	n := frames.CodewordsPerFrame(fm)
	if n <= 0 {
		n = 1
	}
	return time.Duration(n) * peer.FrameCadence
}

// armTimer resets t to fire at `at` (immediately if past), or stops it when there
// is nothing pending — the standard drain-before-reset dance so a stale tick never
// fires spuriously.
func armTimer(t *time.Timer, at time.Time, pending bool) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
	if !pending {
		return
	}
	d := time.Until(at)
	if d < 0 {
		d = 0
	}
	t.Reset(d)
}
