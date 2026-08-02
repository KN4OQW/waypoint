package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/publicview"
	"github.com/KN4OQW/waypoint/internal/status"
)

// The public HTTP surface (D2, D5, D7).
//
// Everything here is reachable without a session, which makes it the only part of
// the daemon where the threat model is "anyone on the internet who found this
// node". Three properties follow, and each is enforced by middleware rather than
// by handler discipline, because a handler that forgets is exactly the failure
// these exist to prevent:
//
//   - Off means gone. When the operator has not opted in, every public route
//     answers 404 — not 401, not an empty 200. A visitor cannot tell a node with
//     the feature turned off from a node that never had it.
//   - CORS is granted to the JSON API and nowhere else, so enabling the public
//     page never widens the authenticated surface.
//   - Everything is rate limited per IP, because these routes read the event
//     database and nothing else is standing between them and a stranger.

// publicRoutePrefixes are the three namespaces the public surface owns (D7).
// The HTML page and the embed widget land in later prompts; the prefixes are
// declared here so the gate covers them from the start rather than being widened
// later, which is the change nobody reviews carefully.
var publicRoutePrefixes = []string{"/api/public/", "/public/", "/embed/"}

// IsPublicRoute reports whether a path belongs to the public surface. The auth
// gate consults it to let these routes past the session wall; the gate below is
// what then decides whether they answer at all.
func IsPublicRoute(path string) bool {
	for _, p := range publicRoutePrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}

// registerPublicRoutes mounts the public JSON API.
//
// The middleware order is deliberate. Gating runs first so a disabled node spends
// nothing on a request it is about to 404, and so the rate limiter's table is not
// filled by traffic to routes that do not exist. CORS runs before the limiter so a
// throttled cross-origin caller still gets a readable error rather than an opaque
// CORS failure that sends its developer hunting the wrong problem.
func (s *server) registerPublicRoutes(mux *http.ServeMux) {
	limiter := publicview.NewRateLimiter()
	api := func(h http.HandlerFunc) http.Handler {
		return s.publicGate(publicCORS(limiter.Middleware(h)))
	}
	mux.Handle("/api/public/node", api(s.publicNode))
	mux.Handle("/api/public/status", api(s.publicStatus))
	mux.Handle("/api/public/lastheard", api(s.publicLastHeard))
	mux.Handle("/api/public/counters", api(s.publicCounters))

	// The HTML page and the embed widget arrive in later prompts, but their
	// namespaces are claimed and gated now, and that is not tidiness.
	//
	// The auth gate exempts these prefixes from the session wall (IsPublicRoute).
	// Without a handler registered they fall through to "/", which is the embedded
	// static file server — so the first file anyone adds under a public/ directory
	// in the UI bundle would be served to anonymous visitors whether or not the
	// operator ever enabled the feature. Claiming the prefixes with a gated
	// not-found closes that today, while the hole is theoretical, rather than in
	// the prompt that happens to create the file.
	notYet := s.publicGate(limiter.Middleware(http.NotFoundHandler()))
	mux.Handle("/public/", notYet)
	mux.Handle("/embed/", notYet)
}

// publicGate is D2's master switch. A node whose operator has not enabled the
// public view answers 404 on every public route, and so does one whose store
// cannot be read — the failure is closed, because a store that cannot say whether
// the operator opted in has not opted in.
func (s *server) publicGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.publicStore == nil {
			http.NotFound(w, r)
			return
		}
		on, err := s.publicStore.Enabled()
		if err != nil || !on {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicCORS opens the JSON API to any origin.
//
// The wildcard is the point of the feature: clubs embed this in their own sites,
// and a same-origin-only API would be useless to them. It is safe here precisely
// because these endpoints are anonymous and read-only — there is no session for a
// cross-origin caller to ride, no cookie is honored, and no state is mutated. The
// same header on any authenticated route would be a serious hole, which is why it
// is applied per route group rather than globally.
func publicCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Max-Age", "600")
		// Deliberately no Access-Control-Allow-Credentials: with the wildcard
		// origin a browser would refuse it anyway, and the combination is the
		// classic way an API accidentally becomes readable by any site a logged-in
		// operator visits.
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// publicJSON writes a response with caching disabled. These answers change with
// every transmission, and a cached last-heard list is a wrong one.
func publicJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_ = json.NewEncoder(w).Encode(v)
}

// publicGET rejects anything but GET. The public API is read-only, and saying so
// with a 405 is better than letting a POST fall through to a handler that ignores
// the method.
func publicGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

// publicNode serves the reach card.
func (s *server) publicNode(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// A configuration that cannot be read yields a card without the configured
	// fields rather than a 500: the operator-authored parts are still true and
	// still worth serving.
	m, _ := config.Load(s.store)

	var live *status.Status
	if s.agg != nil {
		snap := s.agg.Snapshot()
		live = &snap
	}
	var links []publicview.Link
	if set.ShowLinks {
		links, _ = s.publicStore.Links()
	}
	var nets []publicview.Net
	if set.ShowNets {
		nets, _ = s.publicStore.Nets()
	}
	publicJSON(w, publicview.BuildNode(m, set, live, links, nets))
}

// publicStatus serves the simple status line (D5).
func (s *server) publicStatus(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	// A module whose toggle is off is 404, not an empty 200. The page asks only
	// for what it was told exists, so this is the answer to a request that was
	// constructed by hand — and "this node does not publish that" is the honest
	// reply, indistinguishable from the route not existing.
	if !set.ShowStatus {
		http.NotFound(w, r)
		return
	}
	got, err := s.publicSvc.Status()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	publicJSON(w, got)
}

// publicLastHeard serves the last-heard list.
func (s *server) publicLastHeard(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !set.ShowLastHeard {
		http.NotFound(w, r)
		return
	}
	limit := publicLastHeardDefault
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = min(n, publicLastHeardMax)
	}
	got, err := s.publicSvc.LastHeard(limit)
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	publicJSON(w, got)
}

// Bounds on the public last-heard list. The ceiling is what stops a stranger
// asking for the whole retention window in one request; the default is what the
// page and the widget actually render.
const (
	publicLastHeardDefault = 25
	publicLastHeardMax     = 100
)

// publicCounters serves the summary counters.
func (s *server) publicCounters(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !set.ShowCounters {
		http.NotFound(w, r)
		return
	}
	got, err := s.publicSvc.Counters()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	publicJSON(w, got)
}
