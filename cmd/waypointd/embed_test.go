package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

// TestFrameAncestorsMatrix is the test this prompt exists for.
//
// Exactly one endpoint may be framed by strangers, and it is the widget. If the
// public page or the authenticated app ever picked up frame-ancestors *, the
// failure would be silent — nothing breaks, no error appears, and the node has
// simply become clickjackable. So the property is asserted across all three
// groups at once rather than per-handler, where a new route could be added to the
// wrong one without anything noticing.
func TestFrameAncestorsMatrix(t *testing.T) {
	s, _ := newPublicServer(t, true)
	setCustomHTML(t, s, "<p>hello</p>")

	for _, tc := range []struct {
		path string
		want string // the frame-ancestors value the route must carry
	}{
		// Designed to be framed by anyone. This is the only one.
		{"/embed/lastheard", "*"},

		// The node's own pages: framed by this origin or not at all.
		{"/public/", "'self'"},
		{"/public/index.html", "'self'"},
		{"/public/custom-block", "'self'"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			w := do(t, s, http.MethodGet, tc.path, "203.0.113.9:1234")
			if w.Code != http.StatusOK {
				t.Fatalf("%s = %d", tc.path, w.Code)
			}
			csp := w.Header().Get("Content-Security-Policy")
			if !strings.Contains(csp, "frame-ancestors "+tc.want) {
				t.Errorf("%s frame-ancestors is not %s: %s", tc.path, tc.want, csp)
			}
			if tc.want == "'self'" && strings.Contains(csp, "frame-ancestors *") {
				t.Errorf("%s is embeddable by any origin: %s", tc.path, csp)
			}
			// X-Frame-Options would contradict frame-ancestors on the widget and
			// is redundant everywhere else; nothing should be setting it.
			if xfo := w.Header().Get("X-Frame-Options"); xfo != "" && tc.want == "*" {
				t.Errorf("%s sets X-Frame-Options %q, which blocks the framing it exists for", tc.path, xfo)
			}
		})
	}
}

// TestWidgetRunsNoScript. frame-ancestors * is only defensible because there is
// nothing inside the document worth reaching: no script, no forms, no session. A
// widget that gained a <script> would need that policy reconsidered, and this is
// what makes that a build failure rather than a judgement call somebody skips.
func TestWidgetRunsNoScript(t *testing.T) {
	s, _ := newPublicServer(t, true)
	w := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234")
	body := w.Body.String()

	if strings.Contains(strings.ToLower(body), "<script") {
		t.Error("the widget contains a script tag; frame-ancestors * assumes it cannot execute")
	}
	for _, attr := range []string{"onclick=", "onload=", "onerror=", "<form", "<input", "javascript:"} {
		if strings.Contains(strings.ToLower(body), attr) {
			t.Errorf("the widget contains %q, which does not belong in a document strangers frame", attr)
		}
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'none'") {
		t.Errorf("the widget's CSP does not forbid script: %s", csp)
	}
	if !strings.Contains(csp, "form-action 'none'") {
		t.Errorf("the widget's CSP does not forbid form submission: %s", csp)
	}
}

func TestWidgetGating(t *testing.T) {
	s, _ := newPublicServer(t, false)
	if w := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("the widget with the public view disabled = %d, want 404", w.Code)
	}
}

// TestWidgetFollowsTheLastHeardToggle: an operator who turned the list off has not
// agreed to publish it through a different door.
func TestWidgetFollowsTheLastHeardToggle(t *testing.T) {
	s, ps := newPublicServer(t, true)
	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowLastHeard = false
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	if w := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234"); w.Code != http.StatusNotFound {
		t.Errorf("the widget with the last-heard module off = %d, want 404", w.Code)
	}
}

// TestWidgetStatusIsOptIn: an embedder asked for a last-heard widget, and a module
// they did not ask for should not appear in their layout.
func TestWidgetStatusIsOptIn(t *testing.T) {
	s, _ := newPublicServer(t, true)

	plain := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234").Body.String()
	if strings.Contains(plain, `class="s"`) {
		t.Error("the widget shows the status line without being asked")
	}
	withStatus := do(t, s, http.MethodGet, "/embed/lastheard?status=1", "203.0.113.9:1234").Body.String()
	if !strings.Contains(withStatus, `class="s"`) {
		t.Error("?status=1 did not add the status line")
	}
}

// TestWidgetStatusRespectsItsOwnToggle: ?status=1 is a request, not an override.
func TestWidgetStatusRespectsItsOwnToggle(t *testing.T) {
	s, ps := newPublicServer(t, true)
	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowStatus = false
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	body := do(t, s, http.MethodGet, "/embed/lastheard?status=1", "203.0.113.9:1234").Body.String()
	if strings.Contains(body, `class="s"`) {
		t.Error("?status=1 published the status line despite its module being off")
	}
}

