package dmrdata

// The slot type: 20 bits carrying a 4-bit colour code and a 4-bit data type under
// a Golay(20,8,7) code, split either side of the sync in the middle of a burst.
// Ported from g4klx/MMDVMHost DMRSlotType.cpp and Golay2087.cpp.
//
// This is the field the 2026-08-06 bisect turned on. MMDVM-Host reads the data
// type back off an incoming burst (CDMRSlotType::putData in DMRSlot.cpp) and
// re-transmits accordingly, so a burst whose slot type says 0 goes out as data
// type 0 no matter what the network frame's header claimed.

// setSlotType writes the colour code and data type into burst, leaving the
// payload and sync bits alone.
func setSlotType(burst []byte, colorCode byte, dt DataType) {
	var st [3]byte
	st[0] = colorCode<<4&0xF0 | byte(dt)&0x0F
	cksum := golayEncode2087[st[0]]
	st[1] = byte(cksum & 0xFF)
	st[2] = byte(cksum >> 8)

	burst[12] = burst[12]&0xC0 | st[0]>>2&0x3F
	burst[13] = burst[13]&0x0F | st[0]<<6&0xC0 | st[1]>>2&0x30
	burst[19] = burst[19]&0xF0 | st[1]>>2&0x0F
	burst[20] = burst[20]&0x03 | st[1]<<6&0xC0 | st[2]>>2&0x3C
}

// getSlotType recovers the colour code and data type, correcting up to three bit
// errors through the Golay code.
func getSlotType(burst []byte) (colorCode byte, dt DataType) {
	var st [3]byte
	st[0] = burst[12]<<2&0xFC | burst[13]>>6&0x03
	st[1] = burst[13]<<2&0xC0 | burst[19]<<2&0x3C | burst[20]>>6&0x03
	st[2] = burst[20] << 2 & 0xF0

	code := uint32(st[0])<<11 | uint32(st[1])<<3 | uint32(st[2])>>5
	code ^= golayDecode1987[golaySyndrome1987(code)]
	code >>= 11
	return byte(code>>4) & 0x0F, DataType(code & 0x0F)
}

// Golay(20,8,7) syndrome arithmetic, Golay2087.cpp::getSyndrome1987. The constants
// are the vector representations of X^18 and X^11, the test mask, and the
// generator polynomial.
const (
	golayX18    = 0x00040000
	golayX11    = 0x00000800
	golayMask8  = 0xfffff800
	golayGenpol = 0x00000c75
)

func golaySyndrome1987(pattern uint32) uint32 {
	aux := uint32(golayX18)
	if pattern >= golayX11 {
		for pattern&golayMask8 != 0 {
			for aux&pattern == 0 {
				aux >>= 1
			}
			pattern ^= (aux / golayX11) * golayGenpol
		}
	}
	return pattern
}
