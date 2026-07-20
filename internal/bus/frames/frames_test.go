package frames

import (
	"bytes"
	"math/rand"
	"reflect"
	"testing"
)

// ambePerFrame is how many AMBE codewords one voice frame of each mode carries.
func ambePerFrame(m Mode) int {
	switch m {
	case ModeDMR:
		return dmrAMBEPerFrm // 3
	case ModeYSF:
		return ysfVCHPerFrame // 5
	case ModeNXDN:
		return nxdnAMBEPerBlk * 2 // 4
	}
	return 0
}

func construct(t *testing.T, m Mode, f Frame, p Params, r Resolver) []byte {
	t.Helper()
	var b []byte
	var err error
	switch m {
	case ModeDMR:
		b, err = ConstructDMR(f, p, r)
	case ModeYSF:
		b, err = ConstructYSF(f, p, r)
	case ModeNXDN:
		b, err = ConstructNXDN(f, p, r)
	}
	if err != nil {
		t.Fatalf("construct %s: %v", m, err)
	}
	return b
}

func parse(t *testing.T, m Mode, b []byte) Frame {
	t.Helper()
	var f Frame
	var err error
	switch m {
	case ModeDMR:
		f, err = ParseDMR(b)
	case ModeYSF:
		f, err = ParseYSF(b)
	case ModeNXDN:
		f, err = ParseNXDN(b)
	}
	if err != nil {
		t.Fatalf("parse %s: %v", m, err)
	}
	return f
}

func randCodewords(rng *rand.Rand, n int) [][]byte {
	out := make([][]byte, n)
	for i := range out {
		out[i] = randCodeword(rng)
	}
	return out
}

// TestFrameRoundTripDMR: parse(construct(f)) == f for a DMR voice frame, header,
// and terminator (RFC-0003 §6 per-mode round trip).
func TestFrameRoundTripDMR(t *testing.T) {
	rng := rand.New(rand.NewSource(10))
	voice := Frame{Mode: ModeDMR, Kind: KindVoice, SrcID: 3180202, DstID: 91,
		Stream: Stream{ID: 0xDEADBEEF, Seq: 7}, AMBE: randCodewords(rng, 3)}
	got := parse(t, ModeDMR, construct(t, ModeDMR, voice, Params{Slot: 2, DefaultTG: 9}, nil))
	if got.SrcID != voice.SrcID || got.DstID != voice.DstID || got.Kind != KindVoice {
		t.Fatalf("DMR addressing/kind lost: %+v", got)
	}
	if got.Stream != voice.Stream {
		t.Fatalf("DMR stream lost: %+v want %+v", got.Stream, voice.Stream)
	}
	if !reflect.DeepEqual(got.AMBE, voice.AMBE) {
		t.Fatalf("DMR AMBE lost:\n want %x\n  got %x", voice.AMBE, got.AMBE)
	}

	for _, k := range []Kind{KindHeader, KindTerminator} {
		g := parse(t, ModeDMR, construct(t, ModeDMR, Frame{Mode: ModeDMR, Kind: k, SrcID: 1, DstID: 2}, Params{}, nil))
		if g.Kind != k {
			t.Fatalf("DMR kind %v lost -> %v", k, g.Kind)
		}
		if len(g.AMBE) != 0 {
			t.Fatalf("DMR %v should carry no AMBE", k)
		}
	}
}

// TestFrameRoundTripYSF: parse(construct(f)) == f for a YSF DN voice frame.
func TestFrameRoundTripYSF(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	voice := Frame{Mode: ModeYSF, Kind: KindVoice, SrcCallsign: "KN4OQW", DstCallsign: "ALL",
		Stream: Stream{Seq: 3}, AMBE: randCodewords(rng, 5)}
	got := parse(t, ModeYSF, construct(t, ModeYSF, voice, Params{}, nil))
	if got.SrcCallsign != "KN4OQW" || got.DstCallsign != "ALL" || got.Kind != KindVoice {
		t.Fatalf("YSF addressing/kind lost: %+v", got)
	}
	if got.Stream.Seq != 3 {
		t.Fatalf("YSF FN lost: %d", got.Stream.Seq)
	}
	if !reflect.DeepEqual(got.AMBE, voice.AMBE) {
		t.Fatalf("YSF AMBE lost:\n want %x\n  got %x", voice.AMBE, got.AMBE)
	}
	for _, k := range []Kind{KindHeader, KindTerminator} {
		g := parse(t, ModeYSF, construct(t, ModeYSF, Frame{Mode: ModeYSF, Kind: k, SrcCallsign: "W1AW"}, Params{}, nil))
		if g.Kind != k {
			t.Fatalf("YSF kind %v lost -> %v", k, g.Kind)
		}
	}
}

