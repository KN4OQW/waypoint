package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// This file is the render half of RFC-0016 (bus LAN peering): it extends the
// Phase-1 bus renderer (render.go renderBusConfig) with what "renders on each
// side" — the owner-side peering block + member rows inside the existing bus
// config, and the member-side config for each remote membership.
//
// Reconciliation note. The merged Prompt-8 model is OWNER-CENTRIC: a
// remote_attachment { bus_id (local), peer_id, mode } lives on the bus's owner
// and means "this peer's mode joins my bus". A member node's store therefore has
// no record of its memberships, so the member-side config cannot render from the
// member's own store. The owner is the bus's cluster-wide authority (RFC-0016 §4),
// so BOTH sides render here from the owner's model; a later transport/pairing PR
// delivers each member config to its peer. NO key material is ever rendered —
// certs and keys are referenced by conventional file paths under Paths.PeeringDir
// (0600, waypointd-owned; RFC-0002 posture).

// Peering is the node's peering policy (RFC-0016 §5), a store section. The values
// default to the RFC's numbers when unset — they are sourced from config, not
// hardcoded at the render site.
type Peering struct {
	ListenAddr     string `json:"listen_addr,omitempty"`      // owner mTLS listener; default DefaultPeeringListen
	DeadlineMs     int    `json:"deadline_ms,omitempty"`      // per-frame play-out deadline; default 60
	JitterBufferMs int    `json:"jitter_buffer_ms,omitempty"` // jitter buffer depth; default 40
}

// RFC-0016 defaults (§5: deadline derived from the measured p99; §Design 1: the
// listener is a dedicated TCP/mTLS port, distinct from the mode loopbacks).
const (
	DefaultPeeringListen   = "0.0.0.0:42500"
	DefaultPeeringDeadline = 60 // ms
	DefaultPeeringJitter   = 40 // ms
)

// DefaultPeering seeds the section with the zero value; the effective values are
// resolved at render time so a stored zero always means "the RFC default".
func DefaultPeering() Peering { return Peering{} }

func (p Peering) listen() string {
	if p.ListenAddr != "" {
		return p.ListenAddr
	}
	return DefaultPeeringListen
}
func (p Peering) deadline() int {
	if p.DeadlineMs > 0 {
		return p.DeadlineMs
	}
	return DefaultPeeringDeadline
}
func (p Peering) jitter() int {
	if p.JitterBufferMs > 0 {
		return p.JitterBufferMs
	}
	return DefaultPeeringJitter
}

// --- rendered config shapes --------------------------------------------------

// BusPeering is the owner-side peering block added to a bus's config when the bus
// has at least one remote attachment (RFC-0016 §what-renders, home node). The
// owner opens an mTLS listener; each member row is one peer contributing a mode.
type BusPeering struct {
	Listen         string          `json:"listen"`           // mTLS listener (host:port) the owner binds
	KeyPath        string          `json:"key_path"`         // owner's peering private key file — a PATH, never PEM
	DeadlineMs     int             `json:"deadline_ms"`      // RFC-0016 §5
	JitterBufferMs int             `json:"jitter_buffer_ms"` // RFC-0016 §5
	Members        []BusPeerMember `json:"members"`          // one per remote attachment, in stable order
}

// BusPeerMember is one peer's mode joining the owner's bus, with the peer's
// resolved endpoint, pinned-cert PATH, and the RFC-0003 §3 translation params.
type BusPeerMember struct {
	PeerID       string `json:"peer_id"`
	Name         string `json:"name,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`      // host:port resolved from the peer row
	MDNSInstance string `json:"mdns_instance,omitempty"` // resolve at runtime when Endpoint is empty
	Fingerprint  string `json:"fingerprint,omitempty"`   // viewable; pin the incoming cert against it
	CertPath     string `json:"cert_path"`               // pinned peer cert file — a PATH, never PEM

	Mode              Mode              `json:"mode"`
	Slot              string            `json:"slot,omitempty"`
	DefaultTG         string            `json:"default_tg,omitempty"`
	TGMap             map[string]string `json:"tg_map,omitempty"`
	Target            string            `json:"target,omitempty"`
	WiresXPassthrough bool              `json:"wiresx_passthrough,omitempty"`
	ID                string            `json:"id,omitempty"`
	TG                string            `json:"tg,omitempty"`
	DefaultID         string            `json:"default_id,omitempty"`
}

// ConfigRole sniffs a rendered bus-config file to tell an owner BusConfig from a
// member MemberBusConfig without fully decoding either: the member config carries
// `"role":"member"`, the owner config has no role field. The daemon dispatches on
// it (one binary reads both shapes, RFC-0016 §what-renders).
func ConfigRole(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var probe struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "", fmt.Errorf("parse config role %s: %w", path, err)
	}
	if probe.Role == "" {
		return "owner", nil
	}
	return probe.Role, nil
}

// ReadMemberConfig loads a rendered member-side config file (role="member"). It is
// the reader the member daemon imports, mirroring ReadBusConfig for the owner side.
func ReadMemberConfig(path string) (MemberBusConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return MemberBusConfig{}, err
	}
	var c MemberBusConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return MemberBusConfig{}, fmt.Errorf("parse member config %s: %w", path, err)
	}
	if c.Role != "member" {
		return MemberBusConfig{}, fmt.Errorf("member config %s: role is %q, want \"member\"", path, c.Role)
	}
	if c.BusID == "" || len(c.Attachments) == 0 {
		return MemberBusConfig{}, fmt.Errorf("member config %s: needs a bus_id and at least one attachment", path)
	}
	return c, nil
}

