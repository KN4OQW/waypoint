package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/modem"
	"github.com/KN4OQW/waypoint/internal/store"
)

// Detected hardware and configured hardware are two different things, and issue
// #136 is what happens when a project forgets that.
//
// There, a detected HD44780 panel is found at setup and then never reaches the
// LCD config, so the operator sees a panel Waypoint knows about and a
// configuration that does not mention it. The failure is not that detection and
// configuration were separated — they should be — it is that nothing ever
// crossed the gap.
//
// So #18 keeps the split and builds the bridge explicitly:
//
//   - hardware_state is what the MODEM said. Machine-written, never edited,
//     outside the section map so an operator PUT cannot touch it (the same shape
//     update_state uses, and for the same reason).
//   - modem.Board / modem.TCXOHz / modem.Port are what the OPERATOR says. Edited
//     like any other config, and never overwritten behind their back.
//   - Adopt is the crossing, and it is a named operation with a return value
//     saying what it changed — not a side effect of probing.

// hardwareStateKey holds the last detection result. Deliberately not a member of
// Model.sections(): PUT /api/config/hardware_state must not exist, because a
// record of what the hardware said is not a preference.
const hardwareStateKey = "hardware_state"

// HardwareState is the last detection run, stored verbatim.
type HardwareState struct {
	// Identity is the modem that answered, or nil if none did. A nil Identity
	// with a non-empty Scanned is a real, useful state: "we looked, here is
	// where, nothing was there".
	Identity *modem.Identity `json:"identity,omitempty"`
	Scanned  []modem.Scanned `json:"scanned,omitempty"`
	// Bootloader names a port holding a board stuck in DFU, which cannot answer
	// but is one firmware flash (#19) from being able to.
	Bootloader string `json:"bootloader,omitempty"`
	CheckedAt  string `json:"checked_at,omitempty"`
	// Adopted records which detection the config was last taken from, so the UI
	// can tell "we detected this and you are running it" from "we detected this
	// and you are running something else".
	AdoptedAt          string `json:"adopted_at,omitempty"`
	AdoptedDescription string `json:"adopted_description,omitempty"`
}

// GetHardwareState reads the last detection (zero value if never run).
func GetHardwareState(s *store.Store) (HardwareState, error) {
	var st HardwareState
	if _, err := s.GetInto(hardwareStateKey, &st); err != nil {
		return HardwareState{}, err
	}
	return st, nil
}

// SetHardwareState persists a detection run.
func SetHardwareState(s *store.Store, st HardwareState, by string) error {
	return s.Set(hardwareStateKey, &st, by)
}

// Adoption is what crossing the gap actually changed. It is returned rather than
// logged because the UI shows it back to the operator — "port /dev/ttyAMA0,
// board MMDVM_HS_Dual_Hat, 14.7456 MHz" — and a silent adopt is how an operator
// ends up not knowing which of two boards their node is configured for.
type Adoption struct {
	Changed []string `json:"changed"` // human-readable "field: old → new"
	Board   string   `json:"board,omitempty"`
	Port    string   `json:"port,omitempty"`
	TCXOHz  string   `json:"tcxo_hz,omitempty"`
}

// ErrAmbiguousBoard is returned when detection narrowed the board to several
// candidates and the caller named none of them. It is not a failure — it is the
// picker's cue, and issue #18's "explicit picker where not [possible]".
var ErrAmbiguousBoard = fmt.Errorf("config: the modem's identity matches several boards; pick one")

// AdoptDetection writes a detection into the modem section.
//
// boardID may be empty when detection was unambiguous, and must otherwise name
// one of the detected candidates — passing a board the modem's own identity
// string rules out is refused, because the point of the picker is to resolve an
// ambiguity, not to overrule the hardware.
//
// The oscillator is written only when it was REPORTED. An assumed value is
// deliberately left out of the config: a reference frequency guessed wrong
// detunes the radio, and a value in the config reads as a fact.
func AdoptDetection(s *store.Store, id modem.Identity, boardID, by string) (Adoption, error) {
	if boardID == "" {
		boardID = id.BoardID
	}
	if boardID == "" {
		if len(id.Candidates) > 1 {
			return Adoption{}, ErrAmbiguousBoard
		}
		// No candidates at all: an unrecognised but working modem. Its port is
		// still worth adopting; the board stays unnamed rather than invented.
	} else if _, ok := modem.BoardByID(boardID); !ok {
		return Adoption{}, fmt.Errorf("config: unknown board %q", boardID)
	} else if len(id.Candidates) > 0 && !containsString(id.Candidates, boardID) {
		return Adoption{}, fmt.Errorf("config: board %q does not match the modem's identity (%s)", boardID, id.Description)
	}

	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		return Adoption{}, err
	}
	a := Adoption{Board: boardID, Port: id.Port}

	if id.Port != "" && m.Port != id.Port {
		a.Changed = append(a.Changed, change("port", m.Port, id.Port))
		m.Port = id.Port
	}
	if id.Baud > 0 {
		speed := strconv.Itoa(id.Baud)
		if m.UARTSpeed != speed {
			a.Changed = append(a.Changed, change("uart_speed", m.UARTSpeed, speed))
			m.UARTSpeed = speed
		}
	}
	if boardID != "" && m.Board != boardID {
		a.Changed = append(a.Changed, change("board", m.Board, boardID))
		m.Board = boardID
	}
	if id.TCXOHz > 0 && !id.TCXOAssumed {
		hz := strconv.Itoa(id.TCXOHz)
		a.TCXOHz = hz
		if m.TCXOHz != hz {
			a.Changed = append(a.Changed, change("tcxo_hz", m.TCXOHz, hz))
			m.TCXOHz = hz
		}
	}
	if err := s.Set("modem", &m, by); err != nil {
		return Adoption{}, err
	}
	return a, nil
}

