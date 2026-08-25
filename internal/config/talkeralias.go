package config

import (
	"strconv"
	"strings"
)

// talkeralias.go answers the two questions the Talker Alias injector asks of the
// store: which source ids may only ever be named by an announcement, and whether
// this node is wired up to deliver one at all.

// TalkerAliasAnnouncedSourceIDs returns the DMR source ids whose alias must come
// from an announcement and never from the phonebook.
//
// There is at most one, and it is this node's own ID. When a bus puts Zello audio
// on the air it sources every transmission from that ID (see busConfigFor: a Zello
// user without a DMR registration has no ID to borrow, and one who has an ID has
// not authorised this node to transmit as them). The phonebook would therefore
// answer with the NODE's callsign — putting a different operator's name on the
// receiving radio, which is worse than the bare number it replaced. So the id is
// declared here and internal/talkeralias suppresses the phonebook for it: a
// transmission with no announcement gets no alias.
//
// Empty on a node with no Zello channel bridged onto a DMR bus, which is what
// keeps that node's behaviour exactly what it was before announcements existed.
func (m *Model) TalkerAliasAnnouncedSourceIDs() []uint32 {
	if m == nil || !m.hasZelloOnDMRBus() {
		return nil
	}
	// General.ID, not DMR.ID: this has to match the source id the BUS stamps on
	// the audio, and busConfigFor renders that from General.ID. A mismatch here
	// would not fail loudly — it would silently name the wrong operator.
	n, err := strconv.ParseUint(strings.TrimSpace(m.General.ID), 10, 32)
	if err != nil || n == 0 {
		return nil
	}
	return []uint32{uint32(n)}
}

// hasZelloOnDMRBus reports whether any enabled bus both carries DMR and has a
// Zello channel that would actually be rendered. Both halves matter: a Zello
// channel on a bus with no DMR attachment never reaches a radio, and a DMR bus
// with no Zello channel sources nothing on anyone's behalf.
func (m *Model) hasZelloOnDMRBus() bool {
	for _, b := range m.enabledBusesWithMode(ModeDMR) {
		if len(m.busZelloFor(b.ID)) > 0 {
			return true
		}
	}
	return false
}

// zelloTalkerAliasProblems reports the one configuration where the operator has
// asked to name Zello callers and the node cannot do it.
//
// The alias cannot be transmitted by the bus daemon. Its DMR attachment is a
// Homebrew master that the local DMRGateway logs into, and DMRGateway forwards
// Talker Alias only repeater→network — at the pinned 79edbc4, CDMRNetwork::clock
// has no DMRA case, so an alias sent that way is dumped as an unknown packet. The
// only address MMDVM-Host accepts DMR-network datagrams from is its configured
// GatewayAddress:GatewayPort (CUDPSocket::match compares address AND port), which
// is the DMR relay. No relay, no injection point.
//
// A warning and not a refusal: the node works, the Zello audio still reaches the
// air, and the operator loses a name on a screen. Refusing the save would block
// somebody from switching the relay on in a second edit.
func (m *Model) zelloTalkerAliasProblems(add func(field, severity, msg string)) {
	if strings.TrimSpace(m.DMR.TalkerAlias) == "" || !m.hasZelloOnDMRBus() {
		return
	}
	if m.DMRShimEnabled() {
		return
	}
	add("dmrnet.shim_enabled", SeverityWarning,
		"Zello callers cannot be named on the radio while the DMR message relay is off. "+
			"The relay is the only place an alias can be added to the DMR loopback, so switch it on "+
			"under DMR to show who is talking; without it a Zello transmission shows this node's DMR ID.")
}
