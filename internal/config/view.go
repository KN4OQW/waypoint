package config

// View is the settings page's read model: the Model projected for the API, with
// secrets removed. Passwords never appear — a network reports only whether one
// is set. This is what GET /api/config returns.
type View struct {
	Sources Sources     `json:"sources"`
	General ViewGeneral `json:"general"`
	// Modem is the calibration surface (#20) — everything in [Modem] that is not
	// already on the General tab. It is its own projection rather than more
	// fields on ViewGeneral because it is a different kind of thing: General is
	// what the station IS, and this is what the hardware was MEASURED to need.
	Modem    ViewModem     `json:"modem"`
	Display  ViewDisplay   `json:"display"`
	DMR      ViewDMR       `json:"dmr"`
	Modes    []ViewMode    `json:"modes"`
	Networks []ViewNetwork `json:"networks"`
	Routes   []ViewRoute   `json:"routes"`
	YSF      ViewYSF       `json:"ysf"`
	P25      ViewP25       `json:"p25"`
	NXDN     ViewNXDN      `json:"nxdn"`
	DStar    ViewDStar     `json:"dstar"`
	M17      ViewM17       `json:"m17"`
	POCSAG   ViewPOCSAG    `json:"pocsag"`
	FM       ViewFM        `json:"fm"`
	LCD      ViewLCD       `json:"lcd"`
	History  ViewHistory   `json:"history"`
	// StationID shares the Station Settings tab with History.
	StationID ViewStationID `json:"station_id"`
	Update    ViewUpdate    `json:"update"`
	// Mode buses (RFC-0003). Buses and their attachments carry NO secret (a bus
	// authenticates through an existing Networks[] entry named by credentials_ref,
	// never its own master — §3), so they project verbatim; there is nothing to
	// redact. The UI reads these to render the Buses surface and resolves
	// credentials_ref against Networks[] (also in this view).
	Buses       []Bus        `json:"buses"`
	Attachments []Attachment `json:"attachments"`
	// Bus LAN peering (RFC-0016). Peers ARE redacted — the pinned peer certificate
	// and this node's per-peering key are write-only secrets; only the fingerprint
	// is viewable (PeerView). Remote attachments carry no secret and project
	// verbatim.
	Peers             []PeerView         `json:"peers"`
	RemoteAttachments []RemoteAttachment `json:"remote_attachments"`
	// The System tab (#29): the shared MQTT data plane and the per-daemon log
	// levels. MQTT is REDACTED — the broker password is a write-only secret, so
	// ViewMQTT carries HasPassword and never the value itself (D4). Scrubbing
	// happens here, in the projection, not in the browser: a value the server never
	// serializes cannot leak through a cached response, a proxy log, or a curl.
	MQTT    ViewMQTT    `json:"mqtt"`
	Logging ViewLogging `json:"logging"`
	// BlockedGateways names the enabled modes whose gateway apply deliberately does
	// NOT start, because the daemon reads a value at startup that is not set and
	// would exit before opening anything (see gateway_requirements.go). Projected so
	// the UI can say which control to fill in — otherwise the only evidence is a
	// daemon that is not running for no visible reason. Carries field NAMES, never
	// values, so nothing secret is exposed by reporting that a secret is absent.
	BlockedGateways []GatewayRequirement `json:"blocked_gateways"`
	ReadOnly        bool                 `json:"read_only"`
	// The cross-mode transcoding bridges (MMDVM_CM) are no longer projected here.
	// The per-bridge-daemon model is retired for the RFC-0003 bus architecture, so
	// the settings page shows a placeholder instead of bridge cards. The bridge store
	// sections are retained (dormant) — SetCrossBridge/SetSection still accept them
	// and they round-trip through Save/Load — but they have no read-model surface, so
	// nothing is projected. RFC-0003's migration reads them straight from the store.
}

