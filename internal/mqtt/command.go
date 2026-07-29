package mqtt

import (
	"context"
	"log"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
)

// command.go asks a gateway daemon a question and waits for the answer.
//
// Everything else in this package is one-way: the daemons publish and waypointd
// listens. That is fine for events but not for state, because an announcement
// only tells you about the moment it was made — a supervisor that starts up, or
// misses a message, has no way to establish where things actually stand. The
// gateways expose a request/response pair for exactly this ([Remote Commands] in
// the rendered config), and this is the client for it.

// DefaultCommandTimeout bounds one request. The daemon answers from memory in the
// same event loop it runs everything else in, so a reply that has not arrived in a
// couple of seconds means the daemon is wedged or gone — which is itself an answer
// the caller wants promptly rather than a stall.
const DefaultCommandTimeout = 3 * time.Second

// Commander sends remote commands to gateway daemons over MQTT and returns their
// replies. It holds one connection, subscribed to every daemon's response topic
// for its lifetime — subscribing per request would race the reply, which the
// daemon can publish before a fresh subscription is registered.
type Commander struct {
	client  paho.Client
	timeout time.Duration

	mu      sync.Mutex
	waiting map[string]chan string // response topic → the caller waiting on it
}

// NewCommander connects and subscribes to <name>/response for each daemon name.
// It returns even if the broker is momentarily down (connect-retry is on); a
// command issued before the link is up simply times out, and the supervisor reads
// that as "no answer", not as "disconnected".
func NewCommander(opts Options, names []string, timeout time.Duration) *Commander {
	if timeout <= 0 {
		timeout = DefaultCommandTimeout
	}
	c := &Commander{timeout: timeout, waiting: map[string]chan string{}}

	co := paho.NewClientOptions().
		AddBroker("tcp://" + opts.Broker).
		SetClientID("waypointd-command").
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(5 * time.Second).
		SetKeepAlive(30 * time.Second).
		SetCleanSession(true)
	if opts.Username != "" {
		co.SetUsername(opts.Username)
		co.SetPassword(opts.Password)
	}
	// Re-subscribe on every reconnect, so a broker restart does not silently leave
	// the supervisor unable to ask anything.
	co.SetOnConnectHandler(func(cl paho.Client) {
		for _, name := range names {
			if name == "" {
				continue
			}
			topic := name + "/response"
			if tok := cl.Subscribe(topic, 0, c.onResponse(topic)); tok.WaitTimeout(5*time.Second) && tok.Error() != nil {
				log.Printf("mqtt: subscribe %s failed: %v", topic, tok.Error())
			}
		}
	})

	c.client = paho.NewClient(co)
	if tok := c.client.Connect(); tok.WaitTimeout(5*time.Second) && tok.Error() != nil {
		log.Printf("mqtt: commander connect to %s failed (will retry): %v", opts.Broker, tok.Error())
	}
	return c
}

func (c *Commander) onResponse(topic string) paho.MessageHandler {
	return func(_ paho.Client, m paho.Message) {
		c.mu.Lock()
		ch := c.waiting[topic]
		c.mu.Unlock()
		if ch == nil {
			return // nobody asked; an unsolicited or late reply is dropped
		}
		select {
		case ch <- string(m.Payload()):
		default: // the caller has already given up
		}
	}
}

// Ask publishes command to <name>/command and returns the daemon's reply. It
// returns ok=false when nothing answers in time, which the caller must read as
// "no news" rather than as bad news — a daemon that is mid-restart, or a broker
// that is briefly away, is not a failed link.
func (c *Commander) Ask(ctx context.Context, name, command string) (string, bool) {
	if c == nil || c.client == nil {
		return "", false
	}
	topic := name + "/response"
	ch := make(chan string, 1)

	// Registered before publishing, so a daemon that answers immediately is heard.
	c.mu.Lock()
	if _, busy := c.waiting[topic]; busy {
		c.mu.Unlock()
		return "", false // one question at a time per daemon
	}
	c.waiting[topic] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.waiting, topic)
		c.mu.Unlock()
	}()

	if tok := c.client.Publish(name+"/command", 0, false, command); tok.WaitTimeout(c.timeout) && tok.Error() != nil {
		return "", false
	}

	select {
	case reply := <-ch:
		return reply, true
	case <-time.After(c.timeout):
		return "", false
	case <-ctx.Done():
		return "", false
	}
}

// Close disconnects the commander.
func (c *Commander) Close() {
	if c != nil && c.client != nil {
		c.client.Disconnect(250)
	}
}
