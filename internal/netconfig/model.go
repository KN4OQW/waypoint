// Package netconfig is Waypoint's second renderer family: the host / OS
// networking domain. Where internal/config compiles the store into the radio
// daemons' INI files, netconfig compiles it into NetworkManager connection
// KEYFILES (plus timesyncd / hostname later), the same store→model→render→apply
// discipline against a different output target.
//
// Two invariants carry over from RFC-0001 and are load-bearing here:
//
//   - Pure render: the same Model always produces byte-identical keyfiles, so an
//     unchanged store re-applies to no diff. Determinism includes the per-profile
//     UUID, which is derived from the profile name (keyfile.go) rather than being
//     random.
//   - Waypoint owns only what it created. Every managed profile is written as
//     waypoint-<name>.nmconnection with a generated header; netconfig never reads,
//     rewrites, or deletes a profile it did not create. A hand-made NM profile on
//     the same box is invisible to Waypoint.
//
// One invariant is new and specific to this domain: a bad network apply can
// strand the node, so the apply path is guarded by confirm-or-revert (apply.go)
// rather than the radio family's fire-and-restart.
//
// This is the FOUNDATION: read-only status, the keyfile renderer, and the
// confirm-or-revert engine. The Wi-Fi and VLAN edit surfaces land in later
// slices; the Model already carries the Wi-Fi PSK secret so its write-only
// plumbing (view.go / set.go) is in place before that surface exists.
package netconfig

import (
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// storeKey is the single store row that holds the whole netconfig Model as JSON.
// Unlike the radio family (one row per section), the host-network domain is one
// cohesive unit, so it lives under one key — Set merges partial bodies into it
// (set.go) and never touches the radio sections.
const storeKey = "netconfig"

// ConnType is a managed connection's kind. Only these render to a keyfile;
// anything else in the model is a data error, rejected at render time.
type ConnType string

const (
	TypeEthernet ConnType = "ethernet"
	TypeWiFi     ConnType = "wifi"
)

// Model is the authoritative, typed host-network configuration the store holds
// and the keyfile renderer consumes. Like internal/config.Model it carries
// secrets (Wi-Fi PSK) that the API View (view.go) redacts.
type Model struct {
	// Host (hostname + timezone) and NTP apply DIRECTLY — they cannot strand the
	// node, so they skip the confirm-or-revert guard (a third apply mechanism:
	// hostnamectl/timedatectl exec + a timesyncd drop-in, see host.go).
	Host Host `json:"host"`
	NTP  NTP  `json:"ntp"`
	// Connections are the managed NM profiles. Each renders to exactly one
	// waypoint-<Name>.nmconnection keyfile, in slice order (which fixes the write
	// order and therefore render purity).
	Connections []Connection `json:"connections"`
	// VLANs render type=vlan NM keyfiles (waypoint-vlan<id>) and, like connections,
	// go through the confirm-or-revert guard (a bad VLAN can cut the uplink).
	VLANs []VLAN `json:"vlans"`
}

// Host is the node's hostname and timezone. Blank means leave the host default
// untouched. Applied via hostnamectl / timedatectl (host.go), idempotently.
type Host struct {
	Hostname string `json:"hostname"`
	Timezone string `json:"timezone"`
}

// NTP is the systemd-timesyncd surface: whether the client is on and which
// upstream servers to use (empty = the distro/DHCP default).
type NTP struct {
	Enabled bool     `json:"enabled"`
	Servers []string `json:"servers"`
}

// VLAN is a tagged virtual interface on a parent device. It renders an NM
// type=vlan connection (waypoint-vlan<ID>) carrying its own IPv4 block — the same
// dhcp/static shape as an ethernet/wifi profile (H2). ID is the 802.1Q tag
// (1–4094); Name is an operator label (not rendered — NM has no label key beyond
// the id, which is waypoint-vlan<ID>).
type VLAN struct {
	Parent string `json:"parent"`
	ID     int    `json:"id"`
	Name   string `json:"name"`
	IPv4   IPv4   `json:"ipv4"`
}

// Connection is one managed NetworkManager profile. Name is the identity: the
// keyfile is waypoint-<Name>.nmconnection and the [connection] id is
// waypoint-<Name>, so Waypoint's profiles are self-identifying and never collide
// with a hand-made one.
type Connection struct {
	Name        string   `json:"name"`
	Type        ConnType `json:"type"`
	Interface   string   `json:"interface"`   // [connection] interface-name; blank binds by type only
	Autoconnect bool     `json:"autoconnect"` // [connection] autoconnect (default true when zero-value? no — explicit)
	IPv4        IPv4     `json:"ipv4"`
	// Priority is [connection] autoconnect-priority: a higher number wins when
	// several autoconnect profiles could activate (e.g. prefer Wi-Fi over a fallback).
	// Kept as a string for form-friendliness; blank/"0" omits the key.
	Priority string `json:"priority"`
	// WiFi is meaningful only when Type==TypeWiFi. The PSK is a secret: redacted
	// in the view and preserved on a blank write (set.go).
	WiFi WiFi `json:"wifi"`
}

// IPv4 is the [ipv4] group. Method is NM's ipv4.method — "auto" for DHCP,
// "manual" for a static address, "disabled" to turn IPv4 off (the UI presents
// these as DHCP / Static and maps). For manual, Address/Prefix (+ optional
// Gateway) render to address1. DNS renders for either method: with manual it is
// the resolver set; with auto it is a DNS *override* (rendered alongside
// ignore-auto-dns so the operator's servers replace the DHCP-provided ones rather
// than being merged). SearchDomains render to dns-search for either method.
type IPv4 struct {
	Method        string   `json:"method"`
	Address       string   `json:"address"`
	Prefix        string   `json:"prefix"`
	Gateway       string   `json:"gateway"`
	DNS           []string `json:"dns"`
	SearchDomains []string `json:"search_domains"`
}

// WiFi is the [wifi] / [wifi-security] groups. The PSK is write-only (see set.go).
// Hidden renders [wifi] hidden=true for a non-broadcast SSID. Country is the
// regulatory domain (a 2-letter code); it is stored and validated here but is a
// system/radio-wide setting with no per-connection NM keyfile key, so it is
// applied to the wpa_supplicant/reg-domain out of band (a documented follow-up),
// not rendered into the keyfile.
type WiFi struct {
	SSID    string `json:"ssid"`
	PSK     string `json:"psk"`
	Hidden  bool   `json:"hidden"`
	Country string `json:"country"`
}

// DefaultModel is the zero-configuration baseline: NTP on (systemd-timesyncd's
// default posture on the bench Pi), no managed connections, host hostname/timezone
// left untouched. First run seeds this so the status tab and renderer have a
// well-formed model even before the operator configures anything.
func DefaultModel() Model {
	return Model{NTP: NTP{Enabled: true}}
}

// Load reads the netconfig Model from the store, returning DefaultModel when the
// row is absent (first run). It mirrors internal/config.Load's contract: a typed
// model for the renderer and the view.
func Load(s *store.Store) (Model, error) {
	m := DefaultModel()
	if _, err := s.GetInto(storeKey, &m); err != nil {
		return Model{}, err
	}
	return m, nil
}

// managedName reports whether a NetworkManager connection id / filename stem is
// one Waypoint manages. Every managed profile is prefixed waypoint-; the renderer
// writes only these and the apply path never touches anything else.
func managedName(id string) bool { return strings.HasPrefix(id, profilePrefix) }
