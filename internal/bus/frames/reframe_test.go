package frames

import (
	"bytes"
	"math/rand"
	"testing"
)

// randCodeword builds a canonical 49-bit AMBE codeword from random (a12,b12,c25).
func randCodeword(rng *rand.Rand) []byte {
	a := uint32(rng.Intn(1 << 12))
	b := uint32(rng.Intn(1 << 12))
	c := uint32(rng.Intn(1 << 25))
	return ambePutABC(a, b, c)
}

// TestAMBECanonicalRoundTrip: pack/unpack (a,b,c) is identity over the full range.
func TestAMBECanonicalRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 5000; i++ {
		a := uint32(rng.Intn(1 << 12))
		b := uint32(rng.Intn(1 << 12))
		c := uint32(rng.Intn(1 << 25))
		ga, gb, gc := ambeGetABC(ambePutABC(a, b, c))
		if ga != a || gb != b || gc != c {
			t.Fatalf("canonical pack/unpack lost bits: (%d,%d,%d) -> (%d,%d,%d)", a, b, c, ga, gb, gc)
		}
	}
}

// TestDMRAMBEReframeRoundTrip verifies the DMR Golay(24,12)/(23,12)+PRNG FEC is a
// lossless wrapper of the 49 codec bits: canonical -> 9-byte DMR AMBE -> canonical
// is the identity. This proves the ported tables are systematic (data in the high
// bits) and the PRNG cancels — the guarantee RFC-0003 §2 rests on.
func TestDMRAMBEReframeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 20000; i++ {
		cw := randCodeword(rng)
		got := dmrAMBEToCanonical(dmrAMBEFromCanonical(cw))
		if !bytes.Equal(got, cw) {
			a, b, c := ambeGetABC(cw)
			t.Fatalf("DMR AMBE reframe lost bits for (%d,%d,%d): %x -> %x", a, b, c, cw, got)
		}
	}
}

// TestYSFVCHReframeRoundTrip verifies the YSF VD2 VCH (rate-1/3 repeat + whitening
// + interleave) is a lossless wrapper of the 49 codec bits.
func TestYSFVCHReframeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	region := make([]byte, 90)
	for i := 0; i < 20000; i++ {
		cw := randCodeword(rng)
		off := uint(ysfSubblockBits*(i%5) + ysfVCHBitStart)
		ysfVCHFromCanonical(region, off, cw)
		got := ysfVCHToCanonical(region, off)
		if !bytes.Equal(got, cw) {
			t.Fatalf("YSF VCH reframe lost bits: %x -> %x", cw, got)
		}
	}
}

// TestNXDNVCHReframeRoundTrip verifies the flat NXDN VCH copy is lossless.
func TestNXDNVCHReframeRoundTrip(t *testing.T) {
	rng := rand.New(rand.NewSource(4))
	block := make([]byte, 14)
	for i := 0; i < 20000; i++ {
		cw := randCodeword(rng)
		off := uint(0)
		if i%2 == 1 {
			off = nxdnAMBE1Bit
		}
		nxdnVCHFromCanonical(block, off, cw)
		got := nxdnVCHToCanonical(block, off)
		if !bytes.Equal(got, cw) {
			t.Fatalf("NXDN VCH reframe lost bits: %x -> %x", cw, got)
		}
	}
}

// TestFICHRoundTrip: encode then decode a FICH recovers FI/DT/FN and the CRC checks.
func TestFICHRoundTrip(t *testing.T) {
	frame := make([]byte, ysfFrameLen)
	copy(frame, ysfSync[:])
	for fn := byte(0); fn < 6; fn++ {
		for _, fi := range []byte{ysfFIHeader, ysfFICommsChan, ysfFITerminator} {
			var f fich
			f.setFI(fi)
			f.setDT(ysfDTVDMode2)
			f.setFN(fn)
			f.setFT(5)
			f.encode(frame)
			got, ok := fichDecode(frame)
			if !ok {
				t.Fatalf("FICH CRC failed for fi=%d fn=%d", fi, fn)
			}
			if got.fi() != fi || got.dt() != ysfDTVDMode2 || got.fn() != fn {
				t.Fatalf("FICH lost fields: fi %d->%d dt %d fn %d->%d", fi, got.fi(), got.dt(), fn, got.fn())
			}
		}
	}
}
