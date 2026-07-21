package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBusConfigRendersMQTT: the broker + prefix are rendered into the bus config
// when Paths carries a broker (D4), and absent otherwise (tests/demo).
func TestBusConfigRendersMQTT(t *testing.T) {
	m := busModel() // bus-a: DMR + YSF

	// With a broker: BusConfig.MQTT is populated from Paths.
	withBroker := m.renderBusConfig("bus-a", Paths{
		BusConfigDir: "/etc", MQTTBroker: "127.0.0.1:1883", BusTopicPrefix: "waypoint/bus",
	})
	var bc BusConfig
	if err := json.Unmarshal([]byte(withBroker), &bc); err != nil {
		t.Fatal(err)
	}
	if bc.MQTT == nil || bc.MQTT.Broker != "127.0.0.1:1883" || bc.MQTT.Prefix != "waypoint/bus" {
		t.Fatalf("MQTT block not rendered: %+v", bc.MQTT)
	}

	// Default prefix when unset.
	def := m.renderBusConfig("bus-a", Paths{BusConfigDir: "/etc", MQTTBroker: "127.0.0.1:1883"})
	var bcDef BusConfig
	_ = json.Unmarshal([]byte(def), &bcDef)
	if bcDef.MQTT == nil || bcDef.MQTT.Prefix != DefaultBusTopicPrefix {
		t.Fatalf("default prefix not applied: %+v", bcDef.MQTT)
	}

	// No broker: no MQTT block (demo/tests render without publishing).
	none := m.renderBusConfig("bus-a", Paths{BusConfigDir: "/etc"})
	var bc2 BusConfig
	_ = json.Unmarshal([]byte(none), &bc2)
	if bc2.MQTT != nil {
		t.Fatalf("no broker should render no MQTT block, got %+v", bc2.MQTT)
	}

	// Member config carries it too.
	m.Peers = []Peer{{ID: "p1", Name: "p1", State: PeerPaired, Certificate: "c", Fingerprint: "fp"}}
	m.RemoteAttachments = []RemoteAttachment{{BusID: "bus-a", PeerID: "p1", Mode: ModeNXDN}}
	mem := m.renderMemberConfig("bus-a", "p1", Paths{BusConfigDir: "/etc", MQTTBroker: "127.0.0.1:1883"})
	if !strings.Contains(mem, `"mqtt"`) || !strings.Contains(mem, "127.0.0.1:1883") {
		t.Fatalf("member config missing MQTT block:\n%s", mem)
	}
}
