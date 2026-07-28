// Package cal calibrates a modem: it finds the frequency error of a hotspot's
// reference oscillator by sweeping the board's own frequency against a bit error
// rate, and drives the level/invert workflow a full-size repeater board needs.
//
// It is the second thing in Waypoint that talks to a modem, and the first that
// can key a transmitter. RFC-0021 states the safety model before the mechanism
// and this package is built in that order:
//
//   - The receive sweep — the flow an operator actually runs — cannot key the
//     radio, because the firmware refuses CAL_DATA outside a transmit
//     calibration state. That is a property of the board, not a promise of ours.
//   - Everything that CAN key is bounded by a dead-man timer owned by this
//     package (session.go), transmits only on the frequency already configured,
//     and is refused outside an amateur allocation.
//
// The wire format is transcribed from the two firmware parsers rather than from
// a host: MMDVM_HS's SerialPort.cpp::setConfig for the protocol-1 form, and
// g4klx/MMDVM's for protocol 2. Those are the programs that actually accept or
// NAK these bytes, so they are the authority on what the bytes mean — and the
// two disagree about more than length (§ Config.FrameV1).
//
// Framing, the version reply and the serial layer all come from internal/modem;
// nothing here reimplements them.
package cal

import (
	"errors"
	"fmt"
)

// Commands. These are the calibration subset of the MMDVM protocol; the identity
// command lives in internal/modem, which is the only other place Waypoint speaks
// this protocol at all.
const (
	frameStart   byte = 0xE0
	CmdGetStatus byte = 0x01
	CmdSetConfig byte = 0x02
	CmdSetFreq   byte = 0x04
	CmdCalData   byte = 0x08
	CmdRSSIData  byte = 0x09

	CmdDStarHeader byte = 0x10
	CmdDStarData   byte = 0x11
	CmdDStarLost   byte = 0x12
	CmdDStarEOT    byte = 0x13

	CmdDMRData1 byte = 0x18
	CmdDMRLost1 byte = 0x19
	CmdDMRData2 byte = 0x1A
	CmdDMRLost2 byte = 0x1B

	CmdACK byte = 0x70
	CmdNAK byte = 0x7F
)

// State is the modem state byte. The receive states are ordinary operating
// modes; the CAL_* states are the ones that transmit, and they are named as
// such so no call site can key a radio without the word "cal" in front of it.
type State uint8

const (
	StateIdle  State = 0
	StateDStar State = 1
	StateDMR   State = 2
	StateYSF   State = 3
	StateP25   State = 4
	StateNXDN  State = 5

	// Transmit calibration states. Availability differs by board: a hotspot
	// accepts only DMRCal, DMRDMO1K, RSSICal, IntCal, POCSAGCal and DStarCal
	// and NAKs the rest (MMDVM_HS SerialPort.cpp::setConfig), while a full-size
	// MMDVM additionally accepts the low-frequency, 1 kHz and FM patterns.
	StateNXDNCal1K State = 91
	StateDMRDMO1K  State = 92
	StateP25Cal1K  State = 93
	StateDMRCal1K  State = 94
	StateLFCal     State = 95
	StateRSSICal   State = 96
	StateDMRCal    State = 98
	StateDStarCal  State = 99
	StateIntCal    State = 100
	StatePOCSAGCal State = 101
	StateFMCal10K  State = 102
	StateFMCal12K  State = 103
	StateFMCal15K  State = 104
	StateFMCal20K  State = 105
	StateFMCal25K  State = 106
	StateFMCal30K  State = 107
)

// Transmits reports whether entering this state can put a carrier on the air.
// The sweep states (StateDMR and friends) are receive-only: the firmware's
// CAL_DATA handler will not key from them, and this is the predicate the
// session layer checks before it will accept a transmit request at all.
func (s State) Transmits() bool {
	switch s {
	case StateIdle, StateDStar, StateDMR, StateYSF, StateP25, StateNXDN:
		return false
	}
	return true
}

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateDStar:
		return "D-Star"
	case StateDMR:
		return "DMR"
	case StateDMRCal:
		return "DMR deviation"
	case StateDMRDMO1K:
		return "DMR 1031 Hz test pattern"
	case StateDMRCal1K:
		return "DMR duplex 1031 Hz test pattern"
	case StateDStarCal:
		return "D-Star tone"
	case StateLFCal:
		return "80 Hz square wave"
	case StateRSSICal:
		return "RSSI"
	case StateIntCal:
		return "interrupt counter"
	case StatePOCSAGCal:
		return "POCSAG 600 Hz test pattern"
	}
	return fmt.Sprintf("state %d", uint8(s))
}

