package main

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/publicview"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/ui"
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
// All three are claimed on the mux even before they have content, so nothing under
// them can fall through to the embedded file server, which the auth gate would not
// be protecting.
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

	// The public page. Served through the same gate as the API — the page and the
	// data it renders must appear and disappear together, or a visitor sees a shell
	// that cannot fill itself in.
	//
	// This is also the reason the prefix is claimed explicitly rather than left to
	// fall through: the auth gate exempts /public/ from the session wall, and "/"
	// is the embedded static file server, so an unclaimed prefix would serve every
	// asset under it to anonymous visitors regardless of the toggle.
	mux.Handle("/public/", s.publicGate(limiter.Middleware(publicPageCSP(http.HandlerFunc(s.publicPage)))))

	// Branding, inside the same gate (D4). The logo and the custom block are part
	// of the public surface and disappear with it.
	mux.Handle("/public/assets/logo", s.publicGate(limiter.Middleware(http.HandlerFunc(s.publicLogo))))
	mux.Handle("/public/custom-block", s.publicGate(limiter.Middleware(http.HandlerFunc(s.publicCustomBlock))))

	// The embed widget arrives with the documentation prompt. Its namespace is
	// claimed now for the same reason, so the file server never sees it.
	mux.Handle("/embed/", s.publicGate(limiter.Middleware(http.NotFoundHandler())))
}

// publicRootEnabled reports whether a bare "/" should answer with the public page
// (D7). It is consulted by the auth gate, which is what would otherwise turn that
// request into a login screen.
func (s *server) publicRootEnabled() bool {
	if s.publicStore == nil {
		return false
	}
	on, err := s.publicStore.Enabled()
	return err == nil && on
}

// serveRootPublicly answers "/" with the public page for a visitor who has no
// session (D7).
//
// The asymmetry between "/" and "/index.html" is the whole design, and it is
// deliberate rather than incidental:
//
//   - "/" is the address on a QR code, a business card, a repeater directory. When
//     the operator has opted in, a stranger typing it should get the node's public
//     page, not a password prompt for a machine they have no business logging into.
//   - "/index.html" is never swapped. It is the admin entry, it always reaches the
//     login screen or the dashboard, and it is where the public page's "Sign in"
//     link points.
//
// That second rule is load-bearing. Without a path that is guaranteed never to
// become the public page, enabling this feature would leave an operator with no
// URL that reaches their own login screen — the setting would be a lockout with
// extra steps. Anyone who can reach the node can still reach the admin entry,
// whatever the public toggle says.
//
// An authenticated request is never diverted: a keeper who has signed in and
// navigates to "/" wants their dashboard, and the session is the signal that says
// so.
func (s *server) serveRootPublicly(w http.ResponseWriter, r *http.Request) {
	// Rewrite so the asset allow-list and the page's own relative links resolve
	// against /public/ exactly as they do when it is reached directly. Serving two
	// different URLs for one page is how their behaviour drifts apart.
	r2 := r.Clone(r.Context())
	r2.URL.Path = "/public/"
	publicPageCSP(http.HandlerFunc(s.publicPage)).ServeHTTP(w, r2)
}

// publicPageAssets are the files the public page is allowed to serve, mapped to
// their content types.
//
// An allow-list rather than a directory handler, because a directory handler
// serves whatever is in the directory — and this directory is inside the UI bundle
// that the authenticated application also lives in. One misplaced file, one
// editor backup, one future refactor that moves an admin asset a level up, and the
// public surface has grown without anyone deciding it should. Enumerating the
// files makes adding one a deliberate act that shows up in a diff.
var publicPageAssets = map[string]struct{ file, mime string }{
	"/public/":                 {"public/index.html", "text/html; charset=utf-8"},
	"/public/index.html":       {"public/index.html", "text/html; charset=utf-8"},
	"/public/public.css":       {"public/public.css", "text/css; charset=utf-8"},
	"/public/public.js":        {"public/public.js", "text/javascript; charset=utf-8"},
	"/public/vendor/qrcode.js": {"vendor/qrcode.js", "text/javascript; charset=utf-8"},
	"/public/assets/icon.svg":  {"logo-mono.svg", "image/svg+xml"},
}

// publicPage serves the standalone public dashboard.
func (s *server) publicPage(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	a, ok := publicPageAssets[r.URL.Path]
	if !ok {
		http.NotFound(w, r)
		return
	}
	b, err := fs.ReadFile(ui.FS(), a.file)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", a.mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// The page is a shell; everything in it comes from the API, which is
	// no-store. Letting the shell cache briefly keeps a phone at an event from
	// re-fetching it on every glance without ever showing stale data.
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(b)
}

// publicPageCSP is the strict policy the public page renders under.
//
// default-src 'self' with no script-src exception is the load-bearing part: there
// is no inline script and no inline event handler anywhere in the page, so
// operator-authored text that somehow reached the DOM as markup still could not
// execute. That is the second line of defence behind the sanitising, and the one
// that does not depend on getting the sanitising right.
//
//   - style-src allows 'unsafe-inline' only because element.hidden and the
//     stylesheet do not need it — but the QR library writes an <svg> with
//     presentation attributes, and future map tiles will set positions inline.
//     Style injection without script is a defacement risk, not an execution one.
//   - img-src allows data: for the same QR SVG, and https: so the vendored map
//     tiles can load when that lands. No wildcard scheme beyond those.
//   - frame-ancestors 'self' keeps the page itself un-embeddable. The widget on
//     /embed/ is the thing designed to be framed, and it gets its own policy.
//   - form-action 'none' and base-uri 'none': the page has no forms and no
//     relative-URL base to hijack.
func publicPageCSP(next http.Handler) http.Handler {
	const policy = "default-src 'self'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline'; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"font-src 'self'; " +
		// The custom block is framed from this origin and nowhere else. Without
		// this the sandboxed iframe would be blocked by default-src, and with a
		// wider value the page could be made to frame someone else's document.
		"frame-src 'self'; " +
		"object-src 'none'; " +
		"base-uri 'none'; " +
		"form-action 'none'; " +
		"frame-ancestors 'self'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", policy)
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, OPTIONS")
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

// publicGET rejects anything that is not a read. The public surface is read-only,
// and saying so with a 405 is better than letting a POST fall through to a handler
// that ignores the method.
//
// HEAD is a read and is allowed. net/http discards the body for it automatically,
// so the handler needs no special case — and refusing it would break `curl -I`,
// uptime monitors, and link checkers, which is a lot of friction to buy nothing.
func publicGET(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD, OPTIONS")
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
	node := publicview.BuildNode(m, set, live, links, nets)

	// Branding rides with the reach card rather than having an endpoint of its
	// own: it is part of "who is this node", it changes only when an operator
	// edits it, and a second round trip for a paragraph of prose would be a
	// second thing to fail.
	if b, err := s.publicStore.Branding(); err == nil {
		node.HasLogo = b.LogoPath != ""
		node.HasCustomBlock = strings.TrimSpace(b.CustomHTML) != ""
		// Rendered and sanitised here, so the page receives HTML it can insert and
		// never Markdown it would have to render itself — which would put a
		// renderer, and a sanitiser, in the browser where neither can be trusted.
		if html, err := publicview.RenderNarrative(b.NarrativeMarkdown); err == nil {
			node.NarrativeHTML = html
		}
	}
	publicJSON(w, node)
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
