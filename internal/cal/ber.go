package cal

import "math/bits"

// Bit error rate, measured the way every MMDVM tool measures it: by re-running
// the forward error correction that protects the voice bits and counting how
// many bits the correction had to change.
//
// Nothing transmits a known pattern here. The stimulus is an ordinary DMR
// transmission from the operator's own radio, and the trick — which is what
// makes calibration possible with no test equipment at all — is that the AMBE
// voice bits carry their own Golay protection. Decoding a protected field and
// re-encoding it reproduces what the transmitter must have sent; the difference
// against what actually arrived is the error count, whatever was said into the
// microphone.
//
// Ported from MMDVMCal's BERCal.cpp (GPLv2-or-later, Andy Uribe CA6JAU and Bryan
// Biedenkapp N2PLL) — the arithmetic, not the tables. Both the Golay codes
// (golay.go) and the AMBE whitening sequence below are derived here, so the only
// thing carried across is the procedure.

// The de-interleaving tables. A DMR voice frame's three AMBE codewords are
// spread across the payload, and these are the bit positions of each protected
// field. They are structural constants of the DMR air interface, not data.
var (
	dmrATable = [24]uint{0, 4, 8, 12, 16, 20, 24, 28, 32, 36, 40, 44,
		48, 52, 56, 60, 64, 68, 1, 5, 9, 13, 17, 21}
	dmrBTable = [23]uint{25, 29, 33, 37, 41, 45, 49, 53, 57, 61, 65, 69,
		2, 6, 10, 14, 18, 22, 26, 30, 34, 38, 42}

	dstarATable = [24]uint{0, 6, 12, 18, 24, 30, 36, 42, 48, 54, 60, 66,
		1, 7, 13, 19, 25, 31, 37, 43, 49, 55, 61, 67}
	dstarBTable = [24]uint{2, 8, 14, 20, 26, 32, 38, 44, 50, 56, 62, 68,
		3, 9, 15, 21, 27, 33, 39, 45, 51, 57, 63, 69}
)

// ambePRNG is the whitening sequence applied to the second protected field of
// each AMBE codeword, indexed by the twelve data bits of the first.
//
// Upstream ships this as 4096 literal constants. It is a plain 16-bit linear
// congruential generator seeded from the data word, and every one of those 4096
// values is reproduced exactly by this function — which the tests assert against
// the published constants. Deriving it means this package carries no copied data
// at all, only the procedure that produces it.
func ambePRNG(data uint32) uint32 {
	pr := (data & 0xFFF) * 16
	var out uint32
	for i := 0; i < 24; i++ {
		pr = (173*pr + 13849) & 0xFFFF
		out = out<<1 | pr>>15
	}
	return out
}

// bitsPerDMRFrame is what one DMR voice frame contributes: three AMBE codewords
// of 24 + 23 protected bits. The unprotected third field carries no FEC and so
// carries no evidence.
const bitsPerDMRFrame = 141

// bitsPerDStarFrame is D-Star's equivalent: one codeword of 24 + 24.
const bitsPerDStarFrame = 48

// Meter accumulates a bit error rate across the frames of one transmission.
//
// It is deliberately not a running average over all time. A sweep scores each
// candidate frequency on its own frames, and a meter that carried yesterday's
// errors into today's measurement would flatten exactly the curve the sweep is
// looking for.
type Meter struct {
	Frames int `json:"frames"`
	Bits   int `json:"bits"`
	Errors int `json:"errors"`
}

// Add folds one frame's result in.
func (m *Meter) Add(errors, bits int) {
	m.Frames++
	m.Errors += errors
	m.Bits += bits
}

// Reset clears the meter for a new measurement.
func (m *Meter) Reset() { *m = Meter{} }

// Percent is the bit error rate. A meter with no bits reports 0 and the caller
// is expected to check Frames — "no errors" and "no signal" are the same number
// and opposite meanings, and conflating them is how a sweep picks a frequency
// the radio was never heard on.
func (m Meter) Percent() float64 {
	if m.Bits == 0 {
		return 0
	}
	return float64(m.Errors) * 100 / float64(m.Bits)
}

