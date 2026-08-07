// Package messages sends a DMR text message from this node to a radio.
//
// It is the join between three things that already exist: internal/dmrdata builds
// the bursts, internal/dmrshim puts them on the loopback, internal/events
// remembers what happened. What is new here is everything about WHEN.
//
// # One message at a time
//
// A receiving radio reassembles a transfer by position in the burst stream: a data
// header says how many blocks follow, and the blocks that follow are those blocks.
// Two messages emitted at once do not produce two messages, they produce one
// corrupt one. So there is a single sender goroutine and a queue, and no amount of
// concurrent API calls can interleave two transfers.
//
// # Never on top of a transmission
//
// MMDVM-Host does not queue a network frame that arrives while the slot is busy.
// It DROPS it — DMRSlot.cpp::writeNetwork opens with
//
//	if ((m_rfState != RPT_RF_STATE::LISTENING) && (m_netState == RPT_NET_STATE::IDLE)
//	    && !m_modem->getDMRTrunkingEnabled())
//	        return;
//
// so a message sent while a local radio is keying that timeslot is silently
// discarded, burst by burst, and the operator is told it was sent. Worse, a
// message that is half discarded leaves the receiving radio holding a header whose
// blocks never arrive.
//
// Nothing downstream will hold the frames for us, so this package does: it watches
// the relay's tap for traffic on the target slot and waits for the channel to go
// quiet before it emits anything. If the channel never goes quiet — a static
// talkgroup on a simplex node will do exactly that — the message fails with a
// reason that says so, rather than being thrown at a busy slot.
//
// # Pacing
//
// Bursts are emitted one per 60 ms, which is the DMR slot period and the rate the
// bench harness used for every message that arrived. MMDVM-Host does have a queue
// on the far side, so a burst would probably survive being handed over early; "one
// burst per slot period" is what the radio's timing expects and there is no
// argument for going faster than the air can carry it.
package messages

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// BurstInterval is the gap between emitted bursts: one DMR slot period.
const BurstInterval = 60 * time.Millisecond

// IdleWindow is how long the target timeslot must be quiet before a message is
// emitted. A DMR burst is one per 60 ms, so 400 ms is several missed bursts —
// comfortably past a gap in a live stream, comfortably short of making an operator
// wait after a transmission ends.
const IdleWindow = 400 * time.Millisecond

// MaxChannelWait bounds that wait. A channel that has been busy for a minute is
// not about to go quiet because we waited longer, and a message queued behind it
// should say so rather than sit there. The commonest cause on a simplex node is a
// static talkgroup, which never stops.
const MaxChannelWait = 60 * time.Second

// QueueDepth is how many messages may be waiting to transmit. Each takes a second
// or two of air time, so a deep queue is minutes of transmission nobody is
// watching; past this the API says no rather than accepting work it will not get
// to.
const QueueDepth = 32

// Errors callers distinguish.
var (
	// ErrNoRelay is returned when the DMR message relay is switched off or not
	// running. Nothing can be transmitted without it.
	ErrNoRelay = errors.New("messages: the DMR message relay is not running")
	// ErrQueueFull is returned when too many messages are already waiting.
	ErrQueueFull = errors.New("messages: too many messages are waiting to transmit")
	// ErrNotConfigured is returned when the node has no DMR ID to send from.
	ErrNotConfigured = errors.New("messages: this node has no DMR ID configured")
)

// Relay is the part of internal/dmrshim this package needs: somewhere to put a
// frame, and somewhere to watch for traffic. An interface rather than the concrete
// type so the tests drive the sender without a UDP socket, and so the sender never
// acquires the ability to do anything else to the relay.
type Relay interface {
	InjectToHost(datagram []byte) error
	AddTap(dmrshim.Tap) (remove func())
}

// Wiring is what the node's configuration contributes to a message. It is read
// per message rather than captured, so an operator changing the DMR ID or the
// timeslot does not need to restart anything.
type Wiring struct {
	// LocalID is this node's DMR ID: the source a receiving radio displays.
	LocalID uint32
	// Slot is the timeslot to transmit on, 1 or 2.
	Slot int
	// ColorCode and Duplex are stamped into every burst. MMDVM-Host overwrites
	// both with the node's own settings on the way out, so they only have to be
	// legal — but the data type they share those bits with is read back and used.
	ColorCode byte
	Duplex    bool
	// Preambles is how many preamble CSBKs precede a message; 0 takes the default.
	Preambles int
}

