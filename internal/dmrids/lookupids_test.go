package dmrids

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// LookupIDs is the reverse of LookupCallsign: the phonebook's refresh knows a DMR
// ID and needs the row behind it. What matters is that one pass answers a whole
// set, that an absent ID is absent rather than an error, and that a missing file
// is silence rather than a failure.

func writeTable(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "DMRIds.dat")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const sampleTable = `# a comment line
3100001	W1AW	Hiram
3100002	K2ABC	Pat
3180202	KN4OQW	Clint
3180203	KN4OQW	Clint
9990001	ZL6AREC	Club
`

func TestLookupIDsResolvesASet(t *testing.T) {
	p := writeTable(t, sampleTable)
	got, err := LookupIDs(p, []uint32{3180202, 3100001, 9990001})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("resolved %d of 3: %v", len(got), got)
	}
	if got[3180202].Callsign != "KN4OQW" || got[3180202].Name != "Clint" {
		t.Errorf("3180202 = %+v", got[3180202])
	}
	if got[3100001].Callsign != "W1AW" {
		t.Errorf("3100001 = %+v", got[3100001])
	}
}

// An ID that is not in the table is simply not in the result. The phonebook's
// refresh reads that as "leave this entry alone", and it must not be an error:
// a lapsed registration is an ordinary thing, not a failure of the download.
func TestLookupIDsOmitsWhatIsNotThere(t *testing.T) {
	p := writeTable(t, sampleTable)
	got, err := LookupIDs(p, []uint32{3180202, 4444444})
	if err != nil {
		t.Fatalf("an unlisted ID produced an error: %v", err)
	}
	if _, ok := got[4444444]; ok {
		t.Error("an ID absent from the table came back present")
	}
	if _, ok := got[3180202]; !ok {
		t.Error("the listed ID was lost alongside the unlisted one")
	}
}

// A node that has never reached the internet has no table. That is nothing to
// sync, not something wrong — the same contract Load and LookupCallsign keep.
func TestLookupIDsOnAMissingFile(t *testing.T) {
	got, err := LookupIDs(filepath.Join(t.TempDir(), "absent.dat"), []uint32{3180202})
	if err != nil {
		t.Fatalf("a missing table produced an error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a missing table produced %d records", len(got))
	}
}

func TestLookupIDsEmptySetReadsNothing(t *testing.T) {
	// No file at all: if the empty set were not short-circuited this would still
	// return nil/nil, so point it at a REAL file and assert the same, which only
	// holds if nothing was opened.
	p := writeTable(t, sampleTable)
	got, err := LookupIDs(p, nil)
	if err != nil || got != nil {
		t.Errorf("LookupIDs(_, nil) = %v, %v; want nil, nil", got, err)
	}
}

// Duplicate rows for one ID mean a hand-edited or concatenated file. First wins,
// so the answer does not depend on which copy came last.
func TestLookupIDsFirstRowWins(t *testing.T) {
	p := writeTable(t, "3180202\tKN4OQW\tClint\n3180202\tW1AW\tSomebodyElse\n")
	got, err := LookupIDs(p, []uint32{3180202})
	if err != nil {
		t.Fatal(err)
	}
	if got[3180202].Callsign != "KN4OQW" {
		t.Errorf("second row won: %+v", got[3180202])
	}
}

// A malformed line is skipped, never fatal — the tolerance Parse has always had.
func TestLookupIDsSkipsRubbish(t *testing.T) {
	p := writeTable(t, "not-a-number\tW1AW\tHiram\n\n; another comment\n3180202\tKN4OQW\tClint\n")
	got, err := LookupIDs(p, []uint32{3180202})
	if err != nil {
		t.Fatal(err)
	}
	if got[3180202].Callsign != "KN4OQW" {
		t.Errorf("a bad line above the wanted row broke the scan: %+v", got)
	}
}

// The scan stops once every wanted ID is found. Proven by putting a line that
// would panic the parser if read after the last wanted row — a 5 MB line beyond
// the scanner's buffer, which would surface as bufio.ErrTooLong.
func TestLookupIDsStopsWhenSatisfied(t *testing.T) {
	huge := strings.Repeat("x", 5*1024*1024)
	p := writeTable(t, "3180202\tKN4OQW\tClint\n"+huge+"\n")
	got, err := LookupIDs(p, []uint32{3180202})
	if err != nil {
		t.Fatalf("the scan read past the last wanted row: %v", err)
	}
	if got[3180202].Callsign != "KN4OQW" {
		t.Errorf("got %+v", got)
	}
}
