package status

import (
	"encoding/json"
	"strings"
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
