// Package status folds the Waypoint event stream (RFC-0004 hub — fed only by the
// MQTT data plane and the supervisor liveness probe, never by log scraping) into
// a single authoritative, self-healing live-status value (RFC-0008). It is the
// server-side truth served by GET /api/status, streamed over the WebSocket, and
// republished onto waypoint/status/# — so the dashboard and Home Assistant are
// consumers of one computed status, not each re-deriving it from raw events.
package status

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// Status is the node's authoritative live state. Everything here is derived from
// structured events; nothing is a secret, so the whole value is safe to serve and
// publish.
type Status struct {
	Mode      string          `json:"mode"`     // active mode, or "IDLE"
	TX        *Transmission   `json:"tx"`       // the current keyed-up transmission, or null when idle
	Networks  map[string]Link `json:"networks"` // per network/reflector link state (from MQTT link events)
	Gateways  map[string]Link `json:"gateways"` // per gateway-daemon liveness (from the supervisor probe)
	Feed      Feed            `json:"feed"`     // the MMDVM-Host MQTT feed itself
	UpdatedAt time.Time       `json:"updated_at"`
}

// Transmission is the transmission currently on the air.
type Transmission struct {
	Mode      string    `json:"mode"`
	Slot      int       `json:"slot,omitempty"`
	Source    string    `json:"source"`
	Dest      string    `json:"dest,omitempty"`
	Network   string    `json:"network,omitempty"`
	Direction string    `json:"direction"` // "rf" | "network"
	StartedAt time.Time `json:"started_at"`
	expiresAt time.Time // watchdog deadline — not serialized
}

// Link is the up/down state of a network reflector or a gateway daemon.
type Link struct {
	Up     bool      `json:"up"`
	Detail string    `json:"detail,omitempty"`
	Since  time.Time `json:"since"`
}

// Feed is the health of the MMDVM-Host MQTT feed that everything else derives from.
type Feed struct {
	Connected bool      `json:"connected"`
	Detail    string    `json:"detail,omitempty"`
	Since     time.Time `json:"since"`
}

// Event-type constants the fold understands. The voice/mode/link types are what
// the MQTT bridge already emits; the feed_* and gateway_* types are emitted by the
// consumer and the supervisor liveness probe (RFC-0008). All are ordinary hub
// events, so they persist and stream unchanged.
const (
	TypeRFStart  = "rf_voice_start"
	TypeRFEnd    = "rf_voice_end"
	TypeNetStart = "net_voice_start"
	TypeNetEnd   = "net_voice_end"
	TypeMode     = "mode"
	TypeLink     = "link"
	TypeFeedUp   = "feed_up"
	TypeFeedDown = "feed_down"
	TypeGWUp     = "gateway_up"
	TypeGWDown   = "gateway_down"
)

// DefaultTxTTL is the stranded-transmission watchdog: a transmission not ended
// within this window self-clears to idle (RFC-0008). It is the modem transmit
// timeout ceiling plus a margin — a real transmission cannot outlive the modem's
// own timeout, so a TX still "on the air" past this deadline is stranded (its
// daemon died without a closing event) and truth is that the node is idle.
const DefaultTxTTL = 200 * time.Second

// Aggregator holds the single authoritative Status and notifies listeners on every
// change. The mutable copy lives behind mu; readers get a deep value copy, so the
// API/WS/republisher never race the fold.
type Aggregator struct {
	mu     sync.Mutex
	status Status
	txTTL  time.Duration
	now    func() time.Time // injectable for tests

	lmu       sync.Mutex
	listeners map[int]func(Status)
	nextID    int
}

// New returns an idle aggregator with the given stranded-TX watchdog window.
func New(txTTL time.Duration) *Aggregator {
	return &Aggregator{
		status: Status{Mode: "IDLE", Networks: map[string]Link{}, Gateways: map[string]Link{}},
		txTTL:  txTTL,
		now:    time.Now,
	}
}

// Snapshot returns a deep copy of the current status.
func (a *Aggregator) Snapshot() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return cloneStatus(a.status)
}

// OnChange registers a listener invoked (with a value copy) on every status
// change. The returned function unregisters it.
func (a *Aggregator) OnChange(fn func(Status)) func() {
	a.lmu.Lock()
	if a.listeners == nil {
		a.listeners = map[int]func(Status){}
	}
	id := a.nextID
	a.nextID++
	a.listeners[id] = fn
	a.lmu.Unlock()
	return func() {
		a.lmu.Lock()
		delete(a.listeners, id)
		a.lmu.Unlock()
	}
}

