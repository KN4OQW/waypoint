package router

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// These tests are RFC-0003 §6.4 exactly: synthetic frames driven into the hub
// model, asserting the four loop-prevention rules, with no sockets. The router is
// a pure state machine, so this is the whole daemon contract worth testing.

// --- fixtures ---------------------------------------------------------------

type capture struct{ events []hub.Event }

func (c *capture) Publish(e hub.Event) { c.events = append(c.events, e) }

func (c *capture) count(typ string) int {
	n := 0
	for _, e := range c.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}

func attach(m config.Mode) Attachment {
	fm, ok := frameMode(m)
	if !ok {
		panic("test used a non-frame mode: " + m)
	}
	return Attachment{Mode: m, FMode: fm}
}

func newBus(pub Publisher, hang time.Duration, modes ...config.Mode) *Bus {
	cfg := Config{ID: "busA", Name: "Local Bus A", HangTime: hang}
	for _, m := range modes {
		cfg.Attachments = append(cfg.Attachments, attach(m))
	}
	return New(cfg, pub)
}

// cw returns one dummy canonical codeword (AMBEBytes long). The router copies
// codewords opaquely, so their content is irrelevant to arbitration/loop logic.
func cw(b byte) []byte {
	out := make([]byte, frames.AMBEBytes)
	for i := range out {
		out[i] = b
	}
	return out
}

func voice(mode config.Mode, stream uint32, ncw int, fill byte) frames.Frame {
	fm, _ := frameMode(mode)
	ambe := make([][]byte, ncw)
	for i := range ambe {
		ambe[i] = cw(fill + byte(i))
	}
	return frames.Frame{Mode: fm, Kind: frames.KindVoice, SrcID: 3180202, DstID: 91,
		Stream: frames.Stream{ID: stream}, AMBE: ambe}
}

func header(mode config.Mode, stream uint32) frames.Frame {
	fm, _ := frameMode(mode)
	return frames.Frame{Mode: fm, Kind: frames.KindHeader, SrcID: 3180202, DstID: 91,
		Stream: frames.Stream{ID: stream}}
}

func terminator(mode config.Mode, stream uint32) frames.Frame {
	fm, _ := frameMode(mode)
	return frames.Frame{Mode: fm, Kind: frames.KindTerminator, SrcID: 3180202, DstID: 91,
		Stream: frames.Stream{ID: stream}}
}

func dstSet(ems []Emission) map[config.Mode]int {
	m := make(map[config.Mode]int)
	for _, e := range ems {
		m[e.Dst]++
	}
	return m
}

// --- §6.4(a): never emit to origin -----------------------------------------

func TestNeverEmitToOrigin(t *testing.T) {
	pub := &capture{}
	b := newBus(pub, 2*time.Second, config.ModeDMR, config.ModeYSF, config.ModeNXDN)
	t0 := time.Unix(1_700_000_000, 0)

	var all []Emission
	all = append(all, b.Ingest(config.ModeDMR, header(config.ModeDMR, 0xAA), t0)...)
	// Feed enough DMR voice (3 cw each) that both YSF (5/frame) and NXDN (4/frame)
	// emit at least one whole destination frame.
	for i := 0; i < 4; i++ {
		f := voice(config.ModeDMR, 0xAA, 3, byte(i*3))
		all = append(all, b.Ingest(config.ModeDMR, f, t0.Add(time.Duration(i)*60*time.Millisecond))...)
	}

	got := dstSet(all)
	if got[config.ModeDMR] != 0 {
		t.Fatalf("rule 1 violated: %d emissions went back to the DMR origin", got[config.ModeDMR])
	}
	if got[config.ModeYSF] == 0 || got[config.ModeNXDN] == 0 {
		t.Fatalf("expected emissions to both non-origin modes, got %v", got)
	}
	for _, e := range all {
		if e.Dst == config.ModeDMR {
			t.Fatal("no emission may target the source mode")
		}
	}
}

// --- §6.4(b): two simultaneous sources, one token, busy once per stream ------

