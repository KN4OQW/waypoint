package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

func busTestServer(t *testing.T, m *config.Model) *server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := m.Save(st, "test"); err != nil {
		t.Fatal(err)
	}
	return &server{store: st}
}

// TestBusesValidateReasons: the dry-run validator returns the human reason the UI
// greys a mode out with — the exact strings RFC-0003 §2 specifies, straight from
// the one validator (never re-derived).
func TestBusesValidateReasons(t *testing.T) {
	s := busTestServer(t, &config.Model{
		Buses:       []config.Bus{{ID: "b1", Name: "Bus 1", Enabled: true}},
		Attachments: []config.Attachment{{BusID: "b1", Mode: config.ModeDMR}},
	})

	call := func(buses []config.Bus, atts []config.Attachment) busValidateResponse {
		body, _ := json.Marshal(busValidateRequest{Buses: buses, Attachments: atts})
		req := httptest.NewRequest("POST", "/api/buses/validate", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		s.busesValidate(rec, req)
		if rec.Code != 200 {
			t.Fatalf("validate status %d: %s", rec.Code, rec.Body.String())
		}
		var resp busValidateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	oneBus := []config.Bus{{ID: "b1", Name: "Bus 1", Enabled: true}}
	twoBuses := []config.Bus{{ID: "b1", Enabled: true}, {ID: "b2", Enabled: true}}

	// DMR + NXDN is a valid reframe pair.
	if r := call(oneBus, []config.Attachment{{BusID: "b1", Mode: config.ModeDMR}, {BusID: "b1", Mode: config.ModeNXDN}}); !r.OK {
		t.Fatalf("DMR+NXDN should be valid, got reason %q", r.Reason)
	}

	// DMR + D-Star has no converter — the exact reason the UI shows.
	r := call(oneBus, []config.Attachment{{BusID: "b1", Mode: config.ModeDMR}, {BusID: "b1", Mode: config.ModeDStar}})
	if r.OK || !strings.Contains(r.Reason, "no converter for D-Star<->DMR") {
		t.Fatalf("DMR+D-Star should be refused with the converter reason, got ok=%v reason=%q", r.OK, r.Reason)
	}

	// The same mode attached to two buses is refused with the multi-bus reason.
	r = call(twoBuses, []config.Attachment{
		{BusID: "b1", Mode: config.ModeDMR},
		{BusID: "b2", Mode: config.ModeDMR},
	})
	if r.OK || !strings.Contains(r.Reason, "more than one bus") {
		t.Fatalf("a mode on two buses should be refused, got ok=%v reason=%q", r.OK, r.Reason)
	}
}

// TestBusesMigrateEndpoint: the endpoint seeds buses from the bridges, persists
// them, surfaces warnings, and is idempotent.
func TestBusesMigrateEndpoint(t *testing.T) {
	s := busTestServer(t, &config.Model{
		Networks: []config.Network{{Name: "BM", Address: "m.example"}},
		YSF2DMR:  config.YSF2DMR{Enable: true, Master: "m.example", TG: "91"},
	})

	migrate := func() busMigrateResponse {
		req := httptest.NewRequest("POST", "/api/buses/migrate", nil)
		rec := httptest.NewRecorder()
		s.busesMigrate(rec, req)
		if rec.Code != 200 {
			t.Fatalf("migrate status %d: %s", rec.Code, rec.Body.String())
		}
		var resp busMigrateResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	r := migrate()
	if !r.OK || r.Buses != 1 || r.Attachments != 2 {
		t.Fatalf("first migration should seed 1 bus + 2 attachments, got %+v", r)
	}

	// It persisted: the store now has a migrated bus.
	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Buses) != 1 || len(m.Attachments) != 2 {
		t.Fatalf("migration did not persist: %d buses, %d attachments", len(m.Buses), len(m.Attachments))
	}

	// Re-running is idempotent: ok=false with the already-migrated warning.
	r = migrate()
	if r.OK || len(r.Warnings) == 0 || !strings.Contains(strings.Join(r.Warnings, " "), "already exists") {
		t.Fatalf("second migration should be a no-op with a warning, got %+v", r)
	}
}

// TestBusesMigrateWarnsUnmatchedMaster: a bridge master with no Networks[] entry
// migrates but warns the operator to create the network first (Task 4 copy).
func TestBusesMigrateWarnsUnmatchedMaster(t *testing.T) {
	s := busTestServer(t, &config.Model{
		YSF2DMR: config.YSF2DMR{Enable: true, Master: "nomatch.example", TG: "9"},
	})
	req := httptest.NewRequest("POST", "/api/buses/migrate", nil)
	rec := httptest.NewRecorder()
	s.busesMigrate(rec, req)
	var resp busMigrateResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK {
		t.Fatalf("migration should still proceed, got %+v", resp)
	}
	if !strings.Contains(strings.Join(resp.Warnings, " "), "create that network first") {
		t.Fatalf("want the create-network warning, got %v", resp.Warnings)
	}
}
