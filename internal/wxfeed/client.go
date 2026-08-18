package wxfeed

import (
	"context"
	"log"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

// The live subscriber.
//
// It is separated from Decide so the decision logic is testable without a
// broker, and so the only thing this file has to get right is the plumbing:
// connect, subscribe to the right filters, and hand every delivery to Decide
// with its retain flag intact.
//
// # Clean session, deliberately
//
// A durable session would have the broker queue messages while the node is
// offline and deliver the backlog on reconnect. That is exactly wrong here: the
// backlog is hazards from while we were down, some of them over, and a station
// that transmits would announce all of them at once. A clean session plus the
// retained state we get on subscribe is both simpler and safer — we learn what
// is true now, announce none of it, and go live from there.

// Options configures the subscriber.
type Options struct {
	// Broker is the full websocket URL, e.g. wss://mqtt.wxalerts.org/mqtt.
	Broker   string
	Username string
	Password string
	// Topics are the full filters to subscribe to, including the trailing "/#".
	// They come from the config's WXSubscriptions so there is one authority for
	// that wildcard.
	Topics []string
	// ClientID should be stable for a node but unique on the broker.
	ClientID string
}

// Announcer receives an alert that passed every gate and should go on the air.
// It is called from the MQTT client's goroutine and must not block for long;
// delivery queues rather than transmits inline.
type Announcer interface {
	AnnounceAlert(a Alert)
	// ClearAlert is called for a tombstone or an expiry. Implementations use it
	// to drop the hazard from whatever they are displaying.
	ClearAlert(key string)
}

// Client is a running subscription.
type Client struct {
	opts   Options
	policy Policy
	out    Announcer

	mu   sync.Mutex
	seen map[string]time.Time
	// stats are counters an operator can be shown, and are the difference
	// between "quiet week" and "we have been disconnected since Tuesday".
	stats Stats
}

// Stats is what the subscription has done. Every counter that is not Announced
// is something that did NOT reach the air, and each says why.
type Stats struct {
	Connected     bool
	Received      int
	Retained      int
	Announced     int
	Cleared       int
	Deduped       int
	NotRouted     int
	Unparseable   int
	LastMessageAt time.Time
	LastConnectAt time.Time
}

// New builds a client. It does not connect; Run does.
func New(opts Options, policy Policy, out Announcer) *Client {
	if opts.ClientID == "" {
		opts.ClientID = "waypointd-wx"
	}
	return &Client{opts: opts, policy: policy, out: out, seen: map[string]time.Time{}}
}

// Stats returns a snapshot.
func (c *Client) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stats
}

// seenBefore records and reports whether this (hazard, action) has been
// announced. The window exists so a node running for months does not accumulate
// a key per hazard forever; 24 hours comfortably outlives any single hazard.
const seenWindow = 24 * time.Hour

func (c *Client) seenBefore(key, action string) bool {
	k := key + "|" + action
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for old, at := range c.seen {
		if now.Sub(at) > seenWindow {
			delete(c.seen, old)
		}
	}
	if _, ok := c.seen[k]; ok {
		return true
	}
	c.seen[k] = now
	return false
}

// Run connects and subscribes until ctx is cancelled.
//
// It returns nil on a clean shutdown. A broker that is unreachable is not an
// error worth stopping for — paho retries, and a node whose weather feed is down
// should keep being a repeater.
func (c *Client) Run(ctx context.Context) error {
	if len(c.opts.Topics) == 0 {
		log.Printf("wxfeed: no counties configured; not connecting")
		return nil
	}

	co := mqtt.NewClientOptions().
		AddBroker(c.opts.Broker).
		SetClientID(c.opts.ClientID).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(30 * time.Second).
		SetKeepAlive(60 * time.Second).
		// See the package note: a durable session would deliver a backlog of
		// hazards from while we were offline, and a transmitting station must
		// not announce those.
		SetCleanSession(true)
	if c.opts.Username != "" {
		co.SetUsername(c.opts.Username)
		co.SetPassword(c.opts.Password)
	}

	co.SetOnConnectHandler(func(cl mqtt.Client) {
		c.mu.Lock()
		c.stats.Connected = true
		c.stats.LastConnectAt = time.Now()
		c.mu.Unlock()
		// Resubscribe on every reconnect. The retained burst that follows is
		// state, and Decide refuses to announce it.
		for _, t := range c.opts.Topics {
			if tok := cl.Subscribe(t, 1, c.handle); tok.Wait() && tok.Error() != nil {
				log.Printf("wxfeed: subscribe %s failed: %v", t, tok.Error())
			}
		}
		log.Printf("wxfeed: connected, watching %d county topic(s)", len(c.opts.Topics))
	})
	co.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		c.mu.Lock()
		c.stats.Connected = false
		c.mu.Unlock()
		log.Printf("wxfeed: connection lost: %v", err)
	})

	cl := mqtt.NewClient(co)
	if tok := cl.Connect(); tok.Wait() && tok.Error() != nil {
		log.Printf("wxfeed: initial connect failed, will retry: %v", tok.Error())
	}
	<-ctx.Done()
	cl.Disconnect(250)
	return nil
}

// handle is the per-message path. It does as little as possible: hand the
// delivery to Decide with its retain flag, then act on the verdict.
func (c *Client) handle(_ mqtt.Client, m mqtt.Message) {
	a, v := Decide(m.Topic(), m.Payload(), m.Retained(), c.seenBefore, c.policy)

	c.mu.Lock()
	c.stats.Received++
	c.stats.LastMessageAt = time.Now()
	// Retained deliveries are counted ONLY as retained. Letting them also fall
	// into NotRouted was actively misleading on the bench: a healthy node showed
	// "6 not routed" on connect, which reads as a policy that is refusing
	// everything when in fact it was the hazards already in effect arriving as
	// state. An operator reading that would go looking for a fault.
	switch {
	case m.Retained():
		c.stats.Retained++
	case v.Announce:
		c.stats.Announced++
	case v.Clear:
		c.stats.Cleared++
	case v.Reason == "already announced":
		c.stats.Deduped++
	case v.Reason == "unparseable payload":
		c.stats.Unparseable++
	default:
		c.stats.NotRouted++
	}
	c.mu.Unlock()

	if c.out == nil {
		return
	}
	switch {
	case v.Announce:
		log.Printf("wxfeed: announcing %s (%s)", a.Event, a.DedupKey())
		c.out.AnnounceAlert(a)
	case v.Clear:
		c.out.ClearAlert(a.DedupKey())
	}
}
