package dmrids

import (
	"strings"
	"testing"
)

func TestParseAndLookup(t *testing.T) {
	const data = `# DMRIds.dat sample
3180202	KN4OQW	Clint	Milton FL	United States
3021234  W1AW   ARRL   Newington CT
; comment line
bogus line with no id
99                       ; too few fields after id? actually 2 fields -> id 99 call ";" ignored below

2081337	G9BF
`
	tbl, err := Parse(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := tbl.CallsignForID(3180202); got != "KN4OQW" {
		t.Fatalf("CallsignForID(3180202) = %q, want KN4OQW", got)
	}
	if got := tbl.CallsignForID(3021234); got != "W1AW" {
		t.Fatalf("CallsignForID(3021234) = %q, want W1AW", got)
	}
	if got := tbl.IDForCallsign("kn4oqw"); got != 3180202 { // case-insensitive
		t.Fatalf("IDForCallsign(kn4oqw) = %d, want 3180202", got)
	}
	if got := tbl.IDForCallsign("G9BF"); got != 2081337 {
		t.Fatalf("IDForCallsign(G9BF) = %d, want 2081337", got)
	}
	// Misses fall back to zero/empty, never panic.
	if got := tbl.CallsignForID(1); got != "" {
		t.Fatalf("unknown id should miss, got %q", got)
	}
	if got := tbl.IDForCallsign("NOCALL"); got != 0 {
		t.Fatalf("unknown callsign should miss, got %d", got)
	}
}

func TestFirstWinsOnDuplicate(t *testing.T) {
	tbl, _ := Parse(strings.NewReader("100 AAA\n100 BBB\n"))
	if got := tbl.CallsignForID(100); got != "AAA" {
		t.Fatalf("duplicate id: first should win, got %q", got)
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	tbl, err := Load("/no/such/DMRIds.dat")
	if err != nil {
		t.Fatalf("missing file should not error, got %v", err)
	}
	if tbl.Len() != 0 {
		t.Fatalf("missing file should yield empty table, got %d", tbl.Len())
	}
}
