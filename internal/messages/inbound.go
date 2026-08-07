package messages

import (
	"context"
	"log"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
)

// Capturing the text messages that cross the node.
//
// # It only ever watches
//
// This is a tap on the relay, and a tap cannot stop a frame. The relay forwards
// first and offers a COPY to its observers afterwards, through a buffered channel
// that drops rather than blocks — so nothing here, however slow or however broken,
// can delay or prevent a message reaching DMRGateway and the network. A radio
// texting BrandMeister keeps working exactly as it did before this existed, and
// that is a property of the relay's design rather than of care taken here.
//
// The reassembly runs in the tap because it is cheap: FEC and a checksum over a
// few dozen bytes. The database write does not, because it is not — completed
// messages go to a small buffered channel and a separate goroutine stores them. A
// slow disk costs a captured message, never a forwarded frame.
//
// # Both directions
//
// Radio→network and network→radio are both worth having, and they are the same
// job. The first is the operator's own outgoing traffic; the second is the replies
// they were waiting for, which is the entire reason this workstream exists.
//
// They are reassembled separately. Each is an independent burst stream and a
// transfer is reassembled by POSITION within its stream, so folding them into one
// reassembler would let a message travelling one way collect blocks from a message
// travelling the other.
//
// # Whose messages these are
//
// On a hotspot every message that crosses the node is the operator's own, which is
// the case this is built for. On a repeater carrying other people's traffic it is
// not, and an operator running one should know that the node records what crosses
// it. Messages are on the never-public list and no public route serves one, so the
// exposure is to whoever can already log in — but it is worth knowing about rather
// than discovering.

// capturedQueue is how many completed messages may be waiting to be stored. A
// message takes a second of air time to arrive, so anything past a handful means
// the database has stopped answering and dropping is the right answer.
const capturedQueue = 16

// captured is one reassembled message and which way it was going.
type captured struct {
	msg dmrdata.Message
	dir dmrshim.Direction
	at  time.Time
}

// Capture is what the tap has seen. Every counter but Messages is something that
// did not become a record, and each says why — a message that vanishes with no
// counter behind it is the failure mode that cost days on the bench.
type Capture struct {
	// Messages is completed, checksum-verified messages stored.
	Messages int
	// Dropped is completed messages the store could not keep up with.
	Dropped int
	// StoreErrors is completed messages the database refused.
	StoreErrors int
	// FromRadio and FromNetwork split Messages by direction.
	FromRadio   int
	FromNetwork int
	// Reassembly is the codec's own accounting: checksum failures, orphaned
	// blocks, bursts with no sync. See dmrdata.ReassemblyStats.
	Reassembly dmrdata.ReassemblyStats
}

// StartCapture attaches the message tap to the live relay and stores what it
// reassembles, until ctx is cancelled.
//
// It is separate from Run because the two do unrelated things at unrelated times:
// Run drains a queue this node filled, StartCapture reacts to the air. They share
// a Service only because they share a store and a hub.
func (s *Service) StartCapture(ctx context.Context) {
	go s.captureLoop(ctx)
}

func (s *Service) captureLoop(ctx context.Context) {
	// Re-attach whenever the relay is rebuilt. Its ports move on an Apply and the
	// old one stops carrying anything, so a tap attached once at startup would
	// quietly stop seeing traffic and the node would report nothing wrong.
	t := time.NewTicker(captureAttachInterval)
	defer t.Stop()
	defer s.detachCapture()

	s.attachCapture()
	for {
		select {
		case <-ctx.Done():
			return
		case c := <-s.captured:
			s.recordCaptured(c)
		case <-t.C:
			s.attachCapture()
		}
	}
}

// captureAttachInterval is how often the capture checks it is still attached to
// the relay that is actually running. It matches the relay's own reconcile
// cadence; there is nothing to gain from noticing sooner than it changes.
const captureAttachInterval = 15 * time.Second

