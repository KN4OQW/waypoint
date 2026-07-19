package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The gate serves REAL first-run screens (RFC-0009), not the old placeholder copy:
// an unclaimed device gets a claim form posting to /api/claim; a claimed but
// unauthenticated device gets a login form posting to /api/session. A guard against
// regressing these to a stub.
func TestGateServesRealScreens(t *testing.T) {
	as := newAuthStore(t)
	a := New(as, Options{FailDelay: 0})
	// The gated app never runs for a page request pre-auth — the gate answers first.
	gate := a.Gate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("gated handler reached for %s %s; the gate should have served the screen", r.Method, r.URL.Path)
	}))

	// Unclaimed: the claim screen.
	rec := httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("claim page status = %d", rec.Code)
	}
	for _, want := range []string{`<form`, `/api/claim`, "Claim this device", "Confirm password", "width=device-width", `type="password"`} {
		if !strings.Contains(body, want) {
			t.Errorf("claim screen missing %q", want)
		}
	}
	if strings.Contains(body, "ships in a later release") {
		t.Error("claim screen is still the placeholder stub")
	}

	// Claim the device, then an unauthenticated page request gets the login screen.
	rec = httptest.NewRecorder()
	a.HandleClaim(rec, jsonReq(http.MethodPost, "/api/claim", `{"username":"admin","password":"hunter2hunter"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim failed: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	gate.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body = rec.Body.String()
	if rec.Code != 200 {
		t.Fatalf("login page status = %d", rec.Code)
	}
	for _, want := range []string{`<form`, `/api/session`, "Log in", "width=device-width"} {
		if !strings.Contains(body, want) {
			t.Errorf("login screen missing %q", want)
		}
	}
	if strings.Contains(body, "Confirm password") {
		t.Error("login screen should not have a confirm-password field")
	}
	if strings.Contains(body, "ships in a later release") {
		t.Error("login screen is still the placeholder stub")
	}
}

func jsonReq(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	return r
}
