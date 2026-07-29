package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/cal"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// These cover the API's own decisions — the preconditions, the refusals and the
// separation of measuring from applying. The measurement itself is tested in
// internal/cal against a scripted modem; nothing here touches a serial port.

func calTestServer(t *testing.T) *server {
	t.Helper()
	s := hwTestServer(t, nil)
	s.hub = hub.New()
	s.cal = newCalibrator(s.hub, s.store)
	return s
}

func configureModem(t *testing.T, s *server) {
	t.Helper()
	body := `{"port":"/dev/ttyAMA0","rx_freq_hz":"438800000","tx_freq_hz":"438800000","uart_speed":"115200"}`
	if err := config.SetModem(s.store, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
}

func getCal(t *testing.T, s *server) calView {
	t.Helper()
	rec := httptest.NewRecorder()
	s.calStatus(rec, httptest.NewRequest("GET", "/api/cal", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/cal = %d: %s", rec.Code, rec.Body.String())
	}
	var v calView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestCalRefusesWithNothingConfigured checks the panel explains itself rather
// than offering a button that cannot work.
func TestCalRefusesWithNothingConfigured(t *testing.T) {
	s := calTestServer(t)
	v := getCal(t, s)
	if v.Available {
		t.Fatal("calibration reported itself available on a node with no modem")
	}
	if v.Reason == "" {
		t.Fatal("no reason was given for refusing")
	}
}

// TestCalNeedsAFrequency covers the precondition that is easy to miss: a modem
// is configured, but there is nothing to sweep around.
func TestCalNeedsAFrequency(t *testing.T) {
	s := calTestServer(t)
	if err := config.SetModem(s.store, []byte(`{"port":"/dev/ttyAMA0"}`), "test"); err != nil {
		t.Fatal(err)
	}
	v := getCal(t, s)
	if v.Available {
		t.Fatal("a node with no receive frequency reported calibration as available")
	}
	if !strings.Contains(v.Reason, "frequency") {
		t.Fatalf("reason = %q, want it to name the missing frequency", v.Reason)
	}
}

func TestCalAvailableOnceConfigured(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)
	v := getCal(t, s)
	if !v.Available {
		t.Fatalf("calibration unavailable: %s", v.Reason)
	}
	if v.Band != "70 cm" {
		t.Errorf("band = %q, want 70 cm", v.Band)
	}
	// The repeater workflow has never been run against a repeater board, and the
	// API says so rather than letting the UI imply otherwise.
	if v.RepeaterWorkflowVerified {
		t.Error("the API claims the repeater workflow is verified; no repeater board exists to have verified it on")
	}
}

// --- applying is a separate act ------------------------------------------

func TestApplyRefusesWithoutASweep(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)

	rec := httptest.NewRecorder()
	s.calApply(rec, httptest.NewRequest("POST", "/api/cal/apply", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("POST /api/cal/apply = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// storeSweep plants a finished sweep, as runSweep would.
func storeSweep(t *testing.T, s *server, best int) {
	t.Helper()
	res := &cal.Result{
		BaseHz: 438_800_000,
		Points: []cal.Point{
			{OffsetHz: -500, FreqHz: 438_799_500, Heard: false},
			{OffsetHz: 0, FreqHz: 438_800_000, Heard: true, Scored: true, Frames: 12, BER: 4.2},
			{OffsetHz: best, FreqHz: uint32(438_800_000 + best), Heard: true, Scored: true, Frames: 12, BER: 0.31},
		},
	}
	res.Best = &res.Points[2]
	st := config.CalibrationState{Result: res, RanAt: time.Now().UTC().Format(time.RFC3339), Mode: "dmr"}
	if err := config.SetCalibrationState(s.store, st, "test"); err != nil {
		t.Fatal(err)
	}
}

func TestApplyWritesBothOffsetsWithProvenance(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)
	storeSweep(t, s, 400)

	rec := httptest.NewRecorder()
	s.calApply(rec, httptest.NewRequest("POST", "/api/cal/apply", strings.NewReader(`{}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/cal/apply = %d: %s", rec.Code, rec.Body.String())
	}

	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	// One oscillator, both paths (RFC-0021 §5).
	if m.Modem.RXOffset != "400" || m.Modem.TXOffset != "400" {
		t.Fatalf("offsets are rx=%q tx=%q, want both 400", m.Modem.RXOffset, m.Modem.TXOffset)
	}

	st, err := config.GetCalibrationState(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if st.AppliedOffsetHz == nil || *st.AppliedOffsetHz != 400 {
		t.Fatal("the applied offset was not recorded")
	}
	if st.AppliedAt == "" {
		t.Error("the applied time was not recorded")
	}
}

// TestApplyRefusesAnUnmeasuredOffset keeps this route honest about what it is.
// It records a measurement; an arbitrary number posted here would be stored
// with a measurement's provenance.
func TestApplyRefusesAnUnmeasuredOffset(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)
	storeSweep(t, s, 400)

	for _, tc := range []struct {
		name string
		body string
	}{
		{"a frequency the sweep never visited", `{"offset_hz":1234}`},
		{"a frequency the sweep visited but never heard", `{"offset_hz":-500}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.calApply(rec, httptest.NewRequest("POST", "/api/cal/apply", strings.NewReader(tc.body)))
			if rec.Code != 400 {
				t.Fatalf("POST /api/cal/apply = %d, want 400: %s", rec.Code, rec.Body.String())
			}
		})
	}

	m, _ := config.Load(s.store)
	if m.Modem.RXOffset == "1234" {
		t.Fatal("an unmeasured offset reached the config")
	}
}

func TestApplyAcceptsAnyMeasuredPoint(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)
	storeSweep(t, s, 400)

	// The operator may disagree with the winner and pick another point on the
	// curve; what they may not do is invent one.
	rec := httptest.NewRecorder()
	s.calApply(rec, httptest.NewRequest("POST", "/api/cal/apply", strings.NewReader(`{"offset_hz":0}`)))
	if rec.Code != 200 {
		t.Fatalf("POST /api/cal/apply = %d: %s", rec.Code, rec.Body.String())
	}
	m, _ := config.Load(s.store)
	if m.Modem.RXOffset != "0" {
		t.Fatalf("rx_offset = %q, want 0", m.Modem.RXOffset)
	}
}

// --- transmitting --------------------------------------------------------

func TestTransmitRefusesAnUnknownTest(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)

	rec := httptest.NewRecorder()
	s.calTransmit(rec, httptest.NewRequest("POST", "/api/cal/transmit", strings.NewReader(`{"test":"whatever"}`)))
	if rec.Code != 400 {
		t.Fatalf("POST /api/cal/transmit = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// TestTransmitRefusesAnIllegalFrequencyBeforeTouchingTheModem is the ordering
// that matters: the refusal must come before the session opens, or the node has
// already been taken off the air to be told no.
func TestTransmitRefusesAnIllegalFrequencyBeforeTouchingTheModem(t *testing.T) {
	s := calTestServer(t)
	body := `{"port":"/dev/ttyAMA0","rx_freq_hz":"436500000","tx_freq_hz":"436500000"}`
	if err := config.SetModem(s.store, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.calTransmit(rec, httptest.NewRequest("POST", "/api/cal/transmit", strings.NewReader(`{"test":"dmr_deviation"}`)))
	if rec.Code != 400 {
		t.Fatalf("POST /api/cal/transmit = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "satellite") {
		t.Errorf("the refusal should say which segment it is: %s", rec.Body.String())
	}
	if s.hwOps.busy() != "" {
		t.Errorf("the hardware token was left held by a refused request: %q", s.hwOps.busy())
	}
}

// --- one hardware operation at a time ------------------------------------

func TestSweepWaitsForNoOne(t *testing.T) {
	s := calTestServer(t)
	configureModem(t, s)

	release, busy := s.hwOps.acquire("firmware flash")
	if busy != "" {
		t.Fatal("the token was already held")
	}
	defer release()

	rec := httptest.NewRecorder()
	s.calSweep(rec, httptest.NewRequest("POST", "/api/cal/sweep", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("POST /api/cal/sweep during a flash = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "firmware flash") {
		t.Errorf("the refusal should name what is running: %s", rec.Body.String())
	}
}

func TestCancelWithNothingRunning(t *testing.T) {
	s := calTestServer(t)
	rec := httptest.NewRecorder()
	s.calCancel(rec, httptest.NewRequest("POST", "/api/cal/cancel", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("POST /api/cal/cancel = %d, want 409", rec.Code)
	}
}
