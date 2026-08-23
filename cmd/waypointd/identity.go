package main

import (
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/publicview"
	"github.com/KN4OQW/waypoint/internal/status"
)

// Display names on the authenticated dashboard (D4).
//
// Every event and every live transmission the dashboard is served goes through
// one of the two decorators below on its way OUT of the daemon. That placement is
// the design, not an implementation detail:
//
//   - Not at ingest. The MQTT bridge stores what MMDVM-Host said; if it stamped a
//     name in, the history would freeze whatever the phonebook happened to say
//     that minute, and correcting a misspelled name would fix nothing already
//     recorded. Decorated on the way out, a correction fixes the whole history.
//   - Not in the hub. The hub fans one Event out to every subscriber, and the
//     public surface is one of the things reading history. Decorating there would
//     put a full name in front of the code that must not have it. Here, the
//     authenticated handlers decorate and nothing else does.
//   - Not persisted. internal/events writes an explicit column list with no column
//     for this, so a decorated event handed to the store round-trips back without
//     the name rather than silently gaining one.
//
// With no chain wired, or with an empty phonebook, both functions leave their
// argument exactly as they found it and `omitempty` drops the field from the JSON
// entirely — the byte-for-byte no-op D5 asks for.

// decorateEvent adds the phonebook's name for an event's source.
//
// It takes and returns a value, not a pointer: hub.Event arrives from the store
// and from the hub's fan-out, and mutating either in place would hand a name to
// every other subscriber including the ones that must not have it.
func (s *server) decorateEvent(e hub.Event) hub.Event {
	if s.identity == nil || e.Source == "" {
		return e
	}
	d := s.identity.DisplayForSource(e.Source)
	if d.FullName == "" {
		return e
	}
	// Source itself is deliberately left alone. The dashboard already keys its
	// last-heard table by it, and rewriting a callsign under a client that has
	// been accumulating rows against the old spelling would split one station into
	// two rows. The name arrives as its own field and the page composes them.
	e.SourceName = d.FullName
	return e
}

// decorateEvents is decorateEvent over a slice, returning a new one.
func (s *server) decorateEvents(evs []hub.Event) []hub.Event {
	if s.identity == nil || len(evs) == 0 {
		return evs
	}
	out := make([]hub.Event, len(evs))
	for i, e := range evs {
		out[i] = s.decorateEvent(e)
	}
	return out
}

// decorateStatus adds the same name to the transmission currently on the air, so
// the dashboard's "on air" card and its last-heard table agree about who this is.
//
// status.Status is copied shallowly and TX is a pointer, so the transmission is
// replaced with a copy rather than written through — the aggregator hands out the
// same snapshot to every caller, and one of them stamping a name into it would be
// a data race as well as a disclosure.
func (s *server) decorateStatus(st status.Status) status.Status {
	if s.identity == nil || st.TX == nil || st.TX.Source == "" {
		return st
	}
	d := s.identity.DisplayForSource(st.TX.Source)
	if d.FullName == "" {
		return st
	}
	tx := *st.TX
	tx.SourceName = d.FullName
	st.TX = &tx
	return st
}

// identityChain builds the resolver the decorators use: the phonebook over
// DMRIds.dat (D1).
//
// The DMRIds leg is deliberately absent here, and it is worth saying why rather
// than leaving a nil that reads as an oversight.
//
// A *dmrids.Table over the August 2026 export is 35.5 MB of live heap (measured;
// internal/dmrids/lookup.go), on a node whose smallest supported board has 512 MB
// total. waypointd has never held one — it loads a table transiently to count rows
// and drops it — and it does not need one on this path. An event's Source has
// ALREADY been through DMRIds.dat before Waypoint ever sees it: MMDVM-Host
// publishes src_info, which is its own lookup against the very same file, falling
// back to the decimal ID on a miss. A second lookup of that file for that ID can
// only re-derive what is already in the string, so the leg that adds something
// here is the phonebook, and that is the leg that is wired.
//
// The bus daemon is the other way round and stays that way: it holds a Table
// already and has no store handle, so it wires that leg and not this one.
func identityChain(dir idresolve.Directory) *idresolve.Chain {
	if dir == nil {
		return nil
	}
	return idresolve.New(dir, nil)
}

// publicResolver narrows the chain to what the public surface may have (D3).
//
// It exists to avoid the typed-nil trap that publicview.NewService documents for
// its own optional dependencies: assigning a nil *idresolve.Chain straight into
// an interface parameter produces a non-nil interface wrapping a nil pointer, and
// the nil check on the far side sails past it into a dereference. A node with no
// phonebook has a nil chain, so this is the ordinary case here, not a corner.
//
// The narrowing itself is done by publicview declaring a one-method interface;
// this only decides whether to hand it anything at all.
func publicResolver(c *idresolve.Chain) publicview.CallsignResolver {
	if c == nil {
		return nil
	}
	return c
}
