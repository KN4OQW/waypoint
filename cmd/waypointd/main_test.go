package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/events"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
	"github.com/KN4OQW/waypoint/internal/store"
)

func newTestServer(demo bool) *server {
	return &server{hub: hub.New(), demo: demo, started: time.Now()}
}

// backfillDefaults seeds the native LCD section for a store created before it
// existed, and leaves an operator's existing LCD row untouched.
func TestBackfillLCD(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &server{store: st}

	// A store with no lcd row gets DefaultLCD seeded.
	if err := s.backfillDefaults(); err != nil {
		t.Fatal(err)
	}
	var got config.LCD
	if found, err := st.GetInto("lcd", &got); err != nil || !found {
		t.Fatalf("lcd not backfilled: found=%v err=%v", found, err)
	}
	if !reflect.DeepEqual(got, config.DefaultLCD()) {
		t.Fatalf("backfill did not seed DefaultLCD:\n want %+v\n  got %+v", config.DefaultLCD(), got)
	}

	// An operator's existing LCD row survives a later backfill unchanged.
	custom := config.LCD{Enabled: true, I2CBus: "/dev/i2c-9", I2CAddress: "0x20", Rows: "2", Cols: "16"}
	if err := st.Set("lcd", custom, "test"); err != nil {
		t.Fatal(err)
	}
	if err := s.backfillDefaults(); err != nil {
		t.Fatal(err)
	}
	var after config.LCD
	if _, err := st.GetInto("lcd", &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, custom) {
		t.Fatalf("backfill overwrote an existing LCD row:\n want %+v\n  got %+v", custom, after)
	}
}

