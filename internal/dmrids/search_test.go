package dmrids

import (
	"os"
	"strings"
	"testing"
)

// calls is the callsign column of a result, which is what the ranking is about.
func calls(recs []Record) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.Callsign
	}
	return out
}

func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// searchSample is deliberately NOT in callsign order in the file, because the
// export is ordered by ID and the whole point of the ranking is that the answer
// does not inherit that order.
const searchSample = "# sample\n" +
	"3180299\tKN4OQWX\tExtended\n" +
	"3180202\tKN4OQW\tClint\n" +
	"3180150\tKN4OQA\tAlice\n" +
	"3180203\tKN4OQW\tClint\n" + // a second ID behind the same callsign
	"3101900\tN0SZ\tRocky\n" +
	"3180175\tkn4oqb\tBob\n" + // lower case in the file
	"; comment\n" +
	"bogus\tKN4OQZ\tnot an id\n"

func TestSearchCallsignsMatchesPrefix(t *testing.T) {
	p := writeSample(t, searchSample)
	got, truncated, err := SearchCallsigns(p, "KN4OQ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("truncated with no limit set")
	}
	// Ranked: callsign ascending, then ID ascending. N0SZ does not match the
	// prefix at all, and the unparseable row is skipped rather than fatal.
	want := []string{"KN4OQA", "KN4OQB", "KN4OQW", "KN4OQW", "KN4OQWX"}
	if !equal(calls(got), want) {
		t.Errorf("callsigns = %v, want %v", calls(got), want)
	}
	if got[2].ID != 3180202 || got[3].ID != 3180203 {
		t.Errorf("the two KN4OQW rows are not ID-ascending: %d then %d", got[2].ID, got[3].ID)
	}
}

// The exact match sorting first is the property the picker depends on, and it is
// load-bearing that it needs no special case: a string sorts before every string
// that extends it.
func TestSearchCallsignsPutsTheExactMatchFirst(t *testing.T) {
	p := writeSample(t, searchSample)
	got, _, err := SearchCallsigns(p, "KN4OQW", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0].Callsign != "KN4OQW" {
		t.Fatalf("first result = %v, want KN4OQW first", calls(got))
	}
}

// A row the export spells in lower case is the same row. The file here has
// `kn4oqb`; the query is upper case and must still find it, and the result must
// carry it normalized.
func TestSearchCallsignsIsCaseInsensitive(t *testing.T) {
	p := writeSample(t, searchSample)
	got, _, err := SearchCallsigns(p, "kn4oqb", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(calls(got), []string{"KN4OQB"}) {
		t.Errorf("callsigns = %v, want [KN4OQB]", calls(got))
	}
}

// The limit truncates on RANK, not on file order. KN4OQA and KN4OQB are the two
// best-ranked matches but they are NOT the first two in the file — a scan that
// stopped at the first two matches would answer KN4OQWX and KN4OQW.
func TestSearchCallsignsLimitKeepsTheBestNotTheFirst(t *testing.T) {
	p := writeSample(t, searchSample)
	got, truncated, err := SearchCallsigns(p, "KN4OQ", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Error("truncated = false, want true: five rows matched and two were asked for")
	}
	if !equal(calls(got), []string{"KN4OQA", "KN4OQB"}) {
		t.Errorf("callsigns = %v, want the two best-ranked", calls(got))
	}
}

// Exactly as many matches as the limit is not truncation: the caller has the
// whole answer and must not be told it is a window.
func TestSearchCallsignsExactLimitIsNotTruncated(t *testing.T) {
	p := writeSample(t, searchSample)
	got, truncated, err := SearchCallsigns(p, "KN4OQ", 5)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Error("truncated = true for a result that is the complete match set")
	}
	if len(got) != 5 {
		t.Errorf("got %d rows, want 5", len(got))
	}
}

// Under the minimum the function answers nothing rather than the table's
// alphabet. The page uses SearchPrefixMin to say "keep typing"; this is the
// server refusing to be the one that decides otherwise.
func TestSearchCallsignsRefusesAShortPrefix(t *testing.T) {
	p := writeSample(t, searchSample)
	for _, q := range []string{"", " ", "K", "KN", "  KN  "} {
		got, truncated, err := SearchCallsigns(p, q, 10)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(got) != 0 || truncated {
			t.Errorf("%q returned %d rows (truncated=%v), want none", q, len(got), truncated)
		}
	}
}

// A prefix that matches nothing is an empty answer, not an error: "no callsign
// starts with that" is a legitimate thing for the page to render.
func TestSearchCallsignsMisses(t *testing.T) {
	p := writeSample(t, searchSample)
	got, truncated, err := SearchCallsigns(p, "ZZZ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || truncated {
		t.Errorf("got %d rows (truncated=%v), want none", len(got), truncated)
	}
}

// A node that has never reached the internet has no table. That is "nothing to
// suggest", not a failure — the same contract LookupCallsign and LookupIDs keep.
func TestSearchCallsignsMissingFileIsEmpty(t *testing.T) {
	got, truncated, err := SearchCallsigns("/nonexistent/DMRIds.dat", "KN4OQ", 10)
	if err != nil {
		t.Fatalf("missing file returned an error: %v", err)
	}
	if len(got) != 0 || truncated {
		t.Errorf("got %d rows (truncated=%v), want none", len(got), truncated)
	}
}

// A suffixed row is matched as it is spelled. Unlike LookupCallsign there is no
// base-call fallback and there should not be: a prefix search is what the
// operator is still typing, so KN4OQW must reach KN4OQW/P rather than the query
// being rewritten underneath them.
func TestSearchCallsignsMatchesASuffixedRowWithoutFallback(t *testing.T) {
	p := writeSample(t, "4041234\t4X1BY/M\tAvi\n4041235\t4X1BY\tAvi\n")
	got, _, err := SearchCallsigns(p, "4X1BY", 10)
	if err != nil {
		t.Fatal(err)
	}
	if !equal(calls(got), []string{"4X1BY", "4X1BY/M"}) {
		t.Errorf("callsigns = %v, want the bare row then the suffixed one", calls(got))
	}
}

// The trailing text is capped and collapsed the same way the exact lookup caps
// it: a corrupt megabyte-long line must not become a megabyte-long JSON field.
func TestSearchCallsignsCapsName(t *testing.T) {
	p := writeSample(t, "3180202\tKN4OQW\t"+strings.Repeat("x", 500)+"\n")
	got, _, err := SearchCallsigns(p, "KN4OQ", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1", len(got))
	}
	if len(got[0].Name) > maxNameLen {
		t.Errorf("name is %d bytes, want at most %d", len(got[0].Name), maxNameLen)
	}
}

// BenchmarkSearchCallsigns is the evidence for the cost claim in
// SearchCallsigns' doc comment. Like its sibling it runs only against a real
// export (WAYPOINT_DMRIDS=/path/to/DMRIds.dat) — a nine-line file measures
// nothing, and the number that matters is the one over 310k rows.
func BenchmarkSearchCallsigns(b *testing.B) {
	p := os.Getenv("WAYPOINT_DMRIDS")
	if p == "" {
		b.Skip("set WAYPOINT_DMRIDS to a real DMRIds.dat to benchmark the scan")
	}
	for i := 0; i < b.N; i++ {
		if _, _, err := SearchCallsigns(p, "KN4", 50); err != nil {
			b.Fatal(err)
		}
	}
}
