package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/peering"
	"github.com/KN4OQW/waypoint/internal/store"
)

// runResetPeerIdentity implements the `waypointd reset-peer-identity` subcommand
// (RFC-0016 §3, amended): regenerate the node's peering keypair and mark every
// pairing revoked (rows retained). Because trust is pinned to this node's
// certificate, minting a new keypair invalidates every existing pairing at once —
// each peer's next handshake fails until re-paired. This is the reset-claim-lineage
// escape hatch for a compromised node key or a re-homed box, distinct from
// per-peer revocation. It runs directly against the store for an operator with a
// shell on the device, and prints what it did. Returns a process exit code.
func runResetPeerIdentity(args []string) int {
	fs := flag.NewFlagSet("reset-peer-identity", flag.ExitOnError)
	storePath := fs.String("store", "/home/pi-star/waypoint/config.db", "path to the SQLite configuration store")
	peeringDir := fs.String("peering-dir", "/home/pi-star/waypoint/peering", "directory holding the node's peering keypair (node.key/node.crt)")
	_ = fs.Parse(args)

	st, err := store.Open(*storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: open store %s: %v\n", *storePath, err)
		return 1
	}
	defer st.Close()

	m, err := config.Load(st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: load store: %v\n", err)
		return 1
	}

	// Regenerate the node keypair. The cert CN is cosmetic (trust is pinning); use
	// the station callsign when set so the fingerprint is recognisable.
	name := m.General.Callsign
	if name == "" {
		name = "waypoint-node"
	}
	certPEM, keyPEM, err := peering.GenerateKeypair(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: generate keypair: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(*peeringDir, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: mkdir %s: %v\n", *peeringDir, err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(*peeringDir, "node.crt"), []byte(certPEM), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: write node.crt: %v\n", err)
		return 1
	}
	if err := os.WriteFile(filepath.Join(*peeringDir, "node.key"), []byte(keyPEM), 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: write node.key: %v\n", err)
		return 1
	}
	fp, _ := peering.Fingerprint(certPEM)

	// Mark every pairing revoked; keep the rows (RFC-0016 §3: rows retained). Blank
	// cert fields are preserved by SetPeers' write-only merge, so the pinned certs
	// stay on disk for a later re-pair while the state flips to revoked.
	revoked := 0
	for i := range m.Peers {
		if m.Peers[i].State != config.PeerRevoked {
			m.Peers[i].State = config.PeerRevoked
			revoked++
		}
	}
	raw, err := json.Marshal(m.Peers)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: marshal peers: %v\n", err)
		return 1
	}
	if err := config.SetPeers(st, raw, "reset-peer-identity"); err != nil {
		fmt.Fprintf(os.Stderr, "reset-peer-identity: revoke pairings: %v\n", err)
		return 1
	}

	fmt.Printf("reset-peer-identity: regenerated node keypair in %s (fingerprint %s); revoked %d pairing(s), rows retained. Re-pair each peer to restore the link.\n",
		*peeringDir, fp, revoked)
	return 0
}
