package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

func newTestServer(demo bool) *server {
	return &server{hub: hub.New(), demo: demo, started: time.Now()}
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
