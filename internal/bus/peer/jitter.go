package peer

import "time"

// jitter.go is the play-out jitter buffer (RFC-0016 §5). Peered voice is
// play-out-scheduled at the receiver with a small buffer to smooth LAN jitter;
// a frame arriving too late for its slot is DROPPED and counted, never queued
// (voice, not file transfer). The buffer is a pure function of (sequence,
// arrival-time); it is driven with an explicit clock and holds no goroutine.
//
// Defaults from RFC-0016 §5, derived from the spike's measured transport tail:
//   jitter buffer 40 ms (2 frames), per-frame deadline 60 ms.

const (
	// FrameCadence is one voice frame's nominal period (20 ms — the AMBE+2 family's
	// frame rate the whole bus runs at).
	FrameCadence = 20 * time.Millisecond
	// DefaultJitterBuffer / DefaultDeadline are the RFC-0016 §5 defaults.
	DefaultJitterBuffer = 40 * time.Millisecond
	DefaultDeadline     = 60 * time.Millisecond
)

// JitterBuffer schedules one stream's play-out. The caller passes a per-stream
// monotonically increasing frame index (the session derives it), so sequence
// wraparound and reordering are the caller's concern, not the buffer's.
type JitterBuffer struct {
	cadence  time.Duration
	buffer   time.Duration
	deadline time.Duration

	started   bool
	baseSeq   int64
	baseTime  time.Time // arrival time of the first frame seen for this stream
	dropped   int64
	delivered int64
}

// NewJitterBuffer builds a buffer with the given depths (pass DefaultJitterBuffer
// / DefaultDeadline for the RFC defaults). cadence is FrameCadence.
func NewJitterBuffer(cadence, buffer, deadline time.Duration) *JitterBuffer {
	return &JitterBuffer{cadence: cadence, buffer: buffer, deadline: deadline}
}

// Dropped / Delivered are the running counts.
func (j *JitterBuffer) Dropped() int64   { return j.dropped }
func (j *JitterBuffer) Delivered() int64 { return j.delivered }

// Accept schedules the frame at index seq arriving at time `arrival`. It returns
// playAt (when to play the frame out) and drop=true when the frame is too late —
// its lateness relative to when it SHOULD have arrived exceeds the deadline. A
// dropped frame is counted and never scheduled (never queued). An early frame
// (arriving ahead of its slot) is accepted and simply played at its slot, which is
// what the buffer depth absorbs.
func (j *JitterBuffer) Accept(seq int64, arrival time.Time) (playAt time.Time, drop bool) {
	if !j.started {
		j.started, j.baseSeq, j.baseTime = true, seq, arrival
	}
	// When this frame should have arrived if the stream were jitter-free.
	expected := j.baseTime.Add(time.Duration(seq-j.baseSeq) * j.cadence)
	lateness := arrival.Sub(expected)
	if lateness > j.deadline {
		j.dropped++
		return time.Time{}, true
	}
	// Its play-out slot is the expected arrival delayed by the buffer depth; a frame
	// arriving after its own slot is played immediately (max), never held back.
	slot := expected.Add(j.buffer)
	if arrival.After(slot) {
		slot = arrival
	}
	j.delivered++
	return slot, false
}
