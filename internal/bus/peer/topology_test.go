package peer

import (
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
)

// owner is a minimal simulated bus-owner node: it wires a Session to a member to
// the pure token Server + loop checks, exactly as the daemon will, but over an
// in-memory pipe. It records the voice frames it accepted into the fan-out and the
// destinations it would emit toward, so a test can assert loop prevention. All
// shared state is behind mu so the owner goroutine and the test never race.
type owner struct {
	nodeID string
	busID  string
	sess   *Session
	fanout []string // the owner's other attachments to fan a frame to

	mu       sync.Mutex
	srv      *Server
	accepted []Voice  // frames that passed the loop check
	emitted  []string // "dstNode/dstAttachment" the owner would emit toward
	done     chan struct{}
}

func newOwner(nodeID, busID string, sess *Session, hang time.Duration, fanout []string) *owner {
	return &owner{nodeID: nodeID, busID: busID, srv: NewServer(hang), sess: sess, fanout: fanout, done: make(chan struct{})}
}

// run drives the owner until its session closes, then closes done.
func (o *owner) run(now func() time.Time) {
	defer close(o.done)
	for {
		select {
		case m, ok := <-o.sess.Recv():
			if !ok {
				return
			}
			o.handle(m, now())
		case <-o.sess.Closed():
			return
		}
	}
}

func (o *owner) handle(m Message, t time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	switch m.Type {
	case MsgTokenRequest:
		if o.srv.RequestFromMember(o.sess.Peer(), t) == Granted {
			o.sess.Send(Message{Type: MsgTokenGrant, Token: m.Token})
		} else {
			o.sess.Send(Message{Type: MsgTokenDeny, Token: m.Token})
		}
	case MsgTokenRelease:
		o.srv.ReleaseFromMember(o.sess.Peer())
	case MsgVoice:
		if ok, _ := AcceptInbound(m.Voice.Env, o.nodeID, DefaultMaxHops); !ok {
			return // loop: dropped
		}
		o.srv.MemberActivity(o.sess.Peer(), t)
		o.accepted = append(o.accepted, *m.Voice)
		for _, att := range o.fanout {
			if ShouldEmitTo(m.Voice.Env, o.nodeID, att) {
				o.emitted = append(o.emitted, o.nodeID+"/"+att)
			}
		}
	}
}

func (o *owner) holder() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	h, _ := o.srv.Holder()
	return h
}
func (o *owner) acceptedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.accepted)
}
func (o *owner) emittedList() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.emitted...)
}
func (o *owner) disconnect(node string, t time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.srv.MemberDisconnected(node, t)
}
func (o *owner) tick(t time.Time) (bool, string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.srv.Tick(t)
}

// TestTopologyTwoNodeGrantStreamAndLoop is the simulated two-node acceptance: a
// member keys up, requests + is granted the token, streams voice that the owner
// accepts and fans out — never back to the member's origin attachment.
func TestTopologyTwoNodeGrantStreamAndLoop(t *testing.T) {
	clock := t0
	now := func() time.Time { return clock }

	// shack owns bus A with a local DMR attachment; garage contributes YSF.
	ownerSess, memberSess := pipePair(t, "shack", "garage")
	o := newOwner("shack", "A", ownerSess, 2*time.Second, []string{"dmr", "ysf"})
	go o.run(now)

	member := NewClient(2 * time.Second)

	// member key-up -> request
	if !member.LocalKeyup(0x77, clock) {
		t.Fatal("key-up should request the token")
	}
	memberSess.Send(Message{Type: MsgTokenRequest, Token: &Token{BusID: "A", StreamID: 0x77}})

	// await grant
	waitFor(t, memberSess, func(m Message) bool {
		if m.Type == MsgTokenGrant {
			member.RxGrant(m.Token.StreamID, clock)
			return true
		}
		return false
	})
	if !member.CanStream() {
		t.Fatal("member should hold the token after grant")
	}

	// stream a few voice frames with the cross-peer envelope
	env := NewEnvelope("garage", "ysf", "A")
	for i := 0; i < 3; i++ {
		memberSess.Send(Message{Type: MsgVoice, Voice: &Voice{Env: Forward(env), Frame: frames.Frame{
			Mode: frames.ModeYSF, Kind: frames.KindVoice, SrcCallsign: "KN4OQW",
			Stream: frames.Stream{ID: 0x77, Seq: byte(i)}, AMBE: [][]byte{make([]byte, frames.AMBEBytes)},
		}}})
	}

	// give the owner goroutine time to process (real time; the sim clock stays fixed)
	waitUntil(t, func() bool { return o.acceptedCount() >= 3 })

	// loop prevention: the owner never emitted toward garage/ysf (the origin)
	emitted := o.emittedList()
	for _, e := range emitted {
		if e == "garage/ysf" {
			t.Fatal("owner emitted back to the frame's origin attachment")
		}
	}
	// it did fan out to its local DMR
	sawDMR := false
	for _, e := range emitted {
		if e == "shack/dmr" {
			sawDMR = true
		}
	}
	if !sawDMR {
		t.Fatal("owner should fan the member's voice to its local DMR")
	}
	if h := o.holder(); h != "garage" {
		t.Fatalf("garage should hold the owner token, got %q", h)
	}
}

