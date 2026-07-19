package hadiscovery

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// Options configures the Home Assistant discovery publisher. Broker/Username/
// Password mirror the MMDVM-Host feed connection — the publisher targets the same
// broker (the operator points Home Assistant at it too).
type Options struct {
	Broker          string
	Username        string
	Password        string
	DiscoveryPrefix string // HA discovery root; "" -> "homeassistant"
	Device          DeviceInfo
}

// Run connects to the broker, announces the hotspot to Home Assistant, and keeps
// its status topic fresh from the event hub until ctx is canceled. It blocks until
// ctx is done, then publishes a clean "offline" and disconnects.
//
// Announce = publish the retained availability birth ("online"), the retained
// device-discovery bundle, and the current state. The MQTT Last Will (set on
// connect, retained) flips availability to "offline" if the daemon drops, so Home
// Assistant marks the entities unavailable without waiting for a timeout. The
// publisher also re-announces when Home Assistant restarts (its own birth message
// on <prefix>/status), since a retained discovery may have been missed.
func Run(ctx context.Context, h *hub.Hub, opts Options) error {
	if opts.DiscoveryPrefix == "" {
		opts.DiscoveryPrefix = "homeassistant"
	}
	node := Node(opts.Device.Callsign)
	topics := TopicsFor(node, opts.DiscoveryPrefix)
	discoTopic, discoPayload, err := DiscoveryPayload(opts.Device, opts.DiscoveryPrefix)
	if err != nil {
		return err
	}

	var mu sync.Mutex
	state := NewState()
	encodeState := func() []byte {
		mu.Lock()
		defer mu.Unlock()
		b, _ := state.Encode()
		return b
	}

	announce := func(c mqtt.Client) {
		c.Publish(topics.Availability, 1, true, "online")
		c.Publish(discoTopic, 1, true, discoPayload)
		c.Publish(topics.State, 1, true, encodeState())
	}

	co := mqtt.NewClientOptions().
		AddBroker("tcp://"+opts.Broker).
		SetClientID("waypointd-ha").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetKeepAlive(30*time.Second).
		SetCleanSession(true).
		// LWT: if the daemon drops, the broker publishes offline (retained) so HA
		// marks every entity unavailable.
		SetWill(topics.Availability, "offline", 1, true)
	if opts.Username != "" {
		co.SetUsername(opts.Username)
		co.SetPassword(opts.Password)
	}
	co.SetOnConnectHandler(func(c mqtt.Client) {
		announce(c)
		// Re-announce on Home Assistant's own restart (birth message). A brief delay
		// avoids a broker IO spike if many devices react to the same birth.
		haStatus := opts.DiscoveryPrefix + "/status"
		if tok := c.Subscribe(haStatus, 1, func(_ mqtt.Client, m mqtt.Message) {
			if strings.TrimSpace(string(m.Payload())) == "online" {
				time.Sleep(2 * time.Second)
				announce(c)
			}
		}); tok.Wait() && tok.Error() != nil {
			log.Printf("ha: subscribe %s failed: %v", haStatus, tok.Error())
		}
		log.Printf("ha: announced %d entities for %s to %s", EntityCount(), node, opts.Broker)
	})
	co.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("ha: connection to %s lost: %v", opts.Broker, err)
	})

	client := mqtt.NewClient(co)
	if tok := client.Connect(); tok.Wait() && tok.Error() != nil {
		return tok.Error()
	}

	// Fold every hub event into the status and publish it (retained, so a
	// reconnecting HA gets the last value without waiting for the next event).
	ch, _, cancel := h.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			client.Publish(topics.Availability, 1, true, "offline").Wait()
			client.Disconnect(250)
			return nil
		case e := <-ch:
			mu.Lock()
			state.Apply(e)
			mu.Unlock()
			client.Publish(topics.State, 1, true, encodeState())
		}
	}
}
