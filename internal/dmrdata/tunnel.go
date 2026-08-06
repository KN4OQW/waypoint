package dmrdata

import (
	"encoding/binary"
	"errors"
	"unicode/utf16"
	"unicode/utf8"
)

// The message body: a whole IPv4 datagram, tunnelled inside DMR data blocks.
//
// A DMR radio that speaks SAP 4 does not carry text — it carries IP packets, and
// the text is a Motorola TMS payload inside a UDP datagram inside one. The DMR IDs
// become IP addresses in 12.0.0.0/8: 12.<24-bit id>.
//
// # Why these values and not the ones in the specification
//
// This layout is byte-cloned from the construction observed to display on a stock
// BTECH 6X2 Pro with the modem in loopback and BrandMeister stopped. It is NOT
// derived from ETSI TS 102 361-4, which describes a different short-data service
// on a different port that this radio ignores. Where a field's value has no
// evident meaning it is reproduced anyway, because the radio is the only authority
// that matters and it was not consulted about the rationale.
//
// Fields whose purpose IS understood are named below. Everything else is zero
// because the working construction had it zero.
//
// A prerequisite no code here can satisfy: the radio's channel must have SMS
// format M-SMS and APRS Receive OFF. Anytone/BTECH firmware treats APRS receive
// and SMS as mutually exclusive, and with APRS receive on the radio decodes the
// burst, lights up, and discards the message.

// ErrTextTooLong is returned when a message will not fit the tunnel's length
// fields. See MaxTextUnits.
var ErrTextTooLong = errors.New("dmrdata: message text too long")

// ErrMalformedBody is returned when a reassembled body is not a message this
// package built — wrong length, wrong protocol, failed checksum.
var ErrMalformedBody = errors.New("dmrdata: malformed message body")

// ErrInvalidText is returned for text that is not valid UTF-8.
//
// Go's string-to-rune conversion turns every invalid byte into U+FFFD, so without
// this check a caller would get a message full of replacement characters and no
// indication anything went wrong. Found by FuzzBuildMessage, which asserts that
// what goes in comes back out.
var ErrInvalidText = errors.New("dmrdata: message text is not valid UTF-8")

// The two SMS ports, and which way each one is used here.
//
// portTMS (4007) is Motorola TMS. It is the only port this package TRANSMITS on,
// because it is the only one a BTECH 6X2 Pro in M-SMS format was observed to
// display. Messages sent to 5016 were received by the radio and dropped.
//
// portETSI (5016) is the ETSI "DMR Standard" service. It appears here only on the
// receive path: a radio set to DMR-Standard format EMITS on 5016, with no TMS
// header — the CRLF and text start immediately after the UDP header. Both bench
// captures of radio-originated traffic are in that form, so a receiver that only
// understood 4007 would silently ignore every message from a radio configured
// that way. Nothing builds this format; see parseBody.
const (
	portTMS  = 4007
	portETSI = 5016
)

// Tunnel layout. Offsets are from the start of the reassembled body.
const (
	ipHeaderLen  = 20
	udpHeaderLen = 8
	tmsHeaderLen = 6
	// bodyOverhead is the IP + UDP + TMS headers, the CRLF that prefixes every
	// message body as UTF-16LE, and the trailing CRC-32.
	crlfLen       = 4
	crc32Len      = 4
	bodyOverhead  = ipHeaderLen + udpHeaderLen + tmsHeaderLen + crlfLen + crc32Len
	textOffset    = ipHeaderLen + udpHeaderLen + tmsHeaderLen + crlfLen // 38
	blockOctets   = PayloadBytes
	protocolUDP   = 17
	ipVersionIHL  = 0x45
	defaultTTL    = 0x40 // what the radio itself emits
	tmsClassByte  = 0xA0 // TMS octet 2 on every message the radio accepts
	tmsOptionByte = 0x04 // TMS octet 5, likewise
)

// MaxTextUnits is the longest message this package will build, in UTF-16 code
// units (so a character outside the BMP costs two).
//
// The bound comes from the TMS length field, which the proven construction writes
// as a single octet: it holds len(UDP payload) - 10 = 2*units + 8, so 123 units is
// the last value that cannot overflow it. The 7-bit block count in the data header
// would allow far more, and radios impose their own lower limits — but a field
// that silently wraps is the kind of bug this whole exercise was about, so the
// tighter bound is the one enforced.
const MaxTextUnits = 123

