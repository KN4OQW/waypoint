package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/store"
)

// testClock is a race-safe, manually-advanced clock for the auth tests, so idle
// expiry and damper backoff are exercised without real waiting.
type testClock struct{ ns atomic.Int64 }

func (c *testClock) now() time.Time  { return time.Unix(0, c.ns.Load()).UTC() }
func (c *testClock) set(t time.Time) { c.ns.Store(t.UnixNano()) }
func (c *testClock) advance(d time.Duration) {
	c.ns.Add(int64(d))
}

// authEnv is a wired server: a real store, the auth subsystem with a test clock
// and no-op sleep, and the exact gate+mux the daemon serves.
type authEnv struct {
	s       *server
	as      *auth.Store
	handler http.Handler
	clock   *testClock
	logs    *syncBuffer
}

// syncBuffer is a concurrency-safe log sink so tests can grep what was logged.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) logf(format string, args ...any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	fmt.Fprintf(&b.buf, format+"\n", args...)
}
func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newAuthEnv(t *testing.T, storePath string) *authEnv {
	t.Helper()
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return newAuthEnvOverStore(t, st, storePath)
}

func newAuthEnvOverStore(t *testing.T, st *store.Store, storePath string) *authEnv {
	t.Helper()
	as, err := auth.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{}
	clock.set(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	logs := &syncBuffer{}
	a := auth.New(as, auth.Options{
		Now:       clock.now,
		Sleep:     func(time.Duration) {}, // no real delay in tests
		Logf:      logs.logf,
		FailDelay: time.Millisecond, // non-zero so a delay is requested; Sleep is a no-op
	})
	s := &server{hub: hub.New(), started: time.Now(), store: st, storePath: storePath, auth: a}
	return &authEnv{s: s, as: as, handler: a.Gate(s.newMux()), clock: clock, logs: logs}
}

// claim performs a claim and returns the session cookie the server issued.
func (e *authEnv) claim(t *testing.T, user, pass string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, jsonReq("POST", "/api/claim", map[string]string{"username": user, "password": pass}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim: got %d, want 201 (%s)", rec.Code, rec.Body.String())
	}
	return sessionCookie(t, rec.Result())
}

func jsonReq(method, path string, body any) *http.Request {
	var r *http.Request
	if body != nil {
		b, _ := json.Marshal(body)
		r = httptest.NewRequest(method, path, bytes.NewReader(b))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	return r
}

func sessionCookie(t *testing.T, resp *http.Response) *http.Cookie {
	t.Helper()
	for _, c := range resp.Cookies() {
		if c.Name == "waypoint_session" && c.Value != "" {
			return c
		}
	}
	t.Fatal("no session cookie issued")
	return nil
}

// route is one registered route with a representative method for the matrix tests.
type route struct {
	method, path string
}

// allRoutes mirrors newMux's registrations. The matrix tests assert every one of
// these defaults to denied unless explicitly allowlisted, so a route added to
// newMux without a matching allowlist decision fails the test.
var allRoutes = []route{
	{"GET", "/api/health"},
	{"GET", "/api/events"},
	{"GET", "/api/config"},
	{"POST", "/api/config/apply"},
	{"PUT", "/api/config/general"},
	{"GET", "/api/ysf/reflectors"},
	{"GET", "/api/p25/reflectors"},
	{"GET", "/api/nxdn/reflectors"},
	{"GET", "/api/dstar/reflectors"},
	{"GET", "/api/m17/reflectors"},
	{"GET", "/api/dmr/masters"},
	{"GET", "/api/network/status"},
	{"GET", "/api/network/wifi/scan"},
	{"GET", "/api/network/timezones"},
	{"GET", "/api/network/config"},
	{"POST", "/api/network/apply"},
	{"POST", "/api/network/confirm"},
	{"POST", "/api/network/host/apply"},
	{"GET", "/api/update/check"},
	{"POST", "/api/update/apply"},
	{"POST", "/api/claim"},
	{"POST", "/api/session"},
	{"GET", "/"},
	{"GET", "/settings.html"},
}

// gateDenial reports whether a response is the gate turning a request away (as
// opposed to a handler's own error), and the claim-state mode it named. The gate
// is the only responder that stamps a "mode" field, so it cleanly separates "the
// wall stopped you" from "the handler ran and said no" — the two can share a
// status code (a login with bad creds is a handler 401, not a gate 401).
func gateDenial(rec *httptest.ResponseRecorder) (denied bool, mode string) {
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
		return false, ""
	}
	var body map[string]any
	if json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		return false, ""
	}
	m, ok := body["mode"].(string)
	return ok, m
}

