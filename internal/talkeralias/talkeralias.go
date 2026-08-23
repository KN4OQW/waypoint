// Package talkeralias encodes a DMR Talker Alias into the four DMRA frames
// MMDVM-Host accepts on its network socket, so a receiving radio can display an
// operator's callsign and name instead of a bare numeric ID.
//
// # Ground truth
//
// The frame layout is MMDVM-Host's own, read off the function that emits it in
// the other direction (CDMRNetwork::writeTalkerAlias, DMRNetwork.cpp) and mirrored
// by the inbound parser in the Waypoint fork (CDMRNetwork::readTalkerAlias):
//
//	[0:4]  "DMRA"
//	[4:7]  source DMR id, 24-bit big-endian
//	[7]    block type, 0..3 — header, block 1, block 2, block 3
//	[8:15] seven alias bytes: embedded-LC bytes 2..8, after the FLCO and the FID
//
// The alias content format is ETSI's, but the numbers below were MEASURED against
// g4klx's own decoder (CDMRTA::decodeTA) rather than read out of a specification:
// the decoder was compiled and driven with constructed bytes, and every ceiling
// and alignment here is what it actually accepted. Clause-level citations to
// ETSI TS 102 361-2 are pending access to that document and are deliberately not
// invented.
//
// # The header byte and the three encodings
//
// The first alias byte carries the format in bits 7-6 and the character count in
// bits 5-1. Bit 0 is NOT padding — in 7-bit mode it is the top bit of the first
// character, because the text is a bit stream that starts at bit 7 of the block
// rather than at a byte boundary. That one bit is the difference between an alias
// that decodes and one that renders as mojibake, and it is why Format7Bit has its
// own packer instead of sharing the byte-aligned path.
//
// Measured ceilings, and what they are:
//
//	7-bit        31 characters — four blocks are 28 bytes = 224 bits, less the
//	                             7-bit header leaves 217, and 217/7 = 31. The
//	                             length field is 5 bits, so 31 is also the widest
//	                             number it can express: asking for 32 wraps it to 0.
//	ISO-8859-1   27 characters — byte-aligned from block byte 1; 28 - 1 = 27.
//	UTF-8        27 bytes      — same alignment. Bytes, not characters.
//	UTF-16BE     13 characters — pairs from byte 1; 27/2 = 13.
//
// UTF-16 is BIG-endian. The decoder reads the byte at the odd offset as the one
// that must be zero and the byte at the even offset as the character; feeding it
// little-endian produces '?' for every character. Measured, both ways.
package talkeralias

import (
	"errors"
	"strings"
	"unicode"
)

// Format is the encoding named in the alias header's top two bits.
type Format uint8

const (
	// Format7Bit packs seven-bit characters into a bit stream. The default: it
	// fits the most characters, and a callsign and a name are ASCII.
	Format7Bit Format = 0
	// FormatISO8859_1 is one byte per character, byte-aligned.
	FormatISO8859_1 Format = 1
	// FormatUTF8 is byte-aligned UTF-8. Its ceiling is in BYTES, not characters.
	FormatUTF8 Format = 2
	// FormatUTF16BE is two bytes per character, big-endian. Last resort: it holds
	// the fewest characters, and MMDVM-Host's decoder renders anything outside
	// Latin-1 as '?' regardless.
	FormatUTF16BE Format = 3
)

// Measured ceilings. See the package comment for the arithmetic and for how each
// was checked against g4klx's decoder.
const (
	Max7Bit  = 31
	Max8Bit  = 27
	MaxUTF16 = 13
)

// Frame sizes, from MMDVM-Host's writeTalkerAlias.
const (
	// FrameLen is a whole DMRA datagram.
	FrameLen = 15
	// blockPayload is the alias bytes one block carries.
	blockPayload = 7
	// blocks is how many blocks an alias is: a header and three more.
	blocks = 4
	// aliasBytes is the assembled alias buffer the blocks are cut from.
	aliasBytes = blocks * blockPayload // 28
)

// ErrNoAlias reports that there is nothing to send: an empty string after
// trimming. It is a distinct error because "this operator has no alias" is the
// ordinary case on most nodes and a caller must not treat it as a failure.
var ErrNoAlias = errors.New("talkeralias: nothing to encode")

// ErrBadID reports a source id that cannot be carried. The field is 24 bits.
var ErrBadID = errors.New("talkeralias: DMR ID must be 1..16777215")

// MaxID is the widest source id a DMRA frame can carry: the field is three bytes.
const MaxID = 0xFFFFFF

