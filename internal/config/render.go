package config

import (
	"fmt"
	"os"
	"path/filepath"
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
	DAPNETGateway string // POCSAG paging gateway (always rendered, like the mode gateways)
	// Cross-mode bridge INIs (MMDVM_CM). Each is rendered only when its bridge is
	// enabled (RenderTargets), so a disabled bridge writes no file and restarts no
	// unit.
	YSF2DMR  string
	DMR2YSF  string
	YSF2NXDN string
	DMR2NXDN string
	NXDN2DMR string
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

	unitYSF2DMR  = "waypoint-ysf2dmr.service"
	unitDMR2YSF  = "waypoint-dmr2ysf.service"
	unitYSF2NXDN = "waypoint-ysf2nxdn.service"
	unitDMR2NXDN = "waypoint-dmr2nxdn.service"
	unitNXDN2DMR = "waypoint-nxdn2dmr.service"
)

// RenderTarget ties one generated INI to the daemon unit that consumes it and
// the pure function that produces it. A mode contributes its own target rather
// than editing the apply loop — this is issue #21's gateway-plugin seam.
type RenderTarget struct {
	Path   string              // where the daemon reads its INI
	Unit   string              // systemd unit to restart when this file changes
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
	ysf := RenderTarget{Path: paths.YSFGateway, Unit: unitYSFGateway, Render: (*Model).RenderYSFGateway}
	if m.YSFGW.EnableDGId {
		ysf = RenderTarget{Path: paths.DGIdGateway, Unit: unitDGIdGateway, Render: (*Model).RenderDGIdGateway}
	}
	targets := []RenderTarget{
		{Path: paths.MMDVM, Unit: unitMMDVM, Render: (*Model).RenderMMDVM},
		{Path: paths.DMRGateway, Unit: unitDMRGateway, Render: (*Model).RenderDMRGateway},
		ysf,
		{Path: paths.P25Gateway, Unit: unitP25Gateway, Render: (*Model).RenderP25Gateway},
		{Path: paths.NXDNGateway, Unit: unitNXDNGateway, Render: (*Model).RenderNXDNGateway},
		{Path: paths.DStarGateway, Unit: unitDStarGateway, Render: (*Model).RenderDStarGateway},
		{Path: paths.M17Gateway, Unit: unitM17Gateway, Render: (*Model).RenderM17Gateway},
		// POCSAG's DAPNETGateway is an always-on mode gateway (like YSF/P25/NXDN/M17):
		// it is rendered every apply, and the [POCSAG Network] Enable in MMDVM-Host.ini
		// gates whether the daemon actually receives paging traffic.
		{Path: paths.DAPNETGateway, Unit: unitDAPNETGateway, Render: (*Model).RenderDAPNETGateway},
	}
	// Cross-mode bridges append after the always-on gateways, and only when
	// enabled: an off bridge contributes no target, so apply neither writes its
	// INI nor restarts its unit (stopping an already-running bridge on disable is
	// a deploy/systemd concern, like the YSFGateway/DGIdGateway Conflicts=). This
	// keeps the leading MMDVM/DMRGateway indices fixed for the apply/restart order.
	for _, b := range []struct {
		on     bool
		path   string
		unit   string
		render func(*Model) string
	}{
		{m.YSF2DMR.Enable, paths.YSF2DMR, unitYSF2DMR, (*Model).RenderYSF2DMR},
		{m.DMR2YSF.Enable, paths.DMR2YSF, unitDMR2YSF, (*Model).RenderDMR2YSF},
		{m.YSF2NXDN.Enable, paths.YSF2NXDN, unitYSF2NXDN, (*Model).RenderYSF2NXDN},
		{m.DMR2NXDN.Enable, paths.DMR2NXDN, unitDMR2NXDN, (*Model).RenderDMR2NXDN},
		{m.NXDN2DMR.Enable, paths.NXDN2DMR, unitNXDN2DMR, (*Model).RenderNXDN2DMR},
	} {
		if b.on {
			targets = append(targets, RenderTarget{Path: b.path, Unit: b.unit, Render: b.render})
		}
	}
	return targets
}

// WriteFiles renders every target and writes its INI atomically (write to a
// temp file in the same directory, then rename). A crash mid-apply therefore
// never leaves a daemon reading a half-written config.
func (m *Model) WriteFiles(paths Paths) error {
	for _, t := range m.RenderTargets(paths) {
		if err := writeAtomic(t.Path, t.Render(m)); err != nil {
			return err
		}
	}
	return nil
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
	return b.String()
}

// --- cross-mode transcoding bridges (MMDVM_CM) ---------------------------