func TestArbitrationSingleTokenAndBusyOncePerStream(t *testing.T) {
	pub := &capture{}
	b := newBus(pub, 2*time.Second, config.ModeDMR, config.ModeYSF)
	t0 := time.Unix(1_700_000_000, 0)

	// DMR keys up first -> it holds the single token.
	b.Ingest(config.ModeDMR, header(config.ModeDMR, 0x01), t0)
	if h, ok := b.Holder(); !ok || h != config.ModeDMR {
		t.Fatalf("DMR should hold the token, got holder=%q ok=%v", h, ok)
	}

	// YSF tries to talk over it, three frames of the SAME losing stream.
	for i := 0; i < 3; i++ {
		ems := b.Ingest(config.ModeYSF, voice(config.ModeYSF, 0x02, 5, byte(i)), t0.Add(time.Duration(100+i*60)*time.Millisecond))
		if len(ems) != 0 {
			t.Fatalf("loser frame %d produced %d emissions, want 0", i, len(ems))
		}
	}
	if h, _ := b.Holder(); h != config.ModeDMR {
		t.Fatalf("token must stay with DMR while held, holder=%q", h)
	}
	if got := pub.count(EventBusBusy); got != 1 {
		t.Fatalf("bus_busy must fire once per losing stream, got %d", got)
	}
	if b.Dropped() != 3 {
		t.Fatalf("all 3 loser frames should be counted dropped, got %d", b.Dropped())
	}

	// A second, distinct losing stream from YSF -> one more bus_busy (per stream).
	b.Ingest(config.ModeYSF, voice(config.ModeYSF, 0x03, 5, 9), t0.Add(400*time.Millisecond))
	if got := pub.count(EventBusBusy); got != 2 {
		t.Fatalf("a new losing stream should add one bus_busy, got %d", got)
	}

	// The busy event names winner and loser for the UI.
	var busy *hub.Event
	for i := range pub.events {
		if pub.events[i].Type == EventBusBusy {
			busy = &pub.events[i]
			break
		}
	}
	if busy == nil || busy.Source != "DMR" || busy.Mode != "YSF" {
		t.Fatalf("bus_busy should carry winner=DMR loser=YSF, got %+v", busy)
	}
}

// --- §6.4(b cont): token release frees the bus for the former loser ---------

func TestTokenReleaseAfterHang(t *testing.T) {
	pub := &capture{}
	hang := 2 * time.Second
	b := newBus(pub, hang, config.ModeDMR, config.ModeYSF)
	t0 := time.Unix(1_700_000_000, 0)

	b.Ingest(config.ModeDMR, header(config.ModeDMR, 0x01), t0) // DMR holds, then goes silent
	b.Ingest(config.ModeYSF, voice(config.ModeYSF, 0x02, 5, 0), t0.Add(500*time.Millisecond))
	if h, _ := b.Holder(); h != config.ModeDMR {
		t.Fatal("DMR should still hold within hang")
	}

	// Silence past the hang releases the token even with no further traffic.
	b.MaybeRelease(t0.Add(hang + 10*time.Millisecond))
	if _, ok := b.Holder(); ok {
		t.Fatal("token should release after hang-time of silence")
	}

	// The previously losing YSF can now take the bus and be fanned out.
	ems := b.Ingest(config.ModeYSF, header(config.ModeYSF, 0x02), t0.Add(hang+20*time.Millisecond))
	if h, ok := b.Holder(); !ok || h != config.ModeYSF {
		t.Fatalf("YSF should now hold the token, got %q ok=%v", h, ok)
	}
	if dstSet(ems)[config.ModeDMR] == 0 {
		t.Fatal("YSF, now the source, should fan its header out to DMR")
	}
}

// --- §6.4(c): a bus-emitted frame echoed on the DMR loopback is ignored -----

