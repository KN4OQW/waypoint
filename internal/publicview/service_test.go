package publicview

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// fixedNow is the instant every window calculation in this file is relative to.
// A fixed clock is what lets the window-edge cases below be exact rather than
// "close enough", which is the only way to test a boundary.
var fixedNow = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// fakeHistory serves synthetic events. It records the query it was asked, so the
// tests can assert the service bounded the read rather than filtering a full scan
// after the fact — the difference matters on a node with a year of history.
type fakeHistory struct {
	events []hub.Event
	got    events.HistoryQuery
	err    error
}

func (f *fakeHistory) History(q events.HistoryQuery) ([]hub.Event, error) {
	f.got = q
	if f.err != nil {
		return nil, f.err
	}
	out := []hub.Event{}
	for _, e := range f.events {
		if q.Type != "" && e.Type != q.Type {
			continue
		}
		if e.Time.Before(q.Since) {
			continue
		}
		out = append(out, e)
	}
	// The real store returns newest-first (ORDER BY ts_ms DESC), and the service
	// depends on that order — Status takes the first match as the most recent, and
	// LastHeard's limit takes the first n. Sorting here rather than trusting the
	// fixture order is what makes those dependencies actually tested.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	if q.Limit > 0 && len(out) > q.Limit {
		out = out[:q.Limit]
	}
	return out, nil
}

type fakeLive struct{ tx *status.Transmission }

func (f *fakeLive) Snapshot() status.Status { return status.Status{TX: f.tx} }

// end builds a completed RF transmission that ended minutesAgo before fixedNow.
// The BER/RSSI/duration values are deliberately non-zero: the public structs must
// not carry them, and a fixture full of zeros would not prove that.
func end(minutesAgo int, source, mode string) hub.Event {
	return hub.Event{
		Time:    fixedNow.Add(-time.Duration(minutesAgo) * time.Minute),
		Type:    status.TypeRFEnd,
		Mode:    mode,
		Slot:    2,
		Source:  source,
		Dest:    "TG 31123",
		Network: "BM_3112",
		Seconds: 18.6,
		BER:     1.4,
		RSSI:    -74,
	}
}

func newService(t *testing.T, evs []hub.Event, tx *status.Transmission) (*Service, *Store, *fakeHistory) {
	t.Helper()
	ps := newTestStore(t)
	h := &fakeHistory{events: evs}
	svc := NewService(ps, h, &fakeLive{tx: tx})
	svc.now = func() time.Time { return fixedNow }
	return svc, ps, h
}

// lastHeard unwraps the entries for the tests that are about filtering and
// windowing rather than about availability. It asserts availability on the way
// through, so a test that starts failing because the list was withheld says so
// instead of reporting a mysteriously empty result.
func lastHeard(t *testing.T, svc *Service, limit int) ([]Heard, error) {
	t.Helper()
	res, err := svc.LastHeard(limit)
	if err != nil {
		return nil, err
	}
	if !res.Available {
		t.Fatalf("last-heard list withheld unexpectedly: %+v", res)
	}
	return res.Entries, nil
}

// ---------------------------------------------------------------------------
// The field audit — the server-side backstop for D2's never-public list
// ---------------------------------------------------------------------------

