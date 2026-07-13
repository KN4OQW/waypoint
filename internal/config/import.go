package config

import (
	"fmt"
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
	// No YSFGateway/P25Gateway/NXDNGateway/dstargateway.cfg/M17Gateway.ini exists
	// at seed time (waypointd creates them); those sections get defaults.
	// yg/pg/ng/xg/mg are non-nil only in the round-trip harness.
	return fromINI(mm, dg, nil, nil, nil, nil, nil), nil
}

// fromINI builds a Model from already-parsed INIs. Shared by Import (from disk)
// and the round-trip harness (render → parse → fromINI, all in memory). The
// gateway INIs yg (YSFGateway), pg (P25Gateway), ng (NXDNGateway), xg
// (dstargateway.cfg) and mg (M17Gateway) may be nil, in which case their
// sections take their defaults.
func fromINI(mm, dg, yg, pg, ng, xg, mg *INI) *Model {
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
		DMR: DMR{
			ColorCode:      orDefault(mm.Get("DMR", "ColorCode"), "1"),
			ID:             firstNonEmpty(mm.Get("DMR", "Id"), mm.Get("General", "Id")),
			EmbeddedLCOnly: mm.Bool("DMR", "EmbeddedLCOnly"),
			SelfOnly:       mm.Bool("DMR", "SelfOnly"),
			DumpTAData:     mm.Bool("DMR", "DumpTAData"),
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
		Networks: importNetworks(dg),
		YSF: YSF{
			LowDeviation:  mm.Bool("System Fusion", "LowDeviation"),
			SelfOnly:      mm.Bool("System Fusion", "SelfOnly"),
			TXHang:        orDefault(mm.Get("System Fusion", "TXHang"), "4"),
			RemoteGateway: mm.Bool("System Fusion", "RemoteGateway"),
			ModeHang:      orDefault(mm.Get("System Fusion", "ModeHang"), "20"),
		},
		YSFGW: ysfGatewayFromINI(yg),
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
		M17GW: m17GatewayFromINI(mg),
	}
	return m
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

// DefaultYSFGateway is the sane duplex default (suffix RPT, both reflector
// networks on, no startup room). Used to seed a fresh store and to backfill the
// section on a store created before YSF existed.
func DefaultYSFGateway() YSFGateway {
	return YSFGateway{
		// WiresXPassthrough MUST default off: with it on, YSFGateway does not
		// handle the radio's Wires-X commands locally (browse/connect all
		// return NONE), so the radio gets no response. Passthrough is the
		// advanced "hand Wires-X to the network reflector" mode.
		Suffix: "RPT", WiresXPassthrough: false,
		Revert: true, InactivityTimeout: "30",
		YSFNetwork: true, FCSNetwork: true, APRS: false,
	}
}

// ysfGatewayFromINI reads a YSFGateway.ini, or returns the defaults when yg is nil.
func ysfGatewayFromINI(yg *INI) YSFGateway {
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

func importNetworks(dg *INI) []Network {
	var nets []Network
	for n := 1; n <= 8; n++ {
		sec := fmt.Sprintf("DMR Network %d", n)
		addr := dg.Get(sec, "Address")
		if addr == "" {
			continue
		}
		nets = append(nets, Network{
			Name:     firstNonEmpty(dg.Get(sec, "Name"), sec),
			Address:  addr,
			Port:     orDefault(dg.Get(sec, "Port"), "62031"),
			Password: dg.Get(sec, "Password"),
			Enabled:  dg.Get(sec, "Enabled") != "0",
			Rewrites: dg.Prefixed(sec, "Rewrite"),
		})
	}
	return nets
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
