package publicview

import (
	"strings"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/status"
)

// Node is the reach card: "how to reach this node" (D2), and nothing else.
//
// This is the struct the never-public list bites hardest on, because the
// configuration it is built from is full of things that must not cross. The model
// it reads carries modem ports, gateway addresses, network passwords, DMR master
// hosts, calibration levels and the operator's free-text location; none of them
// appear below, and the field audit is what keeps it that way as the config model
// grows.
//
// Every field is omitempty and every field is gated on its toggle. A field whose
// toggle is off is absent from the JSON rather than present and blank: the
// difference matters, because "" is still an answer to "what colour code does this
// node use", and the point of a disclosure toggle is to decline the question.
type Node struct {
	// Callsign is the node's identity and the one field with no toggle. A public
	// page for an unidentified node would be meaningless, so enabling the public
	// view is the consent for this one.
	Callsign string `json:"callsign,omitempty"`

	// Reach card, gated on show_freq / show_cc_ts / show_mode / show_talkgroup.
	RXFrequency string   `json:"rx_frequency,omitempty"` // Hz, as configured
	TXFrequency string   `json:"tx_frequency,omitempty"`
	ColorCode   string   `json:"color_code,omitempty"`
	Slots       string   `json:"slots,omitempty"` // "TS1", "TS2", or "TS1+TS2"
	Modes       []string `json:"modes,omitempty"`
	Talkgroup   string   `json:"talkgroup,omitempty"` // the talkgroup currently up, if any

	// Grid is a 6-character Maidenhead locator at most (D3).
	//
	// It comes from the operator's grid_override and from nothing else, because
	// there is nothing else: the configuration model carries no station
	// coordinates at all. General.Location is free-form prose ("Munson tower, 310
	// ft"), and the one place a latitude reaches a rendered file is
	// DStarGateway.ini, where render.go writes a hardcoded 0.0. So the derivation
	// the design assumed — grid from configured coordinates when no override is
	// set — has no input to derive from.
	//
	// Leaving it override-only is the right answer rather than a stopgap. A grid
	// square is a disclosure, and requiring the operator to type the one they are
	// willing to publish is a clearer consent than inferring one from a field they
	// filled in for another purpose. If station coordinates are ever modeled, this
	// is where the fallback goes.
	Grid string `json:"grid,omitempty"`

	// Operator-authored blocks.
	PowerLine       string   `json:"power_line,omitempty"`
	PurposeTags     []string `json:"purpose_tags,omitempty"`
	PurposeFreetext string   `json:"purpose_freetext,omitempty"`
	Links           []Link   `json:"links,omitempty"`
	Nets            []Net    `json:"nets,omitempty"`
}

// BuildNode assembles the reach card from the configuration, the public settings,
// and the live status, disclosing only what the toggles permit.
//
// live may be nil; the talkgroup is then simply absent, which is the same thing a
// visitor sees when nothing is on the air.
func BuildNode(m *config.Model, set Settings, live *status.Status, links []Link, nets []Net) Node {
	var n Node
	if m != nil {
		n.Callsign = strings.ToUpper(strings.TrimSpace(m.General.Callsign))
	}

	if m != nil && set.ShowFreq {
		n.RXFrequency = m.Modem.RXFreqHz
		n.TXFrequency = m.Modem.TXFreqHz
	}
	if m != nil && set.ShowCCTS {
		n.ColorCode = m.DMR.ColorCode
		n.Slots = slots(m)
	}
	if m != nil && set.ShowMode {
		n.Modes = enabledModes(m.Modes)
	}
	if set.ShowTalkgroup && live != nil && live.TX != nil {
		// Only what is on the air right now, and only its destination — and only
		// when that destination is a group. The transmission also carries the
		// source callsign, the network and the slot; the reach card is about the
		// node, not about who is using it, and the last-heard list is where station
		// identity belongs.
		if tg, ok := publishableTalkgroup(live.TX.Dest); ok {
			n.Talkgroup = tg
		}
	}
	if set.ShowGrid {
		n.Grid = set.GridOverride
	}
	if set.ShowPowerLine {
		n.PowerLine = set.PowerLine
	}
	// Purpose tags and free text ride with the reach card rather than having a
	// toggle of their own: they exist only because an operator typed them, so the
	// act of setting them is the opt-in.
	n.PurposeTags = set.PurposeTags
	n.PurposeFreetext = set.PurposeFreetext
	if set.ShowLinks {
		n.Links = links
	}
	if set.ShowNets {
		n.Nets = nets
	}
	return n
}

// slots renders which DMR timeslots the node carries.
func slots(m *config.Model) string {
	switch {
	case m.DMRNet.Slot1 && m.DMRNet.Slot2:
		return "TS1+TS2"
	case m.DMRNet.Slot1:
		return "TS1"
	case m.DMRNet.Slot2:
		return "TS2"
	}
	return ""
}

// enabledModes lists the modes an operator could actually work, in the order the
// dashboard uses. It reports what is enabled, never what the firmware supports or
// what board is attached — that is hardware inventory, not a reach card.
func enabledModes(m config.Modes) []string {
	out := []string{}
	for _, e := range []struct {
		on   bool
		name string
	}{
		{m.DStar, "D-Star"},
		{m.DMR, "DMR"},
		{m.YSF, "YSF"},
		{m.P25, "P25"},
		{m.NXDN, "NXDN"},
		{m.M17, "M17"},
		{m.FM, "FM"},
		{m.POCSAG, "POCSAG"},
	} {
		if e.on {
			out = append(out, e.name)
		}
	}
	return out
}

// publishableTalkgroup decides whether a transmission's destination may appear on
// the reach card, and it is a narrower question than it looks.
//
// hub.Event.Dest is whatever the mode's destination is, and only some of those are
// groups. internal/mqtt's destination() renders it as:
//
//   - "TG 31123" for a DMR/P25/NXDN group call — a talkgroup, which is exactly
//     what the reach card is advertising and is safe to publish.
//   - a bare decimal ID ("3112345") for a DMR PRIVATE call — the callee's radio
//     ID, which identifies one person and is resolvable to a name and address
//     through the public ID databases.
//   - dst_callsign for D-Star, YSF and M17 — a callsign, either a station being
//     called directly or a reflector/DG-ID label, and the two are not reliably
//     distinguishable from the string.
//
// The last two are the reason this function exists. Publishing them would put a
// third party's identity on an anonymous page as a side effect of them being
// called, which no toggle covers and no suppress list catches — the suppressed
// party is the *source*, and this is the destination. So the rule is a positive
// one again: publish group destinations, decline everything else. The cost is a
// blank "active now" during a private call or a direct D-Star contact, which is
// the correct amount to say about someone else's QSO.
func publishableTalkgroup(dest string) (string, bool) {
	d := strings.TrimSpace(dest)
	if d == "" {
		return "", false
	}
	// The one shape the bridge marks unambiguously as a group call.
	if strings.HasPrefix(strings.ToUpper(d), "TG ") {
		return d, true
	}
	return "", false
}