// Pre-claim matrix: on an unclaimed device only GET /api/health, POST /api/claim,
// and the top-level page are reachable; every other route is turned away by the
// gate with 403 naming claim mode. The matrix is exhaustive over the route table,
// so a newly added route defaults to denied until it is deliberately allowlisted.
func TestPreClaimRouteMatrix(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	allowed := map[route]bool{
		{"GET", "/api/health"}: true,
		{"POST", "/api/claim"}: true,
		{"GET", "/"}:           true,
	}
	for _, rt := range allRoutes {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
		denied, mode := gateDenial(rec)
		if allowed[rt] {
			if denied {
				t.Errorf("pre-claim allowlisted %s %s was gate-denied (%d)", rt.method, rt.path, rec.Code)
			}
			continue
		}
		if !denied || rec.Code != http.StatusForbidden || mode != "claim" {
			t.Errorf("pre-claim %s %s = %d mode=%q, want 403 claim", rt.method, rt.path, rec.Code, mode)
		}
	}

	// Explicit RFC checks: the SSE stream and a config route are denied pre-claim.
	for _, p := range []string{"/api/events", "/api/config"} {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, httptest.NewRequest("GET", p, nil))
		denied, mode := gateDenial(rec)
		if !denied || rec.Code != http.StatusForbidden || mode != "claim" {
			t.Errorf("pre-claim GET %s = %d mode=%q, want 403 claim", p, rec.Code, mode)
		}
	}
}

// Post-claim matrix: unauthenticated callers get 401 everywhere except the
// allowlist (health, POST /api/session, the login page); a valid session unlocks
// the rest. The SSE stream is explicitly behind auth.
func TestPostClaimRouteMatrix(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	cookie := e.claim(t, "kn4oqw", "goodpassword")

	// Once claimed, the pre-auth allowlist is health, POST /api/session, and the
	// login page. Everything else — including the now-unserved claim route — is
	// turned away by the gate with 401 naming login mode.
	allowedUnauth := map[route]bool{
		{"GET", "/api/health"}:   true,
		{"POST", "/api/session"}: true,
		{"GET", "/"}:             true,
	}
	for _, rt := range allRoutes {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, httptest.NewRequest(rt.method, rt.path, nil))
		denied, mode := gateDenial(rec)
		if allowedUnauth[rt] {
			if denied {
				t.Errorf("post-claim allowlisted %s %s was gate-denied (%d)", rt.method, rt.path, rec.Code)
			}
			continue
		}
		if !denied || rec.Code != http.StatusUnauthorized || mode != "login" {
			t.Errorf("post-claim unauth %s %s = %d mode=%q, want 401 login", rt.method, rt.path, rec.Code, mode)
		}
	}

	// The SSE stream is explicitly behind auth: gate-denied without a session.
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, httptest.NewRequest("GET", "/api/events", nil))
	if denied, mode := gateDenial(rec); !denied || rec.Code != http.StatusUnauthorized || mode != "login" {
		t.Errorf("post-claim unauth GET /api/events = %d mode=%q, want 401 login", rec.Code, mode)
	}

	// Authenticated: a representative gated route now succeeds.
	req := httptest.NewRequest("GET", "/api/config", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("authenticated GET /api/config = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
}

// Claim validation: empty username and short passwords are rejected 400; a good
// claim is 201 and a second claim is 409.
func TestClaimValidationAndConflict(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	bad := []map[string]string{
		{"username": "", "password": "longenough1"},
		{"username": "u", "password": "short"},
	}
	for _, b := range bad {
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, jsonReq("POST", "/api/claim", b))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("claim %v = %d, want 400", b, rec.Code)
		}
	}
	cookie := e.claim(t, "kn4oqw", "goodpassword")

	// Once claimed, the claim route is no longer served: an unauthenticated re-claim
	// is turned away by the gate (401 login mode), not handled.
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, jsonReq("POST", "/api/claim", map[string]string{"username": "other", "password": "anotherpass"}))
	if denied, mode := gateDenial(rec); !denied || rec.Code != http.StatusUnauthorized || mode != "login" {
		t.Fatalf("unauth re-claim = %d mode=%q, want 401 login", rec.Code, mode)
	}

	// A re-claim that does reach the handler (authenticated) hits the already-claimed
	// guard and returns 409 — the store transaction is the real conflict check.
	req := jsonReq("POST", "/api/claim", map[string]string{"username": "other", "password": "anotherpass"})
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("authenticated re-claim = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
}

