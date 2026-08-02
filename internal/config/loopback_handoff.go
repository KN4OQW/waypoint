package config

import (
	"fmt"
	"sort"
)

// loopback_handoff.go implements RFC-0003 Addendum A: coordinating an attached
// mode's loopback so a bus and the live stack coexist. The rules are pure
// functions of the whole model (Addendum §Design-0), consumed by the renderers
// (render.go) and the apply path (cmd/waypointd).
//
//   - DMR MULTIPLEXES: the bus gets a dedicated loopback port from a reserved
//     range and DMRGateway gains a [DMR Network N] dialing it — MMDVM-Host and
//     every upstream network stay live (Addendum §1).
//   - YSF/NXDN DISPLACE: an attachment makes the bus the mode's gateway on the
//     mode's existing loopback, and the stock gateway is not rendered/run while
//     the attachment exists (Addendum §2/§3).

// Reserved bus loopback range (Addendum §4): DMR bus networks bind a port here,
// disjoint from every stock consumer (Addendum Appendix A.6). Allocated
// deterministically by stable bus index so the render stays pure.
const (
	dmrBusPortBase = 62100
	dmrBusPortCeil = 62199
)

// busIndex returns the bus's stable index in the id-sorted bus list, so its
// reserved port does not move when an unrelated bus toggles. Not-found is -1.
func (m *Model) busIndex(busID string) int {
	sorted := busesSortedByID(m.Buses)
	for i, b := range sorted {
		if b.ID == busID {
			return i
		}
	}
	return -1
}

// dmrBusPort is the reserved DMR loopback port for a bus, allocated by stable
// index (Addendum §4). ok=false when the bus is unknown; it panics loudly only if
// the range is exhausted (>100 buses on one node — Addendum Open Q4), which is not
// a reachable state on real hardware but must never silently alias a port.
func (m *Model) dmrBusPort(busID string) (port int, ok bool) {
	i := m.busIndex(busID)
	if i < 0 {
		return 0, false
	}
	if dmrBusPortBase+i > dmrBusPortCeil {
		panic(fmt.Sprintf("config: reserved bus DMR port range %d-%d exhausted at bus index %d",
			dmrBusPortBase, dmrBusPortCeil, i))
	}
	return dmrBusPortBase + i, true
}

// busAttachment returns the attachment of a given mode on a bus, if present.
func (m *Model) busAttachment(busID string, mode Mode) (Attachment, bool) {
	for _, a := range m.Attachments {
		if a.BusID == busID && a.Mode == mode {
			return a, true
		}
	}
	return Attachment{}, false
}

// enabledBusesWithMode returns the enabled buses carrying a local attachment of
// the given mode, in stable id order. This is the "displacing/multiplexing" set a
// mode's render coordination keys off.
func (m *Model) enabledBusesWithMode(mode Mode) []Bus {
	var out []Bus
	for _, b := range busesSortedByID(m.Buses) {
		if !b.Enabled {
			continue
		}
		if _, ok := m.busAttachment(b.ID, mode); ok {
			out = append(out, b)
		}
	}
	return out
}

// modeDisplacesGateway reports whether an enabled bus displaces the stock gateway
// for a mode (YSF/NXDN in v1, Addendum §2/§3). DMR never displaces (it
// multiplexes), so this is false for DMR.
func (m *Model) modeDisplacesGateway(mode Mode) bool {
	switch mode {
	case ModeYSF, ModeNXDN:
		return len(m.enabledBusesWithMode(mode)) > 0
	}
	return false
}

// DisplacedGatewayUnits names the stock gateway units NOT rendered because an
// enabled bus displaces them (Addendum §2/§3). The apply path stops these before
// starting the displacing bus (Addendum §5) — they are not in the rendered target
// set, so nothing else stops them.
func (m *Model) DisplacedGatewayUnits() []string {
	var out []string
	if m.modeDisplacesGateway(ModeYSF) {
		// The YSF slot is YSFGateway or DGIdGateway depending on EnableDGId; both are
		// displaced by a YSF bus.
		out = append(out, unitYSFGateway, unitDGIdGateway)
	}
	if m.modeDisplacesGateway(ModeNXDN) {
		out = append(out, unitNXDNGateway)
	}
	return out
}

