package config

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// RenderTargets bus wiring (RFC-0003 §4): one target per ENABLED bus — its
// rendered config plus the templated unit — and none for a disabled bus.

func busModel() *Model {
	return &Model{
		Buses: []Bus{
			{ID: "bus-a", Name: "Local Bus A", Enabled: true},
			{ID: "bus-off", Name: "Disabled", Enabled: false},
		},
		Attachments: []Attachment{
			{BusID: "bus-a", Mode: ModeDMR, Slot: "2", DefaultTG: "91"},
			{BusID: "bus-a", Mode: ModeYSF, Target: "US-Alabama"},
			{BusID: "bus-off", Mode: ModeNXDN},
		},
	}
}

func TestRenderTargetsEmitsEnabledBus(t *testing.T) {
	m := busModel()
	paths := Paths{BusConfigDir: "/etc/waypoint/buses"}

	var busTargets []RenderTarget
	for _, tg := range m.RenderTargets(paths) {
		if strings.HasPrefix(tg.Unit, "waypoint-bus@") {
			busTargets = append(busTargets, tg)
		}
	}
	if len(busTargets) != 1 {
		t.Fatalf("want exactly one bus target (only bus-a is enabled), got %d", len(busTargets))
	}
	tg := busTargets[0]
	if tg.Unit != "waypoint-bus@bus-a.service" {
		t.Fatalf("bus unit = %q, want waypoint-bus@bus-a.service", tg.Unit)
	}
	if tg.Path != "/etc/waypoint/buses/waypoint-bus-bus-a.json" {
		t.Fatalf("bus config path = %q", tg.Path)
	}

	// The rendered config must parse back into a BusConfig the daemon accepts.
	raw := tg.Render(m)
	var bc BusConfig
	if err := json.Unmarshal([]byte(raw), &bc); err != nil {
		t.Fatalf("rendered bus config is not valid JSON: %v\n%s", err, raw)
	}
	if bc.Bus.ID != "bus-a" || len(bc.Attachments) != 2 {
		t.Fatalf("rendered config lost bus/attachments: %+v", bc)
	}
	if err := bc.Validate(); err != nil {
		t.Fatalf("rendered bus config fails the daemon's own validation: %v", err)
	}
}

func TestRenderTargetsDisabledBusEmitsNothing(t *testing.T) {
	m := &Model{
		Buses:       []Bus{{ID: "bus-off", Name: "Off", Enabled: false}},
		Attachments: []Attachment{{BusID: "bus-off", Mode: ModeDMR}, {BusID: "bus-off", Mode: ModeYSF}},
	}
	for _, tg := range m.RenderTargets(Paths{BusConfigDir: "/x"}) {
		if strings.HasPrefix(tg.Unit, "waypoint-bus@") {
			t.Fatalf("a disabled bus must contribute no render target, got %q", tg.Unit)
		}
	}
	// It should instead be named for stopping.
	units := m.DisabledBusUnits()
	if len(units) != 1 || units[0] != "waypoint-bus@bus-off.service" {
		t.Fatalf("disabled bus should be listed for stopping, got %v", units)
	}
}

func TestRenderTargetsBusPureRender(t *testing.T) {
	// RFC-0001 property 1 extended to buses: the target list is deterministic.
	m := busModel()
	paths := Paths{BusConfigDir: "/etc/waypoint/buses"}
	a, b := m.RenderTargets(paths), m.RenderTargets(paths)
	if len(a) != len(b) {
		t.Fatalf("render target count not stable: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Unit != b[i].Unit || a[i].Path != b[i].Path {
			t.Fatalf("target %d not stable: %+v vs %+v", i, a[i], b[i])
		}
		if !reflect.DeepEqual(a[i].Render(m), b[i].Render(m)) {
			t.Fatalf("target %d render not stable", i)
		}
	}
}
