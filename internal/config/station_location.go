package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Where the station is, as coordinates.
//
// # Why this did not exist before
//
// It genuinely did not: publicview/node.go says so at length, and the one
// latitude that reaches a rendered file is a hardcoded 0.0 in DStarGateway.ini.
// General.Location is free-form prose ("Munson tower, 310 ft") and is not
// machine-readable.
//
// It exists now for one reason: the weather feature needs to suggest which
// counties a node should monitor, and the alert metadata service answers that
// from a point. Typing six-digit SAME codes from memory is not a thing an
// operator should be asked to do.
//
// # This does NOT become the public grid
//
// publicview deliberately derives the published grid square from the operator's
// grid_override and from nothing else, and the comment there argues the case
// better than this one can: a grid square is a disclosure, and asking the
// operator to type the one they are willing to publish is clearer consent than
// inferring one from a field they filled in for another purpose.
//
// That argument does not weaken now that coordinates exist -- it gets stronger,
// because these coordinates were entered to pick counties, which is a private
// administrative act. So nothing here feeds publicview, and a test asserts that
// the public projection is unchanged by setting a location. If a future change
// wants to offer "publish my grid, derived from my coordinates", that is a
// consent checkbox somebody designs on purpose, not a side effect of this.
type StationLocation struct {
	// Latitude and Longitude are decimal degrees, WGS84. Zero means unset --
	// see the note in ValidateStationLocation about why that is acceptable here.
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// IsSet reports whether a usable position has been entered.
//
// Null Island (0,0) is treated as unset. It is a real coordinate in the Gulf of
// Guinea and somebody could in principle be there, but every system that has
// ever used a float zero-value as "unset" has met it as an accident far more
// often than as a location, and a node that guesses counties from the Atlantic
// is a more confusing failure than one that asks for a position.
func (l StationLocation) IsSet() bool {
	return l.Latitude != 0 || l.Longitude != 0
}

// ValidateStationLocation refuses coordinates that are not coordinates.
//
// A latitude without a longitude is refused rather than half-accepted: it is
// almost always a half-finished edit, and accepting it would leave the county
// suggestion silently pointing at the prime meridian.
func ValidateStationLocation(l StationLocation) error {
	if math.IsNaN(l.Latitude) || math.IsInf(l.Latitude, 0) ||
		math.IsNaN(l.Longitude) || math.IsInf(l.Longitude, 0) {
		return fmt.Errorf("the station latitude and longitude must be numbers")
	}
	if l.Latitude < -90 || l.Latitude > 90 {
		return fmt.Errorf("the station latitude is %g; it must be between -90 and 90", l.Latitude)
	}
	if l.Longitude < -180 || l.Longitude > 180 {
		return fmt.Errorf("the station longitude is %g; it must be between -180 and 180", l.Longitude)
	}
	if (l.Latitude == 0) != (l.Longitude == 0) {
		return fmt.Errorf("the station position needs both a latitude and a longitude, or neither")
	}
	return nil
}

// SetStationLocation merges a partial body into the location section and
// validates it before writing.
func SetStationLocation(s *store.Store, raw []byte, by string) error {
	var l StationLocation
	if _, err := s.GetInto("station_location", &l); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&l); err != nil {
		return err
	}
	if err := ValidateStationLocation(l); err != nil {
		return err
	}
	return s.Set("station_location", &l, by)
}