// RegisteredBusUnits names the templated units for enabled buses — the ones apply
// enables for boot (Addendum §7). A disabled bus is in DisabledBusUnits instead.
func (m *Model) RegisteredBusUnits() []string {
	var out []string
	for _, b := range busesSortedByID(m.Buses) {
		if b.Enabled {
			out = append(out, busUnit(b.ID))
		}
	}
	return out
}

// BootEnableUnits / BootDisableUnits are the boot-persistence picture apply
// installs (Addendum §7): what systemd should start on its own after a reboot.
//
// Both are DERIVED from the rendered target set, and that is the whole point.
// They used to be hand-maintained lists, and the lists were wrong in both
// directions at once: waypoint-mmdvm.service appeared in neither, so the modem
// host — which every mode sits on, and which the stack updater's health gate
// requires — died at every reboot and came back only when an operator applied
// config; meanwhile the YSF and NXDN gateway units were enabled unconditionally,
// so a node booted two daemons whose modes were switched off. The apply loop's own
// comment said "the render is the boot picture" while the code did something else.
//
// Now it is true by construction. RenderTargets already answers "what should be
// running here" — mode enabled, not displaced, requirements met — so the boot set
// is exactly that set, and the disable set is exactly its complement over the units
// this daemon manages. A mode switched off, a gateway displaced by a bus, a POCSAG
// enable with no AuthKey: each drops the target, and boot-disable follows with no
// second list to keep in step. A mode added in future is covered the day it gets a
// render target.
func (m *Model) BootEnableUnits(paths Paths) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range m.RenderTargets(paths) {
		if t.Unit == "" || seen[t.Unit] {
			continue
		}
		seen[t.Unit] = true
		out = append(out, t.Unit)
	}
	return out
}

// BootDisableUnits is the complement: every unit this daemon manages that the
// render did NOT ask for. Deriving it (rather than listing displaced and blocked
// units by hand) is what makes "switched off" and "not started at boot" the same
// statement — including for a unit left enabled by an older version of waypointd,
// which is reconciled on the next apply rather than persisting forever.
//
// Bus instances are handled separately from the static set: a disabled bus's unit
// is enumerable from the model (DisabledBusUnits), while a DELETED bus's is not,
// and the apply path finds that one through systemd (orphanedBusUnits).
func (m *Model) BootDisableUnits(paths Paths) []string {
	enabled := map[string]bool{}
	for _, u := range m.BootEnableUnits(paths) {
		enabled[u] = true
	}
	var out []string
	for _, u := range managedUnits {
		if !enabled[u] {
			out = append(out, u)
		}
	}
	for _, u := range m.DisabledBusUnits() {
		if !enabled[u] {
			out = append(out, u)
		}
	}
	return out
}

// managedUnits is every static (non-templated) unit waypointd owns the boot state
// of. It is the universe BootDisableUnits takes its complement over, so a unit
// missing here would silently keep whatever boot state it happened to have —
// which is the failure this whole mechanism exists to prevent. Bus units are
// templated and enumerated per-instance instead.
var managedUnits = []string{
	unitMMDVM,
	unitDMRGateway,
	unitYSFGateway,
	unitDGIdGateway,
	unitP25Gateway,
	unitNXDNGateway,
	unitDStarGateway,
	unitM17Gateway,
	unitDAPNETGateway,
}

// ManagedUnits returns every static unit whose boot state waypointd reconciles
// (copied). Exported for the apply path's reporting and for tests that assert the
// complement is total.
func ManagedUnits() []string { return append([]string(nil), managedUnits...) }

