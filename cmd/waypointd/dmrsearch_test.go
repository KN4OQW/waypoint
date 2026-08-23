package main

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/dmrids"
)

// searchServer is a node with a small table whose file order is deliberately not
// callsign order, so a test that passes cannot be passing by inheriting it.
func searchServer(t *testing.T) *server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "DMRIds.dat")
	if err := os.WriteFile(path, []byte(
		"3180299\tKN4OQWX\tExtended\n"+
			"3180202\tKN4OQW\tClint\n"+
			"3180150\tKN4OQA\tAlice\n"+
			"3180203\tKN4OQW\tClint\n"+
			"3101900\tN0SZ\tRocky\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &server{dmrIDs: path}
}

func search(t *testing.T, s *server, q string) (int, dmrIDLookupResponse) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.dmrIDLookup(rec, httptest.NewRequest("GET", "/api/dmr/ids?prefix="+q, nil))
	var got dmrIDLookupResponse
	if rec.Code == 200 {
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
	}
	return rec.Code, got
}

// GET /api/dmr/ids?prefix= is the type-ahead behind the callsign pickers: the
// ranked rows whose callsign starts with what has been typed so far.
func TestDMRIDSearchEndpoint(t *testing.T) {
	s := searchServer(t)
	code, got := search(t, s, "KN4OQ")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if !got.Available {
		t.Error("available = false with a table on disk")
	}
	if got.Callsign != "KN4OQ" {
		t.Errorf("callsign = %q, want the normalized prefix echoed back", got.Callsign)
	}
	var names []string
	for _, r := range got.Records {
		names = append(names, fmt.Sprintf("%s/%d", r.Callsign, r.ID))
	}
	want := "KN4OQA/3180150 KN4OQW/3180202 KN4OQW/3180203 KN4OQWX/3180299"
	if strings.Join(names, " ") != want {
		t.Errorf("records = %v\nwant     %s", names, want)
	}
	// N0SZ does not start with the prefix and must not be in the answer.
	for _, r := range got.Records {
		if r.Callsign == "N0SZ" {
			t.Error("a row that does not match the prefix was returned")
		}
	}
}

// A prefix shorter than the minimum is NOT a bad request — it is somebody two
// letters into typing. The answer is empty and carries the minimum, so the page
// can say "keep typing" rather than rendering "nobody matches".
func TestDMRIDSearchShortPrefixIsAnsweredNotRefused(t *testing.T) {
	s := searchServer(t)
	for _, q := range []string{"", "K", "KN"} {
		code, got := search(t, s, q)
		if code != 200 {
			t.Errorf("prefix=%q: status %d, want 200", q, code)
			continue
		}
		if len(got.Records) != 0 {
			t.Errorf("prefix=%q: %d records, want none", q, len(got.Records))
		}
		if got.MinPrefix != dmrids.SearchPrefixMin {
			t.Errorf("prefix=%q: min_prefix = %d, want %d", q, got.MinPrefix, dmrids.SearchPrefixMin)
		}
	}
}

// The minimum rides on every search answer, not only the short ones: the page
// reads it from whatever came back last, and a field that appeared only in the
// failure case would leave it with nothing to read on the path that matters.
func TestDMRIDSearchAlwaysCarriesTheMinimum(t *testing.T) {
	s := searchServer(t)
	_, got := search(t, s, "KN4OQ")
	if got.MinPrefix != dmrids.SearchPrefixMin {
		t.Errorf("min_prefix = %d, want %d", got.MinPrefix, dmrids.SearchPrefixMin)
	}
}

// The exact-callsign path is unchanged, and in particular does NOT gain the
// minimum: the two are different questions and the page tells them apart by the
// query it sent, not by the answer, so the older response shape stays as it was.
func TestDMRIDLookupStillAnswersTheExactPath(t *testing.T) {
	s := searchServer(t)
	rec := httptest.NewRecorder()
	s.dmrIDLookup(rec, httptest.NewRequest("GET", "/api/dmr/ids?callsign=KN4OQW", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got dmrIDLookupResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Records) != 2 {
		t.Errorf("got %d records, want the two IDs behind KN4OQW", len(got.Records))
	}
	if got.MinPrefix != 0 {
		t.Errorf("min_prefix = %d on the exact path, want it absent", got.MinPrefix)
	}
	// An exact lookup must not start matching the longer callsign.
	for _, r := range got.Records {
		if r.Callsign != "KN4OQW" {
			t.Errorf("exact lookup returned %q — the prefix path has leaked into it", r.Callsign)
		}
	}
}

// The window is reported, never silently applied: a picker showing fifty rows has
// to know whether that is all of them.
func TestDMRIDSearchReportsTruncation(t *testing.T) {
	var b strings.Builder
	for i := 0; i <= dmrIDSearchLimit; i++ {
		fmt.Fprintf(&b, "%d\tKN4A%03d\tOperator\n", 3180000+i, i)
	}
	path := filepath.Join(t.TempDir(), "DMRIds.dat")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &server{dmrIDs: path}
	code, got := search(t, s, "KN4A")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if len(got.Records) != dmrIDSearchLimit || !got.Truncated {
		t.Fatalf("got %d records truncated=%v, want %d and true", len(got.Records), got.Truncated, dmrIDSearchLimit)
	}
}

// No table is "nothing to suggest", which the page must be able to tell apart
// from "nobody matches what you typed" — the first is fixed on the Updates tab
// and the second at radioid.net.
func TestDMRIDSearchWithoutTable(t *testing.T) {
	s := &server{dmrIDs: filepath.Join(t.TempDir(), "nope.dat")}
	code, got := search(t, s, "KN4OQ")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	if got.Available {
		t.Error("available should be false when there is no table on disk")
	}
	if len(got.Records) != 0 {
		t.Errorf("records = %+v, want none", got.Records)
	}
}

// Input the scanner should never see. The prefix path drops the exact path's
// MINIMUM length but keeps every other bound: a 6.6 MB file is not walked for
// arbitrary input.
func TestDMRIDSearchRejectsNonCallsignPrefixes(t *testing.T) {
	s := searchServer(t)
	for _, q := range []string{"KN4OQW%20OR%201", "..%2F..%2Fetc", "%2E%2E", strings.Repeat("A", 40), "KN%2FOQ%2FP", "KN4OQW%2FPORTABLE"} {
		code, _ := search(t, s, q)
		if code != 400 {
			t.Errorf("prefix=%q: status %d, want 400", q, code)
		}
	}
}

// Records is never null on the search path either: the page renders an empty list
// and a JSON null would make it check for two different empties.
func TestDMRIDSearchNeverServesNullRecords(t *testing.T) {
	s := searchServer(t)
	rec := httptest.NewRecorder()
	s.dmrIDLookup(rec, httptest.NewRequest("GET", "/api/dmr/ids?prefix=ZZZ", nil))
	if body := rec.Body.String(); strings.Contains(body, `"records":null`) {
		t.Errorf("records serialized as null: %s", body)
	}
}
