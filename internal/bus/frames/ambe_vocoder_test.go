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
