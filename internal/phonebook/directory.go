package phonebook

// The phonebook's face to the resolver chain (internal/idresolve).
//
// These three methods exist separately from Get/LookupByDMRID/LookupByCallsign
// for one reason: their signatures are primitives. idresolve.Directory is declared
// over (string, string, bool) rather than over a phonebook.Entry, so that package
// imports nothing and this one imports nothing of it — which is what keeps a
// Chain droppable into the frame layer's Resolver slot without dragging a SQL
// driver behind it. The compile-time assertion lives in idresolve's test, where
// importing this package is free.
//
// They also answer a narrower question than the Lookup* methods do. A resolver
// wants "who is this, if anyone" and never wants an error for "nobody": a station
// nobody has entered is the ordinary case on every node, not a failure, and a
// chain that had to distinguish ErrNotFound from a real database fault at every
// link would be a chain that stops resolving when the disk hiccups. So a miss and
// a fault both come back ok=false here, and the surface falls back to the raw id
// exactly as it does with no phonebook at all (D5).
//
// That swallowing is deliberate but it is not free — a database genuinely failing
// goes unreported on this path. It is the right trade for a display name (the
// worst case is a dashboard that shows an id instead of a name), and it would be
// the wrong trade for anything that decides something. Nothing here decides
// anything.

// DisplayForID returns the callsign and full name recorded against a DMR ID.
//
// ok reports that a row exists, NOT that it has a name: an entry with a callsign
// and no full name is a hit with an empty name, and the chain stops there rather
// than falling through to DMRIds.dat for a "better" answer. The operator entered
// that row; it wins (D1).
func (s *Store) DisplayForID(id uint32) (callsign, fullName string, ok bool) {
	e, err := s.LookupByDMRID(id)
	if err != nil {
		return "", "", false
	}
	return e.Callsign, e.FullName, true
}

// DisplayForCallsign returns the canonical callsign and full name recorded for a
// callsign, matched case-insensitively through the table's own NOCASE collation.
//
// The callsign comes back as the phonebook spells it, not as the caller spelled
// it, so a dashboard shows one consistent rendering of a station whatever case
// arrived off the air.
func (s *Store) DisplayForCallsign(cs string) (callsign, fullName string, ok bool) {
	e, err := s.LookupByCallsign(cs)
	if err != nil {
		return "", "", false
	}
	return e.Callsign, e.FullName, true
}

// IDForCallsign returns the DMR ID recorded for a callsign.
//
// ok is false when there is no row and ALSO when the row records no ID. The
// distinction matters to the chain: "I know this operator but not their ID" is
// not an answer to "what is their ID", so it must fall through to DMRIds.dat
// rather than resolving to zero. A phonebook entry with no DMR ID is the common
// case — it is a nullable column precisely because not every operator has one.
func (s *Store) IDForCallsign(cs string) (id uint32, ok bool) {
	e, err := s.LookupByCallsign(cs)
	if err != nil || e.DMRID == 0 {
		return 0, false
	}
	return e.DMRID, true
}
