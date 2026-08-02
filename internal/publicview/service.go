package publicview

import (
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// The public activity surface: what an anonymous visitor may learn about who has
// been using this node. Three answers, and each is deliberately smaller than the
// admin view of the same thing.
//
// Status says whether the node is busy, and nothing about who is on it. LastHeard
// says which callsigns have been heard and when, and nothing about how well.
// Counters says how many, and nothing about which. Every one of them is bounded by
// the operator's retention window (D6) and filtered through the suppress list (D8).
//
// The structs below are the enforcement point for D2's never-public list, which is
// why they are so bare. There is no Duration field to accidentally populate, no
// BER, no RSSI, no loss, no network path, no talkgroup, no coordinates. A field
// that does not exist cannot be leaked by a handler that forgets to clear it, and
// TestPublicStructsCarryNothingElse fails the build if one is ever added.

// Status is the public liveness answer (D5): busy, or idle since when.
//
// It carries no identity at all. "Someone is transmitting" is what a visitor
// needs to decide whether to key up; who it is, on what talkgroup, for how long
// are all the authenticated dashboard's business.
type Status struct {
	// State is "transmitting" or "idle".
	State string `json:"state"`
	// LastActivityMinutes is how long ago the last transmission ended, in whole
	// minutes. Nil while transmitting, and nil when nothing is in the window at
	// all — a node with no recent activity says nothing rather than reporting a
	// number that would disclose how long it has been quiet since some earlier
	// point outside the window.
	LastActivityMinutes *int `json:"last_activity_minutes,omitempty"`
}

const (
	StateTransmitting = "transmitting"
	StateIdle         = "idle"
)

// Heard is one entry in the public last-heard list: a callsign, the mode it was
// heard on, and when. That is the whole permitted set.
//
// Duration, BER, RSSI, loss, talkgroup and network are absent by design, not by
// omission — see the package comment above. The admin history keeps all of them;
// this is a different, smaller answer to a different, anonymous question.
type Heard struct {
	Callsign string    `json:"callsign"`
	Mode     string    `json:"mode"`
	At       time.Time `json:"at"`
}

// Counters is the public activity summary (D5): how much, never who.
type Counters struct {
	Callsigns     int `json:"callsigns"`
	Transmissions int `json:"transmissions"`
	WindowHours   int `json:"window_hours"`
}

// LastHeardResult wraps the list with whether it can be trusted at all.
//
// When the station ID database is missing or corrupt, DMR, P25 and NXDN sources
// arrive as bare numeric IDs (see publishableCallsign), and a list built from
// what is left would be a lie by omission: it would quietly show only the D-Star
// and YSF traffic while presenting itself as everything the node has heard. So the
// list is withheld entirely and replaced by one empty record carrying Notice —
// the page renders that single blank row and stops there.
//
// Saying "the database is broken" is the honest answer, and it is also the useful
// one: it is the state an operator can fix, where a mysteriously short list is not.
type LastHeardResult struct {
	// Available reports whether Entries is a complete answer.
	Available bool `json:"available"`
	// Notice is the operator-facing explanation when it is not. Server-authored,
	// never derived from a path or an OS error.
	Notice string `json:"notice,omitempty"`
	// Entries is empty whenever Available is false.
	Entries []Heard `json:"entries"`
}

// CountersResult wraps the counters the same way and for the same reason.
//
// The counters are computed from resolved callsigns, so a broken ID database
// would silently drop every DMR, P25 and NXDN station out of both figures. A
// visitor reading "3 callsigns heard today" on a busy DMR repeater is worse
// informed than one reading that the database is down.
type CountersResult struct {
	Available bool     `json:"available"`
	Notice    string   `json:"notice,omitempty"`
	Counters  Counters `json:"counters"`
}

// History is the slice of the event store this service reads. Narrowing it to one
// method keeps the service testable against synthetic fixtures without standing up
// a database, and makes it obvious that nothing here writes.
//
// It is exported so callers can hold a nil interface deliberately. Assigning a nil
// *events.Store into an unexported parameter would produce a non-nil interface
// wrapping a nil pointer, and the nil checks below would sail past it into a
// dereference — the classic Go footgun, and one worth designing out rather than
// remembering.
type History interface {
	History(events.HistoryQuery) ([]hub.Event, error)
}

// Live is the slice of the status aggregator this service reads.
type Live interface {
	Snapshot() status.Status
}

// Service answers the public activity questions. It owns no state: every call
// re-reads the settings and the suppress list, so an operator's change to either
// takes effect on the next request rather than at the next restart.
type Service struct {
	store   *Store
	history History
	live    Live
	// idDB reports whether callsign resolution can be trusted. Never nil; see
	// NewService.
	idDB func() IDDBStatus
	// now is injectable so window-edge behavior can be tested exactly rather than
	// approximately.
	now func() time.Time
}

// NewService wires the public activity service. history and live may be nil on a
// node whose event store or aggregator is unavailable; the reads degrade to empty
// answers rather than failing, because a public page that 500s when the event
// database is briefly unavailable is worse than one that says "no recent
// activity".
func NewService(s *Store, h History, l Live) *Service {
	return &Service{store: s, history: h, live: l, idDB: alwaysAvailable, now: time.Now}
}

// WithIDDatabase attaches the station ID database probe, usually DMRIDsProbe over
// the node's DMRIds.dat. Without it the service assumes resolution is fine and
// relies on the per-source filter alone.
func (s *Service) WithIDDatabase(probe func() IDDBStatus) *Service {
	if probe != nil {
		s.idDB = probe
	}
	return s
}

// Status reports whether the node is on the air.
//
// The live aggregator answers "transmitting" directly. "Idle since" comes from the
// event history rather than from the aggregator, because the aggregator's whole
// job is to hold the *current* state and it deliberately does not remember how
// long ago the last one ended.
func (s *Service) Status() (Status, error) {
	set, err := s.store.Settings()
	if err != nil {
		return Status{}, err
	}
	if s.live != nil && s.live.Snapshot().TX != nil {
		return Status{State: StateTransmitting}, nil
	}
	out := Status{State: StateIdle}
	if s.history == nil {
		return out, nil
	}
	now := s.now()
	evs, err := s.history.History(events.HistoryQuery{
		Since: now.Add(-window(set)),
		Limit: events.MaxHistoryLimit,
	})
	if err != nil {
		return Status{}, err
	}
	// Newest-first from the store, so the first voice event in range is the most
	// recent. Suppressed stations still count as activity: hiding a callsign is
	// not the same as claiming the node was quiet, and a suppressed operator would
	// otherwise be identifiable by the node going conspicuously silent.
	for _, e := range evs {
		if !isVoice(e.Type) {
			continue
		}
		mins := int(now.Sub(e.Time) / time.Minute)
		if mins < 0 {
			mins = 0
		}
		out.LastActivityMinutes = &mins
		break
	}
	return out, nil
}

// LastHeard returns the public last-heard list, newest first, one entry per
// transmission.
//
// limit <= 0 means "as many as the window holds", bounded by the event store's own
// read ceiling. The retention window is a query bound, not a deletion policy: the
// admin history keeps everything, and this simply declines to look further back.
//
// A broken station ID database withholds the list rather than shortening it — see
// LastHeardResult.
func (s *Service) LastHeard(limit int) (LastHeardResult, error) {
	if db := s.idDB(); !db.Available {
		return LastHeardResult{Notice: db.Reason, Entries: []Heard{}}, nil
	}
	set, err := s.store.Settings()
	if err != nil {
		return LastHeardResult{}, err
	}
	evs, suppressed, err := s.windowEvents(set)
	if err != nil {
		return LastHeardResult{}, err
	}
	out := []Heard{}
	for _, e := range evs {
		call, ok := publishableCallsign(e.Source)
		if !ok || suppressed[call] {
			continue
		}
		out = append(out, Heard{Callsign: call, Mode: e.Mode, At: e.Time})
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return LastHeardResult{Available: true, Entries: out}, nil
}

// Counters summarizes the same window: distinct callsigns and total
// transmissions, with suppressed stations excluded from both.
//
// Excluding a suppressed station from the transmission count as well as the
// callsign count is the point D8 turns on. A count that still moved when a
// suppressed operator keyed up would let anyone watching the page infer exactly
// when they were on the air, which is the thing they asked not to be published.
// A broken station ID database withholds both figures for the same reason it
// withholds the list — see CountersResult.
func (s *Service) Counters() (CountersResult, error) {
	set, err := s.store.Settings()
	if err != nil {
		return CountersResult{}, err
	}
	if db := s.idDB(); !db.Available {
		return CountersResult{Notice: db.Reason, Counters: Counters{WindowHours: set.RetentionHours}}, nil
	}
	evs, suppressed, err := s.windowEvents(set)
	if err != nil {
		return CountersResult{}, err
	}
	seen := map[string]bool{}
	out := Counters{WindowHours: set.RetentionHours}
	for _, e := range evs {
		call, ok := publishableCallsign(e.Source)
		if !ok || suppressed[call] {
			continue
		}
		out.Transmissions++
		seen[call] = true
	}
	out.Callsigns = len(seen)
	return CountersResult{Available: true, Counters: out}, nil
}

// windowEvents reads the completed transmissions inside the retention window,
// newest first, along with the suppress list to filter them against.
func (s *Service) windowEvents(set Settings) ([]hub.Event, map[string]bool, error) {
	suppressed, err := s.store.SuppressSet()
	if err != nil {
		return nil, nil, err
	}
	if s.history == nil {
		return nil, suppressed, nil
	}
	// Only the *_end events, so one transmission is counted once. Starts and ends
	// both carry the identity (the bridge recovers it for the end from the start),
	// and counting both would double every number on the page.
	evs, err := s.history.History(events.HistoryQuery{
		Since: s.now().Add(-window(set)),
		Type:  status.TypeRFEnd,
		Limit: events.MaxHistoryLimit,
	})
	if err != nil {
		return nil, nil, err
	}
	return evs, suppressed, nil
}

// window is the retention window as a duration, clamped the same way the model
// layer clamps it. A settings row that somehow holds an out-of-range value must
// not become an unbounded query.
func window(set Settings) time.Duration {
	return time.Duration(clampRetention(set.RetentionHours)) * time.Hour
}

// isVoice reports whether an event type represents someone on the air. Used only
// by Status, which cares that the node was busy and not who was on it, so it
// counts network traffic as activity too.
func isVoice(t string) bool {
	switch t {
	case status.TypeRFStart, status.TypeRFEnd, status.TypeNetStart, status.TypeNetEnd:
		return true
	}
	return false
}

// publishableCallsign decides whether an event's source may appear on the public
// page at all, and returns the base callsign to publish it under.
//
// This is the filter the runbook's ground-truth pass exists for. hub.Event.Source
// is not always a callsign — MMDVMHost fills it differently per mode:
//
//   - D-Star and YSF publish src_callsign, which is the callsign off the air.
//   - DMR, P25 and NXDN publish src_info, which is CDMRLookup::find(srcId) and its
//     siblings. On a hit that is the callsign from the ID database. On a miss it
//     falls back to the decimal ID rendered as a string, and the all-stations ID
//     resolves to the literal "ALL".
//   - M17 (KN4OQW/MMDVM-Host, m17-restore) carries base-40 callsigns in its LSF
//     and has no ID database in the path at all, so it will land here shaped like
//     D-Star and YSF once its control class gains a WriteJSON. Today it reports
//     only through LogMessage, so no M17 event reaches this filter.
//   - FM publishes {timestamp, state} and no identity whatsoever, so it never
//     produces a heard entry either.
//
// So a node whose DMR ID database is stale or missing produces a stream of sources
// like "3112345". Publishing those would be worse than publishing nothing: a bare
// DMR ID is trivially resolvable to a name and a postal address through the public
// ID databases, which is exactly the disclosure the public surface is supposed to
// avoid, and it is not even the callsign the page claims to be showing.
//
// The rule is therefore positive rather than subtractive: publish only what is
// callsign-shaped, and drop everything else. An unresolvable ID is dropped, not
// rendered — the list is shorter and honest instead of longer and wrong.
func publishableCallsign(source string) (string, bool) {
	s := strings.TrimSpace(source)
	if s == "" {
		return "", false
	}
	// The all-stations destination resolving into a source position; not a station.
	if strings.EqualFold(s, "ALL") {
		return "", false
	}
	// An unresolved DMR/P25/NXDN ID. Checked before validation because a bare ID
	// otherwise satisfies every structural rule a callsign has to meet.
	if isAllDigits(s) {
		return "", false
	}
	call, err := ValidateCallsign(s)
	if err != nil {
		return "", false
	}
	return call, true
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
