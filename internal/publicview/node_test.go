package publicview

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/status"
)

// TestPublishableTalkgroup is the destination-side counterpart to
// publishableCallsign, and it exists because rendering the page turned up a real
// leak: with the demo feed running, the reach card's "ACTIVE NOW" slot showed
// W4FLA — a callsign — because hub.Event.Dest is only sometimes a talkgroup.
//
// The suppress list cannot help here. It excludes a *source*; this is the
// destination, so a third party's identity would reach an anonymous page purely as
// a side effect of somebody calling them.
func TestPublishableTalkgroup(t *testing.T) {
	for _, tc := range []struct {
		name   string
		in     string
		want   string
		wantOK bool
	}{
		{"DMR group call", "TG 31123", "TG 31123", true},
		{"lowercase prefix", "tg 91", "tg 91", true},
		{"padded", "  TG 3100  ", "TG 3100", true},

		{"DMR private call ID", "3112345", "", false},
		{"D-Star destination callsign", "W4FLA", "", false},
		{"YSF destination", "AE4GHI", "", false},
		{"reflector label", "FL-TREASURE", "", false},
		{"all-stations", "ALL", "", false},
		{"empty", "", "", false},
		{"prefix without a space is not a talkgroup", "TGIF_Network", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := publishableTalkgroup(tc.in)
			if ok != tc.wantOK || got != tc.want {
				t.Errorf("publishableTalkgroup(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestReachCardNeverNamesTheCallee drives the same rule through BuildNode, which
// is where it would actually have failed.
func TestReachCardNeverNamesTheCallee(t *testing.T) {
	set := DefaultSettings()
	set.Enabled = true
	m := &config.Model{}
	m.General.Callsign = "K4SRC"

	for _, tc := range []struct {
		dest string
		want string
	}{
		{"TG 31123", "TG 31123"}, // a group: published
		{"W4FLA", ""},            // a station being called: withheld
		{"3112345", ""},          // a private-call ID: withheld
	} {
		live := &status.Status{TX: &status.Transmission{Mode: "DMR", Source: "KK4WXT", Dest: tc.dest}}
		got := BuildNode(m, set, live, nil, nil)
		if got.Talkgroup != tc.want {
			t.Errorf("with a transmission to %q, reach card talkgroup = %q, want %q", tc.dest, got.Talkgroup, tc.want)
		}
		// The source callsign is on the live status and must never cross into the
		// reach card under any destination.
		if got.Callsign != "K4SRC" {
			t.Errorf("reach card callsign = %q, want the node's own", got.Callsign)
		}
	}
}

// TestTalkgroupToggleOff: the toggle still wins over a publishable group.
func TestTalkgroupToggleOff(t *testing.T) {
	set := DefaultSettings()
	set.ShowTalkgroup = false
	live := &status.Status{TX: &status.Transmission{Dest: "TG 31123"}}
	if got := BuildNode(&config.Model{}, set, live, nil, nil); got.Talkgroup != "" {
		t.Errorf("talkgroup = %q with its toggle off", got.Talkgroup)
	}
}

func TestBuildNodeSurvivesNilInputs(t *testing.T) {
	set := DefaultSettings()
	got := BuildNode(nil, set, nil, nil, nil)
	if got.Callsign != "" || got.Talkgroup != "" {
		t.Errorf("BuildNode(nil, ...) = %+v, want an empty card", got)
	}
}

func TestEnabledModesReportsOnlyWhatIsOn(t *testing.T) {
	got := enabledModes(config.Modes{DMR: true, YSF: true, M17: true})
	want := []string{"DMR", "YSF", "M17"}
	if len(got) != len(want) {
		t.Fatalf("modes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("modes = %v, want %v", got, want)
		}
	}
}
