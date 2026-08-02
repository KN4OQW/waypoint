package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/publicview"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/store"
)

// newPublicServer builds the smallest server that can answer the public API, with
// the feature in whatever state the caller wants.
func newPublicServer(t *testing.T, enabled bool) (*server, *publicview.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() }) //nolint:errcheck // test cleanup

	ps := publicview.New(st)
	set := publicview.DefaultSettings()
	set.Enabled = enabled
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	s := &server{
		store:       st,
		publicStore: ps,
		agg:         status.New(status.DefaultTxTTL, 0),
		publicSvc:   publicview.NewService(ps, nil, nil),
	}
	return s, ps
}

// publicPaths are every route the gate must cover.
var publicPaths = []string{
	"/api/public/node",
	"/api/public/status",
	"/api/public/lastheard",
	"/api/public/counters",
}

func do(t *testing.T, s *server, method, path string, remote string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerPublicRoutes(mux)
	r := httptest.NewRequest(method, path, nil)
	if remote != "" {
		r.RemoteAddr = remote
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// ---------------------------------------------------------------------------
// Gating (D2/D7)
// ---------------------------------------------------------------------------

// TestDisabledIs404Everywhere is the master switch. 404 rather than 401 or an
// empty 200 is the whole point: a visitor must not be able to tell a node with the
// feature off from a node that never had it.
func TestDisabledIs404Everywhere(t *testing.T) {
	s, _ := newPublicServer(t, false)
	for _, p := range publicPaths {
		t.Run(p, func(t *testing.T) {
			w := do(t, s, http.MethodGet, p, "203.0.113.9:1234")
			if w.Code != http.StatusNotFound {
				t.Errorf("%s with the public view disabled = %d, want 404", p, w.Code)
			}
			// A 404 that carries CORS headers still confirms the route exists.
			if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
				t.Errorf("%s leaked a CORS header while disabled: %q", p, got)
			}
		})
	}
}

// TestDisabledGateRunsBeforeEverything: preflight and non-GET methods must not
// reveal the route either.
func TestDisabledGateRunsBeforeEverything(t *testing.T) {
	s, _ := newPublicServer(t, false)
	for _, m := range []string{http.MethodOptions, http.MethodPost, http.MethodHead} {
		w := do(t, s, m, "/api/public/node", "203.0.113.9:1234")
		if w.Code != http.StatusNotFound {
			t.Errorf("%s /api/public/node while disabled = %d, want 404", m, w.Code)
		}
	}
}

// TestNilStoreFailsClosed: a partially-built server must 404 rather than panic or
// serve. Failing closed is the only safe direction for a disclosure switch.
func TestNilStoreFailsClosed(t *testing.T) {
	s := &server{}
	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	if w.Code != http.StatusNotFound {
		t.Errorf("a server with no public store = %d, want 404", w.Code)
	}
}

func TestEnabledServes(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, p := range publicPaths {
		t.Run(p, func(t *testing.T) {
			w := do(t, s, http.MethodGet, p, "203.0.113.9:1234")
			if w.Code != http.StatusOK {
				t.Errorf("%s with the public view enabled = %d, want 200", p, w.Code)
			}
		})
	}
}

// TestModuleToggleIs404 covers the per-field switches: a module an operator turned
// off is indistinguishable from one that does not exist.
func TestModuleToggleIs404(t *testing.T) {
	for _, tc := range []struct {
		path string
		off  func(*publicview.Settings)
	}{
		{"/api/public/status", func(v *publicview.Settings) { v.ShowStatus = false }},
		{"/api/public/lastheard", func(v *publicview.Settings) { v.ShowLastHeard = false }},
		{"/api/public/counters", func(v *publicview.Settings) { v.ShowCounters = false }},
	} {
		t.Run(tc.path, func(t *testing.T) {
			s, ps := newPublicServer(t, true)
			set := publicview.DefaultSettings()
			set.Enabled = true
			tc.off(&set)
			if err := ps.SaveSettings(set); err != nil {
				t.Fatal(err)
			}
			w := do(t, s, http.MethodGet, tc.path, "203.0.113.9:1234")
			if w.Code != http.StatusNotFound {
				t.Errorf("%s with its module off = %d, want 404", tc.path, w.Code)
			}
		})
	}
}

// TestIsPublicRoute pins the three namespaces D7 reserves, and — more usefully —
// that nothing else matches. The auth gate lets these past the session wall, so a
// prefix that matched too much would be a hole in the wall.
func TestIsPublicRoute(t *testing.T) {
	for _, p := range []string{
		"/api/public/node", "/api/public/anything",
		"/public/", "/public/index.html",
		"/embed/lastheard",
	} {
		if !IsPublicRoute(p) {
			t.Errorf("IsPublicRoute(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/", "/index.html", "/api/config", "/api/status", "/api/events",
		"/api/health", "/api/session", "/api/claim",
		"/api/publicity", // prefix must not match a longer route name
		"/api/public", "/publicly", "/embedded",
		"/../api/public/node",
	} {
		if IsPublicRoute(p) {
			t.Errorf("IsPublicRoute(%q) = true — this route would bypass the session wall", p)
		}
	}
}

// ---------------------------------------------------------------------------
// CORS scope
// ---------------------------------------------------------------------------

// TestCORSOnPublicAPIOnly is the containment check. The wildcard is deliberate on
// these four routes and would be a serious hole on any other, so the test asserts
// both halves against the daemon's real mux.
func TestCORSOnPublicAPIOnly(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, p := range publicPaths {
		w := do(t, s, http.MethodGet, p, "203.0.113.9:1234")
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s CORS origin = %q, want *", p, got)
		}
		// The wildcard origin plus credentials is the combination that turns an
		// anonymous API into one readable by any site a logged-in operator visits.
		if got := w.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("%s sends Allow-Credentials with a wildcard origin: %q", p, got)
		}
	}
}

// TestNoCORSOnAuthenticatedRoutes walks the daemon's actual route table rather
// than a hand-written list, so a future route group cannot quietly inherit the
// header by being added somewhere this test does not look.
func TestNoCORSOnAuthenticatedRoutes(t *testing.T) {
	s, _ := newPublicServer(t, true)
	mux := s.newMux()
	for _, p := range []string{
		"/api/health", "/api/status", "/api/config", "/api/events", "/api/history",
		"/api/session", "/api/claim", "/api/hardware", "/api/network/status", "/",
	} {
		r := httptest.NewRequest(http.MethodGet, p, nil)
		r.RemoteAddr = "203.0.113.9:1234"
		w := httptest.NewRecorder()
		// The header is set by middleware before the handler runs, so a handler
		// that panics on this deliberately-minimal server has still revealed
		// whether it would have carried CORS. Recovering keeps the assertion
		// meaningful without standing up the whole daemon.
		func() {
			defer func() { _ = recover() }()
			mux.ServeHTTP(w, r)
		}()
		if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("%s carries a CORS origin header %q — the public wildcard has leaked", p, got)
		}
	}
}

