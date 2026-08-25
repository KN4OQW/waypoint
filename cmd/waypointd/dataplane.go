package main

import (
	"context"
	"log"
	"sync"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/status"
)

// dataPlane owns the two live MQTT connections whose settings the System tab can
// change: the CONSUMER that ingests MMDVM-Host's <Name>/json and the bus daemons'
// <BusPrefix>/#, and the status REPUBLISHER that emits the normalized status under
// <StatusPrefix>/# (RFC-0008) plus the Home Assistant discovery configs
// (RFC-0011).
//
// It exists because those settings stopped being start-time flags. Before the
// System tab, the topic roots could only change by editing a systemd unit and
// rebooting the daemon, so capturing them once in main() was correct. Now an
// operator can rename the modem's topic root in the UI and press Apply: the
// renderer rewrites MMDVM.ini and the apply restarts MMDVM-Host onto the new
// topic, and unless waypointd follows, the dashboard goes dark (feed_down) until
// somebody restarts the daemon. Following is the whole point of moving the
// setting into the store, so reconfigure is part of the apply path, not a
// footnote.
//
// The consumer is torn down and restarted, because a paho subscription is fixed
// at connect time. The publisher is swapped only when the broker or credentials
// move; a prefix change needs no reconnect, just a new topic root, so the
// aggregator callbacks are registered ONCE at start and read the current prefix
// under the mutex rather than capturing it.
type dataPlane struct {
	hub *hub.Hub
	agg *status.Aggregator

	haDiscovery bool
	haPrefix    string
	nodeID      string
	version     string
	// onTalkerAlias receives a bus daemon's statement of who is talking, for the
	// Talker Alias injector to put on the receiving radio (issue #279). Set once at
	// construction: the injector reconciles itself on the relay's tick, so the
	// callback never has to be swapped when the feature is switched on or off.
	onTalkerAlias func(mqtt.TalkerAliasNote)

	mu      sync.Mutex
	cur     dataPlaneConfig
	started bool
	cancel  context.CancelFunc // stops the running consumer
	pub     *mqtt.Publisher
	// cmd is how the supervisor ASKS a gateway where its links stand (#22). It
	// lives here rather than in main() for the same reason the publisher does: the
	// broker it talks to is store-owned, so an operator who moves the broker in the
	// System tab must not leave the supervisor querying the old one forever.
	cmd *mqtt.Commander
	// seen dedupes Home Assistant discovery configs by topic. It is cleared on a
	// prefix change: the new prefix is a different set of topics, and HA needs the
	// configs republished there or the entities never appear.
	seen map[string]bool
}

// dataPlaneConfig is the resolved subset of the mqtt section these two
// connections consume. Comparing values (not the whole model) is what makes an
// Apply that did not touch MQTT a no-op here.
type dataPlaneConfig struct {
	Broker       string
	Name         string
	Username     string
	Password     string
	BusPrefix    string
	StatusPrefix string
}

func dataPlaneConfigFrom(q config.MQTT) dataPlaneConfig {
	c := dataPlaneConfig{
		Broker:       q.Broker(),
		Name:         q.HostName(),
		BusPrefix:    q.BusTopicPrefix(),
		StatusPrefix: q.StatusTopicPrefix(),
	}
	// Credentials are sent only when authentication is on, matching what the
	// renderers put in the daemons' INIs — one rule, so waypointd and the gateways
	// can never disagree about whether this node authenticates.
	if q.Auth {
		c.Username, c.Password = q.Username, q.Password
	}
	return c
}

// consumerChanged reports whether a change requires tearing the consumer down.
// The status prefix is not in the set: the consumer neither subscribes to nor
// publishes on it.
func (c dataPlaneConfig) consumerChanged(prev dataPlaneConfig) bool {
	return c.Broker != prev.Broker || c.Name != prev.Name ||
		c.Username != prev.Username || c.Password != prev.Password ||
		c.BusPrefix != prev.BusPrefix
}

// publisherChanged reports whether the publisher's CONNECTION must be rebuilt. A
// prefix-only change does not qualify — the same connection publishes to the new
// topics.
func (c dataPlaneConfig) publisherChanged(prev dataPlaneConfig) bool {
	return c.Broker != prev.Broker || c.Username != prev.Username || c.Password != prev.Password
}

