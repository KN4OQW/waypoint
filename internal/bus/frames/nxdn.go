package frames

import "encoding/binary"

// NXDN network (loopback) frame handling. The bus rides the NXDNGateway loopback,
// which speaks the "NXDND" protocol: a 43-byte UDP frame wrapping a 33-byte NXDN
// frame (LICH + SACCH + two 14-byte voice blocks). Crucially, on this loopback
// the NXDN VCH carries the AMBE+2 codewords FLAT and unprotected — a(12)@0,
// b(12)@12, c(25)@24 — which is byte-identical to this package's canonical 49-bit
// codeword. So NXDN needs no FEC transform at all: parse/construct is a bit copy.
// (MMDVM-Host applies the on-air interleave/scramble before the loopback; the bus
// never sees it.) 16-bit NXDN ids ride the "NXDND" header.
//
// Ground truth: "NXDND" wrapper is MMDVM_CM NXDN2DMR.cpp; the 33-byte frame's
// LICH(1)+SACCH(4)+2x14B-VCH layout with 2 AMBE per block at bit offsets 0 and 49
// is ModeConv.cpp putNXDN/getNXDN + MMDVMHost NXDNControl.cpp.

const (
	nxdndLen       = 43
	nxdnSrcOff     = 5  // 16-bit BE
	nxdnDstOff     = 7  // 16-bit BE
	nxdnFlagsOff   = 9  // bit3 (0x08) = end/TX_REL, bit0 (0x01) = group
	nxdnLICHOff    = 10 // start of the 33-byte NXDN frame
	nxdnBlockAOff  = 15 // LICH(1)+SACCH(4) then two 14-byte voice blocks
	nxdnBlockBOff  = 29
	nxdnAMBEPerBlk = 2
	nxdnAMBEBits   = 49 // one flat codeword = 12+12+25
	nxdnAMBE1Bit   = 49 // second codeword's bit offset within a 14-byte block
)

// LICH markers used to round-trip the frame kind. The precise NXDN LICH/SACCH
// bit-packing (RFCT/FCT/Option/Direction per NXDNDefines) is a follow-up for
// real-daemon wire validation (Prompt 6); on the bus the "NXDND" header carries
// authoritative addressing and the end bit carries the terminator, so these two
// values suffice to classify and reconstruct a frame losslessly.
const (
	nxdnLICHVoice      = 0x82 // FCT = USC/SACCH superframe (voice)
	nxdnLICHHeaderTerm = 0x80 // FCT = USC/SACCH non-superframe (header / TX_REL)
)

// ParseNXDN parses an "NXDND" frame. A voice frame yields 4 AMBE codewords; a
// header/terminator yields none. Never panics on malformed input.
func ParseNXDN(buf []byte) (Frame, error) {
	if len(buf) < nxdndLen {
		return Frame{}, ErrShort
	}
	if string(buf[0:5]) != "NXDND" {
		return Frame{}, ErrBadMagic
	}
	f := Frame{Mode: ModeNXDN}
	f.SrcID = uint32(binary.BigEndian.Uint16(buf[nxdnSrcOff:]))
	f.DstID = uint32(binary.BigEndian.Uint16(buf[nxdnDstOff:]))

	if buf[nxdnLICHOff] != nxdnLICHVoice {
		if buf[nxdnFlagsOff]&0x08 != 0 {
			f.Kind = KindTerminator
		} else {
			f.Kind = KindHeader
		}
		return f, nil
	}

	f.Kind = KindVoice
	blockA := buf[nxdnBlockAOff : nxdnBlockAOff+14]
	blockB := buf[nxdnBlockBOff : nxdnBlockBOff+14]
	f.AMBE = [][]byte{
		nxdnVCHToCanonical(blockA, 0),
		nxdnVCHToCanonical(blockA, nxdnAMBE1Bit),
		nxdnVCHToCanonical(blockB, 0),
		nxdnVCHToCanonical(blockB, nxdnAMBE1Bit),
	}
	return f, nil
}

// ConstructNXDN builds an "NXDND" frame. A voice frame must carry exactly 4 AMBE
// codewords. The 16-bit destination is the frame's dst, else the attachment TG;
// the source is the frame's src (truncated to 16 bits), else DefaultID. When the
// frame arrived id-addressed from DMR the caller has already mapped the id.
func ConstructNXDN(f Frame, p Params, r Resolver) ([]byte, error) {
	buf := make([]byte, nxdndLen)
	copy(buf, "NXDND")

	src := uint16(f.SrcID)
	if src == 0 {
		src = p.DefaultID
	}
	dst := uint16(f.DstID)
	if dst == 0 {
		dst = p.NXDNTG
	}
	binary.BigEndian.PutUint16(buf[nxdnSrcOff:], src)
	binary.BigEndian.PutUint16(buf[nxdnDstOff:], dst)
	buf[nxdnFlagsOff] = 0x01 // group

	switch f.Kind {
	case KindHeader:
		buf[nxdnLICHOff] = nxdnLICHHeaderTerm
	case KindTerminator:
		buf[nxdnLICHOff] = nxdnLICHHeaderTerm
		buf[nxdnFlagsOff] |= 0x08 // end / TX_REL
	default:
		buf[nxdnLICHOff] = nxdnLICHVoice
		if len(f.AMBE) != nxdnAMBEPerBlk*2 {
			return nil, ErrBadFrame
		}
		blockA := buf[nxdnBlockAOff : nxdnBlockAOff+14]
		blockB := buf[nxdnBlockBOff : nxdnBlockBOff+14]
		nxdnVCHFromCanonical(blockA, 0, f.AMBE[0])
		nxdnVCHFromCanonical(blockA, nxdnAMBE1Bit, f.AMBE[1])
		nxdnVCHFromCanonical(blockB, 0, f.AMBE[2])
		nxdnVCHFromCanonical(blockB, nxdnAMBE1Bit, f.AMBE[3])
	}
	return buf, nil
}

// nxdnVCHToCanonical copies the 49 flat AMBE bits at bitOff into a fresh canonical
// codeword — NXDN's loopback VCH already IS the canonical layout, so this is a
// straight bit copy with no FEC.
func nxdnVCHToCanonical(block []byte, bitOff uint) []byte {
	cw := make([]byte, AMBEBytes)
	for i := uint(0); i < nxdnAMBEBits; i++ {
		if readBit(block, bitOff+i) {
			writeBit(cw, i, true)
		}
	}
	return cw
}

// nxdnVCHFromCanonical writes the 49 canonical bits back into the block at bitOff.
func nxdnVCHFromCanonical(block []byte, bitOff uint, cw []byte) {
	for i := uint(0); i < nxdnAMBEBits; i++ {
		writeBit(block, bitOff+i, readBit(cw, i))
	}
}
