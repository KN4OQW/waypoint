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
//
// Since is when the link entered its *current* Up state, not when it was last
// confirmed: a re-confirmation of an already-up link leaves it alone, so "linked
// since 09:14" stays true across a probe that re-asserts it every few seconds.
type Link struct {
	Up     bool      `json:"up"`
	Detail string    `json:"detail,omitempty"`
	Since  time.Time `json:"since"`

	// expiresAt is the confirmation deadline for a network link — the point past
	// which nothing has re-asserted that it is up and the aggregator stops
	// claiming so. Zero means the assertion does not perish (a gateway's systemd
	// liveness, or a network link folded while the link watchdog is disabled).
	// Not serialized, and deliberately excluded from statusEqual: pushing a
	// deadline out is not an observable change and must not churn the topics.
	expiresAt time.Time
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
	// TypeLink is the original spelling of a network link coming *up*. It stays
	// accepted forever: events.db holds "link" rows written before the pair below
	// existed, and GET /api/history replays them into this same fold.
	TypeLink     = "link"
	TypeLinkUp   = "link_up"
	TypeLinkDown = "link_down"
	TypeFeedUp   = "feed_up"
	TypeFeedDown = "feed_down"
	TypeGWUp     = "gateway_up"
	TypeGWDown   = "gateway_down"
	// TypeGatewayStatus is a gateway daemon's own report about one of its upstream
	// links, verbatim from its MQTT status plane. The fold deliberately ignores it:
	// it is raw daemon chatter, one input the supervisor weighs against the unit
	// state and its own endpoint probe before claiming anything. What lands in
	// Networks is the supervisor's link_up/link_down verdict, not this. It is still
	// a hub event so it persists and shows in the event log, where a "Failed login
	// into DMR Network" line is exactly what an operator wants to see.
	TypeGatewayStatus = "gateway_status"
)

// DefaultTxTTL is the stranded-transmission watchdog: a transmission not ended
// within this window self-clears to idle (RFC-0008). It is the modem transmit
// timeout ceiling plus a margin — a real transmission cannot outlive the modem's
// own timeout, so a TX still "on the air" past this deadline is stranded (its
// daemon died without a closing event) and truth is that the node is idle.
const DefaultTxTTL = 200 * time.Second

// LinkTTLOff disables the network-link confirmation watchdog. It is the current
// default because nothing re-confirms a link yet: a link is asserted up by the
// event that raised it and lowered by an explicit link_down or a feed loss. The
// resilience supervisor (#22) is what periodically re-confirms each attachment,
// and it arms this watchdog so a link whose confirmation stops arriving stops
// being claimed — the same "truth is a function of time" rule the TX watchdog
// enforces. Arming it without a re-confirmer would take every link down once.
const LinkTTLOff time.Duration = 0

// Aggregator holds the single authoritative Status and notifies listeners on every
// change. The mutable copy lives behind mu; readers get a deep value copy, so the
// API/WS/republisher never race the fold.
type Aggregator struct {
	mu      sync.Mutex
	status  Status
	txTTL   time.Duration
	linkTTL time.Duration
	now     func() time.Time // injectable for tests

	lmu       sync.Mutex
	listeners map[int]func(Status)
	nextID    int
}

