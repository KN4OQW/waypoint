package config

import (
	"fmt"

	"github.com/KN4OQW/waypoint/internal/store"
)

// The character panel's half of the detected/configured split (#136).
//
// hardware.go builds this bridge for the modem and names #136 as the failure it
// exists to prevent: a panel found at first boot, never written anywhere the LCD
// config can see it, and an operator asked to declare hardware the daemon had
// already found. The same three pieces answer it here — a machine-written record
// of what the bus said, an operator-authoritative lcd section, and a named
// crossing between them.
//
// It is a SEPARATE store key from hardware_state rather than another field on it,
// because the two detections have nothing in common but the word "hardware". The
// modem probe is an expensive, operator-triggered act that can take the node off
// the air; the panel probe is a handful of one-byte reads that runs unattended at
// startup. Sharing a record would mean the startup probe read-modify-writing the
// same row an in-flight POST /api/hardware/detect is about to write back.
//
// What is NOT here is auto-enable. RFC-0001 makes the store operator-authoritative,
// and a daemon that switches on a display because it saw one is the same class of
// surprise as a probe that reconfigures a working modem. Detection records; the
// operator adopts.

// panelStateKey holds the last panel detection. Like hardwareStateKey it is
// deliberately absent from Model.sections(), so no PUT /api/config/panel_state
// exists — a record of what the bus answered is not a preference.
const panelStateKey = "panel_state"

// PanelFound is a panel that answered a probe. Mirrors hd44780.Found, kept here
// in its own type so the config package stays clear of device code and so the
// record has the snake_case shape the rest of the API serves.
type PanelFound struct {
	Bus  string `json:"bus"`  // e.g. /dev/i2c-1
	Addr string `json:"addr"` // e.g. 0x27
}

func (f PanelFound) String() string { return f.Bus + "@" + f.Addr }

// PanelState is the last panel detection run, stored verbatim.
type PanelState struct {
	// Found is the panel that answered, or nil if none did. A nil Found with a
	// non-empty CheckedAt is the useful "we looked and there was nothing" state,
	// which is what distinguishes a node with no display from one that has never
	// been asked.
	Found     *PanelFound `json:"found,omitempty"`
	CheckedAt string      `json:"checked_at,omitempty"`
	// Adopted records which detection the lcd section was last taken from, so the
	// UI can tell "we found this and you are running it" from "we found this and
	// your config points somewhere else".
	AdoptedAt    string `json:"adopted_at,omitempty"`
	AdoptedPanel string `json:"adopted_panel,omitempty"`
}

// GetPanelState reads the last panel detection (zero value if never run).
func GetPanelState(s *store.Store) (PanelState, error) {
	var st PanelState
	if _, err := s.GetInto(panelStateKey, &st); err != nil {
		return PanelState{}, err
	}
	return st, nil
}

// SetPanelState persists a panel detection run.
func SetPanelState(s *store.Store, st PanelState, by string) error {
	return s.Set(panelStateKey, &st, by)
}

// PanelAdoption is what crossing the gap actually changed, returned rather than
// logged for the same reason AdoptDetection returns one: the operator is shown
// which fields moved, so enabling a display is never something that happened to
// their config without being named.
type PanelAdoption struct {
	Changed []string `json:"changed"` // human-readable "field: old → new"
	Bus     string   `json:"bus,omitempty"`
	Addr    string   `json:"addr,omitempty"`
}

// AdoptPanel writes a detected panel into the lcd section: its bus, its address,
// and the enable that makes startLCD stop returning early.
//
// Enabling is part of adoption rather than a second step because there is nothing
// else adoption could mean here. The modem's adopt writes a port the node was
// going to use anyway; a panel that is configured but disabled is still a dark
// display, which is the exact symptom #136 reports. The operator's authority is
// preserved by this being an explicitly invoked operation, not by leaving them a
// switch to find afterwards.
//
// The page set is left alone. Pages are the operator's content, DefaultLCD seeds
// a usable set, and geometry is not something a PCF8574 address can tell us — a
// 16x2 panel adopted into a 20x4 config renders truncated, which is visible and
// fixable, where guessing at rows would silently rewrite pages the operator wrote.
func AdoptPanel(s *store.Store, f PanelFound, by string) (PanelAdoption, error) {
	if f.Bus == "" || f.Addr == "" {
		return PanelAdoption{}, fmt.Errorf("config: cannot adopt a panel with no bus or address")
	}
	var l LCD
	if _, err := s.GetInto("lcd", &l); err != nil {
		return PanelAdoption{}, err
	}
	a := PanelAdoption{Bus: f.Bus, Addr: f.Addr}
	if l.I2CBus != f.Bus {
		a.Changed = append(a.Changed, change("i2c_bus", l.I2CBus, f.Bus))
		l.I2CBus = f.Bus
	}
	if l.I2CAddress != f.Addr {
		a.Changed = append(a.Changed, change("i2c_address", l.I2CAddress, f.Addr))
		l.I2CAddress = f.Addr
	}
	if !l.Enabled {
		a.Changed = append(a.Changed, change("enabled", "false", "true"))
		l.Enabled = true
	}
	// The same rule every other write to this section passes (SetLCD). Adoption
	// changes no page and no geometry, so this can only fail on a config that was
	// already invalid — and turning the driver on is the moment that stops being
	// harmless, because the renderer is about to try to paint it.
	if err := ValidateLCD(l); err != nil {
		return PanelAdoption{}, err
	}
	if err := s.Set("lcd", &l, by); err != nil {
		return PanelAdoption{}, err
	}
	return a, nil
}