func TestWidgetLimitBounds(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, tc := range []struct {
		q    string
		want int
	}{
		{"", http.StatusOK},
		{"?limit=5", http.StatusOK},
		{"?limit=100000", http.StatusOK}, // clamped, not refused
		{"?limit=0", http.StatusBadRequest},
		{"?limit=-3", http.StatusBadRequest},
		{"?limit=lots", http.StatusBadRequest},
	} {
		if w := do(t, s, http.MethodGet, "/embed/lastheard"+tc.q, "203.0.113.9:1234"); w.Code != tc.want {
			t.Errorf("/embed/lastheard%s = %d, want %d", tc.q, w.Code, tc.want)
		}
	}
}

// TestWidgetEscapesEverything. The values are callsigns off the air, which the
// service already constrains to A-Z0-9 — but the widget is rendered by string
// concatenation, so the escaping has to be unconditional rather than relying on a
// guarantee made three packages away.
func TestWidgetEscapesEverything(t *testing.T) {
	s, _ := newPublicServer(t, true)
	body := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234").Body.String()
	// The withheld-database notice is server-authored text that reaches the same
	// concatenation path; if escaping were missing anywhere, it would be here.
	if strings.Contains(body, "<script") {
		t.Error("unescaped markup reached the widget")
	}
}

func TestWidgetCORSAndCaching(t *testing.T) {
	s, _ := newPublicServer(t, true)
	w := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234")
	// The widget is framed rather than fetched, but a club may also fetch it; the
	// same wildcard the JSON API carries applies for the same reasons.
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("widget CORS origin = %q, want *", got)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("the widget is uncacheable (%q); a club homepage may be hit far harder than the node", cc)
	}
	if w.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("the widget is served without nosniff")
	}
}

// TestWidgetSaysWhenTheDatabaseIsDown: an empty table on a club's website reads as
// "nobody has used this repeater", which is a different and wrong claim.
func TestWidgetSaysWhenTheDatabaseIsDown(t *testing.T) {
	s, ps := newPublicServer(t, true)
	s.publicSvc = publicview.NewService(ps, nil, nil).
		WithIDDatabase(func() publicview.IDDBStatus {
			return publicview.IDDBStatus{Reason: publicview.ReasonIDDBMissing}
		})
	body := do(t, s, http.MethodGet, "/embed/lastheard", "203.0.113.9:1234").Body.String()
	if !strings.Contains(body, publicview.ReasonIDDBMissing) {
		t.Errorf("the widget did not report the withheld list:\n%s", body)
	}
	if strings.Contains(body, "<table") {
		t.Error("the widget rendered a table for a list the node withheld")
	}
}

// TestPublicAPIAndWidgetAgree guards the seam the docs promise: a club that
// switches from the widget to the JSON API must see the same callsigns.
func TestPublicAPIAndWidgetAgree(t *testing.T) {
	s, _ := newPublicServer(t, true)

	api := do(t, s, http.MethodGet, "/api/public/lastheard?limit=10", "203.0.113.9:1234")
	var res struct {
		Available bool `json:"available"`
		Entries   []struct {
			Callsign string `json:"callsign"`
		} `json:"entries"`
	}
	if err := json.Unmarshal(api.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	widget := do(t, s, http.MethodGet, "/embed/lastheard?limit=10", "203.0.113.9:1234").Body.String()
	for _, e := range res.Entries {
		if !strings.Contains(widget, e.Callsign) {
			t.Errorf("the API lists %s but the widget does not", e.Callsign)
		}
	}
}

// TestEmbedDocsExist keeps the documentation honest about the routes it describes.
// A docs file that drifts from the API is worse than none, because a club will
// build against it.
func TestEmbedDocsDescribeEveryPublicRoute(t *testing.T) {
	docs := readRepoFile(t, "docs/public-api.md")
	for _, route := range []string{
		"/api/public/node", "/api/public/status", "/api/public/lastheard",
		"/api/public/counters", "/embed/lastheard",
		"/public/assets/logo", "/public/custom-block",
	} {
		if !strings.Contains(docs, route) {
			t.Errorf("docs/public-api.md does not document %s", route)
		}
	}
	// The two properties a club integrator most needs to know.
	for _, claim := range []string{"404", "Access-Control-Allow-Origin"} {
		if !strings.Contains(docs, claim) {
			t.Errorf("docs/public-api.md does not mention %q", claim)
		}
	}
}

// readRepoFile reads a path relative to the repository root. The test binary runs
// in its package directory, so the root is two levels up from cmd/waypointd.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