// TestPublicStructsCarryNothingElse is the enforcement the runbook asks for: the
// never-public list is a property of the response types, not of handler
// discipline, so it is asserted against the types themselves.
//
// Adding a field to any struct below fails this test. That is the point. If a
// field genuinely belongs on the public surface, the allow-list here is edited
// deliberately, in a diff a reviewer sees, next to the D-decision that permits it.
func TestPublicStructsCarryNothingElse(t *testing.T) {
	for _, tc := range []struct {
		v       any
		allowed []string
	}{
		{Status{}, []string{"State", "LastActivityMinutes"}},
		{Heard{}, []string{"Callsign", "Mode", "At"}},
		{Counters{}, []string{"Callsigns", "Transmissions", "WindowHours"}},
		// The wrappers carry an availability flag and a server-authored notice.
		// Notice is on the allow-list only because it is a fixed string from the
		// const block above — never a path, never an OS error, never anything
		// derived from the machine.
		{LastHeardResult{}, []string{"Available", "Notice", "Entries"}},
		{CountersResult{}, []string{"Available", "Notice", "Counters"}},
	} {
		typ := reflect.TypeOf(tc.v)
		t.Run(typ.Name(), func(t *testing.T) {
			ok := map[string]bool{}
			for _, f := range tc.allowed {
				ok[f] = true
			}
			var got []string
			for i := range typ.NumField() {
				got = append(got, typ.Field(i).Name)
			}
			for _, name := range got {
				if !ok[name] {
					t.Errorf("%s has a field %q that is not on the public allow-list.\n"+
						"If this field really may be disclosed to anonymous visitors, add it to the "+
						"allow-list in this test with the D-decision that permits it. Fields: %v",
						typ.Name(), name, got)
				}
			}
		})
	}
}

