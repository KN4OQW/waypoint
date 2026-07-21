package main

import (
	"context"
	"sync"
	"testing"
	"time"

	imqtt "github.com/KN4OQW/waypoint/internal/mqtt"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// recordSink captures publishes; optionally blocks forever on Publish to prove the
// media path is unaffected by a wedged broker.
type recordSink struct {
	mu        sync.Mutex
	msgs      []sinkMsg
	block     bool
	ready     chan struct{} // closed once the first publish is entered
	readyOnce sync.Once
}

type sinkMsg struct {
	topic    string
	retained bool
	payload  []byte
}

func (s *recordSink) Publish(topic string, retained bool, payload []byte) {
	s.mu.Lock()
	s.msgs = append(s.msgs, sinkMsg{topic, retained, append([]byte(nil), payload...)})
	blk := s.block
	s.mu.Unlock()
	if s.ready != nil {
		s.readyOnce.Do(func() { close(s.ready) })
	}
	if blk {
		select {} // wedged broker: never returns
	}
}
func (s *recordSink) Close() {}
func (s *recordSink) all() []sinkMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sinkMsg(nil), s.msgs...)
}

// TestEventPublisherTopicAndRetained: events publish to <prefix>/<id>/<type>, with
// bus_down retained and voice/arbitration events not.
func TestEventPublisherTopicAndRetained(t *testing.T) {
	sink := &recordSink{}
	p := newEventPublisher(sink, "busA", "waypoint/bus")
	for _, e := range []hub.Event{
		{Type: "bus_voice_start", Source: "KN4OQW"},
		{Type: "bus_busy", Mode: "DMR", Source: "YSF"},
		{Type: "bus_down", Detail: "owner offline"},
	} {
		p.handle(e)
	}
	got := sink.all()
	want := []struct {
		topic    string
		retained bool
	}{
		{"waypoint/bus/busA/bus_voice_start", false},
		{"waypoint/bus/busA/bus_busy", false},
		{"waypoint/bus/busA/bus_down", true},
	}
	if len(got) != len(want) {
		t.Fatalf("published %d messages, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].topic != w.topic || got[i].retained != w.retained {
			t.Fatalf("msg %d = {%s retained=%v}, want {%s retained=%v}", i, got[i].topic, got[i].retained, w.topic, w.retained)
		}
	}
}

// TestEventPublisherClearsOnRecoveryAndShutdown: a bus_up clears the retained
// bus_down (empty retained payload), and run() clears it again on shutdown — no
// latching (RFC-0008), so a reconnected/detached bus never shows stuck-down.
func TestEventPublisherClearsOnRecovery(t *testing.T) {
	sink := &recordSink{}
	p := newEventPublisher(sink, "busA", "waypoint/bus")
	p.handle(hub.Event{Type: "bus_down"})
	p.handle(hub.Event{Type: "bus_up"})

	msgs := sink.all()
	// The bus_up must be followed by an empty-retained publish to the bus_down topic.
	var cleared bool
	for _, m := range msgs {
		if m.topic == "waypoint/bus/busA/bus_down" && m.retained && len(m.payload) == 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatalf("bus_up must clear the retained bus_down (empty retained publish); got %+v", msgs)
	}
}

func TestEventPublisherClearsOnShutdown(t *testing.T) {
	sink := &recordSink{}
	p := newEventPublisher(sink, "busA", "waypoint/bus")
	h := hub.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.run(ctx, h); close(done) }()
	// Retry-publish until the drain (once subscribed) records the bus_down.
	waitFor(func() bool {
		h.Publish(hub.Event{Type: "bus_down"})
		return len(sink.all()) >= 1
	})
	cancel()
	<-done
	last := sink.all()
	m := last[len(last)-1]
	if m.topic != "waypoint/bus/busA/bus_down" || !m.retained || len(m.payload) != 0 {
		t.Fatalf("shutdown must clear the retained bus_down; last publish = %+v", m)
	}
}

// TestEventPublisherNeverBlocksMediaPath: the media path only ever calls
// hub.Publish; with the event publisher's sink WEDGED (a broker that stopped
// reading), hub.Publish must stay non-blocking, so media-loop timing is
// unaffected. Structural: the router reads no hub state, so a blocked publisher
// cannot shift arbitration/hang timing — proven here by the hub staying prompt.
func TestEventPublisherNeverBlocksMediaPath(t *testing.T) {
	sink := &recordSink{block: true, ready: make(chan struct{})}
	p := newEventPublisher(sink, "busA", "waypoint/bus")
	h := hub.New()
	go p.run(context.Background(), h)

	// Retry-publish until the drain has subscribed and entered the (now blocking)
	// first publish — hub.Publish drops if no subscriber is registered yet.
	wedged := false
	for start := time.Now(); !wedged && time.Since(start) < 2*time.Second; {
		h.Publish(hub.Event{Type: "bus_down"})
		select {
		case <-sink.ready:
			wedged = true
		case <-time.After(5 * time.Millisecond):
		}
	}
	if !wedged {
		t.Fatal("the drain never entered the wedged sink")
	}

	// The media path publishes many events; assert it never blocks despite the wedge.
	start := time.Now()
	for i := 0; i < 5000; i++ {
		h.Publish(hub.Event{Type: "bus_voice_start"})
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("hub.Publish stalled behind a wedged broker (%s for 5000 events) — media path is not isolated", d)
	}
}

// TestBusEventRoundTrip is the end-to-end seam (D4): an event published by the bus
// flows through the topic+payload and back to a hub.Event via the consumer's
// mapping, intact — so the Prompt-12 dashboard (which listens on exactly these
// hub types) lights from a real published event with no translation layer.
func TestBusEventRoundTrip(t *testing.T) {
	sink := &recordSink{}
	newEventPublisher(sink, "busA", "waypoint/bus").handle(
		hub.Event{Type: "bus_busy", Mode: "DMR", Source: "YSF", Network: "Bus A", Detail: "busy: via YSF"})

	// The consumer maps the published payload 1:1 back to a hub.Event.
	msg := sink.all()[0]
	got, ok := imqtt.TranslateBusEvent(msg.payload)
	if !ok {
		t.Fatal("consumer failed to translate a published bus event")
	}
	if got.Type != "bus_busy" || got.Mode != "DMR" || got.Source != "YSF" || got.Detail != "busy: via YSF" {
		t.Fatalf("round-trip lost fields: %+v", got)
	}
}

func waitFor(cond func() bool) {
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
}
