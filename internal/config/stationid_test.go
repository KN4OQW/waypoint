package config

import (
	"reflect"
	"strings"
	"testing"
)

// renderedMM parses a model's MMDVM-Host INI so a test can assert on keys rather
// than on substring matches against the whole file.
func renderedMM(t *testing.T, m *Model) *INI {
	t.Helper()
	ini, err := ParseINI(strings.NewReader(m.RenderMMDVM()))
	if err != nil {
		t.Fatal(err)
	}
	return ini
}

// The regression this whole section exists for. A stock Pi-Star / WPSD card
// carries [CW Id] Enable=1 / Time=10; before StationID was modeled, Import
// dropped it on the floor and RenderMMDVM emitted no [CW Id] at all, so the
// generated INI fell back to MMDVM-Host's m_cwIdEnabled(false) — an imported node
// silently stopped identifying itself. The losslessness gate did not catch it
// because "CW Id" was absent from mmManaged (parity_roundtrip_test.go).
func TestStationIDImportPreservesIncumbentSetting(t *testing.T) {
	const incumbent = `[General]
Callsign=W1ABC

[CW Id]
Enable=1
Time=10

[Modem]
CWIdTXLevel=40
`
	ini, err := ParseINI(strings.NewReader(incumbent))
	if err != nil {
		t.Fatal(err)
	}
	got := stationIDFromINI(ini)
	want := StationID{Enable: true, TimeMins: "10", Callsign: "", TXLevel: "40"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("import lost the incumbent CW ID settings:\n want %+v\n  got %+v", want, got)
	}

	// And it must survive all the way back out to the generated INI, since that is
	// the file MMDVM-Host actually reads.
	m := &Model{General: General{Callsign: "W1ABC"}, StationID: got}
	out := renderedMM(t, m)
	if v := out.Get("CW Id", "Enable"); v != "1" {
		t.Errorf("[CW Id] Enable = %q, want \"1\" — an imported node must keep identifying", v)
	}
	if v := out.Get("CW Id", "Time"); v != "10" {
		t.Errorf("[CW Id] Time = %q, want \"10\"", v)
	}
	if v := out.Get("Modem", "CWIdTXLevel"); v != "40" {
		t.Errorf("[Modem] CWIdTXLevel = %q, want \"40\"", v)
	}
}

// An operator who deliberately turned identification off keeps it off: Enable=0
// is a real setting to preserve, not an absent one to default over.
func TestStationIDImportPreservesDisabled(t *testing.T) {
	ini, err := ParseINI(strings.NewReader("[CW Id]\nEnable=0\nTime=5\n"))
	if err != nil {
		t.Fatal(err)
	}
	got := stationIDFromINI(ini)
	if got.Enable {
		t.Error("an explicit Enable=0 must import as disabled, not be defaulted back on")
	}
	if got.TimeMins != "5" {
		t.Errorf("TimeMins = %q, want \"5\" — the interval is kept even while disabled", got.TimeMins)
	}
}

