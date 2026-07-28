// Package modem identifies the MMDVM modem attached to a node: which board it
// is, what firmware it runs, what its reference oscillator is, and which modes
// that firmware can actually carry.
//
// Waypoint asks the modem exactly one question — "what are you?" — and never
// speaks the traffic protocol. MMDVM-Host owns the modem in normal operation,
// and two writers on one UART is not a thing that can be made safe; the probe
// exists for the moments when MMDVM-Host is *not* running (first boot, after a
// flash, when the operator presses Detect) and is arbitrated against it
// everywhere else.
//
// The wire format below is transcribed from MMDVM-Host's Modem.cpp — the
// upstream clone the stack builds, which is deliberately not vendored into this
// repo (.gitignore). It is pure and unit-tested here; the serial I/O is a thin
// separate layer (detect.go), so the framing can be tested without hardware.
package modem

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Protocol constants (Modem.cpp). Only the identity command is implemented:
// there is no code path in Waypoint that can key a transmitter.
const (
	frameStart    byte = 0xE0 // MMDVM_FRAME_START
	cmdGetVersion byte = 0x00 // MMDVM_GET_VERSION
)

// Capability bits reported by protocol-version 2 firmware, first byte
// (MMDVM-Host's CAP1_*). CapM17 and CapAX25 have no CAP1_ constant in current
// upstream MMDVM-Host — g4klx removed M17 from the host — but the firmware still
// sets them, and Waypoint's MMDVM-Host fork restores M17, so both are decoded
// here rather than discarded.
const (
	CapDStar uint8 = 0x01
	CapDMR   uint8 = 0x02
	CapYSF   uint8 = 0x04
	CapP25   uint8 = 0x08
	CapNXDN  uint8 = 0x10
	CapM17   uint8 = 0x20
	CapFM    uint8 = 0x40
	CapAX25  uint8 = 0x80
)

// Capability bits, second byte (CAP2_*).
const CapPOCSAG uint8 = 0x01

// maxSkip bounds the resync scan for a frame start byte. A modem that has just
// been opened may still have traffic in flight, and a port that is not a modem
// at all may be emitting anything; without a bound, "wait for 0xE0" is a hang.
const maxSkip = 512

// maxFrame is a sanity ceiling on a declared frame length. The protocol's
// two-byte form can declare up to 510 bytes; nothing larger is legal, and a
// larger value means the length byte was noise.
const maxFrame = 512

// ErrNotVersion reports a well-formed frame that answers something other than
// GET_VERSION — which on a shared port usually means MMDVM-Host is still
// running and we are reading its traffic.
var ErrNotVersion = errors.New("modem: frame is not a version reply")

// CPU is the microcontroller family a protocol-2 modem reports.
type CPU uint8

const (
	CPUAtmel CPU = 0
	CPUNXP   CPU = 1
	CPUST    CPU = 2
)

func (c CPU) String() string {
	switch c {
	case CPUAtmel:
		return "Atmel ARM"
	case CPUNXP:
		return "NXP ARM"
	case CPUST:
		return "ST-Micro ARM"
	default:
		return fmt.Sprintf("unknown (%d)", uint8(c))
	}
}

// Frame is one decoded MMDVM frame. Payload is the bytes after the type byte,
// so callers index from the start of the answer and never have to know whether
// the frame used the one- or two-byte length form.
type Frame struct {
	Type    byte
	Payload []byte
}

// VersionRequest is the only frame Waypoint ever writes to a modem: three bytes
// asking it to describe itself. It is a constructor rather than a package
// variable so a caller cannot accidentally mutate the one frame we send.
func VersionRequest() []byte { return []byte{frameStart, 0x03, cmdGetVersion} }

// ReadFrame reads one frame, resynchronising to the start byte first.
//
// Timeouts are the caller's business: this reads until it has a whole frame or
// the reader returns an error, and detect.go arms the port with a read timeout
// so a silent device surfaces as io.EOF rather than a stuck goroutine.
func ReadFrame(r io.Reader) (Frame, error) {
	var b [1]byte
	// Resync: drop bytes until the start marker. Upstream treats a non-start
	// byte as a timeout and retries the whole read; skipping is the same
	// recovery with the retry loop moved inside, which matters here because a
	// probe gets far fewer attempts than a running host does.
	for skipped := 0; ; skipped++ {
		if skipped > maxSkip {
			return Frame{}, errors.New("modem: no frame start in " + fmt.Sprint(maxSkip) + " bytes")
		}
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return Frame{}, err
		}
		if b[0] == frameStart {
			break
		}
	}

	// Length. A zero in the one-byte field means "the real length is in the
	// next byte, plus 255" — the protocol's escape for frames longer than a
	// byte can hold. Both forms count the header bytes themselves.
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return Frame{}, err
	}
	total, header := int(b[0]), 2
	if total == 0 {
		if _, err := io.ReadFull(r, b[:]); err != nil {
			return Frame{}, err
		}
		total, header = int(b[0])+255, 3
	}
	if total < header+1 || total > maxFrame {
		return Frame{}, fmt.Errorf("modem: implausible frame length %d", total)
	}

	rest := make([]byte, total-header)
	if _, err := io.ReadFull(r, rest); err != nil {
		return Frame{}, err
	}
	return Frame{Type: rest[0], Payload: rest[1:]}, nil
}

