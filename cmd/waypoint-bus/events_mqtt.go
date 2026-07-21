package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// events_mqtt.go closes D4: the bus republishes its events onto the local broker
// under <prefix>/<bus id>/<type> so waypointd's MQTT consumer ingests them as
// ordinary hub events (RFC-0004 persistence, RFC-0008 status, the Prompt-12
// dashboard badges) with no further plumbing — the stack's inter-process event
// plane is MQTT, not a new IPC or log scraping.
//
// It is best-effort and NEVER blocks the media path (RFC-0008 republisher posture):
// the router publishes to the in-process hub, which drops onto a full subscriber
// channel rather than blocking; a dedicated goroutine drains that channel and
// publishes fire-and-forget QoS 0, so a dead or wedged broker degrades only the
// event plane, never a voice frame.

// retainedBusEvents are the states a late-joining consumer must see current on
// (re)subscribe (RFC-0008): "bus down / owner offline" is retained so the
// dashboard shows a node still down. Voice/arbitration events are transient — not
// retained.
var retainedBusEvents = map[string]bool{"bus_down": true}

// recoveryBusEvents clear the retained down-state (no latching, RFC-0008): a
// "bus up" republishes an empty retained payload to the bus_down topic, so a
// reconnected owner stops showing as offline.
var recoveryBusEvents = map[string]bool{"bus_up": true}

// eventSink is the thin publish surface, injectable so the publisher's logic is
// tested without a broker. The real sink wraps paho; a test sink can block to
// prove the media path is unaffected.
type eventSink interface {
	Publish(topic string, retained bool, payload []byte)
	Close()
}

// eventPublisher drains a bus daemon's hub and republishes its events to MQTT.
type eventPublisher struct {
	sink   eventSink
	busID  string
	prefix string
}

func newEventPublisher(sink eventSink, busID, prefix string) *eventPublisher {
	if prefix == "" {
		prefix = config.DefaultBusTopicPrefix
	}
	return &eventPublisher{sink: sink, busID: busID, prefix: prefix}
}

// run subscribes to the hub and republishes every bus event until ctx is done, then
// clears this bus's retained down-state (so a detach/stop does not leave the
// dashboard showing a bus that no longer exists as down — coordinated with the
// Prompt-15 detach path, which also clears the topics from waypointd's side).
func (p *eventPublisher) run(ctx context.Context, h *hub.Hub) {
	ch, _, cancel := h.Subscribe()
	defer cancel()
	defer p.sink.Close()
	defer p.clearRetained()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-ch:
			if !ok {
				return
			}
			p.handle(e)
		}
	}
}

func (p *eventPublisher) handle(e hub.Event) {
	if !isBusEvent(e.Type) {
		return // the bus hub carries only bus events, but publish only the D4 set
	}
	payload, err := json.Marshal(e)
	if err != nil {
		return
	}
	p.sink.Publish(p.topic(e.Type), retainedBusEvents[e.Type], payload)
	if recoveryBusEvents[e.Type] {
		p.clearRetained() // recovery clears the retained down-state — no latching
	}
}

func (p *eventPublisher) topic(t string) string { return p.prefix + "/" + p.busID + "/" + t }

// clearRetained empties the retained down-state topic (RFC-0008 clear-on-silence).
func (p *eventPublisher) clearRetained() { p.sink.Publish(p.topic("bus_down"), true, nil) }

// isBusEvent gates which hub types cross onto the event plane: the bus/peer states
// (bus_busy, bus_voice_*, bus_down, bus_up, peer_*). Anything else a hub might
// carry is not a bus event and is not republished.
func isBusEvent(t string) bool {
	return strings.HasPrefix(t, "bus_") || strings.HasPrefix(t, "peer_")
}

// --- real paho sink ----------------------------------------------------------

// pahoSink is the best-effort MQTT sink. It holds its own auto-reconnecting
// connection (a distinct client id per bus) and publishes fire-and-forget at
// QoS 0 — the token is never awaited, so a broker hiccup drops the message rather
// than blocking the drain goroutine (RFC-0008 posture).
type pahoSink struct {
	client mqtt.Client
}

func newPahoSink(broker, busID string) *pahoSink {
	co := mqtt.NewClientOptions().
		AddBroker("tcp://" + broker).
		SetClientID("waypoint-bus-" + busID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetWriteTimeout(2 * time.Second). // never let a wedged socket hang the drain goroutine
		SetCleanSession(true)
	c := mqtt.NewClient(co)
	c.Connect() // do not wait — connect-retry brings it up; early publishes drop, which is fine
	return &pahoSink{client: c}
}

func (s *pahoSink) Publish(topic string, retained bool, payload []byte) {
	s.client.Publish(topic, 0, retained, payload) // QoS 0, token not awaited
}

func (s *pahoSink) Close() { s.client.Disconnect(100) }

// startEventPublisher wires a bus daemon's hub to MQTT when the rendered config
// carries a broker (D4). A nil block (tests/demo) means no publishing. Best-effort:
// it runs in its own goroutine and never touches the media path.
func startEventPublisher(ctx context.Context, cfg *config.BusMQTT, busID string, h *hub.Hub) {
	if cfg == nil || cfg.Broker == "" {
		return
	}
	p := newEventPublisher(newPahoSink(cfg.Broker, busID), busID, cfg.Prefix)
	go p.run(ctx, h)
}
