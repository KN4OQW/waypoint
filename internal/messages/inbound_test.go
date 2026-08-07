package messages

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/dmrdata"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/events"
)

// air builds the DMRD frames a radio would put on the loopback for one message,
// so the capture tests drive the same bytes the codec produces rather than a
// hand-rolled approximation of them.
func air(t *testing.T, src, dst uint32, text string, group bool) [][]byte {
	t.Helper()
	bursts, err := dmrdata.BuildMessage(dmrdata.SendOptions{
		Src: src, Dst: dst, Text: text, Group: group, Preambles: 2, ColorCode: 1, Duplex: true,
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	out := make([][]byte, 0, len(bursts))
	for i, b := range bursts {
		f := make([]byte, 55)
		copy(f, "DMRD")
		f[4] = byte(i)
		putU24(f[5:], src)
		putU24(f[8:], dst)
		flags := byte(0x80 | 0x20) // slot 2, data sync
		if !group {
			flags |= 0x40
		}
		f[15] = flags | byte(b.DataType)&0x0F
		putU32(f[16:], 0x11223344)
		copy(f[20:53], b.Payload[:])
		out = append(out, f)
	}
	return out
}

// voiceFrame is a DMRD frame carrying voice rather than data. The capture must
// ignore it: decoding a voice burst as data yields whichever slot type its
// embedded signalling happens to spell.
func voiceFrame() []byte {
	f := make([]byte, 55)
	copy(f, "DMRD")
	f[15] = 0x80 | 0x10 // slot 2, voice sync
	return f
}

func captureHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, defaultWiring())
	ctx, cancel := context.WithCancel(context.Background())
	h.svc.StartCapture(ctx)
	t.Cleanup(cancel)
	// The capture attaches on its own goroutine; wait for the tap to exist before
	// feeding it anything, or the first test frame goes nowhere.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		h.relay.mu.Lock()
		n := len(h.relay.taps)
		h.relay.mu.Unlock()
		if n > 0 {
			return h
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("the capture never attached a tap")
	return nil
}

func (h *harness) feed(frames [][]byte, dir dmrshim.Direction) {
	h.relay.mu.Lock()
	taps := append([]dmrshim.Tap(nil), h.relay.taps...)
	h.relay.mu.Unlock()
	for _, f := range frames {
		for _, tap := range taps {
			tap(dir, f)
		}
	}
}

func (h *harness) waitMessages(t *testing.T, n int) []events.Message {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := h.store.Messages(events.MessageQuery{Direction: events.Inbound})
		if err == nil && len(got) >= n {
			return got
		}
		time.Sleep(time.Millisecond)
	}
	got, _ := h.store.Messages(events.MessageQuery{Direction: events.Inbound})
	t.Fatalf("captured %d messages, want %d (capture %+v)", len(got), n, h.svc.Capture())
	return nil
}

// A radio texts the network; the node records it and the frames still go through.
func TestCapturesAMessageFromTheRadio(t *testing.T) {
	h := captureHarness(t)
	h.feed(air(t, 3180202, 262993, "ECHO", false), dmrshim.ToGateway)

	got := h.waitMessages(t, 1)
	m := got[0]
	if m.Text != "ECHO" || m.Peer != 3180202 || m.Local != 262993 {
		t.Errorf("= %+v", m)
	}
	if m.Direction != events.Inbound || m.State != events.StateReceived {
		t.Errorf("= %s/%s", m.Direction, m.State)
	}
	if c := h.svc.Capture(); c.FromRadio != 1 || c.FromNetwork != 0 {
		t.Errorf("capture = %+v", c)
	}
}

// And the reply coming back the other way, which is the entire reason this
// workstream exists.
func TestCapturesAMessageFromTheNetwork(t *testing.T) {
	h := captureHarness(t)
	h.feed(air(t, 262993, 3180202, "Unknown Format, please start your SMS with the CallSign", false), dmrshim.ToHost)

	got := h.waitMessages(t, 1)
	if !strings.HasPrefix(got[0].Text, "Unknown Format") || got[0].Peer != 262993 {
		t.Errorf("= %+v", got[0])
	}
	if c := h.svc.Capture(); c.FromNetwork != 1 || c.FromRadio != 0 {
		t.Errorf("capture = %+v", c)
	}
}

