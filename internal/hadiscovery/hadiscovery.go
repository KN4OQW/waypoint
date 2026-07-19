// Package hadiscovery publishes the hotspot to Home Assistant over MQTT discovery
// (#9): it announces the node as a single HA device with last-heard / mode /
// activity / signal entities, then keeps a status topic fresh from the event hub.
// Home Assistant picks the entities up with zero YAML.
//
// The topic scheme is the one the architecture doc reserves — normalized status
// under waypoint/status/# — with the discovery bundle under Home Assistant's own
// discovery prefix:
//
//	waypoint/status/<node>/state          one retained JSON blob; entities read fields from it
//	waypoint/status/<node>/availability   "online"/"offline" (retained; the MQTT LWT)
//	<prefix>/device/<node>/config         the HA device-discovery bundle (retained)
//
// where <node> is the sanitized station callsign. It publishes to the same broker
// waypointd consumes the MMDVM-Host feed from, so an operator points Home
// Assistant at that broker and the hotspot appears.
package hadiscovery

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// StatusPrefix is the normalized-status topic root the architecture doc reserves
// for Home-Assistant-friendly telemetry.
const StatusPrefix = "waypoint/status/"

// DeviceInfo identifies the hotspot to Home Assistant. Callsign becomes the device
// name and (sanitized) the topic node id + unique_id root, so entities stay tied
// to one HA device and stable across restarts.
type DeviceInfo struct {
	Callsign  string
	DMRID     string // device serial_number, for the HA device page
	Version   string // sw_version
	ConfigURL string // configuration_url (the node's dashboard), optional
}

// nodeUnsafe matches every character not allowed in an MQTT-discovery node/object
// id (Home Assistant restricts these to [a-zA-Z0-9_-]).
var nodeUnsafe = regexp.MustCompile(`[^a-z0-9_-]+`)

// Node derives the discovery node id from a callsign: lowercased, with every
// disallowed character dropped (W1AW/3 -> w1aw3). A blank or fully-stripped
// callsign falls back to "waypoint" so the topics are always well-formed.
func Node(callsign string) string {
	n := nodeUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(callsign)), "")
	if n == "" {
		return "waypoint"
	}
	return n
}

// Topics are the concrete topics for a node, derived once at publisher start.
type Topics struct {
	State        string
	Availability string
	Discovery    string
}

// TopicsFor builds the topic set for a node under a discovery prefix (blank prefix
// falls back to Home Assistant's default, "homeassistant").
func TopicsFor(node, discoveryPrefix string) Topics {
	if discoveryPrefix == "" {
		discoveryPrefix = "homeassistant"
	}
	base := StatusPrefix + node
	return Topics{
		State:        base + "/state",
		Availability: base + "/availability",
		Discovery:    discoveryPrefix + "/device/" + node + "/config",
	}
}

// component is one HA entity in the device bundle's cmps map, using HA's
// abbreviated discovery keys.
type component struct {
	Platform   string `json:"p"`
	Name       string `json:"name"`
	ValueTmpl  string `json:"val_tpl"`
	UniqueID   string `json:"uniq_id"`
	DeviceCla  string `json:"dev_cla,omitempty"`
	StateCla   string `json:"stat_cla,omitempty"`
	Unit       string `json:"unit_of_meas,omitempty"`
	Icon       string `json:"ic,omitempty"`
	EntityCat  string `json:"ent_cat,omitempty"`
	PayloadOn  string `json:"pl_on,omitempty"`
	PayloadOff string `json:"pl_off,omitempty"`
}

// components returns the entity set the hotspot exposes, keyed by a stable
// component id. The keys and unique_ids must not change across republishes or Home
// Assistant would orphan the old entities and create duplicates.
func components(node string) map[string]component {
	uid := func(k string) string { return "waypoint_" + node + "_" + k }
	return map[string]component{
		"last_heard":      {Platform: "sensor", Name: "Last Heard", ValueTmpl: "{{ value_json.last_heard }}", UniqueID: uid("last_heard"), Icon: "mdi:account-voice"},
		"last_target":     {Platform: "sensor", Name: "Last Target", ValueTmpl: "{{ value_json.last_target }}", UniqueID: uid("last_target"), Icon: "mdi:target"},
		"last_mode":       {Platform: "sensor", Name: "Last Mode", ValueTmpl: "{{ value_json.last_mode }}", UniqueID: uid("last_mode"), Icon: "mdi:radio"},
		"last_heard_time": {Platform: "sensor", Name: "Last Heard Time", ValueTmpl: "{{ value_json.last_time }}", UniqueID: uid("last_heard_time"), DeviceCla: "timestamp"},
		"activity":        {Platform: "binary_sensor", Name: "Activity", ValueTmpl: "{{ value_json.active }}", UniqueID: uid("activity"), DeviceCla: "running", PayloadOn: "ON", PayloadOff: "OFF"},
		"current_mode":    {Platform: "sensor", Name: "Current Mode", ValueTmpl: "{{ value_json.current_mode }}", UniqueID: uid("current_mode"), Icon: "mdi:sine-wave"},
		"network":         {Platform: "sensor", Name: "Network", ValueTmpl: "{{ value_json.network }}", UniqueID: uid("network"), Icon: "mdi:lan-connect"},
		"last_ber":        {Platform: "sensor", Name: "Last BER", ValueTmpl: "{{ value_json.last_ber }}", UniqueID: uid("last_ber"), Unit: "%", StateCla: "measurement", EntityCat: "diagnostic"},
		"last_rssi":       {Platform: "sensor", Name: "Last RSSI", ValueTmpl: "{{ value_json.last_rssi }}", UniqueID: uid("last_rssi"), Unit: "dBm", DeviceCla: "signal_strength", StateCla: "measurement", EntityCat: "diagnostic"},
		"last_duration":   {Platform: "sensor", Name: "Last Duration", ValueTmpl: "{{ value_json.last_duration }}", UniqueID: uid("last_duration"), Unit: "s", DeviceCla: "duration", EntityCat: "diagnostic"},
	}
}

