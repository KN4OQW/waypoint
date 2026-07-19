package status

import (
	"encoding/json"
	"sort"
	"strings"
)

// Home Assistant MQTT Discovery (RFC-0011 / issue #9): retained config messages
// under <prefix>/<component>/<node>/<object>/config that point HA at the
// waypoint/status/# state topics (RFC-0008), so a hotspot appears in Home
// Assistant with zero YAML. Like Republish, this is a pure, transport-agnostic
// projection — it drags no MQTT dependency into this package and is unit-testable
// with the payloads inspected directly.

// DiscoveryOptions configures the discovery projection.
type DiscoveryOptions struct {
	Prefix      string // HA discovery prefix (default "homeassistant")
	NodeID      string // device id + topic node segment (stable across restarts)
	StatePrefix string // the RFC-0008 state prefix (e.g. "waypoint/status")
	Version     string // daemon version, surfaced as the device sw_version
}

// Discovery is one retained config message: its topic and JSON payload.
type Discovery struct {
	Topic   string
	Payload []byte
}

// AvailabilityTopic is the device-level online/offline topic (the MQTT Last-Will
// target). Every discovered entity references it as its availability_topic.
func AvailabilityTopic(statePrefix string) string {
	return strings.TrimRight(statePrefix, "/") + "/availability"
}

// DiscoveryConfigs returns the retained HA discovery config messages for the
// current status: the always-present mode/tx/feed entities plus one binary sensor
// per gateway and per network the status holds. Deterministic order (mode, tx,
// feed, gateways sorted, networks sorted) for stable tests and predictable output.
func DiscoveryConfigs(s Status, opts DiscoveryOptions) []Discovery {
	prefix := strings.TrimRight(firstNonEmpty(opts.Prefix, "homeassistant"), "/")
	node := topicSafe(firstNonEmpty(opts.NodeID, "waypoint"))
	sp := strings.TrimRight(opts.StatePrefix, "/")
	avail := AvailabilityTopic(sp)

	device := map[string]any{
		"identifiers":  []string{"waypoint_" + node},
		"name":         "Waypoint " + node,
		"manufacturer": "Waypoint",
		"model":        "MMDVM hotspot",
	}
	if opts.Version != "" {
		device["sw_version"] = opts.Version
	}

	// base builds the fields every entity shares.
	base := func(object, name, stateTopic string) map[string]any {
		return map[string]any{
			"name":                  name,
			"unique_id":             "waypoint_" + node + "_" + object,
			"object_id":             "waypoint_" + node + "_" + object,
			"state_topic":           stateTopic,
			"availability_topic":    avail,
			"payload_available":     "online",
			"payload_not_available": "offline",
			"device":                device,
		}
	}
	// binary is base + the on/off template shared by every liveness sensor.
	binary := func(object, name, stateTopic, deviceClass string) map[string]any {
		m := base(object, name, stateTopic)
		m["value_template"] = "{{ value_json.up }}"
		m["payload_on"] = "True"
		m["payload_off"] = "False"
		m["device_class"] = deviceClass
		return m
	}

	var out []Discovery
	add := func(component, object string, payload map[string]any) {
		b, err := json.Marshal(payload)
		if err != nil {
			return
		}
		out = append(out, Discovery{Topic: prefix + "/" + component + "/" + node + "/" + object + "/config", Payload: b})
	}

	// Mode.
	mode := base("mode", "Mode", sp+"/mode")
	mode["icon"] = "mdi:radio-tower"
	add("sensor", "mode", mode)

	// Active transmission — "source → dest" when keyed up, "Idle" on the empty payload.
	tx := base("tx", "Active transmission", sp+"/tx")
	tx["value_template"] = "{% if value %}{{ value_json.source }} → {{ value_json.dest }}{% else %}Idle{% endif %}"
	tx["icon"] = "mdi:account-voice"
	add("sensor", "tx", tx)

	// Feed health.
	feed := binary("feed", "MMDVM feed", sp+"/feed", "connectivity")
	feed["value_template"] = "{{ value_json.connected }}"
	add("binary_sensor", "feed", feed)

	// One binary sensor per gateway (liveness) and per network (link), sorted.
	for _, name := range sortedKeys(s.Gateways) {
		safe := topicSafe(name)
		add("binary_sensor", "gateway_"+safe, binary("gateway_"+safe, "Gateway "+name, sp+"/gateway/"+safe, "running"))
	}
	for _, name := range sortedKeys(s.Networks) {
		safe := topicSafe(name)
		add("binary_sensor", "network_"+safe, binary("network_"+safe, "Network "+name, sp+"/network/"+safe, "connectivity"))
	}
	return out
}

func sortedKeys(m map[string]Link) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
