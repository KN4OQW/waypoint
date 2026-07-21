package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Paths locates where each daemon reads its generated INI. The server wires
// these from flags and hands them to RenderTargets. A new mode adds a field
// here and one entry in RenderTargets — the apply path never changes (issue
// #21 gateway-plugin seam).
type Paths struct {
	MMDVM         string
	DMRGateway    string
	YSFGateway    string
	DGIdGateway   string // alternative YSF gateway; rendered here only when YSFGW.EnableDGId
	P25Gateway    string
	NXDNGateway   string
	DStarGateway  string
	M17Gateway    string
	DAPNETGateway string // POCSAG paging gateway (rendered only when POCSAG mode is enabled)
	// BusConfigDir is where each enabled mode bus's rendered config is written
	// (RFC-0003): <BusConfigDir>/waypoint-bus-<id>.json, consumed by
	// waypoint-bus@<id>.service. Unlike the fixed gateway INIs there is one file
	// per enabled bus, so this is a directory, not a path. Empty in tests that only
	// exercise the renderers.
	BusConfigDir string
	// PeeringDir is where the peering cert/key files live (RFC-0016): the node's own
	// key (node.key) and each pinned peer/owner cert (0600, waypointd-owned). The
	// rendered configs reference files under here by PATH — never PEM content
	// (RFC-0002 posture). The pairing/transport layer populates the files.
	PeeringDir string
	// OverridesDir is the root of the operator's override drop-ins (RFC-0005 /
	// issue #2): per-daemon fragments live under <OverridesDir>/<daemon>.d/*.conf and
	// merge last into each rendered INI. Empty disables the override layer entirely
	// (the render is emitted verbatim) — the default in tests and demo mode.
	OverridesDir string
	// The cross-mode bridge INIs (MMDVM_CM: YSF2DMR/DMR2YSF/YSF2NXDN/DMR2NXDN/
	// NXDN2DMR) once had Paths fields here. The per-bridge-daemon model is retired in
	// favour of the RFC-0003 bus architecture: RenderTargets no longer emits a bridge
	// INI, so there is nothing to locate. The bridge store sections are retained
	// (dormant) for RFC-0003's migration — see model.go sections() and RetiredBridgeUnits.
}

// systemd units restarted when a target's file changes. Each render target
// owns its unit name, so adding a mode does not touch the apply code.
const (
	unitMMDVM         = "waypoint-mmdvm.service"
	unitDMRGateway    = "waypoint-dmrgateway.service"
	unitYSFGateway    = "waypoint-ysfgateway.service"
	unitDGIdGateway   = "waypoint-dgidgateway.service" // mutually exclusive with YSFGateway (systemd Conflicts=)
	unitP25Gateway    = "waypoint-p25gateway.service"
	unitNXDNGateway   = "waypoint-nxdngateway.service"
	unitDStarGateway  = "waypoint-dstargateway.service"
	unitM17Gateway    = "waypoint-m17gateway.service"
	unitDAPNETGateway = "waypoint-dapnetgateway.service"

	// Cross-mode transcoding bridge units (MMDVM_CM). The per-bridge-daemon model is
	// retired for the RFC-0003 bus architecture, so RenderTargets no longer restarts
	// them. They remain named here only so apply can STOP any that a node was still
	// running under the old surface — see RetiredBridgeUnits.
	unitYSF2DMR  = "waypoint-ysf2dmr.service"
	unitDMR2YSF  = "waypoint-dmr2ysf.service"
	unitYSF2NXDN = "waypoint-ysf2nxdn.service"
	unitDMR2NXDN = "waypoint-dmr2nxdn.service"
	unitNXDN2DMR = "waypoint-nxdn2dmr.service"
)

// Per-daemon override namespaces (RFC-0005). Each render target owns a daemon key
// naming its override drop-in directory (<OverridesDir>/<key>.d/). The key is
// short and stable — it appears in operators' directory layouts and in the
// Overrides UI — so it must not change casually.
const (
	daemonMMDVM         = "mmdvm"
	daemonDMRGateway    = "dmrgateway"
	daemonYSFGateway    = "ysfgateway"
	daemonDGIdGateway   = "dgidgateway"
	daemonP25Gateway    = "p25gateway"
	daemonNXDNGateway   = "nxdngateway"
	daemonDStarGateway  = "dstargateway"
	daemonM17Gateway    = "m17gateway"
	daemonDAPNETGateway = "dapnetgateway"
)

// retiredBridgeUnits are the cross-mode transcoding bridge daemons (MMDVM_CM)
// retired in favour of the RFC-0003 bus architecture. RenderTargets no longer
// contributes a target for them, so apply never restarts them; instead apply stops
// any that are still active, which closes the stale-daemon-on-disable defect by
// construction (a bridge disabled under the old surface no longer lingers).
var retiredBridgeUnits = []string{unitYSF2DMR, unitDMR2YSF, unitYSF2NXDN, unitDMR2NXDN, unitNXDN2DMR}

// RetiredBridgeUnits returns the systemd units apply must stop if they are still
// running (see retiredBridgeUnits). The slice is copied so callers cannot mutate
// the registry.
func RetiredBridgeUnits() []string { return append([]string(nil), retiredBridgeUnits...) }

// RenderTarget ties one generated INI to the daemon unit that consumes it and
// the pure function that produces it. A mode contributes its own target rather
// than editing the apply loop — this is issue #21's gateway-plugin seam.
type RenderTarget struct {
	Path   string              // where the daemon reads its INI
	Unit   string              // systemd unit to restart when this file changes
	Daemon string              // override namespace: <OverridesDir>/<Daemon>.d/*.conf (RFC-0005)
	Render func(*Model) string // pure renderer for this file
}