// DMRVoiceFrame is the payload of one DMR voice frame: 33 bytes of DMR data.
// Errors reports how many bit errors its FEC had to correct.
//
// data is the frame body — the bytes after the sequence/control byte that the
// modem prepends. A frame shorter than a DMR frame is refused rather than
// scored, because a truncated read would otherwise contribute confident zeros.
func DMRVoiceFrame(data []byte) (errors int, ok bool) {
	const dmrFrameBytes = 33
	if len(data) < dmrFrameBytes {
		return 0, false
	}
	// The three AMBE codewords sit at the same bit positions, 72 and 192 bits
	// further on. The +48 step over the second is the slot's sync/embedded-LC
	// field, which the voice bits are interleaved around.
	for _, shift := range []uint{0, 72, 192} {
		a := gatherBits(data, dmrATable[:], shift, 0x800000)
		b := gatherBits(data, dmrBTable[:], shift, 0x400000)
		errors += regenerateDMR(a, b)
	}
	return errors, true
}

// DStarVoiceFrame scores one D-Star voice frame (9 bytes of AMBE + 3 of slow
// data; only the voice part is protected).
func DStarVoiceFrame(data []byte) (errors int, ok bool) {
	const dstarVoiceBytes = 9
	if len(data) < dstarVoiceBytes {
		return 0, false
	}
	a := gatherBits(data, dstarATable[:], 0, 0x800000)
	b := gatherBits(data, dstarBTable[:], 0, 0x800000)
	return regenerateDStar(a, b), true
}

// gatherBits pulls a codeword out of an interleaved frame. shift moves the whole
// field to the second or third AMBE codeword of a DMR frame; the jump over the
// sync field at bit 108 is applied here because it applies to the field, not to
// individual bits.
func gatherBits(data []byte, table []uint, shift uint, mask uint32) uint32 {
	var out uint32
	for _, pos := range table {
		p := pos + shift
		if shift == 72 && p >= 108 {
			p += 48
		}
		if readBit(data, p) {
			out |= mask
		}
		mask >>= 1
	}
	return out
}

func readBit(data []byte, pos uint) bool {
	i := pos >> 3
	if int(i) >= len(data) {
		return false
	}
	return data[i]&(0x80>>(pos&7)) != 0
}

// regenerateDMR re-derives what the transmitter must have sent for one AMBE
// codeword and counts the bits that differ.
//
// The second field is whitened by a sequence keyed on the first field's data,
// so it has to be un-whitened before its own Golay decode and re-whitened after
// — which also means an error in the first field's data bits corrupts the
// second's whitening key. That is a property of the air interface, not a bug
// here: it is why a badly-tuned receiver's BER climbs faster than the raw bit
// error rate alone would suggest.
func regenerateDMR(a, b uint32) int {
	origA, origB := a, b

	data := decode24128(a)
	a = encode24128(data)

	p := ambePRNG(data) >> 1 // 23 bits for DMR's shorter second field

	b ^= p
	// No shift here, unlike upstream's `encode23127(...) >> 1`. Its encoding
	// table stores each 23-bit codeword already shifted up by one bit — a
	// consequence of generating the 23- and 24-bit tables together — so the
	// shift undoes the table's own layout. golay.go returns the codeword itself,
	// and the tests pin both conventions against the published tables.
	b = encode23127(decode23127(b))
	b ^= p

	return bits.OnesCount32(a^origA) + bits.OnesCount32(b^origB)
}

// regenerateDStar is the same procedure with D-Star's two 24-bit fields.
func regenerateDStar(a, b uint32) int {
	origA, origB := a, b

	data := decode24128(a)
	a = encode24128(data)

	p := ambePRNG(data)

	b ^= p
	b = encode24128(decode24128(b))
	b ^= p

	return bits.OnesCount32(a^origA) + bits.OnesCount32(b^origB)
}
