package mqtt

import (
	"context"
	"log"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// Options configures the connection to the local MQTT broker that MMDVM-Host
// publishes to.
type Options struct {
	Broker   string // host:port of the broker, e.g. 127.0.0.1:1883
	Name     string // MMDVM-Host [MQTT] Name — the topic prefix (default "mmdvm")
	Username string // optional
	Password string // optional
}

// Run connects to the broker, subscribes to <Name>/json, and republishes every
// translated event onto h until ctx is canceled. It relies on paho's built-in
// auto-reconnect, so a broker restart or MMDVM-Host cycling does not require a
// waypointd restart. Run blocks until ctx is done.
func Run(ctx context.Context, h *hub.Hub, opts Options) error {
	if opts.Name == "" {
		opts.Name = "mmdvm"
	}
	topic := opts.Name + "/json"
	bridge := NewBridge()

	co := mqtt.NewClientOptions().
		AddBroker("tcp://" + opts.Broker).
		SetClientID("waypointd").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(true)
	if opts.Username != "" {
		co.SetUsername(opts.Username)
		co.SetPassword(opts.Password)
	}

	// (Re)subscribe on every (re)connect so a dropped broker recovers cleanly.
	co.SetOnConnectHandler(func(c mqtt.Client) {
		if tok := c.Subscribe(topic, 0, func(_ mqtt.Client, m mqtt.Message) {
			for _, e := range bridge.Translate(m.Payload()) {
				h.Publish(e)
			}
		}); tok.Wait() && tok.Error() != nil {
			log.Printf("mqtt: subscribe %s failed: %v", topic, tok.Error())
			return
		}
		log.Printf("mqtt: subscribed to %s on %s", topic, opts.Broker)
		h.Publish(hub.Event{Time: time.Now().UTC(), Type: "link", Detail: "MMDVM-Host feed connected (" + opts.Broker + ")"})
	})
	co.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("mqtt: connection to %s lost: %v", opts.Broker, err)
	})

	client := mqtt.NewClient(co)
	if tok := client.Connect(); tok.Wait() && tok.Error() != nil {
		return tok.Error()
	}

	<-ctx.Done()
	client.Disconnect(250)
	return nil
}