// ViewPOCSAG is the POCSAG tab's read model: the mode enable, the paging channel
// ([POCSAG] Frequency), and the DAPNETGateway settings a user actually sets. The
// DAPNET AuthKey is a secret — never serialized; HasAuthKey reports only whether
// one is set (the write path preserves it when the field is left blank).
type ViewPOCSAG struct {
	Enable     bool   `json:"enable"`
	Frequency  string `json:"frequency"`
	Server     string `json:"server"`
	Callsign   string `json:"callsign"`
	HasAuthKey bool   `json:"has_auth_key"`
	Whitelist  string `json:"whitelist"`
	Blacklist  string `json:"blacklist"`
}

// ViewFM is the FM tab's read model: the mode enable plus the [FM] operator
// parameters. Analog FM has no gateway daemon and no secrets.
type ViewFM struct {
	Enable        bool   `json:"enable"`
	CTCSS         string `json:"ctcss"`
	Timeout       string `json:"timeout"`
	KerchunkTime  string `json:"kerchunk_time"`
	RFAudioBoost  string `json:"rf_audio_boost"`
	ExtAudioBoost string `json:"ext_audio_boost"`
	AccessMode    string `json:"access_mode"`
}

// ViewLCD is the LCD tab's read model: the native HD44780 driver's panel wiring
// and its rotating pages. No secrets — a straight projection of the LCD section.
type ViewLCD struct {
	Enabled           bool          `json:"enabled"`
	I2CBus            string        `json:"i2c_bus"`
	I2CAddress        string        `json:"i2c_address"`
	Rows              string        `json:"rows"`
	Cols              string        `json:"cols"`
	ScrollSpeed       string        `json:"scroll_speed"`
	ActivityInterrupt bool          `json:"activity_interrupt"`
	LingerSecs        string        `json:"linger_secs"`
	Pages             []ViewLCDPage `json:"pages"`
}

// ViewHistory is the Station Settings tab's read model for event-history
// retention (RFC-0004). No secrets — a straight projection of the History section.
type ViewHistory struct {
	RetentionDays int `json:"retention_days"`
}

// ViewStationID is the Station Settings tab's read model for automatic CW
// identification. No secrets — a straight projection of the StationID section.
// EffectiveCallsign is derived, not stored: it resolves the blank-means-inherit
// rule so the UI can show the operator what will actually go out on the air
// without duplicating that logic in JS.
type ViewStationID struct {
	Enable            bool   `json:"enable"`
	TimeMins          string `json:"time_mins"`
	Callsign          string `json:"callsign"`
	EffectiveCallsign string `json:"effective_callsign"`
	TXLevel           string `json:"tx_level"`
}

// ViewUpdate is the Updates tab's read model for the operator update policy
// (RFC-0014). No secrets — a straight projection of the Update section.
type ViewUpdate struct {
	Channel      string `json:"channel"`
	CheckEnabled bool   `json:"check_enabled"`
	AutoApply    bool   `json:"auto_apply"`
	QuietWindow  string `json:"quiet_window"`
}

type ViewLCDPage struct {
	Enabled   bool     `json:"enabled"`
	Name      string   `json:"name"`
	Duration  string   `json:"duration"`
	Interrupt bool     `json:"interrupt"`
	Lines     []string `json:"lines"`
}

