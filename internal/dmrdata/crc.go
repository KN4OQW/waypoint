package dmrdata

// The two checksums a DMR text message rides under, and they are not related.
//
//   - CRC-CCITT-16 protects each 12-byte PDU header (data header, CSBK). It is
//     g4klx/MMDVMHost CCRC::addCCITT162, then XORed with a per-PDU mask so a
//     receiver that checks the wrong PDU type fails rather than half-believing it.
//   - ITU CRC-32 protects the reassembled message body. This one is NOT a
//     standard CRC-32: see ituCRC32.

// PDU checksum masks (DMRDefines.h). The mask is XORed over the two CRC bytes.
var (
	dataHeaderCRCMask = [2]byte{0xCC, 0xCC}
	csbkCRCMask       = [2]byte{0xA5, 0xA5}
)

// ccitt16Table is the CCITT-16 table CCRC::addCCITT162 indexes, generated from
// the 0x1021 polynomial. Worked example, from the first table entries: with an
// accumulator of zero the first byte b indexes entry b, so a leading 0x01 gives
// 0x1021 — the polynomial itself, which is what a shift-and-XOR by hand produces.
var ccitt16Table = func() [256]uint16 {
	var t [256]uint16
	for i := range t {
		c := uint16(i) << 8
		for range 8 {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x1021
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// addCCITT16 writes the CRC over pdu[:10] into pdu[10:12], XORed with mask.
//
// The byte order is upstream's, which relies on a little-endian union: crc8[0] is
// the low byte and crc8[1] the high byte, so the fed-back byte is the HIGH one and
// the high byte lands at index 10. Writing it out in terms of the value rather
// than the union makes it portable and identical.
func addCCITT16(pdu []byte, mask [2]byte) {
	var crc uint16
	for _, b := range pdu[:10] {
		crc = (crc&0xFF)<<8 ^ ccitt16Table[byte(crc>>8)^b]
	}
	crc = ^crc
	pdu[10] = byte(crc>>8) ^ mask[0]
	pdu[11] = byte(crc) ^ mask[1]
}

// checkCCITT16 reports whether a 12-byte PDU's checksum matches under mask.
func checkCCITT16(pdu []byte, mask [2]byte) bool {
	if len(pdu) != 12 {
		return false
	}
	var want [12]byte
	copy(want[:], pdu)
	addCCITT16(want[:], mask)
	return want[10] == pdu[10] && want[11] == pdu[11]
}

// crc32Table is the non-reflected CRC-32 table for polynomial 0x04C11DB7.
var crc32Table = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		c := uint32(i) << 24
		for range 8 {
			if c&0x80000000 != 0 {
				c = c<<1 ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// ituCRC32 is the checksum in the last four octets of a DMR data message body.
//
// It is a CRC-32 over the message with each PAIR of bytes fed in swapped —
// [1],[0],[3],[2],... — non-reflected, initialised to zero, with no final
// inversion. None of those three deviations is standard, and getting any one of
// them wrong yields a checksum the radio silently drops the message over.
//
// The byte swap is the tell that this is really a CRC over 16-bit words that
// somebody serialised little-endian. It is reproduced rather than rationalised,
// because the radio is the specification here. Verified byte-for-byte against
// seven real messages captured off the bench.
//
// A caller must pass an even number of bytes; the message body is always padded
// to a 12-byte block boundary before the checksum is taken, so this holds.
func ituCRC32(data []byte) uint32 {
	var crc uint32
	for i := 0; i+1 < len(data); i += 2 {
		crc = crc32Table[data[i+1]^byte(crc>>24)] ^ crc<<8
		crc = crc32Table[data[i]^byte(crc>>24)] ^ crc<<8
	}
	return crc
}
