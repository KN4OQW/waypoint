package status

import (
	"encoding/json"
	"strings"
	"sync"
)

// Republish maps a Status onto the normalized waypoint/status/# topics (RFC-0008)
// and hands each (topic, payload) to publish. It is transport-agnostic — the
// caller supplies a publish func backed by the MQTT client (retained, best-effort)
// — so the topic scheme is unit-testable with a fake publisher and never drags an
// MQTT dependency into this package. Topics are retained by the caller so a Home
// Assistant that (re)starts reads current state immediately with zero YAML.
//
//	<prefix>/mode            the active mode, e.g. "DMR" (plain string)
//	<prefix>/tx              the current Transmission as JSON, or "" when idle
//	<prefix>/feed            the Feed as JSON
//	<prefix>/network/<name>  each network/reflector Link as JSON
//	<prefix>/gateway/<name>  each gateway-daemon Link as JSON
func Republish(s Status, prefix string, publish func(topic string, payload []byte)) {
	prefix = strings.TrimRight(prefix, "/")

	publish(prefix+"/mode", []byte(s.Mode))

	if s.TX == nil {
		publish(prefix+"/tx", []byte("")) // empty retained payload clears the topic's last value
	} else {
		publish(prefix+"/tx", mustJSON(s.TX))
	}

	publish(prefix+"/feed", mustJSON(s.Feed))

	for name, link := range s.Networks {
		publish(prefix+"/network/"+topicSafe(name), mustJSON(link))
	}
	for name, link := range s.Gateways {
		publish(prefix+"/gateway/"+topicSafe(name), mustJSON(link))
	}
}

// Republisher is Republish with a memory, which is what a *retained* topic scheme
// needs. Republish alone publishes what currently exists; a network the operator
// has deleted simply stops being published, and its last retained payload stays on
// the broker for every future subscriber — so Home Assistant keeps an entity for a
// network the node no longer has, and the topic outlives the thing it described.
//
// Tracking what was published lets a vanished name be cleared explicitly, with the
// same empty retained payload the idle tx topic uses.
type Republisher struct {
	mu       sync.Mutex
	networks map[string]bool
	gateways map[string]bool
}

// Publish emits the current status and clears the topics of anything that has gone
// since the last call.
func (r *Republisher) Publish(s Status, prefix string, publish func(topic string, payload []byte)) {
	Republish(s, prefix, publish)

	prefix = strings.TrimRight(prefix, "/")
	r.mu.Lock()
	defer r.mu.Unlock()
	r.networks = clearGone(r.networks, s.Networks, prefix+"/network/", publish)
	r.gateways = clearGone(r.gateways, s.Gateways, prefix+"/gateway/", publish)
}

// clearGone publishes an empty retained payload for every name that was present
// last time and is not now, and returns the new set.
func clearGone(was map[string]bool, now map[string]Link, prefix string, publish func(string, []byte)) map[string]bool {
	for name := range was {
		if _, still := now[name]; !still {
			publish(prefix+topicSafe(name), []byte(""))
		}
	}
	next := make(map[string]bool, len(now))
	for name := range now {
		next[name] = true
	}
	return next
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte("")
	}
	return b
}

// topicSafe makes a network/gateway name safe for an MQTT topic level: no '/',
// '+', '#', or whitespace (which would break the topic hierarchy or wildcards).
func topicSafe(name string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '/', '+', '#', ' ', '\t', '\n':
			return '_'
		}
		return r
	}, name)
}
