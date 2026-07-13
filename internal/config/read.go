package config

import (
	"fmt"
	"sort"
	"strings"
)

// View is the dashboard-facing projection of the node's configuration, grouped
// to match the settings page's tabs. Secrets are never included — only a flag
// indicating whether one is set.
type View struct {
	Sources  Sources           `json:"sources"`
	General  General           `json:"general"`
	DMR      DMR               `json:"dmr"`
	Modes    []Mode            `json:"modes"`
	Networks []Network         `json:"networks"`
	ReadOnly bool              `json:"read_only"` // true until the write path (waypoint#1) lands
	Errors   map[string]string `json:"errors,omitempty"`
}

type Sources struct {
	MMDVM      string `json:"mmdvm"`
	DMRGateway string `json:"dmrgateway"`
}

type General struct {
	Callsign  string `json:"callsign"`
	DMRID     string `json:"dmr_id"`
	Duplex    bool   `json:"duplex"`
	RXFreqHz  string `json:"rx_freq_hz"`
	TXFreqHz  string `json:"tx_freq_hz"`
	ModemPort string `json:"modem_port"`
	Power     string `json:"power"`
	RXOffset  string `json:"rx_offset"`
	TXOffset  string `json:"tx_offset"`
	Location  string `json:"location"`
	URL       string `json:"url"`
}

type DMR struct {
	Enable         bool   `json:"enable"`
	ColorCode      string `json:"color_code"`
	Slot1          bool   `json:"slot1"`
	Slot2          bool   `json:"slot2"`
	EmbeddedLCOnly bool   `json:"embedded_lc_only"`
	ID             string `json:"id"`
}

type Mode struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type Network struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        string `json:"port"`
	Enabled     bool   `json:"enabled"`
	HasPassword bool   `json:"has_password"`
}

// modeSections maps a display name to its MMDVM-Host [Section].
var modeSections = []struct{ key, name, section string }{
	{"dstar", "D-Star", "D-Star"},
	{"dmr", "DMR", "DMR"},
	{"ysf", "System Fusion", "System Fusion"},
	{"p25", "P25", "P25"},
	{"nxdn", "NXDN", "NXDN"},
	{"m17", "M17", "M17"},
	{"pocsag", "POCSAG", "POCSAG"},
	{"fm", "FM", "FM"},
}

// Read builds the View from the two INI paths. A missing or unreadable file is
// reported per-source in Errors rather than failing the whole response, so the
// page can still render what it could load.
func Read(mmdvmPath, dmrgatewayPath string) *View {
	v := &View{
		Sources:  Sources{MMDVM: mmdvmPath, DMRGateway: dmrgatewayPath},
		ReadOnly: true,
		Errors:   map[string]string{},
	}

	mm, err := ParseINIFile(mmdvmPath)
	if err != nil {
		v.Errors["mmdvm"] = err.Error()
	} else {
		v.General = General{
			Callsign:  mm.Get("General", "Callsign"),
			DMRID:     mm.Get("General", "Id"),
			Duplex:    mm.Bool("General", "Duplex"),
			RXFreqHz:  mm.Get("Info", "RXFrequency"),
			TXFreqHz:  mm.Get("Info", "TXFrequency"),
			ModemPort: firstNonEmpty(mm.Get("Modem", "UARTPort"), mm.Get("Modem", "Port")),
			Power:     mm.Get("Info", "Power"),
			RXOffset:  mm.Get("Modem", "RXOffset"),
			TXOffset:  mm.Get("Modem", "TXOffset"),
			Location:  mm.Get("Info", "Location"),
			URL:       mm.Get("Info", "URL"),
		}
		v.DMR = DMR{
			Enable:         mm.Bool("DMR", "Enable"),
			ColorCode:      mm.Get("DMR", "ColorCode"),
			Slot1:          mm.Bool("DMR Network", "Slot1"),
			Slot2:          mm.Bool("DMR Network", "Slot2"),
			EmbeddedLCOnly: mm.Bool("DMR", "EmbeddedLCOnly"),
			ID:             mm.Get("DMR", "Id"),
		}
		for _, m := range modeSections {
			v.Modes = append(v.Modes, Mode{Key: m.key, Name: m.name, Enabled: mm.Bool(m.section, "Enable")})
		}
	}

	dg, err := ParseINIFile(dmrgatewayPath)
	if err != nil {
		v.Errors["dmrgateway"] = err.Error()
	} else {
		v.Networks = readNetworks(dg)
	}

	if len(v.Errors) == 0 {
		v.Errors = nil
	}
	return v
}

// readNetworks pulls each [DMR Network N] block from the DMRGateway config.
func readNetworks(dg *INI) []Network {
	var nets []Network
	for n := 1; n <= 8; n++ {
		sec := fmt.Sprintf("DMR Network %d", n)
		if !dg.Has(sec) {
			continue
		}
		addr := dg.Get(sec, "Address")
		if addr == "" {
			continue
		}
		nets = append(nets, Network{
			Name:        firstNonEmpty(dg.Get(sec, "Name"), sec),
			Address:     addr,
			Port:        dg.Get(sec, "Port"),
			Enabled:     dg.Get(sec, "Enabled") != "0", // absent means enabled
			HasPassword: strings.TrimSpace(dg.Get(sec, "Password")) != "",
		})
	}
	sort.SliceStable(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	return nets
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
