// Package vocoder turns AMBE+2 codewords into audio and back, which a bridge
// needs when voice has to leave the digital modes for a codec nothing on the air
// speaks.
//
// The bus itself never needs this. It carries AMBE+2 codewords between modes and
// copies them verbatim, which is why internal/bus/router can promise it touches
// no codec. A Zello attachment is the first thing that has to turn those
// codewords into audio, so the vocoder lives at the endpoint edge and that
// promise survives.
//
// # What this wraps, and what it deliberately does not
//
// The implementation is AD8DP's md380_vocoder (GPL-2.0-or-later), linked in
// process behind the `zello` build tag on 32-bit ARM only. Its plain entry
// points take and return the 49-bit codeword packed into seven bytes MSB-first,
// which is byte-for-byte internal/bus/frames' canonical form — so nothing here
// repacks anything, and what Encode returns goes straight to
// dmrAMBEFromCanonical for the on-air FEC.
//
// It does NOT use the library's md380_encode_fec / md380_decode_fec. The 72-bit
// protected form is internal/bus/frames' business and is golden-tested there.
// Applying protection to already-protected bits is noise on the air, and nothing
// between here and the radio reports it.
//
// # Why not md380-emu
//
// The earlier design drove md380-emu's -S UDP server instead. That server
// segfaults on the first request on kernel 6.12.25 — in our build, and in the
// DVSwitch fork's own unmodified binary, so it is md380tools issue #925 rather
// than anything we did. The vocoder inside the same binary is healthy; only the
// server entry point dies. See docs/zello/ground-truth.md.
//
// # Constraints that are not obvious
//
// The firmware is mapped at fixed addresses (0x0800c000 and 0x20000000), so
// there can be exactly one Vocoder per process — Open refuses a second. And the
// library computes through fixed buffers inside that firmware image, so it is
// not reentrant: every call is serialised, and a caller gains nothing from
// calling it on two goroutines.
//
// The decoder needs one frame of warm-up. Measured on a Pi 3, frame 0 of a
// sequence comes back at rms 38 against an input of 8485, frame 1 at 7444, and
// everything after at about 9167. That is the codec's model converging, not a
// fault, and it means the first 20 ms of a transmission is near-silent unless
// the caller primes the decoder.
package vocoder

import (
	"errors"
	"time"
)

const (
	// SamplesPerFrame is one 20 ms frame of 8 kHz audio, the only unit the
	// vocoder works in. There is no such thing as a partial frame.
	SamplesPerFrame = 160

	// PCMBytes is SamplesPerFrame as bytes of signed 16-bit audio.
	PCMBytes = SamplesPerFrame * 2

	// CodewordBytes is the canonical AMBE+2 codeword: 49 bits packed MSB-first,
	// bit 48 in byte 6 as 0x80. Identical to frames.AMBEBytes, and deliberately
	// not the 8-byte .amb file frame, which carries a status byte and puts bit
	// 48 in byte 7 as the LSB.
	CodewordBytes = 7

	// FrameDuration is what one frame represents. A node that cannot service a
	// frame in less than this cannot bridge in real time. The bench Pi 3 does it
	// in about 1 ms.
	FrameDuration = 20 * time.Millisecond
)

var (
	// ErrUnsupported is returned by Open on a build without the `zello` tag, or
	// on any architecture other than 32-bit ARM. The vocoder is the MD380's own
	// firmware executed natively; there is nothing to fall back to elsewhere.
	ErrUnsupported = errors.New("vocoder: not built with the zello tag on linux/arm")

	// ErrAlreadyOpen is returned when a second Vocoder is opened in the same
	// process. The firmware occupies fixed addresses, so a second instance would
	// share one set of internal buffers with the first and interleave its audio.
	ErrAlreadyOpen = errors.New("vocoder: a vocoder is already open in this process")
)

// Config names the firmware images to map at run time.
//
// These are never committed and never linked. The library as distributed
// objcopy-embeds them into its archive, and an artifact built that way
// redistributes a licensed blob in every image — so Waypoint links the vocoder
// without firmware.o and ram.o and maps the bytes from these paths instead.
// They are fetched when the operator enables the feature.
type Config struct {
	// FirmwarePath is the unwrapped MD380 firmware image, D002.032, mapped at
	// 0x0800c000. It is exactly 0xf2c00 bytes, which is the length md380_init
	// makes executable.
	FirmwarePath string

	// RAMPath is the matching SRAM core image, mapped at 0x20000000.
	RAMPath string
}
