package wizard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

type env struct {
	w      *wizard.Wizard
	fake   *privtest.Fake
	marker string
	prog   string
}

// newEnv builds a wizard over the in-memory helper. The paths are real files in a
// temp directory, because resume behaviour is about what survives a restart and a
// fake filesystem would prove nothing about that.
func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	fake := privtest.NewFake()
	claimed := false
	e := &env{
		fake:   fake,
		marker: filepath.Join(dir, "provisioned"),
		prog:   filepath.Join(dir, "setup-progress.json"),
	}
	e.w = &wizard.Wizard{
		Prov:         fake,
		MarkerPath:   e.marker,
		ProgressPath: e.prog,
		Claimed:      func() bool { return claimed },
		Logf:         t.Logf,
	}
	return e
}

// restart returns a second wizard over the same files — what a waypointd restart
// mid-setup produces.
func (e *env) restart(t *testing.T) *wizard.Wizard {
	t.Helper()
	return &wizard.Wizard{
		Prov:         e.fake,
		MarkerPath:   e.marker,
		ProgressPath: e.prog,
		Claimed:      func() bool { return false },
		Logf:         t.Logf,
	}
}

var testKey = privtest.SSHPublicKey(7)

// Property 1: a fresh node starts at the first step and is not provisioned.
func TestFreshNodeStartsAtHostname(t *testing.T) {
	e := newEnv(t)
	if got := e.w.Next(); got != wizard.StepHostname {
		t.Errorf("Next = %q, want %q", got, wizard.StepHostname)
	}
	v := e.w.State()
	if v.Mode != "setup" || v.Provisioned {
		t.Errorf("view = %+v, want setup mode and not provisioned", v)
	}
	if len(v.Completed) != 0 {
		t.Errorf("Completed = %v on a fresh node", v.Completed)
	}
}

// Property 2: the whole flow walks in order and ends provisioned.
func TestWalkTheWholeFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	if _, err := e.w.SetHostname(ctx, wizard.HostnameRequest{Hostname: "hs-shack"}); err != nil {
		t.Fatal(err)
	}
	if got := e.w.Next(); got != wizard.StepUser {
		t.Fatalf("after hostname, Next = %q", got)
	}
	if _, err := e.w.CreateUser(ctx, wizard.UserRequest{
		Username: "rescue", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
	if got := e.w.Next(); got != wizard.StepKey {
		t.Fatalf("after user, Next = %q", got)
	}
	if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{PublicKey: testKey}); err != nil {
		t.Fatal(err)
	}
	if got := e.w.Next(); got != wizard.StepLock {
		t.Fatalf("after key, Next = %q", got)
	}
	v, err := e.w.Lock(ctx, wizard.LockRequest{})
	if err != nil {
		t.Fatal(err)
	}

	if !v.Provisioned || v.Mode != "claim" || v.Next != wizard.StepClaim {
		t.Fatalf("after lock, view = %+v, want provisioned and waiting to be claimed", v)
	}
	if !e.w.Provisioned() {
		t.Error("the marker was not written")
	}

	// The system really was changed, not just the progress file.
	if e.fake.Hostname != "hs-shack" {
		t.Errorf("hostname = %q", e.fake.Hostname)
	}
	if !e.fake.RootLocked {
		t.Error("root is not locked")
	}
	u, ok := e.fake.User("rescue")
	if !ok || !u.Sudo || len(u.Keys) != 1 {
		t.Errorf("recovery account = %+v", u)
	}

	// And the marker records what happened, for anything that reads it later.
	st, err := provision.Load(e.marker)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "hs-shack" || st.RecoveryUser != "rescue" || !st.RootLocked || !st.HostnameSet {
		t.Errorf("marker = %+v", st)
	}
}

