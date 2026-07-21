package peering

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
)

func newManager(t *testing.T, node string) (*Manager, net.Listener) {
	t.Helper()
	cert, key, err := GenerateKeypair(node)
	if err != nil {
		t.Fatal(err)
	}
	st := openMemStore(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, Identity{NodeID: node, Name: node, CertPEM: cert, KeyPEM: key}, node, node, ln.Addr().String(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	go m.Serve(ctx, ln)
	t.Cleanup(cancel)
	return m, ln
}

func paired(t *testing.T, m *Manager, peerID string) bool {
	peers, err := loadPeers(m.store)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range peers {
		if p.ID == peerID {
			return p.State == config.PeerPaired && p.Certificate != ""
		}
	}
	return false
}

func poll(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// TestManagerHappyPathPairsBothStores drives a full pairing between two managers
// over loopback and asserts each store ends with the OTHER peer paired.
func TestManagerHappyPathPairsBothStores(t *testing.T) {
	shack, _ := newManager(t, "shack")
	garage, garageLn := newManager(t, "garage")

	sid, code, err := shack.InitiatePairing(garageLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	// garage sees the incoming session; operator enters the code from shack's screen
	if !poll(t, func() bool { return len(garage.Pending()) > 0 }) {
		t.Fatal("garage never registered the incoming pairing")
	}
	if err := garage.ConfirmPairing(sid, code); err != nil {
		t.Fatalf("garage confirm: %v", err)
	}
	// garage's confirm sends its certificate back to shack asynchronously; shack can
	// only confirm once that has arrived (its pending session then shows the peer
	// fingerprint). Waiting on the readiness signal rather than assuming instant
	// delivery keeps the test deterministic on a slow/contended runner.
	if !poll(t, func() bool {
		for _, p := range shack.Pending() {
			if p.SID == sid && p.Fingerprint != "" {
				return true
			}
		}
		return false
	}) {
		t.Fatal("shack never received garage's certificate")
	}
	if err := shack.ConfirmPairing(sid, ""); err != nil {
		t.Fatalf("shack confirm: %v", err)
	}

	if !poll(t, func() bool { return paired(t, shack, "garage") && paired(t, garage, "shack") }) {
		t.Fatal("both stores should end with the peer paired")
	}
}

// TestManagerWrongCodeNoResidue: the responder enters the wrong code; NEITHER store
// gains a peer row (zero trust residue through the full stack).
func TestManagerWrongCodeNoResidue(t *testing.T) {
	shack, _ := newManager(t, "shack")
	garage, garageLn := newManager(t, "garage")

	sid, code, err := shack.InitiatePairing(garageLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	wrong := "654321"
	if wrong == code {
		wrong = "123456"
	}
	poll(t, func() bool { return len(garage.Pending()) > 0 })
	garage.ConfirmPairing(sid, wrong)
	shack.ConfirmPairing(sid, "")

	time.Sleep(200 * time.Millisecond) // let any (wrong) persistence attempt happen
	sp, _ := loadPeers(shack.store)
	gp, _ := loadPeers(garage.store)
	if len(sp) != 0 || len(gp) != 0 {
		t.Fatalf("wrong code must leave no peer rows: shack=%d garage=%d", len(sp), len(gp))
	}
}
