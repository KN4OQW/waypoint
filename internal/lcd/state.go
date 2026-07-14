// Package lcd is the Waypoint-native HD44780 driver's pure logic: it folds the
// live MQTT status plane (internal/hub events) into a small derived state,
// expands operator templates against it, and paints rotating pages to an
// LCDDevice. Everything here is hardware-free and deterministic — the only
// hardware seam is the LCDDevice interface (the real PCF8574/I2C device lands in
// a later stage). See docs/design/lcd.md.
package lcd

import (
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// state is the renderer's derived view of the event stream (design §3). Handle
// folds events in; the token engine reads it.
type state struct {
	activeMode string          // last mode announced / in progress ("" or "IDLE" → idle)
	active     bool            // a voice transmission is in progress
	actDir     string          // "RX" (RF) or "TX" (network) while active
	actMode    string          // mode of the in-progress call
	actCall    string          // caller callsign of the in-progress call
	actTG      string          // talkgroup/reflector of the in-progress call
	lastHeard  *heard          // most recent completed transmission (nil until the first)
	links      map[string]bool // network → linked (reserved; no v1 token reads it yet)
}

// heard is one completed transmission, captured at key-up.
type heard struct {
	call, tg, mode string
	ber            float64
	rssi           int
	at             time.Time
}

// callTG normalizes an event's caller callsign and talkgroup. RF events carry
// Source=callsign, Dest=talkgroup; network events swap them (internal/demo), so
// {lh_call}/{status} always resolve to a callsign regardless of direction. This
// is the one place that convention lives, so the real MQTT consumer can adjust it
// without touching the token engine.
func callTG(e hub.Event) (call, tg string) {
	if strings.HasPrefix(e.Type, "net_") {
		return e.Dest, e.Source
	}
	return e.Source, e.Dest
}

// dir reports the transmission direction for {status}: RF is received ("RX"), a
// network transmission is keyed out to RF ("TX").
func dir(e hub.Event) string {
	if strings.HasPrefix(e.Type, "net_") {
		return "TX"
	}
	return "RX"
}

// handle folds one event into the derived state (design §3).
func (s *state) handle(e hub.Event) {
	switch e.Type {
	case "mode":
		s.activeMode = e.Mode
	case "link":
		if e.Network != "" {
			if s.links == nil {
				s.links = map[string]bool{}
			}
			s.links[e.Network] = true
		}
	case "rf_voice_start", "net_voice_start":
		call, tg := callTG(e)
		s.active = true
		s.actDir = dir(e)
		s.actMode = e.Mode
		s.activeMode = e.Mode
		s.actCall, s.actTG = call, tg
	case "rf_voice_end", "net_voice_end":
		call, tg := callTG(e)
		s.active = false
		s.lastHeard = &heard{call: call, tg: tg, mode: e.Mode, ber: e.BER, rssi: e.RSSI, at: e.Time}
	}
}
