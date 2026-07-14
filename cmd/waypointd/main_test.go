package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
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

func TestEventsStreamsBacklogAndLive(t *testing.T) {
	s := newTestServer(false)
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
	if len(got) != 2 {
		t.Fatalf("expected 2 events (backlog + live), got %d: %v", len(got), got)
	}
	var e hub.Event
	if err := json.Unmarshal([]byte(got[1]), &e); err != nil {
		t.Fatalf("live event is not JSON: %v", err)
	}
	if e.Source != "KN4OQW" {
		t.Errorf("unexpected live event: %+v", e)
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
