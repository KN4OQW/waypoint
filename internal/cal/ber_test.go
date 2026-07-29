package cal

import "testing"

// Spot values from BERCal.cpp's PRNG_TABLE, the 4096-entry blob this package
// generates instead of carrying. If the generator ever drifts from the published
// sequence, DMR BER stops agreeing with every other MMDVM tool — silently, since
// a wrong whitening sequence produces plausible-looking errors rather than
// nonsense.
var ambePRNGPublished = []struct{ data, want uint32 }{
	{0, 0x42CC47}, {1, 0x19D6FE}, {2, 0x304729}, {3, 0x6B2CD0}, {7, 0xEACF60},
	{15, 0xA5B329}, {31, 0x99A474}, {63, 0x9A6148}, {127, 0xFBCFF9}, {255, 0xAF7B6F},
	{511, 0xF9DD2F}, {1023, 0x5773E7}, {2047, 0xF4C0F6}, {2048, 0xBD33B8},
	{3000, 0xB1D93D}, {4093, 0x5A9567}, {4094, 0x11A6D8}, {4095, 0x0B3F09},
}

func TestAmbePRNGMatchesPublished(t *testing.T) {
	for _, tc := range ambePRNGPublished {
		if got := ambePRNG(tc.data); got != tc.want {
			t.Errorf("ambePRNG(%d) = 0x%06X, published table has 0x%06X", tc.data, got, tc.want)
		}
	}
}

// buildDMRFrame lays three AMBE codewords into a DMR voice frame the way the air
// interface interleaves them, so a test can construct a frame that is correct by
// construction and then damage it in known places.
func buildDMRFrame(datas [3]uint32) []byte {
	frame := make([]byte, 33)
	for n, shift := range []uint{0, 72, 192} {
		data := datas[n] & 0xFFF
		a := encode24128(data)
		b := (encode23127(data^0xA5A) ^ (ambePRNG(data) >> 1)) & 0x7FFFFF
		scatterBits(frame, dmrATable[:], shift, a, 0x800000)
		scatterBits(frame, dmrBTable[:], shift, b, 0x400000)
	}
	return frame
}

func scatterBits(frame []byte, table []uint, shift uint, word, mask uint32) {
	for _, pos := range table {
		p := pos + shift
		if shift == 72 && p >= 108 {
			p += 48
		}
		if word&mask != 0 {
			frame[p>>3] |= 0x80 >> (p & 7)
		}
		mask >>= 1
	}
}

func flipBit(frame []byte, table []uint, shift uint, index int) {
	p := table[index] + shift
	if shift == 72 && p >= 108 {
		p += 48
	}
	frame[p>>3] ^= 0x80 >> (p & 7)
}

// TestDMRCleanFrameScoresZero is the measurement's floor. A perfectly received
// frame must score no errors, because every candidate frequency in a sweep is
// judged against this number.
func TestDMRCleanFrameScoresZero(t *testing.T) {
	frame := buildDMRFrame([3]uint32{0x123, 0xABC, 0x000})
	errs, ok := DMRVoiceFrame(frame)
	if !ok {
		t.Fatal("a 33-byte frame was refused")
	}
	if errs != 0 {
		t.Fatalf("clean frame scored %d errors, want 0", errs)
	}
}

// TestDMRSingleBitErrorIsCounted checks the correction actually runs. A decoder
// that returned the received word unchanged would also score zero on the test
// above, so the clean case alone proves nothing.
func TestDMRSingleBitErrorIsCounted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flip  func(f []byte)
		wants int
	}{
		{"one bit in the first codeword's protected field", func(f []byte) {
			flipBit(f, dmrATable[:], 0, 5)
		}, 1},
		{"one bit in the second codeword", func(f []byte) {
			flipBit(f, dmrATable[:], 72, 11)
		}, 1},
		{"one bit in the third codeword", func(f []byte) {
			flipBit(f, dmrBTable[:], 192, 2)
		}, 1},
		{"two bits in one codeword", func(f []byte) {
			flipBit(f, dmrATable[:], 0, 1)
			flipBit(f, dmrBTable[:], 0, 9)
		}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := buildDMRFrame([3]uint32{0x123, 0xABC, 0x000})
			tc.flip(frame)
			errs, _ := DMRVoiceFrame(frame)
			if errs != tc.wants {
				t.Fatalf("scored %d errors, want %d", errs, tc.wants)
			}
		})
	}
}

// TestDMRShortFrameIsRefused guards the difference between "no errors" and "no
// data". A truncated read scored as a clean frame would be a sweep confidently
// choosing a frequency it never heard anything on.
func TestDMRShortFrameIsRefused(t *testing.T) {
	if _, ok := DMRVoiceFrame(make([]byte, 32)); ok {
		t.Fatal("a 32-byte frame was accepted as a DMR voice frame")
	}
}

func TestDStarCleanFrameScoresZero(t *testing.T) {
	frame := make([]byte, 9)
	data := uint32(0x5A5)
	a := encode24128(data)
	b := encode24128(data^0x333) ^ ambePRNG(data)
	scatterBits(frame, dstarATable[:], 0, a, 0x800000)
	scatterBits(frame, dstarBTable[:], 0, b&0xFFFFFF, 0x800000)

	errs, ok := DStarVoiceFrame(frame)
	if !ok {
		t.Fatal("a 9-byte frame was refused")
	}
	if errs != 0 {
		t.Fatalf("clean D-Star frame scored %d errors, want 0", errs)
	}
}

// TestMeterDistinguishesSilenceFromPerfection is the rule the sweep depends on:
// zero percent with no frames behind it is not a good measurement, it is no
// measurement, and the meter must let a caller tell them apart.
func TestMeterDistinguishesSilenceFromPerfection(t *testing.T) {
	var silent Meter
	if silent.Percent() != 0 || silent.Frames != 0 {
		t.Fatalf("an untouched meter reports %.3f%% over %d frames", silent.Percent(), silent.Frames)
	}

	var clean Meter
	clean.Add(0, bitsPerDMRFrame)
	if clean.Percent() != 0 {
		t.Fatalf("a clean frame reports %.3f%%", clean.Percent())
	}
	if clean.Frames != 1 {
		t.Fatalf("a clean frame counted %d frames", clean.Frames)
	}

	var noisy Meter
	noisy.Add(3, bitsPerDMRFrame)
	noisy.Add(0, bitsPerDMRFrame)
	if got, want := noisy.Percent(), 3*100.0/282.0; got != want {
		t.Fatalf("BER = %.6f%%, want %.6f%%", got, want)
	}
}
