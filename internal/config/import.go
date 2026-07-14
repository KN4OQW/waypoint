package config

import (
	"fmt"
	"sort"
	"strings"
)

// Import builds a Model from existing MMDVM-Host / DMRGateway INI files. It runs
// once, to seed a fresh store from whatever the node is already running, after
// which the store is authoritative and the INIs are regenerated from it. This
// is the only place INIs are parsed *into* the model — everywhere else they are
// compiled outputs.
func Import(mmdvmPath, dmrgatewayPath string) (*Model, error) {
	mm, err := ParseINIFile(mmdvmPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", mmdvmPath, err)
	}
	dg, err := ParseINIFile(dmrgatewayPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dmrgatewayPath, err)
	}
	// No YSFGateway/DGIdGateway/P25Gateway/NXDNGateway/dstargateway.cfg/M17Gateway.ini
	// or cross-mode bridge INI exists at seed time (waypointd creates them); those
	// sections get defaults. The gateway/bridge INIs are non-nil only in the
	// round-trip harness.
	return fromINI(mm, dg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), nil
}

// fromINI builds a Model from already-parsed INIs. Shared by Import (from disk)
// and the round-trip harness (render → parse → fromINI, all in memory). The
// gateway INIs yg (YSFGateway), dgid (DGIdGateway), pg (P25Gateway), ng
// (NXDNGateway), xg (dstargateway.cfg) and mg (M17Gateway) may be nil, in which
// case their sections take their defaults. When dgid is non-nil the System
// Fusion config is read from it (the DG-ID daemon) instead of yg.
//
// dpg (DAPNETGateway.ini) is the POCSAG gateway, read like the always-on gateways
// (yg/pg/ng): nil yields defaults, non-nil is read back. The POCSAG mode enable and
// paging Frequency come from mm's [POCSAG] section, not dpg.
//
// The five trailing INIs are the cross-mode bridges — y2d (YSF2DMR), d2y
// (DMR2YSF), y2n (YSF2NXDN), d2n (DMR2NXDN), n2d (NXDN2DMR). A bridge daemon has
// no INI Enable key, so its presence (non-nil) IS its Enable: a nil bridge INI
// yields a disabled default, a non-nil one an enabled bridge with its fields read
// back (mirrors how dgid's presence implies EnableDGId).
func fromINI(mm, dg, yg, dgid, pg, ng, xg, mg, dpg, y2d, d2y, y2n, d2n, n2d *INI) *Model {
	m := &Model{
		General: General{
			Callsign:    mm.Get("General", "Callsign"),
			ID:          mm.Get("General", "Id"),
			Duplex:      mm.Bool("General", "Duplex"),
			Timeout:     orDefault(mm.Get("General", "Timeout"), "240"),
			RFModeHang:  orDefault(mm.Get("General", "RFModeHang"), "300"),
			NetModeHang: orDefault(mm.Get("General", "NetModeHang"), "300"),
			Power:       orDefault(mm.Get("Info", "Power"), "1"),
			Location:    mm.Get("Info", "Location"),
			URL:         mm.Get("Info", "URL"),
		},
		Modem: Modem{
			Port:      firstNonEmpty(mm.Get("Modem", "UARTPort"), mm.Get("Modem", "Port"), "/dev/ttyAMA0"),
			UARTSpeed: orDefault(mm.Get("Modem", "UARTSpeed"), "115200"),
			RXFreqHz:  mm.Get("Info", "RXFrequency"),
			TXFreqHz:  mm.Get("Info", "TXFrequency"),
			RXOffset:  orDefault(mm.Get("Modem", "RXOffset"), "0"),
			TXOffset:  orDefault(mm.Get("Modem", "TXOffset"), "0"),
			TXInvert:  mm.Bool("Modem", "TXInvert"),
			RXInvert:  mm.Bool("Modem", "RXInvert"),
			PTTInvert: mm.Bool("Modem", "PTTInvert"),
			RXLevel:   orDefault(mm.Get("Modem", "RXLevel"), "50"),
			TXLevel:   orDefault(mm.Get("Modem", "TXLevel"), "50"),
		},
		Display: displayFromINI(mm),
		DMR: DMR{
			ColorCode:      orDefault(mm.Get("DMR", "ColorCode"), "1"),
			ID:             firstNonEmpty(mm.Get("DMR", "Id"), mm.Get("General", "Id")),
			EmbeddedLCOnly: mm.Bool("DMR", "EmbeddedLCOnly"),
			SelfOnly:       mm.Bool("DMR", "SelfOnly"),
			DumpTAData:     mm.Bool("DMR", "DumpTAData"),
			Beacons:        mm.Bool("DMR", "Beacons"),
		},
		DMRNet: DMRNet{
			LocalPort:      orDefault(mm.Get("DMR Network", "LocalPort"), "62032"),
			GatewayAddress: orDefault(mm.Get("DMR Network", "GatewayAddress"), "127.0.0.1"),
			GatewayPort:    orDefault(mm.Get("DMR Network", "GatewayPort"), "62031"),
			Slot1:          mm.Get("DMR Network", "Slot1") != "0",
			Slot2:          mm.Get("DMR Network", "Slot2") != "0",
			Jitter:         orDefault(mm.Get("DMR Network", "Jitter"), "360"),
		},
		Modes: Modes{
			DStar:  mm.Bool("D-Star", "Enable"),
			DMR:    mm.Bool("DMR", "Enable"),
			YSF:    mm.Bool("System Fusion", "Enable"),
			P25:    mm.Bool("P25", "Enable"),
			NXDN:   mm.Bool("NXDN", "Enable"),
			M17:    mm.Bool("M17", "Enable"),
			POCSAG: mm.Bool("POCSAG", "Enable"),
			FM:     mm.Bool("FM", "Enable"),
		},
		Networks: importNetworks(dg, firstNonEmpty(mm.Get("DMR", "Id"), mm.Get("General", "Id"))),
		YSF: YSF{
			LowDeviation:  mm.Bool("System Fusion", "LowDeviation"),
			SelfOnly:      mm.Bool("System Fusion", "SelfOnly"),
			TXHang:        orDefault(mm.Get("System Fusion", "TXHang"), "4"),
			RemoteGateway: mm.Bool("System Fusion", "RemoteGateway"),
			ModeHang:      orDefault(mm.Get("System Fusion", "ModeHang"), "20"),
		},
		YSFGW: ysfGatewayFromINI(yg, dgid),
		P25: P25{
			NAC:              orDefault(mm.Get("P25", "NAC"), "293"),
			SelfOnly:         mm.Bool("P25", "SelfOnly"),
			OverrideUIDCheck: mm.Bool("P25", "OverrideUIDCheck"),
			RemoteGateway:    mm.Bool("P25", "RemoteGateway"),
			TXHang:           orDefault(mm.Get("P25", "TXHang"), "5"),
		},
		P25GW: p25GatewayFromINI(pg),
		NXDN: NXDN{
			RAN:           orDefault(mm.Get("NXDN", "RAN"), "1"),
			SelfOnly:      mm.Bool("NXDN", "SelfOnly"),
			RemoteGateway: mm.Bool("NXDN", "RemoteGateway"),
			TXHang:        orDefault(mm.Get("NXDN", "TXHang"), "5"),
		},
		NXDNGW: nxdnGatewayFromINI(ng),
		DStar: DStar{
			Module:        orDefault(strings.ToUpper(mm.Get("D-Star", "Module")), "B"),
			SelfOnly:      mm.Bool("D-Star", "SelfOnly"),
			RemoteGateway: mm.Bool("D-Star", "RemoteGateway"),
		},
		DStarGW: dstarGatewayFromINI(xg),
		M17: M17{
			CAN:             orDefault(mm.Get("M17", "CAN"), "0"),
			SelfOnly:        mm.Bool("M17", "SelfOnly"),
			AllowEncryption: mm.Bool("M17", "AllowEncryption"),
			TXHang:          orDefault(mm.Get("M17", "TXHang"), "5"),
		},
		M17GW:    m17GatewayFromINI(mg),
		POCSAG:   pocsagFromINI(mm, dpg),
		FM:       fmFromINI(mm),
		YSF2DMR:  ysf2dmrFromINI(y2d),
		DMR2YSF:  dmr2ysfFromINI(d2y),
		YSF2NXDN: ysf2nxdnFromINI(y2n),
		DMR2NXDN: dmr2nxdnFromINI(d2n),
		NXDN2DMR: nxdn2dmrFromINI(n2d),
		// LCD drives no INI, so there is nothing to import — a seeded model gets the
		// display-free defaults (with starter pages), like the gateway sections that
		// have no seed file. The store is authoritative from then on.
		LCD: DefaultLCD(),
	}
	return m
}

