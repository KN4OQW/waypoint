package dmrdata

import (
	"encoding/hex"
	"testing"
)

// The strongest check available for a dialect this node has never transmitted:
// rebuild the radio's own DMR-Standard message from its fields and require the
// bytes back, checksums and all.
//
// It earned its keep immediately. The first version differed at one byte, which
// turned out to be the CRLF prefix: TMS writes it little-endian and ETSI writes it
// big-endian, with the text little-endian in both. Nothing predicted that and no
// round-trip test would have caught it, because parseBody skips those four octets.
func TestETSIBuilderReproducesTheRadiosOwnMessage(t *testing.T) {
	want, _ := hex.DecodeString("450000" + "2a00020000011194" +
		"0d0c3086aa0c3086aa1398139800163ec5" + "000d000a480045004c004c004f00000035" + "5e8fef")
	got, _, _, err := buildBody(3180202, 3180202, "HELLO", 2, false, DialectETSI)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("byte %d = %02x, want %02x\n got %x\nwant %x", i, got[i], want[i], got, want)
		}
	}
}