// Cross-mode bridge fixed ports + shared lookup paths, transcribed from the
// MMDVM_CM sample INIs. Each bridge borrows the loopback of the gateway it reads
// from or writes to (YSF 3200/4200, NXDN 14020/14021, DMR 62031/62032), so a
// bridge and that gateway cannot run at once — a deploy/systemd concern (like the
// YSFGateway/DGIdGateway Conflicts=), not modeled here. The renderers emit a
// faithful, startable INI; the store owns only the operator-facing keys.
const (
	ysf2dmrYSFLocal  = "42013" // YSF2DMR  [YSF Network] LocalPort
	ysf2nxdnYSFLocal = "42014" // YSF2NXDN [YSF Network] LocalPort
	ysfNetworkPort   = "42000" // YSFGateway [YSF Network] Port the YSF-side bridges connect to

	nxdn2dmrNXDNLocal = "42022" // NXDN2DMR [NXDN Network] LocalPort
	nxdnNetworkPort   = "14050" // NXDNGateway [Network] Port the NXDN-side bridge connects to

	dmrMasterPort = "62031" // upstream DMR master port for the master-logging bridges (YSF2DMR/NXDN2DMR)

	crossDMRIdsFile  = "/usr/local/etc/DMRIds.dat"
	crossNXDNIdsFile = "/usr/local/etc/NXDN.csv"
)

// bridgeLog is the pre-MQTT [Log] block shared by every MMDVM_CM bridge: like
// M17Gateway these tools have no libmosquitto, so they log to the console
// (DisplayLevel=1 → the systemd journal) and write no file (FileLevel=0). Their
// own link status is therefore not on the dashboard data plane; RF activity still
// surfaces through MMDVM-Host.
func bridgeLog(b *strings.Builder, root string) {
	sect(b, "Log",
		kv("DisplayLevel", "1"),
		kv("FileLevel", "0"),
		kv("FilePath", "/tmp"),
		kv("FileRoot", root),
	)
}

// bridgeInfo is the shared [Info] block (station frequencies/power/location).
func bridgeInfo(b *strings.Builder, m *Model) {
	sect(b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Location", def(m.General.Location, "Waypoint")),
		kv("Description", "Waypoint"),
	)
}

// dmrMasterNet writes the [DMR Network] block for a bridge that logs into its own
// upstream DMR master (YSF2DMR, NXDN2DMR): the DMR ID, master address+password,
// target talkgroup (StartupDstId, group call so StartupPC=0), and the optional
// WPSD Options line (omitted when blank, like a DMR network's).
func dmrMasterNet(b *strings.Builder, dmrID, master, password, tg, options string) {
	lines := []string{
		kv("Id", dmrID),
		kv("StartupDstId", def(tg, "9990")),
		kv("StartupPC", "0"),
		kv("Address", master),
		kv("Port", dmrMasterPort),
		kv("Jitter", "500"),
		kv("Password", password),
	}
	if strings.TrimSpace(options) != "" {
		lines = append(lines, kv("Options", options))
	}
	lines = append(lines, kv("Debug", "0"))
	sect(b, "DMR Network", lines...)
}

// RenderYSF2DMR renders a complete YSF2DMR.ini — the fat bridge. It reads System
// Fusion from YSFGateway's [YSF Network] (port 42000) and re-emits on its own DMR
// master. Callsign/frequencies come from the shared station config; the DMR ID,
// master, password, options and target TG come from the bridge section.
func (m *Model) RenderYSF2DMR() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	bridgeInfo(&b, m)
	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", "ND"),
		kv("DstAddress", "127.0.0.1"),
		kv("DstPort", ysfNetworkPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysf2dmrYSFLocal),
		kb("EnableWiresX", true),
		kb("RemoteGateway", false),
		kv("Daemon", "0"),
	)
	dmrMasterNet(&b, firstNonEmpty(m.YSF2DMR.DMRId, m.DMR.ID, m.General.ID),
		m.YSF2DMR.Master, m.YSF2DMR.Password, m.YSF2DMR.TG, m.YSF2DMR.Options)
	sect(&b, "DMR Id Lookup",
		kv("File", crossDMRIdsFile),
		kv("Time", "24"),
	)
	bridgeLog(&b, "YSF2DMR")
	return b.String()
}