// RenderTargets is the ordered registry of every generated file. MMDVM-Host and
// DMRGateway lead; each later mode appends its own entry. The order fixes both
// the write order and the restart order, so it must not change casually.
//
// The System Fusion slot is the one conditional target: YSFGateway and
// DGIdGateway share MMDVM-Host's 3200/4200 loopback and cannot run at once, so
// EnableDGId swaps the whole target — file, unit, and renderer — rather than
// adding a second one. The apply loop then restarts exactly one YSF unit; the
// deploy's systemd Conflicts= between the two units stops the other daemon.
func (m *Model) RenderTargets(paths Paths) []RenderTarget {
	ysf := RenderTarget{Path: paths.YSFGateway, Unit: unitYSFGateway, Daemon: daemonYSFGateway, Render: (*Model).RenderYSFGateway}
	if m.YSFGW.EnableDGId {
		ysf = RenderTarget{Path: paths.DGIdGateway, Unit: unitDGIdGateway, Daemon: daemonDGIdGateway, Render: (*Model).RenderDGIdGateway}
	}
	targets := []RenderTarget{
		{Path: paths.MMDVM, Unit: unitMMDVM, Daemon: daemonMMDVM, Render: (*Model).RenderMMDVM},
		{Path: paths.DMRGateway, Unit: unitDMRGateway, Daemon: daemonDMRGateway, Render: (*Model).RenderDMRGateway},
	}
	// RFC-0003 Addendum A §2/§3: a YSF (resp. NXDN) bus attachment DISPLACES the stock
	// gateway — the bus becomes the mode's gateway on the same loopback, so the stock
	// gateway target is not rendered while the attachment exists (mirroring how
	// EnableDGId already swaps the YSF unit). Its running unit is stopped by the apply
	// path (DisplacedGatewayUnits) before the bus starts. DMR never displaces (§1).
	if !m.modeDisplacesGateway(ModeYSF) {
		targets = append(targets, ysf)
	}
	targets = append(targets,
		RenderTarget{Path: paths.P25Gateway, Unit: unitP25Gateway, Daemon: daemonP25Gateway, Render: (*Model).RenderP25Gateway},
	)
	if !m.modeDisplacesGateway(ModeNXDN) {
		targets = append(targets,
			RenderTarget{Path: paths.NXDNGateway, Unit: unitNXDNGateway, Daemon: daemonNXDNGateway, Render: (*Model).RenderNXDNGateway})
	}
	targets = append(targets,
		RenderTarget{Path: paths.DStarGateway, Unit: unitDStarGateway, Daemon: daemonDStarGateway, Render: (*Model).RenderDStarGateway},
		RenderTarget{Path: paths.M17Gateway, Unit: unitM17Gateway, Daemon: daemonM17Gateway, Render: (*Model).RenderM17Gateway},
	)
	// POCSAG's DAPNETGateway is gated on the POCSAG mode enable, NOT always-on.
	// Unlike the digital-mode gateways (YSF/P25/NXDN/M17/D-Star) — which idle
	// harmlessly when their mode is off — DAPNETGateway exits immediately with
	// "AuthKey not set or invalid" when there is no DAPNET credential. Rendering it
	// unconditionally therefore made it crash-loop on every node that had not
	// configured POCSAG, and (because apply does a blocking `systemctl restart`)
	// stretched every Apply to ~45s. Gate it like a bridge: POCSAG off ⇒ no target
	// ⇒ apply neither writes DAPNETGateway.ini nor restarts the unit.
	if m.Modes.POCSAG {
		targets = append(targets, RenderTarget{Path: paths.DAPNETGateway, Unit: unitDAPNETGateway, Daemon: daemonDAPNETGateway, Render: (*Model).RenderDAPNETGateway})
	}
	// The cross-mode transcoding bridges (MMDVM_CM) used to append here when enabled.
	// The per-bridge-daemon model is retired for the RFC-0003 bus architecture, so no
	// bridge ever contributes a target: apply writes no bridge INI and restarts no
	// bridge unit, regardless of the (dormant) bridge sections' Enable flags. Apply
	// instead STOPS any bridge daemon still running from the old surface
	// (RetiredBridgeUnits), which closes the stale-daemon-on-disable defect.

	// Mode buses (RFC-0003 §4): one target per ENABLED bus — its rendered config
	// plus the templated unit that consumes it. A disabled bus contributes none
	// (its config stops being written); its running daemon is stopped by the apply
	// loop (see RetiredBusUnits). Buses are appended in stable id order so the
	// render/restart order is deterministic (RFC-0001 pure-render property).
	peeringDir := paths.PeeringDir
	for _, b := range busesSortedByID(m.Buses) {
		if !b.Enabled {
			continue
		}
		id := b.ID
		targets = append(targets, RenderTarget{
			Path:   filepath.Join(paths.BusConfigDir, busConfigFile(id)),
			Unit:   busUnit(id),
			Render: func(mm *Model) string { return mm.renderBusConfig(id, peeringDir) },
		})
		// RFC-0016 member side: one target per (bus, paired peer) membership. Its Unit
		// is empty — the member config runs on the PEER node, not here; the owner
		// renders it as a provisioning artifact (a later transport PR delivers it), so
		// the owner's apply writes the file but restarts nothing (restartSet skips
		// empty units). A revoked/pending peer contributes no membership, so it renders
		// no target (and deletes nothing from the store).
		for _, peerID := range m.busMembersOf(id) {
			pid := peerID
			targets = append(targets, RenderTarget{
				Path:   filepath.Join(paths.BusConfigDir, memberConfigFile(id, pid)),
				Render: func(mm *Model) string { return mm.renderMemberConfig(id, pid, peeringDir) },
			})
		}
	}
	return targets
}

// busUnit / busConfigFile name the templated systemd unit and rendered config
// file for a bus id. memberConfigFile names the member-side provisioning config
// for a (bus, peer) membership (RFC-0016).
func busUnit(id string) string       { return "waypoint-bus@" + id + ".service" }
func busConfigFile(id string) string { return "waypoint-bus-" + id + ".json" }
func memberConfigFile(busID, peerID string) string {
	return "waypoint-bus-" + busID + "-member-" + peerID + ".json"
}

// DisabledBusUnits names the templated units for buses that exist but are
// disabled. Apply stops these (like RetiredBridgeUnits) so disabling a bus in the
// UI actually stops its daemon — enabled buses contribute a render target and get
// (re)started, disabled ones would otherwise linger. A deleted bus cannot be
// enumerated (its row is gone), the same limitation every removed target has.
func (m *Model) DisabledBusUnits() []string {
	var out []string
	for _, b := range busesSortedByID(m.Buses) {
		if !b.Enabled {
			out = append(out, busUnit(b.ID))
		}
	}
	return out
}