// Service accepts messages, records them, and transmits them one at a time.
type Service struct {
	store  *events.Store
	hub    *hub.Hub
	wiring func() (Wiring, error)

	// relay returns the live relay, or nil when it is switched off. Read per
	// message: the relay is reconciled on a tick and can come and go under an
	// Apply, and a captured nil would mean messages never worked again.
	relay func() Relay

	queue chan int64

	mu      sync.Mutex
	lastSaw map[int]time.Time // slot -> when traffic was last seen on it
	removes []func()
	relayOn Relay     // the relay the tap is currently attached to
	watchAt time.Time // when the current tap started watching; zero = not watching

	// now and sleep are the clock, replaced in tests so a suite does not spend
	// real seconds pacing bursts.
	now   func() time.Time
	sleep func(time.Duration)
}

// New builds the service. It does not start transmitting; call Run.
func New(store *events.Store, h *hub.Hub, relay func() Relay, wiring func() (Wiring, error)) *Service {
	return &Service{
		store:   store,
		hub:     h,
		wiring:  wiring,
		relay:   relay,
		queue:   make(chan int64, QueueDepth),
		lastSaw: map[int]time.Time{},
		now:     time.Now,
		sleep:   time.Sleep,
	}
}

// Send validates a message, records it as queued, and hands it to the sender.
//
// It returns as soon as the message is recorded, because transmitting takes air
// time and an HTTP client should not hold a connection open for it. The returned
// message is the stored one; its state moves afterwards and the caller watches for
// that the same way it watches everything else — the record and the event stream.
func (s *Service) Send(peer uint32, text string, group bool) (events.Message, error) {
	w, err := s.wiring()
	if err != nil {
		return events.Message{}, err
	}
	if w.LocalID == 0 {
		return events.Message{}, ErrNotConfigured
	}
	if peer == 0 || peer > 0xFFFFFF {
		return events.Message{}, fmt.Errorf("%w: %d is not a DMR ID", events.ErrBadMessage, peer)
	}
	// The length check happens here, before anything is stored, because the limit
	// is a property of the on-air format and the caller should be told no rather
	// than watching a message queue and then fail.
	if _, err := dmrdata.BuildMessage(dmrdata.SendOptions{
		Src: w.LocalID, Dst: peer, Text: text, Group: group, Preambles: 1,
	}); err != nil {
		return events.Message{}, err
	}

	m, err := s.store.Enqueue(events.Message{
		Peer: peer, Local: w.LocalID, Group: group, Text: text,
	})
	if err != nil {
		return events.Message{}, err
	}

	select {
	case s.queue <- m.ID:
	default:
		// Recorded and immediately failed, rather than dropped: a message the
		// operator was told about has to end up in the record with a reason.
		failed, ferr := s.store.Advance(m.ID, events.StateFailed, ErrQueueFull.Error())
		if ferr != nil {
			return m, ErrQueueFull
		}
		s.publish(failed)
		return failed, ErrQueueFull
	}
	s.publish(m)
	return m, nil
}

// Run transmits queued messages until ctx is cancelled. One goroutine, one message
// at a time: see the package comment for why that is not an optimisation to
// revisit.
func (s *Service) Run(ctx context.Context) {
	defer s.detachTap()
	// Start watching immediately rather than at the first message, so a node that
	// has been up a while already knows whether its channel is busy. When the relay
	// is not running yet this does nothing, and the first message attaches instead.
	s.attachTap()
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.queue:
			s.transmit(ctx, id)
		}
	}
}

