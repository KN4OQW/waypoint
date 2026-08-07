package messages

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// fakeRelay stands in for the shim: it records what was injected and lets a test
// drive the tap, which is how the channel-discipline tests simulate a radio
// keying up without a radio.
type fakeRelay struct {
	mu       sync.Mutex
	injected [][]byte
	taps     []dmrshim.Tap
	failFrom int // inject fails from this frame onward; 0 disables
}

func (f *fakeRelay) InjectToHost(d []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failFrom > 0 && len(f.injected) >= f.failFrom {
		return errors.New("the socket went away")
	}
	f.injected = append(f.injected, append([]byte(nil), d...))
	return nil
}

func (f *fakeRelay) AddTap(t dmrshim.Tap) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.taps = append(f.taps, t)
	return func() {}
}

func (f *fakeRelay) frames() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.injected...)
}

// traffic feeds a DMRD frame on a slot to every tap, as the relay would.
func (f *fakeRelay) traffic(slot int) {
	frame := make([]byte, 55)
	copy(frame, "DMRD")
	if slot == 2 {
		frame[15] = 0x80
	}
	f.mu.Lock()
	taps := append([]dmrshim.Tap(nil), f.taps...)
	f.mu.Unlock()
	for _, t := range taps {
		t(dmrshim.ToGateway, frame)
	}
}

// testClock is a controllable clock, so a suite that exercises pacing and
// timeouts does not spend real minutes doing it.
type testClock struct {
	mu   sync.Mutex
	t    time.Time
	on   func(time.Duration) // called on each sleep, before time advances
	slep time.Duration       // total slept
}

func newClock() *testClock {
	return &testClock{t: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) sleep(d time.Duration) {
	c.mu.Lock()
	hook := c.on
	c.mu.Unlock()
	if hook != nil {
		hook(d)
	}
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.slep += d
	c.mu.Unlock()
}

func (c *testClock) slept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slep
}

type harness struct {
	svc   *Service
	relay *fakeRelay
	store *events.Store
	clock *testClock
	hub   *hub.Hub
	seen  chan hub.Event
}

func newHarness(t *testing.T, w Wiring) *harness {
	t.Helper()
	st, err := events.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	h := hub.New()
	seen := make(chan hub.Event, 64)
	sub, _, unsub := h.Subscribe()
	t.Cleanup(unsub)
	go func() {
		for e := range sub {
			select {
			case seen <- e:
			default:
			}
		}
	}()

	relay := &fakeRelay{}
	clock := newClock()
	svc := New(st, h, func() Relay { return relay }, func() (Wiring, error) { return w, nil })
	svc.now, svc.sleep = clock.now, clock.sleep
	return &harness{svc: svc, relay: relay, store: st, clock: clock, hub: h, seen: seen}
}

func (h *harness) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); h.svc.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not return")
		}
	})
}

func (h *harness) waitState(t *testing.T, id int64, want events.MessageState) events.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m, err := h.store.Message(id)
		if err == nil && m.State == want {
			return m
		}
		time.Sleep(time.Millisecond)
	}
	m, _ := h.store.Message(id)
	t.Fatalf("message %d is %s, want %s (reason %q)", id, m.State, want, m.Reason)
	return events.Message{}
}

func defaultWiring() Wiring {
	return Wiring{LocalID: 3180202, Slot: 2, ColorCode: 1, Duplex: true, Preambles: 3}
}

