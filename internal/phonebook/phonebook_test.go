package phonebook

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// newTestStore opens an in-memory config store at head and attaches the phonebook
// to it. Going through store.Open rather than executing DDL by hand is deliberate:
// these tests then run against the same schema a node has, so a phonebook column
// that exists only in a test fixture cannot pass.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) //nolint:errcheck // test cleanup
	return New(st)
}

// fixedClock is a settable clock, so the created_at/updated_at assertions compare
// two different instants without a test that sleeps.
type fixedClock struct{ t time.Time }

func (c *fixedClock) now() time.Time { return c.t }

func TestCreateGetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	in := Entry{Callsign: "kn4oqw", DMRID: 3180202, FullName: "Clint", Email: "clint@example.invalid"}

	got, err := s.Create(in)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.ID <= 0 {
		t.Errorf("Create returned id %d, want a positive surrogate", got.ID)
	}
	// The callsign comes back as stored, not as typed. This is the whole reason
	// Create returns the row instead of an id.
	if got.Callsign != "KN4OQW" {
		t.Errorf("stored callsign = %q, want %q — writes are uppercased", got.Callsign, "KN4OQW")
	}
	if got.CreatedAt == "" || got.UpdatedAt == "" {
		t.Errorf("timestamps not stamped: created=%q updated=%q", got.CreatedAt, got.UpdatedAt)
	}

	back, err := s.Get(got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back != got {
		t.Errorf("round trip lost a field:\n got %+v\nwant %+v", back, got)
	}
}

