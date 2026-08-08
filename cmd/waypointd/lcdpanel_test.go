package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
)

// stubDetect points the bus sweep at a fixed answer for the duration of a test.
// found == nil means "nothing answered", which is a distinct outcome from an
// error and has to be exercised as one.
func stubDetect(t *testing.T, found *config.PanelFound) {
	t.Helper()
	prev := detectPanel
	detectPanel = func() (config.PanelFound, error) {
		if found == nil {
			return config.PanelFound{}, hd44780.ErrNoPanel
		}
		return *found, nil
	}
	t.Cleanup(func() { detectPanel = prev })
}

func lcdTestServer(t *testing.T, m *config.Model) *server {
	t.Helper()
	s := hwTestServer(t, m)
	s.hub = hub.New() // adopting starts the renderer, which subscribes
	t.Cleanup(func() {
		s.lcdMu.Lock()
		defer s.lcdMu.Unlock()
		s.stopLCD()
	})
	return s
}

// Detection records what answered, and the record reaches the surface the LCD tab
// reads. This is the whole of #136 in one test: a panel that answers must end up
// somewhere the configuration UI can see it.
func TestLCDDetectRecordsThePanelOnTheHardwareSurface(t *testing.T) {
	s := lcdTestServer(t, nil)
	stubDetect(t, &config.PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"})

	rec := httptest.NewRecorder()
	s.lcdDetect(rec, httptest.NewRequest("POST", "/api/lcd/detect", nil))
	if rec.Code != 200 {
		t.Fatalf("POST /api/lcd/detect = %d: %s", rec.Code, rec.Body.String())
	}
	var v hardwareView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Panel.Found == nil || v.Panel.Found.Bus != "/dev/i2c-1" || v.Panel.Found.Addr != "0x27" {
		t.Fatalf("detect response panel = %+v", v.Panel)
	}
	if v.Panel.CheckedAt == "" {
		t.Fatal("detect did not stamp checked_at")
	}
	// And it is durable — the tab loads GET /api/hardware, not the detect response.
	if got := getHardware(t, s).Panel; got.Found == nil {
		t.Fatal("the detection did not survive into GET /api/hardware")
	}
}

// "We looked and there was nothing" is recorded rather than discarded: it is the
// state that explains a dark panel, and it is not the same as never looking.
func TestLCDDetectRecordsThatNothingAnswered(t *testing.T) {
	s := lcdTestServer(t, nil)
	stubDetect(t, nil)

	rec := httptest.NewRecorder()
	s.lcdDetect(rec, httptest.NewRequest("POST", "/api/lcd/detect", nil))
	if rec.Code != 200 {
		t.Fatalf("POST /api/lcd/detect = %d: %s", rec.Code, rec.Body.String())
	}
	got := getHardware(t, s).Panel
	if got.Found != nil {
		t.Fatalf("found = %+v, want nil", got.Found)
	}
	if got.CheckedAt == "" {
		t.Fatal("an empty answer was not recorded as a look")
	}
}

// Adopting is the crossing: the lcd section gains the panel and the driver comes
// on, and the response names what changed.
func TestLCDAdoptWritesTheDetectionIntoTheConfig(t *testing.T) {
	s := lcdTestServer(t, nil)
	stubDetect(t, &config.PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"})
	s.lcdDetect(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/lcd/detect", nil))

	rec := httptest.NewRecorder()
	s.lcdAdopt(rec, httptest.NewRequest("POST", "/api/lcd/adopt", nil))
	if rec.Code != 200 {
		t.Fatalf("POST /api/lcd/adopt = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Adopted config.PanelAdoption `json:"adopted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Adopted.Changed) == 0 {
		t.Fatal("adopt reported no changes on a node with no panel configured")
	}

	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if !m.LCD.Enabled || m.LCD.I2CBus != "/dev/i2c-1" || m.LCD.I2CAddress != "0x27" {
		t.Fatalf("lcd config after adopt = %+v", m.LCD)
	}
	// The adoption is stamped, so the tab can say "you are running what we found"
	// rather than offering the same panel forever.
	if got := getHardware(t, s).Panel; got.AdoptedAt == "" || got.AdoptedPanel != "/dev/i2c-1@0x27" {
		t.Fatalf("panel state after adopt = %+v", got)
	}
}

// Nothing detected, nothing to adopt — and 409 rather than 500, because the node
// is working exactly as designed and the caller has a way to proceed (detect).
func TestLCDAdoptRefusesWhenNothingHasBeenDetected(t *testing.T) {
	s := lcdTestServer(t, nil)
	rec := httptest.NewRecorder()
	s.lcdAdopt(rec, httptest.NewRequest("POST", "/api/lcd/adopt", nil))
	if rec.Code != 409 {
		t.Fatalf("adopt with no detection = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if m.LCD.Enabled {
		t.Fatal("a refused adopt still enabled the driver")
	}
}

// Both routes are POST-only. A GET that probed hardware would be a link, a
// prefetch or a crawler poking the I2C bus.
func TestLCDPanelRoutesRefuseOtherMethods(t *testing.T) {
	s := lcdTestServer(t, nil)
	for _, tc := range []struct {
		path string
		h    func(w *httptest.ResponseRecorder)
	}{
		{"/api/lcd/detect", func(w *httptest.ResponseRecorder) {
			s.lcdDetect(w, httptest.NewRequest("GET", "/api/lcd/detect", nil))
		}},
		{"/api/lcd/adopt", func(w *httptest.ResponseRecorder) {
			s.lcdAdopt(w, httptest.NewRequest("GET", "/api/lcd/adopt", nil))
		}},
	} {
		rec := httptest.NewRecorder()
		tc.h(rec)
		if rec.Code != 405 {
			t.Fatalf("GET %s = %d, want 405", tc.path, rec.Code)
		}
	}
}

// The startup probe stands down when the driver is already on. There is nothing
// to offer about a panel the operator has declared, and the bus it is on is being
// painted.
func TestProbeForPanelStandsDownWhenTheDriverIsEnabled(t *testing.T) {
	s := lcdTestServer(t, nil)
	probed := false
	prev := detectPanel
	detectPanel = func() (config.PanelFound, error) {
		probed = true
		return config.PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"}, nil
	}
	t.Cleanup(func() { detectPanel = prev })

	s.probeForPanel(&config.Model{LCD: config.LCD{Enabled: true}})
	if probed {
		t.Fatal("the startup probe swept a bus the renderer is driving")
	}
	if got := getHardware(t, s).Panel; got.CheckedAt != "" {
		t.Fatalf("panel state = %+v, want untouched", got)
	}
}

// With the driver off, the probe runs and records — but writes nothing to the lcd
// section. The store is the operator's (RFC-0001); detection only makes the offer.
func TestProbeForPanelRecordsWithoutEnablingAnything(t *testing.T) {
	s := lcdTestServer(t, nil)
	stubDetect(t, &config.PanelFound{Bus: "/dev/i2c-1", Addr: "0x27"})

	s.probeForPanel(&config.Model{LCD: config.LCD{Enabled: false}})

	if got := getHardware(t, s).Panel; got.Found == nil {
		t.Fatal("the startup probe did not record what it found")
	}
	m, err := config.Load(s.store)
	if err != nil {
		t.Fatal(err)
	}
	if m.LCD.Enabled || m.LCD.I2CBus != "" {
		t.Fatalf("the probe wrote the lcd section: %+v", m.LCD)
	}
}