// Levels are the analog settings a full-size MMDVM needs and a hotspot ignores.
//
// A hotspot board is a direct link to an ADF7021 and its SET_CONFIG parser reads
// none of these except the per-mode transmit levels, which it uses as deviation:
// invert flags, RX level and DC offsets are simply not present in MMDVM_HS's
// setConfig. That asymmetry is the whole reason #20 has two workflows, and it is
// recorded here rather than discovered by an operator wondering why the RX level
// slider does nothing on their hotspot.
type Levels struct {
	RX      float32 // 0..100%
	TX      float32 // 0..100%, and the per-mode deviation on a hotspot
	TXDCOff int     // -128..127
	RXDCOff int     // -128..127
}

// Config is one SET_CONFIG. The zero value is not useful: build it with
// SweepConfig or TXConfig, which set the fields that have to agree with each
// other (a state whose mode is not enabled is NAKed by both firmwares).
type Config struct {
	State State

	RXInvert  bool
	TXInvert  bool
	PTTInvert bool
	Simplex   bool // sent as the "simplex" bit; !duplex
	Debug     bool
	YSFLoDev  bool

	DStar, DMR, YSF, P25, NXDN, POCSAG, FM bool

	TXDelay   uint8 // in 10 ms units; the firmware NAKs anything over 50
	ColorCode uint8 // 0..15
	DMRDelay  uint8

	Levels Levels
}

// ErrConfig reports a Config the firmware would NAK, caught here so the failure
// names the field rather than arriving as reason code 4.
var ErrConfig = errors.New("cal: invalid modem configuration")

// SweepConfig is the receive configuration a BER sweep runs in: DMR, simplex,
// nothing else enabled.
//
// It is a constructor rather than a struct literal at the call site because two
// of these fields are load-bearing and neither looks it. StateDMR with DMR
// enabled is an ORDINARY RECEIVE STATE, which is what makes the sweep unable to
// key the radio (RFC-0021 §2) — the firmware's transmit command only acts from a
// cal state. And simplex must be set: a duplex configuration on a single-radio
// board is NAKed with reason 6, which an operator would read as "calibration is
// broken" rather than "that flag was wrong".
func SweepConfig(colorCode uint8, level float32) Config {
	return Config{
		State:     StateDMR,
		DMR:       true,
		Simplex:   true,
		ColorCode: colorCode,
		Levels:    Levels{RX: 50, TX: level},
	}
}

// TXConfig is a transmit calibration state — a tone, a test pattern or a bare
// carrier. It panics on a receive state rather than returning an error, because
// a caller asking to transmit in a state that cannot transmit is a bug in
// Waypoint and not a condition an operator can be told about usefully.
func TXConfig(state State, colorCode uint8, level float32) Config {
	if !state.Transmits() {
		panic("cal: TXConfig called with the receive state " + state.String())
	}
	c := Config{
		State:     state,
		Simplex:   true,
		ColorCode: colorCode,
		Levels:    Levels{RX: 50, TX: level},
	}
	// The firmware maps its DMR cal states onto STATE_DMR internally and needs
	// the matching enable, exactly as a receive state does.
	switch state {
	case StateDMRCal, StateDMRCal1K, StateDMRDMO1K, StateLFCal, StateRSSICal, StateIntCal:
		c.DMR = true
	case StateDStarCal:
		c.DStar = true
	case StatePOCSAGCal:
		c.POCSAG = true
	case StateFMCal10K, StateFMCal12K, StateFMCal15K, StateFMCal20K, StateFMCal25K, StateFMCal30K:
		c.FM = true
	}
	return c
}