func TestCORSPreflight(t *testing.T) {
	s, _ := newPublicServer(t, true)
	w := do(t, s, http.MethodOptions, "/api/public/node", "203.0.113.9:1234")
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS = %d, want 204", w.Code)
	}
	if got := w.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Errorf("preflight methods = %q, want GET", got)
	}
}

func TestPublicAPIIsReadOnly(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		w := do(t, s, m, "/api/public/node", "203.0.113.9:1234")
		if w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s /api/public/node = %d, want 405", m, w.Code)
		}
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// TestRateLimitTripsAndLoopbackIsExempt covers both halves in one place, because
// the interesting property is the contrast: the same burst that throttles a
// stranger must never throttle the node's own dashboard.
func TestRateLimitTripsAndLoopbackIsExempt(t *testing.T) {
	s, _ := newPublicServer(t, true)
	mux := http.NewServeMux()
	s.registerPublicRoutes(mux)

	hit := func(remote string) int {
		r := httptest.NewRequest(http.MethodGet, "/api/public/node", nil)
		r.RemoteAddr = remote
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}

	// Burst is 30; well past it the stranger is refused.
	var throttled bool
	for range publicview.RateLimitBurst * 3 {
		if hit("203.0.113.9:1234") == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("a stranger was never rate limited")
	}

	// A different address has its own bucket: one noisy client must not deny the
	// page to everyone else.
	if code := hit("198.51.100.7:2345"); code != http.StatusOK {
		t.Errorf("a second client got %d — the limiter is global, not per-IP", code)
	}

	// Loopback is exempt however hard it is hit.
	for i := range publicview.RateLimitBurst * 3 {
		if code := hit("127.0.0.1:5555"); code != http.StatusOK {
			t.Fatalf("loopback throttled on request %d with %d", i+1, code)
		}
	}
}