// Version is what a modem answers about itself.
//
// Description is the free-text identity string — the single most useful thing
// the modem says, because it carries board family, firmware version, reference
// oscillator and radio count all at once (see describe.go). Everything else here
// is only available from protocol-2 firmware.
type Version struct {
	Protocol    uint8
	Cap1, Cap2  uint8
	CPU         CPU
	UDID        string // unique chip ID, hex; "" on protocol 1
	Description string
}

// ParseVersion decodes a GET_VERSION reply.
//
// Two wire layouts exist. Protocol 1 is version byte then description. Protocol
// 2 inserts capability bits, a CPU-family byte and a unique chip ID, and puts
// the description at a FIXED offset regardless of how many ID bytes that family
// actually has — ST parts report 12 where the layout reserves 16, and the four
// slack bytes stay in the frame. Reproducing the fixed offset rather than
// computing it from the CPU family is deliberate: it is what the host does, so
// it is what firmware is written against.
func ParseVersion(f Frame) (Version, error) {
	if f.Type != cmdGetVersion {
		return Version{}, ErrNotVersion
	}
	if len(f.Payload) < 1 {
		return Version{}, errors.New("modem: empty version reply")
	}
	v := Version{Protocol: f.Payload[0]}
	switch v.Protocol {
	case 1:
		v.Description = cleanDescription(f.Payload[1:])
	case 2:
		// 0 proto | 1 cap1 | 2 cap2 | 3 cpu | 4..19 UDID | 20.. description
		const descOffset = 20
		if len(f.Payload) < descOffset {
			return Version{}, errors.New("modem: truncated protocol-2 version reply")
		}
		v.Cap1, v.Cap2, v.CPU = f.Payload[1], f.Payload[2], CPU(f.Payload[3])
		udid := f.Payload[4:descOffset]
		if v.CPU == CPUST {
			udid = udid[:12] // ST parts have a 96-bit ID; the rest is padding
		}
		v.UDID = strings.ToUpper(hex.EncodeToString(udid))
		v.Description = cleanDescription(f.Payload[descOffset:])
	default:
		return Version{}, fmt.Errorf("modem: unsupported protocol version %d", v.Protocol)
	}
	return v, nil
}

// cleanDescription trims the trailing NULs and whitespace firmware pads the
// string with, and drops anything unprintable. The result is stored, rendered
// into a fingerprint, and shown to an operator, so it must never carry control
// bytes out of this package.
func cleanDescription(b []byte) string {
	out := make([]rune, 0, len(b))
	for _, c := range b {
		if c >= 0x20 && c < 0x7F {
			out = append(out, rune(c))
		}
	}
	return strings.TrimSpace(string(out))
}

// Modes is which modes the firmware can carry. Known distinguishes "the modem
// told us" from "we assumed": protocol-1 firmware reports no capability bits at
// all, and MMDVM-Host fills in the classic five plus POCSAG. Waypoint mirrors
// that assumption so the two agree, but records that it IS an assumption —
// because the whole point of reading capabilities is to stop an operator
// enabling a mode the board cannot do, and a guess must not be allowed to
// masquerade as a fact.
type Modes struct {
	Known  bool
	DStar  bool
	DMR    bool
	YSF    bool
	P25    bool
	NXDN   bool
	M17    bool
	FM     bool
	POCSAG bool
	AX25   bool
}

// Modes decodes the capability bits.
func (v Version) Modes() Modes {
	if v.Protocol < 2 {
		// MMDVM-Host's protocol-1 assumption, byte for byte (Modem.cpp).
		return Modes{DStar: true, DMR: true, YSF: true, P25: true, NXDN: true, POCSAG: true}
	}
	return Modes{
		Known:  true,
		DStar:  v.Cap1&CapDStar != 0,
		DMR:    v.Cap1&CapDMR != 0,
		YSF:    v.Cap1&CapYSF != 0,
		P25:    v.Cap1&CapP25 != 0,
		NXDN:   v.Cap1&CapNXDN != 0,
		M17:    v.Cap1&CapM17 != 0,
		FM:     v.Cap1&CapFM != 0,
		AX25:   v.Cap1&CapAX25 != 0,
		POCSAG: v.Cap2&CapPOCSAG != 0,
	}
}

// Supports reports whether the firmware carries the named Waypoint mode, using
// the same keys as the config store's modes section. An unknown name is false.
func (m Modes) Supports(mode string) bool {
	switch mode {
	case "dstar":
		return m.DStar
	case "dmr":
		return m.DMR
	case "ysf":
		return m.YSF
	case "p25":
		return m.P25
	case "nxdn":
		return m.NXDN
	case "m17":
		return m.M17
	case "fm":
		return m.FM
	case "pocsag":
		return m.POCSAG
	}
	return false
}
