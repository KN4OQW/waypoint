package dmrdata

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// TestBuildMessageMatchesGoldenFixtures is the one that says this port is a port.
//
// The fixtures were produced by g4klx MMDVMHost's own BPTC, slot type and sync
// encoders — the C++ this package was translated from — driven by the message
// construction proven to display on the radio on 2026-08-06. A single byte out of
// place anywhere in the chain shows up here, and nowhere else does the whole chain
// get compared against an independent implementation.
func TestBuildMessageMatchesGoldenFixtures(t *testing.T) {
	for _, tc := range []struct {
		fixture   string
		text      string
		preambles int
	}{
		{"golden-hello.txt", "hello", 9},
		{"golden-multiblock.txt", "The quick brown fox jumps over the lazy dog 0123456789", 3},
		{"golden-unicode.txt", "café 73 — µW × QRP", 9},
		{"golden-maxlen.txt", strings.Repeat("A", MaxTextUnits), 1},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			want := loadFixture(t, tc.fixture)
			got, err := BuildMessage(SendOptions{
				Src: 1234567, Dst: 7654321, Text: tc.text, Seq: 1,
				Preambles: tc.preambles, ColorCode: 1, Duplex: true,
			})
			if err != nil {
				t.Fatalf("BuildMessage: %v", err)
			}
			if len(got) != len(want) {
				t.Fatalf("built %d bursts, fixture has %d", len(got), len(want))
			}
			for i := range got {
				if got[i].DataType != want[i].DataType {
					t.Errorf("burst %d: data type %v, want %v", i, got[i].DataType, want[i].DataType)
				}
				if !bytes.Equal(got[i].Payload[:], want[i].Payload) {
					t.Errorf("burst %d differs\n got %x\nwant %x", i, got[i].Payload, want[i].Payload)
				}
			}
		})
	}
}

func TestBuildMessageShape(t *testing.T) {
	got, err := BuildMessage(SendOptions{Src: 1, Dst: 2, Text: "hi", Preambles: 4})
	if err != nil {
		t.Fatal(err)
	}
	// Four preambles, one data header, then the body.
	for i := 0; i < 4; i++ {
		if got[i].DataType != DataTypeCSBK {
			t.Fatalf("burst %d is %v, want a preamble", i, got[i].DataType)
		}
	}
	if got[4].DataType != DataTypeDataHeader {
		t.Fatalf("burst 4 is %v, want the data header", got[4].DataType)
	}
	for i := 5; i < len(got); i++ {
		if got[i].DataType != DataTypeRate12 {
			t.Fatalf("burst %d is %v, want a body block", i, got[i].DataType)
		}
	}
	// Every burst, not just the ones a spot check would reach.
	for i, b := range got {
		if !hasDataSync(b.Payload[:]) {
			t.Errorf("burst %d has no sync", i)
		}
		if _, dt := getSlotType(b.Payload[:]); dt != b.DataType {
			t.Errorf("burst %d: slot type %v disagrees with %v", i, dt, b.DataType)
		}
	}
}

func TestBuildMessageRejections(t *testing.T) {
	if _, err := BuildMessage(SendOptions{Src: 0x1000000, Dst: 2, Text: "x"}); err != ErrBadAddress {
		t.Errorf("25-bit source: err = %v, want ErrBadAddress", err)
	}
	if _, err := BuildMessage(SendOptions{Src: 1, Dst: 0x1000000, Text: "x"}); err != ErrBadAddress {
		t.Errorf("25-bit destination: err = %v, want ErrBadAddress", err)
	}
	if _, err := BuildMessage(SendOptions{Src: 1, Dst: 2, Text: strings.Repeat("A", MaxTextUnits+1)}); err != ErrTextTooLong {
		t.Errorf("over-length: err = %v, want ErrTextTooLong", err)
	}
}

// --- Reassembly ---------------------------------------------------------------

// feed pushes a whole built message through a reassembler and returns whatever
// came out, so the tests below read as "transmit this, receive that".
func feed(t *testing.T, r *Reassembler, bursts []Burst, at time.Time) []*Message {
	t.Helper()
	var out []*Message
	for _, b := range bursts {
		if m, ok := r.Feed(b.Payload[:], at); ok {
			out = append(out, m)
		}
	}
	return out
}

