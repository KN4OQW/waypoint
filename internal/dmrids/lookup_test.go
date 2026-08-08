package dmrids

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A sample in the shape of the real export (id TAB callsign TAB name), plus the
// awkward rows a hand-edited or older copy carries: space separators, comments, a
// row with no name, a malformed id, and a suffixed callsign.
const sample = "# DMRIds.dat sample\n" +
	"3180202\tKN4OQW\tClint\n" +
	"3101900\tN0SZ\tRocky\n" +
	"3101901\tN0SZ\tRocky\n" +
	"3101902\tn0sz\tRocky\n" + // lower case in the file, not just in the query
	"3021234  W1AW   ARRL   Newington CT\n" +
	"; comment\n" +
	"2081337\tG9BF\n" +
	"bogus\tN0SZ\tnot an id\n" +
	"4041234\t4X1BY/M\tAvi\n"

func writeSample(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "DMRIds.dat")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func ids(recs []Record) []uint32 {
	out := make([]uint32, len(recs))
	for i, r := range recs {
		out[i] = r.ID
	}
	return out
}

func equalIDs(got []uint32, want ...uint32) bool {
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

// The point of the whole function: one callsign, every ID behind it, in file
// order. Table.IDForCallsign can only ever answer 3101900 here, which is why this
// exists beside it.
func TestLookupCallsignReturnsEveryID(t *testing.T) {
	p := writeSample(t, sample)
	got, truncated, err := LookupCallsign(p, "N0SZ", 0)
	if err != nil {
		t.Fatal(err)
	}
	if truncated {
		t.Fatal("no limit was set, nothing should be reported truncated")
	}
	if !equalIDs(ids(got), 3101900, 3101901, 3101902) {
		t.Fatalf("ids = %v, want [3101900 3101901 3101902]", ids(got))
	}
	// The malformed "bogus N0SZ" row is skipped, not fatal, and does not appear.
	for _, r := range got {
		if r.Name != "Rocky" {
			t.Fatalf("name = %q, want Rocky", r.Name)
		}
		if r.Callsign != "N0SZ" {
			t.Fatalf("callsign = %q, want the upper-cased spelling N0SZ", r.Callsign)
		}
	}
}

func TestLookupCallsignCaseAndSpaceInsensitive(t *testing.T) {
	p := writeSample(t, sample)
	for _, q := range []string{"kn4oqw", "  KN4OQW  ", "Kn4Oqw"} {
		got, _, err := LookupCallsign(p, q, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !equalIDs(ids(got), 3180202) {
			t.Fatalf("LookupCallsign(%q) = %v, want [3180202]", q, ids(got))
		}
	}
}

// Space-separated rows still parse, and everything after the callsign collapses
// into one line rather than carrying separators into the page.
func TestLookupCallsignSpaceSeparatedRow(t *testing.T) {
	p := writeSample(t, sample)
	got, _, err := LookupCallsign(p, "W1AW", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != 3021234 {
		t.Fatalf("got %v, want one row 3021234", ids(got))
	}
	if got[0].Name != "ARRL Newington CT" {
		t.Fatalf("name = %q, want the trailing text collapsed to one line", got[0].Name)
	}
}

func TestLookupCallsignRowWithNoName(t *testing.T) {
	p := writeSample(t, sample)
	got, _, err := LookupCallsign(p, "G9BF", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "" {
		t.Fatalf("got %+v, want one row with an empty name", got)
	}
}

// A suffixed General callsign finds the base row — but only as a fallback, so a
// bare query never starts matching somebody else's /M row.
func TestLookupCallsignSuffixFallback(t *testing.T) {
	p := writeSample(t, sample)

	got, _, err := LookupCallsign(p, "KN4OQW/P", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !equalIDs(ids(got), 3180202) {
		t.Fatalf("KN4OQW/P should fall back to the base row, got %v", ids(got))
	}

	// An exact suffixed row wins over the fallback: 4X1BY/M is in the table.
	got, _, err = LookupCallsign(p, "4X1BY/M", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !equalIDs(ids(got), 4041234) {
		t.Fatalf("4X1BY/M should match its own row, got %v", ids(got))
	}

	// And the reverse must not happen: a bare 4X1BY matches nothing here.
	got, _, err = LookupCallsign(p, "4X1BY", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a bare query must not match suffixed rows, got %v", ids(got))
	}
}

func TestLookupCallsignLimitReportsTruncation(t *testing.T) {
	p := writeSample(t, sample)
	got, truncated, err := LookupCallsign(p, "N0SZ", 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("three matches with a limit of two should report truncation")
	}
	if !equalIDs(ids(got), 3101900, 3101901) {
		t.Fatalf("ids = %v, want the first two", ids(got))
	}

	// A limit that exactly covers the matches is not truncation: the scan runs to
	// the end of the file and finds nothing more.
	got, truncated, err = LookupCallsign(p, "N0SZ", 3)
	if err != nil {
		t.Fatal(err)
	}
	if truncated || len(got) != 3 {
		t.Fatalf("got %d records truncated=%v, want 3 and false", len(got), truncated)
	}
}

func TestLookupCallsignMisses(t *testing.T) {
	p := writeSample(t, sample)
	for _, q := range []string{"", "   ", "NOCALL"} {
		got, truncated, err := LookupCallsign(p, q, 0)
		if err != nil || truncated || len(got) != 0 {
			t.Fatalf("LookupCallsign(%q) = %v, %v, %v; want no records, no truncation, no error", q, got, truncated, err)
		}
	}
}

// A node that has never reached the internet has no table. That is "no
// suggestions", not a failure — same rule Load follows.
func TestLookupCallsignMissingFile(t *testing.T) {
	got, _, err := LookupCallsign("/no/such/DMRIds.dat", "KN4OQW", 0)
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing file should yield no records, got %v", got)
	}
}

// A corrupt line must not become a corrupt JSON field: the trailing text is
// capped, and the cap never splits a UTF-8 sequence.
func TestLookupCallsignCapsName(t *testing.T) {
	long := strings.Repeat("é", 200) // 2 bytes each, so the cap lands mid-rune if unguarded
	p := writeSample(t, "1\tTEST\t"+long+"\n")
	got, _, err := LookupCallsign(p, "TEST", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d records, want 1", len(got))
	}
	if n := len(got[0].Name); n > maxNameLen {
		t.Fatalf("name is %d bytes, want at most %d", n, maxNameLen)
	}
	if !isValidUTF8(got[0].Name) {
		t.Fatalf("name %q was cut mid-rune", got[0].Name)
	}
}

func isValidUTF8(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// BenchmarkLookupCallsign is the evidence for the "a linear scan is enough"
// decision in LookupCallsign's doc comment. It runs against the real export when
// one is on the machine (WAYPOINT_DMRIDS=/path/to/DMRIds.dat) and is skipped
// otherwise, because a synthetic ten-line file measures nothing.
func BenchmarkLookupCallsign(b *testing.B) {
	p := os.Getenv("WAYPOINT_DMRIDS")
	if p == "" {
		b.Skip("set WAYPOINT_DMRIDS to a real DMRIds.dat to benchmark the scan")
	}
	for i := 0; i < b.N; i++ {
		if _, _, err := LookupCallsign(p, "KN4OQW", 0); err != nil {
			b.Fatal(err)
		}
	}
}