// ChooseFormat picks the narrowest encoding that carries s.
//
// 7-bit first because it holds the most characters and a callsign is ASCII;
// ISO-8859-1 when a character needs the eighth bit but still fits one byte;
// UTF-8 when it does not. UTF-16 is never chosen automatically — it holds the
// fewest characters and buys nothing over UTF-8 against a decoder that reduces
// non-Latin-1 to '?' either way. It is in the API because the wire format has it,
// not because anything here should reach for it.
func ChooseFormat(s string) Format {
	ascii := true
	latin1 := true
	for _, r := range s {
		if r > unicode.MaxASCII {
			ascii = false
		}
		if r > 0xFF {
			latin1 = false
		}
	}
	switch {
	case ascii:
		return Format7Bit
	case latin1:
		return FormatISO8859_1
	default:
		return FormatUTF8
	}
}

// Capacity is how many units a format carries — characters, except for UTF-8
// where it is bytes.
func Capacity(f Format) int {
	switch f {
	case Format7Bit:
		return Max7Bit
	case FormatUTF16BE:
		return MaxUTF16
	default:
		return Max8Bit
	}
}

// Encode builds the four DMRA datagrams carrying alias for srcID.
//
// The text is trimmed and truncated to the format's ceiling. Truncation is by
// whole characters, never mid-rune: a name cut in the middle of a multi-byte
// character would decode as a replacement glyph, which looks like a fault rather
// than like a long name.
//
// Every alias is sent as the full four blocks even when the text would fit in
// fewer. The block count is not encoded anywhere in the format — a receiver knows
// an alias is complete when it has assembled the character count the header
// declared — and the trailing blocks are the zero padding that count is measured
// against.
func Encode(srcID uint32, alias string, f Format) ([][]byte, error) {
	if srcID == 0 || srcID > MaxID {
		return nil, ErrBadID
	}
	text := strings.TrimSpace(alias)
	if text == "" {
		return nil, ErrNoAlias
	}
	text = truncate(text, f)
	if text == "" {
		return nil, ErrNoAlias
	}

	buf := make([]byte, aliasBytes)
	n := pack(buf, text, f)
	// Header: format in bits 7-6, character count in bits 5-1. Written after the
	// text because the 7-bit packer needs bit 0 of this byte for the first
	// character, and ORing the header in afterwards is what preserves it.
	buf[0] |= byte(f)<<6 | byte(n&0x1F)<<1

	out := make([][]byte, blocks)
	for i := range blocks {
		fr := make([]byte, FrameLen)
		copy(fr, "DMRA")
		fr[4] = byte(srcID >> 16)
		fr[5] = byte(srcID >> 8)
		fr[6] = byte(srcID)
		fr[7] = byte(i)
		copy(fr[8:], buf[i*blockPayload:(i+1)*blockPayload])
		out[i] = fr
	}
	return out, nil
}

// truncate cuts text to the format's ceiling, by runes for the character formats
// and by whole runes within a byte budget for UTF-8.
func truncate(text string, f Format) string {
	limit := Capacity(f)
	if f == FormatUTF8 {
		if len(text) <= limit {
			return text
		}
		// Back off to a rune boundary rather than slicing mid-sequence.
		cut := limit
		for cut > 0 && !utf8Start(text[cut]) {
			cut--
		}
		return text[:cut]
	}
	r := []rune(text)
	if len(r) <= limit {
		return text
	}
	return string(r[:limit])
}

// utf8Start reports whether b begins a UTF-8 sequence (i.e. is not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// pack writes the text into buf in the format's layout and returns the unit count
// for the header: characters, or bytes for UTF-8.
func pack(buf []byte, text string, f Format) int {
	switch f {
	case Format7Bit:
		// The bit stream starts at bit 7 — immediately after the seven header bits
		// — so the first character's top bit lands in bit 0 of buf[0]. Bits are
		// most-significant first within both the character and the byte.
		bit := 7
		n := 0
		for _, r := range text {
			c := byte(r) & 0x7F
			for j := 6; j >= 0; j-- {
				if c>>uint(j)&1 == 1 {
					buf[bit/8] |= 0x80 >> uint(bit%8)
				}
				bit++
			}
			n++
		}
		return n

	case FormatUTF16BE:
		n := 0
		for _, r := range text {
			// High byte at the odd offset, low at the even one: the decoder tests the
			// odd byte for zero and takes the even one as the character. That is
			// big-endian, and little-endian decodes as '?' for every character.
			buf[2*n+1] = byte(r >> 8)
			buf[2*n+2] = byte(r)
			n++
		}
		return n

	case FormatISO8859_1:
		n := 0
		for _, r := range text {
			buf[1+n] = byte(r)
			n++
		}
		return n

	default: // FormatUTF8 — byte-aligned, count is bytes
		b := []byte(text)
		copy(buf[1:], b)
		return len(b)
	}
}
