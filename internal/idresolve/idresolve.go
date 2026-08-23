// Package idresolve layers the operator's phonebook over the shared DMRIds.dat
// table: one chain that answers "who is this" for a DMR/NXDN id or an event
// source, in a fixed order, with a fixed set of things it is willing to say.
//
// The order is phonebook, then DMRIds.dat, then nothing (D1). The phonebook wins
// because it is the one leg an operator curates by hand: they entered the row,
// they can fix it, and it is the only leg that carries a full name. DMRIds.dat is
// a third party's export of the world.
//
// # Purity
//
// This package imports nothing but the standard library, and that is deliberate
// rather than incidental. Both legs are narrow interfaces over PRIMITIVES — no
// phonebook.Entry, no dmrids.Table — so neither of those packages imports this one
// and this one imports neither of them. internal/phonebook satisfies Directory by
// having the methods; internal/dmrids.Table satisfies Table by having the methods
// it already had.
//
// That is what lets the frame layer consume a Chain. internal/bus/frames declares
// its own Resolver interface (CallsignForID/IDForCallsign) and never imports the
// thing that satisfies it, so a Chain drops in where a *dmrids.Table goes today
// without frames importing this package, this package importing frames, or the
// frame library gaining any file or database I/O. The compile-time assertion in
// the test file pins that.
//
// # What the legs actually cost, and why waypointd wires only one
//
// A *dmrids.Table over the August 2026 export is 35.5 MB of live heap (measured;
// see internal/dmrids/lookup.go), on a node whose smallest supported board has
// 512 MB total. waypointd therefore does not hold one, and did not before this
// package existed — it loads a table transiently to count rows and throws it away.
//
// It does not need one here either, and the reason is worth writing down because
// it looks like an omission. An event's Source has ALREADY been through
// DMRIds.dat by the time Waypoint sees it: MMDVM-Host publishes src_info, which is
// its own CDMRLookup::find(srcId) against the very same file, falling back to the
// decimal id on a miss (internal/mqtt/bridge.go). So a second lookup of the same
// file for the same id is redundant by construction — it can only re-derive what
// is already in the string. The leg that adds something on that path is the
// phonebook, and that is the leg waypointd wires. A nil Table is not a degraded
// chain there; it is the absence of a lookup that had already happened.
//
// The bus daemon is the other way round: it already holds a Table (it is a
// separate process addressing frames, with no store handle and no phonebook), so
// it wires that leg and not this one.
package idresolve

import (
	"strconv"
	"strings"
)

// Display is what a surface may show for a station.
//
// FullName is only ever populated from the phonebook, and only ever handed to an
// authenticated caller (D4). It is a separate field rather than a pre-formatted
// "CALL — Name" string so a caller that must not disclose it can take Callsign and
// structurally cannot take the rest — see CallsignForSource.
type Display struct {
	// Callsign is the resolved callsign, or "" when nothing resolved it.
	Callsign string
	// FullName is the phonebook's name for that operator, or "" — either because
	// there is no phonebook row or because the row has no name.
	FullName string
	// Source names the leg that answered: SourcePhonebook, SourceTable, or
	// SourceNone. It exists for tests and diagnostics, so "the phonebook won" is
	// assertable rather than inferred from a name happening to be present.
	Source Leg
}

// Leg names which leg of the chain answered.
type Leg string

const (
	// LegNone means nothing resolved the id; the caller falls back to the raw id
	// exactly as it did before this package existed (D5).
	LegNone Leg = ""
	// LegPhonebook means the operator's own phonebook answered.
	LegPhonebook Leg = "phonebook"
	// LegTable means DMRIds.dat answered.
	LegTable Leg = "dmrids"
)

// Resolved reports whether any leg answered.
func (d Display) Resolved() bool { return d.Callsign != "" }

// Directory is the admin-curated leg: internal/phonebook.Store satisfies it.
//
// Every signature is primitives, so neither package imports the other. The bool
// is "there is a row", which is NOT the same as "the row has a name" — a
// phonebook entry with a callsign and no full name is a hit that returns an empty
// name, and the chain must not fall through to DMRIds.dat looking for a better
// one. The operator entered that row; it wins (D1).
type Directory interface {
	// DisplayForID returns the callsign and full name recorded against a DMR ID.
	DisplayForID(id uint32) (callsign, fullName string, ok bool)
	// DisplayForCallsign returns the canonical callsign and full name recorded for
	// a callsign, matched case-insensitively.
	DisplayForCallsign(cs string) (callsign, fullName string, ok bool)
	// IDForCallsign returns the DMR ID recorded for a callsign. ok is false when
	// there is no row OR when the row records no ID — the caller must fall through
	// to the table in both cases, because "I know this operator but not their ID"
	// is not an answer to "what is their ID".
	IDForCallsign(cs string) (id uint32, ok bool)
}

// Table is the DMRIds.dat leg. It is exactly internal/bus/frames.Resolver, and
// *dmrids.Table satisfies it unchanged — this package adds no second reader of
// that file and no parallel lookup path (RFC-0003 §3).
type Table interface {
	CallsignForID(id uint32) string
	IDForCallsign(callsign string) uint32
}

// Chain resolves through the phonebook, then the table, then not at all.
//
// Either leg may be nil, and a nil leg is skipped rather than being an error: a
// node with an empty phonebook and no table resolves nothing and every caller
// falls back to the raw id, which is exactly what it did before (D5).
type Chain struct {
	dir Directory
	tab Table
}

