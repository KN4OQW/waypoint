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
	// SourceName is the operator's name for Source, from the phonebook, for the
	// AUTHENTICATED dashboard only (D4). It is stamped by the handlers that serve
	// /api/status and the WebSocket, never by the aggregator — the aggregator hands
	// the same snapshot to every caller, so writing a name into one would be a data
	// race as well as a disclosure. Empty on a node with no phonebook row for the
	// station, and `omitempty` then drops it from the JSON entirely.
	//
	// statusEqual compares the whole struct with reflect.DeepEqual and needs no
	// exception for this: the aggregator never writes the field, so it is zero on
	// both sides of every comparison the aggregator makes. Decoration happens
	// strictly after Snapshot, on a copy.
	SourceName string    `json:"source_name,omitempty"`
	expiresAt  time.Time // watchdog deadline — not serialized
}

// Link is the up/down state of a network reflector or a gateway daemon.
//
// Since is when the link entered its *current* Up state, not when it was last
// confirmed: a re-confirmation of an already-up link leaves it alone, so "linked
// since 09:14" stays true across a probe that re-asserts it every few seconds.
type Link struct {
	// Up is the legacy boolean and stays for compatibility with clients written
	// before State existed. It cannot express "we have not heard", so it reports
	// true for an unknown link -- do not gate on it; gate on State.
	Up bool `json:"up"`
	// State is the authoritative verdict: StateUp and StateDown mean something
	// vouched for the link one way or the other, StateUnknown means nothing has.
	// Detail is prose for a human and is not a machine interface; anything that
	// branches on link health reads this field.
	State  string    `json:"state"`
	Detail string    `json:"detail,omitempty"`
	Since  time.Time `json:"since"`

	// expiresAt is the confirmation deadline for a network link — the point past
	// which nothing has re-asserted that it is up and the aggregator stops claiming
	// so. Set only by ConfirmLink, so zero means nothing is re-checking this link
	// and the claim does not perish: a gateway's systemd liveness, or a network
	// whose daemon Waypoint cannot query. Not serialized, and deliberately excluded
	// from statusEqual: pushing a deadline out is not an observable change and must
	// not churn the topics.
	expiresAt time.Time
}

// The three link states. Lower-case strings, matching Transmission.Direction and
// the event type names — the wire format everywhere else in this package.
const (
	// StateUp: something vouched for this link.
	StateUp = "up"
	// StateDown: something reported it broken.
	StateDown = "down"
	// StateUnknown: nothing has said either way. A fresh waypointd reports this
	// until evidence arrives, and evidence decays back to it. Rendering it as
	// either up or down is the mistake this field exists to prevent.
	StateUnknown = "unknown"
)

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
	// TypeLinkRemoved retires a network entirely: the operator deleted or disabled
	// it, so there is nothing left to report. Distinct from link_down, which says a
	// network that still exists is not currently connected. Without it a removed
	// network keeps its last verdict in Networks forever, which moves the lie from
	// "still linked" to "still exists" rather than fixing it.
	TypeLinkRemoved = "link_removed"
	TypeFeedUp      = "feed_up"
	TypeFeedDown    = "feed_down"
	TypeGWUp        = "gateway_up"
	TypeGWDown      = "gateway_down"
	// TypeGatewayStatus is a gateway daemon's own report about one of its upstream
	// links, verbatim from its MQTT status plane. The fold deliberately ignores it:
	// it is raw daemon chatter, one input the supervisor weighs against the unit
	// state and its own endpoint probe before claiming anything. What lands in
	// Networks is the supervisor's link_up/link_down verdict, not this. It is still
	// a hub event so it persists and shows in the event log, where a "Failed login
	// into DMR Network" line is exactly what an operator wants to see.
	TypeGatewayStatus = "gateway_status"
	// TypeSupervisorAction is something the resilience supervisor did, or declined
	// to do, about a lost upstream link (#22). Unattended recovery has to be
	// legible after the fact — an operator who was asleep when the ISP dropped
	// should be able to read what happened from the event log — so a restart is an
	// event, not just a line in the daemon's own log. The fold ignores it: it
	// records an action, not a state.
	TypeSupervisorAction = "supervisor_action"
)

