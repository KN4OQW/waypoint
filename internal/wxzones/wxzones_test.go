package wxzones

import (
	"regexp"
	"strings"
	"testing"
)

// The shipped table is data, and data that is wrong in one row is wrong silently
// — a malformed line is skipped by load(), so a bad capture shows up as a county
// that simply cannot be found rather than as an error. These first tests are the
// only thing standing between a bad `go run ./cmd/wxzoneseed` and that.

// A capture that half-failed (a truncated response, a changed JSON shape) would
// still parse; it would just be short. 3269 is what api.weather.gov returned on
// 2026-08-23. The bound is loose on purpose: counties really do change, and a
// test that had to be edited for every borough rename would be edited without
// being read. It is here to catch a table that lost a state, not one that gained
// a county.
func TestShippedTableIsWholeAndParses(t *testing.T) {
	if n := Count(); n < 3200 || n > 3400 {
		t.Fatalf("shipped table has %d counties, expected roughly 3269; a capture that half-failed still parses, so check cmd/wxzoneseed output", n)
	}
}

var (
	sameRe = regexp.MustCompile(`^[0-9]{6}$`)
	ugcRe  = regexp.MustCompile(`^[A-Z]{2}C[0-9]{3}$`)
)

// Every row must be well-formed in the two fields anything downstream uses. The
// SAME pattern here is deliberately the same shape config.sameCodePattern
// enforces on a hand-typed code: a picker that could offer a county the
// validator then refuses would be a bug the operator has no way to work around.
func TestEveryRowIsWellFormed(t *testing.T) {
	for _, c := range All() {
		if !sameRe.MatchString(c.SAME) {
			t.Errorf("%q: SAME %q is not six digits", c.Name, c.SAME)
		}
		if !ugcRe.MatchString(c.UGC) {
			t.Errorf("%q: UGC %q is not SSCnnn", c.Name, c.UGC)
		}
		if c.Name == "" || len(c.State) != 2 {
			t.Errorf("%q (%s): name or state missing (state %q)", c.Name, c.SAME, c.State)
		}
	}
}

// The derivation the whole table rests on: SAME is "0" + state FIPS + the UGC's
// three digits, so the last three digits of the two codes are the same county
// FIPS. cmd/wxzoneseed's doc comment records how this was measured — 3246/3269
// against NWS's published SAME list, and 132/132 county UGC/SAME pairs in live
// CAP alerts, which is the check that matters because it is the field the feed
// keys on.
//
// This test is the standing half of that: it asserts the relationship holds for
// every shipped row, so a future capture that broke it fails here rather than at
// a node that subscribes to the wrong county.
func TestSAMEAndUGCAgreeOnTheCountyCode(t *testing.T) {
	for _, c := range All() {
		if c.SAME[3:] != c.UGC[3:] {
			t.Errorf("%s (%s): SAME %s and UGC %s disagree on the county code", c.Name, c.State, c.SAME, c.UGC)
		}
		if c.SAME[0] != '0' {
			t.Errorf("%s (%s): SAME %s does not start with 0; the table holds whole counties only", c.Name, c.State, c.SAME)
		}
	}
}

// A duplicate code would mean two rows subscribe to one feed topic, and which
// name the panel showed would depend on map iteration.
func TestSAMECodesAreUnique(t *testing.T) {
	seen := map[string]string{}
	for _, c := range All() {
		if prev, dup := seen[c.SAME]; dup {
			t.Errorf("SAME %s is used by both %q and %q", c.SAME, prev, c.Name)
		}
		seen[c.SAME] = c.Name
	}
}

func TestLookupResolvesAKnownCode(t *testing.T) {
	// KN4OQW's own county, and the code config.wx uses as its worked example.
	c, ok := Lookup("012113")
	if !ok {
		t.Fatal("012113 (Santa Rosa, FL) is not in the shipped table")
	}
	if c.Name != "Santa Rosa" || c.State != "FL" || c.UGC != "FLC113" {
		t.Fatalf("012113 resolved to %+v, want Santa Rosa FL FLC113", c)
	}
	if c.Label() != "Santa Rosa, FL" {
		t.Fatalf("Label() = %q", c.Label())
	}
}

func TestLookupRejectsWhatIsNotThere(t *testing.T) {
	for _, code := range []string{"", "999999", "12113", "012113x"} {
		if _, ok := Lookup(code); ok {
			t.Errorf("Lookup(%q) resolved; it should not", code)
		}
	}
}

// Lookup trims, because the code reaching it comes from a stored configuration
// that a human may have hand-edited before the picker existed.
func TestLookupTrimsSurroundingSpace(t *testing.T) {
	if _, ok := Lookup("  012113 "); !ok {
		t.Fatal("a padded code did not resolve")
	}
}