// Validate applies the checks both firmwares apply, so a bad frame is refused
// with a sentence instead of a number.
func (c Config) Validate() error {
	if c.TXDelay > 50 {
		return fmt.Errorf("%w: TX delay %d exceeds the firmware's limit of 50", ErrConfig, c.TXDelay)
	}
	if c.ColorCode > 15 {
		return fmt.Errorf("%w: colour code %d is not 0-15", ErrConfig, c.ColorCode)
	}
	// A receive state is only accepted when its own mode is enabled. This is the
	// mistake that produces a bare NAK on the wire, and it is the one a sweep
	// makes if it forgets to set DMR alongside StateDMR.
	need := map[State]struct {
		on   bool
		name string
	}{
		StateDStar: {c.DStar, "D-Star"},
		StateDMR:   {c.DMR, "DMR"},
		StateYSF:   {c.YSF, "YSF"},
		StateP25:   {c.P25, "P25"},
		StateNXDN:  {c.NXDN, "NXDN"},
	}
	if n, ok := need[c.State]; ok && !n.on {
		return fmt.Errorf("%w: state is %s but %s is not enabled", ErrConfig, c.State, n.name)
	}
	if c.Levels.TXDCOff < -128 || c.Levels.TXDCOff > 127 {
		return fmt.Errorf("%w: TX DC offset %d is outside -128..127", ErrConfig, c.Levels.TXDCOff)
	}
	if c.Levels.RXDCOff < -128 || c.Levels.RXDCOff > 127 {
		return fmt.Errorf("%w: RX DC offset %d is outside -128..127", ErrConfig, c.Levels.RXDCOff)
	}
	return nil
}

// FrameV1 builds the protocol-1 SET_CONFIG: 27 bytes on the wire, 24 of payload.
//
// The two forms differ by more than length. Protocol 1 packs the mode enables
// into one byte (POCSAG is bit 5) where protocol 2 uses two (bit 5 becomes FM,
// and POCSAG moves to a byte of its own), and it orders everything after the
// state differently: RX level comes before the per-mode levels here and after
// the DC offsets there. So every field past the state sits at a different
// offset, and writing the two out longhand rather than sharing a builder with
// two index tables is deliberate — a shared table is how a field ends up one
// byte out on the form nobody has hardware to catch it on.
//
// One byte is worth naming: d[8] is dead. It carried an oscillator offset in
// firmware old enough that MMDVM-Host still writes 128 there with the comment
// "Was OscOffset" — a calibration field that predates all of this and was
// abandoned. The offsets this package measures go to SET_FREQ instead.
//
//	0 flags     4 rxLevel      8 (dead)         13 txDCOffset  17 pocsag level
//	1 modes     5 cwId level   9 dstar level    14 rxDCOffset  18 fm level
//	2 txDelay   6 colourCode  10 dmr level      15 nxdn level  19 p25 hang
//	3 state     7 dmrDelay    11 ysf level      16 ysf hang    20 nxdn hang
//	                          12 p25 level                     21 m17 level
func (c Config) FrameV1() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	const total = 27
	b := make([]byte, total)
	b[0], b[1], b[2] = frameStart, total, CmdSetConfig

	d := b[3:]
	d[0] = c.flags()
	d[1] = c.modeBitsV1()
	d[2] = c.TXDelay
	d[3] = byte(c.State)
	d[4] = pct(c.Levels.RX)
	// The CW-ID level. Nothing keys a Morse identifier during calibration, so it
	// tracks the transmit level rather than being left at zero — a hotspot reads
	// this byte >> 2 and a value of zero would be a silent ID if a caller ever
	// left the modem in this configuration.
	d[5] = pct(c.Levels.TX)
	d[6] = c.ColorCode
	d[7] = c.DMRDelay
	d[8] = 128 // was OscOffset; dead, and 128 is what MMDVM-Host still writes
	tx := pct(c.Levels.TX)
	d[9], d[10], d[11], d[12] = tx, tx, tx, tx // D-Star, DMR, YSF, P25
	d[13] = byte(c.Levels.TXDCOff + 128)
	d[14] = byte(c.Levels.RXDCOff + 128)
	d[15] = tx // NXDN
	d[16] = 0
	d[17] = tx // POCSAG
	d[18] = tx
	d[21] = tx  // M17, read only when the payload is at least 22 bytes
	d[22] = 134 // +6, the firmware's own idle default
	return b, nil
}

