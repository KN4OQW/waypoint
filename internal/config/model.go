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
	General  General      `json:"general"`
	Modem    Modem        `json:"modem"`
	Display  Display      `json:"display"`
	DMR      DMR          `json:"dmr"`
	DMRNet   DMRNet       `json:"dmrnet"`
	Modes    Modes        `json:"modes"`
	Networks []Network    `json:"networks"`
	Routes   []DMRRoute   `json:"routes"`
	YSF      YSF          `json:"ysf"`
	YSFGW    YSFGateway   `json:"ysfgw"`
	P25      P25          `json:"p25"`
	P25GW    P25Gateway   `json:"p25gw"`
	NXDN     NXDN         `json:"nxdn"`
	NXDNGW   NXDNGateway  `json:"nxdngw"`
	DStar    DStar        `json:"dstar"`
	DStarGW  DStarGateway `json:"dstargw"`
	M17      M17          `json:"m17"`
	M17GW    M17Gateway   `json:"m17gw"`
	POCSAG   POCSAG       `json:"pocsag"`
	FM       FM           `json:"fm"`
	// Cross-mode transcoding bridges (MMDVM_CM). Each is a standalone daemon with
	// its own INI + systemd unit, gated by its Enable flag (see render.go
	// RenderTargets). YSF2DMR and NXDN2DMR carry a DMR-master password (a secret,
	// redacted in the view and preserved on blank, like a DMR network).
	YSF2DMR  YSF2DMR  `json:"ysf2dmr"`
	DMR2YSF  DMR2YSF  `json:"dmr2ysf"`
	YSF2NXDN YSF2NXDN `json:"ysf2nxdn"`
	DMR2NXDN DMR2NXDN `json:"dmr2nxdn"`
	NXDN2DMR NXDN2DMR `json:"nxdn2dmr"`
	// LCD is the Waypoint-native HD44780 driver (store-only; drives no INI).
	LCD LCD `json:"lcd"`
}

// The cross-mode bridges are the transcoding daemons from the MMDVM_CM tree
// (juribeparada/MMDVM_CM), the same binaries WPSD ships: each reads a source
// digital mode from one of MMDVM-Host's gateways and re-emits it on another
// network. WPSD exposes them as enable toggles in the MMDVM-Host panel; Waypoint
// models each as its own store section + INI renderer + systemd unit + card.
//
// Enable is a Waypoint-model gate, NOT an INI key: the MMDVM_CM INIs carry no
// [Info] Enable, so a bridge runs iff its unit is started. Enable therefore
// decides whether the bridge contributes a render target at all (render.go), and
// import infers it from the file's presence — the same trick ysfgw uses for
// EnableDGId (import.go dgidGatewayFromINI).

// YSF2DMR bridges System Fusion into a DMR master — the fat bridge. It logs into
// its own DMR master (Master/Password, a secret) and lands transcoded traffic on
// a target talkgroup (TG → [DMR Network] StartupDstId), with an optional DMR
// options line (Options, a WPSD addition sent at login like a DMR network's).
// Every non-fixed key here is one the MMDVM_CM YSF2DMR.ini exposes.
type YSF2DMR struct {
	Enable   bool   `json:"enable"`
	DMRId    string `json:"dmr_id"`   // CCS7/DMR ID the bridge logs in with ([DMR Network] Id)
	Master   string `json:"master"`   // DMR master address ([DMR Network] Address)
	Password string `json:"password"` // DMR master password (secret; blank on write = keep stored)
	Options  string `json:"options"`  // DMR options line sent at login (WPSD addition); empty omits it
	TG       string `json:"tg"`       // target talkgroup ([DMR Network] StartupDstId)
}

// DMR2YSF bridges DMR into System Fusion: it rides the local DMRGateway's
// 62031/62032 loopback and re-emits on the YSF side. DefaultTG is the DMR-side
// default destination talkgroup ([DMR Network] DefaultDstTG). No secret — the
// DMR side is the local gateway, not an upstream master.
type DMR2YSF struct {
	Enable    bool   `json:"enable"`
	DMRId     string `json:"dmr_id"`     // [DMR Network] Id
	DefaultTG string `json:"default_tg"` // default DMR-side destination TG ([DMR Network] DefaultDstTG)
}

