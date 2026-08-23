package phonebook

import (
	"testing"
)

// Provenance and the public-list refresh.
//
// The rule these tests hold up: a row the operator typed is theirs and a download
// never argues with it; a row copied from the public list tracks that list until
// the operator edits one of the three fields the list owns. Adding an email is not
// such an edit — the export has no address, so it cannot disagree.

// fakeTable is a lookup the tests control, standing in for dmrids.LookupIDs.
func fakeTable(rows map[uint32]Record) func([]uint32) (map[uint32]Record, error) {
	return func(ids []uint32) (map[uint32]Record, error) {
		out := map[uint32]Record{}
		for _, id := range ids {
			if r, ok := rows[id]; ok {
				out[id] = r
			}
		}
		return out, nil
	}
}

func TestCreateDefaultsToManual(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceManual {
		t.Errorf("a created entry has source %q, want %q", got.Source, SourceManual)
	}
	// And it is not a nullable field that reads back empty.
	back, err := s.Get(got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.Source != SourceManual {
		t.Errorf("re-read source is %q, want %q", back.Source, SourceManual)
	}
}

func TestCreateNormalizesAnUnknownSource(t *testing.T) {
	s := newTestStore(t)
	got, err := s.Create(Entry{Callsign: "W1AW", Source: "something-else"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceManual {
		t.Errorf("an unrecognised source was stored as %q, want %q", got.Source, SourceManual)
	}
}

// TestSyncUpdatesAnImportedCallsign is the case the feature exists for: RadioID
// reissues a vanity callsign against the same DMR ID, and the operator's roster
// should follow rather than sit stale until somebody notices.
func TestSyncUpdatesAnImportedCallsign(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{
		3180202: {ID: 3180202, Callsign: "W1XYZ", Name: "Clint"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 1 || res.Updated != 1 || res.Missing != 0 {
		t.Errorf("result = %+v, want 1 checked / 1 updated / 0 missing", res)
	}
	got, err := s.Get(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Callsign != "W1XYZ" {
		t.Errorf("callsign = %q, want the reissued W1XYZ", got.Callsign)
	}
	// The ID is the key it was matched on and is never rewritten, and the row is
	// still imported so it keeps tracking.
	if got.DMRID != 3180202 {
		t.Errorf("dmr_id = %d, want it untouched", got.DMRID)
	}
	if got.Source != SourceDMRIds {
		t.Errorf("source = %q, want the row still tracking", got.Source)
	}
}

// A row the operator typed is theirs. The refresh must not touch it even when the
// public table has an entry for the same ID saying something different.
func TestSyncLeavesManualEntriesAlone(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint Chance"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{
		3180202: {ID: 3180202, Callsign: "W1XYZ", Name: "Somebody"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 0 {
		t.Errorf("a manual entry was considered for sync: %+v", res)
	}
	got, _ := s.Get(e.ID)
	if got.Callsign != "KN4OQW" || got.FullName != "Clint Chance" {
		t.Errorf("a manual entry was rewritten: %+v", got)
	}
}

// An imported entry whose ID has left the export keeps what it has. Deleting
// somebody's identity because a download changed would be far worse than holding
// a stale name — and accounts key to these rows, so a delete could take a login.
func TestSyncKeepsAnEntryThatLeftTheTable(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{})) // the table no longer lists it
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 1 || res.Updated != 0 || res.Missing != 1 {
		t.Errorf("result = %+v, want 1 checked / 0 updated / 1 missing", res)
	}
	got, err := s.Get(e.ID)
	if err != nil {
		t.Fatalf("the entry was deleted: %v", err)
	}
	if got.Callsign != "KN4OQW" || got.FullName != "Clint" {
		t.Errorf("the entry was altered: %+v", got)
	}
}

// TestEditingIdentityStopsTheTracking: once the operator says what the name is, a
// download must not argue.
func TestEditingIdentityStopsTheTracking(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	e.FullName = "Clint Chance" // the operator completes the first-name-only import
	got, err := s.Update(e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceManual {
		t.Errorf("editing the name left source %q, want %q", got.Source, SourceManual)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{
		3180202: {ID: 3180202, Callsign: "KN4OQW", Name: "Clint"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 0 {
		t.Errorf("an edited entry was still being synced: %+v", res)
	}
	back, _ := s.Get(e.ID)
	if back.FullName != "Clint Chance" {
		t.Errorf("the refresh reverted the operator's name to %q", back.FullName)
	}
}

// TestAddingAnEmailKeepsTheTracking is the distinction that makes the rule worth
// having. The export carries no address, so an email is additive rather than a
// disagreement — an operator who adds contact details should not thereby stop the
// entry following a vanity callsign change.
func TestAddingAnEmailKeepsTheTracking(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	e.Email = "clint@example.org"
	got, err := s.Update(e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceDMRIds {
		t.Fatalf("adding an email demoted the row to %q; it should still track", got.Source)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{
		3180202: {ID: 3180202, Callsign: "W1XYZ", Name: "Clint"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Updated != 1 {
		t.Errorf("result = %+v, want the entry still tracking and updated", res)
	}
	back, _ := s.Get(e.ID)
	if back.Callsign != "W1XYZ" {
		t.Errorf("callsign = %q, want the reissued one", back.Callsign)
	}
	// And the address the operator added survived the rewrite. The sync touches
	// callsign and name only.
	if back.Email != "clint@example.org" {
		t.Errorf("the sync lost the operator's email: %q", back.Email)
	}
}

// Re-saving a form unchanged is not an edit. The callsign comparison folds case to
// match the column's collation, so a panel that lower-cased the display value does
// not silently stop the tracking.
func TestResavingUnchangedKeepsTheTracking(t *testing.T) {
	s := newTestStore(t)
	e, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, FullName: "Clint", Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	e.Callsign = "kn4oqw" // same callsign, different case
	got, err := s.Update(e)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != SourceDMRIds {
		t.Errorf("re-saving the same callsign in a different case demoted the row to %q", got.Source)
	}
}

// A callsign the sync would collide with is skipped, and the rest of the pass
// still runs. One unresolvable row must not stop the other forty syncing.
func TestSyncSkipsACollisionAndCarriesOn(t *testing.T) {
	s := newTestStore(t)
	// W1AW is already taken by a manual entry.
	if _, err := s.Create(Entry{Callsign: "W1AW", DMRID: 3100001}); err != nil {
		t.Fatal(err)
	}
	clash, err := s.Create(Entry{Callsign: "KN4OQW", DMRID: 3180202, Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.Create(Entry{Callsign: "K2ABC", DMRID: 3100002, Source: SourceDMRIds})
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{
		3180202: {ID: 3180202, Callsign: "W1AW", Name: "Clash"},    // would collide
		3100002: {ID: 3100002, Callsign: "K2XYZ", Name: "Renamed"}, // fine
	}))
	if err != nil {
		t.Fatalf("a collision made the whole pass fail: %v", err)
	}
	if res.Updated != 1 {
		t.Errorf("updated = %d, want 1 (the collision skipped, the other applied)", res.Updated)
	}
	if got, _ := s.Get(clash.ID); got.Callsign != "KN4OQW" {
		t.Errorf("the colliding entry was changed to %q", got.Callsign)
	}
	if got, _ := s.Get(other.ID); got.Callsign != "K2XYZ" {
		t.Errorf("the entry after the collision did not sync: %q", got.Callsign)
	}
}

// An entry with no DMR ID cannot be matched against the table, so it is not even
// considered — there is no key to look it up by.
func TestSyncIgnoresImportedEntriesWithNoID(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Create(Entry{Callsign: "KN4OQW", Source: SourceDMRIds}); err != nil {
		t.Fatal(err)
	}
	res, err := s.Sync(fakeTable(map[uint32]Record{}))
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 0 {
		t.Errorf("an entry with no DMR ID was considered: %+v", res)
	}
}
