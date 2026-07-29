//go:build tier2

package tier2

import (
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// mqttwatch.go subscribes to a gateway daemon's own MQTT topics.
//
// The rendered configs set DisplayLevel=0 and MQTTLevel=1, so a running daemon
// says nothing on stdout — its log and its status both go to the broker. That is
// the plane waypointd consumes in production, so a test that wants to see what the
// daemon believes has to look in the same place the product does, and gets to
// check the wire format against the real thing rather than a fixture.

// brokerAddr is where the rendered configs point every daemon (render.go writes
// 127.0.0.1:1883), and so where the test has to listen.
const brokerAddr = "tcp://127.0.0.1:1883"

// watchGatewayTopics subscribes to <name>/# and calls fn for every message until
// the returned function is called. It skips the test if no broker is reachable,
// because tier 2 needs one running anyway (see README).
func watchGatewayTopics(t *testing.T, name string, fn func(topic string, payload []byte)) func() {
	t.Helper()

	opts := paho.NewClientOptions().
		AddBroker(brokerAddr).
		SetClientID("tier2-watch-" + name + "-" + t.Name()).
		SetConnectTimeout(3 * time.Second).
		SetCleanSession(true)

	c := paho.NewClient(opts)
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Skipf("no MQTT broker on %s: %v (see test/tier2/README.md)", brokerAddr, tok.Error())
	}
	topic := name + "/#"
	if tok := c.Subscribe(topic, 0, func(_ paho.Client, m paho.Message) {
		fn(m.Topic(), m.Payload())
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		c.Disconnect(100)
		t.Fatalf("subscribe %s: %v", topic, tok.Error())
	}
	return func() { c.Disconnect(100) }
}

// pollGatewayStatus sends the daemon's "status" remote command and returns its
// reply, or "" if it does not answer in time.
func pollGatewayStatus(t *testing.T, name string, wait time.Duration) string {
	t.Helper()

	opts := paho.NewClientOptions().
		AddBroker(brokerAddr).
		SetClientID("tier2-poll-" + name + "-" + t.Name()).
		SetConnectTimeout(3 * time.Second).
		SetCleanSession(true)
	c := paho.NewClient(opts)
	if tok := c.Connect(); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		t.Skipf("no MQTT broker on %s: %v", brokerAddr, tok.Error())
	}
	defer c.Disconnect(100)

	reply := make(chan string, 1)
	if tok := c.Subscribe(name+"/response", 0, func(_ paho.Client, m paho.Message) {
		select {
		case reply <- string(m.Payload()):
		default:
		}
	}); !tok.WaitTimeout(5*time.Second) || tok.Error() != nil {
		return ""
	}
	c.Publish(name+"/command", 0, false, "status")

	select {
	case r := <-reply:
		return r
	case <-time.After(wait):
		return ""
	}
}
