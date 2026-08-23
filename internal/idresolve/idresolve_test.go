package idresolve_test

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/dmrids"
	"github.com/KN4OQW/waypoint/internal/idresolve"
	"github.com/KN4OQW/waypoint/internal/phonebook"
	"github.com/KN4OQW/waypoint/internal/store"
)

// The package is tested from _test rather than from inside it, so every
// assertion below goes through the exported surface a caller actually has — and
// so this file can import phonebook and frames to prove the two couplings that
// matter without those imports existing in the package itself.

// ---------------------------------------------------------------------------
// The couplings, asserted at compile time
// ---------------------------------------------------------------------------

// A Chain must drop into the frame layer's Resolver slot where a *dmrids.Table
// goes today. This is the deliverable's "the frame layer can consume it without
// import cycles", and it is checked by the compiler rather than by a comment:
// frames does not import idresolve, idresolve does not import frames, and the
// interface is satisfied structurally.
var _ frames.Resolver = (*idresolve.Chain)(nil)

// The real legs satisfy the real interfaces. If either package's method set ever
// drifts, this file stops compiling — which is the point, since nothing else
// forces phonebook.Store and idresolve.Directory to agree.
var (
	_ idresolve.Directory = (*phonebook.Store)(nil)
	_ idresolve.Table     = (*dmrids.Table)(nil)
)

// ---------------------------------------------------------------------------
// Fakes, for the ordering tests
// ---------------------------------------------------------------------------

// fakeDir is a Directory with no database behind it, so the precedence tests say
// something about the chain rather than about SQLite. The real store is exercised
// separately below.
type fakeDir struct {
	byID   map[uint32][2]string // id -> {callsign, fullName}
	byCall map[string][2]string // CALLSIGN -> {callsign, fullName}
	ids    map[string]uint32    // CALLSIGN -> id
}

func (f fakeDir) DisplayForID(id uint32) (string, string, bool) {
	v, ok := f.byID[id]
	return v[0], v[1], ok
}

func (f fakeDir) DisplayForCallsign(cs string) (string, string, bool) {
	v, ok := f.byCall[cs]
	return v[0], v[1], ok
}

func (f fakeDir) IDForCallsign(cs string) (uint32, bool) {
	id, ok := f.ids[cs]
	return id, ok && id != 0
}

func table(rows map[uint32]string) *dmrids.Table {
	t := dmrids.New()
	for id, call := range rows {
		t.Add(id, call)
	}
	return t
}

// ---------------------------------------------------------------------------
// Precedence (D1)
// ---------------------------------------------------------------------------

// TestPhonebookWinsOverTable is the decision this whole package exists to
// implement: the same ID is in both legs, and the admin-curated one answers.
func TestPhonebookWinsOverTable(t *testing.T) {
	dir := fakeDir{
		byID: map[uint32][2]string{3180202: {"KN4OQW", "Clint Chance"}},
	}
	tab := table(map[uint32]string{3180202: "OLDCALL"})
	c := idresolve.New(dir, tab)

	got := c.DisplayForID(3180202)
	if got.Callsign != "KN4OQW" {
		t.Errorf("callsign = %q, want KN4OQW — the phonebook must win (D1)", got.Callsign)
	}
	if got.FullName != "Clint Chance" {
		t.Errorf("full name = %q, want the phonebook's", got.FullName)
	}
	if got.Source != idresolve.LegPhonebook {
		t.Errorf("answered by %q, want %q", got.Source, idresolve.LegPhonebook)
	}
	// The Resolver view of the same answer, since that is what the frame layer sees.
	if call := c.CallsignForID(3180202); call != "KN4OQW" {
		t.Errorf("CallsignForID = %q, want KN4OQW", call)
	}
}

// TestPhonebookRowWithoutANameStillWins is the subtle half of D1. A phonebook
// entry that records only a callsign is a hit, not a partial one: the chain must
// not fall through to DMRIds.dat hunting for a row that has more to say, because
// the operator's row is the authority on who that ID is.
func TestPhonebookRowWithoutANameStillWins(t *testing.T) {
	dir := fakeDir{byID: map[uint32][2]string{3180202: {"KN4OQW", ""}}}
	tab := table(map[uint32]string{3180202: "OLDCALL"})

	got := idresolve.New(dir, tab).DisplayForID(3180202)
	if got.Callsign != "KN4OQW" || got.Source != idresolve.LegPhonebook {
		t.Errorf("got %+v, want the phonebook's callsign with no name", got)
	}
	if got.FullName != "" {
		t.Errorf("full name = %q, want empty", got.FullName)
	}
}