// busesSortedByID returns the buses in stable id order without mutating the model.
func busesSortedByID(buses []Bus) []Bus {
	out := append([]Bus(nil), buses...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// renderBusConfig is the pure renderer for one bus's config file: the BusConfig
// the daemon reads (RFC-0003 §4), assembled from the bus row and its attachments.
// A missing bus renders an empty object rather than panicking (the target list is
// built from the same model, so this is defence in depth).
func (m *Model) renderBusConfig(id, peeringDir string) string {
	var bus Bus
	found := false
	for _, b := range m.Buses {
		if b.ID == id {
			bus, found = b, true
			break
		}
	}
	if !found {
		return "{}\n"
	}
	bc := BusConfig{Bus: bus}
	for _, a := range m.Attachments {
		if a.BusID == id {
			bc.Attachments = append(bc.Attachments, a)
		}
	}
	// RFC-0003 Addendum A: carry the coordinated loopbacks so the daemon binds the
	// hand-off ports (a DMR attachment's reserved multiplex port), never a stock
	// port the live stack owns.
	bc.Loopbacks = m.busLoopbacksFor(id)
	// RFC-0016 owner side: add the peering block + one member row per ACTIVE remote
	// attachment. A bus with no active remote attachment gets no Peering block, so
	// it renders byte-identically to Phase 1 (dormant/revoked peers render nothing).
	if active := m.activeRemoteAttachmentsForBus(id); len(active) > 0 {
		bp := &BusPeering{
			Listen:         m.Peering.listen(),
			KeyPath:        nodeKeyPath(peeringDir),
			DeadlineMs:     m.Peering.deadline(),
			JitterBufferMs: m.Peering.jitter(),
		}
		for _, ra := range active {
			p, _ := m.peerByID(ra.PeerID) // guaranteed present: activeRemote filters to paired peers
			bp.Members = append(bp.Members, BusPeerMember{
				PeerID:       ra.PeerID,
				Name:         p.Name,
				Endpoint:     resolvedEndpoint(p),
				MDNSInstance: p.MDNSInstance,
				Fingerprint:  p.Fingerprint,
				CertPath:     peerCertPath(peeringDir, ra.PeerID),
				Mode:         ra.Mode,
				Slot:         ra.Slot, DefaultTG: ra.DefaultTG, TGMap: ra.TGMap,
				Target: ra.Target, WiresXPassthrough: ra.WiresXPassthrough,
				ID: ra.ID, TG: ra.TG, DefaultID: ra.DefaultID,
			})
		}
		bc.Peering = bp
	}
	raw, err := bc.Marshal()
	if err != nil {
		return "{}\n"
	}
	return string(raw) + "\n"
}

// renderMemberConfig is the RFC-0016 member-side config for one membership — a
// peer (member) contributing its mode(s) to this owner's bus. It renders NOTHING
// (empty object) when the peer is not paired, so a revoked peer's membership
// disappears from the render without its rows being deleted from the store.
func (m *Model) renderMemberConfig(busID, peerID, peeringDir string) string {
	var bus Bus
	for _, b := range m.Buses {
		if b.ID == busID {
			bus = b
			break
		}
	}
	if bus.ID == "" {
		return "{}\n"
	}
	mc := MemberBusConfig{
		Role:  "member",
		BusID: busID,
		Owner: MemberOwner{
			Listen:   m.Peering.listen(),
			CertPath: ownerCertPath(peeringDir, busID),
		},
		KeyPath:         nodeKeyPath(peeringDir),
		DeadlineMs:      m.Peering.deadline(),
		JitterBufferMs:  m.Peering.jitter(),
		HangTimeSeconds: BusConfig{Bus: bus}.HangTimeSeconds,
	}
	for _, ra := range m.activeRemoteAttachmentsForBus(busID) {
		if ra.PeerID != peerID {
			continue
		}
		lb, ok := busLoopbackFor(ra.Mode)
		if !ok {
			continue
		}
		mc.Attachments = append(mc.Attachments, MemberAttachment{
			Mode: ra.Mode, Loopback: lb,
			Slot: ra.Slot, DefaultTG: ra.DefaultTG, TGMap: ra.TGMap,
			Target: ra.Target, WiresXPassthrough: ra.WiresXPassthrough,
			ID: ra.ID, TG: ra.TG, DefaultID: ra.DefaultID,
		})
	}
	if len(mc.Attachments) == 0 {
		return "{}\n" // peer not paired / no modes: renders nothing
	}
	return jsonBlock(mc)
}

// busMembersOf returns the distinct paired peer ids contributing to a bus, in
// stable order — one member target is rendered per (bus, peer) membership.
func (m *Model) busMembersOf(busID string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ra := range m.activeRemoteAttachmentsForBus(busID) {
		if !seen[ra.PeerID] {
			seen[ra.PeerID] = true
			out = append(out, ra.PeerID)
		}
	}
	return out
}

// WriteFiles renders every target, merges the operator's override fragments last
// (RFC-0005), and writes the result atomically (write to a temp file in the same
// directory, then rename). A crash mid-apply therefore never leaves a daemon
// reading a half-written config. Override warnings (malformed fragment lines) are
// returned flattened so the caller can surface them — the override layer never
// drops a line silently.
func (m *Model) WriteFiles(paths Paths) (warnings []string, err error) {
	for _, t := range m.RenderTargets(paths) {
		content, _, w, err := m.renderMerged(t, paths.OverridesDir)
		if err != nil {
			return warnings, err
		}
		warnings = append(warnings, w...)
		if err := writeAtomic(t.Path, content); err != nil {
			return warnings, err
		}
	}
	return warnings, nil
}

// renderMerged renders one target and applies its override fragments. With no
// OverridesDir (tests, demo) or no Daemon key the render is returned verbatim, so
// the override layer is provably inert until an operator drops in a fragment.
func (m *Model) renderMerged(t RenderTarget, overridesDir string) (content string, applied []Applied, warnings []string, err error) {
	content = t.Render(m)
	if overridesDir == "" || t.Daemon == "" {
		return content, nil, nil, nil
	}
	frags, warnings, err := LoadFragments(filepath.Join(overridesDir, t.Daemon+".d"))
	if err != nil {
		return content, nil, warnings, err
	}
	if len(frags) == 0 {
		return content, nil, warnings, nil
	}
	merged, applied := ApplyOverrides(t.Daemon, content, frags)
	return merged, applied, warnings, nil
}

// Overrides reports, per daemon, the override records that shape the current
// store's render — what the next Apply will actually write (RFC-0005). It renders
// but writes nothing, so it is the read model for GET /api/overrides and the
// Overrides UI panel. Records are returned in render-target order (daemon), then
// section/key within each daemon.
func (m *Model) Overrides(paths Paths) (applied []Applied, warnings []string, err error) {
	for _, t := range m.RenderTargets(paths) {
		_, a, w, err := m.renderMerged(t, paths.OverridesDir)
		if err != nil {
			return applied, warnings, err
		}
		applied = append(applied, a...)
		warnings = append(warnings, w...)
	}
	return applied, warnings, nil
}

func writeAtomic(path, content string) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".waypoint-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	// Leave the 0600 CreateTemp mode: the rendered DMRGateway.ini carries the
	// upstream network password, so the generated files stay root-only.
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// generatedHeader tops every rendered file: these are compiled outputs of the
// store, and hand edits are lost on the next apply (the override layer is the
// escape hatch — RFC-0001).
const generatedHeader = `; Generated by waypointd from the configuration store — do NOT edit.
; Edits are overwritten on the next Apply. Use the override layer instead.
`

// RenderMMDVM renders a complete MMDVM-Host.ini from the model. It is a pure
// function: the same model always yields byte-identical output. Managed keys
// come from the model; fixed operational keys come from constants here.
func (m *Model) RenderMMDVM() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Id", m.General.ID),
		kv("Timeout", def(m.General.Timeout, "240")),
		kb("Duplex", m.General.Duplex),
		kv("RFModeHang", def(m.General.RFModeHang, "300")),
		kv("NetModeHang", def(m.General.NetModeHang, "300")),
		kv("Display", def(m.Display.Type, "None")),
		kv("Daemon", "0"),
	)
	sect(&b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Location", m.General.Location),
		kv("URL", m.General.URL),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Host", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Auth", "0"),
		kv("Name", "mmdvm"),
		kv("Keepalive", "60"),
	)
	sect(&b, "DMR Id Lookup",
		kv("File", "/usr/local/etc/DMRIds.dat"),
		kv("Time", "24"),
	)
	sect(&b, "Modem",
		kv("Protocol", "uart"),
		kv("UARTPort", def(m.Modem.Port, "/dev/ttyAMA0")),
		kv("UARTSpeed", def(m.Modem.UARTSpeed, "115200")),
		kb("TXInvert", m.Modem.TXInvert),
		kb("RXInvert", m.Modem.RXInvert),
		kb("PTTInvert", m.Modem.PTTInvert),
		kv("RXOffset", def(m.Modem.RXOffset, "0")),
		kv("TXOffset", def(m.Modem.TXOffset, "0")),
		kv("RXLevel", def(m.Modem.RXLevel, "50")),
		kv("TXLevel", def(m.Modem.TXLevel, "50")),
		kv("RSSIMappingFile", "/usr/local/etc/RSSI.dat"),
	)

	// [D-Star] Module is the band letter appended to the D-Star callsign; it must
	// match the gateway repeater Band. Ack/error replies use MMDVM-Host's own
	// defaults (AckReply=1, AckMessage=0/BER, ErrorReply=1 — Conf.cpp:165-168).
	sect(&b, "D-Star",
		kb("Enable", m.Modes.DStar),
		kv("Module", def(m.DStar.Module, "B")),
		kb("SelfOnly", m.DStar.SelfOnly),
		kv("AckReply", "1"),
		kv("AckMessage", "0"),
		kv("ErrorReply", "1"),
		kb("RemoteGateway", m.DStar.RemoteGateway),
	)
	sect(&b, "DMR",
		kb("Enable", m.Modes.DMR),
		kv("ColorCode", def(m.DMR.ColorCode, "1")),
		kv("Id", firstNonEmpty(m.DMR.ID, m.General.ID)),
		kb("SelfOnly", m.DMR.SelfOnly),
		kb("EmbeddedLCOnly", m.DMR.EmbeddedLCOnly),
		kb("DumpTAData", m.DMR.DumpTAData),
		kb("Beacons", m.DMR.Beacons),
	)
	sect(&b, "System Fusion",
		kb("Enable", m.Modes.YSF),
		kb("LowDeviation", m.YSF.LowDeviation),
		kb("SelfOnly", m.YSF.SelfOnly),
		kv("TXHang", def(m.YSF.TXHang, "4")),
		kb("RemoteGateway", m.YSF.RemoteGateway),
		kv("ModeHang", def(m.YSF.ModeHang, "20")),
	)
	sect(&b, "P25",
		kb("Enable", m.Modes.P25),
		kv("NAC", def(m.P25.NAC, "293")),
		kb("SelfOnly", m.P25.SelfOnly),
		kb("OverrideUIDCheck", m.P25.OverrideUIDCheck),
		kb("RemoteGateway", m.P25.RemoteGateway),
		kv("TXHang", def(m.P25.TXHang, "5")),
	)
	sect(&b, "NXDN",
		kb("Enable", m.Modes.NXDN),
		kv("RAN", def(m.NXDN.RAN, "1")),
		kb("SelfOnly", m.NXDN.SelfOnly),
		kb("RemoteGateway", m.NXDN.RemoteGateway),
		kv("TXHang", def(m.NXDN.TXHang, "5")),
	)
	// M17 uses a decimal CAN (Channel Access Number, like DMR's color code), has
	// no RemoteGateway key, and adds AllowEncryption (pass encrypted M17 frames).
	sect(&b, "M17",
		kb("Enable", m.Modes.M17),
		kv("CAN", def(m.M17.CAN, "0")),
		kb("SelfOnly", m.M17.SelfOnly),
		kb("AllowEncryption", m.M17.AllowEncryption),
		kv("TXHang", def(m.M17.TXHang, "5")),
	)
	// POCSAG is the paging channel: Enable + the transmit Frequency. The rest of
	// the paging config (DAPNET login/filters) lives in DAPNETGateway.ini, which
	// MMDVM-Host reaches over the [POCSAG Network] loopback below.
	sect(&b, "POCSAG",
		kb("Enable", m.Modes.POCSAG),
		kv("Frequency", def(m.POCSAG.Frequency, "439987500")),
	)
	// FM (analog) has no gateway daemon — this [FM] section is the whole surface.
	// The operator-facing keys come from the model; MMDVM-Host's own defaults cover
	// the many fixed calibration keys not modeled here. AccessMode: 0 carrier w/COS,
	// 1 CTCSS-only no COS, 2 CTCSS-only w/COS, 3 CTCSS-start then carrier w/COS.
	sect(&b, "FM",
		kb("Enable", m.Modes.FM),
		kv("CTCSSFrequency", def(m.FM.CTCSS, "88.4")),
		kv("Timeout", def(m.FM.Timeout, "180")),
		kv("KerchunkTime", def(m.FM.KerchunkTime, "0")),
		kv("AccessMode", def(m.FM.AccessMode, "1")),
		kv("RFAudioBoost", def(m.FM.RFAudioBoost, "1")),
		kv("ExtAudioBoost", def(m.FM.ExtAudioBoost, "1")),
	)

	sect(&b, "DMR Network",
		kb("Enable", m.Modes.DMR),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", def(m.DMRNet.LocalPort, "62032")),
		kv("GatewayAddress", def(m.DMRNet.GatewayAddress, "127.0.0.1")),
		kv("GatewayPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("Jitter", def(m.DMRNet.Jitter, "360")),
		kb("Slot1", m.DMRNet.Slot1),
		kb("Slot2", m.DMRNet.Slot2),
	)
	// The D-Star network talks to DStarGateway on the fixed 20010/20011 pair
	// (MMDVM-Host [D-Star Network] already uses the modern GatewayAddress/
	// GatewayPort/LocalPort names — no Address→GatewayAddress rename here).
	sect(&b, "D-Star Network",
		kb("Enable", m.Modes.DStar),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", dstarMMDVMGatewayPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", dstarMMDVMLocalPort),
		kv("Debug", "0"),
	)
	// The System Fusion network talks to YSFGateway on the fixed 3200/4200 pair.
	sect(&b, "System Fusion Network",
		kb("Enable", m.Modes.YSF),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysfMMDVMLocalPort),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", ysfMMDVMGatewayPort),
		kv("ModeHang", def(m.YSF.ModeHang, "20")),
	)
	// The P25 network talks to P25Gateway on the fixed 32010/42020 pair.
	sect(&b, "P25 Network",
		kb("Enable", m.Modes.P25),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", p25MMDVMLocalPort),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", p25MMDVMGatewayPort),
		kv("Debug", "0"),
	)
	// The NXDN network talks to NXDNGateway on the fixed 14021/14020 pair.
	// Protocol=Icom is the MMDVM transport (NXDNGateway's RptProtocol matches).
	sect(&b, "NXDN Network",
		kb("Enable", m.Modes.NXDN),
		kv("Protocol", "Icom"),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", nxdnMMDVMLocalPort),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", nxdnMMDVMGatewayPort),
		kv("Debug", "0"),
	)
	// The M17 network talks to M17Gateway on the fixed 17011/17010 pair. Unlike
	// NXDN there is no Protocol key (M17Gateway speaks the MMDVM M17 transport
	// directly).
	sect(&b, "M17 Network",
		kb("Enable", m.Modes.M17),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", m17MMDVMLocalPort),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", m17MMDVMGatewayPort),
		kv("Debug", "0"),
	)
	// The POCSAG network talks to DAPNETGateway on the fixed 3800/4800 pair. Enable
	// tracks the POCSAG mode: with it off the daemon still runs (always-on target)
	// but MMDVM-Host neither listens for nor forwards paging traffic.
	sect(&b, "POCSAG Network",
		kb("Enable", m.Modes.POCSAG),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", pocsagMMDVMLocalPort),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", pocsagMMDVMGatewayPort),
		kv("Debug", "0"),
	)

	renderDisplaySections(&b, m)
	return b.String()
}