// --- cross-mode bridges --------------------------------------------------
// A bridge INI carries no Enable key, so each reader treats a nil INI as the
// disabled default and a non-nil INI as an enabled bridge (its presence is its
// Enable — see fromINI). Only the operator-facing keys are read back; the fixed
// loopback/log keys are constants in render.go and need no round-trip.

// DefaultYSF2DMR / DefaultDMR2YSF / … are the disabled defaults used to seed a
// fresh store and to backfill a store created before the bridges existed. A zero
// bridge is off with empty fields; the operator fills in the master/TG when
// enabling it.
func DefaultYSF2DMR() YSF2DMR   { return YSF2DMR{} }
func DefaultDMR2YSF() DMR2YSF   { return DMR2YSF{} }
func DefaultYSF2NXDN() YSF2NXDN { return YSF2NXDN{} }
func DefaultDMR2NXDN() DMR2NXDN { return DMR2NXDN{} }
func DefaultNXDN2DMR() NXDN2DMR { return NXDN2DMR{} }

func ysf2dmrFromINI(ini *INI) YSF2DMR {
	if ini == nil {
		return DefaultYSF2DMR()
	}
	return YSF2DMR{
		Enable:   true,
		DMRId:    ini.Get("DMR Network", "Id"),
		Master:   ini.Get("DMR Network", "Address"),
		Password: ini.Get("DMR Network", "Password"),
		Options:  ini.Get("DMR Network", "Options"),
		TG:       ini.Get("DMR Network", "StartupDstId"),
	}
}

