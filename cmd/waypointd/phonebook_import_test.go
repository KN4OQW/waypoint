package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/KN4OQW/waypoint/internal/phonebook"
)

// POST /api/phonebook/import — creating an entry from the public DMRIds.dat.
//
// The claim it holds up is that provenance is the SERVER's to assert. The request
// names an ID; everything stored comes from the table. A client that could post
// the callsign and name alongside would be able to mark anything it liked as
// coming from the public list, and the refresh would then keep rewriting a row
// back to a value nobody ever published.

// withTable points the env's ID table at a fixture and returns the env.
func withTable(t *testing.T, body string) (*authEnv, *http.Cookie) {
	t.Helper()
	e := newAuthEnv(t, ":memory:")
	e.s.phonebook = phonebook.New(e.s.store)
	p := filepath.Join(t.TempDir(), "DMRIds.dat")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	e.s.dmrIDs = p
	return e, e.claim(t, "kn4oqw", "goodpassword")
}

const importTable = "3180202\tKN4OQW\tClint\n3100001\tW1AW\tHiram\n"

func TestImportCreatesFromThePublicTable(t *testing.T) {
	e, cookie := withTable(t, importTable)

	rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": 3180202}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("import = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var got phonebook.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Callsign != "KN4OQW" || got.DMRID != 3180202 || got.FullName != "Clint" {
		t.Errorf("stored %+v, want the row from the table", got)
	}
	// The provenance mark is what makes the refresh willing to touch it later.
	if got.Source != phonebook.SourceDMRIds {
		t.Errorf("source = %q, want %q", got.Source, phonebook.SourceDMRIds)
	}
	// The export has no address, so there is nothing to have set.
	if got.Email != "" {
		t.Errorf("import invented an email: %q", got.Email)
	}
}

// The two ways a lookup finds nothing are DIFFERENT errands for the operator: no
// table is fixed on this node from the Updates tab, an unlisted ID is fixed at
// radioid.net. The response distinguishes them so the panel can too.
func TestImportDistinguishesNoTableFromNotListed(t *testing.T) {
	t.Run("not listed", func(t *testing.T) {
		e, cookie := withTable(t, importTable)
		rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": 4444444}, cookie)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("= %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["reason"] != "not_listed" {
			t.Errorf("reason = %q, want \"not_listed\"", body["reason"])
		}
	})
	t.Run("no table on this node", func(t *testing.T) {
		e := newAuthEnv(t, ":memory:")
		e.s.phonebook = phonebook.New(e.s.store)
		e.s.dmrIDs = filepath.Join(t.TempDir(), "absent.dat")
		cookie := e.claim(t, "kn4oqw", "goodpassword")
		rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": 3180202}, cookie)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("= %d, want 404 (%s)", rec.Code, rec.Body.String())
		}
		var body map[string]string
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["reason"] != "no_table" {
			t.Errorf("reason = %q, want \"no_table\"", body["reason"])
		}
	})
}

// Importing the same person twice is the uniqueness conflict the panel already
// knows how to render, not a special case.
func TestImportTwiceConflicts(t *testing.T) {
	e, cookie := withTable(t, importTable)
	if rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": 3180202}, cookie); rec.Code != http.StatusCreated {
		t.Fatalf("first import = %d", rec.Code)
	}
	rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": 3180202}, cookie)
	if rec.Code != http.StatusConflict {
		t.Errorf("second import = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// TestOrdinaryWritesCannotClaimProvenance. The wire type has no source field and
// the decoder rejects unknown ones, so a client cannot mark a row as coming from
// the public list through the normal create or update path. This is asserted
// rather than left to inspection because it is the whole guarantee: if it were
// possible, the refresh could be pointed at a row nobody published.
func TestOrdinaryWritesCannotClaimProvenance(t *testing.T) {
	e, cookie := withTable(t, importTable)

	// A create carrying "source" is refused outright by DisallowUnknownFields.
	rec := e.do(t, "POST", "/api/phonebook", map[string]any{
		"callsign": "K2ABC", "source": "dmrids",
	}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a create naming source = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}

	// And an ordinary create is manual, so nothing reaches the refresh by default.
	rec = e.do(t, "POST", "/api/phonebook", map[string]any{"callsign": "K2ABC"}, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("ordinary create = %d (%s)", rec.Code, rec.Body.String())
	}
	var made phonebook.Entry
	if err := json.Unmarshal(rec.Body.Bytes(), &made); err != nil {
		t.Fatal(err)
	}
	if made.Source != phonebook.SourceManual {
		t.Errorf("an ordinary create has source %q, want %q", made.Source, phonebook.SourceManual)
	}
}

// A DMR ID is 32 bits on the air. A value that cannot be one is a bad request,
// answered before the table is scanned for something that could never match.
func TestImportRangeChecksTheID(t *testing.T) {
	e, cookie := withTable(t, importTable)
	for _, bad := range []any{0, -1, int64(5000000000)} {
		rec := e.do(t, "POST", "/api/phonebook/import", map[string]any{"dmr_id": bad}, cookie)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("dmr_id %v = %d, want 400 (%s)", bad, rec.Code, rec.Body.String())
		}
	}
}
