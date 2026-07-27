package frames

import (
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestYSFDGIdRoundTrip is issue #17's DG-ID prerequisite: a constructed YSFD
// frame must be able to carry a DG-ID, because that is the only field
// DGIdGateway routes on (DGIdGateway.cpp @ 2b480aa reads getDGId() from the
// repeater side to pick the DG-ID network). Before this, the FICH codec could
// read and write FI/DT/FN/FT but had no way to express a DG-ID at all, so no
// test could push voice through a DG-ID slot.
//
// The DG-ID sits inside the CRC'd region of the FICH, so this also proves the
// CRC still verifies once it is set — an unverifiable FICH is dropped outright
// by the daemon (fich.decode returning false skips the frame entirely).
func TestYSFDGIdRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(17))
	// 0 is the Wires-X/gateway slot, 1 the local Parrot, 5 the rendered startup
	// reflector, 127 the Wires-X sentinel and the top of the 7-bit field.
	for _, dgid := range []uint8{0, 1, 5, 99, 127} {
		voice := Frame{Mode: ModeYSF, Kind: KindVoice, SrcCallsign: "KN4OQW", DstCallsign: "ALL",
			Stream: Stream{Seq: 4}, AMBE: randCodewords(rng, ysfVCHPerFrame)}
		buf := construct(t, ModeYSF, voice, Params{DGId: dgid}, nil)

		got, err := YSFDGId(buf)
		if err != nil {
			t.Fatalf("DG-ID %d: YSFDGId: %v", dgid, err)
		}
		if got != dgid {
			t.Fatalf("DG-ID %d did not survive the FICH: got %d", dgid, got)
		}

		// Setting the DG-ID must not disturb anything else in the frame.
		back := parse(t, ModeYSF, buf)
		if back.Kind != KindVoice || back.SrcCallsign != "KN4OQW" || back.DstCallsign != "ALL" {
			t.Fatalf("DG-ID %d: addressing/kind lost: %+v", dgid, back)
		}
		if back.Stream.Seq != 4 {
			t.Fatalf("DG-ID %d: FN lost: %d", dgid, back.Stream.Seq)
		}
		if !reflect.DeepEqual(back.AMBE, voice.AMBE) {
			t.Fatalf("DG-ID %d: AMBE lost", dgid)
		}
	}

	// Headers and terminators carry the DG-ID too: DGIdGateway selects the
	// network on whichever frame opens the transmission, which is the header.
	for _, k := range []Kind{KindHeader, KindTerminator} {
		buf := construct(t, ModeYSF, Frame{Mode: ModeYSF, Kind: k, SrcCallsign: "KN4OQW"}, Params{DGId: 1}, nil)
		got, err := YSFDGId(buf)
		if err != nil || got != 1 {
			t.Fatalf("%v frame: DG-ID %d, err %v; want 1", k, got, err)
		}
	}

	// The field is 7 bits (CYSFFICH::setDGId masks with 0x7F), so an oversized
	// value truncates rather than corrupting the neighbouring bit.
	buf := construct(t, ModeYSF, Frame{Mode: ModeYSF, Kind: KindHeader, SrcCallsign: "KN4OQW"}, Params{DGId: 200}, nil)
	if got, err := YSFDGId(buf); err != nil || got != 200&0x7F {
		t.Fatalf("oversized DG-ID: got %d, err %v; want %d", got, err, 200&0x7F)
	}
}

// TestYSFDGIdDefaultsToGatewaySlot pins the behaviour of every existing caller
// that passes no DGId: frames stay on DG-ID 0, the gateway slot, which is what
// they carried before the field existed.
func TestYSFDGIdDefaultsToGatewaySlot(t *testing.T) {
	rng := rand.New(rand.NewSource(18))
	buf := construct(t, ModeYSF, Frame{Mode: ModeYSF, Kind: KindVoice, SrcCallsign: "KN4OQW",
		AMBE: randCodewords(rng, ysfVCHPerFrame)}, Params{}, nil)
	if got, err := YSFDGId(buf); err != nil || got != 0 {
		t.Fatalf("unset DGId: got %d, err %v; want 0", got, err)
	}
}

// TestYSFDGIdOnRealCapture reads the DG-ID off REAL YSFD bytes a daemon emitted
// on the bench (testdata/capture/README.md), not off this package's own encoder.
// It proves the accessor decodes the field where real traffic actually puts it.
func TestYSFDGIdOnRealCapture(t *testing.T) {
	for _, name := range []string{"ysf_bench_from_dmr.bin", "ysf_peer_from_dmr.bin"} {
		blob, err := os.ReadFile(filepath.Join("testdata", "capture", name))
		if err != nil {
			t.Fatal(err)
		}
		if len(blob) == 0 || len(blob)%ysfdLen != 0 {
			t.Fatalf("%s is not a whole number of %d-byte YSFD frames", name, ysfdLen)
		}
		for off := 0; off+ysfdLen <= len(blob); off += ysfdLen {
			got, err := YSFDGId(blob[off : off+ysfdLen])
			if err != nil {
				t.Fatalf("%s frame at %d: %v", name, off, err)
			}
			// These captures predate DG-ID support, so every frame rode the
			// gateway slot. A non-zero value here would mean the accessor is
			// reading the wrong bits.
			if got != 0 {
				t.Fatalf("%s frame at %d: DG-ID %d, want 0", name, off, got)
			}
		}
	}
}

// TestYSFDGIdErrors holds YSFDGId to the same never-panic contract as the
// parsers: malformed input is an error, not a crash.
func TestYSFDGIdErrors(t *testing.T) {
	cases := []struct {
		name string
		buf  []byte
		want error
	}{
		{"nil", nil, ErrShort},
		{"short", make([]byte, ysfdLen-1), ErrShort},
		{"bad magic", make([]byte, ysfdLen), ErrBadMagic},
	}
	for _, c := range cases {
		if _, err := YSFDGId(c.buf); err != c.want {
			t.Errorf("%s: err %v, want %v", c.name, err, c.want)
		}
	}
	// Right magic, garbage FICH: the CRC must reject it.
	bad := make([]byte, ysfdLen)
	copy(bad, "YSFD")
	for i := ysfFrameOff; i < ysfdLen; i++ {
		bad[i] = 0xA5
	}
	if _, err := YSFDGId(bad); err != ErrBadFrame {
		t.Errorf("garbage FICH: err %v, want %v", err, ErrBadFrame)
	}
}
