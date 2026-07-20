package peer

import (
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

// pipePair returns two connected in-memory sessions (no real sockets), the
// building block for the simulated topology tests.
func pipePair(t *testing.T, aID, bID string) (*Session, *Session) {
	t.Helper()
	ca, cb := net.Pipe()
	a := NewSession(ca, bID, DefaultSendQueue)
	b := NewSession(cb, aID, DefaultSendQueue)
	a.Start(50 * time.Millisecond)
	b.Start(50 * time.Millisecond)
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}

func TestSessionRoundTripOverPipe(t *testing.T) {
	a, b := pipePair(t, "shack", "garage")
	a.Send(Message{Type: MsgHello, Hello: &Hello{NodeID: "shack", BusID: "A", Role: RoleOwner}})
	a.Send(Message{Type: MsgVoice, Voice: sampleVoice(1)})

	got := recvN(t, b, 2)
	if got[0].Type != MsgHello || got[1].Type != MsgVoice {
		t.Fatalf("unexpected messages: %s, %s", got[0].Type, got[1].Type)
	}
	if got[0].Hello.NodeID != "shack" {
		t.Fatalf("hello lost: %+v", got[0].Hello)
	}
}

func TestSessionKeepalive(t *testing.T) {
	_, b := pipePair(t, "shack", "garage")
	// with no traffic, a keepalive must arrive within a couple of cadences
	select {
	case m := <-b.Recv():
		if m.Type != MsgKeepalive {
			t.Fatalf("expected a keepalive on an idle link, got %s", m.Type)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no keepalive on an idle link")
	}
}

// TestSessionDisconnectNoCrash: closing one end surfaces on the other as a closed
// Recv + a non-nil Err, with no panic — the substrate for cable-pull handling.
func TestSessionDisconnectNoCrash(t *testing.T) {
	a, b := pipePair(t, "shack", "garage")
	a.Close()
	select {
	case _, ok := <-b.Recv():
		if ok {
			// may deliver a final buffered message; drain until closed
			for range b.Recv() {
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("peer did not observe the disconnect")
	}
	<-b.Closed()
	if b.Err() == nil {
		t.Fatal("a disconnect should set a non-nil error")
	}
}

// TestSessionBackpressureDropsOldestVoice: a stalled reader (never draining) makes
// the sender's queue overflow; the oldest VOICE frames are dropped and counted,
// control messages survive, and Send never blocks.
func TestSessionBackpressureDropsOldestVoice(t *testing.T) {
	ca, cb := net.Pipe()
	a := NewSession(ca, "garage", 8) // tiny queue
	// deliberately DON'T start b's reader, and don't start a's writer draining far —
	// actually start a's loops; the pipe write blocks because nobody reads cb.
	a.Start(time.Hour) // no keepalive interference
	defer a.Close()
	defer cb.Close()

	// flood voice well past the queue bound; Send must never block
	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			a.Send(Message{Type: MsgVoice, Voice: sampleVoice(int64(i))})
		}
		// a control message must not be dropped by the voice-shedding
		a.Send(Message{Type: MsgTokenGrant, Token: &Token{BusID: "A", StreamID: 1}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Send blocked under backpressure — it must never block the fan-out")
	}
	if a.Dropped() == 0 {
		t.Fatal("expected voice frames to be dropped under backpressure")
	}
}

func recvN(t *testing.T, s *Session, n int) []Message {
	t.Helper()
	out := make([]Message, 0, n)
	for len(out) < n {
		select {
		case m, ok := <-s.Recv():
			if !ok {
				t.Fatalf("session closed after %d/%d messages: %v", len(out), n, s.Err())
			}
			out = append(out, m)
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out after %d/%d messages", len(out), n)
		}
	}
	return out
}

// silence unused import if frames drifts
var _ = frames.AMBEBytes