func dmr2ysfFromINI(ini *INI) DMR2YSF {
	if ini == nil {
		return DefaultDMR2YSF()
	}
	return DMR2YSF{
		Enable:    true,
		DMRId:     ini.Get("DMR Network", "Id"),
		DefaultTG: ini.Get("DMR Network", "DefaultDstTG"),
	}
}

func ysf2nxdnFromINI(ini *INI) YSF2NXDN {
	if ini == nil {
		return DefaultYSF2NXDN()
	}
	return YSF2NXDN{
		Enable: true,
		NXDNId: ini.Get("NXDN Network", "Id"),
		TG:     ini.Get("NXDN Network", "StartupDstId"),
	}
}

func dmr2nxdnFromINI(ini *INI) DMR2NXDN {
	if ini == nil {
		return DefaultDMR2NXDN()
	}
	return DMR2NXDN{
		Enable: true,
		DMRId:  ini.Get("DMR Network", "Id"),
		NXDNId: ini.Get("NXDN Network", "DefaultID"),
	}
}

func nxdn2dmrFromINI(ini *INI) NXDN2DMR {
	if ini == nil {
		return DefaultNXDN2DMR()
	}
	return NXDN2DMR{
		Enable:   true,
		DMRId:    ini.Get("DMR Network", "Id"),
		Master:   ini.Get("DMR Network", "Address"),
		Password: ini.Get("DMR Network", "Password"),
		Options:  ini.Get("DMR Network", "Options"),
		TG:       ini.Get("DMR Network", "StartupDstId"),
		NXDNTG:   ini.Get("NXDN Network", "TG"),
	}
}

// DefaultDisplay is the display-free default matching Waypoint's own node:
// Display=None (status is served over MQTT, not a physical panel). The per-driver
// fields carry the stock MMDVM-Host.ini defaults so a clone that switches the
// type on gets sane values — HD44780 2×16 over the conventional 0x20 PCF8574 I2C
// address, OLED type 3, Nextion on the G4KLX layout, port on the modem. Used to
// seed a fresh store and to backfill the section on a store created before the
// Display surface existed.
func DefaultDisplay() Display {
	return Display{
		Type: "None", OLEDType: "3", Port: "modem", NextionLayout: "0",
		HD44780Rows: "2", HD44780Cols: "16", HD44780I2CAddr: "0x20",
	}
}

