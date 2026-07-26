package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// setupEnv is the daemon assembled exactly as main() assembles it: the wizard
// gate outside the auth gate outside the real route table. Testing against a
// hand-rolled subset of routes would prove nothing about the order the two gates
// actually run in.
type setupEnv struct {
	handler http.Handler
	fake    *privtest.Fake
	s       *server
	marker  string
	prog    string
	dir     string
	storeDB *store.Store
}

func newSetupEnv(t *testing.T) *setupEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return newSetupEnvOver(t, dir, st, privtest.NewFake())
}

// newSetupEnvOver rebuilds the daemon over existing state — the same files, the
// same store, the same helper — which is what a waypointd restart looks like.
func newSetupEnvOver(t *testing.T, dir string, st *store.Store, fake *privtest.Fake) *setupEnv {
	t.Helper()
	as, err := auth.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(as, auth.Options{
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
		Logf:      func(string, ...any) {},
		FailDelay: time.Millisecond,
	})
	s := &server{
		hub: hub.New(), started: time.Now(), store: st,
		storePath: filepath.Join(dir, "config.db"), auth: a,
	}
	e := &setupEnv{
		fake:    fake,
		s:       s,
		marker:  filepath.Join(dir, "provisioned"),
		prog:    filepath.Join(dir, "setup-progress.json"),
		dir:     dir,
		storeDB: st,
	}
	mirror, err := provision.NewMirror(st)
	if err != nil {
		t.Fatal(err)
	}
	s.wiz = &wizard.Wizard{
		Prov:         fake,
		MarkerPath:   e.marker,
		ProgressPath: e.prog,
		Mirror:       mirror,
		Claimed:      s.Claimed,
		Logf:         t.Logf,
	}
	e.handler = s.setupGate(a.Gate(s.newMux()))
	return e
}

