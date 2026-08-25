package mqtt

import (
	"context"
	"log"
	"strings"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// Options configures the connection to the local MQTT broker that MMDVM-Host
// publishes to.
type Options struct {
	Broker   string // host:port of the broker, e.g. 127.0.0.1:1883
	Name     string // MMDVM-Host [MQTT] Name — the topic prefix (default "mmdvm")
	Username string // optional
	Password string // optional
	// BusPrefix is the topic root the mode-bus daemons publish their events under
	// (D4; default "waypoint/bus"). The consumer subscribes <BusPrefix>/# and maps
	// each JSON payload 1:1 onto a hub.Event.
	BusPrefix string
	// OnTalkerAlias, when set, receives each bus announcement of who is talking
	// (<BusPrefix>/<id>/talker_alias). It is delivered on the paho callback
	// goroutine, so an implementation must not block — the same goroutine carries
	// every bus event.
	//
	// nil drops the notes, which is right for a consumer that has no injector at
	// all. waypointd always sets it; whether there is anywhere to PUT an alias (the
	// DMR relay may be off, the template unset) is the injector's own decision, made
	// per note, not something to answer by leaving this nil.
	OnTalkerAlias func(TalkerAliasNote)
	// GatewayNames are the [MQTT] Name values of the gateway daemons whose own
	// status planes carry upstream link news — DMRGateway's "Logged into DMR
	// Network: X" and its failure counterparts (#22). Each is subscribed at
	// <name>/json alongside MMDVM-Host's. Empty means none, which is what a node
	// running no gateways wants.
	GatewayNames []string
}

// Run connects to the broker, subscribes to <Name>/json, and republishes every
// translated event onto h until ctx is canceled. It relies on paho's built-in
// auto-reconnect, so a broker restart or MMDVM-Host cycling does not require a
// waypointd restart. Run blocks until ctx is done.
func Run(ctx context.Context, h *hub.Hub, opts Options) error {
	if opts.Name == "" {
		opts.Name = "mmdvm"
	}
	if opts.BusPrefix == "" {
		opts.BusPrefix = "waypoint/bus"
	}
	topic := opts.Name + "/json"
	busTopic := opts.BusPrefix + "/#"
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
		// D4: the mode-bus event plane. Each retained/transient message under
		// <BusPrefix>/<id>/<type> is a hub.Event JSON, mapped 1:1 (no translation
		// layer). An empty payload is a retained CLEAR (RFC-0008 no-latching) — skipped.
		//
		// The talker-alias topic is dispatched here rather than through a second
		// subscription of its own. Overlapping subscriptions on one paho client mean
		// the broker matches a message twice and the handler runs twice; keeping it to
		// one subscription and branching on the topic makes double delivery
		// impossible rather than merely unlikely.
		if tok := c.Subscribe(busTopic, 0, func(_ mqtt.Client, msg mqtt.Message) {
			routeBusMessage(msg.Topic(), msg.Payload(), h, opts.OnTalkerAlias)
		}); tok.Wait() && tok.Error() != nil {
			log.Printf("mqtt: subscribe %s failed: %v", busTopic, tok.Error())
			return
		}
		// The gateway daemons' own status planes (#22). Each is its own topic
		// rather than a wildcard so a foreign publisher on the broker cannot inject
		// link news about a network this node does not have.
		for _, name := range opts.GatewayNames {
			if name == "" {
				continue
			}
			gwTopic := name + "/json"
			if tok := c.Subscribe(gwTopic, 0, func(_ mqtt.Client, m mqtt.Message) {
				if e, ok := TranslateGatewayStatus(m.Payload()); ok {
					h.Publish(e)
				}
			}); tok.Wait() && tok.Error() != nil {
				// Not fatal: a gateway that is not running publishes nothing, and the
				// supervisor's other signals carry on without this one.
				log.Printf("mqtt: subscribe %s failed: %v", gwTopic, tok.Error())
			}
		}
		log.Printf("mqtt: subscribed to %s and %s on %s", topic, busTopic, opts.Broker)
		// feed_up drives the status pipeline's Feed health (RFC-0008): the dashboard
		// shows the MMDVM-Host data plane as connected the moment we (re)subscribe.
		h.Publish(hub.Event{Time: time.Now().UTC(), Type: "feed_up", Detail: "MMDVM-Host feed connected (" + opts.Broker + ")"})
	})
	co.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		log.Printf("mqtt: connection to %s lost: %v", opts.Broker, err)
		// feed_down so the dashboard reflects the lost data plane rather than latching
		// the last-known state (RFC-0008 — truth, not a stuck value).
		h.Publish(hub.Event{Time: time.Now().UTC(), Type: "feed_down", Detail: "MMDVM-Host feed lost: " + err.Error()})
	})

	client := mqtt.NewClient(co)
	if tok := client.Connect(); tok.Wait() && tok.Error() != nil {
		return tok.Error()
	}

	<-ctx.Done()
	client.Disconnect(250)
	return nil
}

// routeBusMessage dispatches one message from under <BusPrefix>/#.
//
// Split out of the subscription callback so the branch is testable without a
// broker: which of two handlers a topic reaches is the kind of thing that is
// obviously right when written and silently wrong after a rename.
func routeBusMessage(topic string, payload []byte, h *hub.Hub, onTalkerAlias func(TalkerAliasNote)) {
	if strings.HasSuffix(topic, "/"+config.BusTalkerAliasTopic) {
		if n, ok := TranslateTalkerAlias(payload); ok && onTalkerAlias != nil {
			onTalkerAlias(n)
		}
		return
	}
	if e, ok := TranslateBusEvent(payload); ok {
		h.Publish(e)
	}
}