// Concurrent claims at a fresh device: exactly one 201, the rest 409, and the
// store ends with a single admin. Run under `go test -race`.
func TestConcurrentClaimHTTP(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	const n = 6
	var wg sync.WaitGroup
	codes := make([]int, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			body := map[string]string{"username": fmt.Sprintf("user%d", i), "password": "goodpassword"}
			req := jsonReq("POST", "/api/claim", body)
			rec := httptest.NewRecorder()
			<-start
			e.handler.ServeHTTP(rec, req)
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()

	created, conflicts := 0, 0
	for _, c := range codes {
		switch c {
		case http.StatusCreated:
			created++
		case http.StatusConflict:
			conflicts++
		default:
			t.Errorf("unexpected claim status %d", c)
		}
	}
	if created != 1 {
		t.Fatalf("created = %d, want exactly 1 (codes %v)", created, codes)
	}
	if conflicts != n-1 {
		t.Fatalf("conflicts = %d, want %d (codes %v)", conflicts, n-1, codes)
	}
	if claimed, _ := e.as.IsClaimed(); !claimed {
		t.Fatal("device not claimed after the race")
	}
}

// Login damping: wrong credentials are 401, and after enough failures the source
// is refused with 429; the right credentials then still work once the lockout
// clears (a success resets the counter).
func TestLoginDamping(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "kn4oqw", "goodpassword")

	login := func(pass string) int {
		req := jsonReq("POST", "/api/session", map[string]string{"username": "kn4oqw", "password": pass})
		req.RemoteAddr = "203.0.113.9:5000"
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		return rec.Code
	}

	// A handful of wrong passwords: 401 up to the threshold, then 429 lockout.
	saw429 := false
	for i := 0; i < 10; i++ {
		code := login("wrongpass")
		if code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
		if code != http.StatusUnauthorized {
			t.Fatalf("wrong login = %d, want 401 or 429", code)
		}
	}
	if !saw429 {
		t.Fatal("never locked out after repeated failures")
	}

	// From a different source the correct password logs in fine (per-source damping).
	req := jsonReq("POST", "/api/session", map[string]string{"username": "kn4oqw", "password": "goodpassword"})
	req.RemoteAddr = "198.51.100.4:5000"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("good login from fresh source = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	_ = sessionCookie(t, rec.Result())
}

// Logout revokes the session server-side: the same cookie stops authenticating.
func TestLogoutRevokes(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	cookie := e.claim(t, "kn4oqw", "goodpassword")

	// The cookie authenticates.
	if code := e.get(t, "/api/config", cookie); code != http.StatusOK {
		t.Fatalf("pre-logout GET /api/config = %d, want 200", code)
	}
	// Log out.
	req := jsonReq("DELETE", "/api/session", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", rec.Code)
	}
	// The revoked cookie no longer works.
	if code := e.get(t, "/api/config", cookie); code != http.StatusUnauthorized {
		t.Fatalf("post-logout GET /api/config = %d, want 401", code)
	}
}

func (e *authEnv) get(t *testing.T, path string, cookie *http.Cookie) int {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Code
}

// The admin password and its hash never appear in the config view JSON or in the
// logs. The admin credential lives outside the config surface entirely; this is
// the regression guard that keeps it there.
func TestSecretsNeverInViewOrLogs(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	// Also seed a config secret (a network password) to prove the view redacts it.
	if err := e.s.store.Set("networks", []config.Network{{Name: "BM", Address: "1.2.3.4", Password: "NETSECRET-xyz"}}, "test"); err != nil {
		t.Fatal(err)
	}
	const adminPass = "SuperSecretAdminPass1"
	cookie := e.claim(t, "kn4oqw", adminPass)

	req := httptest.NewRequest("GET", "/api/config", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, secret := range []string{adminPass, "NETSECRET-xyz"} {
		if strings.Contains(body, secret) {
			t.Errorf("config view leaked secret %q", secret)
		}
	}
	// The stored hash must not appear either.
	admin, _, _ := e.as.Admin()
	if strings.Contains(body, admin.Record.Hash) {
		t.Error("config view leaked the password hash")
	}

	// Nothing secret in the logs (the claim log line carries the username, not the
	// password or hash).
	logs := e.logs.String()
	for _, secret := range []string{adminPass, admin.Record.Hash} {
		if strings.Contains(logs, secret) {
			t.Errorf("logs leaked secret %q", secret)
		}
	}
}

// Hash-only at rest (RFC-0002 contract #3): the raw database file contains
// neither the admin password nor the raw session token in any recoverable form —
// only an argon2id record and a token hash. A byte grep of config.db finds nothing.
func TestNoSecretsInRawDB(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	e := newAuthEnv(t, path)
	const adminPass = "RawDbSecretPass99"
	cookie := e.claim(t, "kn4oqw", adminPass)
	rawToken := cookie.Value

	// Force any WAL content to the main file so the grep sees committed rows.
	if _, err := e.s.store.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	blob, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{adminPass, rawToken} {
		if bytes.Contains(blob, []byte(secret)) {
			t.Errorf("raw DB contains a recoverable secret %q", secret)
		}
	}
	// Sanity: the non-secret username IS present, proving the grep would have found
	// the secrets had they been stored in the clear.
	if !bytes.Contains(blob, []byte("kn4oqw")) {
		t.Error("expected the plaintext username in the DB (grep sanity check failed)")
	}
}

// Sessions survive a daemon restart (they live in the store), expire past the
// idle window, and are gone after logout.
func TestSessionSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	e := newAuthEnv(t, path)
	base := e.clock.now()
	cookie := e.claim(t, "kn4oqw", "goodpassword")

	// "Restart": close the store handle and bring a new server up over the same file.
	e.s.store.Close()
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	e2 := newAuthEnvOverStore(t, st2, path)
	e2.clock.set(base) // same wall clock as before the restart

	if e2.get(t, "/api/config", cookie) != http.StatusOK {
		t.Fatal("session did not survive the restart")
	}
	// Advance past idle expiry: the cookie stops authenticating.
	e2.clock.advance(8 * 24 * time.Hour)
	if code := e2.get(t, "/api/config", cookie); code != http.StatusUnauthorized {
		t.Fatalf("expired session GET = %d, want 401", code)
	}
}

// reset-claim (the subcommand path) returns the store to claim mode and old
// session cookies stop working after the daemon comes back up on that store.
func TestResetClaimSubcommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.db")
	e := newAuthEnv(t, path)
	base := e.clock.now()
	cookie := e.claim(t, "kn4oqw", "goodpassword")
	if e.get(t, "/api/config", cookie) != http.StatusOK {
		t.Fatal("claim did not produce a working session")
	}
	e.s.store.Close()

	// Run the subcommand against the same store file.
	if code := runResetClaim([]string{"-store", path}); code != 0 {
		t.Fatalf("reset-claim exit code = %d, want 0", code)
	}

	// Bring the daemon back up over the reset store.
	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st2.Close() })
	e2 := newAuthEnvOverStore(t, st2, path)
	e2.clock.set(base)

	if claimed, _ := e2.as.IsClaimed(); claimed {
		t.Fatal("store still claimed after reset-claim")
	}
	// Pre-claim matrix restored: config is 403 (claim mode), the old cookie is dead.
	if code := e2.get(t, "/api/config", cookie); code != http.StatusForbidden {
		t.Fatalf("post-reset GET /api/config with old cookie = %d, want 403", code)
	}
	if code := e2.get(t, "/", nil); code != http.StatusOK {
		t.Fatalf("post-reset GET / = %d, want 200 (claim page)", code)
	}
}

