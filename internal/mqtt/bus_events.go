package mqtt

import (
	"encoding/json"
	"strings"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// bus_events.go is the D4 consumer mapping: a mode-bus daemon publishes its hub
// events verbatim as JSON under waypoint/bus/<id>/<type>, and this maps each
// message 1:1 back onto a hub.Event — there is no translation layer, because the
// payload IS the hub event shape. RFC-0004 persistence, RFC-0008 status, and the
// Prompt-12 dashboard badges then work with no further plumbing.

// TranslateBusEvent decodes one bus-event MQTT payload into a hub.Event. It
// returns ok=false for an EMPTY payload — a retained CLEAR (RFC-0008 no-latching:
// a "bus down" topic is emptied when the condition clears) — and for a payload
// that is not a well-formed bus event, so a malformed or foreign message is
// dropped rather than published.
func TranslateBusEvent(payload []byte) (hub.Event, bool) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return hub.Event{}, false // retained clear
	}
	var e hub.Event
	if err := json.Unmarshal(payload, &e); err != nil {
		return hub.Event{}, false
	}
	// Only bus/peer event types cross onto the hub — defence against a foreign
	// message landing under the bus prefix.
	if !strings.HasPrefix(e.Type, "bus_") && !strings.HasPrefix(e.Type, "peer_") {
		return hub.Event{}, false
	}
	return e, true
}