// FrameV2 builds the protocol-2 SET_CONFIG: 40 bytes on the wire, 37 of payload,
// which is exactly the minimum g4klx/MMDVM's parser accepts.
//
//	0 flags     3 txDelay   6 rxDCOffset   8 cwId level    13 nxdn level   20 ysf hang
//	1 modes1    4 state     7 rxLevel      9..12 levels    15 pocsag level 21 p25 hang
//	2 modes2    5 txDCOffset                               16 fm level     22 nxdn hang
func (c Config) FrameV2() ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	const total = 40
	b := make([]byte, total)
	b[0], b[1], b[2] = frameStart, total, CmdSetConfig

	d := b[3:]
	d[0] = c.flags()
	d[1] = c.modeBitsV2()
	if c.POCSAG {
		d[2] |= 0x01
	}
	d[3] = c.TXDelay
	d[4] = byte(c.State)
	d[5] = byte(c.Levels.TXDCOff + 128)
	d[6] = byte(c.Levels.RXDCOff + 128)
	d[7] = pct(c.Levels.RX)
	tx := pct(c.Levels.TX)
	for i := 8; i <= 17; i++ {
		d[i] = tx // CW-ID, D-Star, DMR, YSF, P25, NXDN, M17, POCSAG, FM, AX.25
	}
	d[26] = c.ColorCode
	d[27] = c.DMRDelay
	d[28] = 134 // +6
	return b, nil
}

// flags is the first payload byte, identical in both forms.
func (c Config) flags() byte {
	var f byte
	if c.RXInvert {
		f |= 0x01
	}
	if c.TXInvert {
		f |= 0x02
	}
	if c.PTTInvert {
		f |= 0x04
	}
	if c.YSFLoDev {
		f |= 0x08
	}
	if c.Debug {
		f |= 0x10
	}
	if c.Simplex {
		f |= 0x80
	}
	return f
}

// modeBitsV1 packs the enables the protocol-1 parser reads. POCSAG is bit 5 here
// and moves to a second byte in protocol 2 — the single most confusable
// difference between the two forms, because getting it wrong enables FM instead.
func (c Config) modeBitsV1() byte {
	var m byte
	for _, bit := range []struct {
		on bool
		v  byte
	}{
		{c.DStar, 0x01}, {c.DMR, 0x02}, {c.YSF, 0x04},
		{c.P25, 0x08}, {c.NXDN, 0x10}, {c.POCSAG, 0x20},
	} {
		if bit.on {
			m |= bit.v
		}
	}
	return m
}

func (c Config) modeBitsV2() byte {
	var m byte
	for _, bit := range []struct {
		on bool
		v  byte
	}{
		{c.DStar, 0x01}, {c.DMR, 0x02}, {c.YSF, 0x04},
		{c.P25, 0x08}, {c.NXDN, 0x10}, {c.FM, 0x20},
	} {
		if bit.on {
			m |= bit.v
		}
	}
	return m
}

// pct converts a percentage to the modem's 0..255 byte, rounding the way both
// firmwares' hosts do (× 2.55, + 0.5) so a level set here reads back the same as
// one set by MMDVM-Host.
func pct(v float32) byte {
	switch {
	case v <= 0:
		return 0
	case v >= 100:
		return 255
	}
	return byte(v*2.55 + 0.5)
}

// SetFreq builds a SET_FREQ. rx and tx are absolute frequencies in Hz — the
// offset the sweep is looking for is applied by the caller, because the modem
// has no concept of an offset and the arithmetic belongs where it can be tested.
//
// power is the RF level percentage, which a hotspot uses to set ADF7021 output
// power and a full-size board ignores.
func SetFreq(rx, tx uint32, power float32) []byte {
	const total = 13
	b := make([]byte, total)
	b[0], b[1], b[2] = frameStart, total, CmdSetFreq
	b[3] = 0x00 // reserved; the firmware skips it
	putLE32(b[4:], rx)
	putLE32(b[8:], tx)
	b[12] = pct(power)
	return b
}

func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// SetTransmit builds the CAL_DATA frame that keys or unkeys the transmitter.
//
// It is unexported deliberately. Nothing outside this package builds this frame;
// transmitting goes through Session.Transmit, which is where the dead-man timer,
// the band check and the state check live. A caller that can assemble four bytes
// of its own can key a radio with no timer at all, and that is precisely the
// thing RFC-0021 §2 is written to prevent.
func setTransmit(on bool) []byte {
	b := []byte{frameStart, 4, CmdCalData, 0x00}
	if on {
		b[3] = 0x01
	}
	return b
}