// TestFrameRoundTripNXDN: parse(construct(f)) == f for an NXDN voice frame.
func TestFrameRoundTripNXDN(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	voice := Frame{Mode: ModeNXDN, Kind: KindVoice, SrcID: 12345, DstID: 20, AMBE: randCodewords(rng, 4)}
	got := parse(t, ModeNXDN, construct(t, ModeNXDN, voice, Params{}, nil))
	if got.SrcID != 12345 || got.DstID != 20 || got.Kind != KindVoice {
		t.Fatalf("NXDN addressing/kind lost: %+v", got)
	}
	if !reflect.DeepEqual(got.AMBE, voice.AMBE) {
		t.Fatalf("NXDN AMBE lost:\n want %x\n  got %x", voice.AMBE, got.AMBE)
	}
	for _, k := range []Kind{KindHeader, KindTerminator} {
		g := parse(t, ModeNXDN, construct(t, ModeNXDN, Frame{Mode: ModeNXDN, Kind: k, SrcID: 1, DstID: 2}, Params{}, nil))
		if g.Kind != k {
			t.Fatalf("NXDN kind %v lost -> %v", k, g.Kind)
		}
	}
}

// reframeStream models what the hub does: pack a codeword stream into one mode's
// voice frames (grouping by that mode's AMBE-per-frame), then parse them back out.
// The returned stream must be byte-identical to the input, proving the mode's
// frame layer preserves the AMBE. Panics-free; the stream length must be a
// multiple of the mode's per-frame count.
func reframeStream(t *testing.T, m Mode, cws [][]byte) [][]byte {
	t.Helper()
	per := ambePerFrame(m)
	var out [][]byte
	for i := 0; i < len(cws); i += per {
		f := Frame{Mode: m, Kind: KindVoice, SrcID: 3180202, DstID: 9, SrcCallsign: "KN4OQW",
			AMBE: cws[i : i+per]}
		out = append(out, parse(t, m, construct(t, m, f, Params{DefaultTG: 9, DefaultID: 65519, NXDNTG: 20}, nil)).AMBE...)
	}
	return out
}

// TestCrossModeAMBEPreservation is RFC-0003 §2/§6: reframing preserves the AMBE
// byte-exactly across every ordered mode pair (and a full triangle). 60 codewords
// is divisible by 3 (DMR), 4 (NXDN) and 5 (YSF), so no padding is needed.
func TestCrossModeAMBEPreservation(t *testing.T) {
	rng := rand.New(rand.NewSource(20))
	orig := randCodewords(rng, 60)

	modes := []Mode{ModeDMR, ModeYSF, ModeNXDN}
	names := map[Mode]string{ModeDMR: "DMR", ModeYSF: "YSF", ModeNXDN: "NXDN"}

	// Each mode individually preserves the stream (the reframe hop).
	for _, m := range modes {
		if got := reframeStream(t, m, orig); !equalCodewords(got, orig) {
			t.Fatalf("%s reframe hop altered the AMBE", names[m])
		}
	}

	// Every ordered pair src -> dst -> src is byte-exact.
	for _, a := range modes {
		for _, b := range modes {
			if a == b {
				continue
			}
			viaB := reframeStream(t, b, reframeStream(t, a, orig))
			back := reframeStream(t, a, viaB)
			if !equalCodewords(back, orig) {
				t.Fatalf("%s -> %s -> %s did not preserve the AMBE byte-exactly", names[a], names[b], names[a])
			}
		}
	}

	// Full triangle DMR -> YSF -> NXDN -> DMR.
	chain := reframeStream(t, ModeDMR,
		reframeStream(t, ModeNXDN,
			reframeStream(t, ModeYSF,
				reframeStream(t, ModeDMR, orig))))
	if !equalCodewords(chain, orig) {
		t.Fatal("DMR->YSF->NXDN->DMR did not preserve the AMBE")
	}
}