// RenderDMR2YSF renders a complete DMR2YSF.ini. It rides the local DMRGateway
// loopback (62031/62032) on the DMR side and takes over the YSF 3200/4200 pair on
// the YSF side. DefaultTG is the DMR-side default destination talkgroup.
func (m *Model) RenderDMR2YSF() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign),
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", ysfMMDVMGatewayPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysfMMDVMLocalPort),
		kv("FCSRooms", fcsRoomsPath),
		kv("Daemon", "0"),
		kv("Debug", "0"),
	)
	sect(&b, "DMR Network",
		kv("Id", firstNonEmpty(m.DMR2YSF.DMRId, m.DMR.ID, m.General.ID)),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", def(m.DMRNet.LocalPort, "62032")),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("DefaultDstTG", def(m.DMR2YSF.DefaultTG, "9")),
		kv("Debug", "0"),
	)
	sect(&b, "DMR Id Lookup",
		kv("File", crossDMRIdsFile),
		kv("Time", "24"),
	)
	bridgeLog(&b, "DMR2YSF")
	return b.String()
}

// RenderYSF2NXDN renders a complete YSF2NXDN.ini. It reads System Fusion from
// YSFGateway's [YSF Network] and re-emits on the NXDN 14020/14021 pair. NXDNId is
// the id it registers with; TG is the target NXDN talkgroup.
func (m *Model) RenderYSF2NXDN() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	bridgeInfo(&b, m)
	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign),
		kv("Suffix", "ND"),
		kv("DstAddress", "127.0.0.1"),
		kv("DstPort", ysfNetworkPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", ysf2nxdnYSFLocal),
		kb("EnableWiresX", true),
		kv("Daemon", "0"),
	)
	sect(&b, "NXDN Network",
		kv("Id", m.YSF2NXDN.NXDNId),
		kv("StartupDstId", def(m.YSF2NXDN.TG, "0")),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", nxdnMMDVMLocalPort),
		kv("DstAddress", "127.0.0.1"),
		kv("DstPort", nxdnMMDVMGatewayPort),
		kv("Debug", "0"),
	)
	sect(&b, "NXDN Id Lookup",
		kv("File", crossNXDNIdsFile),
		kv("Time", "24"),
	)
	bridgeLog(&b, "YSF2NXDN")
	return b.String()
}

// RenderDMR2NXDN renders a complete DMR2NXDN.ini. It rides the local DMRGateway
// loopback on the DMR side and the NXDN 14020/14021 pair on the NXDN side. NXDNId
// is the NXDN-side default id.
func (m *Model) RenderDMR2NXDN() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	sect(&b, "NXDN Network",
		kv("GatewayAddress", "127.0.0.1"),
		kv("GatewayPort", nxdnMMDVMGatewayPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", nxdnMMDVMLocalPort),
		kv("DefaultID", def(m.DMR2NXDN.NXDNId, "65519")),
		kv("Daemon", "0"),
	)
	sect(&b, "DMR Network",
		kv("Id", firstNonEmpty(m.DMR2NXDN.DMRId, m.DMR.ID, m.General.ID)),
		kv("RptAddress", "127.0.0.1"),
		kv("RptPort", def(m.DMRNet.LocalPort, "62032")),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("Debug", "0"),
	)
	sect(&b, "DMR Id Lookup",
		kv("File", crossDMRIdsFile),
		kv("Time", "24"),
	)
	sect(&b, "NXDN Id Lookup",
		kv("File", crossNXDNIdsFile),
		kv("Time", "24"),
	)
	bridgeLog(&b, "DMR2NXDN")
	return b.String()
}

// RenderNXDN2DMR renders a complete NXDN2DMR.ini — the other fat bridge. It reads
// NXDN from NXDNGateway (port 14050) and re-emits on its own DMR master, exactly
// like YSF2DMR. NXDNTG is the NXDN-side listen talkgroup.
func (m *Model) RenderNXDN2DMR() string {
	var b strings.Builder
	b.WriteString(generatedHeader)

	bridgeInfo(&b, m)
	sect(&b, "NXDN Network",
		kv("Callsign", m.General.Callsign),
		kv("TG", def(m.NXDN2DMR.NXDNTG, "20")),
		kv("DstAddress", "127.0.0.1"),
		kv("DstPort", nxdnNetworkPort),
		kv("LocalAddress", "127.0.0.1"),
		kv("LocalPort", nxdn2dmrNXDNLocal),
		kv("DefaultID", "65519"),
		kv("Daemon", "0"),
	)
	dmrMasterNet(&b, firstNonEmpty(m.NXDN2DMR.DMRId, m.DMR.ID, m.General.ID),
		m.NXDN2DMR.Master, m.NXDN2DMR.Password, m.NXDN2DMR.TG, m.NXDN2DMR.Options)
	sect(&b, "DMR Id Lookup",
		kv("File", crossDMRIdsFile),
		kv("Time", "24"),
	)
	sect(&b, "NXDN Id Lookup",
		kv("File", crossNXDNIdsFile),
		kv("Time", "24"),
	)
	bridgeLog(&b, "NXDN2DMR")
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
