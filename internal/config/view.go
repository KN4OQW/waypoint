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
	YSF      ViewYSF       `json:"ysf"`
	P25      ViewP25       `json:"p25"`
	NXDN     ViewNXDN      `json:"nxdn"`
	DStar    ViewDStar     `json:"dstar"`
	ReadOnly bool          `json:"read_only"`
}

// ViewDStar is the D-Star tab's read model: the mode enable, the [D-Star] mode
// params, and the gateway settings a user actually sets. The ircDDB password is
// a secret — never serialized; HasIRCDDBPassword reports only whether one is set
// (the write path preserves it when the field is left blank).
type ViewDStar struct {
	Enable             bool   `json:"enable"`
	Module             string `json:"module"`
	SelfOnly           bool   `json:"self_only"`
	RemoteGateway      bool   `json:"remote_gateway"`
	Reflector          string `json:"reflector"`
	ReflectorReconnect string `json:"reflector_reconnect"`
	IRCDDBHostname     string `json:"ircddb_hostname"`
	IRCDDBUsername     string `json:"ircddb_username"`
	HasIRCDDBPassword  bool   `json:"has_ircddb_password"`
	Dextra             bool   `json:"dextra"`
	DPlus              bool   `json:"dplus"`
	DPlusLogin         string `json:"dplus_login"`
	DCS                bool   `json:"dcs"`
	XLX                bool   `json:"xlx"`
}

// ViewNXDN is the NXDN tab's read model: the mode enable, the [NXDN] mode
// params, and the gateway settings a user actually sets. No secrets.
type ViewNXDN struct {
	Enable        bool   `json:"enable"`
	RAN           string `json:"ran"`
	SelfOnly      bool   `json:"self_only"`
	RemoteGateway bool   `json:"remote_gateway"`
	Static        string `json:"static"`
	Voice         bool   `json:"voice"`
	RFHangTime    string `json:"rf_hang_time"`
	NetHangTime   string `json:"net_hang_time"`
}

// ViewP25 is the P25 tab's read model: the mode enable, the [P25] mode params,
// and the gateway settings a user actually sets. No secrets.
type ViewP25 struct {
	Enable           bool   `json:"enable"`
	NAC              string `json:"nac"`
	SelfOnly         bool   `json:"self_only"`
	OverrideUIDCheck bool   `json:"override_uid_check"`
	RemoteGateway    bool   `json:"remote_gateway"`
	Static           string `json:"static"`
	Voice            bool   `json:"voice"`
	RFHangTime       string `json:"rf_hang_time"`
	NetHangTime      string `json:"net_hang_time"`
}

// ViewYSF is the System Fusion tab's read model: the mode enable plus the
// gateway settings a user actually sets.
type ViewYSF struct {
	Enable            bool   `json:"enable"`
	Suffix            string `json:"suffix"`
	WiresXPassthrough bool   `json:"wiresx_passthrough"`
	Startup           string `json:"startup"`
	Revert            bool   `json:"revert"`
	InactivityTimeout string `json:"inactivity_timeout"`
	YSFNetwork        bool   `json:"ysf_network"`
	FCSNetwork        bool   `json:"fcs_network"`
	APRS              bool   `json:"aprs"`
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
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	Port        string   `json:"port"`
	Enabled     bool     `json:"enabled"`
	HasPassword bool     `json:"has_password"`
	Rewrites    []string `json:"rewrites"` // not secret; editable
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
	v.YSF = ViewYSF{
		Enable:            m.Modes.YSF,
		Suffix:            m.YSFGW.Suffix,
		WiresXPassthrough: m.YSFGW.WiresXPassthrough,
		Startup:           m.YSFGW.Startup,
		Revert:            m.YSFGW.Revert,
		InactivityTimeout: m.YSFGW.InactivityTimeout,
		YSFNetwork:        m.YSFGW.YSFNetwork,
		FCSNetwork:        m.YSFGW.FCSNetwork,
		APRS:              m.YSFGW.APRS,
	}
	v.P25 = ViewP25{
		Enable:           m.Modes.P25,
		NAC:              m.P25.NAC,
		SelfOnly:         m.P25.SelfOnly,
		OverrideUIDCheck: m.P25.OverrideUIDCheck,
		RemoteGateway:    m.P25.RemoteGateway,
		Static:           m.P25GW.Static,
		Voice:            m.P25GW.Voice,
		RFHangTime:       m.P25GW.RFHangTime,
		NetHangTime:      m.P25GW.NetHangTime,
	}
	v.NXDN = ViewNXDN{
		Enable:        m.Modes.NXDN,
		RAN:           m.NXDN.RAN,
		SelfOnly:      m.NXDN.SelfOnly,
		RemoteGateway: m.NXDN.RemoteGateway,
		Static:        m.NXDNGW.Static,
		Voice:         m.NXDNGW.Voice,
		RFHangTime:    m.NXDNGW.RFHangTime,
		NetHangTime:   m.NXDNGW.NetHangTime,
	}
	v.DStar = ViewDStar{
		Enable:             m.Modes.DStar,
		Module:             m.DStar.Module,
		SelfOnly:           m.DStar.SelfOnly,
		RemoteGateway:      m.DStar.RemoteGateway,
		Reflector:          m.DStarGW.Reflector,
		ReflectorReconnect: m.DStarGW.ReflectorReconnect,
		IRCDDBHostname:     m.DStarGW.IRCDDBHostname,
		IRCDDBUsername:     m.DStarGW.IRCDDBUsername,
		HasIRCDDBPassword:  m.DStarGW.IRCDDBPassword != "",
		Dextra:             m.DStarGW.Dextra,
		DPlus:              m.DStarGW.DPlus,
		DPlusLogin:         m.DStarGW.DPlusLogin,
		DCS:                m.DStarGW.DCS,
		XLX:                m.DStarGW.XLX,
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
			Rewrites:    n.Rewrites,
		})
	}
	return v
}
