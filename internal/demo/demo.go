// Package demo synthesizes realistic digital-voice traffic so the dashboard
// and API can be developed and demonstrated without radio hardware or the
// MQTT bridge. Demo mode is always labeled as such in /api/health.
package demo

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

var stations = []struct {
	call string
	dst  string
	mode string
	slot int
}{
	{"KN4OQW", "TG 9", "DMR", 2},
	{"KN4OQW", "TG 3112", "DMR", 2},
	{"W4ABC", "TG 31121", "DMR", 2},
	{"K4XYZ", "TG 9", "DMR", 2},
	{"N4QRP", "TG 3100", "DMR", 1},
	{"KM4SSB", "FL-TREASURE", "YSF", 0},
	{"W4FLA", "AMERICA-LINK", "YSF", 0},
}

var networks = []string{"BM_3102_United_States", "TGIF_Network", "YSF FL-TREASURE"}

// Run publishes synthetic traffic to h until ctx is canceled.
func Run(ctx context.Context, h *hub.Hub) {
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	h.Publish(hub.Event{Time: time.Now(), Type: "mode", Mode: "IDLE", Detail: "demo feed started — synthetic traffic, no radio attached"})
	for _, n := range networks[:2] {
		h.Publish(hub.Event{Time: time.Now(), Type: "link", Network: n, Detail: "logged in"})
	}

	for {
		// idle gap between QSOs
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(2+rng.Intn(8)) * time.Second):
		}

		s := stations[rng.Intn(len(stations))]
		net := networks[rng.Intn(2)]
		now := time.Now()

		rf := rng.Intn(2) == 0 // RF keyup vs network traffic
		dur := 0.5 + rng.Float64()*8

		if rf {
			h.Publish(hub.Event{Time: now, Type: "rf_voice_start", Mode: s.mode, Slot: s.slot, Source: s.call, Dest: s.dst, Network: net})
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(dur * float64(time.Second))):
			}
			h.Publish(hub.Event{
				Time: time.Now(), Type: "rf_voice_end", Mode: s.mode, Slot: s.slot,
				Source: s.call, Dest: s.dst, Network: net,
				Seconds: round1(dur), BER: round1(rng.Float64() * 1.4), RSSI: -47 - rng.Intn(50),
			})
		} else {
			h.Publish(hub.Event{Time: now, Type: "net_voice_start", Mode: s.mode, Slot: s.slot, Source: s.dst, Dest: s.call, Network: net})
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(dur * float64(time.Second))):
			}
			h.Publish(hub.Event{
				Time: time.Now(), Type: "net_voice_end", Mode: s.mode, Slot: s.slot,
				Source: s.dst, Dest: s.call, Network: net, Seconds: round1(dur),
			})
		}
	}
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

// Banner returns the demo-mode description for /api/health.
func Banner() string {
	return fmt.Sprintf("demo feed: %d synthetic stations, no radio attached", len(stations))
}