// YSF2NXDN bridges System Fusion into NXDN. NXDNId is the NXDN network id it
// registers with ([NXDN Network] Id); TG is the target NXDN talkgroup ([NXDN
// Network] StartupDstId). No secret (the NXDN side is a reflector, not a
// password-authenticated master).
type YSF2NXDN struct {
	Enable bool   `json:"enable"`
	NXDNId string `json:"nxdn_id"` // [NXDN Network] Id
	TG     string `json:"tg"`      // target NXDN talkgroup ([NXDN Network] StartupDstId)
}

// DMR2NXDN bridges DMR into NXDN, riding the local DMRGateway loopback like
// DMR2YSF. NXDNId is the NXDN id used on the NXDN side ([NXDN Network]
// DefaultID). No secret.
type DMR2NXDN struct {
	Enable bool   `json:"enable"`
	DMRId  string `json:"dmr_id"`  // [DMR Network] Id
	NXDNId string `json:"nxdn_id"` // NXDN-side id ([NXDN Network] DefaultID)
}

// NXDN2DMR bridges NXDN into a DMR master — the other fat bridge. Like YSF2DMR it
// logs into its own DMR master (Master/Password, a secret) and lands on a target
// talkgroup (TG → [DMR Network] StartupDstId) with an optional Options line;
// NXDNTG is the NXDN-side listen talkgroup ([NXDN Network] TG).
type NXDN2DMR struct {
	Enable   bool   `json:"enable"`
	DMRId    string `json:"dmr_id"`   // [DMR Network] Id
	Master   string `json:"master"`   // DMR master address ([DMR Network] Address)
	Password string `json:"password"` // DMR master password (secret; blank on write = keep stored)
	Options  string `json:"options"`  // DMR options line sent at login; empty omits it
	TG       string `json:"tg"`       // target talkgroup ([DMR Network] StartupDstId)
	NXDNTG   string `json:"nxdn_tg"`  // NXDN-side listen talkgroup ([NXDN Network] TG)
}

// M17 holds MMDVM-Host's [M17] mode parameters (its enable flag is in Modes,
// like the other modes). M17 diverges from YSF/P25/NXDN: it has no RemoteGateway
// key, and instead of a RAN/NAC it uses a CAN (Channel Access Number, a plain
// decimal 0..15 like DMR's color code). It adds AllowEncryption — whether the
// host passes encrypted M17 frames through. (Host support is Waypoint's fork of
// MMDVM-Host; upstream removed M17 in commit 1e2e0c74.)
type M17 struct {
	CAN             string `json:"can"`              // Channel Access Number, decimal 0..15 (0 = the common default)
	SelfOnly        bool   `json:"self_only"`        // accept only this station's own callsign
	AllowEncryption bool   `json:"allow_encryption"` // pass encrypted M17 frames (off by default)
	TXHang          string `json:"tx_hang"`
}

// M17Gateway is the M17 gateway (M17Gateway.ini): the startup reflector+module,
// the node-type suffix, voice announcements, and the single network hang timer.
// Unlike the YSF/P25/NXDN gateways this daemon is pre-MQTT (file/console
// logging), so its own status is not on the dashboard data plane. Startup is an
// M17 reflector name whose trailing letter is the module, e.g. "M17-M17 C".
type M17Gateway struct {
	Suffix   string `json:"suffix"`    // node type appended to the callsign: H (hotspot) or R (repeater)
	Startup  string `json:"startup"`   // startup reflector+module (empty = don't auto-link on boot)
	Revert   bool   `json:"revert"`    // revert to Startup after inactivity
	HangTime string `json:"hang_time"` // seconds a network reflector is held
	Voice    bool   `json:"voice"`     // spoken link-status announcements
}

