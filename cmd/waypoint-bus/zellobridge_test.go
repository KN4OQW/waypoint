package main

import (
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/router"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/talkeralias"
)

// The real vocoder is the MD380's firmware on 32-bit ARM and the real Opus codec
// is cgo, so neither can run in CI. The arithmetic below — how many samples make
// a packet, how many codewords make a frame, what is held back — is where the
// mistakes live, and it is neither ARM-specific nor codec-specific.

type fakeVocoder struct {
	encoded int
	decoded int
}

func (f *fakeVocoder) Decode(cw []byte) ([]int16, error) {
	f.decoded++
	pcm := make([]int16, vocoderFrameSamples)
	for i := range pcm {
		pcm[i] = int16(cw[0]) // recognisable, so a test can trace which codeword it came from
	}
	return pcm, nil
}

func (f *fakeVocoder) Encode(pcm []int16) ([]byte, error) {
	f.encoded++
	return []byte{byte(f.encoded), 0, 0, 0, 0, 0, 0x80}, nil
}

// fakeOpusEncoder reuses one output buffer, exactly as libopus wrappers do. That
// is not incidental to the test: it is the reason FromBus has to copy.
type fakeOpusEncoder struct {
	samples int
	buf     []byte
	calls   int
}

func (f *fakeOpusEncoder) FrameSamples() int { return f.samples }
func (f *fakeOpusEncoder) Encode(pcm []int16) ([]byte, error) {
	f.calls++
	if f.buf == nil {
		f.buf = make([]byte, 4)
	}
	f.buf[0] = byte(f.calls)
	f.buf[1] = byte(len(pcm) >> 8)
	f.buf[2] = byte(len(pcm))
	return f.buf[:3], nil
}

type fakeOpusDecoder struct{ samples int }

func (f *fakeOpusDecoder) Decode(pkt []byte) ([]int16, error) {
	return make([]int16, f.samples), nil
}

func bridgeFor(packetMS int) (*zelloBridge, *fakeVocoder, *fakeOpusEncoder) {
	voc := &fakeVocoder{}
	enc := &fakeOpusEncoder{samples: 16 * packetMS} // 16 samples per ms at 16 kHz
	dec := &fakeOpusDecoder{samples: 16 * packetMS}
	return newZelloBridge(voc, enc, dec, packetMS), voc, enc
}

func busFrame(n int) [][]byte {
	cws := make([][]byte, n)
	for i := range cws {
		cws[i] = []byte{byte(i + 1), 0, 0, 0, 0, 0, 0x80}
	}
	return cws
}

// The default configuration should buffer nothing: three codewords is 60 ms and
// so is a default Zello packet, so one bus frame is exactly one packet.
func TestSixtyMillisecondPacketsAlignWithABusFrame(t *testing.T) {
	b, voc, _ := bridgeFor(60)

	pkts, err := b.FromBus(busFrame(codewordsPerBusFrame))
	if err != nil {
		t.Fatalf("FromBus: %v", err)
	}
	if len(pkts) != 1 {
		t.Fatalf("got %d packets from one bus frame, want 1", len(pkts))
	}
	if voc.decoded != codewordsPerBusFrame {
		t.Errorf("decoded %d codewords, want %d", voc.decoded, codewordsPerBusFrame)
	}
	if len(b.toZello) != 0 {
		t.Errorf("%d samples left buffered; 60 ms should divide exactly", len(b.toZello))
	}
}

func TestTwentyMillisecondPacketsSplitABusFrame(t *testing.T) {
	b, _, _ := bridgeFor(20)

	pkts, err := b.FromBus(busFrame(codewordsPerBusFrame))
	if err != nil {
		t.Fatalf("FromBus: %v", err)
	}
	if len(pkts) != 3 {
		t.Fatalf("got %d packets, want 3 (60 ms of audio at 20 ms a packet)", len(pkts))
	}
	if len(b.toZello) != 0 {
		t.Errorf("%d samples left buffered", len(b.toZello))
	}
}

