package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/modem"
	"github.com/KN4OQW/waypoint/internal/store"
)

func hwTestServer(t *testing.T, m *config.Model) *server {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if m == nil {
		m = &config.Model{}
	}
	if err := m.Save(st, "test"); err != nil {
		t.Fatal(err)
	}
	return &server{store: st}
}

func benchDetection() config.HardwareState {
	id := modem.Identity{
		Port: "/dev/ttyAMA0", Transport: "gpio", Baud: 115200, Protocol: 1,
		Description: "MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a",
		HWType:      "MMDVM_HS_Dual_Hat", Firmware: "1.6.1",
		TCXOHz: 14_745_600, Duplex: true,
		Candidates: []string{"mmdvm_hs_dual_hat", "zumspot_duplex", "lonestar_dual"},
		Modes:      modem.Version{Protocol: 1}.Modes(),
	}
	return config.HardwareState{Identity: &id, CheckedAt: "2026-07-27T12:00:00Z"}
}

func getHardware(t *testing.T, s *server) hardwareView {
	t.Helper()
	rec := httptest.NewRecorder()
	s.hardware(rec, httptest.NewRequest("GET", "/api/hardware", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/hardware = %d: %s", rec.Code, rec.Body.String())
	}
	var v hardwareView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestHardwareViewServesThePickerOnANodeThatHasNeverBeenProbed(t *testing.T) {
	// The picker has to be there before detection has ever run — that is the
	// node where an operator most needs to say what is attached.
	v := getHardware(t, hwTestServer(t, nil))
	if len(v.Boards) != len(modem.Boards) {
		t.Fatalf("board table has %d rows, want %d", len(v.Boards), len(modem.Boards))
	}
	if v.Detected.Identity != nil {
		t.Error("an unprobed node reported a modem")
	}
	if len(v.Warnings) != 0 {
		t.Error("warned with no detection to warn from")
	}
	var verified int
	for _, b := range v.Boards {
		if b.Verified {
			verified++
		}
		if b.TCXOHz != 0 && b.TCXOLabel == "" {
			t.Errorf("board %s has an oscillator with no label operators would recognise", b.ID)
		}
	}
	if verified != 1 {
		t.Errorf("%d boards claim bench verification; only the reference board may", verified)
	}
}

func TestHardwareViewSurfacesEveryDisagreement(t *testing.T) {
	s := hwTestServer(t, &config.Model{
		General: config.General{Duplex: true},
		Modem:   config.Modem{Port: "/dev/ttyACM0"},
	})
	if err := config.SetHardwareState(s.store, benchDetection(), "test"); err != nil {
		t.Fatal(err)
	}
	v := getHardware(t, s)
	if len(v.Warnings) == 0 {
		t.Fatal("a node configured for a port its modem is not on was not warned")
	}
	var found bool
	for _, w := range v.Warnings {
		if w.Field == "modem.port" && w.Severity == config.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %+v, missing the port mismatch", v.Warnings)
	}
}

func TestHardwareAdoptWritesTheConfigAndReportsWhatChanged(t *testing.T) {
	s := hwTestServer(t, nil)
	if err := config.SetHardwareState(s.store, benchDetection(), "test"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.hardwareAdopt(rec, httptest.NewRequest("POST", "/api/hardware/adopt",
		strings.NewReader(`{"board_id":"mmdvm_hs_dual_hat"}`)))
	if rec.Code != 200 {
		t.Fatalf("adopt = %d: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Adopted  config.Adoption `json:"adopted"`
		Hardware hardwareView    `json:"hardware"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Adopted.Changed) == 0 {
		t.Error("adoption reported no changes")
	}
	if resp.Hardware.Configured.Port != "/dev/ttyAMA0" || resp.Hardware.Configured.Board != "mmdvm_hs_dual_hat" {
		t.Errorf("configured = %+v", resp.Hardware.Configured)
	}
	if resp.Hardware.Configured.TCXOLabel != "14.7456 MHz" {
		t.Errorf("TCXO label = %q, want the label printed on the part", resp.Hardware.Configured.TCXOLabel)
	}
	// The whole node is now consistent, so nothing should be complained about.
	if len(resp.Hardware.Warnings) != 0 {
		t.Errorf("adopting a detection left warnings behind: %+v", resp.Hardware.Warnings)
	}
	// And the crossing must be recorded, so the UI can tell "detected and
	// running it" from "detected and running something else" (#136).
	st, err := config.GetHardwareState(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if st.AdoptedAt == "" || st.AdoptedDescription == "" {
		t.Errorf("adoption was not recorded on the detection: %+v", st)
	}
}

func TestHardwareAdoptAsksWhenTheBoardIsAmbiguous(t *testing.T) {
	s := hwTestServer(t, nil)
	if err := config.SetHardwareState(s.store, benchDetection(), "test"); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.hardwareAdopt(rec, httptest.NewRequest("POST", "/api/hardware/adopt", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("adopt with no board = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Candidates) < 2 {
		t.Fatalf("candidates = %v; the picker needs the narrowed list to show", resp.Candidates)
	}
}

func TestHardwareAdoptWithNothingDetected(t *testing.T) {
	rec := httptest.NewRecorder()
	hwTestServer(t, nil).hardwareAdopt(rec, httptest.NewRequest("POST", "/api/hardware/adopt", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("adopt before any detection = %d, want 409", rec.Code)
	}
}

func TestHardwareRoutesRejectTheWrongMethod(t *testing.T) {
	s := hwTestServer(t, nil)
	for name, call := range map[string]func(*httptest.ResponseRecorder){
		"GET /api/hardware": func(rec *httptest.ResponseRecorder) {
			s.hardware(rec, httptest.NewRequest("POST", "/api/hardware", nil))
		},
		"POST /api/hardware/detect": func(rec *httptest.ResponseRecorder) {
			s.hardwareDetect(rec, httptest.NewRequest("GET", "/api/hardware/detect", nil))
		},
		"POST /api/hardware/adopt": func(rec *httptest.ResponseRecorder) {
			s.hardwareAdopt(rec, httptest.NewRequest("GET", "/api/hardware/adopt", nil))
		},
	} {
		rec := httptest.NewRecorder()
		call(rec)
		if rec.Code != 405 {
			t.Errorf("%s with the wrong method = %d, want 405", name, rec.Code)
		}
	}
}

// The three routes are in allRoutes (auth_test.go), so the pre-claim and
// post-claim gate matrices assert they default to denied. That matters most for
// detect, which can stop MMDVM-Host and take the node off the air.
