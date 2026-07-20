package frames

import (
	"bytes"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Golden byte-exact frame fixtures. These are SYNTHETIC — constructed from the
// upstream frame definitions, not captured off a live loopback (see
// testdata/synthetic/README.md). They pin the exact wire bytes this layer emits
// so a future refactor cannot silently change the format, and Prompt 6 replaces
// them with real bench captures. On first run (file absent) the fixture is
// written; thereafter it is a golden comparison.

func fixtureVoice(m Mode) (Frame, Params) {
	rng := rand.New(rand.NewSource(int64(m) + 1000))
	per := ambePerFrame(m)
	ambe := make([][]byte, per)
	for i := range ambe {
		ambe[i] = randCodeword(rng)
	}
	f := Frame{Mode: m, Kind: KindVoice, SrcID: 3180202, DstID: 9,
		SrcCallsign: "KN4OQW", DstCallsign: "ALL", AMBE: ambe}
	return f, Params{Slot: 2, DefaultTG: 9, NXDNTG: 20, DefaultID: 65519}
}

func TestGoldenFixtures(t *testing.T) {
	dir := filepath.Join("testdata", "synthetic")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Mode{ModeDMR, ModeYSF, ModeNXDN} {
		f, p := fixtureVoice(m)
		got := construct(t, m, f, p, nil)

		path := filepath.Join(dir, m.String()+"_voice.bin")
		want, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			if werr := os.WriteFile(path, got, 0o644); werr != nil {
				t.Fatal(werr)
			}
			t.Logf("generated synthetic fixture %s (%d bytes)", path, len(got))
		} else if err != nil {
			t.Fatal(err)
		} else if !bytes.Equal(got, want) {
			t.Fatalf("fixture %s drifted: the wire format changed.\n want %x\n  got %x", path, want, got)
		}

		// The committed fixture must parse back to the same frame.
		parsed := parse(t, m, got)
		if parsed.Kind != KindVoice || !reflect.DeepEqual(parsed.AMBE, f.AMBE) {
			t.Fatalf("%s fixture did not round-trip its AMBE", m)
		}
	}
}
