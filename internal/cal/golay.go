package cal

import "math/bits"

// The Golay codes that protect AMBE voice bits, derived rather than transcribed.
//
// MMDVMCal and MMDVM-Host both carry these as three literal tables — 4096
// encodings for each of the two code lengths and 2048 decoding entries, about a
// thousand lines of generated data. None of it is a fact about anyone's
// implementation: Golay(23,12) is a PERFECT code, which is the property this
// file leans on. Its 2^11 = 2048 syndromes and its error patterns of weight at
// most three (1 + 23 + 253 + 1771 = 2048) are in exact one-to-one
// correspondence, so a decoder can be built by enumeration and is then correct
// by construction rather than correct if the table was pasted without a typo.
//
// Deriving also removes the only real argument for pulling in a table nobody
// would ever read. The tests check the derivation against known codewords.

// golayPoly is the generator polynomial of the Golay(23,12) code, x^11 + x^9 +
// x^7 + x^6 + x^5 + x + 1, the same one used to generate the upstream tables.
const golayPoly = 0xC75

// decode23127 corrects a 23-bit codeword and returns the 12 data bits.
// decode24128 does the same for the 24-bit even-parity extension.
var golaySyndrome [2048]uint32 // syndrome → error pattern, filled at init

func init() {
	// Enumerate every correctable error pattern — weight 0, 1, 2 and 3 over 23
	// bits — and record the syndrome it produces. Because the code is perfect,
	// every one of the 2048 syndromes is hit exactly once, which the test
	// asserts: a gap would mean the polynomial or the arithmetic is wrong.
	set := func(pattern uint32) {
		golaySyndrome[syndrome23127(pattern)] = pattern
	}
	set(0)
	for i := 0; i < 23; i++ {
		set(1 << i)
		for j := i + 1; j < 23; j++ {
			set(1<<i | 1<<j)
			for k := j + 1; k < 23; k++ {
				set(1<<i | 1<<j | 1<<k)
			}
		}
	}
}

// encode23127 produces the 23-bit codeword for 12 data bits: the data in the
// high bits, the 11 parity bits below it.
func encode23127(data uint32) uint32 {
	return data<<11 | parity23127(data)
}

// encode24128 is the 24-bit form: the 23-bit codeword with an overall even
// parity bit appended.
func encode24128(data uint32) uint32 {
	code := encode23127(data)
	return code<<1 | uint32(bits.OnesCount32(code)&1)
}

// parity23127 is the remainder of the shifted data polynomial modulo the
// generator — the 11 parity bits.
func parity23127(data uint32) uint32 { return syndrome23127((data & 0xFFF) << 11) }

// syndrome23127 is the remainder of a received 23-bit word modulo the generator:
// zero for a codeword, and otherwise an index into the error table.
func syndrome23127(code uint32) uint32 {
	rem := code & 0x7FFFFF
	for i := 22; i >= 11; i-- {
		if rem&(1<<uint(i)) != 0 {
			rem ^= golayPoly << uint(i-11)
		}
	}
	return rem & 0x7FF
}

// decode23127 corrects up to three bit errors in a 23-bit word and returns the
// 12 data bits it carried.
func decode23127(code uint32) uint32 {
	code &= 0x7FFFFF
	corrected := code ^ golaySyndrome[syndrome23127(code)]
	return corrected >> 11
}

// decode24128 corrects a 24-bit word. The overall parity bit is dropped rather
// than used: it distinguishes three errors from four, which changes nothing
// about the data bits recovered, and the caller is counting errors against the
// re-encoded word anyway.
func decode24128(code uint32) uint32 {
	return decode23127(code >> 1)
}
