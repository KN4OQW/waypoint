package mqtt

import (
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// Publisher is a best-effort MQTT publisher for the normalized waypoint/status/#
// topics (RFC-0008). It holds its own auto-reconnecting connection (a distinct
// client id from the consumer), so a broker hiccup never blocks the status
// aggregator that feeds it. Publishing is fire-and-forget at QoS 0, retained, so a
// late-joining Home Assistant reads current state immediately.
type Publisher struct {
	client mqtt.Client
}

// NewPublisher connects a retained-status publisher to the broker. It returns even
// if the broker is momentarily down (connect-retry is on); publishes made before
// the link is up are dropped, which is fine — the next status change republishes.
//
// When availabilityTopic is non-empty (HA discovery, RFC-0011), the connection
// registers a retained "offline" Last-Will on it and publishes "online" on every
// (re)connect, so Home Assistant marks the device unavailable the moment the node
// drops and available again when it returns.
func NewPublisher(opts Options, availabilityTopic string) *Publisher {
	co := mqtt.NewClientOptions().
		AddBroker("tcp://" + opts.Broker).
		SetClientID("waypointd-status").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(true)
	if opts.Username != "" {
		co.SetUsername(opts.Username)
		co.SetPassword(opts.Password)
	}
	if availabilityTopic != "" {
		co.SetWill(availabilityTopic, "offline", 0, true) // retained LWT
		co.SetOnConnectHandler(func(c mqtt.Client) {
			c.Publish(availabilityTopic, 0, true, "online")
		})
	}
	client := mqtt.NewClient(co)
	if tok := client.Connect(); tok.WaitTimeout(5*time.Second) && tok.Error() != nil {
		log.Printf("mqtt: status publisher connect to %s failed (will retry): %v", opts.Broker, tok.Error())
	}
	return &Publisher{client: client}
}

// Publish sends one retained message. It never blocks the caller and swallows
// transient errors — the aggregator must never stall on a wedged broker.
func (p *Publisher) Publish(topic string, payload []byte) {
	if p == nil || p.client == nil {
		return
	}
	p.client.Publish(topic, 0, true, payload) // qos 0, retained
}

// Close disconnects the publisher.
func (p *Publisher) Close() {
	if p != nil && p.client != nil {
		p.client.Disconnect(250)
	}
}