// GetStatus asks the modem for its status frame — buffer space, overflow flags
// and the mode it believes it is in.
func GetStatus() []byte { return []byte{frameStart, 3, CmdGetStatus} }

// LevelReport is the CAL_DATA reply a full-size MMDVM sends while calibrating:
// the extremes of the received waveform, and whether it arrived inverted.
//
// Inverted is the useful one, and the reason the repeater workflow is worth
// shipping without a repeater board to test it on: RX inversion is the one
// analog setting the modem can simply be asked about, rather than measured with
// equipment the operator may not own (RFC-0021 §6).
type LevelReport struct {
	Inverted bool  `json:"inverted"`
	Max      int16 `json:"max"`
	Min      int16 `json:"min"`
	Diff     int16 `json:"diff"`
	Centre   int16 `json:"centre"`
}

// ParseLevels decodes a CAL_DATA reply payload (the bytes after the type byte).
func ParseLevels(payload []byte) (LevelReport, error) {
	if len(payload) < 5 {
		return LevelReport{}, fmt.Errorf("cal: level reply is %d bytes, want at least 5", len(payload))
	}
	high := int16(payload[1])<<8 | int16(payload[2])
	low := int16(payload[3])<<8 | int16(payload[4])
	return LevelReport{
		Inverted: payload[0] == 0x80,
		Max:      high,
		Min:      low,
		Diff:     high - low,
		Centre:   (high + low) / 2,
	}, nil
}

// RSSIReport is the RSSI_DATA reply: raw ADF7021 counts, not dBm. Turning these
// into dBm needs a mapping file produced with a signal generator, which is why
// RFC-0021 §8 leaves generating one out of v1.
type RSSIReport struct {
	Max uint16 `json:"max"`
	Min uint16 `json:"min"`
	Ave uint16 `json:"ave"`
}

// ParseRSSI decodes an RSSI_DATA reply payload.
func ParseRSSI(payload []byte) (RSSIReport, error) {
	if len(payload) < 6 {
		return RSSIReport{}, fmt.Errorf("cal: RSSI reply is %d bytes, want at least 6", len(payload))
	}
	return RSSIReport{
		Max: uint16(payload[0])<<8 | uint16(payload[1]),
		Min: uint16(payload[2])<<8 | uint16(payload[3]),
		Ave: uint16(payload[4])<<8 | uint16(payload[5]),
	}, nil
}

// NAKError is a refusal from the modem, carrying the firmware's reason code.
type NAKError struct {
	Command byte
	Reason  byte
}

func (e *NAKError) Error() string {
	return fmt.Sprintf("cal: the modem refused command 0x%02X: %s", e.Command, nakReason(e.Reason))
}

// nakReason translates the firmware's reason codes. They are shared between
// MMDVM_HS and MMDVM and are otherwise invisible to an operator, who would
// otherwise be told "reason 4" about a frame they never saw.
func nakReason(code byte) string {
	switch code {
	case 1:
		return "the modem is busy"
	case 2:
		// Reason 2 is conventionally "unimplemented", and translating it that
		// way is wrong in the case an operator is most likely to meet it. The
		// hotspot firmware initialises its per-frame error to 2 and only clears
		// it inside a branch that matches the modem's CURRENT STATE, so a
		// command it implements perfectly well — the transmit toggle, asked for
		// from a receive state — comes back as reason 2 with nothing missing.
		// Confirmed on the bench: MMDVM_HS SerialPort.cpp sets `uint8_t err =
		// 2U` and its CAL_DATA handler leaves it untouched when no calibration
		// state is active.
		return "the modem would not act on that command — either this firmware does not implement it, or the modem is not in a state where it means anything"
	case 3:
		return "the command is wrong for the modem's current state"
	case 4:
		return "a value in the command is out of range"
	case 5:
		return "the modem is in the wrong mode for this command"
	case 6:
		return "this firmware or board does not support that (for example duplex on a single-radio board)"
	}
	return fmt.Sprintf("reason %d", code)
}