// renderDisplaySections emits MMDVM-Host's [Display] driver subsections. All
// five are always written (like the stock MMDVM-Host.ini), regardless of which
// one [General] Display selects — the daemon reads only the selected section, and
// carrying them all keeps the file a faithful WPSD clone (and makes every Display
// field round-trip). The operator-set keys come from the model; the fixed
// operational keys are constants transcribed from the pre-MQTT g4klx
// MMDVM-Host.ini. On Waypoint's own node these sections are inert (the forked
// MQTT-era MMDVM-Host has no [Display] parser); they matter for a clone running
// stock MMDVM-Host or driving a physical panel.
func renderDisplaySections(b *strings.Builder, m *Model) {
	// [Nextion]/[TFT Serial] share the same serial Port. ScreenLayout picks the
	// on-screen layout (0 G4KLX / 2 ON7LDS L2 / 3 L3 / 4 L3 HS).
	port := def(m.Display.Port, "modem")
	sect(b, "TFT Serial",
		kv("Port", port),
		kv("Brightness", "50"),
	)
	// HD44780: Pins is the GPIO 4-bit wiring (rs,strb,d0..d3), I2CAddress the
	// PCF8574 adapter address — this node wires over I2C, so Pins stays a constant
	// default and I2CAddress is the operator field. There is no I2C-bus key.
	sect(b, "HD44780",
		kv("Rows", def(m.Display.HD44780Rows, "2")),
		kv("Columns", def(m.Display.HD44780Cols, "16")),
		kv("Pins", "11,10,0,1,2,3"),
		kv("I2CAddress", def(m.Display.HD44780I2CAddr, "0x20")),
		kv("PWM", "0"),
		kv("PWMPin", "21"),
		kv("PWMBright", "100"),
		kv("PWMDim", "16"),
		kv("DisplayClock", "1"),
		kv("UTC", "0"),
	)
	sect(b, "Nextion",
		kv("Port", port),
		kv("Brightness", "50"),
		kv("DisplayClock", "1"),
		kv("UTC", "0"),
		kv("ScreenLayout", def(m.Display.NextionLayout, "0")),
		kv("IdleBrightness", "20"),
	)
	sect(b, "OLED",
		kv("Type", def(m.Display.OLEDType, "3")),
		kv("Brightness", "0"),
		kv("Invert", "0"),
		kv("Scroll", "1"),
		kv("Rotate", "0"),
		kv("Cast", "0"),
		kv("LogoScreensaver", "1"),
	)
	sect(b, "LCDproc",
		kv("Address", "localhost"),
		kv("Port", "13666"),
		kv("LocalPort", "13667"),
		kv("DisplayClock", "1"),
		kv("UTC", "0"),
	)
}