func TestRateLimitIgnoresForwardedFor(t *testing.T) {
	s, _ := newPublicServer(t, true)
	mux := http.NewServeMux()
	s.registerPublicRoutes(mux)

	// Honoring X-Forwarded-For would let one client bypass the limit entirely by
	// inventing an address per request.
	var throttled bool
	for i := range publicview.RateLimitBurst * 3 {
		r := httptest.NewRequest(http.MethodGet, "/api/public/node", nil)
		r.RemoteAddr = "203.0.113.9:1234"
		r.Header.Set("X-Forwarded-For", "10.0.0."+string(rune('0'+i%10)))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("a client bypassed the rate limit by varying X-Forwarded-For")
	}
}

// ---------------------------------------------------------------------------
// Field audit at the wire
// ---------------------------------------------------------------------------

// TestNodeCardNeverLeaksConfig is the field audit applied to the bytes rather than
// the struct. The reach card is assembled from the whole config model — modem
// ports, gateway addresses, network passwords, calibration levels — so the test
// puts real secrets into that model and greps the response for them.
func TestNodeCardNeverLeaksConfig(t *testing.T) {
	s, ps := newPublicServer(t, true)

	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	m.General.Callsign = "K4SRC"
	m.General.Location = "123 Elm Street, Milton FL"
	m.Modem.Port = "/dev/ttyAMA0"
	m.Modem.RXFreqHz = "438900000"
	m.Modem.TXFreqHz = "433900000"
	m.Modem.RXLevel = "57" // digits that appear nowhere in the frequencies below
	m.DMR.ColorCode = "1"
	m.DMRNet.Slot1 = true
	m.DMRNet.Slot2 = true
	m.DMRNet.GatewayAddress = "192.168.1.50"
	m.Modes.DMR = true
	for section, v := range map[string]any{
		"general": m.General, "modem": m.Modem, "dmr": m.DMR,
		"dmrnet": m.DMRNet, "modes": m.Modes,
	} {
		if err := s.store.Set(section, v, "test"); err != nil {
			t.Fatal(err)
		}
	}

	set := publicview.DefaultSettings()
	set.Enabled = true
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	if w.Code != http.StatusOK {
		t.Fatalf("node = %d", w.Code)
	}
	body := w.Body.String()

	for _, secret := range []string{
		"/dev/ttyAMA0",   // modem port
		"192.168.1.50",   // gateway address
		"123 Elm Street", // the operator's free-text location
		"57",             // a calibration level, chosen so it cannot collide with a frequency
		"rx_level", "port", "location", "gateway_address",
	} {
		if strings.Contains(body, secret) {
			t.Errorf("the reach card leaked %q:\n%s", secret, body)
		}
	}
	// And it did serve the fields it is supposed to.
	for _, want := range []string{"K4SRC", "438900000", "433900000"} {
		if !strings.Contains(body, want) {
			t.Errorf("the reach card is missing %q:\n%s", want, body)
		}
	}
}

// TestNodeStructIsAllowListed keeps the reach card's shape under the same audit
// the activity structs get. The config model grows constantly; this is what stops
// a new field being wired in without anyone deciding it may be public.
func TestNodeStructIsAllowListed(t *testing.T) {
	allowed := map[string]bool{
		"Callsign": true, "RXFrequency": true, "TXFrequency": true,
		"ColorCode": true, "Slots": true, "Modes": true, "Talkgroup": true,
		"Grid": true, "PowerLine": true, "PurposeTags": true,
		"PurposeFreetext": true, "Links": true, "Nets": true,
	}
	typ := reflect.TypeOf(publicview.Node{})
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; !allowed[name] {
			t.Errorf("publicview.Node has an un-audited field %q — add it to this "+
				"allow-list with the D-decision that permits disclosing it", name)
		}
	}
}