func TestReassembleRoundTrip(t *testing.T) {
	for _, text := range []string{"", "A", "hello", "café 73 — MHz", strings.Repeat("A", MaxTextUnits)} {
		bursts, err := BuildMessage(SendOptions{Src: 3180202, Dst: 262995, Text: text, Seq: 7})
		if err != nil {
			t.Fatal(err)
		}
		var r Reassembler
		got := feed(t, &r, bursts, t0)
		if len(got) != 1 {
			t.Fatalf("%q: got %d messages, want 1 (stats %+v)", text, len(got), r.Stats)
		}
		if got[0].Text != text {
			t.Errorf("text = %q, want %q", got[0].Text, text)
		}
		if got[0].Src != 3180202 || got[0].Dst != 262995 {
			t.Errorf("addresses = %d -> %d", got[0].Src, got[0].Dst)
		}
		if r.Stats.Messages != 1 || r.Stats.NoSync != 0 {
			t.Errorf("stats = %+v", r.Stats)
		}
	}
}

// The real thing, end to end: bursts recorded off the modem, through the FEC, the
// PDU layer, the tunnel and the checksum, out as the text that was on the radio's
// screen.
func TestReassembleRecordedCaptures(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    []string
	}{
		{"capture-brandmeister.txt", []string{
			"Unknown Format, please start your SMS with the CallSign followed by a SPACE and the message.",
		}},
		// Three separate messages in one recording, so this also exercises a source
		// sending several transfers in sequence.
		{"capture-radio-etsi.txt", []string{"TEST", "HELLO", "HELLO"}},
		// The same radio in M-SMS rather than DMR Standard, addressed to 9000001 —
		// a bot-range id nothing answers to. It decodes anyway, which is the
		// property the bot framework's intercept depends on, and it decodes as
		// UNCONFIRMED data, which is what keeps the inbound codec small.
		{"capture-radio-tms.txt", []string{"TFRT WAYPOINT"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			var r Reassembler
			var got []string
			for _, b := range loadFixture(t, tc.fixture) {
				if m, ok := r.Feed(b.Payload, t0); ok {
					got = append(got, m.Text)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d messages %q, want %d (stats %+v)", len(got), got, len(tc.want), r.Stats)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("message %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
			if r.Stats.BadCRC != 0 || r.Stats.Malformed != 0 || r.Stats.NoSync != 0 {
				t.Errorf("a recorded message produced failures: %+v", r.Stats)
			}
		})
	}
}

// BrandMeister and the radio disagree about the TMS class and option octets — 0xE0
// and 0x64 against the 0xA0 and 0x04 this package emits — and both display on the
// radio. That difference was a prime suspect for two days and was never the fault.
// Nothing here reads those octets; this pins that they stay unread, by decoding a
// recorded message that carries values BuildMessage never produces.
func TestTMSClassOctetsAreNotInterpreted(t *testing.T) {
	var body []byte
	for _, b := range loadFixture(t, "capture-brandmeister.txt") {
		payload, dt, _, _, _, err := ParseBurst(b.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if dt == DataTypeRate12 {
			body = append(body, payload...)
		}
	}
	// Octet 30 is the TMS class and 33 the option, counted from the start of the
	// datagram. Assert they really are the values this package does not send,
	// otherwise the test proves nothing.
	if body[30] != 0xE0 || body[33] != 0x64 {
		t.Fatalf("recorded TMS octets are %02x/%02x, want e0/64", body[30], body[33])
	}
	mine, _, _, err := buildBody(262995, 3180202, "x", 0, false, DialectTMS)
	if err != nil {
		t.Fatal(err)
	}
	if mine[30] == body[30] {
		t.Fatal("this package now emits BrandMeister's class octet; the test no longer contrasts anything")
	}
	msg, err := parseBody(body)
	if err != nil {
		t.Fatalf("parseBody rejected a message over its TMS octets: %v", err)
	}
	if !strings.HasPrefix(msg.Text, "Unknown Format") {
		t.Errorf("text = %q", msg.Text)
	}
}

// The historical failure, replayed from the artefact itself: bursts that carry a
// perfect payload and nothing else. They must be counted, not silently dropped —
// a drop with no counter behind it is why this took two days.
func TestReassemblerCountsTheNoSyncFailure(t *testing.T) {
	var r Reassembler
	fixture := loadFixture(t, "capture-nosync.txt")
	for _, b := range fixture {
		if _, ok := r.Feed(b.Payload, t0); ok {
			t.Fatal("a message reassembled from bursts with no sync")
		}
	}
	if r.Stats.NoSync != len(fixture) {
		t.Errorf("NoSync = %d, want %d", r.Stats.NoSync, len(fixture))
	}
	if r.Stats.Messages != 0 {
		t.Errorf("Messages = %d, want 0", r.Stats.Messages)
	}
}

func TestReassemblerFailureModes(t *testing.T) {
	build := func(t *testing.T, src uint32, text string) []Burst {
		t.Helper()
		b, err := BuildMessage(SendOptions{Src: src, Dst: 262995, Text: text, Preambles: 1})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	t.Run("a body block with no header before it is orphaned", func(t *testing.T) {
		var r Reassembler
		bursts := build(t, 3180202, "hello")
		if _, ok := r.Feed(bursts[len(bursts)-1].Payload[:], t0); ok {
			t.Fatal("a lone body block produced a message")
		}
		if r.Stats.Orphaned != 1 {
			t.Errorf("Orphaned = %d, want 1", r.Stats.Orphaned)
		}
	})

	t.Run("a damaged body fails the CRC rather than yielding garbage", func(t *testing.T) {
		var r Reassembler
		bursts := build(t, 3180202, "hello there this is a longer one")
		// Wreck a body block past what the FEC can repair. The damage stays inside
		// the payload octets: hitting the sync would trip the NoSync counter
		// instead, which is a different failure and has its own test.
		last := len(bursts) - 1
		for _, i := range []int{0, 3, 7, 11, 21, 24, 28, 32} {
			bursts[last].Payload[i] ^= 0xFF
		}
		if got := feed(t, &r, bursts, t0); len(got) != 0 {
			t.Fatalf("got %d messages from a wrecked body", len(got))
		}
		if r.Stats.BadCRC == 0 && r.Stats.Malformed == 0 {
			t.Errorf("a wrecked body was not counted as a failure: %+v", r.Stats)
		}
	})

	t.Run("a partial transfer is abandoned after the timeout", func(t *testing.T) {
		r := Reassembler{Timeout: time.Second}
		bursts := build(t, 3180202, "hello there this is a longer one")
		// Header plus one block, then silence.
		for _, b := range bursts[1:3] {
			r.Feed(b.Payload[:], t0)
		}
		r.Feed(bursts[0].Payload[:], t0.Add(2*time.Second)) // any later burst drives the clock
		if r.Stats.Abandoned != 1 {
			t.Errorf("Abandoned = %d, want 1", r.Stats.Abandoned)
		}
	})

	t.Run("a second header from the same source supersedes the first", func(t *testing.T) {
		var r Reassembler
		first := build(t, 3180202, "this one is abandoned mid transfer")
		second := build(t, 3180202, "and this one arrives")
		for _, b := range first[1:3] {
			r.Feed(b.Payload[:], t0)
		}
		got := feed(t, &r, second, t0)
		if len(got) != 1 || got[0].Text != "and this one arrives" {
			t.Fatalf("got %v, want the second message (stats %+v)", got, r.Stats)
		}
	})

	t.Run("a header with a checksum error is counted", func(t *testing.T) {
		var r Reassembler
		bursts := build(t, 3180202, "hello")
		hdr := bursts[1]
		payload, _, _ := bptcDecode(hdr.Payload[:])
		payload[11] ^= 0xFF // break the CRC, leave the rest readable
		out, err := ReEncodeBurst(hdr.Payload[:], payload)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := r.Feed(out[:], t0); ok {
			t.Fatal("a header with a bad checksum was accepted")
		}
		if r.Stats.BadHeader != 1 {
			t.Errorf("BadHeader = %d, want 1 (stats %+v)", r.Stats.BadHeader, r.Stats)
		}
	})

	t.Run("preambles are not mistaken for anything", func(t *testing.T) {
		var r Reassembler
		bursts := build(t, 3180202, "hello")
		if _, ok := r.Feed(bursts[0].Payload[:], t0); ok {
			t.Fatal("a preamble produced a message")
		}
		if r.Stats != (ReassemblyStats{}) {
			t.Errorf("a preamble was counted as something: %+v", r.Stats)
		}
	})
}

// Two sources sending at once cannot happen on one timeslot, but a shim tapping
// both slots will interleave them. The reassembler must not braid the two bodies
// together into one wrong message; failing to reassemble is fine, silently
// producing the wrong text is not.
func TestInterleavedSourcesDoNotProduceAWrongMessage(t *testing.T) {
	a, err := BuildMessage(SendOptions{Src: 3180202, Dst: 262995, Text: "message from A", Preambles: 1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := BuildMessage(SendOptions{Src: 3180299, Dst: 262995, Text: "message from B", Preambles: 1})
	if err != nil {
		t.Fatal(err)
	}

	var r Reassembler
	var got []string
	for i := 0; i < len(a) || i < len(b); i++ {
		for _, src := range [][]Burst{a, b} {
			if i < len(src) {
				if m, ok := r.Feed(src[i].Payload[:], t0); ok {
					got = append(got, m.Text)
				}
			}
		}
	}
	for _, text := range got {
		if text != "message from A" && text != "message from B" {
			t.Errorf("interleaving produced %q, which nobody sent", text)
		}
	}
}