// ---------------------------------------------------------------------------
// Fallback order (D1) and the raw-ID floor (D5)
// ---------------------------------------------------------------------------

func TestFallbackOrder(t *testing.T) {
	dir := fakeDir{byID: map[uint32][2]string{1: {"PBONLY", "Phone Book"}}}
	tab := table(map[uint32]string{2: "TABLEONLY"})
	c := idresolve.New(dir, tab)

	for _, tc := range []struct {
		name     string
		id       uint32
		wantCall string
		wantName string
		wantLeg  idresolve.Leg
	}{
		{"phonebook only", 1, "PBONLY", "Phone Book", idresolve.LegPhonebook},
		{"table only", 2, "TABLEONLY", "", idresolve.LegTable},
		{"neither", 3, "", "", idresolve.LegNone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.DisplayForID(tc.id)
			if got.Callsign != tc.wantCall || got.FullName != tc.wantName || got.Source != tc.wantLeg {
				t.Errorf("DisplayForID(%d) = %+v, want {%q %q %q}", tc.id, got, tc.wantCall, tc.wantName, tc.wantLeg)
			}
			if got.Resolved() != (tc.wantCall != "") {
				t.Errorf("Resolved() = %v for %+v", got.Resolved(), got)
			}
		})
	}
}

// TestTableNeverCarriesAName: DMRIds.dat has a name column and this chain
// deliberately does not read it. Only the phonebook — the operator's own,
// correctable data — may put a name on a screen.
func TestTableNeverCarriesAName(t *testing.T) {
	c := idresolve.New(nil, table(map[uint32]string{3180202: "KN4OQW"}))
	if got := c.DisplayForID(3180202); got.FullName != "" {
		t.Errorf("the DMRIds leg produced a full name %q; only the phonebook may", got.FullName)
	}
}

// TestZeroIDNeverResolves: zero is not an issued DMR ID and is how the phonebook
// stores "no ID recorded", so resolving it would match whichever nameless row the
// directory happened to key at zero.
func TestZeroIDNeverResolves(t *testing.T) {
	dir := fakeDir{byID: map[uint32][2]string{0: {"WRONG", "Should Not Match"}}}
	if got := idresolve.New(dir, table(map[uint32]string{0: "ALSOWRONG"})).DisplayForID(0); got.Resolved() {
		t.Errorf("DisplayForID(0) resolved to %+v, want nothing", got)
	}
}

// ---------------------------------------------------------------------------
// Empty-phonebook no-op equivalence (D5)
// ---------------------------------------------------------------------------

// TestEmptyPhonebookIsIdenticalToTableAlone is D5 stated as an equivalence: on a
// node whose phonebook nobody has filled in, every answer is the one the bare
// table gives. A nil directory and an empty directory must both behave that way,
// because the first is how waypointd degrades and the second is how a real node
// with an untouched phonebook actually looks.
func TestEmptyPhonebookIsIdenticalToTableAlone(t *testing.T) {
	tab := table(map[uint32]string{3180202: "KN4OQW", 3110000: "W1AW"})
	bare := idresolve.New(nil, tab)
	empty := idresolve.New(fakeDir{}, tab)

	for _, id := range []uint32{3180202, 3110000, 4242424, 0} {
		want := tab.CallsignForID(id)
		if id == 0 {
			want = "" // the chain refuses zero; so does the table, which holds no zero row
		}
		for name, c := range map[string]*idresolve.Chain{"nil directory": bare, "empty directory": empty} {
			got := c.DisplayForID(id)
			if got.Callsign != want {
				t.Errorf("%s: DisplayForID(%d) = %q, want the table's %q", name, id, got.Callsign, want)
			}
			if got.FullName != "" {
				t.Errorf("%s: DisplayForID(%d) invented a name %q", name, id, got.FullName)
			}
		}
	}
}

