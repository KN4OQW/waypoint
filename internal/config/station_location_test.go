package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateStationLocation(t *testing.T) {
	for _, tc := range []struct {
		name string
		loc  StationLocation
		want string // substring of the refusal, or "" to accept
	}{
		{"unset", StationLocation{}, ""},
		{"Pensacola area", StationLocation{Latitude: 30.6435, Longitude: -87.0545}, ""},
		{"south and east", StationLocation{Latitude: -33.87, Longitude: 151.21}, ""},
		{"poles are legal", StationLocation{Latitude: 90, Longitude: 180}, ""},
		{"latitude too far north", StationLocation{Latitude: 91, Longitude: 10}, "between -90 and 90"},
		{"latitude too far south", StationLocation{Latitude: -90.1, Longitude: 10}, "between -90 and 90"},
		{"longitude out of range", StationLocation{Latitude: 10, Longitude: 181}, "between -180 and 180"},
		// A half-finished edit is the common case and it must not be stored: a
		// latitude alone would point the county suggestion at the prime meridian.
		{"latitude without longitude", StationLocation{Latitude: 30.6}, "both a latitude and a longitude"},
		{"longitude without latitude", StationLocation{Longitude: -87.05}, "both a latitude and a longitude"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStationLocation(tc.loc)
			switch {
			case tc.want == "" && err != nil:
				t.Errorf("refused a good position: %v", err)
			case tc.want != "" && err == nil:
				t.Errorf("accepted %+v", tc.loc)
			case tc.want != "" && !strings.Contains(err.Error(), tc.want):
				t.Errorf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestStationLocationIsSet(t *testing.T) {
	if (StationLocation{}).IsSet() {
		t.Error("the zero value reports as set")
	}
	if !(StationLocation{Latitude: 30.6, Longitude: -87.05}).IsSet() {
		t.Error("a real position reports as unset")
	}
}

func TestSetStationLocationRoundTrips(t *testing.T) {
	s := newTestStore(t)
	if err := SetStationLocation(s, []byte(`{"latitude":30.6435,"longitude":-87.0545}`), "api"); err != nil {
		t.Fatalf("SetStationLocation: %v", err)
	}
	var got StationLocation
	if _, err := s.GetInto("station_location", &got); err != nil {
		t.Fatal(err)
	}
	if got.Latitude != 30.6435 || got.Longitude != -87.0545 {
		t.Errorf("stored %+v", got)
	}
	if err := SetStationLocation(s, []byte(`{"latitude":200}`), "api"); err == nil {
		t.Error("accepted a latitude of 200")
	}
}

// The load-bearing privacy property. publicview derives its published grid from
// the operator's grid_override and from nothing else, deliberately: a grid
// square is a disclosure and typing the one you are willing to publish is
// clearer consent than inferring it from a field filled in for another purpose.
//
// These coordinates are entered to pick counties, which is a private
// administrative act. If a future change wants "publish my grid, derived from my
// coordinates", that is a consent checkbox somebody designs on purpose — and
// this test is what will fail to make them notice.
func TestStationLocationDoesNotReachThePublicProjection(t *testing.T) {
	m := &Model{}
	before, err := json.Marshal(m.View(Sources{}).StationLocation)
	if err != nil {
		t.Fatal(err)
	}

	m.StationLocation = StationLocation{Latitude: 30.6435, Longitude: -87.0545}
	v := m.View(Sources{})

	// The ADMIN view carries it — an operator who typed it can see it.
	if v.StationLocation.Latitude != 30.6435 {
		t.Errorf("the admin view lost the position: %+v", v.StationLocation)
	}
	if string(before) == string(mustJSON(t, v.StationLocation)) {
		t.Error("the admin view did not change when a position was set")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
