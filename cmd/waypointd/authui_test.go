package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/ui"
)

// newUIEnv wires the exact gate+mux the daemon serves, with the embedded auth page
// (auth.html) attached the way buildAuth attaches it — so these tests exercise the
// real pre-auth screen serving, not the built-in placeholder fallback.
func newUIEnv(t *testing.T) (http.Handler, *auth.Store) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	as, err := auth.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(as, auth.Options{
		Sleep:     func(time.Duration) {},
		Logf:      func(string, ...any) {},
		FailDelay: time.Millisecond,
		AuthPage:  ui.AuthPage(),
	})
	s := &server{hub: hub.New(), started: time.Now(), store: st, storePath: ":memory:", auth: a}
	return a.Gate(s.newMux()), as
}

func getRec(h http.Handler, method, path string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// The gate serves the real embedded auth page (auth.html) at the top-level route
// both while unclaimed and while claimed-without-a-session — one page for both
// states, branching client-side on the health claim flag. It is served as HTML,
// not the gate's JSON denial, so a browser hitting a fresh device sees the claim
// screen rather than a raw error.
func TestGateServesAuthPage(t *testing.T) {
	h, _ := newUIEnv(t)
	want := ui.AuthPage()

	// Pre-claim: "/" is the claim/login screen (the same auth.html).
	rec := getRec(h, "GET", "/", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pre-claim GET / = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("pre-claim GET / Content-Type = %q, want text/html", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("pre-claim GET / did not serve the embedded auth page")
	}
	// It must be the page, never the gate's JSON denial (no "mode" field, no error).
	if bytes.Contains(rec.Body.Bytes(), []byte(`"mode"`)) {
		t.Error("auth page unexpectedly looks like a gate JSON denial")
	}

	// Claim, then confirm the login screen is served pre-session too.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("POST", "/api/claim", map[string]string{"username": "kn4oqw", "password": "goodpassword"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	rec = getRec(h, "GET", "/", nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Errorf("post-claim unauth GET / = %d, want 200 serving the auth page", rec.Code)
	}
}

// /api/health carries the claim state (RFC-0002) so the pre-auth page can tell "not
// yet claimed" from "claimed, log in" without probing a gated route. It is
// unauthenticated in both states.
func TestHealthReportsClaimState(t *testing.T) {
	h, _ := newUIEnv(t)

	rec := getRec(h, "GET", "/api/health", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("health = %d, want 200", rec.Code)
	}
	var before healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &before); err != nil {
		t.Fatalf("health JSON: %v", err)
	}
	if before.Claimed {
		t.Error("fresh device reported claimed=true")
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("POST", "/api/claim", map[string]string{"username": "kn4oqw", "password": "goodpassword"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim = %d, want 201", rec.Code)
	}

	// Health is still reachable unauthenticated after claim, now reporting claimed.
	rec = getRec(h, "GET", "/api/health", nil)
	var after healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &after); err != nil {
		t.Fatalf("post-claim health JSON: %v", err)
	}
	if !after.Claimed {
		t.Error("claimed device reported claimed=false")
	}
}

// Once authenticated, the top-level route serves the real dashboard from the
// embedded UI filesystem (not the auth page): the claim's session cookie carries
// the operator straight into the app, no second login. This is the seam the auth
// page relies on when it navigates to "/" after a 201/200.
func TestAuthenticatedRootServesDashboard(t *testing.T) {
	h, _ := newUIEnv(t)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, jsonReq("POST", "/api/claim", map[string]string{"username": "kn4oqw", "password": "goodpassword"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim = %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec.Result())

	rec = getRec(h, "GET", "/", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET / = %d, want 200", rec.Code)
	}
	body := rec.Body.Bytes()
	// The dashboard, not the auth page: it carries the on-air panel and pulls in the
	// gated app scripts, neither of which the claim/login screen contains.
	if !bytes.Contains(body, []byte(`id="onair"`)) {
		t.Error("authenticated GET / did not serve the dashboard")
	}
	if bytes.Contains(body, []byte(`id="claim-form"`)) {
		t.Error("authenticated GET / served the auth page instead of the dashboard")
	}
}
