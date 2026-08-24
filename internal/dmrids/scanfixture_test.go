package dmrids

import (
	"os"
	"strings"
	"testing"
)

// The accessibility scan's fixture ID table has to answer the scan's own
// queries, and nothing else checks that it does.
//
// The pairing is silent when it breaks. openCallsignPicker treats an unanswered
// query as the empty case and skips the pass, so a fixture that stopped matching
// would not fail the a11y job — it would turn the populated-dropdown scans into
// skips and the gate would go green having measured a control it never opened.
// That is not hypothetical: scan.mjs carries a comment about exactly that bug,
// found only because someone noticed a skip against a node whose table
// demonstrably answered the same query over HTTP.
//
// So this asserts both halves: the fixture answers, AND the scan still asks what
// this test thinks it asks. Change a query in scan.mjs and the second half fails
// here, pointing at the file that has to change with it.
func TestA11yFixtureAnswersTheScanQueries(t *testing.T) {
	const fixture = "../../ui/a11y/DMRIds.fixture.dat"
	const scan = "../../ui/a11y/scan.mjs"

	src, err := os.ReadFile(scan)
	if err != nil {
		t.Fatalf("read %s: %v", scan, err)
	}

	// The populated states. Both must return rows, or the scan skips the only
	// markup it opens the picker for.
	//
	// KN4OQ is expected to return MORE THAN ONE row on purpose: seedPhonebook
	// imports 3180202, so the Phonebook dropdown is scanned with an "already
	// added" row beside a still-addable one. A fixture trimmed to a single
	// KN4OQW id would still pass a bare non-empty check while quietly costing
	// the scan that state.
	for _, tc := range []struct {
		query   string
		minRows int
	}{
		{"N0SZ", 1},  // the General picker
		{"KN4OQ", 2}, // the Phonebook picker
	} {
		if !strings.Contains(string(src), `"`+tc.query+`"`) {
			t.Errorf("scan.mjs no longer searches %q; the fixture and the scan have drifted", tc.query)
		}
		recs, _, err := SearchCallsigns(fixture, tc.query, 25)
		if err != nil {
			t.Fatalf("search %q: %v", tc.query, err)
		}
		if len(recs) < tc.minRows {
			t.Errorf("search %q: %d rows, want at least %d — the scan would skip this pass", tc.query, len(recs), tc.minRows)
		}
	}

	// The empty branch, which is scanned for its note rather than its rows. A
	// fixture that accidentally matched here would turn that pass into a scan of
	// a populated dropdown, measuring the wrong markup rather than none of it.
	const empty = "ZZ9ZZZ"
	if !strings.Contains(string(src), `"`+empty+`"`) {
		t.Errorf("scan.mjs no longer searches %q; the fixture and the scan have drifted", empty)
	}
	if recs, _, err := SearchCallsigns(fixture, empty, 25); err != nil {
		t.Fatalf("search %q: %v", empty, err)
	} else if len(recs) != 0 {
		t.Errorf("search %q returned %d rows; the no-match pass would scan a populated list", empty, len(recs))
	}

	// Every query the scan makes is at or above the floor the search enforces.
	// A shorter one is answered with "keep typing" and no rows at all, which
	// would read here as a fixture problem and send the next person to the wrong
	// file.
	for _, q := range []string{"N0SZ", "KN4OQ", empty} {
		if len(q) < SearchPrefixMin {
			t.Errorf("scan query %q is shorter than SearchPrefixMin (%d); it can never return rows", q, SearchPrefixMin)
		}
	}
}