// transmit takes one message from queued to sent, or to failed with a reason.
func (s *Service) transmit(ctx context.Context, id int64) {
	m, err := s.store.Message(id)
	if err != nil {
		log.Printf("messages: %v", err)
		return
	}
	fail := func(reason string) {
		failed, err := s.store.Advance(id, events.StateFailed, reason)
		if err != nil {
			log.Printf("messages: recording failure of message %d: %v", id, err)
			return
		}
		log.Printf("messages: message %d to %d failed: %s", id, m.Peer, reason)
		s.publish(failed)
	}

	relay := s.currentRelay()
	if relay == nil {
		fail(ErrNoRelay.Error())
		return
	}
	w, err := s.wiring()
	if err != nil {
		fail(err.Error())
		return
	}
	bursts, err := dmrdata.BuildMessage(dmrdata.SendOptions{
		Src: m.Local, Dst: m.Peer, Text: m.Text, Group: m.Group,
		Seq: uint16(id), Preambles: w.Preambles, ColorCode: w.ColorCode, Duplex: w.Duplex,
	})
	if err != nil {
		fail(err.Error())
		return
	}

	// Wait for the slot before claiming to be transmitting. A message that waits
	// thirty seconds for a busy channel and then fails should never have read as
	// "transmitting" for those thirty seconds.
	if err := s.waitForIdle(ctx, w.Slot); err != nil {
		fail(err.Error())
		return
	}

	if m, err = s.store.Advance(id, events.StateTransmitting, ""); err != nil {
		log.Printf("messages: %v", err)
		return
	}
	s.publish(m)

	streamID := rand.Uint32()
	for i, b := range bursts {
		if ctx.Err() != nil {
			fail("the daemon is shutting down")
			return
		}
		frame := dmrdFrame(b, m, w, byte(i), streamID)
		if err := relay.InjectToHost(frame); err != nil {
			fail(fmt.Sprintf("burst %d of %d: %v", i+1, len(bursts), err))
			return
		}
		if i < len(bursts)-1 {
			s.sleep(BurstInterval)
		}
	}

	if m, err = s.store.Advance(id, events.StateSent, ""); err != nil {
		log.Printf("messages: %v", err)
		return
	}
	log.Printf("messages: sent message %d to %d (%d bursts)", id, m.Peer, len(bursts))
	s.publish(m)
}

// waitForIdle blocks until the timeslot has been quiet for IdleWindow, or gives up.
func (s *Service) waitForIdle(ctx context.Context, slot int) error {
	s.attachTap()
	deadline := s.now().Add(MaxChannelWait)
	for {
		if s.idleFor(slot) >= IdleWindow {
			return nil
		}
		if !s.now().Before(deadline) {
			return fmt.Errorf("timeslot %d has been busy for %s; the channel never went quiet "+
				"(a static talkgroup on a simplex node will do this)", slot, MaxChannelWait)
		}
		if ctx.Err() != nil {
			return errors.New("the daemon is shutting down")
		}
		s.sleep(IdleWindow / 4)
	}
}

// idleFor reports how long the slot has been quiet, counting from whichever is
// later: the last burst seen on it, or the moment the tap started watching.
//
// Counting from the tap's start is the part that matters. A slot nothing has been
// seen on is not known to be quiet — it is unobserved, and those are different.
// Without this the first message after startup transmits immediately, on top of
// whatever transmission was already in progress, and MMDVM-Host discards it burst
// by burst while the operator is told it was sent.
//
// A tap that is not attached at all reports zero idle time: with no way to observe
// the channel there is no basis for claiming it is free.
func (s *Service) idleFor(slot int) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.watchAt.IsZero() {
		return 0
	}
	since := s.watchAt
	if last, ok := s.lastSaw[slot]; ok && last.After(since) {
		since = last
	}
	return s.now().Sub(since)
}

// attachTap makes sure the service is watching the relay it is about to use. The
// relay is rebuilt whenever its ports move, so this re-attaches to whichever one is
// live rather than assuming the one it saw at startup still exists.
func (s *Service) attachTap() {
	relay := s.currentRelay()
	if relay == nil {
		return
	}
	s.mu.Lock()
	if s.relayOn == relay && !s.watchAt.IsZero() {
		s.mu.Unlock()
		return
	}
	s.relayOn = relay
	s.mu.Unlock()

	s.detachTap()
	remove := relay.AddTap(func(_ dmrshim.Direction, datagram []byte) {
		slot, ok := dmrdSlot(datagram)
		if !ok {
			return
		}
		s.mu.Lock()
		s.lastSaw[slot] = s.now()
		s.mu.Unlock()
	})
	s.mu.Lock()
	s.removes = append(s.removes, remove)
	// The watch starts now. Everything before this moment is unobserved, and
	// idleFor must not mistake that for silence.
	s.watchAt = s.now()
	s.mu.Unlock()
}