func (e *setupEnv) do(t *testing.T, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req = httptest.NewRequest(method, path, bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

func (e *setupEnv) state(t *testing.T) wizard.View {
	t.Helper()
	rec := e.do(t, "GET", "/api/setup/state", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/setup/state = %d (%s)", rec.Code, rec.Body.String())
	}
	var v wizard.View
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not JSON: %s", rec.Body.String())
	}
	msg, _ := body["error"].(string)
	return msg
}

const (
	e2eHostname = "hs-shack"
	e2eUser     = "rescue"
	e2ePassword = "correct horse battery"
)

var e2eKey = privtest.SSHPublicKey(11)

// TestSetupE2E walks the whole first-boot flow over HTTP: hostname, user, key,
// lock, claim. It is the acceptance test for the gating — every assertion about
// what is and is not reachable goes through the same handler chain the daemon
// serves.
func TestSetupE2E(t *testing.T) {
	e := newSetupEnv(t)

	// --- unprovisioned: the dashboard and the claim endpoint are both closed ---
	//
	// The claim endpoint in particular: an unprovisioned node still answers to
	// raspberrypi.local with an unlocked root, and letting someone claim it before
	// any of that is fixed would hand them a box the wizard is about to change
	// under them.
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/config"},
		{"GET", "/api/status"},
		{"POST", "/api/claim"},
		{"POST", "/api/session"},
	} {
		rec := e.do(t, tc.method, tc.path, map[string]string{"username": "x", "password": "y"}, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 while unprovisioned", tc.method, tc.path, rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["mode"] != "setup" {
			t.Errorf("%s %s: mode = %v, want setup", tc.method, tc.path, body["mode"])
		}
	}

	// Health stays reachable in every state.
	if rec := e.do(t, "GET", "/api/health", nil, nil); rec.Code != http.StatusOK {
		t.Errorf("GET /api/health = %d while unprovisioned, want 200", rec.Code)
	}

	if v := e.state(t); v.Mode != "setup" || v.Next != wizard.StepHostname {
		t.Fatalf("initial state = %+v", v)
	}

	// --- step 1: hostname ---
	//
	// The default is refused first, because that is the whole reason this step
	// exists and the operator has to be told why rather than merely stopped.
	rec := e.do(t, "POST", "/api/setup/hostname", map[string]string{"hostname": "raspberrypi"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("the default hostname = %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	if msg := errorOf(t, rec); !strings.Contains(msg, "default name") || !strings.Contains(msg, ".local") {
		t.Errorf("error = %q; it should say it is the default and why that matters", msg)
	}
	if e.fake.Hostname != privtest.DefaultHostname {
		t.Error("a rejected hostname reached the helper")
	}

	if rec := e.do(t, "POST", "/api/setup/hostname",
		map[string]string{"hostname": e2eHostname}, nil); rec.Code != http.StatusOK {
		t.Fatalf("hostname = %d (%s)", rec.Code, rec.Body.String())
	}
	if e.fake.Hostname != e2eHostname {
		t.Errorf("the helper's hostname is %q", e.fake.Hostname)
	}

	// --- steps out of order are refused, and say where to go ---
	rec = e.do(t, "POST", "/api/setup/lock", map[string]any{}, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("out-of-order lock = %d, want 409", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["expected_step"] != string(wizard.StepUser) {
		t.Errorf("expected_step = %v, want %q", body["expected_step"], wizard.StepUser)
	}
	if e.fake.RootLocked {
		t.Fatal("a refused lock still locked root")
	}

	// --- step 2: the recovery account ---
	if rec := e.do(t, "POST", "/api/setup/user", map[string]string{
		"username": e2eUser, "password": e2ePassword}, nil); rec.Code != http.StatusOK {
		t.Fatalf("user = %d (%s)", rec.Code, rec.Body.String())
	}
	u, ok := e.fake.User(e2eUser)
	if !ok {
		t.Fatal("the recovery account was not created")
	}
	if !u.Sudo {
		t.Error("the recovery account has no sudo; it could not recover a locked-root node")
	}

	// --- step 3: the key ---
	if rec := e.do(t, "POST", "/api/setup/key",
		map[string]string{"public_key": e2eKey}, nil); rec.Code != http.StatusOK {
		t.Fatalf("key = %d (%s)", rec.Code, rec.Body.String())
	}
	v := e.state(t)
	if !strings.HasPrefix(v.KeyFingerprint, "SHA256:") {
		t.Errorf("KeyFingerprint = %q", v.KeyFingerprint)
	}
	if v.Next != wizard.StepLock {
		t.Fatalf("Next = %q after the key step", v.Next)
	}

	// --- step 4: lock ---
	if rec := e.do(t, "POST", "/api/setup/lock", map[string]any{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("lock = %d (%s)", rec.Code, rec.Body.String())
	}
	if !e.fake.RootLocked {
		t.Error("root is not locked")
	}
	if !e.s.wiz.Provisioned() {
		t.Fatal("the provisioned marker was not written")
	}
	// The mirror in config.db agrees with the marker.
	m, err := provision.NewMirror(e.storeDB)
	if err != nil {
		t.Fatal(err)
	}
	if mirrored, err := m.Get(); err != nil || !mirrored {
		t.Errorf("the config.db mirror = %v (err %v), want true", mirrored, err)
	}

	// --- the gate hands over to the claim flow ---
	//
	// /api/setup/state stays reachable without authentication for the life of the
	// daemon, so once setup is done it must report which surface to show and
	// nothing else. The recovery account's name and the node's hostname are useful
	// to someone deciding how to attack the box and useless to anyone else.
	post := e.state(t)
	if post.Mode != "claim" || post.Next != wizard.StepClaim {
		t.Fatalf("state after lock = %+v, want claim mode", post)
	}
	if post.RecoveryUser != "" || post.Hostname != "" || post.KeyFingerprint != "" {
		t.Errorf("the post-setup state discloses provisioning detail to an unauthenticated caller: %+v", post)
	}
	// The dashboard is still closed, but now by the auth gate rather than the
	// setup gate — the mode in the error is how a client tells which wall it hit.
	rec = e.do(t, "GET", "/api/config", nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/config = %d after provisioning, want 403", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["mode"] != "claim" {
		t.Errorf("mode = %v after provisioning, want claim", body["mode"])
	}

	// --- step 5: claim ---
	rec = e.do(t, "POST", "/api/claim",
		map[string]string{"username": "admin", "password": "a-long-enough-password"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim = %d (%s)", rec.Code, rec.Body.String())
	}
	cookie := sessionCookie(t, rec.Result())

	// --- and the dashboard opens ---
	if rec := e.do(t, "GET", "/api/config", nil, cookie); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/config = %d after claiming, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if v := e.state(t); v.Mode != "done" || v.Next != wizard.StepDone {
		t.Errorf("final state = %+v, want done", v)
	}
}

// TestSetupE2EInterruptedAndResumed runs the same flow with waypointd restarting
// partway through, and asserts it reaches the same end state.
//
// This is not a hypothetical. The operator is frequently setting the node up over
// the setup access point and will lose that connection the moment the node joins
// their real network; a wizard that could not resume would strand them.
func TestSetupE2EInterruptedAndResumed(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// The helper is the same process across the restart — it is a separate daemon
	// on a real node, and its state is the system's, not waypointd's.
	fake := privtest.NewFake()

	first := newSetupEnvOver(t, dir, st, fake)
	if rec := first.do(t, "POST", "/api/setup/hostname",
		map[string]string{"hostname": e2eHostname}, nil); rec.Code != http.StatusOK {
		t.Fatalf("hostname = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := first.do(t, "POST", "/api/setup/user", map[string]string{
		"username": e2eUser, "password": e2ePassword}, nil); rec.Code != http.StatusOK {
		t.Fatalf("user = %d (%s)", rec.Code, rec.Body.String())
	}

	// --- waypointd restarts here, mid-wizard ---
	second := newSetupEnvOver(t, dir, st, fake)

	v := second.state(t)
	if v.Next != wizard.StepKey {
		t.Fatalf("resumed at %q, want %q", v.Next, wizard.StepKey)
	}
	if v.Hostname != e2eHostname || v.RecoveryUser != e2eUser {
		t.Errorf("the resumed wizard lost its state: %+v", v)
	}
	if v.Provisioned {
		t.Error("a half-finished wizard reports the node as provisioned")
	}
	// And the gate is still closed, so a restart mid-setup does not briefly open
	// the dashboard.
	if rec := second.do(t, "GET", "/api/config", nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/config = %d after the restart, want 403", rec.Code)
	}

	if rec := second.do(t, "POST", "/api/setup/key",
		map[string]string{"public_key": e2eKey}, nil); rec.Code != http.StatusOK {
		t.Fatalf("key = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := second.do(t, "POST", "/api/setup/lock",
		map[string]any{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("lock = %d (%s)", rec.Code, rec.Body.String())
	}

	// --- the same end state the uninterrupted run reaches ---
	st2, err := provision.Load(filepath.Join(dir, "provisioned"))
	if err != nil {
		t.Fatal(err)
	}
	if st2.Hostname != e2eHostname || st2.RecoveryUser != e2eUser || !st2.RootLocked || !st2.HostnameSet {
		t.Errorf("marker = %+v", st2)
	}
	if !fake.RootLocked || fake.Hostname != e2eHostname {
		t.Errorf("helper state: hostname=%q rootLocked=%v", fake.Hostname, fake.RootLocked)
	}
	u, ok := fake.User(e2eUser)
	if !ok || !u.Sudo || len(u.Keys) != 1 {
		t.Errorf("recovery account = %+v (present %v)", u, ok)
	}
	// Setup ran once, not twice: the resumed run did not redo the finished steps.
	if n := len(fake.CallsTo("set_hostname")); n != 1 {
		t.Errorf("set_hostname was called %d times across the restart, want 1", n)
	}
	if n := len(fake.CallsTo("create_recovery_user")); n != 1 {
		t.Errorf("create_recovery_user was called %d times across the restart, want 1", n)
	}

	// Claiming works from here, so the resumed node is not merely provisioned but
	// usable.
	rec := second.do(t, "POST", "/api/claim",
		map[string]string{"username": "admin", "password": "a-long-enough-password"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim after a resumed setup = %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestSetupGateAbsentWithoutAWizard: a server with no wizard configured behaves
// exactly as it did before this change. Every existing test builds a server that
// way, and a gate that defaulted to closed would have broken all of them.
func TestSetupGateAbsentWithoutAWizard(t *testing.T) {
	e := newSetupEnv(t)
	e.s.wiz = nil
	h := e.s.setupGate(e.s.auth.Gate(e.s.newMux()))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/config", nil))
	// The auth gate answers, not the setup gate: unclaimed, not unprovisioned.
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["mode"] != "claim" {
		t.Errorf("mode = %v with no wizard, want the auth gate's claim", body["mode"])
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/setup/state", nil))
	if rec.Code != http.StatusForbidden {
		// The auth gate closes it, because /api/setup/ is not on its pre-auth
		// allowlist — which is right: with no wizard there is nothing to serve.
		t.Errorf("GET /api/setup/state = %d with no wizard", rec.Code)
	}
}