// The happy path, end to end: accepted, queued, transmitted, recorded — and what
// went on the wire is what a radio will reassemble.
func TestSendTransmitsAndRecords(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.run(t)

	m, err := h.svc.Send(3180299, "hello from waypoint", false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if m.State != events.StateQueued {
		t.Errorf("Send returned state %s, want queued", m.State)
	}
	sent := h.waitState(t, m.ID, events.StateSent)
	if sent.Text != "hello from waypoint" || sent.Peer != 3180299 || sent.Local != 3180202 {
		t.Errorf("stored = %+v", sent)
	}

	// The bursts, checked against the codec rather than against a count: the
	// message on the wire has to be the message the codec builds for those
	// parameters, or the radio gets something nobody tested.
	want, err := dmrdata.BuildMessage(dmrdata.SendOptions{
		Src: 3180202, Dst: 3180299, Text: "hello from waypoint", Seq: uint16(m.ID),
		Preambles: 3, ColorCode: 1, Duplex: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames := h.relay.frames()
	if len(frames) != len(want) {
		t.Fatalf("injected %d frames, want %d", len(frames), len(want))
	}
	for i := range want {
		f := frames[i]
		if len(f) != 55 || string(f[0:4]) != "DMRD" {
			t.Fatalf("frame %d is not a DMRD frame: %x", i, f[:min(8, len(f))])
		}
		if got := f[20:53]; string(got) != string(want[i].Payload[:]) {
			t.Errorf("frame %d payload differs from what the codec builds", i)
		}
		// The flags byte's data type must agree with the burst's own slot type.
		// MMDVM-Host reads the burst's copy back and retransmits according to THAT,
		// so a disagreement is a message the radio cannot reassemble.
		if dt := f[15] & 0x0F; dt != byte(want[i].DataType) {
			t.Errorf("frame %d: flags say data type %d, burst says %d", i, dt, want[i].DataType)
		}
		if f[15]&0x80 == 0 {
			t.Errorf("frame %d is on slot 1; the wiring said slot 2", i)
		}
		if f[15]&0x40 == 0 {
			t.Errorf("frame %d is not marked as a private call", i)
		}
		if f[15]&0x20 == 0 {
			t.Errorf("frame %d is not marked as a data frame", i)
		}
	}
	// One stream id across the whole transfer, and a sequence that counts up.
	for i, f := range frames {
		if string(f[16:20]) != string(frames[0][16:20]) {
			t.Errorf("frame %d has a different stream id", i)
		}
		if f[4] != byte(i) {
			t.Errorf("frame %d carries sequence %d", i, f[4])
		}
	}
}

// Bursts go out one per slot period. Faster is not better: the far end has real
// timing, and every message that ever arrived on the bench was paced this way.
func TestBurstsArePacedAtTheSlotPeriod(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.run(t)

	m, err := h.svc.Send(3180299, "pace me", false)
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(t, m.ID, events.StateSent)

	n := len(h.relay.frames())
	// Every gap but the last, plus whatever the idle wait cost.
	if want := time.Duration(n-1) * BurstInterval; h.clock.slept() < want {
		t.Errorf("slept %s for %d bursts, want at least %s", h.clock.slept(), n, want)
	}
}

// Two messages must never interleave. A receiving radio reassembles by position in
// the burst stream, so two transfers at once produce one corrupt message.
func TestMessagesDoNotInterleave(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.run(t)

	var ids []int64
	for i := 0; i < 4; i++ {
		m, err := h.svc.Send(uint32(3180290+i), "message", false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	for _, id := range ids {
		h.waitState(t, id, events.StateSent)
	}

	// Walk the wire: every transfer must run to completion before the next starts.
	// The destination id is in bytes 8-10, so a change mid-transfer is interleaving.
	frames := h.relay.frames()
	var order []string
	for _, f := range frames {
		dst := string(f[8:11])
		if len(order) == 0 || order[len(order)-1] != dst {
			order = append(order, dst)
		}
	}
	if len(order) != len(ids) {
		t.Fatalf("the wire shows %d runs for %d messages; transfers interleaved", len(order), len(ids))
	}
	seen := map[string]bool{}
	for _, dst := range order {
		if seen[dst] {
			t.Fatalf("destination %x appears in two separate runs", dst)
		}
		seen[dst] = true
	}
}

// The channel-discipline test, and the reason this package exists in the shape it
// does: MMDVM-Host DROPS a network frame that arrives while the slot is busy
// (DMRSlot.cpp::writeNetwork, first guard), so a message sent over a transmission
// is silently discarded and the operator is told it was sent.
//
// The busy period is counted in sleeps rather than in wall time: the clock is
// virtual, so a loop that spins fast in real time still burns minutes of it.
func TestTransmissionWaitsForAQuietSlot(t *testing.T) {
	h := newHarness(t, defaultWiring())

	const busySleeps = 20
	var mu sync.Mutex
	sleeps, framesWhileBusy := 0, -1
	h.clock.on = func(time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		sleeps++
		if sleeps <= busySleeps {
			h.relay.traffic(2) // still keyed up
			framesWhileBusy = len(h.relay.frames())
		}
	}
	h.run(t)

	m, err := h.svc.Send(3180299, "wait for me", false)
	if err != nil {
		t.Fatal(err)
	}
	// It must transmit once the slot goes quiet, without any further prompting.
	h.waitState(t, m.ID, events.StateSent)

	mu.Lock()
	defer mu.Unlock()
	if framesWhileBusy != 0 {
		t.Errorf("%d frames went out while the slot was busy", framesWhileBusy)
	}
	if sleeps <= busySleeps {
		t.Errorf("the sender only slept %d times; it did not wait for the channel", sleeps)
	}
	if len(h.relay.frames()) == 0 {
		t.Error("nothing was transmitted once the slot went quiet")
	}
}

// Traffic on the OTHER timeslot is not this message's business. A hotspot carries
// both slots and a message on 2 must not be held up by a conversation on 1.
func TestTrafficOnAnotherSlotDoesNotHoldTheMessage(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.clock.on = func(time.Duration) { h.relay.traffic(1) }
	h.run(t)

	m, err := h.svc.Send(3180299, "slot 2 is free", false)
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(t, m.ID, events.StateSent)
}

// A channel that never goes quiet — a static talkgroup on a simplex node does
// exactly this — must fail with a reason, not sit forever.
func TestAChannelThatNeverGoesQuietFails(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.clock.on = func(time.Duration) { h.relay.traffic(2) }
	h.run(t)

	m, err := h.svc.Send(3180299, "never gets out", false)
	if err != nil {
		t.Fatal(err)
	}
	failed := h.waitState(t, m.ID, events.StateFailed)
	if !strings.Contains(failed.Reason, "busy") {
		t.Errorf("reason = %q, want it to say the channel was busy", failed.Reason)
	}
	if !strings.Contains(failed.Reason, "static talkgroup") {
		t.Errorf("reason = %q, want it to name the commonest cause", failed.Reason)
	}
	if n := len(h.relay.frames()); n != 0 {
		t.Errorf("%d frames were transmitted anyway", n)
	}
}

func TestSendRejections(t *testing.T) {
	h := newHarness(t, defaultWiring())

	for _, tc := range []struct {
		name string
		peer uint32
		text string
		want error
	}{
		{"no destination", 0, "hi", events.ErrBadMessage},
		{"a destination wider than 24 bits", 0x1000000, "hi", events.ErrBadMessage},
		{"text past the on-air limit", 3180299, strings.Repeat("A", dmrdata.MaxTextUnits+1), dmrdata.ErrTextTooLong},
		{"text that is not UTF-8", 3180299, "bad \xc3", dmrdata.ErrInvalidText},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := h.svc.Send(tc.peer, tc.text, false); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
		})
	}

	// A rejected message is never recorded: the operator was told no, and a record
	// of something that did not happen is a record nobody can act on.
	if n, _ := h.store.CountMessages(); n != 0 {
		t.Errorf("%d rejected messages were stored", n)
	}
}

func TestSendNeedsADMRID(t *testing.T) {
	h := newHarness(t, Wiring{Slot: 2})
	if _, err := h.svc.Send(3180299, "hi", false); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

// With the relay switched off there is nowhere to transmit. The message is
// recorded and failed with a reason rather than silently dropped, so an operator
// who sent one before switching the relay on can see what happened.
func TestNoRelayFailsWithAReason(t *testing.T) {
	st, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	svc := New(st, hub.New(), func() Relay { return nil }, func() (Wiring, error) { return defaultWiring(), nil })
	clock := newClock()
	svc.now, svc.sleep = clock.now, clock.sleep

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx)

	m, err := svc.Send(3180299, "nowhere to go", false)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := st.Message(m.ID)
		if got.State == events.StateFailed {
			if !strings.Contains(got.Reason, "relay") {
				t.Errorf("reason = %q, want it to mention the relay", got.Reason)
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the message never failed")
}

// An injection that fails partway must fail the message, with the burst it got to.
// A half-sent transfer leaves the radio holding a header whose blocks never
// arrive, and reporting it as sent would hide that entirely.
func TestAPartialTransmissionFails(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.relay.failFrom = 3
	h.run(t)

	m, err := h.svc.Send(3180299, "this one breaks", false)
	if err != nil {
		t.Fatal(err)
	}
	failed := h.waitState(t, m.ID, events.StateFailed)
	if !strings.Contains(failed.Reason, "burst 4") {
		t.Errorf("reason = %q, want it to say which burst failed", failed.Reason)
	}
}

func TestQueueFullIsRecordedNotDropped(t *testing.T) {
	h := newHarness(t, defaultWiring())
	// No Run: nothing drains the queue, so it fills.
	var lastErr error
	var overflow events.Message
	for i := 0; i < QueueDepth+2; i++ {
		m, err := h.svc.Send(3180299, "fill", false)
		if err != nil {
			lastErr, overflow = err, m
		}
	}
	if !errors.Is(lastErr, ErrQueueFull) {
		t.Fatalf("err = %v, want ErrQueueFull", lastErr)
	}
	if overflow.State != events.StateFailed {
		t.Errorf("the overflowing message is %s, want failed", overflow.State)
	}
	if !strings.Contains(overflow.Reason, "waiting") {
		t.Errorf("reason = %q", overflow.Reason)
	}
}

// The hub event carries the id and the state, and NOT the text. The event log is
// republished to MQTT and read by clients that are not entitled to correspondence;
// the id is enough to go and read the message.
func TestEventsCarryNoMessageText(t *testing.T) {
	h := newHarness(t, defaultWiring())
	h.run(t)

	const secret = "this text must not appear in an event"
	m, err := h.svc.Send(3180299, secret, false)
	if err != nil {
		t.Fatal(err)
	}
	h.waitState(t, m.ID, events.StateSent)

	var got int
	for {
		select {
		case e := <-h.seen:
			got++
			if e.Type != EventMessageOut {
				t.Errorf("event type = %q", e.Type)
			}
			for _, field := range []string{e.Detail, e.Source, e.Dest, e.Network, e.Mode} {
				if strings.Contains(field, secret) {
					t.Fatalf("an event carried the message text: %q", field)
				}
			}
		default:
			if got == 0 {
				t.Fatal("no events were published")
			}
			return
		}
	}
}
