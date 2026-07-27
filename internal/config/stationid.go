package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// DefaultStationIDTimeMins is the out-of-the-box CW identification interval, in
// minutes. Ten is stock MMDVM-Host.ini, stock Pi-Star and stock WPSD, and it is
// also the longest interval US §97.119 permits — so it is simultaneously the
// parity default and the conservative one. Jurisdictions that require a shorter
// interval are the business of the compliance presets (RFC-0019), not this
// constant.
const DefaultStationIDTimeMins = 10

// MaxStationIDTimeMins bounds the interval at an hour. This is a
// nonsense-rejection bound, not a legal one: no regulator anywhere permits a
// longer gap, so a larger value is far more likely a typo (600 for 60) than an
// intent. The lower bound is 1 — the host's timer is minute-granular.
const MaxStationIDTimeMins = 60

// DefaultStationID is the identification policy a fresh store seeds and a store
// created before this section existed backfills to.
//
// Enable is TRUE. That is a deliberate change of Waypoint's effective default:
// with no [CW Id] section rendered, MMDVM-Host's own m_cwIdEnabled(false) applied
// and nodes did not identify at all. Every incumbent this project migrates from
// ships identification on, so "on" is both the parity-correct default and the one
// that does not quietly put an operator out of compliance. An operator who does
// not want it turns it off explicitly.
//
// Callsign is blank, meaning "track [General] Callsign" (see StationID).
func DefaultStationID() StationID {
	return StationID{
		Enable:   true,
		TimeMins: strconv.Itoa(DefaultStationIDTimeMins),
		Callsign: "",
		TXLevel:  "50",
	}
}

// EffectiveIDCallsign is the callsign that will actually be keyed in Morse: the
// StationID override when set, otherwise the station callsign MMDVM-Host falls
// back to. It resolves the same rule the renderer implements by omitting the
// [CW Id] Callsign key, so the UI and the generated INI cannot disagree.
func (m *Model) EffectiveIDCallsign() string {
	if c := strings.TrimSpace(m.StationID.Callsign); c != "" {
		return c
	}
	return m.General.Callsign
}

// ValidateStationID enforces the rules the type cannot. The interval is checked
// only when identification is enabled: a disabled section carries whatever
// interval the operator last had, so toggling identification off and back on does
// not silently reset it, and a blank interval on a disabled section is not an
// error.
func ValidateStationID(s StationID) error {
	if !s.Enable {
		return nil
	}
	if s.TimeMins == "" {
		return fmt.Errorf("station ID interval is required when identification is enabled")
	}
	n, err := strconv.Atoi(s.TimeMins)
	if err != nil {
		return fmt.Errorf("station ID interval must be a whole number of minutes, got %q", s.TimeMins)
	}
	if n < 1 || n > MaxStationIDTimeMins {
		return fmt.Errorf("station ID interval must be between 1 and %d minutes, got %d", MaxStationIDTimeMins, n)
	}
	return nil
}

// SetStationID merges a partial JSON body into the station-ID section and writes
// it back, exactly like SetSection (unknown fields rejected, unspecified fields
// preserved), but validates the merged policy before committing. Routed here
// instead of through the generic SetSection so the interval rule is enforced on
// every API write — the same treatment History and Update get.
func SetStationID(s *store.Store, raw []byte, by string) error {
	var sid StationID
	if _, err := s.GetInto("station_id", &sid); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&sid); err != nil {
		return err
	}
	if err := ValidateStationID(sid); err != nil {
		return err
	}
	return s.Set("station_id", &sid, by)
}