// DStarReconnectValues is DStarGateway's allowed [Repeater] ReflectorReconnect
// set (DStarGatewayConfig.cpp:242). Any other value fails the whole config load.
var DStarReconnectValues = []string{"Never", "Fixed", "5", "10", "15", "20", "25", "30", "60", "90", "120", "180"}

func clampReflectorReconnect(v string) string {
	for _, ok := range DStarReconnectValues {
		if v == ok {
			return v
		}
	}
	return "Never"
}

// Fixed loopback ports between MMDVM-Host and its gateways (the g4klx convention).
const (
	ysfMMDVMLocalPort   = "3200" // MMDVM-Host listens here; YSFGateway RptPort
	ysfMMDVMGatewayPort = "4200" // YSFGateway listens here; MMDVM-Host sends here

	p25MMDVMLocalPort   = "32010" // MMDVM-Host listens here; P25Gateway RptPort
	p25MMDVMGatewayPort = "42020" // P25Gateway listens here; MMDVM-Host sends here

	nxdnMMDVMLocalPort   = "14021" // MMDVM-Host listens here; NXDNGateway RptPort
	nxdnMMDVMGatewayPort = "14020" // NXDNGateway listens here; MMDVM-Host sends here

	dstarMMDVMLocalPort   = "20011" // MMDVM-Host listens here; DStarGateway [Repeater 1] Port
	dstarMMDVMGatewayPort = "20010" // DStarGateway [General] HBPort; MMDVM-Host sends here

	m17MMDVMLocalPort   = "17011" // MMDVM-Host listens here; M17Gateway RptPort
	m17MMDVMGatewayPort = "17010" // M17Gateway listens here (LocalPort); MMDVM-Host sends here

	pocsagMMDVMLocalPort   = "3800" // MMDVM-Host listens here; DAPNETGateway RptPort
	pocsagMMDVMGatewayPort = "4800" // DAPNETGateway listens here (LocalPort); MMDVM-Host sends here
)

// RenderYSFGateway renders a complete YSFGateway.ini from the model. Callsign,
// ID, and frequencies come from the shared station config; the rest from the
// YSFGateway section. Reflector/room hostlists are managed files on disk.
func (m *Model) RenderYSFGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", def(m.YSFGW.Suffix, "RPT")),
		kv("Id", m.General.ID),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", ysfMMDVMLocalPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysfMMDVMGatewayPort),
		kb("WiresXCommandPassthrough", m.YSFGW.WiresXPassthrough),
		// NB: this pinned YSFGateway (2b480aa) does not parse WiresXMakeUpper —
		// not emitted (would be a dead key). Re-add if a future pin honors it.
		kv("Daemon", "0"),
	)
	sect(&b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Name", def(m.General.Location, "Waypoint")),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "ysf-gateway"),
	)
	sect(&b, "Network",
		kv("Startup", m.YSFGW.Startup),
		// NB: [Network] Reconnect is NOT parsed by this pinned YSFGateway (only
		// Startup/Options/InactivityTimeout/Revert are) — omitted to avoid a
		// dead key. Inactivity behaviour is driven by Revert + InactivityTimeout.
		kb("Revert", m.YSFGW.Revert),
		kv("InactivityTimeout", def(m.YSFGW.InactivityTimeout, "30")),
		kv("Debug", "0"),
	)
	sect(&b, "YSF Network",
		kb("Enable", m.YSFGW.YSFNetwork),
		kv("Port", "42000"),
		kv("Hosts", ysfHostsPath),
		kv("ReloadTime", "60"),
		kv("ParrotAddress", "127.0.0.1"),
		kv("ParrotPort", "42012"),
	)
	sect(&b, "FCS Network",
		kb("Enable", m.YSFGW.FCSNetwork),
		kv("Port", "42001"),
		kv("Rooms", fcsRoomsPath),
	)
	sect(&b, "APRS",
		kb("Enable", m.YSFGW.APRS),
		kv("Suffix", "Y"),
	)
	return b.String()
}

// Managed reflector/room hostlists, fetched and cached by waypointd. The pinned
// YSFGateway parses YSFHosts as JSON (data["reflectors"]).
const (
	ysfHostsPath = "/home/pi-star/waypoint/etc/YSFHosts.json"
	fcsRoomsPath = "/home/pi-star/waypoint/etc/FCSRooms.txt"
)

// DGIdGateway-internal loopback ports for its per-DG-ID network blocks (from the
// pinned DGIdGateway.ini sample). These are private to DGIdGateway and only bind
// while it runs (YSFGateway is stopped then), so they never clash. DG-ID 0 MUST
// be the local Wires-X gateway or the radio's Wires-X buttons return NONE.
const (
	dgidGatewayPort  = "42025" // [DGId=0] Type=Gateway (Wires-X) remote port
	dgidGatewayLocal = "42026" // [DGId=0] local port
	dgidParrotPort   = "42012" // [DGId=1] Type=Parrot (local echo) remote port
	dgidParrotLocal  = "42013" // [DGId=1] local port
	dgidStartupDGId  = "5"     // [DGId=5] the auto-linked startup reflector (YCSNetwork)
	dgidStartupLocal = "42030" // [DGId=5] local port
)

// ysfStartupType classifies a startup reflector/room id for a DGIdGateway network
// block: an FCS room (e.g. FCS00290) is Type=FCS, anything else Type=YSF (the
// YCS/networked-reflector case). Mirrors how YSFGateway itself dispatches Startup.
func ysfStartupType(startup string) string {
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(startup)), "FCS") {
		return "FCS"
	}
	return "YSF"
}

