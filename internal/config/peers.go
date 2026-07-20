package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/KN4OQW/waypoint/internal/store"
)

// This file is the store/model half of RFC-0016 (bus LAN peering): the peers[]
// and remote_attachments[] sections, the pairing lifecycle, and the validator
// extensions. There is NO transport, render, or UI here — a remote attachment is
// a declared edge of a local bus, validated exactly like a local one, that a
// later layer will actually wire over mTLS.

// PeerState is a peer's pairing lifecycle (RFC-0016 §3, "Pairing"). Revocation is
// the RFC-0001 disable-preserves-data state: it flips to Revoked and keeps the
// row (and its dependents), it never deletes.
type PeerState string

const (
	PeerPending PeerState = "pending" // pairing initiated on this node, not yet mutually confirmed
	PeerPaired  PeerState = "paired"  // mutual pairing complete; the link may carry a bus
	PeerRevoked PeerState = "revoked" // pairing withdrawn; row + dependents retained, but dormant
)

func validPeerState(s PeerState) bool {
	return s == PeerPending || s == PeerPaired || s == PeerRevoked
}

// Peer is a paired (or pairing) Waypoint node on the LAN (RFC-0016 §3 / §Store
// shape). It carries no media — it authenticates a point-to-point mTLS link. The
// peer's pinned Certificate and this node's per-peering PrivateKey are write-only
// secrets: never returned by a read API, preserved on a blank write, and redacted
// in the view (only Fingerprint is viewable). Re-pairing a revoked peer mints
// fresh key material rather than reusing the old (see SetPeers).
type Peer struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`                    // the peer node's display name
	Host         string    `json:"host,omitempty"`          // LAN address; used with Port
	Port         string    `json:"port,omitempty"`          // LAN port
	MDNSInstance string    `json:"mdns_instance,omitempty"` // alternative to host:port (_waypoint._tcp)
	State        PeerState `json:"state"`
	Fingerprint  string    `json:"fingerprint,omitempty"` // peer cert fingerprint — VIEWABLE (out-of-band verification)

	// Secrets — write-only, preserved-on-blank, never in a view.
	Certificate string `json:"certificate,omitempty"` // the pinned peer certificate (PEM)
	PrivateKey  string `json:"private_key,omitempty"` // this node's per-peering private key (PEM)
}

// RemoteAttachment is a remote edge of a LOCAL bus (RFC-0016 §Store shape): it
// joins one mode on a paired peer node to a local bus, carrying that edge's
// RFC-0003 §3 translation params. It holds no secret (the peer's credentials live
// on the peer; the mTLS trust lives in the Peer row). Its field set mirrors
// Attachment minus CredentialsRef (a remote edge authenticates through the peer
// link, not a Networks[] entry).
type RemoteAttachment struct {
	BusID  string `json:"bus_id"`  // the LOCAL bus this remote edge joins
	PeerID string `json:"peer_id"` // the Peer supplying the mode
	Mode   Mode   `json:"mode"`    // dmr | ysf | nxdn (reframe tier)

	// DMR translation params.
	Slot      string            `json:"slot,omitempty"`
	DefaultTG string            `json:"default_tg,omitempty"`
	TGMap     map[string]string `json:"tg_map,omitempty"`

	// YSF translation params.
	Target            string `json:"target,omitempty"`
	WiresXPassthrough bool   `json:"wiresx_passthrough,omitempty"`

	// NXDN translation params.
	ID        string `json:"id,omitempty"`
	TG        string `json:"tg,omitempty"`
	DefaultID string `json:"default_id,omitempty"`
}

// maxBusNodes is RFC-0016 decision 2's v1 VALIDATOR POLICY: a bus may span at
// most this many participating nodes (the local/home node plus its peers). This
// is a validator cap ONLY — the wire protocol and frame envelope are N-node from
// the first byte (RFC-0016 §2), so lifting the cap is changing this constant, not
// a protocol bump.
const maxBusNodes = 2

// DefaultPeers / DefaultRemoteAttachments seed a store that predates RFC-0016 with
// the empty sections, so Load never returns a nil surprise (matching
// DefaultBuses/DefaultAttachments).
func DefaultPeers() []Peer                         { return []Peer{} }
func DefaultRemoteAttachments() []RemoteAttachment { return []RemoteAttachment{} }

// ValidatePeers checks the peers[] section in isolation: non-empty unique ids and
// a legal pairing state. Secrets are not inspected here (they are reconciled in
// SetPeers).
func ValidatePeers(peers []Peer) error {
	seen := make(map[string]bool, len(peers))
	for _, p := range peers {
		if p.ID == "" {
			return fmt.Errorf("peer has an empty id")
		}
		if seen[p.ID] {
			return fmt.Errorf("duplicate peer id %q", p.ID)
		}
		seen[p.ID] = true
		if !validPeerState(p.State) {
			return fmt.Errorf("peer %q has invalid state %q (want pending|paired|revoked)", p.ID, p.State)
		}
	}
	return nil
}

// ValidateRemoteAttachments is the peering extension of the RFC-0003 attach-time
// validator. It is a PURE STRUCTURAL function of the four sections and is
// deliberately INDEPENDENT of peer pairing state (except that a peer must exist),
// so that revoking a peer — a state change only — never changes a validation
// outcome and therefore never rejects the peer's now-dormant remote attachments
// (RFC-0016 §4 / RFC-0001 disable-preserves-data). The pairing-state gate lives
// at add time in SetRemoteAttachments instead.
//
// It enforces, per RFC-0016:
//   - a remote attachment references an existing local bus and an existing peer;
//   - a peer contributes a given mode to a given bus at most once
//     ("peer already contributes mode X to this bus");
//   - a peer's mode (its single loopback) feeds at most one local bus — the
//     cross-node analogue of RFC-0003 §5 rule 3 (mode-uniqueness is per node);
//   - per bus, the UNION of local + remote modes is a valid RFC-0003 §2 reframe
//     set (refusal reuses the Phase-1 reason strings);
//   - per bus, the participating-node count is within the v1 cap (§2).
func ValidateRemoteAttachments(buses []Bus, local []Attachment, remote []RemoteAttachment, peers []Peer) error {
	busByID := make(map[string]bool, len(buses))
	for _, b := range buses {
		busByID[b.ID] = true
	}
	peerByID := make(map[string]Peer, len(peers))
	for _, p := range peers {
		peerByID[p.ID] = p
	}

	// Local modes per bus (the home node's own attachments), plus a flag that the
	// local node contributes to the bus (for the node-count cap).
	localModesByBus := make(map[string][]Mode)
	localParticipates := make(map[string]bool)
	for _, a := range local {
		localModesByBus[a.BusID] = append(localModesByBus[a.BusID], a.Mode)
		localParticipates[a.BusID] = true
	}

	remoteModesByBus := make(map[string][]Mode)
	peersByBus := make(map[string]map[string]bool) // bus -> set of peer ids contributing
	perBusPeerMode := make(map[string]bool)        // "bus|peer|mode" already seen
	peerModeBus := make(map[string]string)         // "peer|mode" -> bus (a peer's loopback feeds one bus)

	for _, ra := range remote {
		if ra.Mode == "" {
			return fmt.Errorf("remote attachment has an empty mode")
		}
		if !busByID[ra.BusID] {
			return fmt.Errorf("remote attachment for %s references unknown bus %q", modeLabel(ra.Mode), ra.BusID)
		}
		p, ok := peerByID[ra.PeerID]
		if !ok {
			return fmt.Errorf("remote attachment for %s references unknown peer %q", modeLabel(ra.Mode), ra.PeerID)
		}

		pbm := ra.BusID + "|" + ra.PeerID + "|" + string(ra.Mode)
		if perBusPeerMode[pbm] {
			return fmt.Errorf("peer %s already contributes mode %s to bus %q", peerLabel(p), modeLabel(ra.Mode), ra.BusID)
		}
		perBusPeerMode[pbm] = true

		pm := ra.PeerID + "|" + string(ra.Mode)
		if prev, dup := peerModeBus[pm]; dup && prev != ra.BusID {
			return fmt.Errorf("peer %s's %s is attached to more than one bus (%q and %q)", peerLabel(p), modeLabel(ra.Mode), prev, ra.BusID)
		}
		peerModeBus[pm] = ra.BusID

		remoteModesByBus[ra.BusID] = append(remoteModesByBus[ra.BusID], ra.Mode)
		if peersByBus[ra.BusID] == nil {
			peersByBus[ra.BusID] = make(map[string]bool)
		}
		peersByBus[ra.BusID][ra.PeerID] = true
	}

	// Per-bus union mode-set validity + node-count cap, in stable bus order.
	busIDs := unionBusIDs(localModesByBus, remoteModesByBus)
	for _, id := range busIDs {
		modes := append(append([]Mode(nil), localModesByBus[id]...), remoteModesByBus[id]...)
		if ok, reason := busModeSetReason(modes); !ok {
			return fmt.Errorf("bus %q: %s", id, reason)
		}
		nodes := len(peersByBus[id])
		if localParticipates[id] {
			nodes++
		}
		if nodes > maxBusNodes {
			return fmt.Errorf("bus %q spans %d nodes; v1 caps a bus at %d nodes (validator policy, RFC-0016 §2 — the wire protocol is uncapped)", id, nodes, maxBusNodes)
		}
	}
	return nil
}

// RemoteAttachmentState reports, for one remote attachment, whether it is ACTIVE
// (its peer is paired) or dormant, and why. Revoking or un-pairing a peer makes
// its dependent remote attachments dormant WITHOUT deleting them (RFC-0016 §4);
// this is the "clear returned state" the revocation contract promises.
type RemoteAttachmentState struct {
	Attachment RemoteAttachment `json:"attachment"`
	Active     bool             `json:"active"`
	Reason     string           `json:"reason,omitempty"` // why it is dormant (empty when active)
}

// RemoteAttachmentStates classifies every remote attachment against the current
// peer states. An attachment is active iff its peer exists and is paired.
func RemoteAttachmentStates(peers []Peer, remote []RemoteAttachment) []RemoteAttachmentState {
	byID := make(map[string]Peer, len(peers))
	for _, p := range peers {
		byID[p.ID] = p
	}
	out := make([]RemoteAttachmentState, 0, len(remote))
	for _, ra := range remote {
		st := RemoteAttachmentState{Attachment: ra}
		p, ok := byID[ra.PeerID]
		switch {
		case !ok:
			st.Reason = fmt.Sprintf("peer %q no longer exists", ra.PeerID)
		case p.State == PeerPaired:
			st.Active = true
		default:
			st.Reason = fmt.Sprintf("peer %s is %s (not paired)", peerLabel(p), p.State)
		}
		out = append(out, st)
	}
	return out
}

// SetPeers writes the peers[] section with the write-only secret rule (RFC-0002):
// a blank Certificate / PrivateKey keeps the stored one, a non-blank one replaces
// it. The one exception is the RFC-0016 §3 re-pairing rule: when a peer leaves the
// Revoked state (revoked -> pending/paired), its old key material is NOT carried
// forward — re-pairing mints fresh material, never reuses the revoked keys — so a
// blank field on that transition clears the secret rather than preserving it.
func SetPeers(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var incoming []Peer
	if err := dec.Decode(&incoming); err != nil {
		return err
	}

	var existing []Peer
	if _, err := s.GetInto("peers", &existing); err != nil {
		return err
	}
	prior := make(map[string]Peer, len(existing))
	for _, p := range existing {
		prior[p.ID] = p
	}
	for i := range incoming {
		old, had := prior[incoming[i].ID]
		// Re-pairing a revoked peer discards the old material (never reuse it).
		rePairing := had && old.State == PeerRevoked && incoming[i].State != PeerRevoked
		if incoming[i].Certificate == "" && !rePairing {
			incoming[i].Certificate = old.Certificate
		}
		if incoming[i].PrivateKey == "" && !rePairing {
			incoming[i].PrivateKey = old.PrivateKey
		}
	}

	if err := ValidatePeers(incoming); err != nil {
		return err
	}
	// Peer state changes never reject dependent remote attachments (the structural
	// validator is state-independent), so revoking a peer here always succeeds and
	// its remote attachments are retained (reported dormant by RemoteAttachmentStates).
	var buses []Bus
	if _, err := s.GetInto("buses", &buses); err != nil {
		return err
	}
	var local []Attachment
	if _, err := s.GetInto("attachments", &local); err != nil {
		return err
	}
	var remote []RemoteAttachment
	if _, err := s.GetInto("remote_attachments", &remote); err != nil {
		return err
	}
	if err := ValidateRemoteAttachments(buses, local, remote, incoming); err != nil {
		return err
	}
	return s.Set("peers", incoming, by)
}

// SetRemoteAttachments writes the remote_attachments[] section through the
// peering validator. Beyond the structural rules, it enforces the RFC-0016 §Store
// add-time gate: a remote attachment that is NEW (not already stored) must
// reference a peer in state paired ("peer not paired"). Existing rows are
// grandfathered so that a peer revoked after the fact does not make its retained
// remote attachments un-writable — they persist, dormant.
func SetRemoteAttachments(s *store.Store, raw []byte, by string) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var incoming []RemoteAttachment
	if err := dec.Decode(&incoming); err != nil {
		return err
	}

	var buses []Bus
	if _, err := s.GetInto("buses", &buses); err != nil {
		return err
	}
	var local []Attachment
	if _, err := s.GetInto("attachments", &local); err != nil {
		return err
	}
	var peers []Peer
	if _, err := s.GetInto("peers", &peers); err != nil {
		return err
	}
	if err := ValidateRemoteAttachments(buses, local, incoming, peers); err != nil {
		return err
	}

	// Add-time pairing gate for NEW rows only (RFC-0016 §Store: "requires ... the
	// peer to be in state paired"). Existing rows are grandfathered so revocation
	// preserves them.
	var stored []RemoteAttachment
	if _, err := s.GetInto("remote_attachments", &stored); err != nil {
		return err
	}
	existing := make(map[string]bool, len(stored))
	for _, ra := range stored {
		existing[remoteKey(ra)] = true
	}
	peerByID := make(map[string]Peer, len(peers))
	for _, p := range peers {
		peerByID[p.ID] = p
	}
	for _, ra := range incoming {
		if existing[remoteKey(ra)] {
			continue // grandfathered
		}
		p := peerByID[ra.PeerID] // existence already checked by the validator
		if p.State != PeerPaired {
			return fmt.Errorf("remote attachment for %s: peer %s not paired (state %s)", modeLabel(ra.Mode), peerLabel(p), p.State)
		}
	}
	return s.Set("remote_attachments", incoming, by)
}

func remoteKey(ra RemoteAttachment) string {
	return ra.BusID + "|" + ra.PeerID + "|" + string(ra.Mode)
}

// peerLabel names a peer in a refusal string — its display name if set, else its id.
func peerLabel(p Peer) string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// unionBusIDs returns the sorted union of the bus ids keying two mode maps.
func unionBusIDs(a, b map[string][]Mode) []string {
	seen := make(map[string]bool, len(a)+len(b))
	for id := range a {
		seen[id] = true
	}
	for id := range b {
		seen[id] = true
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
