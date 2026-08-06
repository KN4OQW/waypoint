// Package dmrdata builds and parses DMR data PDUs — the bursts that carry a text
// message rather than voice.
//
// Everything here is a pure function over byte slices. There is no I/O, no
// network framing, and no notion of a session: the DMRD wire frame belongs to
// whatever transport carries these bursts, and reassembly state is an explicit
// value the caller owns.
//
// # Ground truth
//
// The FEC and PDU layers are ported from g4klx/MMDVMHost — BPTC19696.cpp,
// Hamming.cpp, DMRSlotType.cpp, Golay2087.cpp, Sync.cpp, DMRDataHeader.cpp,
// CRC.cpp — because that is the implementation on the other end of the wire.
//
// The SMS dialect above them is NOT from a specification. It is byte-cloned from
// the one construction observed to display on a stock BTECH 6X2 Pro: Motorola TMS
// over an IPv4/UDP tunnel, port 4007, UTF-16LE text behind a CRLF, addressed as
// 12.<24-bit DMR ID>. Specifications describe several plausible alternatives and
// the radio accepts none of them. See tunnel.go for the layout and why each field
// holds the value it does.
//
// # The 264-bit rule
//
// A DMR burst is 264 bits: 196 of BPTC-protected payload, 20 of Golay-protected
// slot type, and 48 of sync. CBPTC19696::encode writes only the 196. Encoding
// into a zeroed buffer therefore emits a burst with no sync and a slot type of
// zero, which MMDVM-Host reads back and re-transmits as data type 0 — and no
// radio can reassemble that. Two days of a bench investigation went into finding
// this. Every burst-producing function in this package writes all 264 bits, and
// TestBurstWritesEvery264Bits fails if one stops.
package dmrdata

// BurstBytes is the length of an on-air DMR burst payload, DMR_FRAME_LENGTH_BYTES.
const BurstBytes = 33

// PayloadBytes is the BPTC(196,96) payload one burst carries: 96 bits.
const PayloadBytes = 12

// DataType is the burst's slot-type data type (DMRDefines.h DT_*). It travels in
// the burst's own slot type AND in the low nibble of a DMRD frame's flags byte;
// both must agree, because MMDVM-Host reads the burst's copy back off the wire.
type DataType byte

// The data types this package produces or consumes. The remaining DT_* values
// (voice header, terminator, MBC, rate-3/4, rate-1, idle) are deliberately absent:
// nothing here builds them, and naming a constant is not the same as supporting it.
const (
	DataTypeCSBK       DataType = 0x03 // DT_CSBK — the preamble
	DataTypeDataHeader DataType = 0x06 // DT_DATA_HEADER
	DataTypeRate12     DataType = 0x07 // DT_RATE_12_DATA — the message body
)

func (d DataType) String() string {
	switch d {
	case DataTypeCSBK:
		return "csbk"
	case DataTypeDataHeader:
		return "data-header"
	case DataTypeRate12:
		return "rate-1/2"
	default:
		return "unknown"
	}
}

// Burst is one complete 264-bit burst plus the data type it declares. The two are
// kept together because a caller wrapping this for the network has to stamp the
// same type into the frame header, and separating them is how they drift.
type Burst struct {
	DataType DataType
	Payload  [BurstBytes]byte
}

// DefaultColorCode is what this package stamps into a burst it builds. MMDVM-Host
// rewrites the colour code (and the sync) to the node's own configuration on the
// way out, so the value only has to be legal, not correct — but the DATA TYPE it
// shares those 20 bits with is read back and used, so the slot type must be
// written properly regardless.
const DefaultColorCode = 1