// start brings the data plane up for the first time and registers the aggregator
// callbacks. The callbacks live for the process; only what they read changes.
func (dp *dataPlane) start(ctx context.Context, cfg dataPlaneConfig) {
	dp.mu.Lock()
	dp.started = true
	dp.mu.Unlock()

	dp.reconfigure(ctx, cfg)

	// Republish the normalized status onto retained <StatusPrefix>/# topics for
	// Home Assistant and other consumers (RFC-0008). Best-effort: a publish with no
	// live publisher is dropped, not an error, so a broker that is down never
	// blocks the aggregator.
	// A Republisher rather than the bare function: retained topics need to be
	// CLEARED when a network goes away, or a deleted one's last payload outlives it
	// on the broker and Home Assistant keeps an entity for a network the node no
	// longer has.
	rp := &status.Republisher{}
	dp.agg.OnChange(func(st status.Status) {
		prefix, pub := dp.publisher()
		if pub == nil {
			return
		}
		rp.Publish(st, prefix, pub.Publish)
	})
	if !dp.haDiscovery {
		return
	}
	// Home Assistant MQTT discovery (RFC-0011): a retained config per entity,
	// pointing HA at the status topics — zero YAML. Published once per topic as
	// each entity first appears (gateways/networks show up over time).
	publishDiscovery := func(st status.Status) {
		prefix, pub := dp.publisher()
		if pub == nil {
			return
		}
		opts := status.DiscoveryOptions{Prefix: dp.haPrefix, NodeID: dp.nodeID, StatePrefix: prefix, Version: dp.version}
		for _, d := range status.DiscoveryConfigs(st, opts) {
			dp.mu.Lock()
			dup := dp.seen[d.Topic]
			dp.seen[d.Topic] = true
			dp.mu.Unlock()
			if !dup {
				pub.Publish(d.Topic, d.Payload)
			}
		}
	}
	publishDiscovery(dp.agg.Snapshot()) // the always-present mode/tx/feed entities now
	dp.agg.OnChange(publishDiscovery)
}

// publisher returns the live status prefix and publisher together, so a caller
// can never pair a new prefix with a closed publisher.
func (dp *dataPlane) publisher() (string, *mqtt.Publisher) {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	return dp.cur.StatusPrefix, dp.pub
}

// commander returns the live command client, or nil before the data plane has
// started. The supervisor calls this per cycle rather than capturing the value, so
// a broker change swaps the connection underneath it without a restart.
func (dp *dataPlane) commander() *mqtt.Commander {
	if dp == nil {
		return nil
	}
	dp.mu.Lock()
	defer dp.mu.Unlock()
	return dp.cmd
}

// brokerAndPrefix reports the live broker and status prefix (for logging/tests).
func (dp *dataPlane) brokerAndPrefix() (string, string) {
	dp.mu.Lock()
	defer dp.mu.Unlock()
	return dp.cur.Broker, dp.cur.StatusPrefix
}

// reconfigure moves the live connections to cfg, doing nothing when nothing that
// matters changed — so an Apply that only touched, say, a talkgroup does not
// bounce the dashboard's feed.
func (dp *dataPlane) reconfigure(ctx context.Context, cfg dataPlaneConfig) {
	dp.mu.Lock()
	if !dp.started || cfg == dp.cur {
		dp.mu.Unlock()
		return
	}
	prev := dp.cur
	restartConsumer := dp.cancel == nil || cfg.consumerChanged(prev)
	rebuildPub := dp.pub == nil || cfg.publisherChanged(prev)
	oldCancel, oldPub := dp.cancel, dp.pub
	if cfg.StatusPrefix != prev.StatusPrefix {
		dp.seen = map[string]bool{} // new topic root ⇒ HA needs the configs again
	}
	dp.cur = cfg
	if restartConsumer {
		cctx, cancel := context.WithCancel(ctx)
		dp.cancel = cancel
		go func() {
			if err := mqtt.Run(cctx, dp.hub, mqtt.Options{
				Broker:    cfg.Broker,
				Name:      cfg.Name,
				Username:  cfg.Username,
				Password:  cfg.Password,
				BusPrefix: cfg.BusPrefix, // D4: also ingest <BusPrefix>/# as hub events
				// #279: and the talker-alias announcements that ride the same prefix.
				OnTalkerAlias: dp.onTalkerAlias,
				// #22: DMRGateway's own status plane, where a failed or successful
				// master login is announced the moment it happens.
				GatewayNames: []string{config.MQTTNameDMRGateway},
			}); err != nil && cctx.Err() == nil {
				log.Printf("mqtt bridge stopped: %v", err)
			}
		}()
	}
	if rebuildPub {
		avail := ""
		if dp.haDiscovery {
			avail = status.AvailabilityTopic(cfg.StatusPrefix)
		}
		dp.pub = mqtt.NewPublisher(mqtt.Options{
			Broker: cfg.Broker, Name: cfg.Name, Username: cfg.Username, Password: cfg.Password,
		}, avail)
		// The commander's connection depends on exactly what the publisher's does,
		// so it is rebuilt on the same trigger.
		oldCmd := dp.cmd
		dp.cmd = mqtt.NewCommander(mqtt.Options{
			Broker: cfg.Broker, Username: cfg.Username, Password: cfg.Password,
		}, []string{config.MQTTNameDMRGateway}, 0)
		if oldCmd != nil {
			defer oldCmd.Close()
		}
	}
	dp.mu.Unlock()

	// Tear the old connections down OUTSIDE the lock: Publisher.Close and the
	// consumer's shutdown both block on the network, and holding the mutex through
	// them would stall every status callback for the duration.
	if restartConsumer && oldCancel != nil {
		oldCancel()
	}
	if rebuildPub && oldPub != nil {
		oldPub.Close()
	}
	if prev != (dataPlaneConfig{}) {
		log.Printf("mqtt: data plane reconfigured (broker %s, name %s, status %s, bus %s)",
			cfg.Broker, cfg.Name, cfg.StatusPrefix, cfg.BusPrefix)
	}
}