// MemberBusConfig is the member-side config for one remote membership — a peer
// (member) contributing one or more modes to a remote (owner's) bus. It tells the
// member's daemon to bind its local loopback(s) for those modes, dial the owner,
// and run as a token-CLIENT (the owner owns the token, RFC-0016 §4).
type MemberBusConfig struct {
	Role            string             `json:"role"`   // always "member"
	BusID           string             `json:"bus_id"` // the owner's bus id
	Owner           MemberOwner        `json:"owner"`
	Attachments     []MemberAttachment `json:"attachments"` // the modes this member contributes, stable order
	KeyPath         string             `json:"key_path"`    // the member's own peering key file — a PATH, never PEM
	DeadlineMs      int                `json:"deadline_ms"`
	JitterBufferMs  int                `json:"jitter_buffer_ms"`
	HangTimeSeconds float64            `json:"hang_time_seconds,omitempty"`
}

// MemberOwner locates the bus owner the member dials. The owner's LAN address is
// resolved by the member from its own pairing record at runtime (the member knows
// the owner as a paired peer); the config carries the port + the cert PATH to pin.
type MemberOwner struct {
	Listen   string `json:"listen"`            // the owner's mTLS listen (host:port); host may be 0.0.0.0
	CertPath string `json:"cert_path"`         // where the member finds the owner's pinned cert — a PATH, never PEM
	PeerID   string `json:"peer_id,omitempty"` // the owner's peer id from the owner's side (diagnostic)
}

// MemberAttachment is one mode the member contributes, with its local loopback
// binding (the fixed per-mode pair the local bus daemon uses — the one consumer
// of that loopback, RFC-0003 §Motivation-2) and the translation params.
type MemberAttachment struct {
	Mode              Mode              `json:"mode"`
	Loopback          BusLoopback       `json:"loopback"`
	Slot              string            `json:"slot,omitempty"`
	DefaultTG         string            `json:"default_tg,omitempty"`
	TGMap             map[string]string `json:"tg_map,omitempty"`
	Target            string            `json:"target,omitempty"`
	WiresXPassthrough bool              `json:"wiresx_passthrough,omitempty"`
	ID                string            `json:"id,omitempty"`
	TG                string            `json:"tg,omitempty"`
	DefaultID         string            `json:"default_id,omitempty"`
}

// BusLoopback is a mode's fixed 127.0.0.1 loopback pair (matching
// cmd/waypoint-bus/endpoints.go): the port the daemon binds and the peer port it
// sends to.
type BusLoopback struct {
	Bind int `json:"bind"`
	Peer int `json:"peer"`
}

// busLoopbackFor returns the fixed loopback pair for a reframe-tier mode, the same
// mapping cmd/waypoint-bus consumes (DMR rides the local DMRGateway; YSF/NXDN
// replace their gateways). Kept here so the render is a pure config-package
// function; the daemon has the authoritative copy.
func busLoopbackFor(m Mode) (BusLoopback, bool) {
	switch m {
	case ModeDMR:
		return BusLoopback{Bind: 62032, Peer: 62031}, true
	case ModeYSF:
		return BusLoopback{Bind: 4200, Peer: 3200}, true
	case ModeNXDN:
		return BusLoopback{Bind: 14020, Peer: 14021}, true
	}
	return BusLoopback{}, false
}

// --- path conventions (no PEM ever) ------------------------------------------

// nodeKeyPath is this node's own peering private key file. ownerCertPath /
// peerCertPath name the pinned certificates by the OTHER party's key each side
// knows: the owner names a member's cert by the member's peer id; a member names
// its owner's cert by the bus id (both parties know the bus id, and the member
// does not know the owner's own node id). All are PATHS the pairing layer
// populates 0600; the renderer never embeds cert or key bytes (RFC-0016 §Security
// posture, RFC-0002).
func nodeKeyPath(dir string) string          { return filepath.Join(dir, "node.key") }
func peerCertPath(dir, peerID string) string { return filepath.Join(dir, "peer-"+peerID+".crt") }
func ownerCertPath(dir, busID string) string { return filepath.Join(dir, "owner-"+busID+".crt") }

// activeRemoteAttachmentsForBus returns the remote attachments on a bus whose peer
// is PAIRED, in stable (peer id, mode) order. A revoked or pending peer's remote
// attachments are omitted — they render NOTHING (and delete nothing; the row stays
// in the store) per RFC-0016 §4.
func (m *Model) activeRemoteAttachmentsForBus(busID string) []RemoteAttachment {
	paired := make(map[string]Peer, len(m.Peers))
	for _, p := range m.Peers {
		if p.State == PeerPaired {
			paired[p.ID] = p
		}
	}
	var out []RemoteAttachment
	for _, ra := range m.RemoteAttachments {
		if ra.BusID != busID {
			continue
		}
		if _, ok := paired[ra.PeerID]; !ok {
			continue // dormant: revoked/pending/removed peer renders nothing
		}
		out = append(out, ra)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PeerID != out[j].PeerID {
			return out[i].PeerID < out[j].PeerID
		}
		return out[i].Mode < out[j].Mode
	})
	return out
}

// peerByID is a small lookup for the render sites.
func (m *Model) peerByID(id string) (Peer, bool) {
	for _, p := range m.Peers {
		if p.ID == id {
			return p, true
		}
	}
	return Peer{}, false
}

// resolvedEndpoint is a peer's host:port when it has a static address, else "".
// The mDNS instance (returned separately) is resolved at runtime when empty.
func resolvedEndpoint(p Peer) string {
	if p.Host == "" {
		return ""
	}
	if p.Port == "" {
		return p.Host
	}
	return p.Host + ":" + p.Port
}

// jsonBlock marshals a rendered config value to the canonical indented form the
// bus configs use (matching BusConfig.Marshal), with a trailing newline.
func jsonBlock(v any) string {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(raw) + "\n"
}