// busLoopbacksFor is the per-mode loopback OVERRIDE carried into a bus's rendered
// config: only the entries that differ from the fixed per-mode default. Today that
// is exactly a DMR attachment's reserved multiplex port (§1); a displacing YSF/NXDN
// attachment reuses the stock loopback, so the daemon falls back to loopbackFor and
// nothing is carried. Keeping the override minimal means a purely-displacing bus's
// config is unchanged from Phase 2.
func (m *Model) busLoopbacksFor(busID string) map[string]BusLoopback {
	out := map[string]BusLoopback{}
	if _, ok := m.busAttachment(busID, ModeDMR); ok {
		if port, ok := m.dmrBusPort(busID); ok {
			// Dedicated reserved port; DMRGateway connects here (§1). No fixed peer
			// port in the multiplex model — the master replies to the dialer — so
			// bind==peer keeps the daemon's pair shape without claiming a stock port.
			out[string(ModeDMR)] = BusLoopback{Bind: port, Peer: port}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// busEffectiveLoopback is the loopback a bus's local attachment ACTUALLY binds:
// the coordinated override when present (DMR's reserved port), else the fixed
// per-mode loopback (YSF/NXDN displace, reusing the stock port because the gateway
// is not running). This is the daemon's loopbackFrom, at the model layer — used by
// the port-collision property (Addendum §8.3).
func (m *Model) busEffectiveLoopback(busID string, mode Mode) (BusLoopback, bool) {
	if ov := m.busLoopbacksFor(busID); ov != nil {
		if lb, ok := ov[string(mode)]; ok {
			return lb, true
		}
	}
	return busLoopbackFor(mode)
}

// dmrBusNetworks returns the synthetic DMRGateway networks for enabled DMR buses,
// in stable id order — one [DMR Network N] per bus, dialing the bus's reserved
// port, routed by the attachment's TG params (Addendum §1). RenderDMRGateway
// appends these after the operator's Networks[].
func (m *Model) dmrBusNetworks(dmrID string) []dmrBusNetwork {
	var out []dmrBusNetwork
	for _, b := range m.enabledBusesWithMode(ModeDMR) {
		port, ok := m.dmrBusPort(b.ID)
		if !ok {
			continue
		}
		a, _ := m.busAttachment(b.ID, ModeDMR)
		out = append(out, dmrBusNetwork{
			name:      "Bus_" + b.ID,
			port:      port,
			id:        dmrID,
			slot:      def(a.Slot, "2"),
			defaultTG: a.DefaultTG,
			tgMap:     a.TGMap,
		})
	}
	return out
}

// dmrBusNetwork is one rendered [DMR Network N] for a bus.
type dmrBusNetwork struct {
	name      string
	port      int
	id        string
	slot      string
	defaultTG string
	tgMap     map[string]string
}

// rewrites derives the DMRGateway rewrite lines that route this bus's talkgroups
// to its network — the same TGRewrite machinery an upstream network uses
// (Addendum §1). A DefaultTG routes that TG on the attachment's slot; each TGMap
// entry routes its DMR TG. With no TG params the bus passes all talkgroups on its
// slot (a bus with no TG filter takes everything on that slot).
func (n dmrBusNetwork) rewrites() []string {
	var lines []string
	i := 0
	if n.defaultTG != "" {
		lines = append(lines, kv(fmt.Sprintf("TGRewrite%d", i),
			fmt.Sprintf("%s,%s,%s,%s,1", n.slot, n.defaultTG, n.slot, n.defaultTG)))
		i++
	}
	// TGMap values are the DMR-side TGs the bus wants; route each on its slot.
	for _, tg := range sortedTGMapValues(n.tgMap) {
		lines = append(lines, kv(fmt.Sprintf("TGRewrite%d", i),
			fmt.Sprintf("%s,%s,%s,%s,1", n.slot, tg, n.slot, tg)))
		i++
	}
	if i == 0 {
		lines = append(lines, kv("PassAllTG"+n.slot, n.slot))
	}
	return lines
}

// sortedTGMapValues returns the distinct DMR TGs a TGMap targets, sorted, so the
// render is deterministic.
func sortedTGMapValues(tgMap map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range tgMap {
		if v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