// RenderDGIdGateway renders a complete DGIdGateway.ini from the model. DGIdGateway
// is the DG-ID-addressed alternative to YSFGateway (WPSD "DG-ID Gateway"): it
// binds MMDVM-Host's same 3200/4200 loopback (so it is rendered only when
// EnableDGId, replacing the YSFGateway target). It is MQTT-era like YSFGateway
// (Name=dgid-gateway on the data plane). Callsign/frequencies come from the
// shared station config; the reflector hostlist is the same managed YSFHosts.json.
//
// The DG-ID table is generated, not hand-edited: DG-ID 0 is the local Wires-X
// gateway (required), DG-ID 1 the local Parrot, and — when YCSNetwork is on and a
// startup reflector is set — DG-ID 5 auto-links that reflector/room. Every key
// here is one the pinned DGIdGateway Conf.cpp (@ 2b480aa) actually parses.
func (m *Model) RenderDGIdGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", def(m.YSFGW.Suffix, "RPT")),
		kv("Id", m.General.ID),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", ysfMMDVMLocalPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysfMMDVMGatewayPort),
		kv("RFHangTime", "120"),
		kv("NetHangTime", "120"),
		kv("Bleep", "1"),
		kv("Debug", "0"),
		kv("Daemon", "0"),
	)
	sect(&b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Description", def(m.General.Location, "Waypoint")),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "APRS",
		kb("Enable", m.YSFGW.APRS),
		kv("Suffix", "Y"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "dgid-gateway"),
	)
	// [YSF Network] carries the shared hostlist; [FCS Network] has no room-file
	// key (DGIdGateway resolves FCS rooms by Name in the DG-ID blocks below).
	sect(&b, "YSF Network",
		kv("Hosts", ysfHostsPath),
		kv("RFHangTime", "120"),
		kv("NetHangTime", "60"),
		kv("Debug", "0"),
	)
	sect(&b, "FCS Network",
		kv("RFHangTime", "120"),
		kv("NetHangTime", "60"),
		kv("Debug", "0"),
	)
	// DG-ID 0: the local Wires-X gateway (mandatory). DG-ID 1: local Parrot echo.
	sect(&b, "DGId=0",
		kv("Type", "Gateway"),
		kv("Static", "1"),
		kv("Address", "127.0.0.1"),
		kv("Port", dgidGatewayPort),
		kv("Local", dgidGatewayLocal),
		kv("Debug", "0"),
	)
	sect(&b, "DGId=1",
		kv("Type", "Parrot"),
		kv("Static", "0"),
		kv("Address", "127.0.0.1"),
		kv("Port", dgidParrotPort),
		kv("Local", dgidParrotLocal),
		kv("Debug", "0"),
	)
	// YCSNetwork: bind the startup reflector/room to a static DG-ID so the node
	// auto-links it. Type follows the id (FCS room vs YSF/YCS reflector); the
	// daemon resolves Address/Port from the hostlist, so only Name/Local are set.
	if m.YSFGW.YCSNetwork && strings.TrimSpace(m.YSFGW.Startup) != "" {
		sect(&b, "DGId="+dgidStartupDGId,
			kv("Type", ysfStartupType(m.YSFGW.Startup)),
			kv("Static", "1"),
			kv("Name", m.YSFGW.Startup),
			kv("Local", dgidStartupLocal),
			kv("Debug", "0"),
		)
	}
	sect(&b, "GPSD",
		kv("Enable", "0"),
	)
	return b.String()
}

// Managed paths for P25Gateway. The pinned P25Gateway parses P25Hosts as JSON
// (data["reflectors"], each with designator/port/ipv4). Audio holds the spoken
// announcement clips; a missing directory only disables voice, it is not fatal.
const (
	p25HostsPath = "/home/pi-star/waypoint/etc/P25Hosts.json"
	p25AudioDir  = "/home/pi-star/waypoint/etc/P25Audio"
)

// RenderP25Gateway renders a complete P25Gateway.ini from the model. Callsign
// and frequencies come from the shared station config; the rest from the P25
// mode/gateway sections. The reflector hostlist is a managed JSON file on disk.
func (m *Model) RenderP25Gateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", p25MMDVMLocalPort),
		kv("LocalPort", p25MMDVMGatewayPort),
		kv("Debug", "0"),
		kv("Daemon", "0"),
	)
	sect(&b, "Id Lookup",
		kv("Name", "/usr/local/etc/DMRIds.dat"),
		kv("Time", "24"),
	)
	sect(&b, "Voice",
		kb("Enabled", m.P25GW.Voice),
		kv("Language", "en_GB"),
		kv("Directory", p25AudioDir),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "p25-gateway"),
	)
	// Parrot (local echo, TG9990-style) runs on 42011; P252DMR is omitted so no
	// dead cross-mode TG is advertised. Static holds any startup/auto-link TGs.
	sect(&b, "Network",
		kv("Port", "42010"),
		kv("HostsFile1", p25HostsPath),
		// HostsFile2 is the optional local/private TG list. We keep it as an empty
		// source (/dev/null) so P25Gateway's unconditional parseHosts() call opens
		// cleanly instead of logging "Unable to open the Hosts file".
		kv("HostsFile2", "/dev/null"),
		kv("ReloadTime", "60"),
		kv("ParrotAddress", "127.0.0.1"),
		kv("ParrotPort", "42011"),
		kv("Static", m.P25GW.Static),
		kv("RFHangTime", def(m.P25GW.RFHangTime, "120")),
		kv("NetHangTime", def(m.P25GW.NetHangTime, "60")),
		kv("Debug", "0"),
	)
	sect(&b, "Remote Commands",
		kv("Enable", "0"),
	)
	return b.String()
}

// Managed paths for NXDNGateway. Like P25Gateway, NXDNGateway parses NXDNHosts
// as JSON (Reflectors.cpp parseJSON reads data["reflectors"], each with a
// designator/port/ipv4). Audio holds the spoken announcement clips; a missing
// directory only disables voice (NXDNGateway nulls it and continues).
const (
	nxdnHostsPath = "/home/pi-star/waypoint/etc/NXDNHosts.json"
	nxdnAudioDir  = "/home/pi-star/waypoint/etc/NXDNAudio"
)

// RenderNXDNGateway renders a complete NXDNGateway.ini from the model. Callsign
// comes from the shared station config; the rest from the NXDN mode/gateway
// sections. The reflector hostlist is a managed JSON file on disk. RptProtocol
// is Icom, the MMDVM transport (mirrors MMDVM-Host's [NXDN Network] Protocol).
func (m *Model) RenderNXDNGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", "NXDN"),
		kv("RptProtocol", "Icom"),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", nxdnMMDVMLocalPort),
		kv("LocalPort", nxdnMMDVMGatewayPort),
		kv("Debug", "0"),
		kv("Daemon", "0"),
	)
	// NXDN callsign lookup shares the RadioID DMR ID space; NXDNLookup's parser
	// splits on comma/tab, so it reads the tab-separated DMRIds.dat directly.
	sect(&b, "Id Lookup",
		kv("Name", "/usr/local/etc/DMRIds.dat"),
		kv("Time", "24"),
	)
	sect(&b, "Voice",
		kb("Enabled", m.NXDNGW.Voice),
		kv("Language", "en_GB"),
		kv("Directory", nxdnAudioDir),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "nxdn-gateway"),
	)
	// Parrot (local echo, TG10) runs on 42021; NXDN2DMR is omitted so no dead
	// cross-mode TG is advertised (Reflectors.cpp only adds TG20 when its port
	// is set). Static holds any startup/auto-link TGs (empty by default).
	sect(&b, "Network",
		kv("Port", "14050"),
		kv("HostsFile1", nxdnHostsPath),
		// HostsFile2 is the optional local/private TG list. We keep it as an empty
		// source (/dev/null) so NXDNGateway's unconditional parseHosts() opens
		// cleanly instead of logging "Unable to open the Hosts file".
		kv("HostsFile2", "/dev/null"),
		kv("ReloadTime", "60"),
		kv("ParrotAddress", "127.0.0.1"),
		kv("ParrotPort", "42021"),
		kv("Static", m.NXDNGW.Static),
		kv("RFHangTime", def(m.NXDNGW.RFHangTime, "120")),
		kv("NetHangTime", def(m.NXDNGW.NetHangTime, "60")),
		kv("Debug", "0"),
	)
	sect(&b, "GPSD",
		kv("Enable", "0"),
	)
	sect(&b, "Remote Commands",
		kv("Enable", "0"),
	)
	return b.String()
}

