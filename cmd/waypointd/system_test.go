package main

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
)

// flagsFor builds an mqttFlags without going through flag.Parse, naming exactly
// which flags the operator "typed". The real constructor reads flag.Visit, which a
// test cannot drive per-case (the flag package has one global command line), so
// the set is injected here and newMQTTFlags's only untested line is the Visit call
// itself.
func flagsFor(set map[string]bool, broker, name, user, pass, statusPrefix, busPrefix string) mqttFlags {
	if set == nil {
		set = map[string]bool{}
	}
	return mqttFlags{
		broker: broker, name: name, user: user, pass: pass,
		statusPrefix: statusPrefix, busPrefix: busPrefix, set: set,
	}
}

// TestMQTTPrecedenceFlagSet is D1's first case: a flag the operator typed wins
// over the store, and says which store key it is shadowing.
func TestMQTTPrecedenceFlagSet(t *testing.T) {
	m := &config.Model{}
	m.MQTT = config.MQTT{Host: "10.0.0.5", Port: "1883", Name: "stored-name", BusPrefix: "stored/bus"}

	f := flagsFor(map[string]bool{"mqtt-broker": true, "mqtt-name": true},
		"192.168.1.9:1884", "flag-name", "", "", "waypoint/status", "waypoint/bus")
	shadowed := f.resolve(m)

	if m.MQTT.Host != "192.168.1.9" || m.MQTT.Port != "1884" {
		t.Errorf("broker = %s:%s, want the flag's 192.168.1.9:1884", m.MQTT.Host, m.MQTT.Port)
	}
	if m.MQTT.Name != "flag-name" {
		t.Errorf("name = %q, want the flag's", m.MQTT.Name)
	}
	// An UNSET flag must not touch the store, even though it holds a value.
	if m.MQTT.BusPrefix != "stored/bus" {
		t.Errorf("bus prefix = %q, want the store's (its flag was not set)", m.MQTT.BusPrefix)
	}
	if len(shadowed) != 2 {
		t.Fatalf("shadowed = %v, want the two set flags", shadowed)
	}
	for _, name := range shadowed {
		if mqttFlagNames[name] == "" {
			t.Errorf("flag %q shadows no named store key; the warning would be blank", name)
		}
	}
}

// TestMQTTPrecedenceFlagUnset is the case that made flag.Visit necessary rather
// than "differs from the default": the store must be able to move a value ONTO
// the flag's own default, and off it, with no flag set either way.
func TestMQTTPrecedenceFlagUnset(t *testing.T) {
	m := &config.Model{}
	m.MQTT = config.MQTT{Host: "10.0.0.5", Port: "1884", Name: "node7", StatusPrefix: "site/status", BusPrefix: "site/bus"}
	stored := m.MQTT

	// Every flag carries its compiled default and none was typed.
	f := flagsFor(nil, "127.0.0.1:1883", "mmdvm", "", "", "waypoint/status", "waypoint/bus")
	if shadowed := f.resolve(m); len(shadowed) != 0 {
		t.Errorf("shadowed = %v, want none when no flag was set", shadowed)
	}
	if m.MQTT != stored {
		t.Errorf("unset flags overwrote the store: got %+v want %+v", m.MQTT, stored)
	}
	if got := m.MQTT.Broker(); got != "10.0.0.5:1884" {
		t.Errorf("broker = %q, want the store's", got)
	}

	// The store deliberately set to the flag's own default must survive too — the
	// "differs from the default" shortcut would silently ignore this write.
	m.MQTT.Name = "mmdvm"
	f.resolve(m)
	if m.MQTT.Name != "mmdvm" {
		t.Errorf("name = %q, want the store's explicit mmdvm", m.MQTT.Name)
	}
}

// TestMQTTPrecedenceSectionAbsent is D1's third case: no store row at all falls
// through to the compiled defaults, which is what a fresh dev run and -demo have.
func TestMQTTPrecedenceSectionAbsent(t *testing.T) {
	m := &config.Model{} // zero mqtt section
	f := flagsFor(nil, "127.0.0.1:1883", "mmdvm", "", "", "waypoint/status", "waypoint/bus")
	f.resolve(m)

	if got := m.MQTT.Broker(); got != "127.0.0.1:1883" {
		t.Errorf("broker = %q, want the compiled default", got)
	}
	if got := m.MQTT.HostName(); got != config.DefaultMQTTName {
		t.Errorf("name = %q, want %q", got, config.DefaultMQTTName)
	}
	if got := m.MQTT.StatusTopicPrefix(); got != config.DefaultStatusPrefix {
		t.Errorf("status prefix = %q, want %q", got, config.DefaultStatusPrefix)
	}
	if got := m.MQTT.BusTopicPrefix(); got != config.DefaultBusTopicPrefix {
		t.Errorf("bus prefix = %q, want %q", got, config.DefaultBusTopicPrefix)
	}
}