func change(field, from, to string) string {
	if from == "" {
		from = "(unset)"
	}
	return field + ": " + from + " → " + to
}

// ValidateModem rejects a board ID that is not in the table. Everything else
// about the hardware is a warning rather than a refusal — see HardwareWarnings —
// because an operator whose board Waypoint mis-identifies must still be able to
// save a working configuration.
func ValidateModem(m Modem) error {
	if m.Board != "" {
		if _, ok := modem.BoardByID(m.Board); !ok {
			return fmt.Errorf("modem board %q is not a board Waypoint knows", m.Board)
		}
	}
	if m.TCXOHz != "" {
		if _, err := strconv.Atoi(m.TCXOHz); err != nil {
			return fmt.Errorf("modem tcxo_hz must be a whole number of Hz, got %q", m.TCXOHz)
		}
	}

	// The calibration fields (#20). These are refused rather than warned about,
	// because every one of them reaches the modem as a number that changes what
	// the radio does: a level outside 0-100 or a DC offset outside a signed byte
	// is NAKed by the firmware, and a node whose config the modem rejects comes
	// up mute with nothing on screen explaining why.
	for _, lvl := range []struct{ field, val string }{
		{"rx_level", m.RXLevel}, {"tx_level", m.TXLevel}, {"rf_level", m.RFLevel},
		{"dstar_tx_level", m.DStarTXLevel}, {"dmr_tx_level", m.DMRTXLevel},
		{"ysf_tx_level", m.YSFTXLevel}, {"p25_tx_level", m.P25TXLevel},
		{"nxdn_tx_level", m.NXDNTXLevel}, {"pocsag_tx_level", m.POCSAGTXLevel},
		{"fm_tx_level", m.FMTXLevel},
	} {
		if err := validPercent("modem."+lvl.field, lvl.val); err != nil {
			return err
		}
	}
	for _, off := range []struct{ field, val string }{
		{"rx_dc_offset", m.RXDCOffset}, {"tx_dc_offset", m.TXDCOffset},
	} {
		if off.val == "" {
			continue
		}
		n, err := strconv.Atoi(off.val)
		if err != nil {
			return fmt.Errorf("modem %s must be a whole number, got %q", off.field, off.val)
		}
		if n < -128 || n > 127 {
			return fmt.Errorf("modem %s must be between -128 and 127, got %d", off.field, n)
		}
	}
	if m.DMRDelay != "" {
		n, err := strconv.Atoi(m.DMRDelay)
		if err != nil || n < 0 {
			return fmt.Errorf("modem dmr_delay must be a whole number of bits, got %q", m.DMRDelay)
		}
	}
	if f := strings.TrimSpace(m.RSSIMappingFile); f != "" && !strings.HasPrefix(f, "/") {
		return fmt.Errorf("modem rssi_mapping_file must be an absolute path, got %q", f)
	}
	return nil
}

// validPercent accepts a blank value or a percentage the modem can carry. Blank
// is always allowed: for the per-mode levels it means "follow tx_level", which
// is the setting almost every node should have.
func validPercent(field, v string) error {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return fmt.Errorf("%s must be a percentage, got %q", field, v)
	}
	if f < 0 || f > 100 {
		return fmt.Errorf("%s must be between 0 and 100, got %s", field, v)
	}
	return nil
}

// SetModem merges a partial JSON body into the modem section and writes it back
// (unknown fields rejected, unspecified fields preserved), validating the merged
// result so an unknown board or a non-numeric oscillator is refused at save time
// rather than discovered by a renderer.
func SetModem(s *store.Store, raw []byte, by string) error {
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return err
	}
	if err := ValidateModem(m); err != nil {
		return err
	}
	return s.Set("modem", &m, by)
}