// Managed paths for DStarGateway. Unlike the other gateways, DStarGateway reads
// a single DStar_Hosts.json ({"reflectors":[{name,reflector_type,ipv4,…}]},
// HostsFilesManager.cpp) from the HostsFiles *directory*, and there is no live
// download URL upstream — waypointd caches the pinned bundled file here. Data is
// the audio-clip dir; a missing dir only disables voice, it is not fatal
// (loadPaths only records the path). CustomHostsfiles must differ from the data
// dir, so it points at a sibling overrides directory.
const (
	dstarHostsDir      = "/home/pi-star/waypoint/etc/"                    // holds DStar_Hosts.json
	dstarDataDir       = "/home/pi-star/waypoint/etc/dstar/"              // audio clips (optional)
	dstarCustomHostDir = "/home/pi-star/waypoint/etc/dstar-hostsfiles.d/" // local host overrides
)

// RenderDStarGateway renders a complete dstargateway.cfg from the model.
// Callsign comes from the shared station config; the module letter is
// m.DStar.Module (the single source of truth, mirrored into [Repeater 1] Band);
// the ircDDB login, startup reflector, and protocol enables come from the D-Star
// gateway section. IRCDDBUsername and D-Plus Login fall back to the station
// callsign, matching DStarGateway's own defaults (DStarGatewayConfig.cpp:298/128).
func (m *Model) RenderDStarGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	// Foreground: systemd manages the process (Daemon=0) — a forking daemon would
	// look dead to the unit.
	sect(&b, "Daemon",
		kv("Daemon", "0"),
	)
	// Type=Repeater is DStarGateway's own default and matches the homebrew (HB)
	// repeater MMDVM-Host presents; HBPort is where the gateway listens for
	// MMDVM-Host (the 20010 loopback).
	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Type", "Repeater"),
		kv("Address", "0.0.0.0"),
		kv("HBAddress", "127.0.0.1"),
		kv("HBPort", dstarMMDVMGatewayPort),
		kv("Latitude", "0.0"),
		kv("Longitude", "0.0"),
	)
	// ircDDB does the callsign→gateway routing lookups. Username defaults to the
	// station callsign; a blank password connects anonymously.
	sect(&b, "IRCDDB 1",
		kv("Enabled", "1"),
		kv("Hostname", def(m.DStarGW.IRCDDBHostname, "ircv4.openquad.net")),
		kv("Username", firstNonEmpty(m.DStarGW.IRCDDBUsername, m.General.Callsign)),
		kv("Password", m.DStarGW.IRCDDBPassword),
	)
	// The single D-Star module. Band must equal MMDVM-Host [D-Star] Module. Type
	// HB = homebrew (MMDVM-Host). Port 20011 is where the gateway sends to
	// MMDVM-Host. ReflectorAtStartup is derived: link the startup reflector only
	// when one is set (mirrors DStarGatewayConfig.cpp:239).
	sect(&b, "Repeater 1",
		kv("Enabled", "1"),
		kv("Band", def(m.DStar.Module, "B")),
		kv("Address", "127.0.0.1"),
		kv("Port", dstarMMDVMLocalPort),
		kv("Type", "HB"),
		kv("Reflector", m.DStarGW.Reflector),
		kb("ReflectorAtStartup", strings.TrimSpace(m.DStarGW.Reflector) != ""),
		// ReflectorReconnect is enum-validated upstream; an out-of-set value makes
		// DStarGateway's config load fail and the daemon abort. Clamp so a bad
		// store value can never render an unstartable config.
		kv("ReflectorReconnect", clampReflectorReconnect(m.DStarGW.ReflectorReconnect)),
	)
	// Reflector protocols. D-Plus Login defaults to the callsign; upstream
	// force-disables D-Plus when Login is empty (DStarGatewayConfig.cpp:130), and
	// REF linking additionally needs the callsign registered with DPlus/US-Trust.
	sect(&b, "Dextra",
		kb("Enabled", m.DStarGW.Dextra),
	)
	sect(&b, "D-Plus",
		kb("Enabled", m.DStarGW.DPlus),
		kv("Login", firstNonEmpty(m.DStarGW.DPlusLogin, m.General.Callsign)),
	)
	sect(&b, "DCS",
		kb("Enabled", m.DStarGW.DCS),
	)
	sect(&b, "XLX",
		kb("Enabled", m.DStarGW.XLX),
	)
	sect(&b, "APRS",
		kv("Enabled", "0"),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	// DStarGateway's [MQTT] key is Authenticate (not Auth like the other
	// gateways); Name is the topic prefix (<name>/json status, <name>/log).
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Authenticate", "0"),
		kv("Name", "dstar-gateway"),
	)
	sect(&b, "Paths",
		kv("Data", dstarDataDir),
	)
	// HostsFiles is a directory; the gateway reads DStar_Hosts.json inside it.
	sect(&b, "Hosts Files",
		kv("HostsFiles", dstarHostsDir),
		kv("CustomHostsfiles", dstarCustomHostDir),
	)
	sect(&b, "Remote Commands",
		kv("Enabled", "0"),
	)
	return b.String()
}

// Managed paths for M17Gateway. Unlike the other reflector daemons M17Gateway
// parses M17Hosts as SPACE/TAB-delimited text (Reflectors.cpp strtok on
// " \t\r\n": name, address, port) — NOT JSON. Audio holds the spoken
// announcement clips; a missing directory only nulls voice, it is not fatal.
const (
	m17HostsPath = "/home/pi-star/waypoint/etc/M17Hosts.txt"
	m17AudioDir  = "/home/pi-star/waypoint/etc/M17Audio"
)