// 40 ms divides neither way. One bus frame yields one packet and holds 20 ms;
// the next completes a second and a third. Padding the remainder would put
// invented silence on the far end in the middle of an over.
func TestAPacketSizeThatDoesNotDivideHoldsTheRemainder(t *testing.T) {
	b, _, _ := bridgeFor(40)

	first, err := b.FromBus(busFrame(codewordsPerBusFrame))
	if err != nil {
		t.Fatalf("FromBus: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first frame gave %d packets, want 1", len(first))
	}
	if want := 20 * vocoderRate / 1000; len(b.toZello) != want {
		t.Errorf("buffered %d samples, want %d (20 ms)", len(b.toZello), want)
	}

	// The held 20 ms plus another 60 ms is 80 ms, which is two whole 40 ms
	// packets and nothing left over. The pattern is 1, 2, 1, 2 — not one packet
	// per frame — and that is the arithmetic worth pinning, because a bridge that
	// assumed one-in-one-out would fall a packet further behind every other frame.
	second, err := b.FromBus(busFrame(codewordsPerBusFrame))
	if err != nil {
		t.Fatalf("FromBus: %v", err)
	}
	if len(second) != 2 {
		t.Errorf("second frame gave %d packets, want 2", len(second))
	}
	if len(b.toZello) != 0 {
		t.Errorf("%d samples left after two frames; 80 ms is exactly two 40 ms packets", len(b.toZello))
	}
}

// libopus wrappers hand back a slice of a reused buffer. Keeping it would give
// every packet in a burst the last one's bytes — audio that is wrong rather than
// missing, and identical in length so nothing downstream notices.
func TestPacketsAreCopiedOutOfTheEncodersBuffer(t *testing.T) {
	b, _, _ := bridgeFor(20)

	pkts, err := b.FromBus(busFrame(codewordsPerBusFrame))
	if err != nil {
		t.Fatalf("FromBus: %v", err)
	}
	if len(pkts) < 2 {
		t.Fatalf("need at least two packets to detect aliasing, got %d", len(pkts))
	}
	if pkts[0][0] == pkts[1][0] {
		t.Errorf("packets 0 and 1 both start %#x; they alias the encoder's buffer", pkts[0][0])
	}
}