// DefaultLCD is the native LCD driver's default: disabled, wired for the common
// case (a PCF8574-backpack HD44780 at 0x27 on /dev/i2c-1, 20×4), with a starter
// page set so a first-time operator who enables it sees something immediately.
// Every starter page declares at most two lines, so the set is valid on a 20×2
// bench panel and a 20×4 alike (a page must never have more lines than the panel
// has rows — see ValidateLCD). The lines use only grounded tokens
// (docs/design/lcd.md §5). Used to seed a fresh store and to backfill a store
// created before the LCD driver existed.
func DefaultLCD() LCD {
	return LCD{
		Enabled:           false,
		I2CBus:            "/dev/i2c-1",
		I2CAddress:        "0x27",
		Rows:              "4",
		Cols:              "20",
		ScrollSpeed:       "300",
		ActivityInterrupt: true,
		LingerSecs:        "3",
		Pages: []LCDPage{
			{Enabled: true, Name: "Idle", Duration: "8", Lines: []string{
				"{callsign}  {mode}",
				"{freq_rx}  {time}",
			}},
			{Enabled: true, Name: "Activity", Duration: "5", Interrupt: true, Lines: []string{
				"{mode}  {source}",
				"TG {tg}",
			}},
			{Enabled: true, Name: "Network", Duration: "5", Lines: []string{
				"{ip}",
				"{hostname}",
			}},
		},
	}
}

// displayFromINI reconstructs the Display section from an MMDVM-Host INI. The
// per-driver subsections are always present in a Waypoint-rendered file, so each
// field reads directly; a hand-written file missing a key falls back to the
// display-free default for that field.
func displayFromINI(mm *INI) Display {
	return Display{
		Type:           orDefault(mm.Get("General", "Display"), "None"),
		OLEDType:       orDefault(mm.Get("OLED", "Type"), "3"),
		Port:           orDefault(mm.Get("Nextion", "Port"), "modem"),
		NextionLayout:  orDefault(mm.Get("Nextion", "ScreenLayout"), "0"),
		HD44780Rows:    orDefault(mm.Get("HD44780", "Rows"), "2"),
		HD44780Cols:    orDefault(mm.Get("HD44780", "Columns"), "16"),
		HD44780I2CAddr: orDefault(mm.Get("HD44780", "I2CAddress"), "0x20"),
	}
}

// DefaultM17 is the MMDVM-Host [M17] default: CAN 0 (Channel Access Number,
// decimal), TXHang 5, and the restrictive/advanced flags off — matching the
// forked MMDVM-Host's own member initializers (Conf.cpp: m_m17CAN(0U),
// m_m17SelfOnly(false), m_m17AllowEncryption(false), m_m17TXHang(5U)). M17 has no
// RemoteGateway. Used to backfill a store seeded before M17.
func DefaultM17() M17 {
	return M17{CAN: "0", SelfOnly: false, AllowEncryption: false, TXHang: "5"}
}

// DefaultM17Gateway is the sane hotspot default: node-type suffix H (hotspot; the
// M17Gateway.ini offers H for hotspots, R for repeaters), no startup reflector
// (don't auto-link on boot), voice announcements on, upstream network hang. Used
// to seed a fresh store and to backfill the section on a store created before
// M17 existed.
func DefaultM17Gateway() M17Gateway {
	return M17Gateway{
		Suffix: "H", Startup: "", Revert: true, HangTime: "240", Voice: true,
	}
}

// m17GatewayFromINI reads an M17Gateway.ini, or returns the defaults when mg is nil.
func m17GatewayFromINI(mg *INI) M17Gateway {
	if mg == nil {
		return DefaultM17Gateway()
	}
	return M17Gateway{
		Suffix:   orDefault(mg.Get("General", "Suffix"), "H"),
		Startup:  mg.Get("Network", "Startup"),
		Revert:   mg.Bool("Network", "Revert"),
		HangTime: orDefault(mg.Get("Network", "HangTime"), "240"),
		Voice:    mg.Bool("Voice", "Enabled"),
	}
}

