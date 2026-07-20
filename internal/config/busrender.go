package config

import (
	"sort"
	"strings"
)

// Bus config format (RFC-0003 §4, render half). One file per bus, rendered as a
// pure function of the model — the same model always yields byte-identical
// output. A bus with N attachments is ONE file with N attachment sections, not N
// files: the hub is one supervised unit (waypoint-bus@<id>.service), and the N
// endpoints are rows inside its one rendered config.
//
// The format is INI, matching the digital-voice daemon family (MMDVM-Host,
// DMRGateway, the retired MMDVM_CM bridges). Layout:
//
//	[Bus]
//	Id=<bus id>
//	Name=<bus name>
//
//	[DMR] | [YSF] | [NXDN]   ; one section per attachment, keyed by mode
//	BindAddress/BindPort     ; the endpoint this attachment's socket binds
//	PeerAddress/PeerPort     ; the fixed loopback peer it talks to
//	<translation params>     ; per §3, keyed by the RFC-0003 store names
//	HangTime=<ms>            ; arbitration hang the CM tools expose (§5)
//	IdLookup=<path>          ; DMR/NXDN only — the SHARED DMRIds.dat (no 2nd file)
//
// Every attachment uses uniform Bind*/Peer* endpoint keys (rather than the CM
// tools' per-tool RptPort/GatewayPort spellings) because the bus is one hub
// binding many endpoints; a single vocabulary keeps the daemon reader (Prompt 3,
// ParseBusConfig) simple. The endpoints are the FIXED per-mode loopback pairs
// (render.go): DMR rides the local DMRGateway loopback (BindPort=62031 /
// PeerPort=62032, exactly as DMR2YSF.ini's [DMR Network] LocalPort=62031
// RptPort=62032), YSF the MMDVM-Host 3200/4200 pair, NXDN the 14021/14020 pair.
//
// CRITICAL: a bus config carries NO credential of any kind. A DMR attachment
// reaches upstream through the existing DMRGateway routing, so it names the
// Networks[] entry via CredentialsRef (a name, never an address or password).
// The renderer never reads Network.Address or Network.Password — enforced by a
// test that no rendered bus config contains a Networks[] secret.

const (
	// busIdLookupFile is the SHARED station DMR ID lookup the gateways already
	// render (render.go: DMR "DMR Id Lookup" File, NXDN "Id Lookup" Name). A bus
	// adds no second lookup file (RFC-0003 §3): DMR and NXDN attachments both point
	// here, and NXDN shares the DMR ID space.
	busIdLookupFile = "/usr/local/etc/DMRIds.dat"

	// busHangTime is the per-attachment arbitration hang (ms of silence before the
	// bus's single source token releases, RFC-0003 §5). It mirrors the hang the CM
	// tools expose ([YSF Network] HangTime=1000 in YSF2DMR.ini).
	busHangTime = "1000"

	// busDMRDefaultTG / busDMRDefaultSlot are the DMR-side fallbacks matching the CM
	// bridges (DMR2YSF.ini [DMR Network] DefaultDstTG=9; TS2 is the conventional
	// cross-mode slot).
	busDMRDefaultTG   = "9"
	busDMRDefaultSlot = "2"

	// busNXDNDefaultID is the NXDN-side default id fallback (NXDN2DMR.ini
	// [NXDN Network] DefaultID=65519).
	busNXDNDefaultID = "65519"

	loopbackAddr = "127.0.0.1"
)

// busAttachments returns the attachments on one bus, sorted by mode rank (buses.go
// modeRank) so the rendered file is deterministic regardless of store order.
func (m *Model) busAttachments(busID string) []Attachment {
	var out []Attachment
	for _, a := range m.Attachments {
		if a.BusID == busID {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return modeRank(out[i].Mode) < modeRank(out[j].Mode) })
	return out
}

// renderBus renders one bus's config file. Pure function of the bus, its
// attachments, and the DMR loopback ports (DMRNet) — nothing else, so an
// unrelated store edit never changes a bus's output (RFC-0003 §6.1).
func (m *Model) renderBus(bus Bus) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	sect(&b, "Bus",
		kv("Id", bus.ID),
		kv("Name", bus.Name),
	)
	for _, a := range m.busAttachments(bus.ID) {
		switch a.Mode {
		case ModeDMR:
			m.renderBusDMR(&b, a)
		case ModeYSF:
			renderBusYSF(&b, a)
		case ModeNXDN:
			renderBusNXDN(&b, a)
		}
	}
	return b.String()
}

