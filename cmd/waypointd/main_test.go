package main

import (
	"bufio"
	"context"
	"encoding/json"
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
