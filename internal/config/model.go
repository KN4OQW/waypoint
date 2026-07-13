package config

import (
	"bytes"
	"encoding/json"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Model is the authoritative, typed configuration the store holds and the
// renderers consume. It is grouped into sections; each section is one store row
// (key = section name, value = the section's JSON). Disabling a mode flips a
// bool in Modes — it never removes a row, so a disabled mode's settings survive
// untouched, the property the incumbent writers failed (RFC-0001, #1).
//
// Unlike the API View (view.go), the Model carries secrets (network passwords)
// and the keys needed to render working daemon configs. Phase 1 covers the
// settings-page surface plus core operation; the many fixed modem-calibration
// keys render from constants (render.go) until the model grows to cover them
// before the live node is cut over to store-rendered files.
type Model struct {
	General  General    `json:"general"`
	Modem    Modem      `json:"modem"`
	DMR      DMR        `json:"dmr"`
	DMRNet   DMRNet     `json:"dmrnet"`
	Modes    Modes      `json:"modes"`
	Networks []Network  `json:"networks"`
	YSF      YSF        `json:"ysf"`
	YSFGW    YSFGateway `json:"ysfgw"`
	P25      P25        `json:"p25"`
	P25GW    P25Gateway `json:"p25gw"`
}

// P25 holds MMDVM-Host's [P25] mode parameters (its enable flag is in Modes,
// like the other modes).
type P25 struct {
	NAC              string `json:"nac"`                // Network Access Code, hex (293 = the common default)
	SelfOnly         bool   `json:"self_only"`          // accept only this station's own ID
	OverrideUIDCheck bool   `json:"override_uid_check"` // skip the source-ID (UID) validity check
	RemoteGateway    bool   `json:"remote_gateway"`     // hand network control to a remote gateway (off for a local P25Gateway)
	TXHang           string `json:"tx_hang"`
}

// P25Gateway is the P25 gateway (P25Gateway.ini): which reflector talkgroups to
// link on startup, voice announcements, and the RF/net hang timers.
type P25Gateway struct {
	Static      string `json:"static"`        // comma-separated startup/static TGs, e.g. "10100,10200"
	Voice       bool   `json:"voice"`         // spoken link-status announcements
	RFHangTime  string `json:"rf_hang_time"`  // seconds RF holds a talkgroup
	NetHangTime string `json:"net_hang_time"` // seconds a network talkgroup is held
}

// YSF holds MMDVM-Host's [System Fusion] mode parameters (its enable flag is in
// Modes, like the other modes).
type YSF struct {
	LowDeviation  bool   `json:"low_deviation"`
	SelfOnly      bool   `json:"self_only"`
	TXHang        string `json:"tx_hang"`
	RemoteGateway bool   `json:"remote_gateway"`
	ModeHang      string `json:"mode_hang"`
}

// YSFGateway is the System Fusion gateway (YSFGateway.ini): reflector/room
// connection, Wires-X behaviour, and which of the YSF/FCS networks are on.
type YSFGateway struct {
	Suffix            string `json:"suffix"`             // RPT (duplex) / ND (simplex) / a letter
	WiresXPassthrough bool   `json:"wiresx_passthrough"` // let the radio's Wires-X buttons drive the gateway
	WiresXMakeUpper   bool   `json:"wiresx_make_upper"`
	Startup           string `json:"startup"` // startup reflector/room id, e.g. FCS00290
	Reconnect         bool   `json:"reconnect"`
	Revert            bool   `json:"revert"`             // revert to Startup after inactivity
	InactivityTimeout string `json:"inactivity_timeout"` // minutes
	YSFNetwork        bool   `json:"ysf_network"`
	FCSNetwork        bool   `json:"fcs_network"`
	APRS              bool   `json:"aprs"`
}

// General is station identity and top-level behaviour.
type General struct {
	Callsign    string `json:"callsign"`
	ID          string `json:"id"`
	Duplex      bool   `json:"duplex"`
	Timeout     string `json:"timeout"`
	RFModeHang  string `json:"rf_mode_hang"`
	NetModeHang string `json:"net_mode_hang"`
	Power       string `json:"power"`
	Location    string `json:"location"`
	URL         string `json:"url"`
}

// Modem holds RF/modem-hardware settings. Frequencies stay in Hz (the daemons'
// unit) as strings to avoid float drift.
type Modem struct {
	Port      string `json:"port"`
	UARTSpeed string `json:"uart_speed"`
	RXFreqHz  string `json:"rx_freq_hz"`
	TXFreqHz  string `json:"tx_freq_hz"`
	RXOffset  string `json:"rx_offset"`
	TXOffset  string `json:"tx_offset"`
	TXInvert  bool   `json:"tx_invert"`
	RXInvert  bool   `json:"rx_invert"`
	PTTInvert bool   `json:"ptt_invert"`
	RXLevel   string `json:"rx_level"`
	TXLevel   string `json:"tx_level"`
}

// DMR holds DMR parameters; its enable flag lives in Modes so all modes toggle
// uniformly.
type DMR struct {
	ColorCode      string `json:"color_code"`
	ID             string `json:"id"`
	EmbeddedLCOnly bool   `json:"embedded_lc_only"`
	SelfOnly       bool   `json:"self_only"`
	DumpTAData     bool   `json:"dump_ta_data"`
}

// DMRNet is MMDVM-Host's link to the local DMRGateway.
type DMRNet struct {
	LocalPort      string `json:"local_port"`
	GatewayAddress string `json:"gateway_address"`
	GatewayPort    string `json:"gateway_port"`
	Slot1          bool   `json:"slot1"`
	Slot2          bool   `json:"slot2"`
	Jitter         string `json:"jitter"`
}

// Modes carries the per-mode enable flags.
type Modes struct {
	DStar  bool `json:"dstar"`
	DMR    bool `json:"dmr"`
	YSF    bool `json:"ysf"`
	P25    bool `json:"p25"`
	NXDN   bool `json:"nxdn"`
	M17    bool `json:"m17"`
	POCSAG bool `json:"pocsag"`
	FM     bool `json:"fm"`
}

// Network is one DMRGateway upstream (BrandMeister, TGIF, …). Password is a
// secret: it is stored, but the API View never serializes it. Rewrites are the
// verbatim TG/PC/Src rewrite lines, preserved so routing is not lost.
type Network struct {
	Name     string   `json:"name"`
	Address  string   `json:"address"`
	Port     string   `json:"port"`
	Password string   `json:"password"`
	Enabled  bool     `json:"enabled"`
	Rewrites []string `json:"rewrites"`
}

// sections maps a store key to the field pointer, in one place so load and save
// can never drift apart.
func (m *Model) sections() map[string]any {
	return map[string]any{
		"general":  &m.General,
		"modem":    &m.Modem,
		"dmr":      &m.DMR,
		"dmrnet":   &m.DMRNet,
		"modes":    &m.Modes,
		"networks": &m.Networks,
		"ysf":      &m.YSF,
		"ysfgw":    &m.YSFGW,
		"p25":      &m.P25,
		"p25gw":    &m.P25GW,
	}
}

// Load reads the whole model from the store. Missing sections keep their zero
// value (a fresh store yields a zero Model, which the caller seeds).
func Load(s *store.Store) (*Model, error) {
	m := &Model{}
	for key, ptr := range m.sections() {
		if _, err := s.GetInto(key, ptr); err != nil {
			return nil, err
		}
	}
	return m, nil
}

// Save writes every section back to the store, attributing the write to by.
// Each section is an independent row, so a save touches only what changed.
func (m *Model) Save(s *store.Store, by string) error {
	for key, ptr := range m.sections() {
		if err := s.Set(key, ptr, by); err != nil {
			return err
		}
	}
	return nil
}

// SetSection merges a partial JSON body into one section and writes it back,
// rejecting unknown fields (schema drift is a caller error, not silently kept).
// It is a merge, not a replace: the current section is loaded first, then the
// body is decoded over it, so a UI that sends only the fields it manages never
// drops the keys it doesn't (timeout, invert flags, …). Returns known=false for
// an unrecognized section name.
func SetSection(s *store.Store, section string, raw []byte, by string) (known bool, err error) {
	m := &Model{}
	ptr, ok := m.sections()[section]
	if !ok {
		return false, nil
	}
	// Load the current section so unspecified fields survive the merge.
	if _, err := s.GetInto(section, ptr); err != nil {
		return true, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(ptr); err != nil {
		return true, err
	}
	return true, s.Set(section, ptr, by)
}
