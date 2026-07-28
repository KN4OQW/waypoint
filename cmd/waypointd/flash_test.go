package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/flash"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/modem"
)

// --- a source that needs no network --------------------------------------

type stubSource struct {
	cat   flash.Catalog
	image []byte
	err   error
}

func (s stubSource) Catalog(context.Context) (flash.Catalog, error) {
	if s.err != nil {
		return flash.Catalog{}, s.err
	}
	return s.cat, nil
}

func (s stubSource) Artifact(context.Context, flash.Variant) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.image, nil
}

func stubCatalog() flash.Catalog {
	return flash.Catalog{
		Version: "v1.6.1-wp1",
		Variants: []flash.Variant{{
			ID:       "hs_dual_hat-14m7456",
			BoardIDs: []string{"mmdvm_hs_dual_hat", "zumspot_duplex", "lonestar_dual"},
			TCXOHz:   14_745_600, Duplex: true, Transport: "gpio",
			LoadAddress: 0x08000000, URL: "https://example.invalid/dual.bin", SHA256: "cc",
		}},
	}
}

// flashTestServer builds a server with a flasher wired to a stub source. The
// engine's own hardware seams are left unset because no test here reaches them:
// what is under test is the API, the job bookkeeping and the exclusion rules.
func flashTestServer(t *testing.T, st config.HardwareState, src flash.Source, keyed bool) *server {
	t.Helper()
	s := hwTestServer(t, nil)
	s.hub = hub.New()
	if st.Identity != nil {
		if err := config.SetHardwareState(s.store, st, "test"); err != nil {
			t.Fatal(err)
		}
	}
	f := &flasher{
		source: src,
		hub:    s.hub,
		store:  s.store,
		subs:   map[chan flash.Progress]struct{}{},
		keyed:  keyed,
	}
	f.eng = &flash.Engine{Source: src}
	s.flash = f
	return s
}

func getFlash(t *testing.T, s *server) flashView {
	t.Helper()
	rec := httptest.NewRecorder()
	s.flashRoot(rec, httptest.NewRequest("GET", "/api/flash", nil))
	if rec.Code != 200 {
		t.Fatalf("GET /api/flash = %d: %s", rec.Code, rec.Body.String())
	}
	var v flashView
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// --- the panel -----------------------------------------------------------

func TestFlashViewNamesTheImageThatFitsTheDetectedModem(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)

	v := getFlash(t, s)
	if !v.Available {
		t.Fatalf("Available = false, reason %q", v.Reason)
	}
	if v.Match == nil || v.Match.ID != "hs_dual_hat-14m7456" {
		t.Fatalf("Match = %+v, want the dual-hat image", v.Match)
	}
	if v.Match.TCXOLabel != "14.7456 MHz" {
		t.Errorf("TCXOLabel = %q, want 14.7456 MHz", v.Match.TCXOLabel)
	}
}

// The refusal reason comes from the server, so the UI greys the button and
// prints what it is told rather than re-deriving the matching rules in
// JavaScript — the same rule the bus picker follows.
func TestFlashViewCarriesTheRefusalAndTheChoices(t *testing.T) {
	st := benchDetection()
	st.Identity.TCXOAssumed = true
	s := flashTestServer(t, st, stubSource{cat: stubCatalog()}, true)

	v := getFlash(t, s)
	if v.Match != nil {
		t.Error("offered a flash on an assumed oscillator")
	}
	if !strings.Contains(v.Reason, "oscillator") {
		t.Errorf("Reason = %q, want it to name the oscillator", v.Reason)
	}
	if len(v.Choices) == 0 {
		t.Error("a refusal with no choices leaves the operator nowhere to go")
	}
}

func TestFlashViewSaysWhenNothingHasBeenDetected(t *testing.T) {
	s := flashTestServer(t, config.HardwareState{}, stubSource{cat: stubCatalog()}, true)

	v := getFlash(t, s)
	if v.Match != nil || !strings.Contains(v.Reason, "detection") {
		t.Errorf("Match = %+v, Reason = %q; want a pointer at detection", v.Match, v.Reason)
	}
	// The catalog is still served: an operator can see what firmware exists
	// before they have plugged anything in.
	if !v.Available || len(v.Variants) == 0 {
		t.Error("the catalog should be visible before a modem is detected")
	}
}