// Severity ranks a finding, from "this is broken" down to "this is worth knowing".
//
//   - Error: the configuration cannot work. Something will not come up, or will
//     come up and carry nothing.
//   - Warning: it probably is not what the operator meant. The node works; the
//     result is likely to surprise them.
//   - Info: the configuration is fine and there is something about it worth
//     knowing anyway. This tier exists for guidance that is HEURISTIC — where the
//     mechanism is real but whether it applies to this node cannot be determined
//     from the configuration alone. The other two tiers are reserved for facts,
//     and a guess wearing a warning's colours is how an operator learns to ignore
//     warnings.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
	SeverityInfo    = "info"
)

// HardwareWarning is one disagreement between the configuration and what the
// modem said about itself.
type HardwareWarning struct {
	Field    string `json:"field"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// HardwareWarnings compares the configuration against the last detection.
//
// This is where board identity earns its keep. Nothing here changes a rendered
// INI — MMDVM-Host is told a port and a speed and nothing else — but every one
// of these checks catches a configuration that produces a node which comes up,
// reports itself healthy, and does not work on the air:
//
//   - Duplex on a board with one ADF7021: the host starts, and never keys.
//   - A mode the firmware does not carry: the host starts, the mode is silently
//     absent, and the operator debugs their reflector.
//   - A port that is not where the modem actually is: the host exits, and the
//     update health gate reads it as a bad update.
//
// It returns nothing at all when no detection has run. Warning about a
// configuration on the strength of no evidence is worse than silence.
func HardwareWarnings(m *Model, st HardwareState) []HardwareWarning {
	if m == nil || st.Identity == nil {
		return nil
	}
	id := st.Identity
	var out []HardwareWarning
	add := func(field, sev, msg string) {
		out = append(out, HardwareWarning{Field: field, Severity: sev, Message: msg})
	}

	if m.General.Duplex && !id.Duplex {
		add("general.duplex", SeverityError, fmt.Sprintf(
			"Duplex is on, but %s reports a single ADF7021. A simplex board cannot transmit and receive at once; the host will start and never key.",
			describeBoard(id)))
	}

	// Capability bits are only meaningful on protocol-2 firmware. On protocol 1
	// they are MMDVM-Host's assumption, not the modem's answer, and refusing a
	// mode on the strength of a guess is exactly what modem.Modes.Known exists
	// to prevent.
	if id.Modes.Known {
		for _, mode := range []struct {
			key, label string
			on         bool
		}{
			{"dstar", "D-Star", m.Modes.DStar},
			{"dmr", "DMR", m.Modes.DMR},
			{"ysf", "System Fusion", m.Modes.YSF},
			{"p25", "P25", m.Modes.P25},
			{"nxdn", "NXDN", m.Modes.NXDN},
			{"m17", "M17", m.Modes.M17},
			{"pocsag", "POCSAG", m.Modes.POCSAG},
			{"fm", "FM", m.Modes.FM},
		} {
			if mode.on && !id.Modes.Supports(mode.key) {
				add("modes."+mode.key, SeverityError, fmt.Sprintf(
					"%s is enabled, but the firmware on this modem (%s) does not carry it. The mode will be silently absent on the air.",
					mode.label, firmwareLabel(id)))
			}
		}
	}

	if m.Modem.Port != "" && id.Port != "" && m.Modem.Port != id.Port {
		add("modem.port", SeverityError, fmt.Sprintf(
			"The modem answered on %s, but this node is configured for %s. MMDVM-Host exits when its port has no modem.",
			id.Port, m.Modem.Port))
	}

	if m.Modem.TCXOHz != "" && id.TCXOHz > 0 && !id.TCXOAssumed && m.Modem.TCXOHz != strconv.Itoa(id.TCXOHz) {
		add("modem.tcxo_hz", SeverityWarning, fmt.Sprintf(
			"This node is configured for a %s reference, but the modem reports %s.",
			hzLabel(m.Modem.TCXOHz), modem.TCXOLabel(id.TCXOHz)))
	}

	if m.Modem.Board != "" && len(id.Candidates) > 0 && !containsString(id.Candidates, m.Modem.Board) {
		name := m.Modem.Board
		if b, ok := modem.BoardByID(m.Modem.Board); ok {
			name = b.Name
		}
		add("modem.board", SeverityWarning, fmt.Sprintf(
			"This node is configured as a %s, but the attached modem identifies as %q, which no %s does.",
			name, id.Description, name))
	}
	return out
}

func describeBoard(id *modem.Identity) string {
	if id.HWType != "" {
		return id.HWType
	}
	return "the attached modem"
}

func firmwareLabel(id *modem.Identity) string {
	if id.Description != "" {
		return id.Description
	}
	return "unknown firmware"
}

func hzLabel(hz string) string {
	n, err := strconv.Atoi(hz)
	if err != nil {
		return hz + " Hz"
	}
	if l := modem.TCXOLabel(n); l != "" {
		return l
	}
	return hz + " Hz"
}

func containsString(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