// renderBusDMR renders the [DMR] attachment section. It rides the local DMRGateway
// loopback exactly as DMR2YSF.ini does (LocalPort=62031, RptPort=62032), sourced
// from DMRNet so a customized loopback follows. CredentialsRef names a Networks[]
// entry; no address or password is ever emitted.
func (m *Model) renderBusDMR(b *strings.Builder, a Attachment) {
	lines := []string{
		kv("BindAddress", loopbackAddr),
		kv("BindPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("PeerAddress", loopbackAddr),
		kv("PeerPort", def(m.DMRNet.LocalPort, "62032")),
		kv("Slot", def(a.Slot, busDMRDefaultSlot)),
		kv("DefaultTG", def(a.DefaultTG, busDMRDefaultTG)),
	}
	if tg := busTGMap(a.TGMap); tg != "" {
		lines = append(lines, kv("TGMap", tg))
	}
	if a.CredentialsRef != "" {
		lines = append(lines, kv("CredentialsRef", a.CredentialsRef))
	}
	lines = append(lines,
		kv("HangTime", busHangTime),
		kv("IdLookup", busIdLookupFile),
	)
	sect(b, "DMR", lines...)
}

// renderBusYSF renders the [YSF] attachment section. YSF attaches as DN on the
// fixed MMDVM-Host 3200/4200 loopback (render.go ysfMMDVMLocalPort/GatewayPort).
func renderBusYSF(b *strings.Builder, a Attachment) {
	lines := []string{
		kv("BindAddress", loopbackAddr),
		kv("BindPort", ysfMMDVMLocalPort),
		kv("PeerAddress", loopbackAddr),
		kv("PeerPort", ysfMMDVMGatewayPort),
	}
	if a.Target != "" {
		lines = append(lines, kv("Target", a.Target))
	}
	lines = append(lines,
		kb("WiresXPassthrough", a.WiresXPassthrough),
		kv("HangTime", busHangTime),
	)
	sect(b, "YSF", lines...)
}

// renderBusNXDN renders the [NXDN] attachment section on the fixed 14021/14020
// loopback (render.go nxdnMMDVMLocalPort/GatewayPort). IdLookup points at the
// shared DMRIds.dat — the NXDN gateway's own lookup path (render.go) — not a
// second NXDN.csv, per RFC-0003 §3.
func renderBusNXDN(b *strings.Builder, a Attachment) {
	lines := []string{
		kv("BindAddress", loopbackAddr),
		kv("BindPort", nxdnMMDVMLocalPort),
		kv("PeerAddress", loopbackAddr),
		kv("PeerPort", nxdnMMDVMGatewayPort),
	}
	if a.ID != "" {
		lines = append(lines, kv("Id", a.ID))
	}
	if a.TG != "" {
		lines = append(lines, kv("TG", a.TG))
	}
	lines = append(lines, kv("DefaultID", def(a.DefaultID, busNXDNDefaultID)))
	if a.Target != "" {
		lines = append(lines, kv("Target", a.Target))
	}
	lines = append(lines,
		kv("HangTime", busHangTime),
		kv("IdLookup", busIdLookupFile),
	)
	sect(b, "NXDN", lines...)
}

// busTGMap renders an attachment's tg_map (source-mode target -> DMR TG) as a
// single deterministic "src:dst,src:dst" value, sorted by source so the output is
// byte-stable. One key (not repeated lines) keeps the value round-trippable
// through the flat INI reader.
func busTGMap(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, k := range keys {
		pairs = append(pairs, k+":"+m[k])
	}
	return strings.Join(pairs, ",")
}

// RenderBusUnit renders the systemd TEMPLATE unit waypoint-bus@.service — one
// template the installer deploys once; each enabled bus runs as the instance
// waypoint-bus@<id>.service (%i = the bus id, which is also its config basename).
// After= the gateways a bus rides (DMR/YSF/NXDN) so their loopbacks exist first;
// Restart=on-failure. The ExecStart binary (waypoint-bus, built in Prompt 3) is
// referenced by path and need not exist for this to render.
func RenderBusUnit() string {
	return `; Generated by waypointd — do NOT edit. Deploy as waypoint-bus@.service.
[Unit]
Description=Waypoint mode bus %i
After=waypoint-dmrgateway.service waypoint-ysfgateway.service waypoint-nxdngateway.service
Wants=waypoint-dmrgateway.service

[Service]
Type=simple
ExecStart=/usr/local/bin/waypoint-bus --config /etc/waypoint/bus/%i.conf
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
`
}