// TestNoLegsResolvesNothing is the floor: no phonebook, no table, and every
// caller falls back to the raw id exactly as it did before this package existed.
func TestNoLegsResolvesNothing(t *testing.T) {
	c := idresolve.New(nil, nil)
	if got := c.DisplayForID(3180202); got.Resolved() {
		t.Errorf("a chain with no legs resolved %+v", got)
	}
	if got := c.CallsignForID(3180202); got != "" {
		t.Errorf("CallsignForID = %q, want empty", got)
	}
	if got := c.IDForCallsign("KN4OQW"); got != 0 {
		t.Errorf("IDForCallsign = %d, want 0", got)
	}
	// A callsign source still comes back as itself — the string arrived resolved
	// and the chain is not what resolved it.
	if got := c.DisplayForSource("KN4OQW"); got.Callsign != "KN4OQW" || got.FullName != "" {
		t.Errorf("DisplayForSource = %+v, want the callsign unchanged and no name", got)
	}
}

// ---------------------------------------------------------------------------
// IDForCallsign (the reverse direction)
// ---------------------------------------------------------------------------

// TestIDForCallsignFallsThroughARowWithNoID is the one place "phonebook wins"
// does not apply, because the phonebook has no answer to give.
func TestIDForCallsignFallsThroughARowWithNoID(t *testing.T) {
	dir := fakeDir{
		byCall: map[string][2]string{"W1AW": {"W1AW", "ARRL HQ"}},
		ids:    map[string]uint32{"W1AW": 0}, // a row exists; it records no DMR ID
	}
	tab := table(map[uint32]string{3110000: "W1AW"})

	if got := idresolve.New(dir, tab).IDForCallsign("W1AW"); got != 3110000 {
		t.Errorf("IDForCallsign = %d, want the table's 3110000 — a phonebook row with "+
			"no ID is not an answer to what the ID is", got)
	}
}

