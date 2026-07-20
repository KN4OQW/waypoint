package frames

// MSB-first bit addressing and the AMBE+2 FEC transforms, ported from
// juribeparada/MMDVM_CM (DMR2YSF/ModeConv.cpp, Golay24128.cpp). The bit macros
// match the upstream WRITE_BIT/READ_BIT exactly (BIT_MASK_TABLE {0x80..0x01},
// index i -> byte i>>3, mask [i&7]). Every transform here is bit copying, table
// permutation, XOR, and Golay block coding — there is NO vocoder or audio codec.

var bitMask = [8]byte{0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01}

// readBit / writeBit are MSB-first, matching upstream READ_BIT/WRITE_BIT.
func readBit(p []byte, i uint) bool { return p[i>>3]&bitMask[i&7] != 0 }

func writeBit(p []byte, i uint, b bool) {
	if b {
		p[i>>3] |= bitMask[i&7]
	} else {
		p[i>>3] &^= bitMask[i&7]
	}
}

// --- Golay(24,12)/(23,12) — DMR2YSF/Golay24128.cpp -----------------------------

// Golay syndrome constants (Golay24128.cpp:1042-1045).
const (
	golayX22    = 0x00400000
	golayX11    = 0x00000800
	golayMask12 = 0xfffff800
	golayGenpol = 0x00000c75
)

// getSyndrome23127 is CGolay24128 ::get_syndrome_23127 (Golay24128.cpp:1047).
func getSyndrome23127(pattern uint32) uint32 {
	aux := uint32(golayX22)
	if pattern >= golayX11 {
		for pattern&golayMask12 != 0 {
			for aux&pattern == 0 {
				aux >>= 1
			}
			pattern ^= (aux / golayX11) * golayGenpol
		}
	}
	return pattern
}

// golayEncode24128 / golayEncode23127 are the table lookups
// CGolay24128::encode24128 / encode23127 (data is 12 bits).
func golayEncode24128(data uint32) uint32 { return golayEnc24128[data&0xFFF] }
func golayEncode23127(data uint32) uint32 { return golayEnc23127[data&0xFFF] }

// golayDecode23127 / golayDecode24128 are CGolay24128::decode23127 / decode24128
// (Golay24128.cpp:1082-1095) — error-correct then return the 12 data bits.
func golayDecode23127(code uint32) uint32 {
	syndrome := getSyndrome23127(code) & 0x7FF
	code ^= golayDec23127[syndrome]
	return code >> 11
}

func golayDecode24128(code uint32) uint32 { return golayDecode23127(code >> 1) }

// --- Normalized 49-bit AMBE codeword (a12,b12,c25) -----------------------------
//
// The canonical 7-byte codeword IS NXDN's flat loopback layout: a at bit 0
// (12 bits, MSB-first), b at bit 12, c at bit 24. ambeGetABC / ambePutABC are the
// bridge between that packed form and the three ints the DMR/YSF FEC works on.

func ambeGetABC(cw []byte) (a, b, c uint32) {
	for i := uint(0); i < ambeABits; i++ {
		if readBit(cw, i) {
			a |= 1 << (ambeABits - 1 - i)
		}
	}
	for i := uint(0); i < ambeBBits; i++ {
		if readBit(cw, ambeABits+i) {
			b |= 1 << (ambeBBits - 1 - i)
		}
	}
	for i := uint(0); i < ambeCBits; i++ {
		if readBit(cw, ambeABits+ambeBBits+i) {
			c |= 1 << (ambeCBits - 1 - i)
		}
	}
	return
}

func ambePutABC(a, b, c uint32) []byte {
	cw := make([]byte, AMBEBytes)
	for i := uint(0); i < ambeABits; i++ {
		writeBit(cw, i, a&(1<<(ambeABits-1-i)) != 0)
	}
	for i := uint(0); i < ambeBBits; i++ {
		writeBit(cw, ambeABits+i, b&(1<<(ambeBBits-1-i)) != 0)
	}
	for i := uint(0); i < ambeCBits; i++ {
		writeBit(cw, ambeABits+ambeBBits+i, c&(1<<(ambeCBits-1-i)) != 0)
	}
	return cw
}

// --- DMR 9-byte on-air AMBE codeword <-> canonical 49-bit ----------------------
//
// A DMR AMBE frame is 72 bits: a = Golay(24,12) at DMR_A_TABLE positions, b =
// Golay(23,12) at DMR_B_TABLE (PRNG-scrambled by a's data), c = 25 raw bits at
// DMR_C_TABLE. Ported from ModeConv.cpp putDMR (decode) and putAMBE2DMR (encode).

const dmrAMBEBytes = 9

// dmrAMBEToCanonical lifts the 49 codec bits out of a 9-byte DMR AMBE frame
// (ModeConv.cpp:485-533 putDMR read + :655-676 de-FEC: a>>=12; b^=PRNG[a]>>1;
// b>>=11). No Golay DECODE — upstream truncates the data bits directly.
func dmrAMBEToCanonical(sub []byte) []byte {
	var a, b, c uint32
	for i := 0; i < 24; i++ {
		if readBit(sub, dmrATable[i]) {
			a |= 1 << (23 - uint(i))
		}
	}
	for i := 0; i < 23; i++ {
		if readBit(sub, dmrBTable[i]) {
			b |= 1 << (22 - uint(i))
		}
	}
	for i := 0; i < 25; i++ {
		if readBit(sub, dmrCTable[i]) {
			c |= 1 << (24 - uint(i))
		}
	}
	a >>= 12
	b ^= prngTable[a] >> 1
	b >>= 11
	return ambePutABC(a, b, c)
}

// dmrAMBEFromCanonical builds a 9-byte DMR AMBE frame from the 49 canonical bits
// (ModeConv.cpp putAMBE2DMR: a=encode24128(A); b=encode23127(B)>>1; b^=PRNG[A]>>1;
// place via DMR_A/B/C_TABLE).
func dmrAMBEFromCanonical(cw []byte) []byte {
	da, db, dc := ambeGetABC(cw)
	a := golayEncode24128(da)
	b := (golayEncode23127(db) >> 1) ^ (prngTable[da] >> 1)
	sub := make([]byte, dmrAMBEBytes)
	for i := 0; i < 24; i++ {
		writeBit(sub, dmrATable[i], a&(1<<(23-uint(i))) != 0)
	}
	for i := 0; i < 23; i++ {
		writeBit(sub, dmrBTable[i], b&(1<<(22-uint(i))) != 0)
	}
	for i := 0; i < 25; i++ {
		writeBit(sub, dmrCTable[i], dc&(1<<(24-uint(i))) != 0)
	}
	return sub
}