func TestEchoSuppression(t *testing.T) {
	pub := &capture{}
	b := newBus(pub, 2*time.Second, config.ModeYSF, config.ModeDMR)
	t0 := time.Unix(1_700_000_000, 0)

	// YSF holds and is reframed out to DMR; the DMR emissions carry YSF's stream id.
	b.Ingest(config.ModeYSF, header(config.ModeYSF, 0x77), t0)
	ems := b.Ingest(config.ModeYSF, voice(config.ModeYSF, 0x77, 5, 0), t0.Add(60*time.Millisecond))
	emittedToDMR := false
	for _, e := range ems {
		if e.Dst == config.ModeDMR {
			emittedToDMR = true
		}
	}
	if !emittedToDMR {
		t.Fatal("YSF voice should have reframed out to DMR (setup for the echo test)")
	}

	// The DMRGateway loopback echoes that emission back to us: same stream id,
	// arriving on DMR. It must NOT re-enter the fan-out and must NOT count as a
	// competing (busy) source.
	busyBefore := pub.count(EventBusBusy)
	echo := b.Ingest(config.ModeDMR, voice(config.ModeDMR, 0x77, 3, 0), t0.Add(120*time.Millisecond))
	if len(echo) != 0 {
		t.Fatalf("echo of our own emission must not be re-fanned, got %d emissions", len(echo))
	}
	if pub.count(EventBusBusy) != busyBefore {
		t.Fatal("echo must not be surfaced as a busy/competing source")
	}
	if h, _ := b.Holder(); h != config.ModeYSF {
		t.Fatalf("echo must not disturb the token holder, got %q", h)
	}

	// A genuine competing DMR source (a DIFFERENT stream id) is still arbitrated.
	comp := b.Ingest(config.ModeDMR, voice(config.ModeDMR, 0x88, 3, 0), t0.Add(150*time.Millisecond))
	if len(comp) != 0 {
		t.Fatal("a real competing DMR source must be dropped while YSF holds")
	}
	if pub.count(EventBusBusy) != busyBefore+1 {
		t.Fatal("a real competing DMR source (new stream) should raise exactly one bus_busy")
	}
}

// --- §6.4(d): no configuration lets a frame traverse two buses --------------

func TestNoCrossBusTraversal(t *testing.T) {
	// Structural: a Bus only knows its own attachments, so its emissions are always
	// a subset of them — a frame can never reach a mode that lives on another bus.
	pub := &capture{}
	busA := newBus(pub, 2*time.Second, config.ModeDMR, config.ModeYSF) // NXDN is deliberately absent
	t0 := time.Unix(1_700_000_000, 0)

	var all []Emission
	all = append(all, busA.Ingest(config.ModeDMR, header(config.ModeDMR, 1), t0)...)
	for i := 0; i < 6; i++ {
		all = append(all, busA.Ingest(config.ModeDMR, voice(config.ModeDMR, 1, 3, byte(i*3)), t0.Add(time.Duration(i)*60*time.Millisecond))...)
	}
	for _, e := range all {
		if e.Dst != config.ModeYSF {
			t.Fatalf("busA (DMR+YSF) emitted to %q — a frame reached a mode outside the bus", e.Dst)
		}
	}

	// And the store forbids the config that would even put NXDN on two buses: a
	// mode belongs to exactly one attachment across all buses (§5 rule 3).
	buses := []config.Bus{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}
	atts := []config.Attachment{
		{BusID: "a", Mode: config.ModeNXDN},
		{BusID: "b", Mode: config.ModeNXDN}, // same mode, second bus
	}
	if err := config.ValidateBuses(buses, atts, nil); err == nil {
		t.Fatal("ValidateBuses must reject a mode attached to two buses (§5 rule 3)")
	}
}

// --- voice bracketing: one start + one end per transmission -----------------

func TestVoiceStartEndBracketing(t *testing.T) {
	pub := &capture{}
	b := newBus(pub, 2*time.Second, config.ModeDMR, config.ModeYSF)
	t0 := time.Unix(1_700_000_000, 0)

	b.Ingest(config.ModeDMR, header(config.ModeDMR, 1), t0)
	for i := 0; i < 3; i++ {
		b.Ingest(config.ModeDMR, voice(config.ModeDMR, 1, 3, byte(i*3)), t0.Add(time.Duration(i+1)*60*time.Millisecond))
	}
	b.Ingest(config.ModeDMR, terminator(config.ModeDMR, 1), t0.Add(300*time.Millisecond))

	if got := pub.count(EventBusVoiceStart); got != 1 {
		t.Fatalf("want exactly one bus_voice_start, got %d", got)
	}
	if got := pub.count(EventBusVoiceEnd); got != 1 {
		t.Fatalf("want exactly one bus_voice_end, got %d", got)
	}
}