// ViewM17 is the M17 tab's read model: the mode enable, the [M17] mode params
// (CAN, no RemoteGateway, AllowEncryption), and the gateway settings a user
// actually sets. No secrets.
type ViewM17 struct {
	Enable          bool   `json:"enable"`
	CAN             string `json:"can"`
	SelfOnly        bool   `json:"self_only"`
	AllowEncryption bool   `json:"allow_encryption"`
	Suffix          string `json:"suffix"`
	Startup         string `json:"startup"`
	Revert          bool   `json:"revert"`
	HangTime        string `json:"hang_time"`
	Voice           bool   `json:"voice"`
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

// ViewYSF is the System Fusion tab's read model: the mode enable, the [System
// Fusion] mode params, and the gateway settings a user actually sets. The five
// mode params (self_only, low_deviation, tx_hang, mode_hang, remote_gateway) are
// stored + rendered into MMDVM-Host's [System Fusion] and are surfaced here for
// parity with P25/NXDN/M17, which expose their equivalents (parity gap G1). No
// secrets.
type ViewYSF struct {
	Enable            bool   `json:"enable"`
	SelfOnly          bool   `json:"self_only"`
	LowDeviation      bool   `json:"low_deviation"`
	TXHang            string `json:"tx_hang"`
	ModeHang          string `json:"mode_hang"`
	RemoteGateway     bool   `json:"remote_gateway"`
	Suffix            string `json:"suffix"`
	WiresXPassthrough bool   `json:"wiresx_passthrough"`
	Startup           string `json:"startup"`
	Revert            bool   `json:"revert"`
	InactivityTimeout string `json:"inactivity_timeout"`
	YSFNetwork        bool   `json:"ysf_network"`
	FCSNetwork        bool   `json:"fcs_network"`
	APRS              bool   `json:"aprs"`
	EnableDGId        bool   `json:"enable_dgid"`
	YCSNetwork        bool   `json:"ycs_network"`
	UpperHostfiles    bool   `json:"upper_hostfiles"`
}

// ViewDisplay is the Setup tab's Display card: the driver type, the shared
// serial port, and the per-driver sub-fields (OLED type, Nextion layout, HD44780
// geometry + I2C address). No secrets. TRX Mode (Simplex/Duplex) is not here — it
// is general.duplex, surfaced by the Setup tab's Control Software card.
type ViewDisplay struct {
	Type           string `json:"type"`
	OLEDType       string `json:"oled_type"`
	Port           string `json:"port"`
	NextionLayout  string `json:"nextion_layout"`
	HD44780Rows    string `json:"hd44780_rows"`
	HD44780Cols    string `json:"hd44780_cols"`
	HD44780I2CAddr string `json:"hd44780_i2c_addr"`
}

// Sources names the deployment-owned locations the settings page displays but
// does not edit: they come from the packaged systemd unit's flags, not the store.
//
// Listen is the waypointd HTTPS listen address, shown read-only on the System tab.
// It is deliberately NOT store-owned (#29 scope amendment): making it live-editable
// would let an operator move the UI out from under the browser doing the edit — the
// worst failure this product has — and the confirm-or-revert machinery that makes
// host networking safe is not worth duplicating for a value nobody changes twice.
type Sources struct {
	Store  string `json:"store"`
	Listen string `json:"listen"`
}

// ViewMQTT is the System tab's read model for the shared data plane. The broker
// password is a write-only secret: it is never serialized, and HasPassword reports
// only whether one is stored (the same rule the DMR network passwords, the ircDDB
// password and the DAPNET AuthKey follow).
type ViewMQTT struct {
	Host         string `json:"host"`
	Port         string `json:"port"`
	Auth         bool   `json:"auth"`
	Username     string `json:"username"`
	HasPassword  bool   `json:"has_password"`
	Name         string `json:"name"`
	StatusPrefix string `json:"status_prefix"`
	BusPrefix    string `json:"bus_prefix"`
}

// ViewLogging is the System tab's read model for the per-daemon log levels. No
// secret; it projects verbatim. m17gateway is the pre-MQTT shape (display + file),
// matching what the pinned M17Gateway actually parses.
type ViewLogging struct {
	MMDVM         LogLevels     `json:"mmdvm"`
	DMRGateway    LogLevels     `json:"dmrgateway"`
	YSFGateway    LogLevels     `json:"ysfgateway"`
	DGIdGateway   LogLevels     `json:"dgidgateway"`
	P25Gateway    LogLevels     `json:"p25gateway"`
	NXDNGateway   LogLevels     `json:"nxdngateway"`
	DStarGateway  LogLevels     `json:"dstargateway"`
	DAPNETGateway LogLevels     `json:"dapnetgateway"`
	M17Gateway    FileLogLevels `json:"m17gateway"`
}

type ViewGeneral struct {
	Callsign  string `json:"callsign"`
	DMRID     string `json:"dmr_id"`
	Duplex    bool   `json:"duplex"`
	RXFreqHz  string `json:"rx_freq_hz"`
	TXFreqHz  string `json:"tx_freq_hz"`
	ModemPort string `json:"modem_port"`
	// The board the operator says is attached, and its reference oscillator
	// (#18). Both may be empty on a node that has never been detected or told.
	ModemBoard  string `json:"modem_board"`
	ModemTCXOHz string `json:"modem_tcxo_hz"`
	UARTSpeed   string `json:"uart_speed"`
	Power       string `json:"power"`
	RXOffset    string `json:"rx_offset"`
	TXOffset    string `json:"tx_offset"`
	Location    string `json:"location"`
	URL         string `json:"url"`
}

// ViewModem projects the calibration keys. Nothing here is a secret, so it is a
// verbatim projection; it exists to keep the editor from having to fetch the
// whole model to render one card.
type ViewModem struct {
	RXLevel    string `json:"rx_level"`
	TXLevel    string `json:"tx_level"`
	RXDCOffset string `json:"rx_dc_offset"`
	TXDCOffset string `json:"tx_dc_offset"`
	RFLevel    string `json:"rf_level"`
	DMRDelay   string `json:"dmr_delay"`
	TXInvert   bool   `json:"tx_invert"`
	RXInvert   bool   `json:"rx_invert"`
	PTTInvert  bool   `json:"ptt_invert"`

	DStarTXLevel  string `json:"dstar_tx_level"`
	DMRTXLevel    string `json:"dmr_tx_level"`
	YSFTXLevel    string `json:"ysf_tx_level"`
	P25TXLevel    string `json:"p25_tx_level"`
	NXDNTXLevel   string `json:"nxdn_tx_level"`
	POCSAGTXLevel string `json:"pocsag_tx_level"`
	FMTXLevel     string `json:"fm_tx_level"`

	RSSIMappingFile string `json:"rssi_mapping_file"`
}

type ViewDMR struct {
	Enable         bool   `json:"enable"`
	ColorCode      string `json:"color_code"`
	Slot1          bool   `json:"slot1"`
	Slot2          bool   `json:"slot2"`
	EmbeddedLCOnly bool   `json:"embedded_lc_only"`
	DumpTAData     bool   `json:"dump_ta_data"`
	Beacons        bool   `json:"beacons"`
	// SelfOnly is WPSD's "Node Lock" moved into the DMR panel: Private (locked to
	// this node's own DMR ID) when true, Public (allows other DMR IDs) when false.
	// It is the single [DMR] SelfOnly bit — the "Node Lock" and "allow other DMR
	// IDs" controls are two framings of the same setting (MMDVM-Host has no
	// multi-ID allowlist), so Waypoint models one field and never a dead key.
	SelfOnly bool   `json:"self_only"`
	ID       string `json:"id"`
}

type ViewMode struct {
	Key     string `json:"key"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// ViewNetwork is the DMR-networks tab's read model. Routing is derived from
// Type + Primary (WPSD-style generation), so no raw rewrite lines are exposed —
// except Rewrites, which is populated only for a "custom" network (the advanced
// escape hatch) and empty otherwise.
// PeerView is the redacted read model for a LAN peer (RFC-0016 §Security posture):
// the pinned peer certificate and this node's per-peering private key are NEVER
// serialized — only whether they are set (HasCertificate/HasKey) and the
// out-of-band-verifiable Fingerprint are exposed, mirroring ViewNetwork's
// has_password treatment.
type PeerView struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Host           string    `json:"host,omitempty"`
	Port           string    `json:"port,omitempty"`
	MDNSInstance   string    `json:"mdns_instance,omitempty"`
	State          PeerState `json:"state"`
	Fingerprint    string    `json:"fingerprint,omitempty"`
	HasCertificate bool      `json:"has_certificate"`
	HasKey         bool      `json:"has_key"`
}

type ViewNetwork struct {
	Name        string      `json:"name"`
	Type        NetworkType `json:"type"`
	Address     string      `json:"address"`
	Port        string      `json:"port"`
	Primary     bool        `json:"primary"`
	Options     string      `json:"options"`
	ESSID       string      `json:"essid"`
	Enabled     bool        `json:"enabled"`
	HasPassword bool        `json:"has_password"`
	AutoRewrite bool        `json:"auto_rewrite"`
	TGListFile  string      `json:"tg_list_file"`
	XLXStartup  string      `json:"xlx_startup"`
	XLXModule   string      `json:"xlx_module"`
	XLXSlot     string      `json:"xlx_slot"`
	Rewrites    []string    `json:"rewrites"` // custom type only; not secret; editable
}

// ViewRoute is one row of the "tie this talkgroup to this gateway" table.
type ViewRoute struct {
	Slot    string `json:"slot"`
	TG      string `json:"tg"`
	Network string `json:"network"`
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
func (m *Model) View(src Sources) *View {
	v := &View{
		Sources:  src,
		ReadOnly: false, // store + apply are wired end to end; the page edits
		General: ViewGeneral{
			Callsign:    m.General.Callsign,
			DMRID:       m.General.ID,
			Duplex:      m.General.Duplex,
			RXFreqHz:    m.Modem.RXFreqHz,
			TXFreqHz:    m.Modem.TXFreqHz,
			ModemPort:   m.Modem.Port,
			ModemBoard:  m.Modem.Board,
			ModemTCXOHz: m.Modem.TCXOHz,
			UARTSpeed:   m.Modem.UARTSpeed,
			Power:       m.General.Power,
			RXOffset:    m.Modem.RXOffset,
			TXOffset:    m.Modem.TXOffset,
			Location:    m.General.Location,
			URL:         m.General.URL,
		},
		Modem: ViewModem{
			RXLevel:         m.Modem.RXLevel,
			TXLevel:         m.Modem.TXLevel,
			RXDCOffset:      m.Modem.RXDCOffset,
			TXDCOffset:      m.Modem.TXDCOffset,
			RFLevel:         m.Modem.RFLevel,
			DMRDelay:        m.Modem.DMRDelay,
			TXInvert:        m.Modem.TXInvert,
			RXInvert:        m.Modem.RXInvert,
			PTTInvert:       m.Modem.PTTInvert,
			DStarTXLevel:    m.Modem.DStarTXLevel,
			DMRTXLevel:      m.Modem.DMRTXLevel,
			YSFTXLevel:      m.Modem.YSFTXLevel,
			P25TXLevel:      m.Modem.P25TXLevel,
			NXDNTXLevel:     m.Modem.NXDNTXLevel,
			POCSAGTXLevel:   m.Modem.POCSAGTXLevel,
			FMTXLevel:       m.Modem.FMTXLevel,
			RSSIMappingFile: m.Modem.RSSIMappingFile,
		},
		DMR: ViewDMR{
			Enable:         m.Modes.DMR,
			ColorCode:      m.DMR.ColorCode,
			Slot1:          m.DMRNet.Slot1,
			Slot2:          m.DMRNet.Slot2,
			EmbeddedLCOnly: m.DMR.EmbeddedLCOnly,
			DumpTAData:     m.DMR.DumpTAData,
			Beacons:        m.DMR.Beacons,
			SelfOnly:       m.DMR.SelfOnly,
			ID:             m.DMR.ID,
		},
	}
	v.Display = ViewDisplay{
		Type:           m.Display.Type,
		OLEDType:       m.Display.OLEDType,
		Port:           m.Display.Port,
		NextionLayout:  m.Display.NextionLayout,
		HD44780Rows:    m.Display.HD44780Rows,
		HD44780Cols:    m.Display.HD44780Cols,
		HD44780I2CAddr: m.Display.HD44780I2CAddr,
	}
	v.YSF = ViewYSF{
		Enable:            m.Modes.YSF,
		SelfOnly:          m.YSF.SelfOnly,
		LowDeviation:      m.YSF.LowDeviation,
		TXHang:            m.YSF.TXHang,
		ModeHang:          m.YSF.ModeHang,
		RemoteGateway:     m.YSF.RemoteGateway,
		Suffix:            m.YSFGW.Suffix,
		WiresXPassthrough: m.YSFGW.WiresXPassthrough,
		Startup:           m.YSFGW.Startup,
		Revert:            m.YSFGW.Revert,
		InactivityTimeout: m.YSFGW.InactivityTimeout,
		YSFNetwork:        m.YSFGW.YSFNetwork,
		FCSNetwork:        m.YSFGW.FCSNetwork,
		APRS:              m.YSFGW.APRS,
		EnableDGId:        m.YSFGW.EnableDGId,
		YCSNetwork:        m.YSFGW.YCSNetwork,
		UpperHostfiles:    m.YSFGW.UpperHostfiles,
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
	v.M17 = ViewM17{
		Enable:          m.Modes.M17,
		CAN:             m.M17.CAN,
		SelfOnly:        m.M17.SelfOnly,
		AllowEncryption: m.M17.AllowEncryption,
		Suffix:          m.M17GW.Suffix,
		Startup:         m.M17GW.Startup,
		Revert:          m.M17GW.Revert,
		HangTime:        m.M17GW.HangTime,
		Voice:           m.M17GW.Voice,
	}
	v.POCSAG = ViewPOCSAG{
		Enable:     m.Modes.POCSAG,
		Frequency:  m.POCSAG.Frequency,
		Server:     m.POCSAG.Server,
		Callsign:   m.POCSAG.Callsign,
		HasAuthKey: m.POCSAG.AuthKey != "",
		Whitelist:  m.POCSAG.Whitelist,
		Blacklist:  m.POCSAG.Blacklist,
	}
	v.FM = ViewFM{
		Enable:        m.Modes.FM,
		CTCSS:         m.FM.CTCSS,
		Timeout:       m.FM.Timeout,
		KerchunkTime:  m.FM.KerchunkTime,
		RFAudioBoost:  m.FM.RFAudioBoost,
		ExtAudioBoost: m.FM.ExtAudioBoost,
		AccessMode:    m.FM.AccessMode,
	}
	v.BlockedGateways = m.UnmetGatewayRequirements()
	// The cross-mode transcoding bridges are retired (RFC-0003) and no longer
	// projected — their store sections stay dormant (see the View doc comment).
	for _, md := range modeDisplay {
		v.Modes = append(v.Modes, ViewMode{Key: md.key, Name: md.name, Enabled: md.get(m.Modes)})
	}
	for _, n := range m.Networks {
		vn := ViewNetwork{
			Name:        n.Name,
			Type:        n.Type,
			Address:     n.Address,
			Port:        n.Port,
			Primary:     n.Primary,
			Options:     n.Options,
			ESSID:       n.ESSID,
			Enabled:     n.Enabled,
			HasPassword: n.Password != "",
			AutoRewrite: n.AutoRewrite,
			TGListFile:  n.TGListFile,
			XLXStartup:  n.XLXStartup,
			XLXModule:   n.XLXModule,
			XLXSlot:     n.XLXSlot,
		}
		if n.Type == NetCustom || n.Type == "" {
			vn.Rewrites = n.Rewrites // raw lines surface for custom + legacy (untyped) networks
		}
		v.Networks = append(v.Networks, vn)
	}
	for _, r := range m.Routes {
		v.Routes = append(v.Routes, ViewRoute{Slot: r.Slot, TG: r.TG, Network: r.Network})
	}
	v.LCD = ViewLCD{
		Enabled:           m.LCD.Enabled,
		I2CBus:            m.LCD.I2CBus,
		I2CAddress:        m.LCD.I2CAddress,
		Rows:              m.LCD.Rows,
		Cols:              m.LCD.Cols,
		ScrollSpeed:       m.LCD.ScrollSpeed,
		ActivityInterrupt: m.LCD.ActivityInterrupt,
		LingerSecs:        m.LCD.LingerSecs,
	}
	for _, p := range m.LCD.Pages {
		v.LCD.Pages = append(v.LCD.Pages, ViewLCDPage{
			Enabled:   p.Enabled,
			Name:      p.Name,
			Duration:  p.Duration,
			Interrupt: p.Interrupt,
			Lines:     append([]string(nil), p.Lines...),
		})
	}
	v.History = ViewHistory{RetentionDays: m.History.RetentionDays}
	v.StationID = ViewStationID{
		Enable:            m.StationID.Enable,
		TimeMins:          m.StationID.TimeMins,
		Callsign:          m.StationID.Callsign,
		EffectiveCallsign: m.EffectiveIDCallsign(),
		TXLevel:           m.StationID.TXLevel,
	}
	v.Update = ViewUpdate{Channel: m.Update.Channel, CheckEnabled: m.Update.CheckEnabled, AutoApply: m.Update.AutoApply, QuietWindow: m.Update.QuietWindow}
	// Buses/attachments project verbatim (no secrets). Copy the slices so the view
	// never aliases the model's backing arrays.
	v.Buses = append([]Bus(nil), m.Buses...)
	v.Attachments = append([]Attachment(nil), m.Attachments...)
	// Peers are redacted (secrets stripped); remote attachments carry none.
	v.Peers = make([]PeerView, 0, len(m.Peers))
	for _, p := range m.Peers {
		v.Peers = append(v.Peers, PeerView{
			ID: p.ID, Name: p.Name, Host: p.Host, Port: p.Port, MDNSInstance: p.MDNSInstance,
			State: p.State, Fingerprint: p.Fingerprint,
			HasCertificate: p.Certificate != "", HasKey: p.PrivateKey != "",
		})
	}
	v.RemoteAttachments = append([]RemoteAttachment(nil), m.RemoteAttachments...)
	// System tab (#29). Both project their EFFECTIVE values — the defaults resolved
	// — rather than the raw row, so the page shows what the node will actually do
	// instead of a blank box on a store that has never been written. The password is
	// the one field that does not project at all (D4): only whether one is set.
	v.MQTT = ViewMQTT{
		Host:         m.MQTT.host(),
		Port:         m.MQTT.port(),
		Auth:         m.MQTT.Auth,
		Username:     m.MQTT.Username,
		HasPassword:  m.MQTT.Password != "",
		Name:         m.MQTT.HostName(),
		StatusPrefix: m.MQTT.StatusTopicPrefix(),
		BusPrefix:    m.MQTT.BusTopicPrefix(),
	}
	v.Logging = ViewLogging{
		MMDVM:         LogLevels{Display: m.Logging.MMDVM.display("0"), MQTT: m.Logging.MMDVM.mqtt("1")},
		DMRGateway:    LogLevels{Display: m.Logging.DMRGateway.display("0"), MQTT: m.Logging.DMRGateway.mqtt("1")},
		YSFGateway:    LogLevels{Display: m.Logging.YSFGateway.display("0"), MQTT: m.Logging.YSFGateway.mqtt("1")},
		DGIdGateway:   LogLevels{Display: m.Logging.DGIdGateway.display("0"), MQTT: m.Logging.DGIdGateway.mqtt("1")},
		P25Gateway:    LogLevels{Display: m.Logging.P25Gateway.display("0"), MQTT: m.Logging.P25Gateway.mqtt("1")},
		NXDNGateway:   LogLevels{Display: m.Logging.NXDNGateway.display("0"), MQTT: m.Logging.NXDNGateway.mqtt("1")},
		DStarGateway:  LogLevels{Display: m.Logging.DStarGateway.display("0"), MQTT: m.Logging.DStarGateway.mqtt("1")},
		DAPNETGateway: LogLevels{Display: m.Logging.DAPNETGateway.display("0"), MQTT: m.Logging.DAPNETGateway.mqtt("1")},
		M17Gateway:    FileLogLevels{Display: m.Logging.M17Gateway.display("1"), File: m.Logging.M17Gateway.file("0")},
	}
	return v
}
