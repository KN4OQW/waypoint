package config

// The static-talkgroup hint.
//
// # The symptom
//
// An operator adds a busy static talkgroup on a simplex hotspot, and their radio
// stops working. They key up and nothing happens: no repeat, nothing on the
// network, no error anywhere. The node is healthy, every daemon is running, and
// the dashboard is green.
//
// # The mechanism, which is two things and not one
//
// PHYSICAL, and simplex only. A simplex node transmits and receives on one
// frequency, so while it is playing a talkgroup's downlink it cannot hear the
// radio at all. A duplex node has no such problem: it listens on a different
// frequency from the one it transmits on.
//
// LOGICAL, and both. MMDVM-Host drops the operator's RF while a network stream is
// in progress, in two places that between them cover both directions:
//
//	DMRSlot.cpp CDMRSlot::writeQueueRF     if (m_netState != RPT_NET_STATE::IDLE) return;
//	DMRSlot.cpp CDMRSlot::writeNetworkRF   if (m_netState != RPT_NET_STATE::IDLE) return;
//
// The first stops the transmission being repeated; the second stops it reaching
// the network. CDMRSlot::isBusy is the same condition. So even in the gaps between
// bursts, a slot carrying a network stream will not carry the operator.
//
// A busy static talkgroup keeps m_netState out of IDLE more or less continuously,
// and the two effects compound.
//
// # Why this is Info and not Warning
//
// Because the second half of it is a guess. Waypoint can see that the node is
// simplex and that it has a DMR network; it CANNOT see that network's static
// talkgroup list, which lives on the network's own portal. So this fires on
// configurations where the problem is possible, not on configurations where it is
// present — and most of those nodes are working fine.
//
// A guess wearing a warning's colours teaches an operator to ignore warnings.
// SeverityInfo exists so this can be said without that cost.

// staticTalkgroupHint reports the simplex-plus-network combination that makes a
// busy static talkgroup able to lock a radio out.
//
// It says nothing on a duplex node, where the physical half does not apply and the
// logical half is bounded by however long a transmission actually lasts.
func (m *Model) staticTalkgroupHint(add func(field, severity, msg string)) {
	if m.General.Duplex || !m.hasEnabledDMRNetwork() {
		return
	}
	add("general.duplex", SeverityInfo,
		"This node is simplex, so it cannot hear your radio while it is transmitting a "+
			"talkgroup. If you subscribe to a busy static talkgroup, the channel can stay "+
			"occupied long enough that keying up appears to do nothing at all - no repeat, "+
			"no network, and no error. If that happens, thin out the static talkgroups on "+
			"your DMR network's own portal (BrandMeister calls it SelfCare); Waypoint cannot "+
			"see that list from here.")
}

// hasEnabledDMRNetwork reports whether any DMR network is switched on. XLX renders
// its own section and carries no talkgroup subscriptions, so it is not one.
func (m *Model) hasEnabledDMRNetwork() bool {
	for _, n := range m.Networks {
		if n.Enabled && n.Type != NetXLX {
			return true
		}
	}
	return false
}
