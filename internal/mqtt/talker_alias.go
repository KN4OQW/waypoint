package mqtt

import (
	"encoding/json"
	"strings"
)

// talker_alias.go carries "who is talking" from a bus daemon to the one process
// that can put it on a radio.
//
// # Why it goes this way round
//
// The bus daemon knows the name — it came off Zello's own on_stream_start — and
// cannot deliver it. Its DMR attachment is a Homebrew master that the local
// DMRGateway logs into, and DMRGateway's Talker Alias path runs one way only:
// CMMDVMNetwork::readTalkerAlias → CDMRNetwork::writeTalkerAlias, repeater to
// network. At the pinned 79edbc4 the network side (CDMRNetwork::clock) has no DMRA
// case at all, so an alias sent from a bus lands in the final else and becomes
// CUtils::dump("Unknown packet from the master") — measured against that source,
// not assumed.
//
// waypointd can deliver it and does not know the name. MMDVM-Host accepts
// DMR-network datagrams from exactly one address:port (CUDPSocket::match compares
// address AND port), which is the relay in internal/dmrshim — so the relay is the
// only injection point on the box, and waypointd owns it. It also gets the timing
// right for free: the relay's taps see a datagram AFTER it has been forwarded, and
// the fork's CDMRSlot::setTalkerAlias drops any block arriving before the slot is
// in RPT_NET_STATE::AUDIO for that source.
//
// So the name crosses the process boundary and the frames are built at the seam.

// TalkerAliasNote is one bus announcement: the name to display on a transmission,
// keyed by the stream id that transmission will carry.
//
// Keyed by stream id and not by source id because the source id says nothing —
// every Zello transmission a bus sources carries the SAME id, the node's own. The
// stream id is unique per transmission and survives the hop through DMRGateway
// untouched (see talkeralias.Call.StreamID for where that was read off).
type TalkerAliasNote struct {
	// Type discriminates the payload. Checked, so a foreign message that happens to
	// land on this topic is dropped rather than transmitted as somebody's name.
	Type string `json:"type"`
	// StreamID is the DMRD stream id of the transmission being named.
	StreamID uint32 `json:"stream_id"`
	// SrcID is the DMR id the transmission is sourced from. Carried so the receiver
	// can tell an announcement for a call it expects from one it does not, and
	// because the DMRA frames must be addressed to the same id or the fork's slot
	// matching drops them.
	SrcID uint32 `json:"src_id"`
	// Name is displayed verbatim. It is a Zello display name, not a callsign: its
	// case is part of it and nothing here upper-cases it.
	Name string `json:"name"`
}

// TalkerAliasNoteType is the Type every valid note carries.
const TalkerAliasNoteType = "talker_alias"

// TranslateTalkerAlias decodes one announcement, or reports that the payload is
// not one.
//
// ok=false covers an empty payload (a retained clear, which this topic never
// publishes but the broker may replay), malformed JSON, the wrong type, and a note
// with nothing usable in it. A note is dropped rather than partially believed: a
// stream id with no name would suppress nothing and display nothing, and a name
// with no stream id could only ever be attached to the wrong transmission.
func TranslateTalkerAlias(payload []byte) (TalkerAliasNote, bool) {
	if len(strings.TrimSpace(string(payload))) == 0 {
		return TalkerAliasNote{}, false
	}
	var n TalkerAliasNote
	if err := json.Unmarshal(payload, &n); err != nil {
		return TalkerAliasNote{}, false
	}
	if n.Type != TalkerAliasNoteType {
		return TalkerAliasNote{}, false
	}
	n.Name = strings.TrimSpace(n.Name)
	if n.StreamID == 0 || n.SrcID == 0 || n.Name == "" {
		return TalkerAliasNote{}, false
	}
	return n, true
}
