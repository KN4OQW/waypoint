// Package hub is the in-process event bus: everything the daemon learns
// (modem events, network state, supervisor actions) flows through here, and
// every consumer — the SSE endpoint today, the WebSocket API and MQTT
// republisher later — is just a subscriber.
package hub

import (
	"sync"
	"time"
)

// Event is the wire shape for all Waypoint events. The schema matches what
// the MQTT bridge will emit from MMDVM-Host's JSON data plane, so UI code
// written against the demo feed keeps working against real hardware.
type Event struct {
	Time    time.Time `json:"time"`
	Type    string    `json:"type"`              // rf_voice_start, rf_voice_end, net_voice_start, net_voice_end, link, mode
	Mode    string    `json:"mode,omitempty"`    // DMR, YSF, ...
	Slot    int       `json:"slot,omitempty"`    // DMR slot, 0 otherwise
	Source  string    `json:"source,omitempty"`  // callsign or network name
	Dest    string    `json:"dest,omitempty"`    // talkgroup / reflector
	Network string    `json:"network,omitempty"` // BM_3102_United_States, ...
	Seconds float64   `json:"seconds,omitempty"` // duration, on *_end
	BER     float64   `json:"ber,omitempty"`     // percent, on rf_voice_end
	RSSI    int       `json:"rssi,omitempty"`    // dBm, on rf_voice_end
	Detail  string    `json:"detail,omitempty"`
}

const backlogSize = 200

// Hub fans events out to subscribers and keeps a bounded backlog so a
// freshly-opened dashboard can render recent history immediately.
type Hub struct {
	mu      sync.Mutex
	subs    map[chan Event]struct{}
	backlog []Event
}

func New() *Hub {
	return &Hub{subs: make(map[chan Event]struct{})}
}

// Publish delivers e to all current subscribers without blocking the
// publisher: a subscriber that has stopped draining is skipped.
func (h *Hub) Publish(e Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backlog = append(h.backlog, e)
	if len(h.backlog) > backlogSize {
		h.backlog = h.backlog[len(h.backlog)-backlogSize:]
	}
	for ch := range h.subs {
		select {
		case ch <- e:
		default:
		}
	}
}

// Subscribe returns a channel of future events plus a snapshot of the
// backlog. Call the returned cancel function to unsubscribe.
func (h *Hub) Subscribe() (ch chan Event, backlog []Event, cancel func()) {
	ch = make(chan Event, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	backlog = append([]Event(nil), h.backlog...)
	h.mu.Unlock()

	return ch, backlog, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}