// TestCreateOptionalFieldsOmitted is the other half of the round trip: an entry
// with nothing but a callsign. Those three columns are NULL in the table and must
// come back as the zero values a caller can use without a nil check.
func TestCreateOptionalFieldsOmitted(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Create(Entry{Callsign: "W1AW"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.DMRID != 0 || got.FullName != "" || got.Email != "" {
		t.Errorf("optional fields not empty: %+v", got)
	}
	back, err := s.Get(got.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if back != got {
		t.Errorf("NULL columns did not round-trip:\n got %+v\nwant %+v", back, got)
	}
}

func TestUpdateReplacesAndKeepsCreatedAt(t *testing.T) {
	s := newTestStore(t)
	clock := &fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	s.WithClock(clock.now)

	made, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Email: "clint@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}

	clock.t = clock.t.Add(48 * time.Hour)
	// Every field sent, and full_name deliberately omitted: Update is a
	// replacement, so this must clear it rather than keep the old one.
	got, err := s.Update(Entry{ID: made.ID, Callsign: "KN4OQW", DMRID: 3180203, Email: "new@example.invalid"})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.FullName != "" {
		t.Errorf("full_name = %q after an update that omitted it; Update replaces, it does not merge", got.FullName)
	}
	if got.DMRID != 3180203 || got.Email != "new@example.invalid" {
		t.Errorf("update did not take: %+v", got)
	}
	if got.CreatedAt != made.CreatedAt {
		t.Errorf("created_at moved from %q to %q; an update must not rewrite the row's age", made.CreatedAt, got.CreatedAt)
	}
	if got.UpdatedAt == made.UpdatedAt {
		t.Errorf("updated_at is still %q after an update two days later", got.UpdatedAt)
	}
}

func TestUpdateUnknownIDIsNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Update(Entry{ID: 4242, Callsign: "W1AW"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update on an absent row = %v, want ErrNotFound", err)
	}
	// A zero or negative id never reaches the database — there is no such row by
	// construction, and letting it through would UPDATE ... WHERE id = 0.
	if _, err := s.Update(Entry{ID: 0, Callsign: "W1AW"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update with id 0 = %v, want ErrNotFound", err)
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	made, err := s.Create(Entry{Callsign: "W1AW"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(made.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get(made.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Delete = %v, want ErrNotFound", err)
	}
	// A second click on the same delete button is a 404, not a 500.
	if err := s.Delete(made.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v, want ErrNotFound", err)
	}
}

// TestListIsOrderedByCallsign pins the ordering, because the panel renders this
// list in the order it arrives and a map-iteration ordering would make two reads
// of an unchanged phonebook look like a changed one.
func TestListIsOrderedByCallsign(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []string{"W1AW", "KN4OQW", "N0CALL"} {
		if _, err := s.Create(Entry{Callsign: c}); err != nil {
			t.Fatal(err)
		}
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range list {
		got = append(got, e.Callsign)
	}
	want := []string{"KN4OQW", "N0CALL", "W1AW"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("List order = %v, want %v", got, want)
	}
}

// TestListEmptyIsNotNil: a client rendering a list should get an empty one, not a
// type error on the first node whose phonebook nobody has filled in.
func TestListEmptyIsNotNil(t *testing.T) {
	s := newTestStore(t)
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if list == nil {
		t.Error("List returned nil on an empty phonebook, want an empty slice")
	}
}

// TestDuplicatesAreDistinguishable is the rule the panel depends on: an operator
// who typed both a callsign and a DMR ID and got "conflict" has no way to know
// which one the node already knows.
func TestDuplicatesAreDistinguishable(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202}); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		in   Entry
		want error
	}{
		{"same callsign", Entry{Callsign: "KN4OQW", DMRID: 3180999}, ErrCallsignTaken},
		// The index is NOCASE, so this collides in the database; the diagnosis must
		// survive the case difference too, since it re-queries with the normalized
		// callsign rather than the typed one.
		{"same callsign, different case", Entry{Callsign: "kn4oqw"}, ErrCallsignTaken},
		{"same DMR ID", Entry{Callsign: "W1AW", DMRID: 3180202}, ErrDMRIDTaken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Create(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("Create %+v = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

// TestUpdateDoesNotConflictWithItself: an entry keeping its own callsign while
// changing an email must not be told its callsign is taken — by itself.
func TestUpdateDoesNotConflictWithItself(t *testing.T) {
	s := newTestStore(t)
	made, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, Email: "old@example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Update(Entry{ID: made.ID, Callsign: "KN4OQW", DMRID: 3180202, Email: "new@example.invalid"})
	if err != nil {
		t.Fatalf("Update keeping its own unique values: %v", err)
	}
	if got.Email != "new@example.invalid" {
		t.Errorf("email = %q, want the updated one", got.Email)
	}
}

// TestUpdateOntoAnotherRowConflicts is the other side of it: taking a value that
// genuinely belongs to a different row is still refused, and still says which.
func TestUpdateOntoAnotherRowConflicts(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202}); err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(Entry{Callsign: "W1AW", DMRID: 3110000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(Entry{ID: other.ID, Callsign: "KN4OQW"}); !errors.Is(err, ErrCallsignTaken) {
		t.Errorf("update onto another row's callsign = %v, want ErrCallsignTaken", err)
	}
	if _, err := s.Update(Entry{ID: other.ID, Callsign: "W1AW", DMRID: 3180202}); !errors.Is(err, ErrDMRIDTaken) {
		t.Errorf("update onto another row's DMR ID = %v, want ErrDMRIDTaken", err)
	}
}

// TestManyEntriesWithoutDMRID: the unique index must not make "no DMR ID" a thing
// only one operator in the phonebook can be.
func TestManyEntriesWithoutDMRID(t *testing.T) {
	s := newTestStore(t)
	for _, c := range []string{"W1AW", "N0CALL", "G0ABC"} {
		if _, err := s.Create(Entry{Callsign: c}); err != nil {
			t.Fatalf("Create %s with no DMR ID: %v", c, err)
		}
	}
}

func TestLookupByCallsignIsCaseInsensitive(t *testing.T) {
	s := newTestStore(t)
	made, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202})
	if err != nil {
		t.Fatal(err)
	}
	for _, typed := range []string{"KN4OQW", "kn4oqw", "Kn4OqW", "  kn4oqw  "} {
		got, err := s.LookupByCallsign(typed)
		if err != nil {
			t.Errorf("LookupByCallsign(%q): %v", typed, err)
			continue
		}
		if got.ID != made.ID {
			t.Errorf("LookupByCallsign(%q) found id %d, want %d", typed, got.ID, made.ID)
		}
	}
	if _, err := s.LookupByCallsign("W1AW"); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup of an absent callsign = %v, want ErrNotFound", err)
	}
	if _, err := s.LookupByCallsign("   "); !errors.Is(err, ErrBadCallsign) {
		t.Errorf("lookup of a blank callsign = %v, want ErrBadCallsign", err)
	}
}

func TestLookupByDMRID(t *testing.T) {
	s := newTestStore(t)
	made, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(Entry{Callsign: "W1AW"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.LookupByDMRID(3180202)
	if err != nil {
		t.Fatalf("LookupByDMRID: %v", err)
	}
	if got.ID != made.ID {
		t.Errorf("LookupByDMRID found id %d, want %d", got.ID, made.ID)
	}
	if _, err := s.LookupByDMRID(9999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("lookup of an absent DMR ID = %v, want ErrNotFound", err)
	}
	// Zero is the encoding of "none recorded". It must miss rather than match the
	// W1AW row, which has no DMR ID at all.
	if _, err := s.LookupByDMRID(0); !errors.Is(err, ErrNotFound) {
		t.Errorf("LookupByDMRID(0) = %v, want ErrNotFound", err)
	}
}

func TestValidationRejects(t *testing.T) {
	s := newTestStore(t)
	for _, tc := range []struct {
		name string
		in   Entry
		want error
	}{
		{"empty callsign", Entry{Callsign: ""}, ErrBadCallsign},
		{"whitespace callsign", Entry{Callsign: "  \t "}, ErrBadCallsign},
		{"email with no @", Entry{Callsign: "W1AW", Email: "clint.example.invalid"}, ErrBadEmail},
		{"email with a space", Entry{Callsign: "W1AW", Email: "clint @example.invalid"}, ErrBadEmail},
		{"email that is really a name", Entry{Callsign: "W1AW", Email: "Clint Chance"}, ErrBadEmail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := s.Create(tc.in); !errors.Is(err, tc.want) {
				t.Errorf("Create %+v = %v, want %v", tc.in, err, tc.want)
			}
		})
	}
}

// TestValidationAccepts is the clean case beside the failure table. A check that
// fires on a good entry is worse than no check: the email rule is deliberately the
// weakest one worth having, and these are the addresses it must not refuse.
func TestValidationAccepts(t *testing.T) {
	s := newTestStore(t)
	for i, e := range []Entry{
		{Callsign: "W1AW"},
		{Callsign: "W1AW/4"},                       // portable suffix
		{Callsign: "VP2E/W1ABC"},                   // reciprocal-licence prefix
		{Callsign: "GB2RS", FullName: "RSGB News"}, // special event, no ID
		{Callsign: "N0CALL", Email: "a+tag@sub.example.invalid"},
		// Unusual but real: a plus-tag, and a bare address with no dot in the
		// domain. Both are things this check has no business refusing.
		{Callsign: "G0ABC", Email: "operator@localhost"},
	} {
		if _, err := s.Create(e); err != nil {
			t.Errorf("case %d: Create(%+v) refused a valid entry: %v", i, e, err)
		}
	}
}

func TestValidateDMRID(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		ok   bool
		want uint32
	}{
		{3180202, true, 3180202},
		{1, true, 1},
		{MaxDMRID, true, MaxDMRID},
		{0, false, 0},
		{-1, false, 0},
		{MaxDMRID + 1, false, 0},
		{5000000000, false, 0}, // the typo that a narrowing conversion would silently accept
	} {
		got, err := ValidateDMRID(tc.in)
		if tc.ok {
			if err != nil || got != tc.want {
				t.Errorf("ValidateDMRID(%d) = %d, %v; want %d, nil", tc.in, got, err, tc.want)
			}
			continue
		}
		if !errors.Is(err, ErrBadDMRID) {
			t.Errorf("ValidateDMRID(%d) = %d, %v; want ErrBadDMRID", tc.in, got, err)
		}
	}
}

// TestErrorsCarryNoFieldValues is the D4 guard on the error paths. An operator's
// email address must not be able to reach a log by way of a rejected write, and
// the only thing standing between it and one is that these sentinels are constant
// text. A future error built with %q around the offending value would fail here.
func TestErrorsCarryNoFieldValues(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, Email: "secret@example.invalid"}); err != nil {
		t.Fatal(err)
	}
	secrets := []string{"secret@example.invalid", "3180202", "KN4OQW"}
	var errs []error
	_, err := s.Create(Entry{Callsign: "KN4OQW", Email: "secret@example.invalid"})
	errs = append(errs, err)
	_, err = s.Create(Entry{Callsign: "W1AW", DMRID: 3180202})
	errs = append(errs, err)
	_, err = s.Create(Entry{Callsign: "W2XYZ", Email: "secret at example.invalid"})
	errs = append(errs, err)

	for _, e := range errs {
		if e == nil {
			t.Fatal("expected a rejection")
		}
		for _, secret := range secrets {
			if strings.Contains(e.Error(), secret) {
				t.Errorf("error %q quotes the field value %q; a rejection must not disclose what was typed", e, secret)
			}
		}
	}
}
