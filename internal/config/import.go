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
	// No YSFGateway.ini exists at seed time (waypointd creates it); YSFGW gets
	// defaults. yg is non-nil only in the round-trip harness.
	return fromINI(mm, dg, nil), nil
}

// fromINI builds a Model from already-parsed INIs. Shared by Import (from disk)
// and the round-trip harness (render → parse → fromINI, all in memory). yg (the
// YSFGateway INI) may be nil, in which case YSFGW takes its defaults.
func fromINI(mm, dg, yg *INI) *Model {
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
	}
	return m
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
		Suffix: "RPT", WiresXPassthrough: false, WiresXMakeUpper: true,
		Reconnect: true, Revert: true, InactivityTimeout: "30",
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
		WiresXMakeUpper:   yg.Bool("General", "WiresXMakeUpper"),
		Startup:           yg.Get("Network", "Startup"),
		Reconnect:         yg.Bool("Network", "Reconnect"),
		Revert:            yg.Bool("Network", "Revert"),
		InactivityTimeout: orDefault(yg.Get("Network", "InactivityTimeout"), "30"),
		YSFNetwork:        yg.Bool("YSF Network", "Enable"),
		FCSNetwork:        yg.Bool("FCS Network", "Enable"),
		APRS:              yg.Bool("APRS", "Enable"),
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