// DefaultPOCSAG is the sane paging default: the common WPSD paging channel
// (439.9875 MHz), the RWTH DAPNET core server, and no login credentials/filters
// yet (the operator fills in a DAPNET callsign + AuthKey to actually connect —
// DAPNETGateway won't start with an unconfigured key). Callsign is left blank so
// it renders as the station callsign. Used to seed a fresh store and to backfill
// the section on a store created before POCSAG existed.
func DefaultPOCSAG() POCSAG {
	return POCSAG{
		Frequency: "439987500", Server: "dapnet.afu.rwth-aachen.de",
		Callsign: "", AuthKey: "", Whitelist: "", Blacklist: "",
	}
}

// pocsagFromINI reconstructs the POCSAG section: Frequency (and the mode enable,
// read separately in fromINI) come from MMDVM-Host's [POCSAG] section; the DAPNET
// login/filter fields come from DAPNETGateway.ini (dpg), or the defaults when dpg
// is nil (no gateway file at seed time). Callsign is read verbatim — a
// Waypoint-rendered file carries the resolved callsign, and the store is
// authoritative afterward.
func pocsagFromINI(mm, dpg *INI) POCSAG {
	p := DefaultPOCSAG()
	p.Frequency = orDefault(mm.Get("POCSAG", "Frequency"), "439987500")
	if dpg != nil {
		p.Server = orDefault(dpg.Get("DAPNET", "Address"), "dapnet.afu.rwth-aachen.de")
		p.Callsign = dpg.Get("General", "Callsign")
		p.AuthKey = dpg.Get("DAPNET", "AuthKey")
		p.Whitelist = dpg.Get("General", "WhiteList")
		p.Blacklist = dpg.Get("General", "BlackList")
	}
	return p
}

// DefaultFM is the MMDVM-Host [FM] default for the modeled operator keys, matching
// the pinned g4klx MMDVM-Host.ini: CTCSS 88.4 Hz, transmit timeout 180 s (the
// daemon default; the ini leaves Timeout commented), no kerchunk hold, unity
// audio boosts, and access mode 1 (CTCSS-only access without COS). Used to
// backfill a store seeded before FM.
func DefaultFM() FM {
	return FM{
		CTCSS: "88.4", Timeout: "180", KerchunkTime: "0",
		RFAudioBoost: "1", ExtAudioBoost: "1", AccessMode: "1",
	}
}

// fmFromINI reads the modeled [FM] operator keys from an MMDVM-Host INI, falling
// back to the pinned-ini defaults for any key a hand-written file omits (the rest
// of the large [FM] block is MMDVM-Host's own defaults, not modeled).
func fmFromINI(mm *INI) FM {
	return FM{
		CTCSS:         orDefault(mm.Get("FM", "CTCSSFrequency"), "88.4"),
		Timeout:       orDefault(mm.Get("FM", "Timeout"), "180"),
		KerchunkTime:  orDefault(mm.Get("FM", "KerchunkTime"), "0"),
		RFAudioBoost:  orDefault(mm.Get("FM", "RFAudioBoost"), "1"),
		ExtAudioBoost: orDefault(mm.Get("FM", "ExtAudioBoost"), "1"),
		AccessMode:    orDefault(mm.Get("FM", "AccessMode"), "1"),
	}
}

// DefaultYSFGateway is the sane duplex default (suffix RPT, both reflector
// networks on, no startup room). Used to seed a fresh store and to backfill the
// section on a store created before YSF existed. The DG-ID additions default off
// (run the classic YSFGateway, no DG-ID daemon, no hostlist uppercasing).
func DefaultYSFGateway() YSFGateway {
	return YSFGateway{
		// WiresXPassthrough MUST default off: with it on, YSFGateway does not
		// handle the radio's Wires-X commands locally (browse/connect all
		// return NONE), so the radio gets no response. Passthrough is the
		// advanced "hand Wires-X to the network reflector" mode.
		Suffix: "RPT", WiresXPassthrough: false,
		Revert: true, InactivityTimeout: "30",
		YSFNetwork: true, FCSNetwork: true, APRS: false,
		EnableDGId: false, YCSNetwork: false, UpperHostfiles: false,
	}
}