// POCSAG holds the paging surface: the single MMDVM-Host [POCSAG] parameter
// (Frequency, the paging channel) plus the DAPNETGateway.ini settings (its enable
// flag is in Modes, like the other modes). MMDVM-Host talks to DAPNETGateway over
// the fixed 3800/4800 [POCSAG Network] loopback; the gateway logs the operator
// into DAPNET (the amateur paging network) and relays pages. AuthKey is a secret
// (redacted in the API view, preserved on blank — like the ircDDB password).
// Server/AuthKey/Callsign/Whitelist/Blacklist render into DAPNETGateway.ini;
// Frequency is the only [POCSAG] key. Every non-fixed key is one the pinned
// pre-MQTT g4klx MMDVM-Host.ini / DAPNETGateway.ini exposes.
type POCSAG struct {
	Frequency string `json:"frequency"` // MMDVM-Host [POCSAG] Frequency (paging channel, Hz)
	Server    string `json:"server"`    // DAPNET server ([DAPNET] Address), e.g. dapnet.afu.rwth-aachen.de
	Callsign  string `json:"callsign"`  // POCSAG/DAPNET login callsign ([General] Callsign); defaults to the station callsign when blank
	AuthKey   string `json:"auth_key"`  // DAPNET AuthKey (secret; blank on write = keep stored)
	Whitelist string `json:"whitelist"` // comma-separated RIC whitelist ([General] WhiteList); empty omits it
	Blacklist string `json:"blacklist"` // comma-separated RIC blacklist ([General] BlackList); empty omits it
}

// FM is analog FM: MMDVM-Host's [FM] mode parameters (its enable flag is in
// Modes, like the other modes). Unlike every digital mode, FM has NO gateway
// daemon — the [FM] section is the whole surface, so there is no render target
// beyond MMDVM-Host. The modeled keys are the operator-facing ones (CTCSS tone,
// timeout, kerchunk time, the two audio-boost levels, access mode); MMDVM-Host's
// own defaults cover the rest of the large [FM] block. Every key name is verbatim
// from the pinned pre-MQTT g4klx MMDVM-Host.ini.
type FM struct {
	CTCSS         string `json:"ctcss"`           // [FM] CTCSSFrequency, the sub-audible access tone (Hz, e.g. 88.4)
	Timeout       string `json:"timeout"`         // [FM] Timeout, transmit time limit (seconds)
	KerchunkTime  string `json:"kerchunk_time"`   // [FM] KerchunkTime, anti-kerchunk hold before keying (seconds; 0 = off)
	RFAudioBoost  string `json:"rf_audio_boost"`  // [FM] RFAudioBoost, RF-side audio gain multiplier
	ExtAudioBoost string `json:"ext_audio_boost"` // [FM] ExtAudioBoost, network-side audio gain multiplier
	AccessMode    string `json:"access_mode"`     // [FM] AccessMode, 0..3 (carrier/CTCSS access; see the ini comment)
}

// DStar holds MMDVM-Host's [D-Star] mode parameters (its enable flag is in
// Modes, like the other modes). Module is the single band letter for this
// hotspot's D-Star module; it is appended as the 8th char of the D-Star
// callsign (DStarControl.cpp) and MUST match the gateway repeater Band, so it is
// the single source of truth rendered into both files.
type DStar struct {
	Module        string `json:"module"`         // band letter, e.g. B (upstream default is C; must match the gateway Band)
	SelfOnly      bool   `json:"self_only"`      // accept only this station's own callsign
	RemoteGateway bool   `json:"remote_gateway"` // hand network control to a remote gateway (off for a local DStarGateway)
}

// DStarGateway is the D-Star gateway (dstargateway.cfg): the ircDDB login used
// for callsign routing, the startup reflector, and which reflector protocols
// (DExtra/DPlus/DCS/XLX) are on. IRCDDBPassword is a secret (redacted in the API
// view, preserved on blank). DPlus is force-disabled upstream when its Login is
// empty (DStarGatewayConfig.cpp:130) and needs DPlus/US-Trust registration to
// link REF reflectors — DPlusLogin defaults to the station callsign when blank.
type DStarGateway struct {
	Reflector          string `json:"reflector"`           // startup reflector, e.g. "REF001 C" / "DCS006 B"; empty = none
	ReflectorReconnect string `json:"reflector_reconnect"` // Never / Fixed / 5..180 (minutes)
	IRCDDBHostname     string `json:"ircddb_hostname"`     // ircDDB network, e.g. ircv4.openquad.net
	IRCDDBUsername     string `json:"ircddb_username"`     // ircDDB login; defaults to the station callsign when blank
	IRCDDBPassword     string `json:"ircddb_password"`     // ircDDB password (secret); blank connects anonymously
	Dextra             bool   `json:"dextra"`              // DExtra (XRF) reflector protocol
	DPlus              bool   `json:"dplus"`               // D-Plus (REF) reflector protocol; needs DPlusLogin registered
	DPlusLogin         string `json:"dplus_login"`         // registered callsign for D-Plus; defaults to the station callsign when blank
	DCS                bool   `json:"dcs"`                 // DCS reflector protocol
	XLX                bool   `json:"xlx"`                 // XLX reflector protocol
}