func (s *Service) attachCapture() {
	relay := s.currentRelay()
	if relay == nil {
		return
	}
	s.mu.Lock()
	if s.capOn == relay {
		s.mu.Unlock()
		return
	}
	s.capOn = relay
	prev := s.capRemove
	// A rebuilt relay is a new stream in both directions; the old reassemblers
	// hold blocks from a transfer that can no longer complete.
	s.rx = map[dmrshim.Direction]*dmrdata.Reassembler{
		dmrshim.ToGateway: {},
		dmrshim.ToHost:    {},
	}
	s.mu.Unlock()
	if prev != nil {
		prev()
	}

	remove := relay.AddTap(s.observe)
	s.mu.Lock()
	s.capRemove = remove
	s.mu.Unlock()
}

func (s *Service) detachCapture() {
	s.mu.Lock()
	remove := s.capRemove
	s.capRemove, s.capOn = nil, nil
	s.mu.Unlock()
	if remove != nil {
		remove()
	}
}

// observe is the tap. It runs on the relay's fan-out goroutine, off the forwarding
// path, and must not block: a completed message that will not fit the queue is
// dropped and counted.
func (s *Service) observe(dir dmrshim.Direction, datagram []byte) {
	burst, ok := dmrdBurst(datagram)
	if !ok {
		return
	}
	s.mu.Lock()
	r := s.rx[dir]
	s.mu.Unlock()
	if r == nil {
		return
	}
	msg, done := r.Feed(burst, s.now())
	if !done {
		return
	}
	select {
	case s.captured <- captured{msg: *msg, dir: dir, at: s.now()}:
	default:
		s.mu.Lock()
		s.cap.Dropped++
		s.mu.Unlock()
	}
}

// recordCaptured writes one captured message and announces it.
func (s *Service) recordCaptured(c captured) {
	// Peer is who sent it, Local is who it was addressed to. For a message merely
	// crossing the node, "Local" is the addressed destination rather than this
	// node's own id — the record says what was on the air, which is the only thing
	// it can honestly say.
	m, err := s.store.RecordInbound(events.Message{
		Peer:  c.msg.Src,
		Local: c.msg.Dst,
		Group: c.msg.Group,
		Text:  c.msg.Text,
	})
	s.mu.Lock()
	if err != nil {
		s.cap.StoreErrors++
		s.mu.Unlock()
		log.Printf("messages: storing a captured message from %d: %v", c.msg.Src, err)
		return
	}
	s.cap.Messages++
	if c.dir == dmrshim.ToGateway {
		s.cap.FromRadio++
	} else {
		s.cap.FromNetwork++
	}
	s.mu.Unlock()

	log.Printf("messages: captured a message %d -> %d (%d characters)",
		c.msg.Src, c.msg.Dst, len([]rune(c.msg.Text)))
	s.publish(m)
}

// Capture reports what the tap has seen, folding in the codec's own accounting.
func (s *Service) Capture() Capture {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.cap
	for _, r := range s.rx {
		if r == nil {
			continue
		}
		st := r.Stats
		out.Reassembly.Messages += st.Messages
		out.Reassembly.BadCRC += st.BadCRC
		out.Reassembly.BadHeader += st.BadHeader
		out.Reassembly.Unusable += st.Unusable
		out.Reassembly.Unsupported += st.Unsupported
		out.Reassembly.Orphaned += st.Orphaned
		out.Reassembly.Abandoned += st.Abandoned
		out.Reassembly.Malformed += st.Malformed
		out.Reassembly.NoSync += st.NoSync
	}
	return out
}

// dmrdBurst pulls the 33-byte burst out of a DMRD frame, if the frame is one and
// carries data rather than voice.
//
// The voice check is the caller's job that nothing else can do: a voice burst
// decoded as data yields whichever slot type its embedded signalling happens to
// spell, and the reassembler says so in its own documentation.
func dmrdBurst(datagram []byte) ([]byte, bool) {
	if len(datagram) < 53 || string(datagram[0:4]) != "DMRD" {
		return nil, false
	}
	if datagram[15]&0x20 == 0 {
		return nil, false // voice
	}
	return datagram[20:53], true
}