// ysfGatewayFromINI reads the System Fusion gateway config. When dgid is non-nil
// the node runs the DG-ID daemon, so it is read from DGIdGateway.ini; otherwise
// from YSFGateway.ini (yg), or the defaults when both are nil.
func ysfGatewayFromINI(yg, dgid *INI) YSFGateway {
	if dgid != nil {
		return dgidGatewayFromINI(dgid)
	}
	if yg == nil {
		return DefaultYSFGateway()
	}
	return YSFGateway{
		Suffix:            orDefault(yg.Get("General", "Suffix"), "RPT"),
		WiresXPassthrough: yg.Bool("General", "WiresXCommandPassthrough"),
		Startup:           yg.Get("Network", "Startup"),
		Revert:            yg.Bool("Network", "Revert"),
		InactivityTimeout: orDefault(yg.Get("Network", "InactivityTimeout"), "30"),
		YSFNetwork:        yg.Bool("YSF Network", "Enable"),
		FCSNetwork:        yg.Bool("FCS Network", "Enable"),
		APRS:              yg.Bool("APRS", "Enable"),
	}
}

// dgidGatewayFromINI reconstructs the YSFGateway section from a DGIdGateway.ini:
// EnableDGId is implied by the daemon's presence, and the startup reflector +
// YCSNetwork flag are recovered from the generated static DG-ID network block
// (DGId=5). DGIdGateway.ini does not carry the YSFGateway-only knobs
// (WiresXPassthrough, Revert/InactivityTimeout, the YSF/FCS network enables), so
// those keep their store defaults — the store, not the file, is authoritative for
// the inactive daemon's settings (RFC-0001).
func dgidGatewayFromINI(d *INI) YSFGateway {
	g := DefaultYSFGateway()
	g.EnableDGId = true
	g.Suffix = orDefault(d.Get("General", "Suffix"), "RPT")
	g.APRS = d.Bool("APRS", "Enable")
	if name := d.Get("DGId="+dgidStartupDGId, "Name"); name != "" {
		g.Startup = name
		g.YCSNetwork = true
	}
	return g
}

// DefaultP25 is the MMDVM-Host [P25] default: NAC 293 (the common hex default),
// TXHang 5, and every restrictive/advanced flag off — matching MMDVM-Host's own
// member initializers (Conf.cpp). Used to backfill a store seeded before P25.
func DefaultP25() P25 {
	return P25{NAC: "293", SelfOnly: false, OverrideUIDCheck: false, RemoteGateway: false, TXHang: "5"}
}

// DefaultP25Gateway is the sane hotspot default: no startup/static TG (don't
// auto-link the user anywhere on boot), voice announcements on, upstream hang
// timers. Used to seed a fresh store and to backfill the section on a store
// created before P25 existed.
func DefaultP25Gateway() P25Gateway {
	return P25Gateway{
		Static: "", Voice: true, RFHangTime: "120", NetHangTime: "60",
	}
}

// p25GatewayFromINI reads a P25Gateway.ini, or returns the defaults when pg is nil.
func p25GatewayFromINI(pg *INI) P25Gateway {
	if pg == nil {
		return DefaultP25Gateway()
	}
	return P25Gateway{
		Static:      pg.Get("Network", "Static"),
		Voice:       pg.Bool("Voice", "Enabled"),
		RFHangTime:  orDefault(pg.Get("Network", "RFHangTime"), "120"),
		NetHangTime: orDefault(pg.Get("Network", "NetHangTime"), "60"),
	}
}

// DefaultNXDN is the MMDVM-Host [NXDN] default: RAN 1 (decimal Radio Access
// Number), TXHang 5, and every restrictive/advanced flag off — matching
// MMDVM-Host's own member initializers (Conf.cpp). Used to backfill a store
// seeded before NXDN.
func DefaultNXDN() NXDN {
	return NXDN{RAN: "1", SelfOnly: false, RemoteGateway: false, TXHang: "5"}
}

