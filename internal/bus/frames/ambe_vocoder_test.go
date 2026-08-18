package frames

import (
	"encoding/hex"
	"testing"
)

// Cross-check this package's canonical->on-air conversion against an independent
// implementation of the same transform.
//
// Both byte strings below are the SAME 20 ms of audio, encoded on a Pi by the
// md380 vocoder: once with its plain entry point (the raw 49-bit canonical form,
// 7 bytes) and once with its FEC entry point (the 72-bit on-air form, 9 bytes).
// That library's FEC routine reads a from bits 0-11, b from 12-23 and c from
// 24-48 of the plain output, which is exactly this package's canonical layout,
// then applies Golay and the PRNG. So dmrAMBEFromCanonical over the first must
// reproduce the second, byte for byte.
//
// If it does not, a vocoder feeding this package produces noise on the air while
// every layer in between reports a clean transmission -- which is precisely the
// symptom this was written to explain.
func TestCanonicalToOnAirMatchesAnIndependentVocoder(t *testing.T) {
	canonical, err := hex.DecodeString("f8fe14398c5080")
	if err != nil {
		t.Fatal(err)
	}
	wantOnAir, err := hex.DecodeString("b9ee8354eebc1aae89")
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) != AMBEBytes || len(wantOnAir) != dmrAMBEBytes {
		t.Fatalf("fixture sizes wrong: %d, %d", len(canonical), len(wantOnAir))
	}

	got := dmrAMBEFromCanonical(canonical)
	if hex.EncodeToString(got) != hex.EncodeToString(wantOnAir) {
		t.Errorf("canonical -> on-air mismatch\n got %s\nwant %s",
			hex.EncodeToString(got), hex.EncodeToString(wantOnAir))
	}

	// And the reverse must recover what we started with.
	if back := dmrAMBEToCanonical(wantOnAir); hex.EncodeToString(back) != hex.EncodeToString(canonical) {
		t.Errorf("on-air -> canonical mismatch\n got %s\nwant %s",
			hex.EncodeToString(back), hex.EncodeToString(canonical))
	}
}

// A DMR voice superframe is not six identical frames. Frame A carries the sync;
// B through F carry their position in the low nibble so the host knows which
// embedded link-control fragment to insert. Getting this wrong transmits
// cleanly -- no loss, no bit errors -- and decodes as noise, because the radio
// never assembles the link control.
func TestVoiceSuperframeLabelling(t *testing.T) {
	ambe := [][]byte{make([]byte, AMBEBytes), make([]byte, AMBEBytes), make([]byte, AMBEBytes)}
	for n := uint8(0); n < DMRVoiceSuperframe; n++ {
		f := Frame{Mode: ModeDMR, Kind: KindVoice, DstID: 9, AMBE: ambe, VoiceSeq: n}
		b, err := ConstructDMR(f, Params{Slot: 2, DefaultTG: 9}, nil)
		if err != nil {
			t.Fatalf("position %d: %v", n, err)
		}
		flags := b[15]
		if flags&0x80 == 0 {
			t.Errorf("position %d: slot 2 bit not set (%#02x)", n, flags)
		}
		if flags&0x20 != 0 {
			t.Errorf("position %d: data-sync bit set on a voice frame (%#02x)", n, flags)
		}
		if n == 0 {
			if flags&0x10 == 0 {
				t.Errorf("frame A is not marked as a voice sync frame (%#02x)", flags)
			}
		} else {
			if flags&0x10 != 0 {
				t.Errorf("position %d is marked as a sync frame; only A may be (%#02x)", n, flags)
			}
			if got := flags & 0x0F; got != n {
				t.Errorf("position %d carries N=%d in the low nibble", n, got)
			}
		}
	}
}

// The position wraps, so a transmission longer than six frames keeps its
// structure instead of running the nibble past what the field holds.
func TestVoiceSuperframeWraps(t *testing.T) {
	ambe := [][]byte{make([]byte, AMBEBytes), make([]byte, AMBEBytes), make([]byte, AMBEBytes)}
	f := Frame{Mode: ModeDMR, Kind: KindVoice, DstID: 9, AMBE: ambe, VoiceSeq: DMRVoiceSuperframe}
	b, err := ConstructDMR(f, Params{Slot: 2, DefaultTG: 9}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b[15]&0x10 == 0 {
		t.Errorf("position 6 did not wrap to frame A (%#02x)", b[15])
	}
}
