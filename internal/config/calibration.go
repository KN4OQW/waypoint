package config

import (
	"fmt"
	"strconv"

	"github.com/KN4OQW/waypoint/internal/cal"
	"github.com/KN4OQW/waypoint/internal/store"
)

// Calibration is a measurement, and measurements need provenance.
//
// The same split RFC-0020 draws between what the modem SAID and what the
// operator CONFIGURED applies here, for the same reason:
//
//   - calibration_state is the sweep that ran — when, against which board and
//     firmware, the whole curve, and how many frames each point was scored on.
//     Machine-written, outside Model.sections() so no operator PUT can assert a
//     measurement that never happened.
//   - modem.rx_offset / tx_offset are the CONFIGURATION. Written through the
//     ordinary config path with by: "calibration", so the change appears in
//     history like any other edit and can be corrected by hand afterwards.
//
// Keeping them apart is what makes "was this node ever calibrated, against
// what, and how well" answerable months later — and what lets an operator
// override an offset without Waypoint quietly claiming it measured one.

const calibrationStateKey = "calibration_state"

// CalibrationState is the last sweep, stored verbatim.
type CalibrationState struct {
	// Result is the curve itself, exactly as the engine produced it.
	Result *cal.Result `json:"result,omitempty"`
	RanAt  string      `json:"ran_at,omitempty"`
	// Mode names what the sweep listened to. Only DMR today (RFC-0021 §3), but
	// recorded rather than assumed, because a curve measured in a mode with
	// automatic frequency correction would mean something quite different.
	Mode string `json:"mode,omitempty"`

	// The hardware the measurement belongs to. An offset is a property of one
	// board's oscillator: swap the board and the number is worthless, and these
	// are what let the UI notice.
	BoardID  string `json:"board_id,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	UDID     string `json:"udid,omitempty"`

	// What was done with it, if anything. A sweep the operator did not believe
	// is still worth keeping — it is evidence about the board.
	AppliedOffsetHz *int   `json:"applied_offset_hz,omitempty"`
	AppliedAt       string `json:"applied_at,omitempty"`
	// Error records a sweep that ended without a usable answer, in the words the
	// operator was shown, so the panel can still explain itself after a reload.
	Error string `json:"error,omitempty"`
}

// GetCalibrationState reads the last sweep (zero value if none has run).
func GetCalibrationState(s *store.Store) (CalibrationState, error) {
	var st CalibrationState
	if _, err := s.GetInto(calibrationStateKey, &st); err != nil {
		return CalibrationState{}, err
	}
	return st, nil
}

// SetCalibrationState persists a sweep.
func SetCalibrationState(s *store.Store, st CalibrationState, by string) error {
	return s.Set(calibrationStateKey, &st, by)
}

// ApplyOffset writes a measured offset into the modem configuration.
//
// Both offsets are written from the one measurement, and that is a decision
// worth stating rather than hiding: a hotspot has ONE reference oscillator
// clocking both paths, so an error measured on receive is the same error on
// transmit. The sweep can only measure the receive side without test equipment;
// applying it to both is what makes the node work in both directions.
//
// It returns what it changed so the UI can show it back, in the same shape
// AdoptDetection uses — a silent write of a number that detunes a radio is
// exactly the kind of thing this codebase keeps refusing to do.
func ApplyOffset(s *store.Store, offsetHz int, by string) (Adoption, error) {
	var m Modem
	if _, err := s.GetInto("modem", &m); err != nil {
		return Adoption{}, err
	}
	if m.RXFreqHz == "" || m.TXFreqHz == "" {
		return Adoption{}, fmt.Errorf("config: this node has no frequencies configured, so an offset would have nothing to correct")
	}
	v := strconv.Itoa(offsetHz)

	var a Adoption
	if m.RXOffset != v {
		a.Changed = append(a.Changed, change("rx_offset", m.RXOffset, v))
		m.RXOffset = v
	}
	if m.TXOffset != v {
		a.Changed = append(a.Changed, change("tx_offset", m.TXOffset, v))
		m.TXOffset = v
	}
	if err := ValidateModem(m); err != nil {
		return Adoption{}, err
	}
	if err := s.Set("modem", &m, by); err != nil {
		return Adoption{}, err
	}
	return a, nil
}
