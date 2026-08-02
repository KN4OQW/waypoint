package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/publicview"
)

func seedPosition(t *testing.T, s *server, station string, lat, lon float64) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"station": station, "lat": lat, "lon": lon})
	mux := http.NewServeMux()
	s.registerAdminMapRoutes(mux)
	r := httptest.NewRequest(http.MethodPost, "/api/map/position", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		t.Fatalf("seeding %s = %d: %s", station, w.Code, w.Body.String())
	}
}

func mapGet(t *testing.T, s *server, path string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.registerPublicRoutes(mux) // includes the public map
	s.registerAdminMapRoutes(mux)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = "203.0.113.9:1234"
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

// TestPrecisionSplit is D3 asserted end to end at the wire: the same fix,
// requested through two doors, comes back at two precisions.
func TestPrecisionSplit(t *testing.T) {
	s, _ := newPublicServer(t, true)
	const lat, lon = 30.63214, -87.04057
	seedPosition(t, s, "N4ABC", lat, lon)

	// Public: grid centre, and the received coordinate appears nowhere.
	pub := mapGet(t, s, "/api/public/map")
	if pub.Code != http.StatusOK {
		t.Fatalf("/api/public/map = %d", pub.Code)
	}
	body := pub.Body.String()
	for _, frag := range []string{"30.63214", "87.04057", "30.632", "87.040"} {
		if strings.Contains(body, frag) {
			t.Errorf("the public map leaked the received coordinate (%q):\n%s", frag, body)
		}
	}
	var got struct {
		Stations []struct {
			Station string  `json:"station"`
			Grid    string  `json:"grid"`
			Lat     float64 `json:"lat"`
			Lon     float64 `json:"lon"`
		} `json:"stations"`
		WindowHours int `json:"window_hours"`
	}
	if err := json.Unmarshal(pub.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Stations) != 1 {
		t.Fatalf("public map = %+v", got)
	}
	if got.Stations[0].Grid != "EM60lp" {
		t.Errorf("public grid = %q, want EM60lp", got.Stations[0].Grid)
	}
	if got.Stations[0].Lat == lat || got.Stations[0].Lon == lon {
		t.Error("the public map returned the received coordinate as its marker position")
	}
	if got.WindowHours == 0 {
		t.Error("the public map does not report its window, so an empty map is unreadable")
	}

	// Admin: exactly what was received.
	adm := mapGet(t, s, "/api/map")
	if adm.Code != http.StatusOK {
		t.Fatalf("/api/map = %d", adm.Code)
	}
	var admin struct {
		Stations []publicview.Position `json:"stations"`
	}
	if err := json.Unmarshal(adm.Body.Bytes(), &admin); err != nil {
		t.Fatal(err)
	}
	if len(admin.Stations) != 1 {
		t.Fatalf("admin map = %+v", admin)
	}
	if admin.Stations[0].Lat != lat || admin.Stations[0].Lon != lon {
		t.Errorf("the admin map lost precision: %v, %v want %v, %v",
			admin.Stations[0].Lat, admin.Stations[0].Lon, lat, lon)
	}
}

// TestAdminMapIsNotPublic. /api/map carries full precision, so it must be behind
// the session wall — the whole precision split collapses if it is not.
func TestAdminMapIsNotPublic(t *testing.T) {
	for _, p := range []string{"/api/map", "/api/map/position"} {
		if IsPublicRoute(p) {
			t.Errorf("%s is on the anonymous route list — it serves full-precision positions", p)
		}
	}
}

func TestPublicMapGating(t *testing.T) {
	s, _ := newPublicServer(t, false)
	seedPosition(t, s, "N4ABC", 30.6, -87.0)
	if w := mapGet(t, s, "/api/public/map"); w.Code != http.StatusNotFound {
		t.Errorf("the public map with the view disabled = %d, want 404", w.Code)
	}
}

func TestPublicMapFollowsItsToggle(t *testing.T) {
	s, ps := newPublicServer(t, true)
	set := publicview.DefaultSettings()
	set.Enabled = true
	set.ShowMap = false
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	seedPosition(t, s, "N4ABC", 30.6, -87.0)
	if w := mapGet(t, s, "/api/public/map"); w.Code != http.StatusNotFound {
		t.Errorf("the public map with its module off = %d, want 404", w.Code)
	}
	// The admin side is unaffected: the toggle governs disclosure, not the
	// operator's own view of their node.
	if w := mapGet(t, s, "/api/map"); w.Code != http.StatusOK {
		t.Errorf("the admin map = %d with the public module off, want 200", w.Code)
	}
}

func TestManualIngestValidates(t *testing.T) {
	s, _ := newPublicServer(t, true)
	mux := http.NewServeMux()
	s.registerAdminMapRoutes(mux)
	for _, tc := range []struct {
		name, body string
	}{
		{"null island", `{"station":"N4ABC","lat":0,"lon":0}`},
		{"out of range", `{"station":"N4ABC","lat":200,"lon":0}`},
		{"no station", `{"station":"","lat":30,"lon":-87}`},
		{"unknown field", `{"station":"N4ABC","lat":30,"lon":-87,"source":"aprs-is"}`},
		{"junk", `not json`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/map/position", strings.NewReader(tc.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("%s = %d, want 400", tc.name, w.Code)
			}
		})
	}
}

// TestManualIngestCannotForgeATransport. The body has no source field, so an
// operator-entered fix is always recorded as manual and can never be passed off
// as something the node heard on a mesh.
func TestManualIngestCannotForgeATransport(t *testing.T) {
	s, ps := newPublicServer(t, true)
	seedPosition(t, s, "N4ABC", 30.6, -87.0)
	got, err := ps.Positions(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("positions = %+v", got)
	}
	if got[0].Source != publicview.SourceManual {
		t.Errorf("a hand-entered fix was recorded as source %q, want %q",
			got[0].Source, publicview.SourceManual)
	}
}

// TestLeafletIsServedFromTheNode. The map must not need a CDN — the CSP forbids
// one, and a hotspot may have no internet at all.
func TestLeafletIsServedFromTheNode(t *testing.T) {
	s, _ := newPublicServer(t, true)
	for _, p := range []string{
		"/public/vendor/leaflet/leaflet.js",
		"/public/vendor/leaflet/leaflet.css",
		"/public/vendor/leaflet/images/marker-icon.png",
		"/public/vendor/leaflet/images/layers.png",
		"/public/vendor/leaflet/images/marker-shadow.png",
	} {
		w := do(t, s, http.MethodGet, p, "203.0.113.9:1234")
		if w.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", p, w.Code)
		}
	}
	js := do(t, s, http.MethodGet, "/public/vendor/leaflet/leaflet.js", "203.0.113.9:1234").Body.String()
	if !strings.Contains(js, "Leaflet 1.9.4") || !strings.Contains(js, "Vladimir Agafonkin") {
		t.Error("the vendored Leaflet lost its version banner or copyright header")
	}
	// leaflet.css references images/ by relative URL, so the CSS and the images
	// have to stay in the same shape. This is what catches a well-meaning flatten.
	css := do(t, s, http.MethodGet, "/public/vendor/leaflet/leaflet.css", "203.0.113.9:1234").Body.String()
	if !strings.Contains(css, "images/marker-icon.png") {
		t.Error("leaflet.css no longer references images/ — check the vendored layout")
	}
}
