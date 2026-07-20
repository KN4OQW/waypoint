package frames

import (
	"math/rand"
	"testing"
)

// The parsers must never panic on hostile input — a bus daemon reads these off a
// UDP socket. These fuzz targets assert that: any byte slice either parses to a
// Frame or returns an error, and a parsed Frame re-constructs without panicking.
// Run a smoke pass with:  go test -run x -fuzz FuzzParseDMR -fuzztime 20s ./...
//
// The f.Add seeds ARE the committed seed corpus: one valid frame per mode plus
// the malformed shapes most likely to trip a length/offset bug (empty, short,
// wrong magic, all-0xFF, exact-boundary lengths).

func seedFrames(m Mode) [][]byte {
	rng := rand.New(rand.NewSource(99))
	per := ambePerFrame(m)
	ambe := make([][]byte, per)
	for i := range ambe {
		ambe[i] = randCodeword(rng)
	}
	var valid []byte
	f := Frame{Mode: m, Kind: KindVoice, SrcID: 3180202, DstID: 9, SrcCallsign: "KN4OQW", AMBE: ambe}
	switch m {
	case ModeDMR:
		valid, _ = ConstructDMR(f, Params{DefaultTG: 9}, nil)
	case ModeYSF:
		valid, _ = ConstructYSF(f, Params{}, nil)
	case ModeNXDN:
		valid, _ = ConstructNXDN(f, Params{NXDNTG: 20, DefaultID: 65519}, nil)
	}
	return [][]byte{
		valid,
		{},                         // empty
		[]byte("DMRD"),             // magic only
		[]byte("NXDND"),            //
		make([]byte, 4),            // short, no magic
		make([]byte, len(valid)-1), // one short of a full frame
		bytesRepeat(0xFF, len(valid)),
		bytesRepeat(0x00, len(valid)),
	}
}

func bytesRepeat(b byte, n int) []byte {
	if n < 0 {
		n = 0
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return out
}

// reconstructNoPanic re-serializes a parsed voice frame; a construct must tolerate
// whatever a parse produced (it is the hub's next step).
func reconstructNoPanic(m Mode, f Frame) {
	if f.Kind == KindVoice && len(f.AMBE) != ambePerFrame(m) {
		return // a well-formed parse always yields the right count; guard just in case
	}
	switch m {
	case ModeDMR:
		_, _ = ConstructDMR(f, Params{DefaultTG: 9}, nil)
	case ModeYSF:
		_, _ = ConstructYSF(f, Params{}, nil)
	case ModeNXDN:
		_, _ = ConstructNXDN(f, Params{NXDNTG: 20, DefaultID: 65519}, nil)
	}
}

func FuzzParseDMR(f *testing.F) {
	for _, s := range seedFrames(ModeDMR) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if fr, err := ParseDMR(data); err == nil {
			reconstructNoPanic(ModeDMR, fr)
		}
	})
}

func FuzzParseYSF(f *testing.F) {
	for _, s := range seedFrames(ModeYSF) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if fr, err := ParseYSF(data); err == nil {
			reconstructNoPanic(ModeYSF, fr)
		}
	})
}

func FuzzParseNXDN(f *testing.F) {
	for _, s := range seedFrames(ModeNXDN) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if fr, err := ParseNXDN(data); err == nil {
			reconstructNoPanic(ModeNXDN, fr)
		}
	})
}

// TestParsersRejectGarbageNoPanic is the always-on (non-fuzz) companion: a large
// deterministic sweep of random/truncated buffers must never panic and must
// classify each as parsed-or-errored.
func TestParsersRejectGarbageNoPanic(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 20000; i++ {
		n := rng.Intn(160)
		b := make([]byte, n)
		rng.Read(b)
		// Occasionally stamp a valid magic so length/offset paths past the magic run.
		switch rng.Intn(4) {
		case 0:
			copy(b, "DMRD")
		case 1:
			copy(b, "YSFD")
		case 2:
			copy(b, "NXDND")
		}
		_, _ = ParseDMR(b)
		_, _ = ParseYSF(b)
		_, _ = ParseNXDN(b)
	}
}