// The boot-partition marker reset wipes the credential, revokes sessions, deletes
// the marker, and logs loudly.
func TestResetMarker(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "kn4oqw", "goodpassword")
	_ = e.as.CreateSession("hashX", time.Now(), time.Now().Add(time.Hour))

	dir := t.TempDir()
	marker := filepath.Join(dir, "waypoint-reset")
	if err := os.WriteFile(marker, []byte("reset"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	did, err := checkResetMarker(e.as, []string{filepath.Join(dir, "absent"), marker}, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("marker present but no reset performed")
	}
	if claimed, _ := e.as.IsClaimed(); claimed {
		t.Fatal("still claimed after marker reset")
	}
	if n, _ := e.as.SessionCount(); n != 0 {
		t.Fatalf("sessions survived marker reset: %d", n)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("marker file was not deleted")
	}
	if !strings.Contains(logs.String(), "SECURITY") {
		t.Errorf("marker reset was not logged loudly:\n%s", logs.String())
	}
}

// Marker deletion failure is logged and tolerated, not looped silently: the reset
// still happens and the function returns without error.
func TestResetMarkerDeleteFailureTolerated(t *testing.T) {
	e := newAuthEnv(t, ":memory:")
	_ = e.claim(t, "kn4oqw", "goodpassword")

	// A directory standing where the marker "file" is: os.Remove of a non-empty dir
	// fails, simulating an undeletable marker (read-only boot partition, etc.).
	dir := t.TempDir()
	marker := filepath.Join(dir, "waypoint-reset")
	if err := os.Mkdir(marker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(marker, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	did, err := checkResetMarker(e.as, []string{marker}, logs.logf)
	if err != nil {
		t.Fatalf("marker reset with undeletable marker returned error: %v", err)
	}
	if !did {
		t.Fatal("reset not performed")
	}
	if claimed, _ := e.as.IsClaimed(); claimed {
		t.Fatal("still claimed after marker reset")
	}
	if !strings.Contains(logs.String(), "could NOT be deleted") {
		t.Errorf("undeletable marker not surfaced in logs:\n%s", logs.String())
	}
}
