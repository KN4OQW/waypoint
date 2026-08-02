package publicview

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

// Note on the state of this feature, because it matters for reading these tests:
// RFC-0018's transports (Meshtastic, MeshCore, LoRa APRS) are NOT merged — the
// RFC is still `proposed` and no transport code exists in the repo. IngestPosition
// is the seam they will call; today the only producer is the operator-entered
// path. So these tests drive the seam directly, which is what they would do
// anyway: a transport's decoding is its own problem, and this file is about what
// happens to a fix once it is decoded.

func TestGridForKnownPoints(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lat, lon float64
		want     string
	}{
		// Published reference points, not values computed here — the point of a
		// table like this is to check the algorithm against the outside world.
		// Munich is the worked example in the Maidenhead literature; the
		// Washington Monument is the other commonly cited one.
		{"Munich (JN58td)", 48.14666, 11.60833, "JN58td"},
		{"Washington Monument (FM18lv)", 38.8895, -77.0353, "FM18lv"},
		{"origin of the system", -90, -180, "AA00aa"},
		{"Milton, Florida", 30.632, -87.040, "EM60lp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := GridFor(tc.lat, tc.lon)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Errorf("GridFor(%v, %v) = %q, want %q", tc.lat, tc.lon, got, tc.want)
			}
		})
	}
}

func TestGridForRejectsNonsense(t *testing.T) {
	for _, tc := range []struct{ lat, lon float64 }{
		{math.NaN(), 0}, {0, math.NaN()}, {91, 0}, {-91, 0}, {0, 181}, {0, -181},
	} {
		if _, err := GridFor(tc.lat, tc.lon); !errors.Is(err, ErrBadPosition) {
			t.Errorf("GridFor(%v, %v) accepted an impossible coordinate", tc.lat, tc.lon)
		}
	}
	// The extremes are legal and must not index past the last field.
	for _, tc := range []struct{ lat, lon float64 }{{90, 180}, {90, -180}, {-90, 180}} {
		if _, err := GridFor(tc.lat, tc.lon); err != nil {
			t.Errorf("GridFor(%v, %v) rejected a legal extreme: %v", tc.lat, tc.lon, err)
		}
	}
}

// TestSnapIsLossy is the disclosure property, stated as a round trip: a precise
// fix goes in, a grid centre comes out, and the difference is at least the width
// of a house and at most the width of the square.
func TestSnapRoundTrip(t *testing.T) {
	const lat, lon = 30.6321, -87.0405
	grid, err := GridFor(lat, lon)
	if err != nil {
		t.Fatal(err)
	}
	cLat, cLon, err := GridCentre(grid)
	if err != nil {
		t.Fatal(err)
	}
	// The centre must be inside the square the fix was in.
	back, err := GridFor(cLat, cLon)
	if err != nil {
		t.Fatal(err)
	}
	if back != grid {
		t.Errorf("the centre of %s is in %s", grid, back)
	}
	// And it must not be the fix itself — that is the whole point.
	if cLat == lat && cLon == lon {
		t.Error("snapping returned the original coordinate")
	}
	// A subsquare is 5' lon x 2.5' lat: about 0.0833 x 0.0417 degrees. The centre
	// can be at most half of that from any point inside it.
	if d := math.Abs(cLat - lat); d > 0.0209 {
		t.Errorf("snapped latitude moved %v deg, more than half a subsquare", d)
	}
	if d := math.Abs(cLon - lon); d > 0.0417 {
		t.Errorf("snapped longitude moved %v deg, more than half a subsquare", d)
	}
}

// TestGridCentreIsCentre: a corner would sit on the boundary and read as a more
// precise claim than it is.
func TestGridCentreIsCentre(t *testing.T) {
	lat, lon, err := GridCentre("EM60lp")
	if err != nil {
		t.Fatal(err)
	}
	// EM60 starts at lon -88, lat 30. Subsquare "l" is the 12th of 24 columns
	// (2 deg / 24 = 5 min each), "p" the 16th of 24 rows (1 deg / 24 = 2.5 min).
	// So EM60lp spans lon [-87.0833, -87.0) and lat [30.625, 30.6667).
	westEdge := -88.0 + 11*(2.0/24)
	southEdge := 30.0 + 15*(1.0/24)
	wantLon := westEdge + (2.0/24)/2
	wantLat := southEdge + (1.0/24)/2
	if math.Abs(lon-wantLon) > 1e-9 || math.Abs(lat-wantLat) > 1e-9 {
		t.Errorf("GridCentre(EM60lp) = %v, %v; want %v, %v", lat, lon, wantLat, wantLon)
	}
	// Independently: the centre must round-trip to the square it came from, and
	// both edges must not.
	if g, _ := GridFor(lat, lon); g != "EM60lp" {
		t.Errorf("the centre of EM60lp is in %s", g)
	}
}

