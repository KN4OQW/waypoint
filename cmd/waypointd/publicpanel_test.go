package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func pvReq(t *testing.T, s *server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerPublicPanelRoutes(mux)
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestPanelEndpointsAreBehindTheWall. These decide what the world can see; an
// anonymous caller reaching any of them could publish a node's location.
func TestPanelEndpointsAreBehindTheWall(t *testing.T) {
	for _, p := range []string{
		"/api/public-view/settings", "/api/public-view/suppress",
		"/api/public-view/links", "/api/public-view/links/1",
		"/api/public-view/nets", "/api/public-view/nets/1",
	} {
		if IsPublicRoute(p) {
			t.Errorf("%s is on the anonymous route list", p)
		}
	}
}

// TestPanelWriteChangesPublicAPI is the integration the runbook asks for: a panel
// write, then the public API, in one test. Anything that decouples them — a cache,
// a restart requirement, a second source of truth — shows up here.
func TestPanelWriteChangesPublicAPI(t *testing.T) {
	s, _ := newPublicServer(t, false)

	// Disabled: the public API is dark.
	if w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Fatalf("public node with the view off = %d, want 404", w.Code)
	}

	// Enable through the panel endpoint, exactly as the UI does.
	cur := pvReq(t, s, http.MethodGet, "/api/public-view/settings", "")
	if cur.Code != http.StatusOK {
		t.Fatalf("GET settings = %d", cur.Code)
	}
	var set map[string]any
	if err := json.Unmarshal(cur.Body.Bytes(), &set); err != nil {
		t.Fatal(err)
	}
	// The panel renders the tag options the server will accept, rather than a
	// list duplicated in JavaScript that can drift from the validator.
	if tags, ok := set["available_tags"].([]any); !ok || len(tags) == 0 {
		t.Error("GET settings does not tell the panel which purpose tags are valid")
	}
	if set["min_retention_hours"] == nil || set["max_retention_hours"] == nil {
		t.Error("GET settings does not tell the panel the retention bounds")
	}
	delete(set, "available_tags")
	delete(set, "min_retention_hours")
	delete(set, "max_retention_hours")
	set["enabled"] = true
	set["show_grid"] = true
	set["grid_override"] = "EM60lp"
	body, _ := json.Marshal(set)
	if w := pvReq(t, s, http.MethodPut, "/api/public-view/settings", string(body)); w.Code != http.StatusNoContent {
		t.Fatalf("PUT settings = %d: %s", w.Code, w.Body.String())
	}

	// No apply, no restart: the very next public request reflects it.
	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	if w.Code != http.StatusOK {
		t.Fatalf("public node after enabling = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "EM60lp") {
		t.Errorf("the grid set through the panel did not reach the public API: %s", w.Body.String())
	}

	// And turning a module off closes that door immediately too.
	set["show_lastheard"] = false
	body, _ = json.Marshal(set)
	if w := pvReq(t, s, http.MethodPut, "/api/public-view/settings", string(body)); w.Code != http.StatusNoContent {
		t.Fatal(w.Body.String())
	}
	if w := do(t, s, http.MethodGet, "/api/public/lastheard", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("last heard after switching its module off = %d, want 404", w.Code)
	}
}

// TestPanelValidationSurfacesAsBadRequest: the panel shows these messages to the
// operator, so they have to be 400s carrying something readable rather than 500s.
func TestPanelValidationSurfacesAsBadRequest(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, tc := range []struct{ name, method, path, body string }{
		{"javascript link", http.MethodPost, "/api/public-view/links", `{"label":"x","url":"javascript:alert(1)"}`},
		{"bad callsign", http.MethodPost, "/api/public-view/suppress", `{"callsign":"not a call"}`},
		{"net with no name", http.MethodPost, "/api/public-view/nets", `{"schedule_text":"MON 20:00"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := pvReq(t, s, tc.method, tc.path, tc.body)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", tc.name, w.Code)
			}
			if strings.TrimSpace(w.Body.String()) == "" {
				t.Error("the rejection carries no message for the operator to read")
			}
		})
	}
	// A bad grid is rejected on the settings write, not silently dropped.
	w := pvReq(t, s, http.MethodPut, "/api/public-view/settings", `{"enabled":true,"grid_override":"nonsense","retention_hours":24}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("a bad grid override = %d, want 400", w.Code)
	}
}

// TestSuppressReturnsTheNormalisedForm. The operator typed "n4abc-7"; showing
// them "N4ABC" is how they learn the SSID does not matter.
func TestSuppressReturnsTheNormalisedForm(t *testing.T) {
	s, _ := newPublicServer(t, true)
	w := pvReq(t, s, http.MethodPost, "/api/public-view/suppress", `{"callsign":"n4abc-7"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("= %d: %s", w.Code, w.Body.String())
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["callsign"] != "N4ABC" {
		t.Errorf("stored callsign reported as %q, want N4ABC", got["callsign"])
	}
	// Removable by any variant, matching how it was added.
	if w := pvReq(t, s, http.MethodDelete, "/api/public-view/suppress?callsign=N4ABC%2FM", ""); w.Code != http.StatusNoContent {
		t.Errorf("delete by a portable variant = %d, want 204", w.Code)
	}
}

func TestLinksAndNetsCRUDOverHTTP(t *testing.T) {
	s, _ := newPublicServer(t, true)

	w := pvReq(t, s, http.MethodPost, "/api/public-view/links", `{"label":"Club","url":"https://example.org","sort_order":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add link = %d: %s", w.Code, w.Body.String())
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	// An edit revalidates the URL — an https link can be edited to javascript:.
	if w := pvReq(t, s, http.MethodPut, "/api/public-view/links/"+itoa(created.ID),
		`{"label":"Club","url":"javascript:alert(1)"}`); w.Code != http.StatusBadRequest {
		t.Errorf("editing a link to a javascript: URL = %d, want 400", w.Code)
	}
	if w := pvReq(t, s, http.MethodDelete, "/api/public-view/links/"+itoa(created.ID), ""); w.Code != http.StatusNoContent {
		t.Errorf("delete link = %d", w.Code)
	}
	if w := pvReq(t, s, http.MethodDelete, "/api/public-view/links/"+itoa(created.ID), ""); w.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", w.Code)
	}

	w = pvReq(t, s, http.MethodPost, "/api/public-view/nets",
		`{"name":"Weekly Net","schedule_text":"MON 20:00","target":"TG 31123","note":"","sort_order":0}`)
	if w.Code != http.StatusOK {
		t.Fatalf("add net = %d: %s", w.Code, w.Body.String())
	}
	if w := pvReq(t, s, http.MethodGet, "/api/public-view/nets", ""); !strings.Contains(w.Body.String(), "Weekly Net") {
		t.Error("the net did not come back")
	}
	if w := pvReq(t, s, http.MethodPut, "/api/public-view/links/notanumber", `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("a non-numeric id = %d, want 400", w.Code)
	}
}

// TestPanelIsUsableWhileDisabled. An operator configures what would be shown,
// looks at it, and then decides to enable — a panel that demanded the switch
// first would make the first thing they do the irreversible-feeling one.
func TestPanelIsUsableWhileDisabled(t *testing.T) {
	s, _ := newPublicServer(t, false)
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/public-view/settings", ""},
		{http.MethodPost, "/api/public-view/links", `{"label":"Club","url":"https://example.org"}`},
		{http.MethodPost, "/api/public-view/nets", `{"name":"Net","schedule_text":"MON 20:00"}`},
		{http.MethodPost, "/api/public-view/suppress", `{"callsign":"N4ABC"}`},
	} {
		w := pvReq(t, s, tc.method, tc.path, tc.body)
		if w.Code >= 400 {
			t.Errorf("%s %s with the public view off = %d: %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	// And none of it became visible as a side effect.
	if w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("configuring the panel published the node: %d", w.Code)
	}
}

func itoa(v int64) string { return strconv.FormatInt(v, 10) }