// Property 3: an interrupted run resumes at the first incomplete step and reaches
// the same end state. This is the case that actually happens: the operator is
// often changing the very network they are connected over, so setup losing its
// connection partway through is normal, not exceptional.
func TestInterruptedRunResumesToTheSameEndState(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	if _, err := e.w.SetHostname(ctx, wizard.HostnameRequest{Hostname: "hs-shack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.CreateUser(ctx, wizard.UserRequest{
		Username: "rescue", SSHKey: testKey}); err != nil {
		t.Fatal(err)
	}

	// waypointd restarts here. A second wizard over the same files has to pick up
	// where the first left off, from disk alone.
	w2 := e.restart(t)
	if got := w2.Next(); got != wizard.StepLock {
		// The key arrived with the account, so the key step is already satisfied.
		t.Fatalf("resumed at %q, want %q", got, wizard.StepLock)
	}
	v := w2.State()
	if v.Hostname != "hs-shack" || v.RecoveryUser != "rescue" {
		t.Errorf("resumed view lost its state: %+v", v)
	}
	if v.KeyFingerprint == "" {
		t.Error("resumed view lost the installed key's fingerprint")
	}

	if _, err := w2.Lock(ctx, wizard.LockRequest{}); err != nil {
		t.Fatal(err)
	}

	// Same end state as the uninterrupted walk.
	if !w2.Provisioned() {
		t.Fatal("the resumed run did not provision the node")
	}
	st, err := provision.Load(e.marker)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "hs-shack" || st.RecoveryUser != "rescue" || !st.RootLocked {
		t.Errorf("marker = %+v", st)
	}
	if !e.fake.RootLocked {
		t.Error("root is not locked")
	}
}

// Property 4: resuming after every single step lands on the right one. Checking
// only one interruption point would leave the state machine's other transitions
// untested, and the interruption point is not something the operator chooses.
func TestResumePointAfterEveryStep(t *testing.T) {
	ctx := context.Background()
	steps := []struct {
		run  func(*testing.T, *env)
		next wizard.Step
	}{
		{func(t *testing.T, e *env) {}, wizard.StepHostname},
		{func(t *testing.T, e *env) {
			mustHostname(t, e.w)
		}, wizard.StepUser},
		{func(t *testing.T, e *env) {
			mustHostname(t, e.w)
			mustUserWithPassword(t, e.w)
		}, wizard.StepKey},
		{func(t *testing.T, e *env) {
			mustHostname(t, e.w)
			mustUserWithPassword(t, e.w)
			if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{PublicKey: testKey}); err != nil {
				t.Fatal(err)
			}
		}, wizard.StepLock},
	}
	for _, tc := range steps {
		t.Run(string(tc.next), func(t *testing.T) {
			e := newEnv(t)
			tc.run(t, e)
			if got := e.restart(t).Next(); got != tc.next {
				t.Errorf("resumed at %q, want %q", got, tc.next)
			}
		})
	}
}

// Property 5: the default hostname is refused, with a message that tells the
// operator what is wrong rather than a code.
func TestDefaultHostnameIsRejectedWithAClearError(t *testing.T) {
	e := newEnv(t)
	for _, name := range []string{"raspberrypi", "RaspberryPi", "waypoint"} {
		t.Run(name, func(t *testing.T) {
			_, err := e.w.SetHostname(context.Background(), wizard.HostnameRequest{Hostname: name})
			if err == nil {
				t.Fatal("the default hostname was accepted")
			}
			if privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
				t.Errorf("code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeInvalidArgument)
			}
			if !strings.Contains(err.Error(), "default name") {
				t.Errorf("the error does not say why: %v", err)
			}
			if e.w.Next() != wizard.StepHostname {
				t.Error("a rejected hostname advanced the wizard")
			}
		})
	}
}

// Property 6: steps cannot be skipped, and the refusal names the step the wizard
// is actually waiting for — a client that has lost its place needs directions,
// not just a rejection.
func TestStepsCannotBeSkipped(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	_, err := e.w.Lock(ctx, wizard.LockRequest{})
	if err == nil {
		t.Fatal("lock ran before anything else")
	}
	var order *wizard.ErrOutOfOrder
	if !asOutOfOrder(err, &order) {
		t.Fatalf("err = %v, want ErrOutOfOrder", err)
	}
	if order.Expected != wizard.StepHostname {
		t.Errorf("Expected = %q, want %q", order.Expected, wizard.StepHostname)
	}
	if e.fake.RootLocked {
		t.Fatal("a refused lock step still locked root")
	}

	if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{PublicKey: testKey}); err == nil {
		t.Error("the key step ran before the account existed")
	}
}