// TestNeighbouringStationsDoNotCollapse: snapping must spread stations across the
// area each is somewhere in, not pile them onto one point.
func TestNeighbouringSquaresGetDifferentCentres(t *testing.T) {
	aLat, aLon, err := GridCentre("EM60lp")
	if err != nil {
		t.Fatal(err)
	}
	bLat, bLon, err := GridCentre("EM60lq") // one subsquare north
	if err != nil {
		t.Fatal(err)
	}
	cLat, cLon, err := GridCentre("EM60mp") // one subsquare east
	if err != nil {
		t.Fatal(err)
	}
	if aLat == bLat && aLon == bLon {
		t.Error("vertically adjacent grid squares snapped to the same point")
	}
	if aLat == cLat && aLon == cLon {
		t.Error("horizontally adjacent grid squares snapped to the same point")
	}
}

func TestIngestValidation(t *testing.T) {
	ps := newTestStore(t)
	for _, tc := range []struct {
		name string
		r    Report
		want error
	}{
		{"no station", Report{Source: SourceLoRaAPRS, Lat: 30, Lon: -87}, ErrBadPosition},
		{"unknown source", Report{Station: "N4ABC", Source: "carrier pigeon", Lat: 30, Lon: -87}, ErrBadSource},
		{"NaN", Report{Station: "N4ABC", Source: SourceLoRaAPRS, Lat: math.NaN(), Lon: -87}, ErrBadPosition},
		{"infinity", Report{Station: "N4ABC", Source: SourceLoRaAPRS, Lat: math.Inf(1), Lon: -87}, ErrBadPosition},
		{"out of range", Report{Station: "N4ABC", Source: SourceLoRaAPRS, Lat: 200, Lon: -87}, ErrBadPosition},
		// The fix a GPS emits when it has no fix. One of the most common bogus
		// coordinates in any position feed.
		{"null island", Report{Station: "N4ABC", Source: SourceLoRaAPRS, Lat: 0, Lon: 0}, ErrBadPosition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ps.IngestPosition(tc.r); !errors.Is(err, tc.want) {
				t.Errorf("IngestPosition(%s) = %v, want %v", tc.name, err, tc.want)
			}
		})
	}
}