// Both directions at once, interleaved frame by frame. They are independent burst
// streams and a transfer reassembles by POSITION within its stream, so a single
// shared reassembler would braid them into one wrong message.
func TestDirectionsDoNotBraidTogether(t *testing.T) {
	h := captureHarness(t)
	up := air(t, 3180202, 262993, "from the radio", false)
	down := air(t, 262993, 3180202, "from the network", false)

	for i := 0; i < len(up) || i < len(down); i++ {
		if i < len(up) {
			h.feed([][]byte{up[i]}, dmrshim.ToGateway)
		}
		if i < len(down) {
			h.feed([][]byte{down[i]}, dmrshim.ToHost)
		}
	}

	got := h.waitMessages(t, 2)
	texts := map[string]bool{}
	for _, m := range got {
		texts[m.Text] = true
	}
	if !texts["from the radio"] || !texts["from the network"] {
		t.Errorf("captured %v, want both messages intact", texts)
	}
	if c := h.svc.Capture(); c.Reassembly.BadCRC != 0 || c.Reassembly.Malformed != 0 {
		t.Errorf("interleaving produced failures: %+v", c.Reassembly)
	}
}

// Two radios texting on the same slot. Nothing may be stored that nobody sent;
// failing to reassemble is acceptable, inventing a message is not.
func TestInterleavedSourcesProduceNothingNobodySent(t *testing.T) {
	h := captureHarness(t)
	a := air(t, 3180202, 262993, "message from A", false)
	b := air(t, 3180299, 262993, "message from B", false)

	for i := 0; i < len(a) || i < len(b); i++ {
		if i < len(a) {
			h.feed([][]byte{a[i]}, dmrshim.ToGateway)
		}
		if i < len(b) {
			h.feed([][]byte{b[i]}, dmrshim.ToGateway)
		}
	}
	time.Sleep(50 * time.Millisecond)

	got, err := h.store.Messages(events.MessageQuery{Direction: events.Inbound})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got {
		if m.Text != "message from A" && m.Text != "message from B" {
			t.Errorf("captured %q, which nobody sent", m.Text)
		}
	}
}

// A message damaged past what the FEC can repair must be counted and NOT stored.
// A checksum exists so that a corrupt message is a known absence rather than
// silently wrong text on somebody's screen.
func TestAFailedChecksumStoresNothingAndCounts(t *testing.T) {
	h := captureHarness(t)
	frames := air(t, 3180202, 262993, "this message gets wrecked in transit", false)

	// Wreck a body block, avoiding the sync region so this exercises the CHECKSUM
	// rather than the no-sync detection, which has its own test.
	last := frames[len(frames)-1]
	for _, i := range []int{20, 23, 27, 31, 41, 44, 48, 52} {
		last[i] ^= 0xFF
	}
	h.feed(frames, dmrshim.ToGateway)
	time.Sleep(50 * time.Millisecond)

	if n, _ := h.store.CountMessages(); n != 0 {
		t.Errorf("%d messages were stored from a wrecked transfer", n)
	}
	c := h.svc.Capture()
	if c.Reassembly.BadCRC == 0 && c.Reassembly.Malformed == 0 {
		t.Errorf("a wrecked transfer was not counted: %+v", c.Reassembly)
	}
}

// Voice must not be decoded as data. A voice burst's embedded signalling reads as
// whichever slot type the bits happen to spell, and acting on that is how a
// conversation turns into a stream of imaginary data headers.
func TestVoiceIsIgnored(t *testing.T) {
	h := captureHarness(t)
	for i := 0; i < 100; i++ {
		h.feed([][]byte{voiceFrame()}, dmrshim.ToGateway)
	}
	time.Sleep(50 * time.Millisecond)

	if n, _ := h.store.CountMessages(); n != 0 {
		t.Errorf("%d messages were captured from voice frames", n)
	}
	if c := h.svc.Capture(); c.Reassembly != (dmrdata.ReassemblyStats{}) {
		t.Errorf("voice reached the reassembler: %+v", c.Reassembly)
	}
}