// TestTopologyCablePullNoCrashLoop is issue #65 acceptance 3 end-to-end over the
// pipe: the member holding the token vanishes (connection closed); the owner
// observes the disconnect and reclaims the token after the timeout with no panic
// or spin.
func TestTopologyCablePullMidTransmission(t *testing.T) {
	clock := t0
	ownerSess, memberSess := pipePair(t, "shack", "garage")
	o := newOwner("shack", "A", ownerSess, 30*time.Second, []string{"dmr"})
	go o.run(func() time.Time { return clock })

	memberSess.Send(Message{Type: MsgTokenRequest, Token: &Token{BusID: "A", StreamID: 1}})
	waitUntil(t, func() bool { return o.holder() == "garage" })

	// cable pull: the member connection dies mid-transmission
	memberSess.Close()
	<-ownerSess.Closed()
	<-o.done // the owner goroutine has fully stopped; safe to drive srv from the test
	o.disconnect("garage", clock)

	// within the reclaim window it is still held; after it, reclaimed — no crash
	if rel, _ := o.tick(clock.Add(1 * time.Second)); rel {
		t.Fatal("token reclaimed too early")
	}
	rel, holder := o.tick(clock.Add(TokenReclaimTimeout))
	if !rel || holder != "garage" {
		t.Fatalf("owner should reclaim the dropped member's token, rel=%v holder=%q", rel, holder)
	}
}

// TestTopologyThreeNodeHopCount is the protocol-level three-node check: a frame
// originating on spare, forwarded garage->shack, carries hop count 2 and is never
// accepted back at its origin (spare), nor emitted to its origin attachment.
func TestTopologyThreeNodeHopCount(t *testing.T) {
	env := NewEnvelope("spare", "nxdn", "A")
	atGarage := Forward(env)     // spare -> garage
	atShack := Forward(atGarage) // garage -> shack

	if atShack.HopCount != 2 {
		t.Fatalf("two links crossed should be hop 2, got %d", atShack.HopCount)
	}
	if ok, _ := AcceptInbound(atShack, "shack", DefaultMaxHops); !ok {
		t.Fatal("shack should accept a spare-origin frame at hop 2")
	}
	if ok, _ := AcceptInbound(atShack, "spare", DefaultMaxHops); ok {
		t.Fatal("the origin node (spare) must never re-accept its own frame")
	}
	if ShouldEmitTo(atShack, "spare", "nxdn") {
		t.Fatal("must never emit back to the origin node+attachment")
	}
}

// --- helpers ---------------------------------------------------------------

func waitFor(t *testing.T, s *Session, f func(Message) bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-s.Recv():
			if !ok {
				t.Fatal("session closed before the awaited message")
			}
			if f(m) {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for a message")
		}
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
