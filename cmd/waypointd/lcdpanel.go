package main

import (
	"log"
	"net/http"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
)

// The panel's detect/adopt surface (#136).
//
// Two routes, matching the two things an operator does with a display they have
// wired but never declared:
//
//	POST /api/lcd/detect → sweep the I2C buses now and record what answered
//	POST /api/lcd/adopt  → write that record into the lcd section and light it up
//
// Both feed GET /api/hardware, which already carries "what we detected" for the
// modem and is what the settings UI loads on every visit — so the LCD tab shows
// the offer without a fetch of its own.
//
// Detection also runs unattended: once at startup for a node whose driver is off,
// and inside runSetupPanel, which was already probing at first boot and throwing
// the answer away. That last one is the actual bug in #136 — the panel WAS found,
// on the one boot where finding it mattered most, and nothing kept the result.

// detectPanel is the bus sweep, indirected so tests can drive the routes without
// an I2C bus. hd44780.Detect skips buses that do not exist, which is the common
// case, so the real one costs nothing on a node with no display wired.
var detectPanel = func() (config.PanelFound, error) {
	f, err := hd44780.Detect(nil)
	if err != nil {
		return config.PanelFound{}, err
	}
	return config.PanelFound{Bus: f.Bus, Addr: f.Addr}, nil
}

// recordPanel stores a detection run. found is nil when nothing answered, which
// is recorded rather than discarded for the same reason the modem's empty scan is:
// "we looked and there was nothing" is the state that explains a dark panel, and
// it is not the same state as never having looked.
//
// Best-effort by design — a store that will not take the record must not stop a
// daemon from starting or a probe from reporting.
func (s *server) recordPanel(found *config.PanelFound) {
	if s.store == nil {
		return // no store to remember it in (a probe running before one is open)
	}
	st, err := config.GetPanelState(s.store)
	if err != nil {
		log.Printf("lcd: reading the panel detection record failed: %v", err)
		return
	}
	st.Found = found
	st.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err := config.SetPanelState(s.store, st, "waypointd"); err != nil {
		log.Printf("lcd: recording the panel detection failed: %v", err)
	}
}

// probeForPanel looks for an undeclared panel at startup and records what it finds.
//
// It runs only when the driver is off, because that is the only state where the
// answer changes anything: a configured panel is already open, and probing the bus
// it is being painted on would be asking a question nobody is waiting on. It also
// stands down before the node is provisioned, where runSetupPanel owns the display
// and records its own find.
//
// Nothing here writes the lcd section. The record is an offer; adopting it is the
// operator's (RFC-0001).
func (s *server) probeForPanel(m *config.Model) {
	if m.LCD.Enabled {
		return
	}
	if s.wiz != nil && !s.wiz.Provisioned() {
		return
	}
	found, err := detectPanel()
	if err != nil {
		s.recordPanel(nil)
		return
	}
	s.recordPanel(&found)
	log.Printf("lcd: a panel answered on %s and the driver is off — the LCD settings tab can enable it", found)
}

// lcdDetect serves POST /api/lcd/detect.
//
// Unlike the modem's detect this takes no lock and stops nothing. The sweep is a
// handful of one-byte reads on an I2C bus that no other Waypoint subsystem uses,
// so it cannot collide with a firmware flash or take the node off the air — the
// arbitration that guards /api/hardware/detect is guarding a resource this does
// not touch.
func (s *server) lcdDetect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	found, err := detectPanel()
	if err != nil {
		s.recordPanel(nil)
	} else {
		s.recordPanel(&found)
	}
	v, err := s.hardwareSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, v)
}

// lcdAdopt serves POST /api/lcd/adopt: write the detected panel into the lcd
// section and start the renderer on it.
//
// The restart is what makes this one click rather than two. Everywhere else the
// lcd section is changed through the config apply path, which reloads the renderer
// on the way out; adopting writes the section directly, so it has to do the same
// or the operator would enable a display that stays dark until the next restart —
// which is the symptom they came here to fix.
func (s *server) lcdAdopt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	st, err := config.GetPanelState(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if st.Found == nil {
		http.Error(w, "no panel has been detected on this node yet", http.StatusConflict)
		return
	}
	adoption, err := config.AdoptPanel(s.store, *st.Found, "api")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	st.AdoptedAt = time.Now().UTC().Format(time.RFC3339)
	st.AdoptedPanel = st.Found.String()
	if err := config.SetPanelState(s.store, st, "api"); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if m, err := config.Load(s.store); err != nil {
		log.Printf("lcd: adopted %s but the config would not reload, so the renderer was not started: %v", st.Found, err)
	} else {
		s.reloadLCD(m)
	}

	v, err := s.hardwareSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"adopted": adoption, "hardware": v})
}