// Property 7: a step may be re-submitted. A browser that reloads mid-request must
// not be told it is out of order for asking again.
func TestStepsAreReSubmittable(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	mustHostname(t, e.w)
	if _, err := e.w.SetHostname(ctx, wizard.HostnameRequest{Hostname: "hs-shack"}); err != nil {
		t.Errorf("re-submitting the hostname: %v", err)
	}
	if got := e.w.Next(); got != wizard.StepUser {
		t.Errorf("Next = %q after a re-submission", got)
	}
}

// Property 8: the key step may be skipped only when the account has a password.
// Skipping on a key-only account would leave nobody able to log in — the exact
// outcome the recovery account exists to prevent.
func TestKeySkipRequiresAPassword(t *testing.T) {
	t.Run("key-only account", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		mustHostname(t, e.w)
		if _, err := e.w.CreateUser(ctx, wizard.UserRequest{
			Username: "rescue", SSHKey: testKey}); err != nil {
			t.Fatal(err)
		}
		// The key came with the account, so the step is already done; force the
		// question by checking the view's advice to the UI.
		if e.w.State().KeySkippable {
			t.Error("KeySkippable = true for an account with no password")
		}
	})

	t.Run("password account may skip", func(t *testing.T) {
		e := newEnv(t)
		ctx := context.Background()
		mustHostname(t, e.w)
		mustUserWithPassword(t, e.w)
		if !e.w.State().KeySkippable {
			t.Fatal("KeySkippable = false for an account with a password")
		}
		if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{Skip: true}); err != nil {
			t.Fatal(err)
		}
		if got := e.w.Next(); got != wizard.StepLock {
			t.Errorf("Next = %q after skipping the key", got)
		}
	})
}

// Property 9: the lock step refuses to turn off password authentication on an
// account with no key. That combination is the other way to end up with a node
// nobody can reach.
func TestLockRefusesToStrandTheRecoveryAccount(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	mustHostname(t, e.w)
	mustUserWithPassword(t, e.w)
	if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{Skip: true}); err != nil {
		t.Fatal(err)
	}

	no := false
	_, err := e.w.Lock(ctx, wizard.LockRequest{PasswordAuth: &no})
	if privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}
	if e.fake.RootLocked {
		t.Error("the refused lock step still locked root")
	}
	if e.w.Provisioned() {
		t.Error("the refused lock step wrote the marker")
	}
}

// Property 10: the gate serves the wizard and closes everything else, then gets
// out of the way once the marker is written.
func TestGate(t *testing.T) {
	e := newEnv(t)
	behind := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // a status nothing else returns
	})
	h := e.w.Gate(mount(e.w, behind))

	// Unprovisioned: the dashboard and the claim endpoint are closed.
	for _, path := range []string{"/api/config", "/api/claim", "/api/status", "/api/events"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s = %d, want 403 while unprovisioned", path, rec.Code)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if body["mode"] != "setup" {
			t.Errorf("GET %s: mode = %v, want setup", path, body["mode"])
		}
	}

	// Health and the wizard's own routes are open.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/health", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("GET /api/health = %d, want the handler behind the gate", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/setup/state", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/setup/state = %d, want 200", rec.Code)
	}

	// The page is served, so a browser sees a setup screen rather than a JSON 403.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "set up") {
		t.Errorf("GET / = %d %q", rec.Code, rec.Body.String())
	}

	// Provisioned: the gate is a pass-through.
	walk(t, e.w)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/claim", nil))
	if rec.Code != http.StatusTeapot {
		t.Errorf("GET /api/claim = %d after provisioning, want the handler behind the gate", rec.Code)
	}
}

