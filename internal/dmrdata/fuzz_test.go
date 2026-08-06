package dmrdata

import (
	"math/rand"
	"testing"
	"time"
)

// These parsers see whatever is on the air. A neighbouring repeater's data, a
// radio with a firmware quirk, a burst mangled past what the FEC can fix — all of
// it arrives at ParseBurst and Feed, and none of it may panic or run away.
//
// Run a smoke pass with:
//
//	go test -run x -fuzz FuzzFeed -fuzztime 30s ./internal/dmrdata/
//
// The f.Add seeds are the committed corpus: real recorded bursts plus the
// malformed shapes most likely to trip a length or offset bug.

func fuzzSeeds(t *testing.F) [][]byte {
	t.Helper()
	var seeds [][]byte
	for _, name := range []string{"capture-brandmeister.txt", "capture-radio-etsi.txt", "capture-nosync.txt"} {
		for _, b := range loadFixture(t, name) {
			seeds = append(seeds, b.Payload)
		}
	}
	return append(seeds,
		nil,
		make([]byte, BurstBytes-1),
		make([]byte, BurstBytes),
		make([]byte, BurstBytes+1),
		bytesOf(0xFF, BurstBytes),
		bytesOf(0xAA, BurstBytes),
	)
}

func bytesOf(b byte, n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// FuzzParseBurst: any input either errors or yields a payload that re-encodes.
func FuzzParseBurst(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		payload, dt, cc, _, _, err := ParseBurst(data)
		if err != nil {
			return
		}
		if len(payload) != PayloadBytes {
			t.Fatalf("parsed payload is %d bytes", len(payload))
		}
		if _, err := ReEncodeBurst(data, payload); err != nil {
			t.Fatalf("a burst that parsed would not re-encode: %v", err)
		}
		// Whatever came out has to be buildable again, since the shim's job is to
		// pass frames along.
		if _, err := BuildBurst(payload, dt, cc, true); err != nil {
			t.Fatalf("BuildBurst refused a parsed burst: %v", err)
		}
	})
}

// FuzzFeed drives the whole receive path — FEC, PDU parse, reassembly, tunnel,
// checksum — with one arbitrary burst at a time. The header PDU's block count and
// pad are attacker-controlled, so this is where an out-of-range slice would show.
func FuzzFeed(f *testing.F) {
	for _, s := range fuzzSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var r Reassembler
		now := time.Unix(1780000000, 0)
		for i := 0; i < 4; i++ {
			if m, ok := r.Feed(data, now.Add(time.Duration(i)*time.Second)); ok && m == nil {
				t.Fatal("Feed reported a message and returned nil")
			}
		}
	})
}

// FuzzBuildMessage: no text, address or option may make the builder panic, and
// anything it does build must survive its own reassembler.
func FuzzBuildMessage(f *testing.F) {
	f.Add(uint32(3180202), uint32(262995), "hello", 9, false)
	f.Add(uint32(0), uint32(0), "", 0, true)
	f.Add(uint32(0xFFFFFF), uint32(0xFFFFFF), "café 73 — µW", 1, false)
	f.Fuzz(func(t *testing.T, src, dst uint32, text string, preambles int, group bool) {
		if preambles < 0 || preambles > 64 {
			return // the caller's range; unbounded values only make the fuzzer slow
		}
		bursts, err := BuildMessage(SendOptions{
			Src: src, Dst: dst, Text: text, Preambles: preambles, Group: group,
		})
		if err != nil {
			return
		}
		var r Reassembler
		now := time.Unix(1780000000, 0)
		var got *Message
		for _, b := range bursts {
			if m, ok := r.Feed(b.Payload[:], now); ok {
				got = m
			}
		}
		if got == nil {
			t.Fatalf("a message this package built did not reassemble: %+v", r.Stats)
		}
		if got.Text != text {
			t.Fatalf("text round-tripped as %q, want %q", got.Text, text)
		}
	})
}

// The always-on companion: a deterministic sweep that runs in normal CI, so the
// no-panic property is checked on every test run and not only when somebody
// remembers to fuzz.
func TestReceivePathSurvivesGarbage(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	var r Reassembler
	now := time.Unix(1780000000, 0)
	for i := 0; i < 20000; i++ {
		b := make([]byte, rng.Intn(40))
		rng.Read(b)
		// Half the time make it burst-shaped with a plausible slot type, so the
		// paths past the length check actually run.
		if len(b) >= BurstBytes && rng.Intn(2) == 0 {
			b = b[:BurstBytes]
			setSlotType(b, byte(rng.Intn(16)), DataType(rng.Intn(16)))
			addDataSync(b, rng.Intn(2) == 0)
		}
		_, _, _, _, _, _ = ParseBurst(b)
		r.Feed(b, now.Add(time.Duration(i)*time.Millisecond))
	}
	if r.Stats.Messages != 0 {
		t.Errorf("random bytes produced %d messages", r.Stats.Messages)
	}
}
