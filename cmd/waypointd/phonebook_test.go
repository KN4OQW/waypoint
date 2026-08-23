package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/phonebook"
)

// The phonebook API. The store's own behaviour is tested in internal/phonebook;
// what is tested here is the HTTP layer over it — the session wall, the status
// codes the panel branches on, and the fact that a conflict still says which
// field conflicted by the time it reaches a client.

// newPhonebookEnv is an authenticated environment with a live phonebook attached.
// The shared auth harness leaves s.phonebook nil (nothing else needs it), so this
// fills it in over the same store the rest of the server has.
func newPhonebookEnv(t *testing.T) (*authEnv, *http.Cookie) {
	t.Helper()
	e := newAuthEnv(t, ":memory:")
	e.s.phonebook = phonebook.New(e.s.store)
	return e, e.claim(t, "kn4oqw", "goodpassword")
}

// do issues a request with the session cookie and returns the recorder. A nil
// cookie sends none, which is how the unauthenticated cases are written.
func (e *authEnv) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := jsonReq(method, path, body)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// TestPhonebookRequiresASession is the D4 gate assertion: every method on both
// routes is 401 without a session, and the denial is the gate's, not a handler
// that ran and happened to say no.
//
// It matters that this is asserted with a POPULATED phonebook. An empty one would
// answer an anonymous GET with an empty list, and a test that accepted that as a
// pass would keep passing after the surface started disclosing rows.
func TestPhonebookRequiresASession(t *testing.T) {
	e, cookie := newPhonebookEnv(t)
	made := mustCreate(t, e, cookie, map[string]any{
		"callsign": "KN4OQW", "dmr_id": 3180202,
		"full_name": "Clint", "email": "clint@example.invalid",
	})

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/phonebook", nil},
		{"POST", "/api/phonebook", map[string]any{"callsign": "W1AW"}},
		{"PUT", "/api/phonebook/" + made["id"].(string), map[string]any{"callsign": "W1AW"}},
		{"DELETE", "/api/phonebook/" + made["id"].(string), nil},
	} {
		rec := e.do(t, tc.method, tc.path, tc.body, nil)
		denied, mode := gateDenial(rec)
		if !denied || rec.Code != http.StatusUnauthorized || mode != "login" {
			t.Errorf("unauthenticated %s %s = %d mode=%q, want 401 login", tc.method, tc.path, rec.Code, mode)
		}
		// Belt and braces on the thing that actually matters: whatever the status
		// code, the response must not carry a stored entry.
		for _, secret := range []string{"clint@example.invalid", "KN4OQW", "3180202"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Errorf("unauthenticated %s %s disclosed %q", tc.method, tc.path, secret)
			}
		}
	}

	// The row is still there — the unauthenticated DELETE above was turned away,
	// not merely unreported.
	rec := e.do(t, "GET", "/api/phonebook", nil, cookie)
	if n := len(decodeList(t, rec)); n != 1 {
		t.Errorf("phonebook has %d entries after the unauthenticated sweep, want 1", n)
	}
}

// mustCreate POSTs an entry and returns the created row with its id as a string,
// ready to build a path with.
func mustCreate(t *testing.T, e *authEnv, cookie *http.Cookie, body any) map[string]any {
	t.Helper()
	rec := e.do(t, "POST", "/api/phonebook", body, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /api/phonebook = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("create response is not JSON: %v (%s)", err, rec.Body.String())
	}
	id, ok := got["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("create response carries no id: %s", rec.Body.String())
	}
	// Re-stamped as a string so callers can build "/api/phonebook/"+id without
	// each of them reaching through the float64 JSON decodes numbers as.
	got["id"] = itoa(int64(id))
	return got
}

