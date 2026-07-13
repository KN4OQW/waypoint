// Package mqtt bridges MMDVM-Host's MQTT/JSON data plane onto the Waypoint
// event hub. MMDVM-Host publishes one retained-free message per event to
// <name>/json, each shaped as {"<MODE>": {...fields...}}; this package
// translates those envelopes into hub.Event values so the SSE/UI layer sees
// the same schema whether it is driven by real hardware or the demo feed.
package mqtt

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/hub"
)

// voiceModes are the envelope keys that carry per-transmission voice activity.
// The other top-level keys MMDVM-Host emits (RSSI, BER, Text, MMDVM) are
// periodic side-channels the dashboard does not surface yet.
var voiceModes = map[string]bool{
	"DMR": true, "D-Star": true, "YSF": true, "P25": true, "NXDN": true, "FM": true, "M17": true,
}

// frame is the superset of fields MMDVM-Host places inside a mode envelope.
// Modes populate different subsets — D-Star carries callsigns, DMR carries
// numeric IDs plus a resolved src_info — so every field is optional and we
// read whatever is present.
type frame struct {
	Timestamp string     `json:"timestamp"`
	Source    string     `json:"source"` // "rf" | "network"; absent on end/lost/timeout
	Action    string     `json:"action"` // start, late_entry, end, lost, timeout, rejected, csbk, ...
	Slot      int        `json:"slot"`
	SrcID     int        `json:"src_id"`
	DstID     int        `json:"dst_id"`
	SrcInfo   string     `json:"src_info"`
	Group     string     `json:"group"` // "yes" | "no"
	SrcCall   string     `json:"src_callsign"`
	DstCall   string     `json:"dst_callsign"`
	Reflector string     `json:"reflector"`
	Duration  float64    `json:"duration"`
	BER       float64    `json:"ber"`
	RSSI      *rssiBlock `json:"rssi"`
}

type rssiBlock struct {
	Min int `json:"min"`
	Max int `json:"max"`
	Ave int `json:"ave"`
}

// active records the identity of the transmission currently keyed up on a
// (mode, slot), so an end/lost/timeout — which MMDVM-Host emits without any
// identity or source fields — can be reported with the same callsign, target,
// and rf/network direction as its opening start.
type active struct {
	network bool
	source  string
	dest    string
	net     string
}

// Bridge translates MMDVM-Host json envelopes into hub events. It is safe for
// use from a single MQTT callback goroutine; the mutex guards the active map in
// case the client dispatches concurrently.
type Bridge struct {
	mu     sync.Mutex
	active map[string]active
}

// NewBridge returns a Bridge with no transmissions in flight.
func NewBridge() *Bridge {
	return &Bridge{active: make(map[string]active)}
}

// Translate parses one <name>/json payload and returns the hub events it maps
// to (usually one, occasionally zero for non-voice actions, rarely several if a
// payload carries multiple mode keys). A malformed payload yields no events.
func (b *Bridge) Translate(payload []byte) []hub.Event {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil
	}

	// Deterministic order so a multi-mode payload maps predictably (and tests
	// are stable); single-mode payloads — the common case — are unaffected.
	modes := make([]string, 0, len(env))
	for m := range env {
		modes = append(modes, m)
	}
	sort.Strings(modes)

	var out []hub.Event
	for _, mode := range modes {
		if !voiceModes[mode] {
			continue
		}
		var f frame
		if err := json.Unmarshal(env[mode], &f); err != nil {
			continue
		}
		if e, ok := b.toEvent(mode, f); ok {
			out = append(out, e)
		}
	}
	return out
}

func (b *Bridge) toEvent(mode string, f frame) (hub.Event, bool) {
	key := mode + "/" + strconv.Itoa(f.Slot)
	e := hub.Event{Time: parseTimestamp(f.Timestamp), Mode: mode, Slot: f.Slot}

	switch f.Action {
	case "start", "late_entry":
		network := f.Source == "network"
		e.Source = firstNonEmpty(f.SrcInfo, f.SrcCall, idString(f.SrcID))
		e.Dest = destination(f)
		e.Network = f.Reflector
		if network {
			e.Type = "net_voice_start"
		} else {
			e.Type = "rf_voice_start"
		}
		b.mu.Lock()
		b.active[key] = active{network: network, source: e.Source, dest: e.Dest, net: e.Network}
		b.mu.Unlock()
		return e, true

	case "end", "lost", "timeout":
		b.mu.Lock()
		a := b.active[key]
		delete(b.active, key)
		b.mu.Unlock()
		// The end payload omits source/identity; recover them from the start.
		network := a.network || f.Source == "network"
		e.Source = firstNonEmpty(f.SrcInfo, f.SrcCall, a.source)
		e.Dest = firstNonEmpty(destination(f), a.dest)
		e.Network = firstNonEmpty(f.Reflector, a.net)
		e.Seconds = round1(f.Duration)
		e.BER = round1(f.BER)
		if f.RSSI != nil {
			e.RSSI = f.RSSI.Ave
		}
		if f.Action == "timeout" {
			e.Detail = "timeout"
		} else if f.Action == "lost" {
			e.Detail = "signal lost"
		}
		if network {
			e.Type = "net_voice_end"
		} else {
			e.Type = "rf_voice_end"
		}
		return e, true

	default:
		// rejected, csbk, and any future actions are not surfaced yet.
		return hub.Event{}, false
	}
}

// destination renders the call target: a callsign for the analog/FM and
// C4FM-style modes, or a talkgroup / private-call ID for DMR.
func destination(f frame) string {
	if f.DstCall != "" {
		return f.DstCall
	}
	if f.DstID == 0 {
		return ""
	}
	if f.Group == "yes" {
		return "TG " + strconv.Itoa(f.DstID)
	}
	return strconv.Itoa(f.DstID)
}

func idString(id int) string {
	if id == 0 {
		return ""
	}
	return strconv.Itoa(id)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// parseTimestamp reads MMDVM-Host's "2006-01-02T15:04:05.000Z" stamp, falling
// back to now if it is missing or unparseable.
func parseTimestamp(s string) time.Time {
	if s == "" {
		return time.Now().UTC()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	return time.Now().UTC()
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}