// The search cases an operator actually types. Each names what it is protecting,
// because a ranking test that only asserts "some result came back" passes just as
// happily when the ranking is inverted.
func TestSearchFindsWhatAnOperatorTypes(t *testing.T) {
	cases := []struct {
		query string
		want  string // SAME code that must be the FIRST result
		why   string
	}{
		{"santa rosa", "012113", "the plain case: type the name, get the county"},
		{"012113", "012113", "the code itself still works, for anyone who knows it"},
		{"flc113", "012113", "so does the UGC, which is what appears in the alert"},
		{"santa rosa fl", "012113", "adding the state narrows rather than widens"},
		{"st marys", "024037", "St. Mary's, MD — punctuation nobody types the same way twice"},
		{"prince georges", "024033", "Prince George's, MD — the apostrophe again"},
		{"dekalb il", "017037", "six states have a DeKalb; the state token picks one"},
		{"baltimore city", "024510", "the independent city, not Baltimore County"},
		{"oglala", "046102", "a county renamed since the 2016 SAME list"},
	}
	for _, tc := range cases {
		got := Search(tc.query, 10)
		if len(got) == 0 {
			t.Errorf("Search(%q) found nothing — %s", tc.query, tc.why)
			continue
		}
		if got[0].SAME != tc.want {
			t.Errorf("Search(%q)[0] = %s (%s), want %s — %s", tc.query, got[0].SAME, got[0].Label(), tc.want, tc.why)
		}
	}
}

// Every token must land. This is the difference between a picker that narrows as
// you type and one that grows: "santa rosa fl" must not return New Mexico's Santa
// Rosa-adjacent counties just because "santa" matched somewhere.
func TestEveryTokenMustMatch(t *testing.T) {
	got := Search("santa rosa fl", 50)
	for _, c := range got {
		if c.State != "FL" {
			t.Errorf("Search(\"santa rosa fl\") returned %s, which is not in FL", c.Label())
		}
	}
	if len(got) == 0 {
		t.Fatal("no results at all")
	}
}

// A state abbreviation outranks the same two letters used as a name prefix.
// "fl" is Florida, and it is also the start of Floyd, Fleming, Florence and
// Fluvanna elsewhere; an operator in Florida who types their state must not get
// Floyd, Georgia first. Both still match — see the ranking comment in match() for
// why excluding the name matches would be the worse fix — so this asserts the
// order, which is the part the operator sees.
func TestStateTokenOutranksTheSameLettersInAName(t *testing.T) {
	got := Search("fl", 500)
	if len(got) < 70 {
		t.Fatalf("Search(\"fl\") returned %d results; Florida alone has 67 counties", len(got))
	}
	// Florida's 67 counties must come before anything from another state.
	for i, c := range got {
		if c.State == "FL" {
			continue
		}
		if i < 67 {
			t.Fatalf("Search(\"fl\")[%d] = %s in %s, ahead of some Florida counties", i, c.Label(), c.State)
		}
		break
	}
	// And the out-of-state name matches are still reachable rather than dropped.
	var sawOther bool
	for _, c := range got {
		if c.State != "FL" {
			sawOther = true
			break
		}
	}
	if !sawOther {
		t.Error("no name-prefix matches outside FL survived; they should rank lower, not vanish")
	}
}

// Punctuation and spacing an operator gets "wrong" must not put a county out of
// reach. The property is that both spellings return the same SET — not the same
// order, because an exact name match legitimately ranks first, and forcing the
// orders to agree would mean throwing that signal away.
//
// The DeKalb pair is the case this is really about, and it is real data: six
// states have one, and the NWS table spells it "DeKalb" in three of them and
// "De Kalb" in the other three. Before squash() existed the two queries returned
// disjoint sets of three, so an operator spelling it the way their neighbours
// across the state line do found nothing.
func TestSpellingVariantsReachTheSameCounties(t *testing.T) {
	pairs := [][2]string{
		{"dekalb", "de kalb"},
		{"st marys md", "St. Mary's, MD"},
		{"prince georges", "Prince George's"},
		{"miami dade", "Miami-Dade"},
	}
	for _, p := range pairs {
		a, b := codes(Search(p[0], 50)), codes(Search(p[1], 50))
		if len(a) == 0 {
			t.Errorf("%q found nothing", p[0])
			continue
		}
		if !sameSet(a, b) {
			t.Errorf("%q and %q reach different counties:\n  %q -> %v\n  %q -> %v", p[0], p[1], p[0], a, p[1], b)
		}
	}
}

// The DeKalb case spelled out, since it is the one that was broken: all six
// states' DeKalb counties must be reachable by either spelling.
func TestEveryDeKalbIsReachableBySpelling(t *testing.T) {
	// AL, GA and MO spell it "DeKalb"; IL, IN and TN spell it "De Kalb".
	want := []string{"001049", "013089", "017037", "018033", "029063", "047041"}
	for _, q := range []string{"dekalb", "de kalb"} {
		got := codes(Search(q, 50))
		for _, w := range want {
			if !contains(got, w) {
				c, _ := Lookup(w)
				t.Errorf("Search(%q) does not reach %s (%s); got %v", q, w, c.Label(), got)
			}
		}
	}
}

func codes(cs []County) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.SAME)
	}
	return out
}

func contains(hay []string, n string) bool {
	for _, h := range hay {
		if h == n {
			return true
		}
	}
	return false
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !contains(b, x) {
			return false
		}
	}
	return true
}