func decodeList(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/phonebook = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Entries []map[string]any `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("list response is not JSON: %v (%s)", err, rec.Body.String())
	}
	return body.Entries
}

// TestPhonebookCRUDOverHTTP walks the surface the panel drives.
func TestPhonebookCRUDOverHTTP(t *testing.T) {
	e, cookie := newPhonebookEnv(t)

	// An empty phonebook lists as an empty array, not null — a client rendering a
	// table must not have to special-case a node nobody has filled in.
	if body := e.do(t, "GET", "/api/phonebook", nil, cookie).Body.String(); !strings.Contains(body, `"entries":[]`) {
		t.Errorf("empty list = %s, want an empty array", strings.TrimSpace(body))
	}

	made := mustCreate(t, e, cookie, map[string]any{
		"callsign": "kn4oqw", "dmr_id": 3180202,
		"full_name": "Clint", "email": "clint@example.invalid",
	})
	if made["callsign"] != "KN4OQW" {
		t.Errorf("created callsign = %v, want KN4OQW — the response is what was stored", made["callsign"])
	}
	id := made["id"].(string)

	list := decodeList(t, e.do(t, "GET", "/api/phonebook", nil, cookie))
	if len(list) != 1 || list[0]["callsign"] != "KN4OQW" {
		t.Fatalf("list after create = %+v", list)
	}
	// Email is served here and only here: an authenticated admin is the audience
	// D4 permits.
	if list[0]["email"] != "clint@example.invalid" {
		t.Errorf("authenticated list dropped the email: %+v", list[0])
	}

	rec := e.do(t, "PUT", "/api/phonebook/"+id, map[string]any{
		"callsign": "KN4OQW", "dmr_id": 3180203, "full_name": "Clint C", "email": "new@example.invalid",
	}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	var updated map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated["dmr_id"] != float64(3180203) || updated["email"] != "new@example.invalid" {
		t.Errorf("update did not take: %+v", updated)
	}

	if rec := e.do(t, "DELETE", "/api/phonebook/"+id, nil, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE = %d, want 204 (%s)", rec.Code, rec.Body.String())
	}
	if n := len(decodeList(t, e.do(t, "GET", "/api/phonebook", nil, cookie))); n != 0 {
		t.Errorf("%d entries after delete, want 0", n)
	}
	// A second delete is a 404, which is what lets the panel treat a double-click
	// as a no-op rather than as a node that broke.
	if rec := e.do(t, "DELETE", "/api/phonebook/"+id, nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rec.Code)
	}
}

// TestPhonebookConflictsNameTheirField: the panel needs to put the error next to
// the input that caused it, so the distinction the store makes has to survive the
// trip out.
func TestPhonebookConflictsNameTheirField(t *testing.T) {
	e, cookie := newPhonebookEnv(t)
	mustCreate(t, e, cookie, map[string]any{"callsign": "KN4OQW", "dmr_id": 3180202})

	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"duplicate callsign", map[string]any{"callsign": "KN4OQW"}, "callsign"},
		{"duplicate callsign, other case", map[string]any{"callsign": "kn4oqw"}, "callsign"},
		{"duplicate DMR ID", map[string]any{"callsign": "W1AW", "dmr_id": 3180202}, "dmr_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(t, "POST", "/api/phonebook", tc.body, cookie)
			if rec.Code != http.StatusConflict {
				t.Fatalf("= %d, want 409 (%s)", rec.Code, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["field"] != tc.field {
				t.Errorf("conflict names field %q, want %q — the panel cannot show the operator which input to fix", got["field"], tc.field)
			}
		})
	}
}

// TestPhonebookRejections: what the operator typed comes back as a 400 naming the
// field, not as a decoder error or a 500.
func TestPhonebookRejections(t *testing.T) {
	e, cookie := newPhonebookEnv(t)
	for _, tc := range []struct {
		name  string
		body  map[string]any
		field string
	}{
		{"no callsign", map[string]any{"callsign": ""}, "callsign"},
		{"email with no @", map[string]any{"callsign": "W1AW", "email": "clint.example.invalid"}, "email"},
		// The DMR ID cases are the reason the wire type decodes into an int64: a
		// uint32 field would answer these with a JSON decoder's overflow message
		// instead of a limit the operator can act on.
		{"DMR ID too wide", map[string]any{"callsign": "W1AW", "dmr_id": 5000000000}, "dmr_id"},
		{"DMR ID zero", map[string]any{"callsign": "W1AW", "dmr_id": 0}, "dmr_id"},
		{"DMR ID negative", map[string]any{"callsign": "W1AW", "dmr_id": -1}, "dmr_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.do(t, "POST", "/api/phonebook", tc.body, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("= %d, want 400 (%s)", rec.Code, rec.Body.String())
			}
			var got map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got["field"] != tc.field {
				t.Errorf("rejection names field %q, want %q", got["field"], tc.field)
			}
			if got["error"] == "" {
				t.Error("rejection carries no message; an error must say what to do")
			}
		})
	}

	// An unknown field is a client bug worth reporting rather than silently
	// dropping — "e-mail" would otherwise store an entry with no address.
	rec := e.do(t, "POST", "/api/phonebook", map[string]any{"callsign": "W1AW", "e-mail": "x@y.invalid"}, cookie)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
}

// TestPhonebookItemPathValidation: the id comes from the path, and a path that is
// not an id is the client's error, not a 500 from a failed scan.
func TestPhonebookItemPathValidation(t *testing.T) {
	e, cookie := newPhonebookEnv(t)
	for _, path := range []string{"/api/phonebook/", "/api/phonebook/abc", "/api/phonebook/0", "/api/phonebook/-1"} {
		if rec := e.do(t, "DELETE", path, nil, cookie); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := e.do(t, "DELETE", "/api/phonebook/4242", nil, cookie); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE of an absent id = %d, want 404", rec.Code)
	}
	// The path id wins over one in the body, so a request cannot edit one row
	// while addressing another.
	made := mustCreate(t, e, cookie, map[string]any{"callsign": "W1AW"})
	other := mustCreate(t, e, cookie, map[string]any{"callsign": "N0CALL"})
	otherID, err := strconv.ParseInt(other["id"].(string), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.do(t, "PUT", "/api/phonebook/"+made["id"].(string),
		map[string]any{"id": otherID, "callsign": "W1AW/4"}, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d (%s)", rec.Code, rec.Body.String())
	}
	list := decodeList(t, e.do(t, "GET", "/api/phonebook", nil, cookie))
	var sawEdited, sawUntouched bool
	for _, row := range list {
		switch row["callsign"] {
		case "W1AW/4":
			sawEdited = true
		case "N0CALL":
			sawUntouched = true
		}
	}
	if !sawEdited || !sawUntouched {
		t.Errorf("the body's id overrode the path's: %+v", list)
	}
}

func TestPhonebookMethodNotAllowed(t *testing.T) {
	e, cookie := newPhonebookEnv(t)
	made := mustCreate(t, e, cookie, map[string]any{"callsign": "W1AW"})
	for _, tc := range []struct{ method, path, allow string }{
		{"DELETE", "/api/phonebook", "GET, POST"},
		{"POST", "/api/phonebook/" + made["id"].(string), "PUT, DELETE"},
	} {
		rec := e.do(t, tc.method, tc.path, nil, cookie)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s %s = %d, want 405", tc.method, tc.path, rec.Code)
		}
		if got := rec.Header().Get("Allow"); got != tc.allow {
			t.Errorf("%s %s Allow = %q, want %q", tc.method, tc.path, got, tc.allow)
		}
	}
}

// TestPhonebookUnavailableWithoutAStore: a server built without the phonebook
// attached answers 503 rather than panicking on a nil pointer. Every other
// subsystem on this daemon behaves the same way, and several tests build a server
// directly.
func TestPhonebookUnavailableWithoutAStore(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	cookie := e.claim(t, "kn4oqw", "goodpassword")
	e.s.phonebook = nil
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/phonebook"},
		{"PUT", "/api/phonebook/1"},
	} {
		if rec := e.do(t, tc.method, tc.path, nil, cookie); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with no phonebook = %d, want 503", tc.method, tc.path, rec.Code)
		}
	}
}
