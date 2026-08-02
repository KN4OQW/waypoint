package publicview

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// Locally-heard positions (D3).
//
// Two rules define this whole file, and they are both about what is NOT here.
//
// Nothing comes from APRS-IS or aprs.fi. The map shows stations this node heard
// on its own mesh transports and nothing else. Republishing the APRS-IS feed
// would put other people's data under this node's name, and aprs.fi's terms
// forbid it outright.
//
// The public surface never sees a precise coordinate. It is given a 6-character
// Maidenhead grid and the CENTRE of that grid — about 5 x 3 km — and the raw
// fix is not in the struct at all, so a handler cannot leak what it does not
// hold. The authenticated view gets what was received, because an operator
// debugging their own mesh needs the real number.

// PositionSource names a transport. These are the three RFC-0018 describes; the
// constants exist so the store holds a known set rather than whatever string a
// future transport passes in.
const (
	SourceMeshtastic = "meshtastic"
	SourceMeshCore   = "meshcore"
	SourceLoRaAPRS   = "lora_aprs"
	// SourceManual is a position an operator entered themselves — the bench case,
	// and the only source that exists until a transport is merged.
	SourceManual = "manual"
)

var knownSources = map[string]bool{
	SourceMeshtastic: true, SourceMeshCore: true, SourceLoRaAPRS: true, SourceManual: true,
}

var (
	ErrBadPosition = errors.New("publicview: position is not a usable fix")
	ErrBadSource   = errors.New("publicview: unknown position source")
)

// Report is one position as received from a transport. It is the input side of
// the ingestion seam RFC-0018's transports will call.
type Report struct {
	// Station is the callsign where the transport carries one (LoRa APRS always
	// does), or the mesh node id where it does not.
	Station string
	Source  string
	Lat     float64
	Lon     float64
	// HeardAt is when the node received it. Zero means now.
	HeardAt time.Time
}

// Position is a stored fix at full precision. Admin-side only — see PublicPosition.
type Position struct {
	Station string    `json:"station"`
	Source  string    `json:"source"`
	Lat     float64   `json:"lat"`
	Lon     float64   `json:"lon"`
	HeardAt time.Time `json:"heard_at"`
}

// PublicPosition is a fix as an anonymous visitor may see it.
//
// Lat/Lon here are the CENTRE of the grid square, not the received fix. That is
// why the type exists rather than the public handler zeroing fields on a Position:
// a struct that has never held the precise value cannot disclose it by omission,
// and the field audit can prove the shape rather than trusting a code path.
type PublicPosition struct {
	Station string    `json:"station"`
	Grid    string    `json:"grid"`
	Lat     float64   `json:"lat"` // grid centre
	Lon     float64   `json:"lon"` // grid centre
	HeardAt time.Time `json:"heard_at"`
}

// PositionPruneHorizon is how long a fix is kept at all, independent of the
// operator's public retention window.
//
// The two are different questions. Retention bounds what the public map SHOWS;
// this bounds what the node KEEPS. A generous horizon means an operator who
// widens their retention window still has something to widen it onto, while a
// station that has not been heard in a week is gone from disk regardless of any
// setting — the smallest amount of other people's location data the feature can
// work with.
const PositionPruneHorizon = 7 * 24 * time.Hour

// IngestPosition records a position report, replacing any previous fix from the
// same station on the same transport.
//
// This is the seam RFC-0018's transports will call. Nothing calls it today except
// the operator-entered path, because no transport is merged — see the package
// notes in positions_test.go for what that means for the map.
func (s *Store) IngestPosition(r Report) error {
	station := strings.ToUpper(strings.TrimSpace(r.Station))
	if station == "" {
		return fmt.Errorf("%w: no station", ErrBadPosition)
	}
	if !knownSources[r.Source] {
		return fmt.Errorf("%w: %q", ErrBadSource, r.Source)
	}
	// NaN and infinities reach here from a half-decoded frame, and SQLite will
	// happily store them; a NaN latitude then renders as a marker at an undefined
	// place, or crashes a client that assumes a number.
	if math.IsNaN(r.Lat) || math.IsNaN(r.Lon) || math.IsInf(r.Lat, 0) || math.IsInf(r.Lon, 0) {
		return fmt.Errorf("%w: %v, %v", ErrBadPosition, r.Lat, r.Lon)
	}
	if r.Lat < -90 || r.Lat > 90 || r.Lon < -180 || r.Lon > 180 {
		return fmt.Errorf("%w: %v, %v out of range", ErrBadPosition, r.Lat, r.Lon)
	}
	// 0,0 is Null Island: the fix a GPS emits when it has no fix, and one of the
	// most common bogus coordinates in any position feed. Dropping it costs a
	// station genuinely in the Gulf of Guinea a map pin and saves every other
	// station from a spurious one.
	if r.Lat == 0 && r.Lon == 0 {
		return fmt.Errorf("%w: null island", ErrBadPosition)
	}
	at := r.HeardAt
	if at.IsZero() {
		at = time.Now()
	}
	_, err := s.db.Exec(
		`INSERT INTO heard_positions(station, transport, lat, lon, heard_at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(station, transport) DO UPDATE SET
		   lat = excluded.lat, lon = excluded.lon, heard_at = excluded.heard_at`,
		station, r.Source, r.Lat, r.Lon, at.UTC().Format(time.RFC3339))
	return err
}