// EntityCount is how many entities the discovery bundle announces.
func EntityCount() int { return len(components("x")) }

// DiscoveryPayload builds the retained device-discovery bundle for a device. The
// device block ties every entity to one HA device via a stable identifier (the
// node id); availability and the shared state topic sit at the root so all
// components inherit them.
func DiscoveryPayload(dev DeviceInfo, discoveryPrefix string) (topic string, payload []byte, err error) {
	node := Node(dev.Callsign)
	t := TopicsFor(node, discoveryPrefix)

	name := strings.TrimSpace(dev.Callsign)
	if name == "" {
		name = "Waypoint"
	}
	device := map[string]any{
		"ids":  node,
		"name": "Waypoint " + name,
		"mf":   "Waypoint",
		"mdl":  "MMDVM Hotspot",
	}
	if dev.Version != "" {
		device["sw"] = dev.Version
	}
	if dev.DMRID != "" {
		device["sn"] = dev.DMRID
	}
	if dev.ConfigURL != "" {
		device["cu"] = dev.ConfigURL
	}

	bundle := map[string]any{
		"dev": device,
		"o": map[string]any{
			"name": "waypointd",
			"url":  "https://github.com/KN4OQW/waypoint",
		},
		"avty_t":       t.Availability,
		"pl_avail":     "online",
		"pl_not_avail": "offline",
		"stat_t":       t.State,
		"qos":          1,
		"cmps":         components(node),
	}
	payload, err = json.Marshal(bundle)
	return t.Discovery, payload, err
}

// State is the current hotspot status published to the state topic. Every entity
// reads one field from this single JSON blob via its value_template — one retained
// topic, many entities. It mirrors the dashboard's reducer (ui/static/app.js).
type State struct {
	LastHeard   string  `json:"last_heard"`
	LastTarget  string  `json:"last_target"`
	LastMode    string  `json:"last_mode"`
	LastTime    string  `json:"last_time"` // RFC-3339; the timestamp entity reads this
	Active      string  `json:"active"`    // "ON" while a transmission is keyed, else "OFF"
	CurrentMode string  `json:"current_mode"`
	Network     string  `json:"network"`
	LastBER     float64 `json:"last_ber"`
	LastRSSI    int     `json:"last_rssi"`
	LastSeconds float64 `json:"last_duration"`
}

// NewState is the idle starting status: nothing heard yet, not transmitting.
func NewState() *State { return &State{Active: "OFF"} }

// Apply folds one hub event into the status, mirroring the dashboard's handle():
// a keyup sets the current transmitter and marks activity on; a keydown records
// the transmission's metrics and clears activity; link and mode events update the
// network and current-mode fields.
func (s *State) Apply(e hub.Event) {
	switch e.Type {
	case "rf_voice_start", "net_voice_start":
		s.Active = "ON"
		s.setTransmission(e)
	case "rf_voice_end", "net_voice_end":
		s.Active = "OFF"
		s.setTransmission(e)
		s.LastBER = e.BER
		s.LastRSSI = e.RSSI
		s.LastSeconds = e.Seconds
	case "link":
		if d := firstNonEmpty(e.Detail, e.Network); d != "" {
			s.Network = d
		}
	case "mode":
		if e.Mode != "" {
			s.CurrentMode = e.Mode
		}
	}
}

func (s *State) setTransmission(e hub.Event) {
	if e.Source != "" {
		s.LastHeard = e.Source
	}
	if e.Dest != "" {
		s.LastTarget = e.Dest
	}
	if e.Mode != "" {
		s.LastMode = e.Mode
	}
	if !e.Time.IsZero() {
		s.LastTime = e.Time.UTC().Format(time.RFC3339)
	}
}

// Encode serializes the status for the state topic.
func (s *State) Encode() ([]byte, error) { return json.Marshal(s) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
