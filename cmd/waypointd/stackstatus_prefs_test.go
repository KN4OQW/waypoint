package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

// GET /api/update/stack is where the Updates panel reads the operator's update
// policy from, and it used to build that reply field by field while leaving
// AllowUnrevertable out — so the endpoint reported it false however the node was
// actually set. That silences the one warning in the UI whose whole job is to be
// temporary ("updates that cannot be undone are allowed … turn this back OFF once
// the node is up to date"), leaving the rollback guarantee off with nothing on
// screen saying so.
//
// s.stack is nil here, which is the no-apt-repo path: it returns before touching
// apt and after filling in the policy, so it exercises exactly the field copying
// under test without needing a package manager.
func TestStackStatusReportsAllowUnrevertable(t *testing.T) {
	for _, want := range []bool{true, false} {
		st, err := store.Open(":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		if err := st.Set("update", config.UpdatePrefs{
			Channel: config.ChannelStable, CheckEnabled: true,
			QuietWindow: config.DefaultQuietWindow, AllowUnrevertable: want,
		}, "test"); err != nil {
			t.Fatal(err)
		}

		s := &server{store: st}
		rec := httptest.NewRecorder()
		s.stackStatus(rec, httptest.NewRequest(http.MethodGet, "/api/update/stack", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		var got struct {
			Prefs config.ViewUpdate `json:"prefs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
		}
		if got.Prefs.AllowUnrevertable != want {
			t.Errorf("allow_unrevertable = %v, want %v (stored value must reach the panel)",
				got.Prefs.AllowUnrevertable, want)
		}
		// The fields that already worked must keep working.
		if !got.Prefs.CheckEnabled || got.Prefs.Channel != config.ChannelStable {
			t.Errorf("the rest of the policy did not survive: %+v", got.Prefs)
		}
	}
}