// Apply folds one event into the status, emitting on change.
func (a *Aggregator) Apply(e hub.Event) {
	a.commit(func(s Status) Status { return applyEvent(s, e, a.txTTL) })
}

// Expire runs the stranded-TX watchdog against now, emitting on change. Called on
// a ticker by Run, and directly (with an injected clock) by tests.
func (a *Aggregator) Expire(now time.Time) {
	a.commit(func(s Status) Status { return expire(s, now) })
}

func (a *Aggregator) commit(f func(Status) Status) {
	a.mu.Lock()
	old := a.status
	next := f(cloneStatus(old))
	changed := !statusEqual(old, next)
	if changed {
		next.UpdatedAt = a.now()
		a.status = next
	}
	snap := cloneStatus(a.status)
	a.mu.Unlock()
	if changed {
		a.notify(snap)
	}
}

func (a *Aggregator) notify(s Status) {
	a.lmu.Lock()
	fns := make([]func(Status), 0, len(a.listeners))
	for _, fn := range a.listeners {
		fns = append(fns, fn)
	}
	a.lmu.Unlock()
	for _, fn := range fns {
		fn(s)
	}
}

// Run subscribes to the hub, folds the backlog then the live stream, and runs the
// watchdog ticker until ctx is canceled.
func (a *Aggregator) Run(ctx context.Context, h *hub.Hub, tick time.Duration) {
	ch, backlog, cancel := h.Subscribe()
	defer cancel()
	for _, e := range backlog {
		a.Apply(e)
	}
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-ch:
			a.Apply(e)
		case now := <-t.C:
			a.Expire(now)
		}
	}
}

// applyEvent is the pure fold: (status, event) -> status. It never mutates the
// input's maps (it clones a map before writing), so the caller can compare old vs
// new to detect a change.
func applyEvent(s Status, e hub.Event, txTTL time.Duration) Status {
	switch e.Type {
	case TypeRFStart, TypeNetStart:
		dir := "rf"
		if e.Type == TypeNetStart {
			dir = "network"
		}
		s.Mode = e.Mode
		s.TX = &Transmission{
			Mode: e.Mode, Slot: e.Slot, Source: e.Source, Dest: e.Dest, Network: e.Network,
			Direction: dir, StartedAt: e.Time, expiresAt: e.Time.Add(txTTL),
		}
	case TypeRFEnd, TypeNetEnd:
		s.TX = nil
		s.Mode = "IDLE"
	case TypeMode:
		if e.Mode != "" {
			s.Mode = e.Mode
		}
	case TypeLink:
		if e.Network != "" {
			s.Networks = cloneLinks(s.Networks)
			s.Networks[e.Network] = Link{Up: true, Detail: e.Detail, Since: e.Time}
		}
	case TypeGWUp, TypeGWDown:
		name := firstNonEmpty(e.Network, e.Mode)
		if name != "" {
			s.Gateways = cloneLinks(s.Gateways)
			s.Gateways[name] = Link{Up: e.Type == TypeGWUp, Detail: e.Detail, Since: e.Time}
		}
	case TypeFeedUp, TypeFeedDown:
		s.Feed = Feed{Connected: e.Type == TypeFeedUp, Detail: e.Detail, Since: e.Time}
	}
	return s
}

// expire clears a transmission whose watchdog deadline has passed — the
// self-heal that makes a stranded TX (daemon died mid-transmission, no closing
// event) return to idle instead of counting forever (RFC-0008).
func expire(s Status, now time.Time) Status {
	if s.TX != nil && !s.TX.expiresAt.IsZero() && now.After(s.TX.expiresAt) {
		s.TX = nil
		s.Mode = "IDLE"
	}
	return s
}

// statusEqual compares two statuses ignoring UpdatedAt, so an event that changes
// nothing observable doesn't churn the topics/stream.
func statusEqual(a, b Status) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

func cloneStatus(s Status) Status {
	s.Networks = cloneLinks(s.Networks)
	s.Gateways = cloneLinks(s.Gateways)
	if s.TX != nil {
		tx := *s.TX
		s.TX = &tx
	}
	return s
}

func cloneLinks(m map[string]Link) map[string]Link {
	out := make(map[string]Link, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