// buildBody lays out the IPv4 datagram carrying text, padded to a whole number of
// 12-octet blocks with the ITU CRC-32 in the final four octets.
//
// seq becomes both the IP identification field and the TMS message number; the
// radio echoes neither, so it exists to make retransmissions distinguishable in a
// capture rather than to drive any protocol behaviour.
func buildBody(src, dst uint32, text string, seq uint16, group bool) (body []byte, blocks, pad int, err error) {
	if !utf8.ValidString(text) {
		return nil, 0, 0, ErrInvalidText
	}
	units := utf16.Encode([]rune(text))
	if len(units) > MaxTextUnits {
		return nil, 0, 0, ErrTextTooLong
	}

	udpLen := len(units)*2 + udpHeaderLen + tmsHeaderLen + crlfLen
	total := len(units)*2 + bodyOverhead
	pad = (blockOctets - total%blockOctets) % blockOctets
	total += pad
	blocks = total / blockOctets
	b := make([]byte, total)

	// --- IPv4 header. Addresses are 12.<24-bit DMR ID>; a group destination uses
	// 225.<tg> instead, the multicast form the radio expects for a talkgroup.
	b[0] = ipVersionIHL
	binary.BigEndian.PutUint16(b[2:], uint16(udpLen+ipHeaderLen))
	binary.BigEndian.PutUint16(b[4:], seq)
	b[8] = defaultTTL
	b[9] = protocolUDP
	b[12] = 0x0C
	putU24(b[13:], src)
	if group {
		b[16] = 0xE1
	} else {
		b[16] = 0x0C
	}
	putU24(b[17:], dst)
	binary.BigEndian.PutUint16(b[10:], onesComplementSum(b[:ipHeaderLen]))

	// --- UDP header, checksum filled in below once the payload exists.
	binary.BigEndian.PutUint16(b[20:], portTMS)
	binary.BigEndian.PutUint16(b[22:], portTMS)
	binary.BigEndian.PutUint16(b[24:], uint16(udpLen))

	// --- TMS header. Octet 0-1 is the length of what follows it; the proven
	// construction writes only the low octet, and MaxTextUnits keeps it in range.
	b[29] = byte(udpLen - 10)
	b[30] = tmsClassByte
	b[32] = 0x80 | byte(seq&0x7F)
	b[33] = tmsOptionByte

	// --- Body: CRLF then the text, all UTF-16LE. The CRLF is not decoration —
	// BrandMeister emits it too, and it is part of what the radio parses.
	binary.LittleEndian.PutUint16(b[34:], '\r')
	binary.LittleEndian.PutUint16(b[36:], '\n')
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[textOffset+i*2:], u)
	}

	binary.BigEndian.PutUint16(b[26:], udpChecksum(b, udpLen))
	crc := ituCRC32(b[:total-crc32Len])
	binary.LittleEndian.PutUint32(b[total-crc32Len:], crc)
	return b, blocks, pad, nil
}

// Message is a decoded inbound text message.
type Message struct {
	Src   uint32
	Dst   uint32
	Group bool
	Seq   uint16
	Text  string
}

// parseBody decodes a reassembled body, verifying the CRC-32 before it believes
// any of it. The CRC covers the padding octets too, so the body must be presented
// at its full block-aligned length exactly as it came off the air.
//
// Two framings are accepted, distinguished by destination port. On 4007 a 6-octet
// TMS header sits between the UDP header and the CRLF; on 5016 there is none.
// Every other field is identical, including the checksum, so the difference is one
// offset.
func parseBody(body []byte) (Message, error) {
	if len(body) < ipHeaderLen+udpHeaderLen+crlfLen+crc32Len || len(body)%blockOctets != 0 {
		return Message{}, ErrMalformedBody
	}
	want := binary.LittleEndian.Uint32(body[len(body)-crc32Len:])
	if got := ituCRC32(body[:len(body)-crc32Len]); got != want {
		return Message{}, ErrBadCRC
	}
	if body[0]>>4 != 4 || body[9] != protocolUDP {
		return Message{}, ErrMalformedBody
	}

	var hdrLen int
	switch binary.BigEndian.Uint16(body[22:]) {
	case portTMS:
		hdrLen = udpHeaderLen + tmsHeaderLen
	case portETSI:
		hdrLen = udpHeaderLen
	default:
		return Message{}, ErrMalformedBody
	}

	// Bounds come from the datagram's own length field, checked against what
	// actually arrived: a header that overstates its payload must not be able to
	// steer a slice past the end of the blocks that carried it.
	udpLen := int(binary.BigEndian.Uint16(body[24:]))
	if udpLen < hdrLen+crlfLen || ipHeaderLen+udpLen > len(body) {
		return Message{}, ErrMalformedBody
	}
	start := ipHeaderLen + hdrLen + crlfLen
	end := start + (udpLen - hdrLen - crlfLen)
	if end > len(body) || (end-start)%2 != 0 {
		return Message{}, ErrMalformedBody
	}
	units := make([]uint16, 0, (end-start)/2)
	for i := start; i < end; i += 2 {
		units = append(units, binary.LittleEndian.Uint16(body[i:]))
	}
	return Message{
		Src:   u24(body[13:]),
		Dst:   u24(body[17:]),
		Group: body[16] == 0xE1,
		Seq:   binary.BigEndian.Uint16(body[4:]),
		Text:  string(utf16.Decode(units)),
	}, nil
}

// udpChecksum computes the UDP checksum over the pseudo-header (source and
// destination addresses, protocol, UDP length) and the datagram, with the
// checksum field itself zero.
func udpChecksum(b []byte, udpLen int) uint16 {
	pseudo := make([]byte, 12+udpLen)
	copy(pseudo[0:4], b[12:16])
	copy(pseudo[4:8], b[16:20])
	pseudo[9] = protocolUDP
	binary.BigEndian.PutUint16(pseudo[10:], uint16(udpLen))
	copy(pseudo[12:], b[ipHeaderLen:ipHeaderLen+udpLen])
	return onesComplementSum(pseudo)
}

// onesComplementSum is the internet checksum: sum the data as big-endian 16-bit
// words, fold the carries back in, complement.
//
// The fold LOOPS. The reference implementation this dialect came from folds once,
// which is correct only while the accumulated carry stays under 16 bits — true for
// every message short enough to have been tested by hand, and not true in general.
// Folding to a fixed point costs nothing and cannot be wrong.
func onesComplementSum(data []byte) uint16 {
	var sum uint32
	for i := 0; i+1 < len(data); i += 2 {
		sum += uint32(binary.BigEndian.Uint16(data[i:]))
	}
	if len(data)%2 != 0 {
		sum += uint32(data[len(data)-1]) << 8
	}
	for sum>>16 != 0 {
		sum = sum&0xFFFF + sum>>16
	}
	return ^uint16(sum)
}