// New builds a chain. Both legs are optional; see Chain.
//
// The legs are interfaces held as given, including the typed-nil hazard: a caller
// passing a nil *phonebook.Store through this parameter produces a non-nil
// interface wrapping a nil pointer, and the nil checks below would sail past it.
// Callers that may not have a leg should pass an untyped nil, the same discipline
// publicview.NewService documents for its own optional dependencies.
func New(dir Directory, tab Table) *Chain { return &Chain{dir: dir, tab: tab} }

// DisplayForID is the chain proper (D1/D2): phonebook, then DMRIds.dat, then
// nothing.
//
// Zero is never a DMR ID any authority issues, and the phonebook stores "no ID
// recorded" as exactly that, so a zero id short-circuits rather than matching the
// rows that have no id.
func (c *Chain) DisplayForID(id uint32) Display {
	if id == 0 {
		return Display{}
	}
	if c.dir != nil {
		if call, name, ok := c.dir.DisplayForID(id); ok && call != "" {
			return Display{Callsign: call, FullName: name, Source: LegPhonebook}
		}
	}
	if c.tab != nil {
		if call := c.tab.CallsignForID(id); call != "" {
			return Display{Callsign: call, Source: LegTable}
		}
	}
	return Display{}
}

// CallsignForID satisfies frames.Resolver. It is the chain's answer with the name
// dropped — the frame layer addresses stations, it does not describe them.
func (c *Chain) CallsignForID(id uint32) string { return c.DisplayForID(id).Callsign }

// IDForCallsign satisfies frames.Resolver, in the same order.
//
// A phonebook row with no DMR ID falls through to the table rather than answering
// 0. That is the one place the "phonebook wins" rule does not apply, because the
// phonebook does not have an answer to give: an operator recorded a person, not
// an id, and the table may still know it.
func (c *Chain) IDForCallsign(cs string) uint32 {
	cs = strings.TrimSpace(cs)
	if cs == "" {
		return 0
	}
	if c.dir != nil {
		if id, ok := c.dir.IDForCallsign(cs); ok && id != 0 {
			return id
		}
	}
	if c.tab != nil {
		return c.tab.IDForCallsign(cs)
	}
	return 0
}

// DisplayForSource resolves whatever an event actually carries.
//
// hub.Event has no numeric source field — it has one string, which the MQTT
// bridge fills from src_info, then src_callsign, then the decimal id. So the input
// here is a callsign on most events and a bare number on the ones nothing
// resolved, and this is the seam that takes either.
//
// A numeric source goes through the chain by id. A callsign source is already
// resolved as far as a callsign goes, so the only thing left to add is the
// phonebook's name for it — looked up by callsign, not by id, because going
// callsign -> id -> row would fail for exactly the rows that make this feature
// worth having: the operator who entered a name and no DMR ID.
//
// A source that is neither (a network name, "ALL", empty) comes back unresolved
// and the caller shows what it already had.
func (c *Chain) DisplayForSource(source string) Display {
	s := strings.TrimSpace(source)
	if s == "" {
		return Display{}
	}
	if id, ok := decimalID(s); ok {
		return c.DisplayForID(id)
	}
	// Upper-cased for the LOOKUP only. YSF and D-Star put the callsign on the air
	// in whatever case the radio was configured with, so sources arrive as "ae4ghi"
	// as readily as "AE4GHI", and everything else here has settled on upper —
	// dmrids.Parse upper-cases every row it reads, the phonebook upper-cases on
	// write. Matching without folding would miss the row.
	//
	// What comes back on a MISS is the string exactly as it arrived, and that is
	// not fastidiousness. A source is not always a callsign: an event can carry a
	// network name, and upper-casing turned BM_3102_United_States into
	// BM_3102_UNITED_STATES on a path that had resolved nothing at all. A chain
	// that rewrites what it could not resolve is a chain that changes the display
	// on a node with an empty phonebook, which is the one thing D5 forbids. Both
	// halves were caught by the table below rather than reasoned out.
	if c.dir != nil {
		if call, name, ok := c.dir.DisplayForCallsign(strings.ToUpper(s)); ok && call != "" {
			return Display{Callsign: call, FullName: name, Source: LegPhonebook}
		}
	}
	// Not in the phonebook, and already resolved as far as it is going to be: the
	// source stands byte-for-byte as it arrived. Reported as LegNone because no leg
	// of the chain answered.
	return Display{Callsign: s}
}

// CallsignForSource is DisplayForSource with the name unreachable.
//
// It exists for the public surface (D3). Anonymous callers may learn that a
// station was heard; they may not learn the operator's name or address. Rather
// than trusting a handler to copy only one field out of a Display, publicview
// depends on an interface whose ONLY method is this one — so a full name is not
// something it forgot to use, it is something it has no way to obtain.
func (c *Chain) CallsignForSource(source string) string {
	return c.DisplayForSource(source).Callsign
}

// decimalID parses a bare decimal DMR id. It is deliberately strict: only ASCII
// digits, only a value that fits the 32 bits the id space uses, and never zero.
// Anything else is a callsign or a label and is not put through an id lookup.
func decimalID(s string) (uint32, bool) {
	if s == "" || len(s) > 10 {
		return 0, false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, false
	}
	return uint32(n), true
}