// A query nothing matches returns nothing, rather than falling through to the
// whole table. A picker that answered "asdf" with 3,269 counties would look like
// it had matched them.
func TestNoMatchReturnsNothing(t *testing.T) {
	if got := Search("qqzzxx", 10); len(got) != 0 {
		t.Fatalf("Search(\"qqzzxx\") returned %d results, want none", len(got))
	}
}

// An empty query is the picker's opening state — everything, capped.
func TestEmptyQueryReturnsTheTableCapped(t *testing.T) {
	if got := Search("   ", 25); len(got) != 25 {
		t.Fatalf("empty query returned %d, want the cap of 25", len(got))
	}
	if got := Search("", 0); len(got) != Count() {
		t.Fatalf("empty query with no cap returned %d, want all %d", len(got), Count())
	}
}

// Within one score, the list reads alphabetically. This is the property the
// original length tie-break broke: searching "fl" ties all 67 Florida counties,
// and shortest-name-first opened the list with Bay, Lee, Clay, Gulf — an order
// nobody scanning for their own county can follow.
func TestResultsOfEqualRankReadAlphabetically(t *testing.T) {
	got := Search("fl", 67)
	var fl []County
	for _, c := range got {
		if c.State == "FL" {
			fl = append(fl, c)
		}
	}
	if len(fl) < 10 {
		t.Fatalf("only %d Florida counties came back", len(fl))
	}
	for i := 1; i < len(fl); i++ {
		if fl[i-1].Name > fl[i].Name {
			t.Fatalf("out of order at %d: %q before %q", i, fl[i-1].Name, fl[i].Name)
		}
	}
	if fl[0].Name != "Alachua" {
		t.Errorf("first Florida county is %q, want Alachua", fl[0].Name)
	}
}

// An exact name match still outranks a longer name that merely contains it, now
// that the score band rather than the name length is what does it.
func TestAnExactNameBeatsALongerOneContainingIt(t *testing.T) {
	got := Search("monroe", 30)
	if len(got) == 0 {
		t.Fatal("no results")
	}
	if got[0].Name != "Monroe" {
		t.Fatalf("first result is %q (%s), want an exact \"Monroe\"", got[0].Name, got[0].State)
	}
	// "Mainland Monroe" is still reachable, just lower.
	var sawLonger bool
	for _, c := range got {
		if c.Name != "Monroe" {
			sawLonger = true
			break
		}
	}
	if !sawLonger {
		t.Error("no longer-named Monroe survived; they should rank lower, not vanish")
	}
}

// Ordering is stable and repeatable. The panel re-runs the search on every
// keystroke; a list that reshuffled between two identical queries would move the
// row under the operator's cursor as they reached for it.
func TestSearchOrderIsStable(t *testing.T) {
	first := Search("monroe", 20)
	for i := 0; i < 5; i++ {
		again := Search("monroe", 20)
		if len(again) != len(first) {
			t.Fatalf("run %d returned %d results, first run returned %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].SAME != first[j].SAME {
				t.Fatalf("run %d differs at %d: %s vs %s", i, j, again[j].SAME, first[j].SAME)
			}
		}
	}
}

// All() hands out a copy. A caller that sorted the returned slice — which is
// exactly what an API handler might do — would otherwise reorder the package's
// own storage and change what every later search returned.
func TestAllReturnsACopy(t *testing.T) {
	a := All()
	if len(a) < 2 {
		t.Fatal("table too small to test")
	}
	first := a[0]
	a[0] = a[1]
	if All()[0].SAME != first.SAME {
		t.Fatal("mutating the slice from All() changed the shipped table")
	}
}

// The limit is a cap, not a promise of that many.
func TestLimitCapsButDoesNotPad(t *testing.T) {
	if got := Search("santa rosa fl", 100); len(got) > 100 {
		t.Fatalf("limit not applied: %d", len(got))
	}
	if got := Search("012113", 5); len(got) > 5 {
		t.Fatalf("limit not applied: %d", len(got))
	}
}

func TestNormalizeFoldsPunctuationTheWayNamesUseIt(t *testing.T) {
	cases := map[string]string{
		"St. Mary's":       "st marys",
		"Prince George's":  "prince georges",
		"O'Brien":          "obrien",
		"Miami-Dade":       "miami dade",
		"  Santa   Rosa  ": "santa rosa",
		"DeKalb":           "dekalb",
	}
	for in, want := range cases {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

// Nothing in this package reaches the network, and nothing in it should acquire
// the ability to quietly. The privacy argument for shipping the table whole is in
// the package comment; this is the standing check that the argument still holds.
// It reads the package's own source rather than trusting the doc.
func TestPackageOpensNoSockets(t *testing.T) {
	src := readSource(t)
	for _, banned := range []string{`"net/http"`, `"net"`, "http.Get", "http.Client"} {
		if strings.Contains(src, banned) {
			t.Errorf("wxzones.go references %s; the county table ships whole and must not fetch (see the package comment and GOVERNANCE.md principle 2)", banned)
		}
	}
}