// TestMQTTPasswordFlagEnablesAuth pins the reading of -mqtt-pass: an operator who
// puts a password on the command line means to authenticate, and leaving Auth off
// would render the credentials into no file at all, making the override silently
// do nothing.
func TestMQTTPasswordFlagEnablesAuth(t *testing.T) {
	m := &config.Model{}
	f := flagsFor(map[string]bool{"mqtt-pass": true, "mqtt-user": true},
		"127.0.0.1:1883", "mmdvm", "wp", "s3cret", "waypoint/status", "waypoint/bus")
	f.resolve(m)
	if !m.MQTT.Auth {
		t.Errorf("auth = false after -mqtt-pass; the credentials would render nowhere")
	}
	if m.MQTT.Username != "wp" || m.MQTT.Password != "s3cret" {
		t.Errorf("credentials = %q/%q, want the flags'", m.MQTT.Username, m.MQTT.Password)
	}
}

func TestSplitBroker(t *testing.T) {
	cases := []struct{ in, host, port string }{
		{"127.0.0.1:1883", "127.0.0.1", "1883"},
		{"broker.local:8883", "broker.local", "8883"},
		{"  10.0.0.2:1884  ", "10.0.0.2", "1884"},
		{"bare-host", "bare-host", ""}, // no colon: keep the stored/compiled port
		{"[::1]:1883", "[::1]", "1883"},
	}
	for _, tc := range cases {
		host, port := splitBroker(tc.in)
		if host != tc.host || port != tc.port {
			t.Errorf("splitBroker(%q) = %q,%q want %q,%q", tc.in, host, port, tc.host, tc.port)
		}
	}
}

// TestEffectivePathsFollowsStore is D5 at the daemon seam: Paths is built once at
// startup, so without this the bus configs would keep rendering the prefix
// waypointd booted with while the gateway INIs followed the operator's edit.
func TestEffectivePathsFollowsStore(t *testing.T) {
	s := &server{paths: config.Paths{MQTTBroker: "127.0.0.1:1883", BusTopicPrefix: config.DefaultBusTopicPrefix}}
	m := &config.Model{}
	m.MQTT = config.MQTT{Host: "10.0.0.9", Port: "1885", BusPrefix: "site7/bus"}

	got := s.effectivePaths(m)
	if got.MQTTBroker != "10.0.0.9:1885" {
		t.Errorf("broker = %q, want the store's", got.MQTTBroker)
	}
	if got.BusTopicPrefix != "site7/bus" {
		t.Errorf("bus prefix = %q, want the store's", got.BusTopicPrefix)
	}

	// Demo mode has no broker and must keep none, or a demo would render MQTT
	// blocks pointing at a broker that is not there.
	demo := &server{paths: config.Paths{}}
	if p := demo.effectivePaths(m); p.MQTTBroker != "" {
		t.Errorf("demo broker = %q, want empty", p.MQTTBroker)
	}
}

// TestDataPlaneReconfigure covers the decisions that keep an Apply from bouncing
// the dashboard for no reason, and the ones that must bounce it.
func TestDataPlaneReconfigure(t *testing.T) {
	base := dataPlaneConfig{Broker: "127.0.0.1:1883", Name: "mmdvm", BusPrefix: "waypoint/bus", StatusPrefix: "waypoint/status"}

	renamed := base
	renamed.Name = "node7"
	if !renamed.consumerChanged(base) {
		t.Errorf("renaming the modem topic must restart the consumer, or the feed goes dark")
	}
	if renamed.publisherChanged(base) {
		t.Errorf("a name change needs no publisher reconnect")
	}

	statusOnly := base
	statusOnly.StatusPrefix = "site/status"
	if statusOnly.consumerChanged(base) {
		t.Errorf("the consumer does not read the status prefix; it must not restart")
	}
	if statusOnly.publisherChanged(base) {
		t.Errorf("a prefix change publishes on the same connection")
	}

	moved := base
	moved.Broker = "10.0.0.9:1883"
	if !moved.consumerChanged(base) || !moved.publisherChanged(base) {
		t.Errorf("a broker move must rebuild both connections")
	}

	// Credentials ride only when auth is on, so waypointd and the rendered INIs
	// can never disagree about whether this node authenticates.
	withCreds := dataPlaneConfigFrom(config.MQTT{Username: "wp", Password: "s3cret"})
	if withCreds.Username != "" || withCreds.Password != "" {
		t.Errorf("auth off but credentials were carried: %+v", withCreds)
	}
	withCreds = dataPlaneConfigFrom(config.MQTT{Auth: true, Username: "wp", Password: "s3cret"})
	if withCreds.Username != "wp" || withCreds.Password != "s3cret" {
		t.Errorf("auth on but credentials were dropped: %+v", withCreds)
	}
	// Defaults resolve, so an empty section still produces a connectable plane.
	if d := dataPlaneConfigFrom(config.MQTT{}); d != base {
		t.Errorf("empty section = %+v, want the compiled defaults %+v", d, base)
	}
}
