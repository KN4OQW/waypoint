package main

import (
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
)

// TestLoopbackTable pins the fixed loopback pairs against render.go's constants:
// DMR rides the local DMRGateway (bind host 62032 -> peer 62031); YSF and NXDN
// replace their gateways (bind gateway 4200/14020 -> peer host 3200/14021).
func TestLoopbackTable(t *testing.T) {
	cases := []struct {
		mode       config.Mode
		bind, peer int
	}{
		{config.ModeDMR, 62032, 62031},
		{config.ModeYSF, 4200, 3200},
		{config.ModeNXDN, 14020, 14021},
	}
	for _, c := range cases {
		lb, err := loopbackFor(c.mode)
		if err != nil {
			t.Fatalf("%s: %v", c.mode, err)
		}
		if lb.bind != c.bind || lb.peer != c.peer {
			t.Fatalf("%s: got bind %d peer %d, want bind %d peer %d", c.mode, lb.bind, lb.peer, c.bind, c.peer)
		}
	}
	if _, err := loopbackFor(config.ModeP25); err == nil {
		t.Fatal("a non-reframe mode should have no loopback endpoint")
	}
}