// TestApplyStopsRunningBridgeUnit: apply stops any retired cross-mode bridge daemon
// (MMDVM_CM) that is still active, and leaves inactive ones alone. This closes the
// stale-daemon-on-disable defect by construction — a bridge enabled under the old
// surface no longer lingers once the surface is retired. The systemctl calls are
// faked (there is no systemd under `go test`).
func TestApplyStopsRunningBridgeUnit(t *testing.T) {
	// Only ysf2dmr is "running"; the other four bridges are inactive.
	active := map[string]bool{"waypoint-ysf2dmr.service": true}
	var stops []string
	orig := systemctlRun
	systemctlRun = func(args ...string) ([]byte, error) {
		switch args[0] {
		case "is-active":
			unit := args[len(args)-1]
			if active[unit] {
				return []byte("active\n"), nil
			}
			return []byte("inactive\n"), fmt.Errorf("exit status 3") // is-active non-zero when not active
		case "stop":
			stops = append(stops, args[1])
			return nil, nil
		default: // restart of the always-on gateway units
			return nil, nil
		}
	}
	t.Cleanup(func() { systemctlRun = orig })

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	s := &server{
		store: st,
		paths: config.Paths{
			MMDVM: dir + "/MMDVM-Host.ini", DMRGateway: dir + "/DMRGateway.ini",
			YSFGateway: dir + "/YSFGateway.ini", P25Gateway: dir + "/P25Gateway.ini",
			NXDNGateway: dir + "/NXDNGateway.ini", DStarGateway: dir + "/dstargateway.cfg",
			M17Gateway: dir + "/M17Gateway.ini",
		},
	}

	rec := httptest.NewRecorder()
	s.configApply(rec, httptest.NewRequest("POST", "/api/config/apply", nil))
	if rec.Code != 200 {
		t.Fatalf("apply returned %d: %s", rec.Code, rec.Body.String())
	}

	// Exactly the one active bridge was stopped; the inactive ones were not.
	if !reflect.DeepEqual(stops, []string{"waypoint-ysf2dmr.service"}) {
		t.Fatalf("apply stopped %v, want only the active bridge waypoint-ysf2dmr.service", stops)
	}

	var body struct {
		Applied   bool     `json:"applied"`
		Restarted []string `json:"restarted"`
		Stopped   []string `json:"stopped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("apply response is not JSON: %v", err)
	}
	if !body.Applied || !reflect.DeepEqual(body.Stopped, []string{"waypoint-ysf2dmr.service"}) {
		t.Fatalf("apply response should report the stopped bridge, got %+v", body)
	}
	// A bridge unit is never in the restart set — it is not a render target.
	for _, u := range body.Restarted {
		if strings.Contains(u, "ysf2dmr") || strings.Contains(u, "dmr2ysf") || strings.Contains(u, "nxdn2dmr") {
			t.Fatalf("a retired bridge unit must never be restarted, got %v", body.Restarted)
		}
	}
}

func TestHealthHandler(t *testing.T) {
	s := newTestServer(true)
	rec := httptest.NewRecorder()
	s.health(rec, httptest.NewRequest("GET", "/api/health", nil))

	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var body healthResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body.Status != "ok" {
		t.Errorf("expected status ok, got %q", body.Status)
	}
	if !body.Demo {
		t.Error("demo mode must be labeled in health output")
	}
	if body.Version == "" {
		t.Error("version must never be empty")
	}
}

func TestEventsStreamsLiveTail(t *testing.T) {
	s := newTestServer(false)
	// A pre-subscribe event lands in the hub backlog. Since /api/events is now a
	// pure live tail (history is served by /api/history, RFC-0004), it must NOT be
	// replayed to the SSE client — only events published after subscribe are sent.
	s.hub.Publish(hub.Event{Time: time.Now(), Type: "mode", Mode: "IDLE"})

	req := httptest.NewRequest("GET", "/api/events", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() { s.events(rec, req); close(done) }()

	// live event after subscribe
	time.Sleep(50 * time.Millisecond)
	s.hub.Publish(hub.Event{Time: time.Now(), Type: "rf_voice_start", Mode: "DMR", Source: "KN4OQW", Dest: "TG 9"})
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	var got []string
	sc := bufio.NewScanner(strings.NewReader(rec.Body.String()))
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			got = append(got, strings.TrimPrefix(sc.Text(), "data: "))
		}
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 event (live only, no backlog replay), got %d: %v", len(got), got)
	}
	var e hub.Event
	if err := json.Unmarshal([]byte(got[0]), &e); err != nil {
		t.Fatalf("live event is not JSON: %v", err)
	}
	if e.Source != "KN4OQW" {
		t.Errorf("unexpected live event: %+v", e)
	}
}

// GET /api/history serves the persistent record newest-first, honours the type
// filter, and returns [] (not null) when the store is empty (RFC-0004).
func TestHistoryEndpoint(t *testing.T) {
	s := newTestServer(false)
	ev, err := events.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer ev.Close()
	s.evStore = ev
	base := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	if err := ev.Insert([]hub.Event{
		{Time: base, Type: "mode", Mode: "DMR"},
		{Time: base.Add(time.Second), Type: "rf_voice_start", Source: "KN4OQW", Dest: "TG 91"},
	}); err != nil {
		t.Fatal(err)
	}

	// Unfiltered: newest-first.
	rec := httptest.NewRecorder()
	s.history(rec, httptest.NewRequest("GET", "/api/history", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	var got []hub.Event
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(got) != 2 || got[0].Type != "rf_voice_start" {
		t.Fatalf("want 2 events newest-first, got %+v", got)
	}

	// Type filter is exact.
	rec = httptest.NewRecorder()
	s.history(rec, httptest.NewRequest("GET", "/api/history?type=mode", nil))
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if len(got) != 1 || got[0].Type != "mode" {
		t.Fatalf("type filter: want 1 mode event, got %+v", got)
	}

	// A bad since is a 400.
	rec = httptest.NewRecorder()
	s.history(rec, httptest.NewRequest("GET", "/api/history?since=not-a-time", nil))
	if rec.Code != 400 {
		t.Errorf("bad since: want 400, got %d", rec.Code)
	}

	// Empty store serializes as [], never null.
	empty, _ := events.Open(":memory:")
	defer empty.Close()
	s.evStore = empty
	rec = httptest.NewRecorder()
	s.history(rec, httptest.NewRequest("GET", "/api/history", nil))
	if b := strings.TrimSpace(rec.Body.String()); b != "[]" {
		t.Errorf("empty history must be [], got %q", b)
	}
}

// hostIPv4 returns the first non-loopback IPv4, or "no-ip" on error / none.
func TestHostIPv4(t *testing.T) {
	ok := func() ([]net.Addr, error) {
		return []net.Addr{
			&net.IPNet{IP: net.IPv6loopback},
			&net.IPNet{IP: net.ParseIP("127.0.0.1")},
			&net.IPNet{IP: net.ParseIP("192.168.1.42")},
		}, nil
	}
	if got := hostIPv4(ok); got != "192.168.1.42" {
		t.Errorf("hostIPv4 = %q, want 192.168.1.42", got)
	}
	loopbackOnly := func() ([]net.Addr, error) {
		return []net.Addr{&net.IPAddr{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	if got := hostIPv4(loopbackOnly); got != "no-ip" {
		t.Errorf("loopback-only hostIPv4 = %q, want no-ip", got)
	}
	failing := func() ([]net.Addr, error) { return nil, net.UnknownNetworkError("boom") }
	if got := hostIPv4(failing); got != "no-ip" {
		t.Errorf("failing hostIPv4 = %q, want no-ip", got)
	}
}

// lcdInfo snapshots the config-derived tokens: callsign, DMR id, enabled modes
// (short keys), and version.
func TestLCDInfo(t *testing.T) {
	m := &config.Model{
		General: config.General{Callsign: "KN4OQW", ID: "3180202"},
		Modes:   config.Modes{DMR: true, YSF: true}, // others off
	}
	started := time.Now()
	info := lcdInfo(m, "1.2.3", started)
	if info.Callsign != "KN4OQW" || info.DMRID != "3180202" || info.Version != "1.2.3" || !info.Started.Equal(started) {
		t.Fatalf("lcdInfo scalars: %+v", info)
	}
	if !reflect.DeepEqual(info.Modes, []string{"DMR", "YSF"}) {
		t.Fatalf("lcdInfo modes = %v, want [DMR YSF]", info.Modes)
	}
}

// startLCD starts a subscriber only when the config enables the driver.
func TestStartLCD(t *testing.T) {
	s := &server{hub: hub.New(), started: time.Now()}

	if s.startLCD(context.Background(), &config.Model{LCD: config.LCD{Enabled: false}}) {
		t.Fatal("startLCD started with the driver disabled")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stop the renderer goroutine when the test ends
	m := &config.Model{LCD: config.DefaultLCD()}
	m.LCD.Enabled = true
	if !s.startLCD(ctx, m) {
		t.Fatal("startLCD did not start with the driver enabled")
	}
}

// reloadLCD makes the renderer track config changes: it starts a stopped-and-now-
// enabled driver, is a no-op when the LCD section is unchanged, and stops a
// now-disabled driver — all without a daemon restart (the apply path).
func TestReloadLCD(t *testing.T) {
	s := &server{hub: hub.New(), started: time.Now()}

	// Disabled → nothing running.
	s.reloadLCD(&config.Model{LCD: config.LCD{Enabled: false}})
	if s.lcdCancel != nil {
		t.Fatal("reloadLCD started a renderer for a disabled driver")
	}

	// Enable via reload → renderer starts. The done channel identifies this run.
	on := &config.Model{LCD: config.DefaultLCD()}
	on.LCD.Enabled = true
	s.reloadLCD(on)
	if s.lcdCancel == nil {
		t.Fatal("reloadLCD did not start the enabled renderer")
	}
	firstDone := s.lcdDone

	// Same config again → no restart (same run, done channel unchanged and open).
	same := &config.Model{LCD: on.LCD}
	s.reloadLCD(same)
	if s.lcdDone != firstDone {
		t.Error("reloadLCD restarted the renderer for an unchanged config")
	}

	// Change a page → restart: a new run (new done channel) and the old one stopped.
	edited := &config.Model{LCD: on.LCD}
	edited.LCD.Pages = append([]config.LCDPage(nil), on.LCD.Pages...)
	edited.LCD.Pages[0].Duration = "11"
	s.reloadLCD(edited)
	if s.lcdDone == firstDone {
		t.Error("reloadLCD did not restart the renderer after a page edit")
	}
	select {
	case <-firstDone: // closed → the previous renderer stopped and released the device
	default:
		t.Error("reloadLCD left the previous renderer running after a restart")
	}

	// Disable via reload → renderer stops.
	off := &config.Model{LCD: edited.LCD}
	off.LCD.Enabled = false
	s.reloadLCD(off)
	if s.lcdCancel != nil {
		t.Error("reloadLCD did not stop the renderer when disabled")
	}
}

// GET /api/overrides reports the override records that shape the current render,
// names the override root, and returns [] (not null) when nothing is overridden
// (RFC-0005 / issue #2).
func TestOverridesEndpoint(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	etc := t.TempDir()
	ovr := t.TempDir()
	s := &server{
		store: st,
		paths: config.Paths{
			MMDVM: filepath.Join(etc, "MMDVM-Host.ini"), DMRGateway: filepath.Join(etc, "DMRGateway.ini"),
			YSFGateway: filepath.Join(etc, "YSFGateway.ini"), P25Gateway: filepath.Join(etc, "P25Gateway.ini"),
			NXDNGateway: filepath.Join(etc, "NXDNGateway.ini"), DStarGateway: filepath.Join(etc, "dstargateway.cfg"),
			M17Gateway: filepath.Join(etc, "M17Gateway.ini"), OverridesDir: ovr,
		},
	}

	// No fragments yet: empty (non-null) report.
	rec := httptest.NewRecorder()
	s.overridesView(rec, httptest.NewRequest("GET", "/api/overrides", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var empty struct {
		Dir       string           `json:"dir"`
		Overrides []config.Applied `json:"overrides"`
		Warnings  []string         `json:"warnings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if empty.Dir != ovr {
		t.Errorf("dir = %q, want %q", empty.Dir, ovr)
	}
	if empty.Overrides == nil || len(empty.Overrides) != 0 {
		t.Errorf("expected empty (non-null) overrides, got %#v", empty.Overrides)
	}

	// Drop in a fragment; it must appear in the report with its provenance.
	if err := os.MkdirAll(filepath.Join(ovr, "mmdvm.d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ovr, "mmdvm.d", "10-local.conf"), []byte("[General]\nTimeout=777"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	s.overridesView(rec, httptest.NewRequest("GET", "/api/overrides", nil))
	var got struct {
		Overrides []config.Applied `json:"overrides"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if len(got.Overrides) != 1 {
		t.Fatalf("expected 1 override record, got %d: %+v", len(got.Overrides), got.Overrides)
	}
	a := got.Overrides[0]
	if a.Daemon != "mmdvm" || a.Section != "General" || a.Key != "Timeout" || a.New != "777" || a.Source != "10-local.conf" {
		t.Errorf("override record wrong: %+v", a)
	}
}

// The connection-profiles API (RFC-0006 / issue #3): capture the current store as
// a profile, list it, export a scrubbed artifact, import it back, activate it
// (which re-renders + restarts), and delete it.
func TestProfilesAPI(t *testing.T) {
	// Fake systemctl so activate's re-render/restart works under `go test`.
	orig := systemctlRun
	systemctlRun = func(args ...string) ([]byte, error) {
		if args[0] == "is-active" {
			return []byte("inactive\n"), fmt.Errorf("exit status 3")
		}
		return nil, nil
	}
	t.Cleanup(func() { systemctlRun = orig })

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := config.InitProfiles(st); err != nil {
		t.Fatal(err)
	}
	// Seed a real config incl. a secret, so export scrubbing is exercised.
	if err := st.Set("pocsag", config.POCSAG{AuthKey: "TOP-SECRET", Frequency: "439987500"}, "seed"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &server{
		store: st,
		paths: config.Paths{
			MMDVM: dir + "/MMDVM-Host.ini", DMRGateway: dir + "/DMRGateway.ini",
			YSFGateway: dir + "/YSFGateway.ini", P25Gateway: dir + "/P25Gateway.ini",
			NXDNGateway: dir + "/NXDNGateway.ini", DStarGateway: dir + "/dstargateway.cfg",
			M17Gateway: dir + "/M17Gateway.ini",
		},
	}

	// Capture the current store as "BM DMR".
	rec := httptest.NewRecorder()
	s.profilesView(rec, httptest.NewRequest("POST", "/api/profiles", strings.NewReader(`{"name":"BM DMR"}`)))
	if rec.Code != 201 {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	// List shows it.
	rec = httptest.NewRecorder()
	s.profilesView(rec, httptest.NewRequest("GET", "/api/profiles", nil))
	var list []profileSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "BM DMR" || !list[0].Active {
		t.Fatalf("list wrong: %+v", list)
	}

	// Export: scrubbed artifact, no secret.
	rec = httptest.NewRecorder()
	s.profilesRouter(rec, httptest.NewRequest("GET", "/api/profiles/BM%20DMR/export", nil))
	if rec.Code != 200 {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "TOP-SECRET") {
		t.Errorf("export leaked the secret:\n%s", rec.Body.String())
	}
	artifact := rec.Body.String()

	// Import the artifact under a new name (edit the name field).
	imported := strings.Replace(artifact, `"name":"BM DMR"`, `"name":"Imported"`, 1)
	rec = httptest.NewRecorder()
	s.profilesImport(rec, httptest.NewRequest("POST", "/api/profiles/import", strings.NewReader(imported)))
	if rec.Code != 201 {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}

	// Importing a name that already exists is a 409 without ?overwrite=1.
	rec = httptest.NewRecorder()
	s.profilesImport(rec, httptest.NewRequest("POST", "/api/profiles/import", strings.NewReader(imported)))
	if rec.Code != 409 {
		t.Errorf("duplicate import should 409, got %d", rec.Code)
	}

	// Activate the imported profile — its blank AuthKey must PRESERVE the store's.
	rec = httptest.NewRecorder()
	s.profilesRouter(rec, httptest.NewRequest("POST", "/api/profiles/Imported/activate", nil))
	if rec.Code != 200 {
		t.Fatalf("activate: %d %s", rec.Code, rec.Body.String())
	}
	var pg config.POCSAG
	if _, err := st.GetInto("pocsag", &pg); err != nil {
		t.Fatal(err)
	}
	if pg.AuthKey != "TOP-SECRET" {
		t.Errorf("activation of an imported profile blanked the store's secret: %q", pg.AuthKey)
	}

	// Delete.
	rec = httptest.NewRecorder()
	s.profilesRouter(rec, httptest.NewRequest("DELETE", "/api/profiles/Imported", nil))
	if rec.Code != 200 {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.profilesRouter(rec, httptest.NewRequest("POST", "/api/profiles/Imported/activate", nil))
	if rec.Code != 404 {
		t.Errorf("activate of deleted profile should 404, got %d", rec.Code)
	}
}

// The config-import API (RFC-0007 / issue #4): scan a mounted incumbent card
// (preview, no write), then apply (bulk-write to the store). Also the upload path.
func TestImportAPI(t *testing.T) {
	const mmdvm = "[General]\nCallsign=W1ABC\nId=3161234\nDuplex=1\n[Info]\nRXFrequency=438800000\nTXFrequency=431000000\n[DMR]\nEnable=1\nId=3161234\n[System Fusion]\nEnable=1\n"
	const dmrgw = "[DMR Network 1]\nName=BM_United_States_3103\nEnabled=1\nAddress=3103.master.brandmeister.network\nPassword=passw0rd\nPort=62031\nId=3161234\n"

	// A mounted card at <dir>/etc.
	card := t.TempDir()
	etc := filepath.Join(card, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "mmdvmhost"), []byte(mmdvm), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "dmrgateway"), []byte(dmrgw), 0o600); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &server{store: st}

	// Scan (dir): report + preview, and the store is NOT written.
	rec := httptest.NewRecorder()
	s.importScan(rec, httptest.NewRequest("POST", "/api/import/scan", strings.NewReader(`{"dir":"`+card+`"}`)))
	if rec.Code != 200 {
		t.Fatalf("scan: %d %s", rec.Code, rec.Body.String())
	}
	var scan struct {
		Report  config.MigrationReport `json:"report"`
		Preview map[string]any         `json:"preview"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &scan); err != nil {
		t.Fatal(err)
	}
	if scan.Report.Platform != "Pi-Star" && !strings.HasPrefix(scan.Report.Platform, "Pi-Star") && scan.Report.Platform != "unknown" {
		t.Logf("platform: %q", scan.Report.Platform)
	}
	if len(scan.Report.Modes) == 0 {
		t.Errorf("scan report has no modes")
	}
	// Scan wrote nothing: the store has no general section yet.
	if _, ok, _ := st.Get("general"); ok {
		t.Error("scan must not write to the store")
	}
	// The preview must not leak the network password.
	if strings.Contains(rec.Body.String(), "passw0rd") {
		t.Errorf("scan preview leaked a secret")
	}

	// Apply (dir): the store now carries the callsign.
	rec = httptest.NewRecorder()
	s.importApply(rec, httptest.NewRequest("POST", "/api/import/apply", strings.NewReader(`{"dir":"`+card+`"}`)))
	if rec.Code != 200 {
		t.Fatalf("apply: %d %s", rec.Code, rec.Body.String())
	}
	m, err := config.Load(st)
	if err != nil {
		t.Fatal(err)
	}
	if m.General.Callsign != "W1ABC" || m.General.ID != "3161234" {
		t.Errorf("import did not populate the store: %+v", m.General)
	}
	if !m.Modes.DMR || !m.Modes.YSF {
		t.Errorf("modes not imported: %+v", m.Modes)
	}
	if len(m.Networks) != 1 || m.Networks[0].Password != "passw0rd" {
		t.Errorf("network not imported with secret: %+v", m.Networks)
	}

	// Upload path: same mapping from multipart files.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "mmdvmhost")
	fw.Write([]byte(mmdvm))
	mw.Close()
	st2, _ := store.Open(":memory:")
	defer st2.Close()
	s2 := &server{store: st2}
	req := httptest.NewRequest("POST", "/api/import/apply", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec = httptest.NewRecorder()
	s2.importApply(rec, req)
	if rec.Code != 200 {
		t.Fatalf("upload apply: %d %s", rec.Code, rec.Body.String())
	}
	m2, _ := config.Load(st2)
	if m2.General.Callsign != "W1ABC" {
		t.Errorf("upload import did not populate the store: %+v", m2.General)
	}
}

// GET /api/status returns the aggregator's live snapshot, and the liveness probe
// emits gateway_up/gateway_down which the aggregator folds — the #5 "kill/restart
// a gateway → truth" path, exercised with a faked systemctl (RFC-0008).
func TestStatusAndLivenessProbe(t *testing.T) {
	// dmrgateway "inactive", the rest "active".
	down := map[string]bool{"waypoint-dmrgateway.service": true}
	orig := systemctlRun
	systemctlRun = func(args ...string) ([]byte, error) {
		if args[0] == "is-active" {
			unit := args[len(args)-1]
			if down[unit] {
				return []byte("inactive\n"), fmt.Errorf("exit status 3")
			}
			return []byte("active\n"), nil
		}
		return nil, nil
	}
	t.Cleanup(func() { systemctlRun = orig })

	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	// A minimal enabled config so RenderTargets yields gateway units to probe.
	if err := st.Set("modes", config.Modes{DMR: true, YSF: true}, "seed"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s := &server{
		hub: hub.New(), store: st, agg: status.New(status.DefaultTxTTL),
		paths: config.Paths{
			MMDVM: dir + "/MMDVM-Host.ini", DMRGateway: dir + "/DMRGateway.ini",
			YSFGateway: dir + "/YSFGateway.ini", P25Gateway: dir + "/P25Gateway.ini",
			NXDNGateway: dir + "/NXDNGateway.ini", DStarGateway: dir + "/dstargateway.cfg",
			M17Gateway: dir + "/M17Gateway.ini",
		},
	}

	// Feed the aggregator directly from the hub, as main() wires it.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.agg.Run(ctx, s.hub, 10*time.Millisecond)

	// One probe pass emits gateway_up/down per unit.
	pctx, pcancel := context.WithCancel(context.Background())
	go s.runLivenessProbe(pctx, 20*time.Millisecond)
	// Wait for the fold to see the dmrgateway-down event.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := s.agg.Snapshot()
		if g, ok := snap.Gateways["dmrgateway"]; ok && !g.Up {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	pcancel()

	snap := s.agg.Snapshot()
	if g, ok := snap.Gateways["dmrgateway"]; !ok || g.Up {
		t.Errorf("dmrgateway should be probed down: %+v", snap.Gateways)
	}
	if g, ok := snap.Gateways["dmrgateway"]; ok && g.Up {
		t.Errorf("dmrgateway up=true, want down")
	}

	// GET /api/status serves the snapshot.
	rec := httptest.NewRecorder()
	s.statusView(rec, httptest.NewRequest("GET", "/api/status", nil))
	if rec.Code != 200 {
		t.Fatalf("status: %d", rec.Code)
	}
	var got status.Status
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Mode == "" {
		t.Error("status mode should be set (IDLE at minimum)")
	}
}
