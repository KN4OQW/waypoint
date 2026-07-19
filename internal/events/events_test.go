package events

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

func memStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// at is a fixed base instant; tests offset from it so ordering is deterministic
// without touching the wall clock in a way that would make assertions flaky.
var at = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// Property 1 (round-trip fidelity): every field of a persisted event reads back
// equal, newest-first, with no type dropped.
func TestInsertHistoryRoundTrip(t *testing.T) {
	s := memStore(t)
	in := []hub.Event{
		{Time: at, Type: "mode", Mode: "DMR"},
		{Time: at.Add(1 * time.Second), Type: "rf_voice_start", Mode: "DMR", Slot: 2, Source: "KN4OQW", Dest: "TG 91"},
		{Time: at.Add(2 * time.Second), Type: "rf_voice_end", Mode: "DMR", Slot: 2, Source: "KN4OQW", Dest: "TG 91", Seconds: 3.5, BER: 0.2, RSSI: -71, Detail: "ok"},
		{Time: at.Add(3 * time.Second), Type: "link", Network: "BM_3102", Detail: "linked"},
	}
	if err := s.Insert(in); err != nil {
		t.Fatal(err)
	}

	got, err := s.History(HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(in) {
		t.Fatalf("want %d events, got %d", len(in), len(got))
	}
	// Newest-first: got[0] is the last inserted.
	for i, e := range got {
		want := in[len(in)-1-i]
		if !e.Time.Equal(want.Time) || e.Type != want.Type || e.Mode != want.Mode ||
			e.Slot != want.Slot || e.Source != want.Source || e.Dest != want.Dest ||
			e.Network != want.Network || e.Seconds != want.Seconds || e.BER != want.BER ||
			e.RSSI != want.RSSI || e.Detail != want.Detail {
			t.Errorf("row %d mismatch:\n want %+v\n  got %+v", i, want, e)
		}
	}
}

// Property 2 (durability across restart): events survive a close+reopen of the
// same file — the #68 "survives waypointd restart / host reboot" acceptance.
func TestDurabilityAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Insert([]hub.Event{{Time: at, Type: "rf_voice_end", Source: "KN4OQW"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.History(HistoryQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Source != "KN4OQW" {
		t.Fatalf("event did not survive reopen: %+v", got)
	}
}

// Property 3 (batching does not lose the tail): a partial buffer flushed on
// context-cancel (shutdown) is present on read. No event handed to Run is lost on
// a clean stop.
func TestRunFlushesTailOnCancel(t *testing.T) {
	s := memStore(t)
	h := hub.New()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	// A long flush interval and large batch so nothing flushes on its own — only the
	// shutdown flush can persist these events.
	go func() { Run(ctx, s, h, time.Hour, 1000); close(done) }()

	time.Sleep(30 * time.Millisecond) // let Run subscribe
	h.Publish(hub.Event{Time: at, Type: "mode", Mode: "DMR"})
	h.Publish(hub.Event{Time: at.Add(time.Second), Type: "rf_voice_start", Source: "KN4OQW"})
	time.Sleep(30 * time.Millisecond) // let Run drain the channel into its buffer

	cancel()
	<-done

	n, err := s.Count()
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 events flushed on shutdown, got %d", n)
	}
}

// Property 4 (retention prune correct and bounded): prune deletes exactly the
// events older than the cutoff; a 0-day window (keep forever) is the caller's job
// to skip, so Prune itself is exercised with a real cutoff here.
func TestPrune(t *testing.T) {
	s := memStore(t)
	old := at.Add(-10 * 24 * time.Hour)
	recent := at.Add(-1 * time.Hour)
	if err := s.Insert([]hub.Event{
		{Time: old, Type: "mode", Mode: "OLD"},
		{Time: recent, Type: "mode", Mode: "RECENT"},
	}); err != nil {
		t.Fatal(err)
	}

	cutoff := at.Add(-7 * 24 * time.Hour)
	n, err := s.Prune(cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 pruned, got %d", n)
	}
	got, _ := s.History(HistoryQuery{})
	if len(got) != 1 || got[0].Mode != "RECENT" {
		t.Fatalf("prune kept the wrong events: %+v", got)
	}
}

// Property 6 (endpoint filter/limit): the since boundary is inclusive-at, the
// type filter is exact, and limit caps the row count.
func TestHistoryFilters(t *testing.T) {
	s := memStore(t)
	if err := s.Insert([]hub.Event{
		{Time: at, Type: "mode", Mode: "DMR"},
		{Time: at.Add(1 * time.Second), Type: "rf_voice_start", Source: "A"},
		{Time: at.Add(2 * time.Second), Type: "rf_voice_start", Source: "B"},
		{Time: at.Add(3 * time.Second), Type: "link", Network: "BM"},
	}); err != nil {
		t.Fatal(err)
	}

	// type filter is exact.
	got, err := s.History(HistoryQuery{Type: "rf_voice_start"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("type filter: want 2, got %d", len(got))
	}

	// since is inclusive-at: an event exactly at the boundary is included.
	got, _ = s.History(HistoryQuery{Since: at.Add(2 * time.Second)})
	if len(got) != 2 {
		t.Fatalf("since boundary: want 2 (>= boundary), got %d", len(got))
	}

	// limit caps the row count (and stays newest-first).
	got, _ = s.History(HistoryQuery{Limit: 1})
	if len(got) != 1 || got[0].Type != "link" {
		t.Fatalf("limit: want 1 newest (link), got %+v", got)
	}
}

// A limit above the cap is clamped, and a zero/absent limit uses the default —
// so one request can never scan the whole retention window.
func TestHistoryLimitClamp(t *testing.T) {
	s := memStore(t)
	// Insert a couple so the query returns rows; the assertion is on the effective
	// LIMIT, which we can only observe indirectly, so assert the clamp does not error
	// and returns at most what exists.
	_ = s.Insert([]hub.Event{{Time: at, Type: "mode"}, {Time: at.Add(time.Second), Type: "mode"}})
	got, err := s.History(HistoryQuery{Limit: MaxHistoryLimit + 100_000})
	if err != nil {
		t.Fatalf("clamped limit must not error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows, got %d", len(got))
	}
}

// A store from a newer schema is refused (rollback safety), mirroring the config
// store's guard.
func TestRefusesNewerSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE meta SET schema_version = ? WHERE id = 1`, SchemaVersion+1); err != nil {
		t.Fatal(err)
	}
	s.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("opening a newer-schema events database must be refused")
	}
}
