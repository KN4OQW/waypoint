package peering

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

// store.go bridges the pairing result to the Prompt-8 peer store rows. It reuses
// config.SetPeers so the write-only secret rule and the re-pair-mints-fresh-keys
// transition are enforced in one place — this file never re-implements them.

// ApplyPairing writes a successful handshake Result as a PAIRED peer row: the
// pinned peer certificate, this node's per-pairing key, the viewable fingerprint,
// and the peer's address. Re-pairing a previously-revoked peer flows through
// SetPeers, which discards the old key material (Prompt 8) — fresh keys, never
// reused.
func ApplyPairing(s *store.Store, res *Result, host string, port int, myKeyPEM string) error {
	peers, err := loadPeers(s)
	if err != nil {
		return err
	}
	row := config.Peer{
		ID: res.NodeID, Name: res.Name, Host: host, Port: strconv.Itoa(port),
		State: config.PeerPaired, Fingerprint: res.Fingerprint,
		Certificate: res.CertPEM, PrivateKey: myKeyPEM,
	}
	peers = upsertPeer(peers, row)
	return writePeers(s, peers)
}

// Revoke flips a peer to the revoked state, RETAINING the row (RFC-0001
// disable-preserves-data / Prompt 8). Revocation is immediate locally: the daemon
// rebuilds its pinned-cert set from PAIRED peers only, so a revoked peer's cert is
// no longer trusted and its next TLS handshake is refused. Returns false if there
// is no such peer.
func Revoke(s *store.Store, peerID string) (bool, error) {
	peers, err := loadPeers(s)
	if err != nil {
		return false, err
	}
	found := false
	for i := range peers {
		if peers[i].ID == peerID {
			peers[i].State = config.PeerRevoked
			found = true
		}
	}
	if !found {
		return false, nil
	}
	return true, writePeers(s, peers)
}

// PairedCerts returns the pinned certificate PEMs of every PAIRED peer — the trust
// set the transport's mTLS verifier pins against. A revoked or pending peer is
// excluded, which is exactly what makes revocation refuse connections.
func PairedCerts(peers []config.Peer) []string {
	var out []string
	for _, p := range peers {
		if p.State == config.PeerPaired && p.Certificate != "" {
			out = append(out, p.Certificate)
		}
	}
	return out
}

func loadPeers(s *store.Store) ([]config.Peer, error) {
	var peers []config.Peer
	if _, err := s.GetInto("peers", &peers); err != nil {
		return nil, err
	}
	return peers, nil
}

func upsertPeer(peers []config.Peer, row config.Peer) []config.Peer {
	for i := range peers {
		if peers[i].ID == row.ID {
			peers[i] = row
			return peers
		}
	}
	return append(peers, row)
}

func writePeers(s *store.Store, peers []config.Peer) error {
	raw, err := json.Marshal(peers)
	if err != nil {
		return err
	}
	if err := config.SetPeers(s, raw, "pairing"); err != nil {
		return fmt.Errorf("peering: persist peers: %w", err)
	}
	return nil
}
