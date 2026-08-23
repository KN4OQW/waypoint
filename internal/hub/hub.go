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
	Type    string    `json:"type"`              // rf_voice_start, rf_voice_end, net_voice_start, net_voice_end, link_up, link_down, mode
	Mode    string    `json:"mode,omitempty"`    // DMR, YSF, ...
	Slot    int       `json:"slot,omitempty"`    // DMR slot, 0 otherwise
	Source  string    `json:"source,omitempty"`  // callsign or network name
	Dest    string    `json:"dest,omitempty"`    // talkgroup / reflector
	Network string    `json:"network,omitempty"` // BM_3102_United_States, ...
	Seconds float64   `json:"seconds,omitempty"` // duration, on *_end
	BER     float64   `json:"ber,omitempty"`     // percent, on rf_voice_end
	RSSI    int       `json:"rssi,omitempty"`    // dBm, on rf_voice_end
	Detail  string    `json:"detail,omitempty"`
	// State is the machine-readable link verdict ("up"/"down"/"unknown") that
	// accompanies Detail's prose on link_up/link_down. Empty on every other event
	// type, and empty from producers that have only a boolean to offer — the
	// status aggregator falls back to the boolean's meaning in that case.
	State string `json:"state,omitempty"`
	// SourceName is the operator's name for Source, from the phonebook, for the
	// AUTHENTICATED dashboard only (D4). Empty everywhere else, and empty always on
	// a node whose phonebook has no row for that station.
	//
	// Three things about it are deliberate.
	//
	// It is decorated at SERVE time, never at ingest: the handlers that answer
	// /api/history, /api/events and the WebSocket fill it in on their way out
	// (cmd/waypointd). An event therefore carries whatever the phonebook says
	// right now, so correcting a name fixes the history too, and a producer cannot
	// bake a stale one in.
	//
	// It is never persisted. internal/events writes an explicit column list and has
	// no column for this, so a field added to this struct cannot reach the database
	// by accident — which is what keeps the stored event identical to what it was
	// and the no-op guarantee (D5) structural rather than careful.
	//
	// It never reaches the public surface. internal/publicview builds its own Heard
	// struct with three fields and copies nothing from here; its resolver dependency
	// is an interface that cannot even return a name (D3).
	SourceName string `json:"source_name,omitempty"`
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

// Alive reports whether the hub can still be reached — that its lock is not held
// by something that has stopped, and its subscriber map is intact.
//
// It exists for the systemd watchdog. The interesting failure on an appliance is
// not the process exiting, which systemd already handles, but the process
// deadlocking: the listener still accepts, nothing is ever served, and Restart=
// never fires. Every event in the daemon passes through this mutex, so a
// goroutine that cannot take it is a daemon that has stopped working, whatever
// the process table says.
//
// The caller runs this on its own goroutine with a deadline; taking the lock is
// the test, and blocking forever is the failure it is testing for.
func (h *Hub) Alive() bool {
	done := make(chan struct{})
	go func() {
		h.mu.Lock()
		ok := h.subs != nil
		h.mu.Unlock()
		if ok {
			close(done)
		}
	}()
	select {
	case <-done:
		return true
	case <-time.After(aliveTimeout):
		return false
	}
}

// aliveTimeout bounds the liveness probe. It is generous relative to how long
// taking an uncontended mutex takes, because the cost of a false positive here is
// a restart of a working node.
const aliveTimeout = 5 * time.Second

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
