package dmrdata

import "errors"

// The two 12-byte PDUs that precede a message body: the preamble CSBK that wakes
// the receiver, and the data header that describes what follows.
//
// Ported from g4klx/MMDVMHost DMRDataHeader.cpp with the field layout confirmed
// against captures from the bench. Only the pieces this package builds and reads
// are modelled; the UDT, response and short-data formats are parsed far enough to
// be identified and then declined, because supporting them is a separate job with
// its own hardware validation.

// ErrBadCRC is returned when a PDU's checksum does not match under its mask.
var ErrBadCRC = errors.New("dmrdata: PDU checksum mismatch")

// ErrUnsupportedPDU is returned for a well-formed PDU this package does not build
// or consume — a confirmed-data header, a UDT header, a non-preamble CSBK.
var ErrUnsupportedPDU = errors.New("dmrdata: unsupported PDU format")

// Data packet formats, DMRDefines.h DPF_*.
//
// Only the two this package acts on are named. The others — UDT, response,
// defined-short, defined-raw, proprietary — are real formats with real meanings,
// and each would need its own parser and its own hardware validation; a constant
// for one would suggest it is handled when it is not. parseDataHeader declines
// everything but DPFUnconfirmedData by exclusion, so nothing is silently misread.
type DPF byte

const (
	DPFUnconfirmedData DPF = 0x02 // the only format this package builds
	DPFConfirmedData   DPF = 0x03 // per-block CRC-9 and an ARQ exchange; declined
)

// SAPIPData is service access point 4, "IP based packet data" — the SAP the
// message tunnel rides under, and the one the radio listens on.
const SAPIPData = 4

// DataHeader is an unconfirmed-data header: who the message is for, how many
// rate-1/2 blocks follow, and how many octets of the last one are padding.
type DataHeader struct {
	Group  bool // G/I: a group call rather than an individual one
	Src    uint32
	Dst    uint32
	SAP    byte
	Blocks int // rate-1/2 blocks that follow this header
	Pad    int // padding octets in the final block
}

// buildDataHeader lays out the 12 bytes and checksums them.
//
// Pad is split across two fields: bit 4 of octet 0 and the low nibble of octet 1.
// That is not a quirk of this implementation — it is where the format puts a
// 5-bit value, and reassembly reads it back the same way.
func buildDataHeader(h DataHeader) []byte {
	b := make([]byte, 12)
	b[0] = boolBit(h.Group, 0x80) | byte(h.Pad)&0x10 | byte(DPFUnconfirmedData)
	b[1] = h.SAP<<4&0xF0 | byte(h.Pad)&0x0F
	putU24(b[2:], h.Dst)
	putU24(b[5:], h.Src)
	// F=1: this is a full message, not a fragment of a larger transfer.
	b[8] = 0x80 | byte(h.Blocks)&0x7F
	b[9] = 0x00
	addCCITT16(b, dataHeaderCRCMask)
	return b
}

// parseDataHeader reads a 12-byte header PDU. It returns ErrUnsupportedPDU for a
// format this package cannot reassemble, so a caller can log what it saw rather
// than guessing at the bytes.
func parseDataHeader(pdu []byte) (DataHeader, error) {
	if len(pdu) != 12 {
		return DataHeader{}, ErrShortPayload
	}
	if !checkCCITT16(pdu, dataHeaderCRCMask) {
		return DataHeader{}, ErrBadCRC
	}
	if dpf := DPF(pdu[0] & 0x0F); dpf != DPFUnconfirmedData {
		return DataHeader{}, ErrUnsupportedPDU
	}
	return DataHeader{
		Group:  pdu[0]&0x80 != 0,
		Pad:    int(pdu[0]&0x10) | int(pdu[1]&0x0F),
		SAP:    pdu[1] >> 4 & 0x0F,
		Dst:    u24(pdu[2:]),
		Src:    u24(pdu[5:]),
		Blocks: int(pdu[8] & 0x7F),
	}, nil
}

// csbkoPreamble is CSBK opcode 0x3D, the preamble that tells a receiver traffic
// is coming and how much of it. A radio that misses the preambles may never open
// its receiver for the header that follows, which is why a message is sent with
// several and not one.
const csbkoPreamble = 0x3D

// buildPreambleCSBK builds one preamble. blocksToFollow counts down across the
// preamble run so the last one says "one more PDU after me".
func buildPreambleCSBK(src, dst uint32, blocksToFollow int, group bool) []byte {
	b := make([]byte, 12)
	b[0] = 0x80 | csbkoPreamble // LB=1 (last block), PF=0, opcode
	b[1] = 0x00                 // FID_ETSI
	// Data-follows bit, plus the group/individual bit for what follows.
	b[2] = 0x80 | boolBit(group, 0x40)
	b[3] = byte(blocksToFollow)
	putU24(b[4:], dst)
	putU24(b[7:], src)
	addCCITT16(b, csbkCRCMask)
	return b
}

// isPreambleCSBK reports whether a 12-byte CSBK PDU is a data preamble with a
// valid checksum.
func isPreambleCSBK(pdu []byte) bool {
	return len(pdu) == 12 && checkCCITT16(pdu, csbkCRCMask) && pdu[0]&0x3F == csbkoPreamble
}

func u24(b []byte) uint32 { return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2]) }

func putU24(b []byte, v uint32) {
	b[0] = byte(v >> 16)
	b[1] = byte(v >> 8)
	b[2] = byte(v)
}