// Inbound, the router can only reframe whole groups, so a partial one is held
// rather than emitted short.
func TestInboundAudioIsGroupedIntoWholeBusFrames(t *testing.T) {
	b, voc, _ := bridgeFor(20) // 20 ms in = one codeword out per packet

	for i := 1; i <= 2; i++ {
		got, err := b.ToBus([]byte{1, 2, 3})
		if err != nil {
			t.Fatalf("ToBus: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("packet %d produced %d frames; a group is %d codewords", i, len(got), codewordsPerBusFrame)
		}
	}
	got, err := b.ToBus([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("ToBus: %v", err)
	}
	if len(got) != 1 || len(got[0]) != codewordsPerBusFrame {
		t.Fatalf("third packet gave %d frames; want 1 of %d codewords", len(got), codewordsPerBusFrame)
	}
	if voc.encoded != codewordsPerBusFrame {
		t.Errorf("encoded %d codewords, want %d", voc.encoded, codewordsPerBusFrame)
	}
}

func TestResetDropsPartialAudio(t *testing.T) {
	b, _, _ := bridgeFor(40)
	if _, err := b.FromBus(busFrame(codewordsPerBusFrame)); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ToBus([]byte{1}); err != nil {
		t.Fatal(err)
	}
	b.Reset()
	if len(b.toZello) != 0 || len(b.toBus) != 0 || len(b.pendingCW) != 0 {
		t.Errorf("Reset left %d/%d/%d buffered", len(b.toZello), len(b.toBus), len(b.pendingCW))
	}
}

// --- the router integration, which is the point of the synthetic mode ---------

type capturingPublisher struct{ events []hub.Event }

func (c *capturingPublisher) Publish(e hub.Event) { c.events = append(c.events, e) }

func zelloBusFixture() (*router.Bus, *capturingPublisher, config.Mode) {
	zm, zf := zelloAttachment("chan1")
	pub := &capturingPublisher{}
	cfg := router.Config{
		ID: "b1", Name: "Bus 1", HangTime: 3 * time.Second,
		Attachments: []router.Attachment{
			{Mode: config.ModeDMR, FMode: frames.ModeDMR},
			{Mode: zm, FMode: zf},
		},
	}
	return router.New(cfg, pub), pub, zm
}

func voiceFrame(stream uint32, n int) frames.Frame {
	return frames.Frame{Kind: frames.KindVoice, Stream: frames.Stream{ID: stream}, AMBE: busFrame(n)}
}

// A Zello endpoint is an ordinary attachment as far as the router is concerned,
// so RF audio fans out to it with no special case anywhere in the router.
func TestBusFansOutToAZelloEndpoint(t *testing.T) {
	b, _, zm := zelloBusFixture()

	out := b.Ingest(config.ModeDMR, voiceFrame(1, 3), time.Now())
	if len(out) != 1 {
		t.Fatalf("got %d emissions, want 1", len(out))
	}
	if out[0].Dst != zm {
		t.Errorf("emitted to %q, want %q", out[0].Dst, zm)
	}
	if len(out[0].Frame.AMBE) != codewordsPerBusFrame {
		t.Errorf("emitted %d codewords, want %d", len(out[0].Frame.AMBE), codewordsPerBusFrame)
	}
}

// And the reverse: audio arriving from Zello reaches the RF attachment.
func TestZelloAudioReachesTheModeAttachment(t *testing.T) {
	b, _, zm := zelloBusFixture()

	out := b.Ingest(zm, voiceFrame(7, 3), time.Now())
	if len(out) != 1 {
		t.Fatalf("got %d emissions, want 1", len(out))
	}
	if out[0].Dst != config.ModeDMR {
		t.Errorf("emitted to %q, want dmr", out[0].Dst)
	}
}

// RFC-0003 §5 rule 1, inherited rather than reimplemented: a Zello endpoint never
// receives its own audio back, which is what would otherwise loop a channel's
// traffic straight back into it.
func TestAZelloEndpointNeverReceivesItsOwnAudio(t *testing.T) {
	b, _, zm := zelloBusFixture()

	for _, e := range b.Ingest(zm, voiceFrame(7, 3), time.Now()) {
		if e.Dst == zm {
			t.Fatal("the bus emitted a Zello endpoint's own audio back to it")
		}
	}
}

// §5 rule 2, also inherited: a Zello talker arriving while RF holds the token
// loses, and surfaces as the same bus_busy event the UI already renders for a
// mode. Nothing about arbitration was written for this feature.
func TestAZelloTalkerLosesToTheTokenHolderAndSaysSo(t *testing.T) {
	b, pub, zm := zelloBusFixture()
	now := time.Now()

	b.Ingest(config.ModeDMR, voiceFrame(1, 3), now)
	out := b.Ingest(zm, voiceFrame(2, 3), now.Add(20*time.Millisecond))
	if len(out) != 0 {
		t.Fatalf("the losing Zello stream produced %d emissions, want 0", len(out))
	}

	var busy int
	for _, e := range pub.events {
		if e.Type == router.EventBusBusy {
			busy++
		}
	}
	if busy != 1 {
		t.Fatalf("got %d bus_busy events, want exactly 1 per losing stream", busy)
	}
}

// The synthetic mode has to be distinguishable from every real one, or a Zello
// endpoint could collide with an RF attachment and silently take its place in the
// arbitration map.
func TestTheSyntheticModeCannotCollideWithARealOne(t *testing.T) {
	real := []config.Mode{config.ModeDMR, config.ModeYSF, config.ModeNXDN, config.ModeDStar,
		config.ModeP25, config.ModeM17, config.ModeYSFVW, config.ModeFM, config.ModePOCSAG, config.ModeModem}
	for _, id := range []string{"chan1", "dmr", "ysf", ""} {
		zm := zelloMode(id)
		for _, r := range real {
			if zm == r {
				t.Errorf("zelloMode(%q) = %q, which collides with the real mode %q", id, zm, r)
			}
		}
	}
	if a, bm := zelloMode("one"), zelloMode("two"); a == bm {
		t.Error("two endpoints on one bus share a mode token")
	}
}

// --- Talker Alias -------------------------------------------------------------

func aliasVoice(stream uint32, from string) frames.Frame {
	return frames.Frame{
		Kind: frames.KindVoice, SrcID: 3180202, SrcCallsign: from,
		Stream: frames.Stream{ID: stream}, AMBE: busFrame(codewordsPerBusFrame),
	}
}

// One alias per transmission, not one per frame. A radio needs it once; sending
// it with every voice frame would put a burst of DMRA on the seam for the whole
// over.
func TestTheAliasIsEmittedOncePerTransmission(t *testing.T) {
	a := newZelloAliaser("callsign", 3180202)
	if a == nil {
		t.Fatal("aliaser was disabled for a valid template and id")
	}

	first := a.framesFor(aliasVoice(1, "Booting6228"))
	if len(first) == 0 {
		t.Fatal("no alias on the first frame of a transmission")
	}
	for i := 0; i < 5; i++ {
		if got := a.framesFor(aliasVoice(1, "Booting6228")); got != nil {
			t.Fatalf("frame %d of the same stream emitted another alias", i+2)
		}
	}

	// A new transmission gets its own.
	if got := a.framesFor(aliasVoice(2, "Someone Else")); len(got) == 0 {
		t.Error("a second transmission got no alias")
	}
}

// The alias decodes back to the Zello name and the node's own DMR ID. This is the
// end the radio sees, so it is worth asserting against the real decoder rather
// than trusting the encoder.
func TestTheAliasCarriesTheZelloNameAndTheNodesID(t *testing.T) {
	a := newZelloAliaser("callsign", 3180202)
	out := a.framesFor(aliasVoice(1, "Booting6228"))
	if len(out) == 0 {
		t.Fatal("no alias frames")
	}
	id, alias, _, err := talkeralias.Decode(out)
	if err != nil {
		t.Fatalf("the emitted frames do not decode: %v", err)
	}
	if id != 3180202 {
		t.Errorf("alias source id = %d, want the node's own 3180202", id)
	}
	// Case preserved. A Zello account name is a display name, not a callsign, and
	// putting "BOOTING6228" on a radio misrepresents what the operator is called.
	if alias != "Booting6228" {
		t.Errorf("alias = %q, want the Zello username with its case intact", alias)
	}
}

// A transmission Zello did not name gets no alias rather than an invented one.
// Announcing a guess is worse than announcing nothing.
func TestNoAliasWithoutAName(t *testing.T) {
	a := newZelloAliaser("callsign", 3180202)
	if got := a.framesFor(aliasVoice(1, "")); got != nil {
		t.Errorf("an unnamed talker produced an alias: %v", got)
	}
}

// Off is the default and must emit nothing at all — not an empty frame, nothing —
// so a node that has not asked for this puts no DMRA on the seam.
func TestTheAliaserIsOffByDefault(t *testing.T) {
	if a := newZelloAliaser("", 3180202); a != nil {
		t.Error("an empty template produced an aliaser")
	}
	if a := newZelloAliaser("not-a-template", 3180202); a != nil {
		t.Error("an unrecognised template produced an aliaser")
	}
	// No ID means nothing to attribute the alias to. Encoding would fail anyway;
	// refusing here keeps the failure at configuration rather than per frame.
	if a := newZelloAliaser("callsign", 0); a != nil {
		t.Error("a zero source id produced an aliaser")
	}
	// The nil aliaser must be safe to call, because handleFrame does.
	var nilA *zelloAliaser
	if got := nilA.framesFor(aliasVoice(1, "x")); got != nil {
		t.Error("a nil aliaser emitted frames")
	}
}

// The per-stream memory has to be released, or a long-running bus accumulates an
// entry for every transmission it has ever carried.
func TestTheAliaserForgetsAStreamWhenItEnds(t *testing.T) {
	a := newZelloAliaser("callsign", 3180202)
	a.framesFor(aliasVoice(1, "Booting6228"))
	if len(a.sent) != 1 {
		t.Fatalf("sent has %d entries, want 1", len(a.sent))
	}
	a.framesFor(frames.Frame{Kind: frames.KindTerminator, Stream: frames.Stream{ID: 1}})
	if len(a.sent) != 0 {
		t.Errorf("sent still has %d entries after the terminator", len(a.sent))
	}
}