// RenderM17Gateway renders a complete M17Gateway.ini from the model. This gateway
// is PRE-MQTT (the pinned g4klx/M17Gateway has no libmosquitto): it logs to the
// console/journal instead of publishing over MQTT, so unlike the YSF/P25/NXDN
// gateways there is no [MQTT] section and DisplayLevel is 1 (foreground →
// systemd journal) rather than 0. Callsign/frequencies come from the shared
// station config; the rest from the M17 gateway section. The reflector hostlist
// is a managed space/tab text file on disk.
func (m *Model) RenderM17Gateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	// Suffix is the node-type character appended to the callsign (H hotspot / R
	// repeater). RptPort 17011 is where the gateway sends to MMDVM-Host; LocalPort
	// 17010 is where it listens for MMDVM-Host.
	sect(&b, "General",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", def(m.M17GW.Suffix, "H")),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", m17MMDVMLocalPort),
		kv("LocalPort", m17MMDVMGatewayPort),
		kv("Debug", "0"),
		kv("Daemon", "0"),
	)
	sect(&b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Name", def(m.General.Location, "Waypoint")),
	)
	// Pre-MQTT gateway: log to the console at level 1 so the systemd journal
	// captures startup/link events; no separate log file (FileLevel 0).
	sect(&b, "Log",
		kv("DisplayLevel", "1"),
		kv("FileLevel", "0"),
		kv("FilePath", "/tmp"),
		kv("FileRoot", "M17Gateway"),
		kv("FileRotate", "0"),
	)
	sect(&b, "Voice",
		kb("Enabled", m.M17GW.Voice),
		kv("Language", "en_GB"),
		kv("Directory", m17AudioDir),
	)
	sect(&b, "APRS",
		kv("Enable", "0"),
	)
	// Port 17000 is the M17 reflector network (outbound to reflectors). HostsFile2
	// is the optional local/private list; /dev/null so the unconditional fopen
	// returns EOF cleanly instead of logging an error. Startup is a reflector name
	// whose trailing letter is the module (empty = don't auto-link on boot).
	sect(&b, "Network",
		kv("Port", "17000"),
		kv("HostsFile1", m17HostsPath),
		kv("HostsFile2", "/dev/null"),
		kv("ReloadTime", "60"),
		kv("Startup", m.M17GW.Startup),
		kb("Revert", m.M17GW.Revert),
		kv("HangTime", def(m.M17GW.HangTime, "240")),
		kv("Debug", "0"),
	)
	sect(&b, "Remote Commands",
		kv("Enable", "0"),
	)
	return b.String()
}

// RenderDAPNETGateway renders a complete DAPNETGateway.ini from the model — the
// POCSAG paging gateway. It logs the node into DAPNET (the amateur paging network)
// and relays pages to MMDVM-Host over the fixed 3800/4800 [POCSAG Network]
// loopback. Like the YSF/P25/NXDN gateways it is MQTT-era (DisplayLevel=0,
// MQTTLevel=1, Name=dapnet-gateway on the data plane). Callsign defaults to the
// station callsign; the DAPNET server and AuthKey come from the POCSAG section.
// AuthKey is the secret — it renders verbatim (empty until the operator sets one,
// and DAPNETGateway will not start with an unconfigured key). WhiteList/BlackList
// are RIC filters, omitted when blank (an empty value would filter everything).
func (m *Model) RenderDAPNETGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	// RptPort 3800 is where the gateway sends to MMDVM-Host; LocalPort 4800 is where
	// it listens for MMDVM-Host — the mirror of the [POCSAG Network] pair.
	general := []string{
		kv("Callsign", firstNonEmpty(m.POCSAG.Callsign, m.General.Callsign)),
	}
	if strings.TrimSpace(m.POCSAG.Whitelist) != "" {
		general = append(general, kv("WhiteList", m.POCSAG.Whitelist))
	}
	if strings.TrimSpace(m.POCSAG.Blacklist) != "" {
		general = append(general, kv("BlackList", m.POCSAG.Blacklist))
	}
	general = append(general,
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", pocsagMMDVMLocalPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", pocsagMMDVMGatewayPort),
		kv("Daemon", "0"),
	)
	sect(&b, "General", general...)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "dapnet-gateway"),
	)
	// DAPNET core server on the fixed transmitter port 43434; AuthKey authenticates
	// the login (a per-operator secret from the DAPNET web portal).
	sect(&b, "DAPNET",
		kv("Address", def(m.POCSAG.Server, "dapnet.afu.rwth-aachen.de")),
		kv("Port", "43434"),
		kv("AuthKey", m.POCSAG.AuthKey),
		kv("Debug", "0"),
	)
	return b.String()
}

// RenderDMRGateway renders a complete DMRGateway.ini from the model.
func (m *Model) RenderDMRGateway() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "General",
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", def(m.DMRNet.LocalPort, "62032")),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("Timeout", "10"),
		kv("Daemon", "0"),
	)
	sect(&b, "Log",
		kv("MQTTLevel", "1"),
		kv("DisplayLevel", "0"),
	)
	sect(&b, "MQTT",
		kv("Address", "127.0.0.1"),
		kv("Port", "1883"),
		kv("Keepalive", "60"),
		kv("Auth", "0"),
		kv("Name", "dmr-gateway"),
	)

	dmrID := firstNonEmpty(m.DMR.ID, m.General.ID)
	n := 0
	for _, net := range m.Networks {
		if net.Type == NetXLX {
			// XLX talks over a dedicated [XLX Network] section, not a DMR Network
			// block. Startup reflector, module, and slot are their own fields.
			sect(&b, "XLX Network",
				kb("Enabled", net.Enabled),
				kv("Startup", net.XLXStartup),
				kv("File", "/usr/local/etc/XLXHosts.txt"),
				kv("Port", def(net.Port, "62030")),
				kv("Password", net.Password),
				kv("ReloadTime", "60"),
				kv("Slot", def(net.XLXSlot, "2")),
				kv("TG", "6"),
				kv("Base", "64000"),
				kv("Relink", "60"),
				kb("Debug", false),
				kv("Id", dmrID),
				kv("UserControl", "1"),
				kv("Module", def(net.XLXModule, "A")),
			)
			continue
		}
		n++
		lines := []string{
			kv("Name", net.Name),
			kv("Address", net.Address),
			kv("Port", def(net.Port, "62031")),
			kv("Password", net.Password),
			kv("Id", dmrID+net.ESSID), // ESSID extends the DMR ID (Pi-Star extended ID)
		}
		if strings.TrimSpace(net.Options) != "" {
			lines = append(lines, kv("Options", net.Options))
		}
		if net.Primary {
			lines = append(lines, kv("Location", "1"))
		}
		lines = append(lines, kb("Enabled", net.Enabled), kb("Debug", false))
		// Routing generated from Type + Primary (mirrors WPSD); custom renders
		// the operator's verbatim lines. DMRRoute overrides append as TGRewrites.
		lines = append(lines, networkRewrites(net, m.Routes)...)
		sect(&b, fmt.Sprintf("DMR Network %d", n), lines...)
	}
	// RFC-0003 Addendum A §1: a DMR bus attachment multiplexes here as one more
	// [DMR Network N] — DMRGateway dials the bus's reserved loopback port and routes
	// the bus's talkgroups to it, exactly as it routes an upstream network. MMDVM-Host
	// and every upstream network above are untouched (no displacement). The numbering
	// continues past the operator's networks so a bus never renumbers a real one.
	for _, bn := range m.dmrBusNetworks(dmrID) {
		n++
		lines := []string{
			kv("Name", bn.name),
			kv("Address", "127.0.0.1"),
			kv("Port", fmt.Sprintf("%d", bn.port)),
			kv("Password", "passw0rd"), // local loopback peer; not a network credential (Addendum Open Q3)
			kv("Id", bn.id),
			kb("Enabled", true),
			kb("Debug", false),
		}
		lines = append(lines, bn.rewrites()...)
		sect(&b, fmt.Sprintf("DMR Network %d", n), lines...)
	}
	return b.String()
}

// --- rendering helpers (deterministic) -----------------------------------

func sect(b *strings.Builder, name string, lines ...string) {
	b.WriteString("\n[")
	b.WriteString(name)
	b.WriteString("]\n")
	for _, l := range lines {
		b.WriteString(l)
		b.WriteByte('\n')
	}
}

func kv(k, v string) string { return k + "=" + v }

func kb(k string, on bool) string {
	if on {
		return k + "=1"
	}
	return k + "=0"
}

func def(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