// NXDN holds MMDVM-Host's [NXDN] mode parameters (its enable flag is in Modes,
// like the other modes). Unlike P25's NAC (hex), RAN is a plain decimal Radio
// Access Number, and NXDN has no OverrideUIDCheck.
type NXDN struct {
	RAN           string `json:"ran"`            // Radio Access Number, decimal 0..63 (1 = the common default)
	SelfOnly      bool   `json:"self_only"`      // accept only this station's own ID
	RemoteGateway bool   `json:"remote_gateway"` // hand network control to a remote gateway (off for a local NXDNGateway)
	TXHang        string `json:"tx_hang"`
}

// NXDNGateway is the NXDN gateway (NXDNGateway.ini): which reflector talkgroups
// to link on startup, voice announcements, and the RF/net hang timers.
type NXDNGateway struct {
	Static      string `json:"static"`        // comma-separated startup/static TGs, e.g. "10200,65000"
	Voice       bool   `json:"voice"`         // spoken link-status announcements
	RFHangTime  string `json:"rf_hang_time"`  // seconds RF holds a talkgroup
	NetHangTime string `json:"net_hang_time"` // seconds a network talkgroup is held
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
//
// The last three fields are the WPSD "DG-ID Gateway" additions. DGIdGateway is
// an *alternative* YSF gateway daemon (also from the pinned YSFClients tree) that
// shares MMDVM-Host's fixed 3200/4200 loopback with YSFGateway — the two are
// mutually exclusive, so EnableDGId swaps the rendered YSF file/unit from
// YSFGateway.ini to DGIdGateway.ini (render.go RenderTargets). It brings DG-ID
// addressing: one connection, many rooms selected by DG-ID, and the local
// Wires-X gateway on DG-ID 0. YCSNetwork adds the startup reflector as a static
// DG-ID network block (the YCS/networked-reflector path). UpperHostfiles is a
// hostlist-fetch transform (uppercase the managed reflector names) consumed by
// the ysfhosts fetcher — NOT a daemon INI key: neither pinned binary
// (YSFGateway/DGIdGateway @ 2b480aa) parses WiresXMakeUpper.
type YSFGateway struct {
	Suffix            string `json:"suffix"`             // RPT (duplex) / ND (simplex) / a letter
	WiresXPassthrough bool   `json:"wiresx_passthrough"` // let the radio's Wires-X buttons drive the gateway
	Startup           string `json:"startup"`            // startup reflector/room id, e.g. FCS00290
	Revert            bool   `json:"revert"`             // revert to Startup after inactivity
	InactivityTimeout string `json:"inactivity_timeout"` // minutes
	YSFNetwork        bool   `json:"ysf_network"`
	FCSNetwork        bool   `json:"fcs_network"`
	APRS              bool   `json:"aprs"`
	EnableDGId        bool   `json:"enable_dgid"`     // run DGIdGateway instead of YSFGateway on the 3200/4200 pair
	YCSNetwork        bool   `json:"ycs_network"`     // (DG-ID) add the startup reflector as a static DG-ID network
	UpperHostfiles    bool   `json:"upper_hostfiles"` // uppercase reflector names in the managed hostlist (fetch transform)
}

// Display is MMDVM-Host's [Display] surface: the [General] Display driver
// selector plus the per-driver subsection ([OLED]/[Nextion]/[HD44780]/[TFT
// Serial]/[LCDproc]) it points at. This is a WPSD-parity clone: Waypoint's own
// node runs display-free (its forked MMDVM-Host is MQTT-era and ignores these
// keys entirely — Conf.cpp has no [Display] parser, so they are inert on this
// node), but a WPSD clone carries the fields so an operator on stock/pre-MQTT
// MMDVM-Host, or one who drives a physical panel, gets working config. Every key
// name below is verbatim from the pre-MQTT g4klx MMDVM-Host.ini (the version WPSD
// targets); nothing is guessed.
//
// HD44780 note: the driver takes EITHER a GPIO 4-bit pin list ([HD44780] Pins,
// rendered from a constant — this node wires over I2C, so the pin list is not
// operator-edited) OR a PCF8574 I2C adapter address ([HD44780] I2CAddress, hex
// like 0x20). There is NO separate "I2C bus" key in the [HD44780] section — the
// bus is fixed by the driver — so I2C wiring is modeled as the single I2CAddress
// field, not an address+bus pair.
type Display struct {
	Type           string `json:"type"`             // [General] Display: None | OLED | Nextion | HD44780 | TFT Serial | LCDproc
	OLEDType       string `json:"oled_type"`        // [OLED] Type: 3 (0.96") or 6 (1.3")
	Port           string `json:"port"`             // serial port for Nextion/TFT Serial: None | modem | /dev/tty*
	NextionLayout  string `json:"nextion_layout"`   // [Nextion] ScreenLayout: 0 G4KLX / 2 ON7LDS L2 / 3 ON7LDS L3 / 4 ON7LDS L3 HS
	HD44780Rows    string `json:"hd44780_rows"`     // [HD44780] Rows
	HD44780Cols    string `json:"hd44780_cols"`     // [HD44780] Columns
	HD44780I2CAddr string `json:"hd44780_i2c_addr"` // [HD44780] I2CAddress (PCF8574 adapter; hex, e.g. 0x20)
}

// LCD is the Waypoint-NATIVE HD44780 driver (docs/design/lcd.md), distinct from
// the inert Display parity keys above. Waypoint's forked MMDVM-Host has no
// [Display] parser, so nothing lights a physical panel from Display; this section
// instead configures a driver that runs inside waypointd, subscribes to the live
// MQTT status plane, and paints the LCD itself. It is store-only — it drives no
// INI file — so it round-trips through the store (Save/Load), never through INI
// render/parse, and never appears in RenderTargets.
//
// Because the native driver opens the bus itself, I2CBus is a real field here —
// the thing MMDVM-Host's [HD44780] had no key for. Each page is a set of
// templated lines (tokens like {callsign} {status} {lh_call}); pages rotate on
// their Duration, and lines wider than Cols scroll.
type LCD struct {
	Enabled           bool      `json:"enabled"`
	I2CBus            string    `json:"i2c_bus"`            // e.g. /dev/i2c-1 (the native driver picks the bus)
	I2CAddress        string    `json:"i2c_address"`        // PCF8574 backpack address, hex e.g. 0x27
	Rows              string    `json:"rows"`               // 2 or 4
	Cols              string    `json:"cols"`               // 16 or 20
	ScrollSpeed       string    `json:"scroll_speed"`       // ms per scroll step for over-wide lines
	ActivityInterrupt bool      `json:"activity_interrupt"` // master switch for interrupt pages (below)
	LingerSecs        string    `json:"linger_secs"`        // hold an interrupt page this long after key-up before resuming
	Pages             []LCDPage `json:"pages"`
}

// LCDPage is one screen the operator defines: a name, whether it participates,
// how long it holds, whether it is an activity-interrupt page, and one templated
// string per row. A template line is literal text plus {tokens} (see
// internal/lcd/tokens.go). Pages are data, not code — the renderer expands them
// against live state. A page must not declare more lines than the panel has rows
// (ValidateLCD rejects it at save time, naming the geometry); extra rows on the
// panel render blank.
type LCDPage struct {
	Enabled   bool     `json:"enabled"`
	Name      string   `json:"name"`
	Duration  string   `json:"duration"`  // seconds this page holds before rotating
	Interrupt bool     `json:"interrupt"` // take over immediately on TX/RX; excluded from normal rotation
	Lines     []string `json:"lines"`     // one templated line per row
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
	Beacons        bool   `json:"beacons"` // DMR Roaming Beacon (MMDVM-Host [DMR Network] Beacons)
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

// NetworkType selects a network's DMRGateway rewrite template and dial prefix,
// mirroring WPSD. The routing rules are generated from the type (render.go)
// rather than hand-written, so the operator never edits raw rewrite lines. The
// one exception is NetCustom, which renders the verbatim Rewrites escape hatch.
type NetworkType string

const (
	NetBrandmeister NetworkType = "brandmeister" // prefix 2 (when not primary)
	NetDMRPlus      NetworkType = "dmrplus"      // DMR+/FreeDMR/HB-Link, prefix 8
	NetDMR2YSF      NetworkType = "dmr2ysf"      // DMR2YSF/DMR2NXDN, prefix 7
	NetTGIF         NetworkType = "tgif"         // prefix 5
	NetSystemX      NetworkType = "systemx"      // prefix 4
	NetXLX          NetworkType = "xlx"          // prefix 6
	NetCustom       NetworkType = "custom"       // raw Rewrites, no generation
)

// Network is one DMRGateway upstream (BrandMeister, TGIF, …). Password is a
// secret: it is stored, but the API View never serializes it.
//
// Routing is generated from Type + Primary (render.go), the WPSD model: exactly
// one network is Primary (no dial prefix, catches everything not claimed by a
// prefix rule via PassAllTG/PassAllPC on both slots — this is what makes the
// TG9990 Parrot echo without any config); every other network is reached by its
// type's dial prefix. Options is the verbatim per-network options string sent at
// login (BrandMeister TG subscriptions, DMR+ StartRef=…, …); empty omits it.
// Rewrites is used only when Type is NetCustom (or preserved by import for a
// network Waypoint could not classify), where it renders verbatim.
type Network struct {
	Name     string      `json:"name"`
	Type     NetworkType `json:"type"`
	Address  string      `json:"address"`
	Port     string      `json:"port"`
	Password string      `json:"password"`
	Options  string      `json:"options"`
	ESSID    string      `json:"essid"` // extended-ID suffix appended to the DMR ID ("01".."99"); "" = none
	Primary  bool        `json:"primary"`
	Enabled  bool        `json:"enabled"`
	// Custom-network extras (Pi-Star "Custom DMR Network"): AutoRewrite generates
	// the prefix-9 template instead of rendering raw Rewrites; TGListFile is an
	// optional talkgroup lookup file.
	AutoRewrite bool     `json:"auto_rewrite"`
	TGListFile  string   `json:"tg_list_file"`
	Rewrites    []string `json:"rewrites"`
	// XLX-specific (rendered into the [XLX Network] section): the startup
	// reflector number, startup module letter, and time slot.
	XLXStartup string `json:"xlx_startup"`
	XLXModule  string `json:"xlx_module"`
	XLXSlot    string `json:"xlx_slot"`
}

// DMRRoute ties one dialed talkgroup on one slot to a specific network by Name,
// overriding prefix/primary routing — the "tie this channel to this gateway"
// table. It renders as a direct TGRewrite on the target network; because
// DMRGateway evaluates every network's explicit rewrites before any PassAll
// (DMRGateway.cpp), a route always wins over the primary's catch-all.
type DMRRoute struct {
	Slot    string `json:"slot"`    // "1" or "2"
	TG      string `json:"tg"`      // dialed talkgroup
	Network string `json:"network"` // target Network.Name
}

// sections maps a store key to the field pointer, in one place so load and save
// can never drift apart.
func (m *Model) sections() map[string]any {
	return map[string]any{
		"general":  &m.General,
		"modem":    &m.Modem,
		"display":  &m.Display,
		"dmr":      &m.DMR,
		"dmrnet":   &m.DMRNet,
		"modes":    &m.Modes,
		"networks": &m.Networks,
		"routes":   &m.Routes,
		"ysf":      &m.YSF,
		"ysfgw":    &m.YSFGW,
		"p25":      &m.P25,
		"p25gw":    &m.P25GW,
		"nxdn":     &m.NXDN,
		"nxdngw":   &m.NXDNGW,
		"dstar":    &m.DStar,
		"dstargw":  &m.DStarGW,
		"m17":      &m.M17,
		"m17gw":    &m.M17GW,
		"pocsag":   &m.POCSAG,
		"fm":       &m.FM,
		"ysf2dmr":  &m.YSF2DMR,
		"dmr2ysf":  &m.DMR2YSF,
		"ysf2nxdn": &m.YSF2NXDN,
		"dmr2nxdn": &m.DMR2NXDN,
		"nxdn2dmr": &m.NXDN2DMR,
		"lcd":      &m.LCD,
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