// DefaultTxTTL is the stranded-transmission watchdog: a transmission not ended
// within this window self-clears to idle (RFC-0008). It is the modem transmit
// timeout ceiling plus a margin — a real transmission cannot outlive the modem's
// own timeout, so a TX still "on the air" past this deadline is stranded (its
// daemon died without a closing event) and truth is that the node is idle.
const DefaultTxTTL = 200 * time.Second

// LinkTTLOff disables the network-link confirmation watchdog entirely.
const LinkTTLOff time.Duration = 0

// DefaultLinkTTL is how long a *confirmed* link stays claimed without a fresh
// confirmation. Only links something is actively re-checking are subject to it
// (see ConfirmLink), so this can be on by default without penalising a link that
// has no confirmation source.
//
// Three minutes against a supervisor that re-confirms every thirty seconds: five
// missed cycles of slack, so a broker hiccup or a daemon mid-restart does not
// lower a working link, while a supervisor that has genuinely stopped checking
// stops being believed inside a few minutes.
const DefaultLinkTTL = 3 * time.Minute

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
	a.commit(func(s Status) Status { return applyEvent(s, e, a.txTTL) })
}

// Expire runs the stranded-TX and unconfirmed-link watchdogs against now, emitting
// on change. Called on a ticker by Run, and directly (with an injected clock) by
// tests.
func (a *Aggregator) Expire(now time.Time) {
	a.commit(func(s Status) Status { return expire(s, now, a.linkTTL) })
}

// ConfirmLink records that something has just re-checked this network and found it
// up, pushing its confirmation deadline out.
//
// This is what makes a link claim perishable, and it is deliberately the ONLY way
// one becomes so. A link the supervisor can re-check (it asks DMRGateway every
// cycle) stops being claimed if those confirmations stop arriving; a link nothing
// can re-check — DAPNET, whose daemon Waypoint does not package yet — has no
// deadline and keeps its last known state. Arming the watchdog for both would take
// the second kind down on a timer that says nothing whatsoever about the link.
//
// Confirming is not an observable change (statusEqual ignores the deadline), so
// this neither wakes a listener nor republishes a topic; it runs every cycle
// without churn, and only the absence of it is ever visible.
func (a *Aggregator) ConfirmLink(name string, at time.Time) {
	if name == "" || a.linkTTL <= 0 {
		return
	}
	a.commit(func(s Status) Status {
		l, ok := s.Networks[name]
		if !ok || !l.Up {
			return s // nothing to keep alive
		}
		l.expiresAt = at.Add(a.linkTTL)
		s.Networks = cloneLinks(s.Networks)
		s.Networks[name] = l
		return s
	})
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
	case TypeLink, TypeLinkUp, TypeLinkDown:
		if e.Network != "" {
			// No deadline here. A claim becomes perishable only when something is
			// actually re-confirming it — see ConfirmLink — because a link nobody can
			// re-check must keep its last known state rather than decay to "down" on a
			// timer that says nothing about the link.
			s.Networks = setLink(s.Networks, e.Network, e.Type != TypeLinkDown, e.State, e.Detail, e.Time, time.Time{})
		}
	case TypeLinkRemoved:
		if e.Network != "" {
			if _, ok := s.Networks[e.Network]; ok {
				s.Networks = cloneLinks(s.Networks)
				delete(s.Networks, e.Network)
			}
		}
	case TypeGWUp, TypeGWDown:
		name := firstNonEmpty(e.Network, e.Mode)
		if name != "" {
			s.Gateways = setLink(s.Gateways, name, e.Type == TypeGWUp, e.State, e.Detail, e.Time, time.Time{})
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
					s.Networks = setLink(s.Networks, name, false, StateUnknown, "unconfirmed — MMDVM-Host feed down", e.Time, time.Time{})
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
func setLink(m map[string]Link, name string, up bool, state, detail string, at, expiresAt time.Time) map[string]Link {
	out := cloneLinks(m)
	if state == "" {
		// A caller with no verdict to offer gets the boolean's meaning, not a
		// silent "unknown": the gateway liveness probe genuinely knows whether the
		// unit is running, and that is evidence.
		state = StateUp
		if !up {
			state = StateDown
		}
	}
	since := at
	if prev, ok := m[name]; ok && prev.Up == up && prev.State == state {
		since = prev.Since
	}
	out[name] = Link{Up: up, State: state, Detail: detail, Since: since, expiresAt: expiresAt}
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
			s.Networks = setLink(s.Networks, name, false, StateUnknown, "unconfirmed for "+linkTTL.String(), now, time.Time{})
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
