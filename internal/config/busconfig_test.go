package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func validBusConfig() BusConfig {
	return BusConfig{
		Bus: Bus{ID: "bus-a", Name: "Local Bus A", Enabled: true},
		Attachments: []Attachment{
			{BusID: "bus-a", Mode: ModeDMR, Slot: "2", DefaultTG: "91"},
			{BusID: "bus-a", Mode: ModeYSF, Target: "US-Alabama-Link"},
		},
		HangTimeSeconds: 4,
	}
}

// TestBusConfigRoundTrip: Marshal -> ReadBusConfig preserves every field (the
// daemon reads exactly what the renderer writes — RFC-0003 §6.2).
func TestBusConfigRoundTrip(t *testing.T) {
	want := validBusConfig()
	raw, err := want.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "bus-a.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadBusConfig(path)
	if err != nil {
		t.Fatalf("ReadBusConfig: %v", err)
	}
	if got.Bus != want.Bus {
		t.Fatalf("bus drifted: %+v vs %+v", got.Bus, want.Bus)
	}
	if len(got.Attachments) != 2 || got.Attachments[0].Slot != "2" || got.Attachments[1].Target != "US-Alabama-Link" {
		t.Fatalf("attachments drifted: %+v", got.Attachments)
	}
	if got.HangTime() != 4*time.Second {
		t.Fatalf("hang time drifted: %v", got.HangTime())
	}
}

func TestBusConfigHangTimeDefault(t *testing.T) {
	c := validBusConfig()
	c.HangTimeSeconds = 0
	if c.HangTime() != DefaultBusHangTime {
		t.Fatalf("unset hang time should default to %v, got %v", DefaultBusHangTime, c.HangTime())
	}
}

func TestBusConfigRejectsInvalid(t *testing.T) {
	cases := map[string]func(*BusConfig){
		"disabled bus":        func(c *BusConfig) { c.Bus.Enabled = false },
		"empty bus id":        func(c *BusConfig) { c.Bus.ID = "" },
		"single attachment":   func(c *BusConfig) { c.Attachments = c.Attachments[:1] },
		"attachment mismatch": func(c *BusConfig) { c.Attachments[0].BusID = "other" },
		"non-reframe mode":    func(c *BusConfig) { c.Attachments[1].Mode = ModeDStar },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := validBusConfig()
			mutate(&c)
			raw, _ := c.Marshal()
			path := filepath.Join(t.TempDir(), "bad.json")
			os.WriteFile(path, raw, 0o644)
			if _, err := ReadBusConfig(path); err == nil {
				t.Fatalf("%s should be rejected by ReadBusConfig", name)
			}
		})
	}
}