// A source INI with no [CW Id] section has nothing to preserve, so the
// fresh-store default applies (on, 10 minutes) rather than MMDVM-Host's "off".
func TestStationIDImportAbsentSectionTakesDefaults(t *testing.T) {
	ini, err := ParseINI(strings.NewReader("[General]\nCallsign=W1ABC\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := stationIDFromINI(ini), DefaultStationID(); !reflect.DeepEqual(got, want) {
		t.Fatalf("absent [CW Id] must seed the defaults:\n want %+v\n  got %+v", want, got)
	}
}

// Blank Callsign means "track the station callsign". The renderer proves it by
// omitting the key entirely — MMDVM-Host seeds m_cwIdCallsign from [General]
// Callsign and only overrides it when the key is present, so an emitted-but-empty
// key would be a key that does nothing.
func TestStationIDCallsignInheritance(t *testing.T) {
	m := &Model{
		General:   General{Callsign: "KN4OQW"},
		StationID: StationID{Enable: true, TimeMins: "10"},
	}
	if _, present := lookupCI(renderedMM(t, m).section("CW Id"), "Callsign"); present {
		t.Error("a blank override must omit [CW Id] Callsign, not emit an empty one")
	}
	if got := m.EffectiveIDCallsign(); got != "KN4OQW" {
		t.Errorf("EffectiveIDCallsign = %q, want the station callsign %q", got, "KN4OQW")
	}

	// An explicit override is emitted and wins.
	m.StationID.Callsign = "KN4OQW/R"
	if got := renderedMM(t, m).Get("CW Id", "Callsign"); got != "KN4OQW/R" {
		t.Errorf("[CW Id] Callsign = %q, want the override %q", got, "KN4OQW/R")
	}
	if got := m.EffectiveIDCallsign(); got != "KN4OQW/R" {
		t.Errorf("EffectiveIDCallsign = %q, want the override %q", got, "KN4OQW/R")
	}

	// Whitespace is not an override — it would key blank Morse.
	m.StationID.Callsign = "   "
	if got := m.EffectiveIDCallsign(); got != "KN4OQW" {
		t.Errorf("whitespace override must fall back to the station callsign, got %q", got)
	}
}

// A fresh store identifies. This is the behavior change the section makes: before
// it, no [CW Id] was rendered and MMDVM-Host's own default (off) applied.
func TestDefaultStationIDIdentifies(t *testing.T) {
	d := DefaultStationID()
	if !d.Enable {
		t.Error("identification must default ON — every incumbent Waypoint migrates from ships it on")
	}
	if err := ValidateStationID(d); err != nil {
		t.Errorf("the default policy must be valid: %v", err)
	}
	if d.Callsign != "" {
		t.Errorf("the default must inherit the station callsign, got override %q", d.Callsign)
	}
}

func TestValidateStationID(t *testing.T) {
	for _, tc := range []struct {
		name    string
		in      StationID
		wantErr bool
	}{
		{"default", DefaultStationID(), false},
		{"one minute", StationID{Enable: true, TimeMins: "1"}, false},
		{"hour bound", StationID{Enable: true, TimeMins: "60"}, false},
		{"zero interval", StationID{Enable: true, TimeMins: "0"}, true},
		{"negative", StationID{Enable: true, TimeMins: "-5"}, true},
		{"over bound", StationID{Enable: true, TimeMins: "61"}, true},
		{"not a number", StationID{Enable: true, TimeMins: "ten"}, true},
		{"blank while enabled", StationID{Enable: true, TimeMins: ""}, true},
		// A disabled section carries whatever interval the operator last had, so
		// toggling off must not require a valid one — otherwise turning
		// identification off and back on would silently reset the interval.
		{"blank while disabled", StationID{Enable: false, TimeMins: ""}, false},
		{"garbage while disabled", StationID{Enable: false, TimeMins: "ten"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateStationID(tc.in)
			if tc.wantErr && err == nil {
				t.Errorf("ValidateStationID(%+v) = nil, want an error", tc.in)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("ValidateStationID(%+v) = %v, want nil", tc.in, err)
			}
		})
	}
}

func TestStationIDStoreRoundTrip(t *testing.T) {
	s := memStore(t)
	m := fixture()
	m.StationID = StationID{Enable: true, TimeMins: "8", Callsign: "KN4OQW/P", TXLevel: "33"}
	if err := m.Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m.StationID, got.StationID) {
		t.Fatalf("StationID store round-trip lost data:\n want %+v\n  got %+v", m.StationID, got.StationID)
	}
	// Isolation: the StationID write must not have disturbed an unrelated section.
	if !reflect.DeepEqual(m.General, got.General) {
		t.Errorf("StationID round-trip perturbed General:\n want %+v\n  got %+v", m.General, got.General)
	}
}

// SetStationID validates the merged result and keeps SetSection's
// merge-and-reject-unknown contract.
func TestSetStationIDValidates(t *testing.T) {
	s := memStore(t)
	if err := s.Set("station_id", DefaultStationID(), "seed"); err != nil {
		t.Fatal(err)
	}

	// An out-of-range interval is rejected and the store is left untouched.
	if err := SetStationID(s, []byte(`{"time_mins":"0"}`), "test"); err == nil {
		t.Fatal("a zero-minute interval must be rejected")
	}
	m, _ := Load(s)
	if m.StationID.TimeMins != "10" {
		t.Errorf("a rejected save must not mutate the store: time_mins=%q", m.StationID.TimeMins)
	}

	// Unknown fields are rejected, as everywhere else in the config API.
	if err := SetStationID(s, []byte(`{"enabled":true}`), "test"); err == nil {
		t.Fatal("an unknown field must be rejected")
	}

	// A partial body merges: setting only the interval preserves Enable and TXLevel.
	if err := SetStationID(s, []byte(`{"time_mins":"5"}`), "test"); err != nil {
		t.Fatalf("valid write rejected: %v", err)
	}
	m, _ = Load(s)
	want := StationID{Enable: true, TimeMins: "5", Callsign: "", TXLevel: "50"}
	if !reflect.DeepEqual(m.StationID, want) {
		t.Fatalf("partial merge did not preserve the untouched fields:\n want %+v\n  got %+v", want, m.StationID)
	}

	// Disabling identification is always allowed, whatever the stored interval is.
	if err := SetStationID(s, []byte(`{"enable":false}`), "test"); err != nil {
		t.Fatalf("disabling identification rejected: %v", err)
	}
}