// DefaultNXDNGateway is the sane hotspot default: no startup/static TG (don't
// auto-link the user anywhere on boot), voice announcements on, upstream hang
// timers. Used to seed a fresh store and to backfill the section on a store
// created before NXDN existed.
func DefaultNXDNGateway() NXDNGateway {
	return NXDNGateway{
		Static: "", Voice: true, RFHangTime: "120", NetHangTime: "60",
	}
}

// nxdnGatewayFromINI reads an NXDNGateway.ini, or returns the defaults when ng is nil.
func nxdnGatewayFromINI(ng *INI) NXDNGateway {
	if ng == nil {
		return DefaultNXDNGateway()
	}
	return NXDNGateway{
		Static:      ng.Get("Network", "Static"),
		Voice:       ng.Bool("Voice", "Enabled"),
		RFHangTime:  orDefault(ng.Get("Network", "RFHangTime"), "120"),
		NetHangTime: orDefault(ng.Get("Network", "NetHangTime"), "60"),
	}
}

// DefaultDStar is the MMDVM-Host [D-Star] default: Module B (the common 70cm
// hotspot band letter; upstream's own initializer is C, but the module letter
// only needs to match the gateway repeater Band, and B is the conventional
// choice), and both restrictive flags off. RemoteGateway stays off so the local
// DStarGateway keeps control (DStarControl.cpp:741 rewrites RPT calls only when
// on). Used to backfill a store seeded before D-Star.
func DefaultDStar() DStar {
	return DStar{Module: "B", SelfOnly: false, RemoteGateway: false}
}

// DefaultDStarGateway is the sane hotspot default: openquad ircDDB for callsign
// routing, no startup reflector, and all four reflector protocols on — matching
// DStarGateway's own initializers (DExtra/DPlus/DCS/XLX all default Enabled=true,
// DStarGatewayConfig.cpp:119/126/138/105). IRCDDBUsername/DPlusLogin are left
// blank so they render as the station callsign (upstream's own default). Used to
// seed a fresh store and to backfill the section on a store created before
// D-Star existed.
func DefaultDStarGateway() DStarGateway {
	return DStarGateway{
		Reflector: "", ReflectorReconnect: "Never",
		IRCDDBHostname: "ircv4.openquad.net", IRCDDBUsername: "", IRCDDBPassword: "",
		Dextra: true, DPlus: true, DPlusLogin: "", DCS: true, XLX: true,
	}
}

// dstarGatewayFromINI reads a dstargateway.cfg, or returns the defaults when xg
// is nil. Enable flags are read from each protocol section; the ircDDB/repeater
// values come from the first (only) instance Waypoint renders.
func dstarGatewayFromINI(xg *INI) DStarGateway {
	if xg == nil {
		return DefaultDStarGateway()
	}
	return DStarGateway{
		Reflector:          xg.Get("Repeater 1", "Reflector"),
		ReflectorReconnect: orDefault(xg.Get("Repeater 1", "ReflectorReconnect"), "Never"),
		IRCDDBHostname:     orDefault(xg.Get("IRCDDB 1", "Hostname"), "ircv4.openquad.net"),
		IRCDDBUsername:     xg.Get("IRCDDB 1", "Username"),
		IRCDDBPassword:     xg.Get("IRCDDB 1", "Password"),
		Dextra:             xg.Bool("Dextra", "Enabled"),
		DPlus:              xg.Bool("D-Plus", "Enabled"),
		DPlusLogin:         xg.Get("D-Plus", "Login"),
		DCS:                xg.Bool("DCS", "Enabled"),
		XLX:                xg.Bool("XLX", "Enabled"),
	}
}

