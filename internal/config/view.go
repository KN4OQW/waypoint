package config

// View is the settings page's read model: the Model projected for the API, with
// secrets removed. Passwords never appear — a network reports only whether one
// is set. This is what GET /api/config returns.
type View struct {
	Sources  Sources       `json:"sources"`
	General  ViewGeneral   `json:"general"`
	DMR      ViewDMR       `json:"dmr"`
	Modes    []ViewMode    `json:"modes"`
	Networks []ViewNetwork `json:"networks"`
	ReadOnly bool          `json:"read_only"`
}

type Sources struct {
	Store string `json:"store"`
}

type ViewGeneral struct {
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

type ViewDMR struct {
	Enable         bool   `json:"enable"`
	ColorCode      string `json:"color_code"`
	Slot1          bool   `json:"slot1"`
	Slot2          bool   `json:"slot2"`
	EmbeddedLCOnly bool   `json:"embedded_lc_only"`
	ID             string `json:"id"`
}

type ViewMode struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

type ViewNetwork struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	Port        string `json:"port"`
	Enabled     bool   `json:"enabled"`
	HasPassword bool   `json:"has_password"`
}

// modeDisplay maps a mode key to its display name and its Modes-struct value.
var modeDisplay = []struct {
	key, name string
	get       func(Modes) bool
}{
	{"dstar", "D-Star", func(m Modes) bool { return m.DStar }},
	{"dmr", "DMR", func(m Modes) bool { return m.DMR }},
	{"ysf", "System Fusion", func(m Modes) bool { return m.YSF }},
	{"p25", "P25", func(m Modes) bool { return m.P25 }},
	{"nxdn", "NXDN", func(m Modes) bool { return m.NXDN }},
	{"m17", "M17", func(m Modes) bool { return m.M17 }},
	{"pocsag", "POCSAG", func(m Modes) bool { return m.POCSAG }},
	{"fm", "FM", func(m Modes) bool { return m.FM }},
}

// View projects the Model onto the redacted API shape.
func (m *Model) View(storePath string) *View {
	v := &View{
		Sources:  Sources{Store: storePath},
		ReadOnly: false, // store + apply are wired end to end; the page edits
		General: ViewGeneral{
			Callsign:  m.General.Callsign,
			DMRID:     m.General.ID,
			Duplex:    m.General.Duplex,
			RXFreqHz:  m.Modem.RXFreqHz,
			TXFreqHz:  m.Modem.TXFreqHz,
			ModemPort: m.Modem.Port,
			Power:     m.General.Power,
			RXOffset:  m.Modem.RXOffset,
			TXOffset:  m.Modem.TXOffset,
			Location:  m.General.Location,
			URL:       m.General.URL,
		},
		DMR: ViewDMR{
			Enable:         m.Modes.DMR,
			ColorCode:      m.DMR.ColorCode,
			Slot1:          m.DMRNet.Slot1,
			Slot2:          m.DMRNet.Slot2,
			EmbeddedLCOnly: m.DMR.EmbeddedLCOnly,
			ID:             m.DMR.ID,
		},
	}
	for _, md := range modeDisplay {
		v.Modes = append(v.Modes, ViewMode{Key: md.key, Name: md.name, Enabled: md.get(m.Modes)})
	}
	for _, n := range m.Networks {
		v.Networks = append(v.Networks, ViewNetwork{
			Name:        n.Name,
			Address:     n.Address,
			Port:        n.Port,
			Enabled:     n.Enabled,
			HasPassword: n.Password != "",
		})
	}
	return v
}