func (s *Service) detachTap() {
	s.mu.Lock()
	removes := s.removes
	s.removes = nil
	s.watchAt = time.Time{}
	s.mu.Unlock()
	for _, r := range removes {
		r()
	}
}

func (s *Service) currentRelay() Relay {
	if s.relay == nil {
		return nil
	}
	r := s.relay()
	// A nil inside a non-nil interface is the classic way for "no relay" to look
	// like "a relay", so the caller returns a typed nil at its own peril; check the
	// value the honest way.
	if r == nil {
		return nil
	}
	return r
}

// publish emits the message as a hub event, which is what drives the SSE poke and
// the MQTT republish. The event carries no message TEXT: the event log is a
// different disclosure surface from the message record, it is republished to MQTT,
// and a text message is correspondence. The id is enough for a client to go and
// read the message it is entitled to.
func (s *Service) publish(m events.Message) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(hub.Event{
		Time:   m.UpdatedAt,
		Type:   EventType(m),
		Mode:   "DMR",
		Source: fmt.Sprintf("%d", m.Local),
		Dest:   fmt.Sprintf("%d", m.Peer),
		Detail: fmt.Sprintf("message %d %s", m.ID, m.State),
	})
}

// Event types for the message lifecycle. They are distinct from the link and voice
// events so a client can subscribe to messages alone.
const (
	// EventMessageOut is any state change of an outbound message.
	EventMessageOut = "message_out"
	// EventMessageIn is an inbound message being recorded.
	EventMessageIn = "message_in"
)

// EventType is the hub event type for a message's direction.
func EventType(m events.Message) string {
	if m.Direction == events.Inbound {
		return EventMessageIn
	}
	return EventMessageOut
}

// dmrdFrame wraps one burst as the 55-byte homebrew DMRD frame both daemons speak
// (g4klx MMDVMHost DMRNetwork.cpp::write).
//
// The data type goes in the flags byte AND is already in the burst's own slot
// type. They must agree: MMDVM-Host reads the burst's copy back off the wire
// (DMRSlot.cpp, CDMRSlotType::putData) and re-transmits according to THAT, so the
// flags byte alone decides nothing.
func dmrdFrame(b dmrdata.Burst, m events.Message, w Wiring, seq byte, streamID uint32) []byte {
	f := make([]byte, 55)
	copy(f, "DMRD")
	f[4] = seq
	putU24(f[5:], m.Local)
	putU24(f[8:], m.Peer)
	// Repeater id (bytes 11-14) is the sender's own id on a real link. DMRGateway
	// fills its own in on the way upstream and MMDVM-Host does not read it on the
	// way down, so it carries this node's id rather than a zero that would look
	// like a bug in a capture.
	putU32(f[11:], m.Local)

	var flags byte = 0x20 // data sync
	if w.Slot == 2 {
		flags |= 0x80
	}
	if !m.Group {
		flags |= 0x40 // private call
	}
	f[15] = flags | byte(b.DataType)&0x0F
	putU32(f[16:], streamID)
	copy(f[20:53], b.Payload[:])
	return f
}

// dmrdSlot reports which timeslot a DMRD frame is on. Anything that is not a DMRD
// frame is not traffic this package should wait for.
func dmrdSlot(datagram []byte) (int, bool) {
	if len(datagram) < 20 || string(datagram[0:4]) != "DMRD" {
		return 0, false
	}
	if datagram[15]&0x80 != 0 {
		return 2, true
	}
	return 1, true
}

func putU24(b []byte, v uint32) {
	b[0], b[1], b[2] = byte(v>>16), byte(v>>8), byte(v)
}

func putU32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}
