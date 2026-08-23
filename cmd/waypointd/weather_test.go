package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/wxfeed"
)

// GOVERNANCE.md principle 2 is a merge gate: a Waypoint device must not put an
// identifier for itself on the wire. The alert feed is a new outbound
// connection, and the client id it presents is the one field that could carry
// one, so this pins that it does not.
//
// It must name the software and nothing about this node -- not the callsign,
// not the DMR id, not the hostname. Randomness gives MQTT the uniqueness it
// needs without any of that.
func TestFeedClientIDCarriesNoDeviceIdentity(t *testing.T) {
	const (
		callsign = "KN4OQW"
		dmrID    = "3180202"
	)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := wxFeedClientID()
		if !strings.HasPrefix(id, "waypointd-wx") {
			t.Fatalf("client id %q does not name the software", id)
		}
		for _, ident := range []string{callsign, strings.ToLower(callsign), dmrID} {
			if strings.Contains(id, ident) {
				t.Errorf("client id %q contains a device identifier (%s)", id, ident)
			}
		}
		seen[id] = true
	}
	// Unique per connection, or a broker evicts one node when another connects.
	if len(seen) < 40 {
		t.Errorf("only %d distinct client ids in 50 calls; they must not collide", len(seen))
	}
}

// The off switch has to actually stop the outbound connection. A subscriber
// with no counties has nothing to watch and must not contact the broker at all
// -- which is also the state every node ships in, since the feature is off and
// the county list empty by default.
func TestFeedDoesNotConnectWithNothingToWatch(t *testing.T) {
	// An address that would fail slowly and loudly if it were ever dialled.
	c := wxfeed.New(wxfeed.Options{
		Broker: "wss://198.51.100.1/mqtt", // TEST-NET-2, unroutable by design
		Topics: nil,
	}, nil, nil)

	done := make(chan error, 1)
	go func() { done <- c.Run(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run with no topics returned %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run with no counties did not return; it is trying to reach the broker")
	}
	if c.Stats().Connected {
		t.Error("a subscriber with no counties reports itself connected")
	}
}
