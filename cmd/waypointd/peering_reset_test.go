package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

// TestResetPeerIdentity: the subcommand regenerates the node keypair on disk and
// marks every pairing revoked while retaining the rows (RFC-0016 §3, amended).
func TestResetPeerIdentity(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "config.db")
	peeringDir := filepath.Join(dir, "peering")

	st, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	peers := []config.Peer{
		{ID: "garage", Name: "garage", State: config.PeerPaired, Fingerprint: "AA:BB", Certificate: "-----BEGIN CERTIFICATE-----x-----END CERTIFICATE-----"},
		{ID: "shack", Name: "shack", State: config.PeerPaired, Fingerprint: "CC:DD", Certificate: "-----BEGIN CERTIFICATE-----y-----END CERTIFICATE-----"},
	}
	raw, _ := json.Marshal(peers)
	if err := config.SetPeers(st, raw, "seed"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	if code := runResetPeerIdentity([]string{"-store", storePath, "-peering-dir", peeringDir}); code != 0 {
		t.Fatalf("reset-peer-identity exit code = %d, want 0", code)
	}

	// A fresh keypair was written.
	for _, f := range []string{"node.crt", "node.key"} {
		if _, err := os.Stat(filepath.Join(peeringDir, f)); err != nil {
			t.Fatalf("expected %s minted: %v", f, err)
		}
	}

	// Every pairing is revoked; the rows (and their certs) survive.
	st2, err := store.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	m, err := config.Load(st2)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Peers) != 2 {
		t.Fatalf("pairing rows must be retained, got %d", len(m.Peers))
	}
	for _, p := range m.Peers {
		if p.State != config.PeerRevoked {
			t.Fatalf("peer %s state = %q, want revoked", p.ID, p.State)
		}
		if p.Certificate == "" {
			t.Fatalf("peer %s cert should be retained for a later re-pair", p.ID)
		}
	}
}