// importNetworks seeds the network list from an existing DMRGateway.ini (one
// time; the store is authoritative afterward). It classifies each network by
// address/name and adopts the clean WPSD type only when the file's routing
// already matches what Waypoint would regenerate — otherwise the operator's
// verbatim lines are preserved as a "custom" network so no hand-tuned routing is
// lost (best-effort + preserve). Per-TG DMRRoute overrides are a Waypoint-native
// concept and are not reverse-engineered from the file.
func importNetworks(dg *INI, dmrID string) []Network {
	var nets []Network
	for n := 1; n <= 8; n++ {
		sec := fmt.Sprintf("DMR Network %d", n)
		addr := dg.Get(sec, "Address")
		if addr == "" {
			continue
		}
		name := firstNonEmpty(dg.Get(sec, "Name"), sec)
		raw := dg.Matching(sec, "Rewrite", "PassAll")
		net := Network{
			Name:     name,
			Address:  addr,
			Port:     orDefault(dg.Get(sec, "Port"), "62031"),
			Password: dg.Get(sec, "Password"),
			Options:  dg.Get(sec, "Options"),
			ESSID:    strings.TrimPrefix(dg.Get(sec, "Id"), dmrID), // the Id's suffix past the base DMR ID
			Enabled:  dg.Get(sec, "Enabled") != "0",
			Primary:  len(dg.Matching(sec, "PassAll")) > 0,
		}
		switch t := classifyNetwork(name, addr); {
		case dg.Get(sec, "WPSD_AutoRewrites") == "1":
			net.Type = NetCustom // custom host with auto-generated prefix-9 routing
			net.AutoRewrite = true
		case t != NetCustom && sameRewrites(raw, networkRewrites(Network{Type: t, Primary: net.Primary}, nil)):
			net.Type = t // clean, standard routing → store as a typed network
		default:
			net.Type = NetCustom // unrecognized or hand-tuned → preserve verbatim
			net.Rewrites = raw
		}
		nets = append(nets, net)
	}
	if xlx := importXLX(dg); xlx != nil {
		nets = append(nets, *xlx)
	}
	return nets
}

// classifyNetwork guesses a network's WPSD type from its name and address.
// Custom hosts (FreeDMR/HB-Link and other DMR+-family servers) fold into
// NetDMRPlus per the WPSD prefix table. NetCustom means "could not classify".
func classifyNetwork(name, addr string) NetworkType {
	s := strings.ToLower(name + " " + addr)
	switch {
	case strings.Contains(s, "brandmeister") || strings.Contains(s, "bm_"):
		return NetBrandmeister
	case strings.Contains(s, "tgif"):
		return NetTGIF
	case strings.Contains(s, "systemx") || strings.Contains(s, "system-x") || strings.Contains(s, "system_x"):
		return NetSystemX
	case strings.Contains(s, "dmr2ysf") || strings.Contains(s, "dmr2nxdn") || strings.Contains(s, "cross-over") || strings.Contains(s, "crossover"):
		return NetDMR2YSF
	case strings.Contains(s, "freedmr") || strings.Contains(s, "hblink") || strings.Contains(s, "hb-link") ||
		strings.Contains(s, "dmrplus") || strings.Contains(s, "dmr+") || strings.Contains(s, "phoenix"):
		return NetDMRPlus
	default:
		return NetCustom
	}
}

// sameRewrites reports whether two rewrite-line sets are equal ignoring order
// (dg.Matching returns them sorted; the generator returns them in template
// order), so a standard file is recognized as its clean type.
func sameRewrites(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	as := append([]string(nil), a...)
	bs := append([]string(nil), b...)
	sort.Strings(as)
	sort.Strings(bs)
	for i := range as {
		if as[i] != bs[i] {
			return false
		}
	}
	return true
}

// importXLX seeds an XLX network from the dedicated [XLX Network] section, if
// present. The startup reflector number maps to Address, the module letter to
// Options — the inverse of RenderDMRGateway's XLX block.
func importXLX(dg *INI) *Network {
	sec := "XLX Network"
	if dg.Get(sec, "Startup") == "" && dg.Get(sec, "Module") == "" && dg.Get(sec, "Enabled") == "" {
		return nil
	}
	return &Network{
		Name:       "XLX",
		Type:       NetXLX,
		Port:       orDefault(dg.Get(sec, "Port"), "62030"),
		Password:   dg.Get(sec, "Password"),
		Enabled:    dg.Get(sec, "Enabled") != "0",
		XLXStartup: dg.Get(sec, "Startup"),
		XLXModule:  dg.Get(sec, "Module"),
		XLXSlot:    dg.Get(sec, "Slot"),
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
