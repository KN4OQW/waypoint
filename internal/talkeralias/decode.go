package talkeralias

import (
	"errors"
	"strings"
)

// Decoding exists so the encoder can be checked against the format rather than
// against itself. It mirrors the semantics of MMDVM-Host's CDMRTA::decodeTA —
// which is the code that will actually read these frames — including the details
// that are easy to get wrong: the 7-bit stream starting at bit 7, the byte
// alignment of the 8-bit formats, and UTF-16's big-endian pairs.
//
// Nothing in the daemon calls this. It is the other half of a round trip, and a
// round trip that both halves of get wrong the same way proves nothing — so the
// package's tests ALSO drive the real C++ decoder, compiled from the fork, over
// the bytes this package produces. See the test file.

// ErrBadFrame reports a DMRA datagram that is not one.
var ErrBadFrame = errors.New("talkeralias: not a DMRA frame")

// ErrIncomplete reports a block set that is missing blocks or has duplicates.
var ErrIncomplete = errors.New("talkeralias: need exactly the four blocks, one of each")

// Decode reassembles the alias from four DMRA datagrams and returns it with the
// source id and the format it was carried in.
//
// The blocks may be supplied in any order — the wire has no ordering guarantee
// and each frame names its own block type — but all four must be present exactly
// once and all four must name the same source.
func Decode(frames [][]byte) (srcID uint32, alias string, f Format, err error) {
	if len(frames) != blocks {
		return 0, "", 0, ErrIncomplete
	}
	var (
		buf  = make([]byte, aliasBytes)
		seen [blocks]bool
		id   uint32
	)
	for i, fr := range frames {
		if len(fr) != FrameLen || string(fr[0:4]) != "DMRA" {
			return 0, "", 0, ErrBadFrame
		}
		t := fr[7]
		if t > 3 || seen[t] {
			return 0, "", 0, ErrIncomplete
		}
		seen[t] = true
		this := uint32(fr[4])<<16 | uint32(fr[5])<<8 | uint32(fr[6])
		if i == 0 {
			id = this
		} else if this != id {
			return 0, "", 0, ErrIncomplete
		}
		copy(buf[int(t)*blockPayload:], fr[8:])
	}

	f = Format(buf[0] >> 6)
	n := int(buf[0] >> 1 & 0x1F)
	return id, unpack(buf, n, f), f, nil
}

// unpack is pack's inverse, and deliberately reproduces MMDVM-Host's reading of
// each format rather than a tidier one.
func unpack(buf []byte, n int, f Format) string {
	switch f {
	case Format7Bit:
		// The stream starts at bit 7: the seven header bits are followed
		// immediately by the first character's top bit, still inside buf[0].
		var sb strings.Builder
		bit := 7
		for range n {
			var c byte
			for range 7 {
				c <<= 1
				if buf[bit/8]&(0x80>>uint(bit%8)) != 0 {
					c |= 1
				}
				bit++
			}
			sb.WriteByte(c & 0x7F)
		}
		return sb.String()

	case FormatUTF16BE:
		var sb strings.Builder
		for i := range n {
			// High byte at the odd offset, low at the even one — see pack.
			sb.WriteRune(rune(buf[2*i+1])<<8 | rune(buf[2*i+2]))
		}
		return sb.String()

	case FormatISO8859_1:
		var sb strings.Builder
		for i := range n {
			sb.WriteRune(rune(buf[1+i]))
		}
		return sb.String()

	default: // FormatUTF8 — n is a byte count
		return string(buf[1 : 1+n])
	}
}