func equalCodewords(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			return false
		}
	}
	return true
}

// TestAddressingResolution: a DMR id resolves to a YSF callsign and back through
// the injected resolver (the shared DMRIds.dat lookup), with numeric fallback on
// a miss.
func TestAddressingResolution(t *testing.T) {
	rng := rand.New(rand.NewSource(30))
	res := &mapResolver{
		byID:   map[uint32]string{3180202: "KN4OQW"},
		byCall: map[string]uint32{"KN4OQW": 3180202},
	}

	// DMR (id) -> YSF: the constructed YSF frame carries the resolved callsign.
	dmr := Frame{Mode: ModeDMR, Kind: KindVoice, SrcID: 3180202, DstID: 9, AMBE: randCodewords(rng, 5)}
	ysf := parse(t, ModeYSF, construct(t, ModeYSF, Frame{Mode: ModeYSF, Kind: KindVoice, SrcID: dmr.SrcID, AMBE: dmr.AMBE}, Params{}, res))
	if ysf.SrcCallsign != "KN4OQW" {
		t.Fatalf("id->callsign resolution failed: %q", ysf.SrcCallsign)
	}

	// YSF (callsign) -> DMR: the constructed DMR frame carries the resolved id.
	back := parse(t, ModeDMR, construct(t, ModeDMR, Frame{Mode: ModeDMR, Kind: KindVoice, SrcCallsign: "KN4OQW", DstID: 9, AMBE: randCodewords(rng, 3)}, Params{}, res))
	if back.SrcID != 3180202 {
		t.Fatalf("callsign->id resolution failed: %d", back.SrcID)
	}

	// Miss: an unknown callsign yields src id 0 (numeric fallback), never a panic.
	miss := parse(t, ModeDMR, construct(t, ModeDMR, Frame{Mode: ModeDMR, Kind: KindVoice, SrcCallsign: "NOCALL", DstID: 9, AMBE: randCodewords(rng, 3)}, Params{}, res))
	if miss.SrcID != 0 {
		t.Fatalf("unknown callsign should fall back to 0, got %d", miss.SrcID)
	}
}

// TestTGMapAndDefault: DMR destination applies tg_map, then passthrough, then default.
func TestTGMapAndDefault(t *testing.T) {
	rng := rand.New(rand.NewSource(31))
	ambe := randCodewords(rng, 3)
	// tg_map hit: source dst 65000 -> DMR TG 91.
	g := parse(t, ModeDMR, construct(t, ModeDMR,
		Frame{Mode: ModeDMR, Kind: KindVoice, DstID: 65000, AMBE: ambe},
		Params{TGMap: map[uint32]uint32{65000: 91}, DefaultTG: 9}, nil))
	if g.DstID != 91 {
		t.Fatalf("tg_map not applied: %d", g.DstID)
	}
	// default: no dst, no map -> default_tg.
	g = parse(t, ModeDMR, construct(t, ModeDMR,
		Frame{Mode: ModeDMR, Kind: KindVoice, DstID: 0, AMBE: ambe},
		Params{DefaultTG: 9}, nil))
	if g.DstID != 9 {
		t.Fatalf("default_tg not applied: %d", g.DstID)
	}
}

// mapResolver is a test Resolver backed by two maps.
type mapResolver struct {
	byID   map[uint32]string
	byCall map[string]uint32
}

func (m *mapResolver) CallsignForID(id uint32) string { return m.byID[id] }
func (m *mapResolver) IDForCallsign(cs string) uint32 { return m.byCall[cs] }