// Positions returns every fix newer than since, at full precision. Authenticated
// callers only.
func (s *Store) Positions(since time.Time) ([]Position, error) {
	rows, err := s.db.Query(
		`SELECT station, transport, lat, lon, heard_at FROM heard_positions
		  WHERE heard_at >= ? ORDER BY heard_at DESC`,
		since.UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Position{}
	for rows.Next() {
		var (
			p  Position
			at string
		)
		if err := rows.Scan(&p.Station, &p.Source, &p.Lat, &p.Lon, &at); err != nil {
			return nil, err
		}
		p.HeardAt, _ = time.Parse(time.RFC3339, at)
		out = append(out, p)
	}
	return out, rows.Err()
}

// PrunePositions deletes fixes older than the horizon and reports how many went.
func (s *Store) PrunePositions(before time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM heard_positions WHERE heard_at < ?`,
		before.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PublicPositions returns the map an anonymous visitor sees: inside the operator's
// retention window, suppress list applied, every fix snapped to its grid centre.
//
// The snap happens here rather than in the handler on purpose. It is the single
// place precision is dropped, it returns a type that cannot carry the precise
// value onward, and a handler written later cannot skip a step it never performs.
func (s *Service) PublicPositions() ([]PublicPosition, error) {
	set, err := s.store.Settings()
	if err != nil {
		return nil, err
	}
	suppressed, err := s.store.SuppressSet()
	if err != nil {
		return nil, err
	}
	raw, err := s.store.Positions(s.now().Add(-window(set)))
	if err != nil {
		return nil, err
	}
	out := []PublicPosition{}
	for _, p := range raw {
		// Same rule the last-heard list uses: publish only what is callsign-shaped.
		// A mesh node id is an identifier for a device whose owner never chose to
		// be on this page, and it is not the callsign the map claims to show.
		call, ok := publishableCallsign(p.Station)
		if !ok || suppressed[call] {
			continue
		}
		grid, err := GridFor(p.Lat, p.Lon)
		if err != nil {
			continue
		}
		cLat, cLon, err := GridCentre(grid)
		if err != nil {
			continue
		}
		out = append(out, PublicPosition{
			Station: call, Grid: grid, Lat: cLat, Lon: cLon, HeardAt: p.HeardAt,
		})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Maidenhead
// ---------------------------------------------------------------------------

// GridFor converts a coordinate to a 6-character Maidenhead locator.
//
// Six characters is the ceiling the public surface is allowed (D3): one subsquare
// is 5 minutes of longitude by 2.5 minutes of latitude, roughly 5 x 3 km at
// mid-latitudes. Anything finer would identify a street.
func GridFor(lat, lon float64) (string, error) {
	if math.IsNaN(lat) || math.IsNaN(lon) || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return "", fmt.Errorf("%w: %v, %v", ErrBadPosition, lat, lon)
	}
	// Maidenhead measures from the antimeridian and the south pole.
	adjLon := lon + 180
	adjLat := lat + 90
	// A pole-exact or antimeridian-exact value would index one past the last
	// field; clamp rather than reject, since the coordinate is legal.
	if adjLon >= 360 {
		adjLon = math.Nextafter(360, 0)
	}
	if adjLat >= 180 {
		adjLat = math.Nextafter(180, 0)
	}

	f1 := int(adjLon / 20)              // field: 20 deg of longitude
	f2 := int(adjLat / 10)              // field: 10 deg of latitude
	s1 := int(math.Mod(adjLon, 20) / 2) // square: 2 deg
	s2 := int(math.Mod(adjLat, 10) / 1) // square: 1 deg
	u1 := int(math.Mod(adjLon, 2) / (2.0 / 24))
	u2 := int(math.Mod(adjLat, 1) / (1.0 / 24))

	return fmt.Sprintf("%c%c%d%d%c%c",
		'A'+f1, 'A'+f2, s1, s2, 'a'+u1, 'a'+u2), nil
}

// GridCentre returns the middle of a Maidenhead square.
//
// The centre, not a corner: a marker on the corner sits on the boundary with the
// neighbouring square and reads as a more precise claim than it is, and four
// stations in adjacent squares would cluster at one point rather than spreading
// across the area each of them is actually somewhere in.
func GridCentre(grid string) (lat, lon float64, err error) {
	g, err := NormalizeGrid(grid)
	if err != nil {
		return 0, 0, err
	}
	b := []byte(strings.ToUpper(g))
	lon = float64(b[0]-'A')*20 + float64(b[2]-'0')*2 - 180
	lat = float64(b[1]-'A')*10 + float64(b[3]-'0')*1 - 90
	if len(b) == 6 {
		lon += float64(b[4]-'A') * (2.0 / 24)
		lat += float64(b[5]-'A') * (1.0 / 24)
		// Half a subsquare.
		lon += (2.0 / 24) / 2
		lat += (1.0 / 24) / 2
		return lat, lon, nil
	}
	// Half a square, for the 4-character case.
	return lat + 0.5, lon + 1, nil
}
