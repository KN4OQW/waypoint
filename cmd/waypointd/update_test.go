package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/updater"
)

// releaseServer stands in for the release host. It counts hits and records the
// last request, so a test can assert on the node's outbound traffic itself rather
// than on a log line. Both are guarded: the handler runs on the server's goroutine.
type releaseServer struct {
	url  string
	mu   sync.Mutex
	hits int
	last *http.Request
}

func newReleaseServer(t *testing.T, version string) *releaseServer {
	t.Helper()
	rs := &releaseServer{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.hits++
		rs.last = r.Clone(context.Background())
		rs.mu.Unlock()
		_ = json.NewEncoder(w).Encode(updater.Manifest{
			Version:  version,
			NotesURL: "https://example.invalid/notes",
			Artifacts: map[string]updater.Artifact{
				runtime.GOOS + "/" + runtime.GOARCH: {URL: "https://example.invalid/waypointd", SHA256: "not-fetched-by-a-check"},
			},
		})
	}))
	t.Cleanup(srv.Close)
	rs.url = srv.URL + "/update.json"
	return rs
}

func (rs *releaseServer) Hits() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.hits
}

func (rs *releaseServer) Last() *http.Request {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.last
}

// updateOnlyServer is a node with the binary updater configured and no apt stack —
// the poller must still run for it, and the privacy gate must still apply.
func updateOnlyServer(t *testing.T, manifestURL string, checkEnabled bool) *server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	prefs := config.DefaultUpdate()
	prefs.CheckEnabled = checkEnabled
	if err := st.Set("update", prefs, "test"); err != nil {
		t.Fatal(err)
	}
	return &server{
		store:  st,
		update: &updateConfig{url: manifestURL, platform: runtime.GOOS + "/" + runtime.GOARCH},
	}
}

// The #15 acceptance as a test: with automatic checks off, the unattended poller
// makes no outbound request at all. With them on it checks once per interval and
// caches the verdict for the Updates panel.
func TestUpdatePollerHonorsCheckEnabled(t *testing.T) {
	rs := newReleaseServer(t, "9.9.9")

	off := updateOnlyServer(t, rs.url, false)
	if got := off.pollUpdatesOnce(context.Background(), time.Time{}, time.Hour); !got.IsZero() {
		t.Fatalf("a gated tick must not stamp a check time, got %v", got)
	}
	if n := rs.Hits(); n != 0 {
		t.Fatalf("checks are off, yet the node made %d request(s) to the release server", n)
	}
	st, err := config.GetUpdateState(off.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Binary) != 0 || !st.LastBinaryCheck.IsZero() {
		t.Fatalf("a gated tick wrote check state: %+v", st)
	}

	on := updateOnlyServer(t, rs.url, true)
	last := on.pollUpdatesOnce(context.Background(), time.Time{}, time.Hour)
	if last.IsZero() {
		t.Fatal("a check tick must stamp the check time it carries forward")
	}
	if n := rs.Hits(); n != 1 {
		t.Fatalf("checks are on: want exactly 1 request, got %d", n)
	}
	// Throttle: a tick inside checkEvery re-evaluates the quiet window but does not
	// fetch again.
	if got := on.pollUpdatesOnce(context.Background(), last, time.Hour); !got.Equal(last) {
		t.Fatalf("throttled tick moved the stamp: %v -> %v", last, got)
	}
	if n := rs.Hits(); n != 1 {
		t.Fatalf("throttled tick fetched again: %d requests total", n)
	}

	// The verdict is cached where the Updates panel reads it.
	state, err := config.GetUpdateState(on.store)
	if err != nil {
		t.Fatal(err)
	}
	var cached binaryAvailability
	if err := json.Unmarshal(state.Binary, &cached); err != nil {
		t.Fatalf("cached binary check is not readable: %v", err)
	}
	if !cached.Available || cached.Version != "9.9.9" {
		t.Fatalf("cached the wrong verdict: %+v", cached)
	}
	if state.LastBinaryCheck.IsZero() {
		t.Fatal("cached the verdict without a timestamp")
	}
}

// Disabling automatic checks disables nothing else: the operator's own check still
// reaches the release server and answers.
func TestManualCheckWorksWithAutomaticChecksOff(t *testing.T) {
	rs := newReleaseServer(t, "9.9.9")
	s := updateOnlyServer(t, rs.url, false)

	rec := httptest.NewRecorder()
	s.updateCheck(rec, httptest.NewRequest("GET", "/api/update/check", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("manual check returned %d: %s", rec.Code, rec.Body.String())
	}
	if n := rs.Hits(); n != 1 {
		t.Fatalf("manual check made %d request(s), want 1", n)
	}
	var got binaryAvailability
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Available || got.Version != "9.9.9" {
		t.Fatalf("manual check reported %+v, want the 9.9.9 release", got)
	}
}

// What leaves the device on a check: a plain GET of the manifest path, one fixed
// User-Agent, no query string, no cookies, no header naming this node. The
// version/platform/channel comparison happens locally, after the download.
func TestCheckSendsNoDeviceIdentifier(t *testing.T) {
	rs := newReleaseServer(t, "9.9.9")
	s := updateOnlyServer(t, rs.url, true)
	if _, err := s.binaryCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := rs.Last()
	if r == nil {
		t.Fatal("no request reached the release server")
	}
	if r.Method != http.MethodGet {
		t.Fatalf("check used %s, want GET", r.Method)
	}
	if r.URL.RawQuery != "" {
		t.Fatalf("check sent a query string: %q", r.URL.RawQuery)
	}
	if len(r.Cookies()) != 0 {
		t.Fatalf("check sent cookies: %v", r.Cookies())
	}
	if ua := r.Header.Get("User-Agent"); ua != "Waypoint updater" {
		t.Fatalf("User-Agent is %q; it must be one fixed string carrying no node detail", ua)
	}
	// Nothing beyond what Go's client always sends may appear — every header is a
	// place an identifier can hide.
	allowed := map[string]bool{"User-Agent": true, "Accept-Encoding": true}
	for k := range r.Header {
		if !allowed[http.CanonicalHeaderKey(k)] {
			t.Fatalf("check sent an unexpected header %q: %q", k, r.Header.Get(k))
		}
	}
}
