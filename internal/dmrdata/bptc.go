package dmrdata

// BPTC(196,96) — the block product turbo code that protects a DMR data burst's
// payload. Ported from g4klx/MMDVMHost BPTC19696.cpp.
//
// The 196 deinterleaved bits are a 13x15 matrix preceded by one unused bit R(3)
// at index 0. Rows 0-8 carry the 96 payload bits and four Hamming(15,11,3) parity
// bits each; all 15 columns then carry Hamming(13,9,3) parity in rows 9-12. The
// interleave is a fixed multiply-by-181 permutation modulo 196.
//
// The payload bits are not contiguous within the matrix: the first row
// contributes 8 bits and the next eight contribute 11 each. bptcDataPositions
// spells that out once so encode and decode cannot disagree about it.

const bptcBits = 196

// bptcDataPositions lists, in payload bit order, where each of the 96 payload
// bits sits in the deinterleaved matrix (BPTC19696.cpp decodeExtractData /
// encodeExtractData: 4-11, then 16-26, 31-41, ... 121-131).
var bptcDataPositions = buildBPTCDataPositions()

func buildBPTCDataPositions() [96]int {
	var pos [96]int
	n := 0
	add := func(lo, hi int) {
		for a := lo; a <= hi; a++ {
			pos[n] = a
			n++
		}
	}
	add(4, 11)
	for row := 1; row <= 8; row++ {
		start := row*15 + 1
		add(start, start+10)
	}
	if n != len(pos) {
		panic("dmrdata: BPTC data positions do not cover 96 bits")
	}
	return pos
}

// bptcInterleave[a] is where deinterleaved bit a lives in the raw burst
// (BPTC19696.cpp: interleaveSequence = (a * 181) % 196).
var bptcInterleave = func() [bptcBits]int {
	var t [bptcBits]int
	for a := range t {
		t[a] = (a * 181) % bptcBits
	}
	return t
}()

// bptcEncode writes the 196 BPTC bits of the 12-byte payload into burst.
//
// It writes ONLY those 196 bits. The slot type and sync bits burst already holds
// survive untouched, which is what makes re-encoding a captured burst lossless —
// and what makes encoding into a zeroed buffer produce an unusable burst. Callers
// building a burst from nothing want BuildBurst, which fills all 264.
func bptcEncode(payload []byte, burst []byte) {
	var deInter [bptcBits]bool
	for i, pos := range bptcDataPositions {
		deInter[pos] = payload[i/8]&bitMaskBE[i%8] != 0
	}

	// Rows first, then columns: the column parity has to cover the row parity.
	for r := 0; r < 9; r++ {
		hammingEncode15113(deInter[r*15+1 : r*15+16])
	}
	var col [13]bool
	for c := 0; c < 15; c++ {
		for a := range col {
			col[a] = deInter[c+1+a*15]
		}
		hammingEncode1393(col[:])
		for a := range col {
			deInter[c+1+a*15] = col[a]
		}
	}

	var raw [bptcBits]bool
	for a := range deInter {
		raw[bptcInterleave[a]] = deInter[a]
	}

	// Bits 0-95 and 100-195 are whole bytes; bits 96-99 straddle the slot type,
	// two in the top of burst[12] and two in the bottom of burst[20].
	for i := 0; i < 12; i++ {
		burst[i] = bitsToByteBE(raw[i*8:])
	}
	burst[12] = burst[12]&0x3F | boolBit(raw[96], 0x80) | boolBit(raw[97], 0x40)
	burst[20] = burst[20]&0xFC | boolBit(raw[98], 0x02) | boolBit(raw[99], 0x01)
	for i := 0; i < 12; i++ {
		burst[21+i] = bitsToByteBE(raw[100+i*8:])
	}
}

// bptcDecode recovers the 12-byte payload from a burst, correcting what the two
// Hamming codes can. It reports the number of bit positions the error check
// changed and whether anything is still inconsistent afterwards.
//
// A caller should treat unfixable as "this burst is corrupt" rather than trusting
// the payload: the CRC over the reassembled message is the real gate, but knowing
// the FEC gave up localises the damage to one burst.
func bptcDecode(burst []byte) (payload []byte, corrected int, unfixable bool) {
	var raw [bptcBits]bool
	for i := 0; i < 13; i++ {
		byteToBitsBE(burst[i], raw[i*8:])
	}
	raw[98] = burst[20]&0x02 != 0
	raw[99] = burst[20]&0x01 != 0
	for i := 0; i < 12; i++ {
		byteToBitsBE(burst[21+i], raw[100+i*8:])
	}

	var deInter [bptcBits]bool
	for a := range deInter {
		deInter[a] = raw[bptcInterleave[a]]
	}

	// Alternate columns and rows until nothing changes, or five passes have gone
	// by without converging — upstream's iteration bound, kept so a pathological
	// burst costs the same here as it does in MMDVM-Host.
	var col [13]bool
	fixing := true
	for count := 0; fixing && count < 5; count++ {
		fixing = false
		for c := 0; c < 15; c++ {
			for a := range col {
				col[a] = deInter[c+1+a*15]
			}
			if hammingDecode1393(col[:]) {
				for a := range col {
					deInter[c+1+a*15] = col[a]
				}
				fixing = true
				corrected++
			}
		}
		for r := 0; r < 9; r++ {
			if hammingDecode15113(deInter[r*15+1 : r*15+16]) {
				fixing = true
				corrected++
			}
		}
	}

	payload = make([]byte, PayloadBytes)
	for i, pos := range bptcDataPositions {
		if deInter[pos] {
			payload[i/8] |= bitMaskBE[i%8]
		}
	}
	return payload, corrected, fixing
}

// bitMaskBE addresses bits MSB-first, matching upstream byteToBitsBE.
var bitMaskBE = [8]byte{0x80, 0x40, 0x20, 0x10, 0x08, 0x04, 0x02, 0x01}

func byteToBitsBE(b byte, out []bool) {
	for i := 0; i < 8; i++ {
		out[i] = b&bitMaskBE[i] != 0
	}
}

func bitsToByteBE(bits []bool) byte {
	var b byte
	for i := 0; i < 8; i++ {
		if bits[i] {
			b |= bitMaskBE[i]
		}
	}
	return b
}

func boolBit(v bool, mask byte) byte {
	if v {
		return mask
	}
	return 0
}