// TestTogglesOmitRatherThanBlank: a field whose toggle is off must be absent from
// the JSON, not present and empty. "" is still an answer to "what colour code does
// this node use", and the point of the toggle is to decline the question.
func TestTogglesOmitRatherThanBlank(t *testing.T) {
	s, ps := newPublicServer(t, true)
	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	m.General.Callsign = "K4SRC"
	m.Modem.RXFreqHz = "438900000"
	m.DMR.ColorCode = "1"
	for section, v := range map[string]any{"general": m.General, "modem": m.Modem, "dmr": m.DMR} {
		if err := s.store.Set(section, v, "test"); err != nil {
			t.Fatal(err)
		}
	}

	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowFreq = false
	set.ShowCCTS = false
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}

	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"rx_frequency", "tx_frequency", "color_code", "slots"} {
		if _, present := got[key]; present {
			t.Errorf("%q is present with its toggle off: %v", key, got[key])
		}
	}
	if got["callsign"] != "K4SRC" {
		t.Errorf("callsign = %v, want K4SRC", got["callsign"])
	}
}

// TestGridNeverExceedsSixCharacters is D3's precision ceiling at the wire.
func TestGridNeverExceedsSixCharacters(t *testing.T) {
	s, ps := newPublicServer(t, true)
	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowGrid = true
	set.GridOverride = "EM60lp37" // an 8-character paste
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["grid"] != "EM60lp" {
		t.Errorf("grid = %v, want EM60lp truncated to 6 characters", got["grid"])
	}
}

func TestGridAbsentWhenToggledOff(t *testing.T) {
	s, ps := newPublicServer(t, true)
	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowGrid = false
	set.GridOverride = "EM60lp"
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	w := do(t, s, http.MethodGet, "/api/public/node", "203.0.113.9:1234")
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["grid"]; present {
		t.Errorf("grid is present with show_grid off: %v", got["grid"])
	}
}

// TestLastHeardLimitBounds stops a stranger asking for the whole window at once.
func TestLastHeardLimitBounds(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, tc := range []struct {
		q    string
		want int
	}{
		{"", http.StatusOK},
		{"?limit=10", http.StatusOK},
		{"?limit=100000", http.StatusOK}, // clamped, not refused
		{"?limit=0", http.StatusBadRequest},
		{"?limit=-1", http.StatusBadRequest},
		{"?limit=banana", http.StatusBadRequest},
	} {
		w := do(t, s, http.MethodGet, "/api/public/lastheard"+tc.q, "203.0.113.9:1234")
		if w.Code != tc.want {
			t.Errorf("lastheard%s = %d, want %d", tc.q, w.Code, tc.want)
		}
	}
}

func TestPublicResponsesAreNotCached(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, p := range publicPaths {
		w := do(t, s, http.MethodGet, p, "203.0.113.9:1234")
		if got := w.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("%s Cache-Control = %q, want no-store", p, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s is missing nosniff", p)
		}
	}
}

// TestReservedNamespacesNeverReachTheFileServer is the regression guard for a
// hole that was theoretical when it was closed and would not have been for long.
//
// IsPublicRoute exempts /public/ and /embed/ from the session wall so the page and
// the widget can be anonymous. If nothing claims those prefixes on the mux they
// fall through to "/", the embedded static file server — and the first asset added
// under a public/ directory in the UI bundle would be served to anyone, whether or
// not the operator ever turned the feature on.
func TestReservedNamespacesNeverReachTheFileServer(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		s, _ := newPublicServer(t, enabled)
		mux := s.newMux()
		for _, p := range []string{
			"/public/", "/public/index.html", "/public/assets/logo",
			"/embed/lastheard", "/embed/anything.js",
		} {
			r := httptest.NewRequest(http.MethodGet, p, nil)
			r.RemoteAddr = "203.0.113.9:1234"
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			// Until the page and widget land these are 404 in both states. What
			// matters is that the answer comes from the gated handler rather than
			// from the file server, which is what the next assertion pins.
			if w.Code != http.StatusNotFound {
				t.Errorf("enabled=%v: %s = %d, want 404 from the gated handler", enabled, p, w.Code)
			}
			if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "text/html") && w.Body.Len() > 0 &&
				!strings.Contains(w.Body.String(), "404") {
				t.Errorf("enabled=%v: %s was served content by the file server: %q", enabled, p, ct)
			}
		}
	}
}