// TestIngestUpserts is the privacy property expressed as schema: the table holds
// where a station is, never where it has been. It cannot leak a track because it
// does not hold one.
func TestIngestUpserts(t *testing.T) {
	ps := newTestStore(t)
	base := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)

	for i, p := range [][2]float64{{30.60, -87.00}, {30.61, -87.01}, {30.62, -87.02}} {
		if err := ps.IngestPosition(Report{
			Station: "n4abc", Source: SourceLoRaAPRS,
			Lat: p[0], Lon: p[1], HeardAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ps.Positions(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("the store kept %d rows for one station — it is accumulating a track", len(got))
	}
	if got[0].Lat != 30.62 || got[0].Lon != -87.02 {
		t.Errorf("the stored fix is %v, %v; want the most recent", got[0].Lat, got[0].Lon)
	}
	if got[0].Station != "N4ABC" {
		t.Errorf("station stored as %q, want it normalised", got[0].Station)
	}

	// A different transport is a different row: the same operator may be on two
	// meshes, and merging them would silently drop one.
	if err := ps.IngestPosition(Report{
		Station: "N4ABC", Source: SourceMeshtastic, Lat: 30.7, Lon: -87.1, HeardAt: base,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = ps.Positions(base.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("two transports collapsed into %d row(s)", len(got))
	}
}

func TestPrunePositions(t *testing.T) {
	ps := newTestStore(t)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	for i, age := range []time.Duration{time.Hour, 3 * 24 * time.Hour, 10 * 24 * time.Hour} {
		if err := ps.IngestPosition(Report{
			Station: []string{"N4ABC", "W4RJM", "KK4WXT"}[i],
			Source:  SourceLoRaAPRS, Lat: 30 + float64(i)/100, Lon: -87, HeardAt: now.Add(-age),
		}); err != nil {
			t.Fatal(err)
		}
	}
	n, err := ps.PrunePositions(now.Add(-PositionPruneHorizon))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pruned %d rows, want the one older than the horizon", n)
	}
	left, err := ps.Positions(time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 2 {
		t.Errorf("%d rows left, want 2", len(left))
	}
}

// ---------------------------------------------------------------------------
// The precision split
// ---------------------------------------------------------------------------

func TestPublicPositionsAreSnapped(t *testing.T) {
	svc, ps, _ := newService(t, nil, nil)
	const lat, lon = 30.6321, -87.0405
	if err := ps.IngestPosition(Report{
		Station: "N4ABC", Source: SourceLoRaAPRS, Lat: lat, Lon: lon, HeardAt: fixedNow.Add(-time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	pub, err := svc.PublicPositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 {
		t.Fatalf("public positions = %+v, want one", pub)
	}
	if pub[0].Lat == lat || pub[0].Lon == lon {
		t.Error("a raw fix reached the public map")
	}
	if pub[0].Grid == "" {
		t.Error("the public position carries no grid")
	}
	// The admin side still has the real number.
	adm, err := ps.Positions(fixedNow.Add(-24 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if adm[0].Lat != lat || adm[0].Lon != lon {
		t.Errorf("the admin view lost precision: %v, %v", adm[0].Lat, adm[0].Lon)
	}
}

// TestPublicPositionStructCarriesNoRawFix extends the field audit. The public type
// must not grow a field that could hold what was received.
func TestPublicPositionStructIsAllowListed(t *testing.T) {
	allowed := map[string]bool{"Station": true, "Grid": true, "Lat": true, "Lon": true, "HeardAt": true}
	typ := reflect.TypeOf(PublicPosition{})
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; !allowed[name] {
			t.Errorf("PublicPosition grew a field %q — Lat/Lon here are the GRID CENTRE, and "+
				"anything that could hold the received fix does not belong", name)
		}
	}
	// Source is deliberately absent: which mesh someone is on is a fact about
	// their equipment that the map has no reason to publish.
	if _, ok := typ.FieldByName("Source"); ok {
		t.Error("PublicPosition exposes the transport a station was heard on")
	}
}

func TestPublicPositionsApplySuppression(t *testing.T) {
	svc, ps, _ := newService(t, nil, nil)
	for _, c := range []string{"N4ABC", "W4RJM"} {
		if err := ps.IngestPosition(Report{
			Station: c, Source: SourceLoRaAPRS, Lat: 30.6, Lon: -87.0, HeardAt: fixedNow.Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ps.AddSuppressed("n4abc-9"); err != nil {
		t.Fatal(err)
	}
	pub, err := svc.PublicPositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 || pub[0].Station != "W4RJM" {
		t.Errorf("public map = %+v, want only W4RJM", pub)
	}
}

func TestPublicPositionsHonourTheWindow(t *testing.T) {
	svc, ps, _ := newService(t, nil, nil)
	set := DefaultSettings()
	set.RetentionHours = 2
	if err := ps.SaveSettings(set); err != nil {
		t.Fatal(err)
	}
	for i, c := range []string{"N4ABC", "W4RJM"} {
		if err := ps.IngestPosition(Report{
			Station: c, Source: SourceLoRaAPRS, Lat: 30.6 + float64(i)/10, Lon: -87.0,
			HeardAt: fixedNow.Add(-time.Duration(i*5) * time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	pub, err := svc.PublicPositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 || pub[0].Station != "N4ABC" {
		t.Errorf("public map = %+v, want only the fix inside a 2 h window", pub)
	}
}

// TestPublicPositionsDropNodeIDs: a mesh node id identifies a device whose owner
// never chose to be on this page, and it is not the callsign the map claims to
// show. Same rule the last-heard list applies to sources.
func TestPublicPositionsDropNodeIDs(t *testing.T) {
	svc, ps, _ := newService(t, nil, nil)
	for _, st := range []string{"N4ABC", "3735928559", "!a1b2c3d4"} {
		_ = ps.IngestPosition(Report{
			Station: st, Source: SourceMeshtastic, Lat: 30.6, Lon: -87.0, HeardAt: fixedNow.Add(-time.Hour),
		})
	}
	pub, err := svc.PublicPositions()
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != 1 || pub[0].Station != "N4ABC" {
		t.Errorf("public map = %+v, want only the callsign-shaped station", pub)
	}
}