// Property 11: helper failures reach the client as the right HTTP status. The
// distinction that matters is 400 from 503: "fix what you typed" against "the
// helper is not reachable, and this is not your fault".
func TestHTTPStatusMapping(t *testing.T) {
	e := newEnv(t)
	h := e.w.Handler()

	// Bad input.
	rec := post(t, h, "/api/setup/hostname", map[string]any{"hostname": "raspberrypi"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("default hostname = %d, want 400", rec.Code)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if msg, _ := body["error"].(string); !strings.Contains(msg, "default name") {
		t.Errorf("error = %q, want an explanation", msg)
	}
	if strings.Contains(body["error"].(string), "privhelper:") {
		t.Errorf("the error still carries its log prefix: %q", body["error"])
	}

	// Out of order.
	rec = post(t, h, "/api/setup/lock", map[string]any{})
	if rec.Code != http.StatusConflict {
		t.Errorf("out-of-order lock = %d, want 409", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["expected_step"] != string(wizard.StepHostname) {
		t.Errorf("expected_step = %v, want %q", body["expected_step"], wizard.StepHostname)
	}

	// Helper unreachable.
	e.fake.Errs[privhelper.MethodSetHostname] = privhelper.Errorf(privhelper.CodeUnsupported,
		"cannot reach the provisioning helper")
	rec = post(t, h, "/api/setup/hostname", map[string]any{"hostname": "hs-shack"})
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unreachable helper = %d, want 503", rec.Code)
	}

	// An unknown field is refused rather than half-applied.
	rec = post(t, h, "/api/setup/user", map[string]any{"username": "rescue", "uid": 0})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field = %d, want 400", rec.Code)
	}

	// GET on a step endpoint does nothing: a prefetching browser must not create
	// an account by following a link.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/setup/user", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET on a step = %d, want 405", rec.Code)
	}
}

// Property 12: a corrupt progress file starts over rather than skipping work.
// Repeating a step is harmless — every one of them is idempotent — while skipping
// one leaves a node half set up.
func TestCorruptProgressStartsOver(t *testing.T) {
	e := newEnv(t)
	mustHostname(t, e.w)
	if err := writeFile(e.prog, "{ this is not json"); err != nil {
		t.Fatal(err)
	}
	if got := e.restart(t).Next(); got != wizard.StepHostname {
		t.Errorf("Next = %q with a corrupt progress file, want %q", got, wizard.StepHostname)
	}
}

// --- helpers --------------------------------------------------------------

func mustHostname(t *testing.T, w *wizard.Wizard) {
	t.Helper()
	if _, err := w.SetHostname(context.Background(), wizard.HostnameRequest{Hostname: "hs-shack"}); err != nil {
		t.Fatal(err)
	}
}

func mustUserWithPassword(t *testing.T, w *wizard.Wizard) {
	t.Helper()
	if _, err := w.CreateUser(context.Background(), wizard.UserRequest{
		Username: "rescue", Password: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}
}

func walk(t *testing.T, w *wizard.Wizard) {
	t.Helper()
	ctx := context.Background()
	mustHostname(t, w)
	mustUserWithPassword(t, w)
	if _, err := w.InstallKey(ctx, wizard.KeyRequest{PublicKey: testKey}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Lock(ctx, wizard.LockRequest{}); err != nil {
		t.Fatal(err)
	}
}

// mount puts the wizard's routes in front of a stand-in for the rest of the mux,
// which is how the daemon assembles them.
func mount(w *wizard.Wizard, rest http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, wizard.Prefix) {
			w.Handler().ServeHTTP(rw, r)
			return
		}
		rest.ServeHTTP(rw, r)
	})
}

func post(t *testing.T, h http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func asOutOfOrder(err error, target **wizard.ErrOutOfOrder) bool {
	e, ok := err.(*wizard.ErrOutOfOrder)
	if ok {
		*target = e
	}
	return ok
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