// Anything that is not a DMRD frame is not this package's business, and must not
// panic it. The relay carries the Homebrew login exchange over the same sockets.
func TestNonDMRDTrafficIsIgnored(t *testing.T) {
	h := captureHarness(t)
	for _, junk := range [][]byte{
		nil, {}, []byte("RPTL"), []byte("MSTPONG"), []byte("DMRD"),
		make([]byte, 52), make([]byte, 54), make([]byte, 2048),
	} {
		h.feed([][]byte{junk}, dmrshim.ToGateway)
		h.feed([][]byte{junk}, dmrshim.ToHost)
	}
	time.Sleep(20 * time.Millisecond)
	if n, _ := h.store.CountMessages(); n != 0 {
		t.Errorf("%d messages were captured from junk", n)
	}
}

// The bursts an encoder produced without their sync — the 2026-08-06 bug — must
// be counted rather than silently ignored, so a regression in the transmit path is
// visible from the receive side without a radio.
func TestNoSyncBurstsAreCounted(t *testing.T) {
	h := captureHarness(t)
	frames := air(t, 3180202, 262993, "broken", false)
	for _, f := range frames {
		// Zero the sync/slot-type region, which is exactly what encoding into a
		// zeroed buffer used to leave behind.
		for i := 20 + 12; i < 20+21; i++ {
			f[i] = 0
		}
	}
	h.feed(frames, dmrshim.ToGateway)
	time.Sleep(50 * time.Millisecond)

	if c := h.svc.Capture(); c.Reassembly.NoSync == 0 {
		t.Errorf("bursts with no sync were not counted: %+v", c.Reassembly)
	}
	if n, _ := h.store.CountMessages(); n != 0 {
		t.Errorf("%d messages were stored from bursts with no sync", n)
	}
}

// The capture must survive whatever arrives. The corpus is the shapes that
// actually reach a loopback plus the mutations most likely to trip an offset bug.
func FuzzObserve(f *testing.F) {
	base := make([]byte, 55)
	copy(base, "DMRD")
	base[15] = 0x80 | 0x20 | 0x07
	f.Add(base)
	f.Add([]byte("DMRD"))
	f.Add([]byte{})
	f.Add(make([]byte, 53))
	f.Fuzz(func(t *testing.T, data []byte) {
		st, err := events.Open(":memory:")
		if err != nil {
			t.Skip()
		}
		defer st.Close()
		svc := New(st, nil, func() Relay { return nil }, func() (Wiring, error) { return defaultWiring(), nil })
		for _, dir := range []dmrshim.Direction{dmrshim.ToGateway, dmrshim.ToHost} {
			svc.observe(dir, data)
		}
	})
}

// A completed message that will not fit the store queue is dropped and counted,
// never allowed to block: the tap runs on the relay's fan-out goroutine and a stall
// there costs the relay its observations.
func TestAStalledStoreDropsRatherThanBlocks(t *testing.T) {
	h := newHarness(t, defaultWiring())
	// No capture loop: nothing drains s.captured, so it fills and then drops.
	h.svc.attachCapture()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < capturedQueue+8; i++ {
			h.feed(air(t, uint32(3180200+i), 262993, "fill the queue", false), dmrshim.ToGateway)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the tap blocked when the store queue filled")
	}
	if c := h.svc.Capture(); c.Dropped == 0 {
		t.Errorf("the queue never overflowed; the test did not exercise the drop path: %+v", c)
	}
}

// The capture follows a rebuilt relay. Its ports move on an Apply, and a tap left
// on the old one would quietly stop seeing traffic with nothing reporting a fault.
func TestCaptureFollowsARebuiltRelay(t *testing.T) {
	h := newHarness(t, defaultWiring())
	var mu sync.Mutex
	current := h.relay
	h.svc.relay = func() Relay {
		mu.Lock()
		defer mu.Unlock()
		return current
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.svc.StartCapture(ctx)

	waitTaps := func(r *fakeRelay) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			r.mu.Lock()
			n := len(r.taps)
			r.mu.Unlock()
			if n > 0 {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Fatal("the capture never attached")
	}
	waitTaps(h.relay)

	// Swap the relay, as a port change does, and re-attach by hand rather than
	// waiting out the ticker.
	replacement := &fakeRelay{}
	mu.Lock()
	current = replacement
	mu.Unlock()
	h.svc.attachCapture()
	waitTaps(replacement)

	h.relay = replacement
	h.feed(air(t, 3180202, 262993, "after the rebuild", false), dmrshim.ToGateway)
	got := h.waitMessages(t, 1)
	if got[0].Text != "after the rebuild" {
		t.Errorf("= %q", got[0].Text)
	}
}