// New returns an idle aggregator with the given stranded-TX watchdog window and
// network-link confirmation window (LinkTTLOff disables the latter).
func New(txTTL, linkTTL time.Duration) *Aggregator {
	return &Aggregator{
		status:  Status{Mode: "IDLE", Networks: map[string]Link{}, Gateways: map[string]Link{}},
		txTTL:   txTTL,
		linkTTL: linkTTL,
		now:     time.Now,
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
	a.commit(func(s Status) Status { return applyEvent(s, e, a.txTTL, a.linkTTL) })
}

// Expire runs the stranded-TX and unconfirmed-link watchdogs against now, emitting
// on change. Called on a ticker by Run, and directly (with an injected clock) by
// tests.
func (a *Aggregator) Expire(now time.Time) {
	a.commit(func(s Status) Status { return expire(s, now, a.linkTTL) })
}

func (a *Aggregator) commit(f func(Status) Status) {
	a.mu.Lock()
	old := a.status
	next := f(cloneStatus(old))
	// The new value is ALWAYS stored, even when nothing observable changed: a
	// re-confirmation carries a fresh watchdog deadline, which statusEqual
	// deliberately ignores, and dropping the result here would leave the link
	// pinned to its first deadline and expire it mid-confirmation. What the
	// comparison gates is whether anyone is *told* — an unchanged status keeps its
	// UpdatedAt and wakes no listener, so the topics don't churn.
	changed := !statusEqual(old, next)
	if changed {
		next.UpdatedAt = a.now()
	} else {
		next.UpdatedAt = old.UpdatedAt
	}
	a.status = next
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
func applyEvent(s Status, e hub.Event, txTTL, linkTTL time.Duration) Status {
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
	case TypeLink, TypeLinkUp, TypeLinkDown:
		if e.Network != "" {
			up := e.Type != TypeLinkDown
			// Only an up assertion perishes. A link already reported down needs no
			// deadline — there is nothing left to stop claiming.
			var deadline time.Time
			if up && linkTTL > 0 {
				deadline = e.Time.Add(linkTTL)
			}
			s.Networks = setLink(s.Networks, e.Network, up, e.Detail, e.Time, deadline)
		}
	case TypeGWUp, TypeGWDown:
		name := firstNonEmpty(e.Network, e.Mode)
		if name != "" {
			s.Gateways = setLink(s.Gateways, name, e.Type == TypeGWUp, e.Detail, e.Time, time.Time{})
		}
	case TypeFeedUp, TypeFeedDown:
		s.Feed = Feed{Connected: e.Type == TypeFeedUp, Detail: e.Detail, Since: e.Time}
		if e.Type == TypeFeedDown {
			// Every network link is asserted by traffic on this feed. With the feed
			// gone nothing can re-assert one, so continuing to show them linked is
			// the latch RFC-0008 exists to prevent: the honest report is that the
			// link is no longer being confirmed. They come back as the link events
			// resume. Gateway liveness is untouched — it comes from systemd, which
			// is a source the feed's loss says nothing about.
			for name, l := range s.Networks {
				if l.Up {
					s.Networks = setLink(s.Networks, name, false, "unconfirmed — MMDVM-Host feed down", e.Time, time.Time{})
				}
			}
		}
	}
	return s
}

// setLink writes one link, preserving Since when the up/down state is unchanged.
// A re-confirmation is not a state change: without this, a supervisor that
// re-asserts a link every few seconds would keep resetting "linked since" and the
// dashboard could never show how long a link had held.
func setLink(m map[string]Link, name string, up bool, detail string, at, expiresAt time.Time) map[string]Link {
	out := cloneLinks(m)
	since := at
	if prev, ok := m[name]; ok && prev.Up == up {
		since = prev.Since
	}
	out[name] = Link{Up: up, Detail: detail, Since: since, expiresAt: expiresAt}
	return out
}

// expire runs both watchdogs (RFC-0008):
//
//   - a transmission past its deadline clears to idle — the self-heal that makes a
//     stranded TX (daemon died mid-transmission, no closing event) return to idle
//     instead of counting forever;
//   - a network link past its confirmation deadline stops being claimed. The
//     entry is kept, reported down, and says why, rather than being deleted: a
//     link that has gone quiet is news, and dropping the row would just move the
//     lie from "still linked" to "never existed".
func expire(s Status, now time.Time, linkTTL time.Duration) Status {
	if s.TX != nil && !s.TX.expiresAt.IsZero() && now.After(s.TX.expiresAt) {
		s.TX = nil
		s.Mode = "IDLE"
	}
	for name, l := range s.Networks {
		if l.Up && !l.expiresAt.IsZero() && now.After(l.expiresAt) {
			s.Networks = setLink(s.Networks, name, false, "unconfirmed for "+linkTTL.String(), now, time.Time{})
		}
	}
	return s
}

// statusEqual compares two statuses ignoring UpdatedAt and the links' internal
// confirmation deadlines, so neither the clock nor a re-confirmation that changes
// nothing observable churns the topics/stream.
func statusEqual(a, b Status) bool {
	a.UpdatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	a.Networks, b.Networks = withoutDeadlines(a.Networks), withoutDeadlines(b.Networks)
	a.Gateways, b.Gateways = withoutDeadlines(a.Gateways), withoutDeadlines(b.Gateways)
	return reflect.DeepEqual(a, b)
}

func withoutDeadlines(m map[string]Link) map[string]Link {
	out := make(map[string]Link, len(m))
	for k, v := range m {
		v.expiresAt = time.Time{}
		out[k] = v
	}
	return out
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