// Firmware verification is not optional (RFC-0019 §2), so a node with no
// release key cannot flash — and must say that rather than looking broken.
func TestFlashViewExplainsAMissingReleaseKey(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, false)

	v := getFlash(t, s)
	if v.Available {
		t.Error("offered flashing on a node that cannot verify firmware")
	}
	if !strings.Contains(v.Reason, "key") {
		t.Errorf("Reason = %q, want it to name the missing key", v.Reason)
	}
}

func TestFlashViewSurfacesAnUnreachableCatalog(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{err: errors.New("no route to host")}, true)

	v := getFlash(t, s)
	if v.Available {
		t.Error("reported firmware as available with no catalog")
	}
	if !strings.Contains(v.Reason, "no route to host") {
		t.Errorf("Reason = %q, want the fetch error", v.Reason)
	}
}

// --- starting a flash ----------------------------------------------------

func postFlash(t *testing.T, s *server, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/flash", strings.NewReader(body))
	s.flashRoot(rec, req)
	return rec
}

func TestFlashStartRefusesWithoutADetectedModem(t *testing.T) {
	s := flashTestServer(t, config.HardwareState{}, stubSource{cat: stubCatalog()}, true)

	rec := postFlash(t, s, `{"stop_host":true}`)
	if rec.Code != 409 {
		t.Fatalf("POST /api/flash = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// Only one operation may hold the modem. A stack update health-gates on
// MMDVM-Host being up while a flash stops it, so the two together turn a good
// update into a spurious rollback.
func TestFlashStartRefusesWhileAnotherHardwareOperationRuns(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)

	release, busy := s.hwOps.acquire("stack update")
	if busy != "" {
		t.Fatal("the token was already held")
	}
	defer release()

	rec := postFlash(t, s, `{"stop_host":true}`)
	if rec.Code != 409 {
		t.Fatalf("POST /api/flash = %d, want 409", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "stack update") {
		t.Errorf("body = %s, want it to name what is running", rec.Body.String())
	}
}

func TestDetectionRefusesWhileAFlashRuns(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)

	release, _ := s.hwOps.acquire("firmware flash")
	defer release()

	rec := httptest.NewRecorder()
	s.hardwareDetect(rec, httptest.NewRequest("POST", "/api/hardware/detect", strings.NewReader(`{}`)))
	if rec.Code != 409 {
		t.Fatalf("POST /api/hardware/detect = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "firmware flash") {
		t.Errorf("body = %s, want it to name the flash", rec.Body.String())
	}
}

// The token must come back however the job ends, or the node needs a restart
// before it can be detected or updated again.
func TestTheHardwareTokenIsReleasedWhenAJobFails(t *testing.T) {
	s := flashTestServer(t, benchDetection(),
		stubSource{cat: stubCatalog(), err: errors.New("signature verification failed")}, true)

	rec := postFlash(t, s, `{"stop_host":true}`)
	if rec.Code != 202 {
		t.Fatalf("POST /api/flash = %d, want 202: %s", rec.Code, rec.Body.String())
	}
	waitFor(t, func() bool { return !s.flash.running() })

	if busy := s.hwOps.busy(); busy != "" {
		t.Errorf("the hardware token is still held by %q after a failed flash", busy)
	}
	job := s.flash.snapshot()
	if job == nil || job.Error == "" {
		t.Fatalf("job = %+v, want the failure recorded", job)
	}
}

func TestHwOpsReleaseIsIdempotent(t *testing.T) {
	var h hwOps
	release, _ := h.acquire("firmware flash")
	release()
	release() // a double release must not free somebody else's later claim

	other, busy := h.acquire("detection")
	if busy != "" {
		t.Fatalf("acquire after release reported %q busy", busy)
	}
	release()
	if h.busy() != "detection" {
		t.Error("the stale release freed a token it no longer owned")
	}
	other()
}

// --- progress ------------------------------------------------------------

// Byte-level progress must not reach the hub: every hub event is persisted to
// SQLite, and a 128 KB image is roughly five hundred chunks. Milestones do.
func TestOnlyMilestonesReachThePersistedEventHub(t *testing.T) {
	s := flashTestServer(t, benchDetection(),
		stubSource{cat: stubCatalog(), err: errors.New("nope")}, true)

	ch, _, cancel := s.hub.Subscribe()
	defer cancel()

	var events []hub.Event
	done := make(chan struct{})
	go func() {
		defer close(done)
		deadline := time.After(2 * time.Second)
		for {
			select {
			case e := <-ch:
				events = append(events, e)
				if e.Type == "flash_failed" || e.Type == "flash_ok" {
					return
				}
			case <-deadline:
				return
			}
		}
	}()

	// A hundred progress ticks, none of which may be published to the hub.
	for i := 0; i < 100; i++ {
		s.flash.publish(flash.Progress{Stage: flash.StageWriting, Done: i, Total: 100})
	}
	s.flash.event("flash_failed", "mmdvm_hs_dual_hat", "nope")
	<-done

	for _, e := range events {
		if e.Type != "flash_failed" {
			t.Errorf("hub carried a %q event; only milestones belong there", e.Type)
		}
	}
	if len(events) != 1 {
		t.Errorf("hub events = %d, want exactly the one milestone", len(events))
	}
}

// A browser on a slow link must not be able to stall a write to a
// microcontroller: a subscriber whose buffer is full is skipped, not waited on.
func TestASlowSubscriberDoesNotStallTheFlash(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)

	ch, cancel := s.flash.subscribe()
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10_000; i++ { // far beyond the channel's buffer
			s.flash.publish(flash.Progress{Stage: flash.StageWriting, Done: i, Total: 10_000})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing blocked on a subscriber that is not reading")
	}
	// And what the subscriber did get is real progress, not a corrupted value.
	select {
	case p := <-ch:
		if p.Stage != flash.StageWriting {
			t.Errorf("stage = %q, want writing", p.Stage)
		}
	default:
		t.Error("the subscriber received nothing at all")
	}
}

func TestProgressUpdatesTheJobSnapshot(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)
	s.flash.job = &flashJob{ID: "1", Started: time.Now().UTC()}

	s.flash.publish(flash.Progress{Stage: flash.StageWriting, Done: 512, Total: 2048, Detail: "hs_dual_hat"})

	job := s.flash.snapshot()
	if job.Stage != flash.StageWriting || job.Done != 512 || job.Total != 2048 {
		t.Errorf("job = %+v, want the writing progress", job)
	}
	if !job.Running() {
		t.Error("a job with no end time should read as running")
	}
}

// A client that connects mid-flash gets the current state immediately rather
// than waiting for the next chunk to be written.
func TestTheEventStreamOpensWithTheCurrentState(t *testing.T) {
	s := flashTestServer(t, benchDetection(), stubSource{cat: stubCatalog()}, true)
	s.flash.job = &flashJob{ID: "1", Stage: flash.StageVerifying, Done: 900, Total: 1000, Started: time.Now().UTC()}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	rec := httptest.NewRecorder()
	s.flashEvents(rec, httptest.NewRequest("GET", "/api/flash/events", nil).WithContext(ctx))

	body := rec.Body.String()
	if !strings.Contains(body, `"stage":"verifying"`) || !strings.Contains(body, `"done":900`) {
		t.Errorf("stream opened with %q, want the current job state", body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// --- configuration -------------------------------------------------------

func TestFirmwareCachePlacement(t *testing.T) {
	if got := firmwareCacheDir("/var/lib/fw", "/home/pi/waypoint/config.db"); got != "/var/lib/fw" {
		t.Errorf("explicit cache dir = %q", got)
	}
	if got := firmwareCacheDir("", "/home/pi/waypoint/config.db"); got != "/home/pi/waypoint/firmware" {
		t.Errorf("default cache dir = %q, want it beside the store", got)
	}
	if got := firmwareCacheDir("", ""); got != "" {
		t.Errorf("cache dir with no store = %q, want empty", got)
	}
}

func TestFlashAPIIsCleanlyAbsentWhenUnconfigured(t *testing.T) {
	s := hwTestServer(t, nil) // no flasher

	v := getFlash(t, s)
	if v.Available || v.Reason == "" {
		t.Errorf("view = %+v, want a clean 'not configured' answer", v)
	}
	rec := httptest.NewRecorder()
	s.flashEvents(rec, httptest.NewRequest("GET", "/api/flash/events", nil))
	if rec.Code != 501 {
		t.Errorf("GET /api/flash/events = %d, want 501", rec.Code)
	}
}

func TestLinesComeFromTheBoardTable(t *testing.T) {
	b, ok := modem.BoardByID("mmdvm_hs_dual_hat")
	if !ok {
		t.Fatal("the bench board is missing from the table")
	}
	cfg := flash.LinesForBoard(b).WithDefaults()
	if cfg.BOOT0 != flash.DefaultBOOT0Line || cfg.Reset != flash.DefaultResetLine {
		t.Errorf("lines = %d/%d, want the family default %d/%d",
			cfg.BOOT0, cfg.Reset, flash.DefaultBOOT0Line, flash.DefaultResetLine)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the condition")
}