// TestNeverPublicFieldNamesAbsent states the prohibition the other way round, by
// name, so the failure message says what was leaked rather than merely that the
// shape changed.
func TestNeverPublicFieldNamesAbsent(t *testing.T) {
	banned := []string{
		"Duration", "Seconds", "BER", "RSSI", "Loss", "Errors",
		"Version", "Firmware", "IP", "Address", "Host", "Peer",
		"Lat", "Lon", "Latitude", "Longitude", "Coord",
		"Config", "Password", "Token", "Secret", "Log", "Audit",
		"Network", "Dest", "Talkgroup", "Slot",
	}
	for _, v := range []any{Status{}, Heard{}, Counters{}, LastHeardResult{}, CountersResult{}} {
		typ := reflect.TypeOf(v)
		for i := range typ.NumField() {
			name := typ.Field(i).Name
			for _, b := range banned {
				if name == b {
					t.Errorf("%s.%s is on the never-public list (D2)", typ.Name(), name)
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Source filtering — the ground-truth pass
// ---------------------------------------------------------------------------

// TestPublishableCallsign covers what MMDVMHost actually puts in Event.Source.
//
// The numeric cases are the ones that matter. DMR, P25 and NXDN carry src_info,
// which is the ID-database lookup, and that lookup falls back to the decimal ID on
// a miss — so a node with a stale database emits sources like "3112345", and
// publishing one would disclose more than the callsign it is standing in for.
func TestPublishableCallsign(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"D-Star src_callsign", "KK4WXT", "KK4WXT", true},
		{"YSF src_callsign", "ae4ghi", "AE4GHI", true},
		{"DMR src_info resolved", "W4RJM", "W4RJM", true},
		{"SSID stripped", "N4DEF-9", "N4DEF", true},
		{"portable stripped", "W1AW/4", "W1AW", true},
		{"padded", "  KI4TSA  ", "KI4TSA", true},

		{"DMR src_info unresolved", "3112345", "", false},
		{"NXDN src_info unresolved", "12345", "", false},
		{"all-stations", "ALL", "", false},
		{"all-stations lowercase", "all", "", false},
		{"empty", "", "", false},
		{"whitespace", "   ", "", false},
		{"network name leaked into source", "BrandMeister", "", false},
		{"junk", "!!!", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := publishableCallsign(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("publishableCallsign(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestUnresolvedIDsNeverReachTheList is the same rule asserted end to end, because
// that is where it would actually fail.
func TestUnresolvedIDsNeverReachTheList(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{
		end(1, "KK4WXT", "DMR"),
		end(2, "3112345", "DMR"), // ID database miss
		end(3, "ALL", "DMR"),
		end(4, "W4RJM", "DMR"),
	}, nil)

	res, err := svc.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available {
		t.Fatalf("list withheld with a healthy ID database: %+v", res)
	}
	got := res.Entries
	if len(got) != 2 {
		t.Fatalf("last heard = %+v, want only the two resolvable callsigns", got)
	}
	for _, h := range got {
		if h.Callsign == "3112345" || h.Callsign == "ALL" {
			t.Errorf("a raw ID reached the public list: %q", h.Callsign)
		}
	}

	// And they must not be counted either — a count that moves for a station the
	// list refuses to name still tells a visitor it happened.
	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 2 || c.Counters.Callsigns != 2 {
		t.Errorf("counters = %+v, want 2 transmissions from 2 callsigns", c)
	}
}

// ---------------------------------------------------------------------------
// Suppression (D8)
// ---------------------------------------------------------------------------

func TestSuppressionHidesEverySSIDVariant(t *testing.T) {
	svc, ps, _ := newService(t, []hub.Event{
		end(1, "N4ABC", "DMR"),
		end(2, "N4ABC-7", "DMR"),
		end(3, "n4abc-9", "YSF"),
		end(4, "KP4/N4ABC", "DMR"),
		end(5, "KK4WXT", "DMR"),
	}, nil)
	if _, err := ps.AddSuppressed("n4abc"); err != nil {
		t.Fatal(err)
	}

	got, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Callsign != "KK4WXT" {
		t.Fatalf("last heard = %+v, want only KK4WXT", got)
	}

	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 1 || c.Counters.Callsigns != 1 {
		t.Errorf("counters = %+v, want 1 and 1 — suppressed stations count in neither", c)
	}
}

// TestSuppressionDoesNotSuppressStatus: hiding a callsign is not the same as
// claiming the node was quiet. If the status line went idle whenever a suppressed
// operator was the only one using the node, their activity would be inferable from
// the silence.
func TestSuppressionDoesNotSuppressStatus(t *testing.T) {
	svc, ps, _ := newService(t, []hub.Event{end(3, "N4ABC", "DMR")}, nil)
	if _, err := ps.AddSuppressed("N4ABC"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastActivityMinutes == nil || *got.LastActivityMinutes != 3 {
		t.Errorf("status = %+v, want activity 3 minutes ago despite the callsign being suppressed", got)
	}
}

// ---------------------------------------------------------------------------
// Retention window (D6)
// ---------------------------------------------------------------------------

func TestWindowEdges(t *testing.T) {
	// Retention 24 h. An event exactly at the boundary is inside it; one a minute
	// past it is not.
	svc, ps, h := newService(t, []hub.Event{
		end(1, "KK4WXT", "DMR"),
		end(24*60, "W4RJM", "DMR"),   // exactly on the edge
		end(24*60+1, "N4DEF", "DMR"), // just outside
		end(48*60, "KI4TSA", "DMR"),  // well outside
	}, nil)

	set := DefaultSettings()
	set.RetentionHours = 24
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	got, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("last heard = %+v, want the two inside a 24 h window", got)
	}
	if got[0].Callsign != "KK4WXT" || got[1].Callsign != "W4RJM" {
		t.Errorf("last heard = %+v, want newest first", got)
	}

	// The window has to be a query bound, not a filter applied after reading
	// everything — otherwise a node with a year of history scans all of it to
	// serve an anonymous request.
	if want := fixedNow.Add(-24 * time.Hour); !h.got.Since.Equal(want) {
		t.Errorf("history queried Since = %v, want %v", h.got.Since, want)
	}
}

func TestRetentionChangeTakesEffectImmediately(t *testing.T) {
	svc, ps, _ := newService(t, []hub.Event{
		end(30, "KK4WXT", "DMR"),
		end(90, "W4RJM", "DMR"),
	}, nil)

	set := DefaultSettings()
	set.RetentionHours = 1
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	got, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Callsign != "KK4WXT" {
		t.Fatalf("with a 1 h window, last heard = %+v, want only KK4WXT", got)
	}

	// No restart, no cache to invalidate: the next read sees the new window.
	set.RetentionHours = 24
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	got, err = lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("after widening the window, last heard = %+v, want both", got)
	}
}

// TestRetentionIsNotDeletion states the D6 property that the admin side keeps
// everything: the service narrows its own query and never prunes.
func TestRetentionIsNotDeletion(t *testing.T) {
	all := []hub.Event{end(30, "KK4WXT", "DMR"), end(90, "W4RJM", "DMR")}
	svc, ps, h := newService(t, all, nil)
	set := DefaultSettings()
	set.RetentionHours = 1
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if _, err := lastHeard(t, svc, 0); err != nil {
		t.Fatal(err)
	}
	if len(h.events) != len(all) {
		t.Errorf("the service removed events from the store: %d left of %d", len(h.events), len(all))
	}
}

// ---------------------------------------------------------------------------
// Counters
// ---------------------------------------------------------------------------

func TestCountersCountTransmissionsNotStations(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{
		end(1, "KK4WXT", "DMR"),
		end(2, "KK4WXT-9", "DMR"), // same operator, second transmission
		end(3, "W4RJM", "DMR"),
		end(4, "N4DEF", "YSF"),
	}, nil)

	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 4 {
		t.Errorf("transmissions = %d, want 4", c.Counters.Transmissions)
	}
	if c.Counters.Callsigns != 3 {
		t.Errorf("unique callsigns = %d, want 3 — SSID variants are one operator", c.Counters.Callsigns)
	}
	if c.Counters.WindowHours != DefaultRetentionHours {
		t.Errorf("window = %d h, want %d", c.Counters.WindowHours, DefaultRetentionHours)
	}
}

// TestCountersCountEndsOnce guards the double-count a naive "all voice events"
// query would produce: the bridge emits a start and an end per transmission, and
// both carry the identity.
func TestCountersCountEndsOnce(t *testing.T) {
	start := end(1, "KK4WXT", "DMR")
	start.Type = status.TypeRFStart
	svc, _, _ := newService(t, []hub.Event{start, end(1, "KK4WXT", "DMR")}, nil)

	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 1 {
		t.Errorf("transmissions = %d, want 1 — a start and its end are one transmission", c.Counters.Transmissions)
	}
}

func TestEmptyWindowCounters(t *testing.T) {
	svc, _, _ := newService(t, nil, nil)
	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 0 || c.Counters.Callsigns != 0 {
		t.Errorf("counters on an empty window = %+v, want zeros", c)
	}
	got, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("last heard on an empty window = %+v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func TestStatusTransmitting(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{end(5, "KK4WXT", "DMR")},
		&status.Transmission{Mode: "DMR", Source: "KK4WXT", Dest: "TG 31123"})

	got, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateTransmitting {
		t.Errorf("state = %q, want %q", got.State, StateTransmitting)
	}
	// No "last activity" while live, and above all no identity: the aggregator
	// snapshot holds the callsign, the talkgroup and the network, and none of them
	// may cross into this answer.
	if got.LastActivityMinutes != nil {
		t.Errorf("transmitting status carries last-activity = %d", *got.LastActivityMinutes)
	}
}

func TestStatusIdleWithActivity(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{end(7, "KK4WXT", "DMR"), end(40, "W4RJM", "DMR")}, nil)
	got, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateIdle {
		t.Errorf("state = %q, want %q", got.State, StateIdle)
	}
	if got.LastActivityMinutes == nil || *got.LastActivityMinutes != 7 {
		t.Errorf("status = %+v, want the most recent activity, 7 minutes ago", got)
	}
}

// TestStatusIdleWithNothingInWindow: a quiet node reports idle and declines to say
// how long it has been quiet, rather than reporting a figure derived from activity
// the window no longer covers.
func TestStatusIdleWithNothingInWindow(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{end(48*60, "KK4WXT", "DMR")}, nil)
	got, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateIdle {
		t.Errorf("state = %q, want idle", got.State)
	}
	if got.LastActivityMinutes != nil {
		t.Errorf("status = %+v, want no last-activity figure at all", got)
	}
}

// TestDegradesWithoutBackends covers the node whose event store or aggregator is
// not up. An anonymous page that 500s during a restart is worse than one that says
// there is no recent activity.
func TestDegradesWithoutBackends(t *testing.T) {
	svc := NewService(newTestStore(t), nil, nil)
	svc.now = func() time.Time { return fixedNow }

	st, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if st.State != StateIdle || st.LastActivityMinutes != nil {
		t.Errorf("status without backends = %+v, want a bare idle", st)
	}
	heard, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(heard) != 0 {
		t.Errorf("last heard without backends = %+v, want empty", heard)
	}
	c, err := svc.Counters()
	if err != nil {
		t.Fatal(err)
	}
	if c.Counters.Transmissions != 0 {
		t.Errorf("counters without backends = %+v, want zeros", c)
	}
}

func TestLastHeardLimit(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{
		end(1, "KK4WXT", "DMR"), end(2, "W4RJM", "DMR"),
		end(3, "N4DEF", "DMR"), end(4, "KI4TSA", "DMR"),
	}, nil)
	got, err := lastHeard(t, svc, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Callsign != "KK4WXT" || got[1].Callsign != "W4RJM" {
		t.Errorf("limited last heard = %+v, want the two newest", got)
	}
}

// TestHeardCarriesOnlyWhatItMayCarry checks the values, not just the shape: the
// fixture events are full of duration, BER and RSSI, and none of it may survive
// the projection.
func TestHeardCarriesOnlyWhatItMayCarry(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{end(3, "KK4WXT", "DMR")}, nil)
	got, err := lastHeard(t, svc, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("last heard = %+v", got)
	}
	want := Heard{Callsign: "KK4WXT", Mode: "DMR", At: fixedNow.Add(-3 * time.Minute)}
	if got[0] != want {
		t.Errorf("heard entry = %+v, want %+v", got[0], want)
	}
}

// ---------------------------------------------------------------------------
// Station ID database failover
// ---------------------------------------------------------------------------

// brokenIDDB is a probe reporting the table as missing or corrupt.
func brokenIDDB() IDDBStatus { return IDDBStatus{Reason: ReasonIDDBMissing} }

// TestBrokenIDDatabaseWithholdsTheList is the failover rule: when callsign
// resolution cannot be trusted, the public list is withheld entirely rather than
// shortened.
//
// The fixture is the shape that makes a shortened list dangerous. Two DMR stations
// would resolve to nothing but bare IDs, and one YSF station carries its callsign
// off the air regardless. Filtering alone would leave a page confidently showing
// one station and implying the DMR repeater had been silent all day.
func TestBrokenIDDatabaseWithholdsTheList(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{
		end(1, "3112345", "DMR"), // unresolvable: the database is gone
		end(2, "3112346", "DMR"),
		end(3, "AE4GHI", "YSF"), // resolves anyway — YSF carries the callsign
	}, nil)
	svc.WithIDDatabase(brokenIDDB)

	res, err := svc.LastHeard(0)
	if err != nil {
		t.Fatalf("a broken ID database must not be an error: %v", err)
	}
	if res.Available {
		t.Error("the list was served with a broken ID database")
	}
	if len(res.Entries) != 0 {
		t.Errorf("entries = %+v, want none — a partial list is worse than no list", res.Entries)
	}
	if res.Notice != ReasonIDDBMissing {
		t.Errorf("notice = %q, want %q", res.Notice, ReasonIDDBMissing)
	}
}

// TestBrokenIDDatabaseWithholdsCounters: the counters are built from resolved
// callsigns, so they would collapse to the YSF traffic alone. "1 callsign heard
// today" on a busy DMR repeater is a worse answer than "the database is down".
func TestBrokenIDDatabaseWithholdsCounters(t *testing.T) {
	svc, ps, _ := newService(t, []hub.Event{
		end(1, "3112345", "DMR"),
		end(2, "AE4GHI", "YSF"),
	}, nil)
	svc.WithIDDatabase(brokenIDDB)

	set := DefaultSettings()
	set.RetentionHours = 12
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Counters()
	if err != nil {
		t.Fatalf("a broken ID database must not be an error: %v", err)
	}
	if res.Available {
		t.Error("counters were served with a broken ID database")
	}
	if res.Counters.Transmissions != 0 || res.Counters.Callsigns != 0 {
		t.Errorf("counters = %+v, want zeros alongside the notice", res.Counters)
	}
	if res.Notice != ReasonIDDBMissing {
		t.Errorf("notice = %q, want %q", res.Notice, ReasonIDDBMissing)
	}
	// The window is still reported: it describes the page's own policy, not the
	// data, and stays true whether or not the database is readable.
	if res.Counters.WindowHours != 12 {
		t.Errorf("window = %d h, want the configured 12 even while unavailable", res.Counters.WindowHours)
	}
}

// TestBrokenIDDatabaseLeavesStatusAlone: the status line never resolves a
// callsign, so nothing about it is less true when the table is gone. Blanking it
// would hide a working node behind an unrelated fault.
func TestBrokenIDDatabaseLeavesStatusAlone(t *testing.T) {
	svc, _, _ := newService(t, []hub.Event{end(4, "3112345", "DMR")}, nil)
	svc.WithIDDatabase(brokenIDDB)

	got, err := svc.Status()
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateIdle {
		t.Errorf("state = %q, want idle", got.State)
	}
	if got.LastActivityMinutes == nil || *got.LastActivityMinutes != 4 {
		t.Errorf("status = %+v, want activity 4 minutes ago regardless of the ID database", got)
	}
}

// TestNoticeDisclosesNothingAboutTheMachine. The notice reaches anonymous
// visitors, so it must stay a fixed sentence rather than an OS error carrying a
// filesystem path.
func TestNoticeDisclosesNothingAboutTheMachine(t *testing.T) {
	probe := DMRIDsProbe("/nonexistent/deep/path/DMRIds.dat")
	got := probe()
	if got.Available {
		t.Fatal("a missing file probed as available")
	}
	if got.Reason != ReasonIDDBMissing {
		t.Errorf("reason = %q, want the fixed notice %q", got.Reason, ReasonIDDBMissing)
	}
	for _, leak := range []string{"/nonexistent", "deep", "DMRIds.dat", "no such file"} {
		if strings.Contains(got.Reason, leak) {
			t.Errorf("the public notice leaks %q: %q", leak, got.Reason)
		}
	}
}

func TestDMRIDsProbe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DMRIds.dat")

	probe := DMRIDsProbe(path)
	if probe().Available {
		t.Error("probe reported a missing file as available")
	}

	// Present but empty: resolves nothing, so it is the same failure as missing.
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	if probe().Available {
		t.Error("probe reported an empty file as available")
	}

	// Present but only comments: parses cleanly to zero rows — still unusable.
	if err := os.WriteFile(path, []byte("# nothing here\n; nor here\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := probe(); got.Available {
		t.Errorf("probe reported a rowless file as available: %+v", got)
	}

	// A real table.
	if err := os.WriteFile(path, []byte("3112345 KN4OQW Clint\n3112346 W4RJM Bob\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := probe(); !got.Available {
		t.Errorf("probe reported a usable table as unavailable: %+v", got)
	}

	// And it notices the table going away again, rather than serving a stale
	// verdict from its cache.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if probe().Available {
		t.Error("probe kept reporting available after the table was deleted")
	}
}

// TestHealthyIDDatabaseServesTheList closes the loop: the withholding is
// conditional, not a permanent downgrade.
func TestHealthyIDDatabaseServesTheList(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "DMRIds.dat")
	if err := os.WriteFile(path, []byte("3112345 KN4OQW Clint\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc, _, _ := newService(t, []hub.Event{end(1, "KN4OQW", "DMR")}, nil)
	svc.WithIDDatabase(DMRIDsProbe(path))

	res, err := svc.LastHeard(0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Available || len(res.Entries) != 1 {
		t.Errorf("with a healthy table, result = %+v, want the one station", res)
	}
	if res.Notice != "" {
		t.Errorf("healthy result carries a notice: %q", res.Notice)
	}
}
