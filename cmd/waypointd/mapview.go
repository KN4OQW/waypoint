package main

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

// The map (D3).
//
// Positions come from the mesh transports RFC-0018 describes and from nowhere
// else. APRS-IS is not consulted and the aprs.fi API is never called: the first
// would republish other people's data under this node's name, and the second is
// forbidden by its terms.
//
// Tiles are the one deliberate exception to the "everything is served from this
// node" rule that the rest of the public page follows. A map needs tiles and
// nobody can vendor the planet, so they come from OpenStreetMap at run time —
// which is why the page's CSP names tile.openstreetmap.org explicitly rather than
// opening img-src to https:. Widening a policy to "any HTTPS image" to admit one
// host is how a strict CSP becomes decorative.
//
// OSM's tile usage policy is a condition of that, not a courtesy. What it requires
// of a client like this: identify yourself (the browser's own UA does that), do
// not bulk-download or prefetch, cache what you fetch, and display the attribution.
// The map is configured accordingly — no prefetching, default caching, attribution
// on by default and not removable from the page.

// The two sides of the precision split are registered separately, and by
// different callers, because they are not the same kind of route. Mixing them in
// one function invited exactly the confusion it sounds like it would: the public
// half belongs inside the gated, CORS'd, rate-limited group, and the admin half
// belongs behind the session wall with every other /api route.

// registerPublicMapRoute mounts the snapped public map. Called from
// registerPublicRoutes, which owns the middleware chain.
func (s *server) registerPublicMapRoute(mux *http.ServeMux, limiter *publicview.RateLimiter) {
	mux.Handle("/api/public/map", s.publicGate(publicCORS(limiter.Middleware(
		http.HandlerFunc(s.publicMap)))))
}

// registerAdminMapRoutes mounts the full-precision map and the manual ingest.
// Called from newMux alongside the other authenticated routes.
func (s *server) registerAdminMapRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/map", s.adminMap)
	mux.HandleFunc("/api/map/position", s.adminMapIngest)
}

// publicMap serves stations snapped to their grid centres.
func (s *server) publicMap(w http.ResponseWriter, r *http.Request) {
	if !publicGET(w, r) {
		return
	}
	set, err := s.publicStore.Settings()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if !set.ShowMap {
		http.NotFound(w, r)
		return
	}
	got, err := s.publicSvc.PublicPositions()
	if err != nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	publicJSON(w, map[string]any{
		"stations": got,
		// The window is the page's own policy, not data about anyone, so it is
		// reported even when the list is empty — a map with no pins reads very
		// differently once you know it covers the last hour rather than the week.
		"window_hours": set.RetentionHours,
	})
}

// adminMap serves full-precision positions to an authenticated operator.
//
// The precision split is the whole point of D3: an operator debugging their own
// mesh needs the real number, and a stranger does not need it at all. This is the
// side that has it.
func (s *server) adminMap(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// The admin view is not bound by the public retention setting (D6) — that
	// setting governs disclosure, not the operator's own visibility into their
	// node. The prune horizon is what bounds this.
	got, err := s.publicStore.Positions(time.Now().Add(-publicview.PositionPruneHorizon))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"stations":      got,
		"horizon_hours": int(publicview.PositionPruneHorizon / time.Hour),
	})
}

// adminMapIngest lets an operator enter a position by hand.
//
// It exists because no transport does yet. RFC-0018 is still `proposed` and none
// of Meshtastic, MeshCore or LoRa APRS is merged, so without this the map has no
// producer at all and could not be exercised on a bench or validated on hardware.
// When the transports land they call publicview.Store.IngestPosition directly and
// this stays as what it is: the manual entry an operator uses to place their own
// node, or to test.
//
// It is authenticated, and the source it records is SourceManual rather than a
// transport name, so an operator-entered fix is never mistaken for something the
// node actually heard.
func (s *server) adminMapIngest(w http.ResponseWriter, r *http.Request) {
	if s.publicStore == nil {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodPost, http.MethodPut:
	default:
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Station string  `json:"station"`
		Lat     float64 `json:"lat"`
		Lon     float64 `json:"lon"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		http.Error(w, `invalid body: want {"station":"N4ABC","lat":30.6,"lon":-87.0}`, http.StatusBadRequest)
		return
	}
	err := s.publicStore.IngestPosition(publicview.Report{
		Station: body.Station, Source: publicview.SourceManual,
		Lat: body.Lat, Lon: body.Lon,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// runPositionPrune drops fixes past the horizon, hourly.
//
// Bounded retention of other people's location data is not housekeeping. A
// station heard once and never again should not sit on this node's disk
// indefinitely, and the operator's public retention setting does not govern that
// — it governs what is shown.
func runPositionPrune(s *publicview.Store, every time.Duration, stop <-chan struct{}) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			if _, err := s.PrunePositions(time.Now().Add(-publicview.PositionPruneHorizon)); err != nil {
				// Not fatal: the next tick tries again, and a map that keeps a few
				// extra hours of fixes is a smaller problem than a daemon that
				// stops because a delete failed.
				continue
			}
		}
	}
}