func TestIDForCallsignPrefersThePhonebook(t *testing.T) {
	dir := fakeDir{ids: map[string]uint32{"KN4OQW": 3180202}}
	tab := table(map[uint32]string{3999999: "KN4OQW"})
	if got := idresolve.New(dir, tab).IDForCallsign("KN4OQW"); got != 3180202 {
		t.Errorf("IDForCallsign = %d, want the phonebook's 3180202", got)
	}
	if got := idresolve.New(dir, tab).IDForCallsign("  "); got != 0 {
		t.Errorf("IDForCallsign(blank) = %d, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// DisplayForSource — the seam the event path actually uses
// ---------------------------------------------------------------------------

// TestDisplayForSource covers what hub.Event.Source really contains: a callsign
// on most events (MMDVM-Host's src_info already resolved it), a bare decimal id
// on the ones nothing resolved, and occasionally neither.
func TestDisplayForSource(t *testing.T) {
	dir := fakeDir{
		byID:   map[uint32][2]string{3999999: {"ZZ9ABC", "Unlisted Operator"}},
		byCall: map[string][2]string{"KN4OQW": {"KN4OQW", "Clint Chance"}},
	}
	tab := table(map[uint32]string{3110000: "W1AW"})
	c := idresolve.New(dir, tab)

	for _, tc := range []struct {
		name       string
		in         string
		call, full string
	}{
		// The common case: src_info already resolved it, and the phonebook adds
		// the name. Looked up BY CALLSIGN — going callsign->id->row would miss
		// exactly the rows that make this worth having (no DMR ID recorded).
		{"callsign in the phonebook", "KN4OQW", "KN4OQW", "Clint Chance"},
		{"callsign not in the phonebook", "W4RJM", "W4RJM", ""},
		{"lowercase off the air", "kn4oqw", "KN4OQW", "Clint Chance"},
		// A numeric source is one MMDVM-Host could not resolve against DMRIds.dat.
		// The phonebook is the only leg that can add anything here, and does.
		{"numeric resolved by the phonebook", "3999999", "ZZ9ABC", "Unlisted Operator"},
		{"numeric resolved by the table", "3110000", "W1AW", ""},
		{"numeric nobody knows", "4242424", "", ""},
		// Not stations.
		{"empty", "", "", ""},
		{"blank", "   ", "", ""},
		{"network name", "BM_3102_United_States", "BM_3102_United_States", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := c.DisplayForSource(tc.in)
			if got.Callsign != tc.call || got.FullName != tc.full {
				t.Errorf("DisplayForSource(%q) = {%q %q}, want {%q %q}",
					tc.in, got.Callsign, got.FullName, tc.call, tc.full)
			}
			// CallsignForSource is the same answer with the name unreachable (D3).
			if only := c.CallsignForSource(tc.in); only != tc.call {
				t.Errorf("CallsignForSource(%q) = %q, want %q", tc.in, only, tc.call)
			}
		})
	}
}

// TestNumericSourceOutOfRange: a decimal that cannot be a DMR ID is not put
// through an id lookup. It is left alone rather than being truncated into
// somebody else's ID by a narrowing conversion.
func TestNumericSourceOutOfRange(t *testing.T) {
	// 5000000000 & 0xFFFFFFFF == 705032704. A narrowing parse would resolve it.
	tab := table(map[uint32]string{705032704: "WRONG"})
	got := idresolve.New(nil, tab).DisplayForSource("5000000000")
	if got.Callsign == "WRONG" {
		t.Error("an out-of-range decimal was narrowed into a real ID")
	}
	if got.Callsign != "5000000000" {
		t.Errorf("DisplayForSource = %q, want the string left as it arrived", got.Callsign)
	}
}

// ---------------------------------------------------------------------------
// The real phonebook behind the chain
// ---------------------------------------------------------------------------

// TestChainOverTheRealStore runs the precedence rule through the actual
// phonebook.Store and the actual dmrids.Table, so the interface satisfaction
// above is backed by behaviour and not only by shape.
func TestChainOverTheRealStore(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test cleanup
	pb := phonebook.New(st)

	if _, err := pb.Create(phonebook.Entry{
		Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance", Email: "clint@example.invalid",
	}); err != nil {
		t.Fatal(err)
	}
	// An operator with no DMR ID recorded — the case that makes by-callsign
	// resolution necessary.
	if _, err := pb.Create(phonebook.Entry{Callsign: "W1AW", FullName: "ARRL HQ"}); err != nil {
		t.Fatal(err)
	}

	tab := table(map[uint32]string{3180202: "STALECALL", 3110000: "W1AW"})
	c := idresolve.New(pb, tab)

	got := c.DisplayForID(3180202)
	if got.Callsign != "KN4OQW" || got.FullName != "Clint Chance" || got.Source != idresolve.LegPhonebook {
		t.Errorf("DisplayForID over the real store = %+v, want the phonebook's row", got)
	}
	// By callsign, for the row that has no ID at all.
	if got := c.DisplayForSource("w1aw"); got.Callsign != "W1AW" || got.FullName != "ARRL HQ" {
		t.Errorf("DisplayForSource(w1aw) = %+v, want the phonebook's row", got)
	}
	// And the reverse direction falls through that same row to the table.
	if id := c.IDForCallsign("W1AW"); id != 3110000 {
		t.Errorf("IDForCallsign(W1AW) = %d, want the table's 3110000", id)
	}

	// The email never appears in anything the chain returns. There is no field for
	// it, which is the enforcement; this asserts the shape has not grown one.
	if got := c.DisplayForID(3180202); got.FullName == "clint@example.invalid" {
		t.Error("the chain returned an email address")
	}
}

// TestChainOverAnEmptyRealStore is D5 against the real store: a node whose
// phonebook nobody has touched resolves exactly what the table alone resolves.
func TestChainOverAnEmptyRealStore(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close() //nolint:errcheck // test cleanup

	tab := table(map[uint32]string{3180202: "KN4OQW"})
	withPB := idresolve.New(phonebook.New(st), tab)
	without := idresolve.New(nil, tab)

	for _, src := range []string{"3180202", "KN4OQW", "4242424", "W4RJM", ""} {
		a, b := withPB.DisplayForSource(src), without.DisplayForSource(src)
		if a != b {
			t.Errorf("source %q: with an empty phonebook = %+v, without = %+v; "+
				"an untouched phonebook must change nothing (D5)", src, a, b)
		}
	}
}
