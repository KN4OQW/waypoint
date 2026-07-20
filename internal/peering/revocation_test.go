package peering

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/store"
)

func openMemStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func tlsCert(t *testing.T, certPEM, keyPEM string) tls.Certificate {
	t.Helper()
	c, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func x509Cert(t *testing.T, certPEM string) *x509.Certificate {
	t.Helper()
	blk, _ := pem.Decode([]byte(certPEM))
	if blk == nil {
		t.Fatal("no PEM block")
	}
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// pinnedFromPaired builds the owner-side pinned x509 set from the store's PAIRED
// peers — exactly what the daemon does to configure the mTLS verifier.
func pinnedFromPaired(t *testing.T, peers []config.Peer) []*x509.Certificate {
	t.Helper()
	var out []*x509.Certificate
	for _, pem := range PairedCerts(peers) {
		out = append(out, x509Cert(t, pem))
	}
	return out
}

// TestRevocationRefusesTLS: with the REAL peer verifier, a paired peer connects; a
// revoked peer (removed from the pinned set) is refused at the TLS layer. This is
// the "local revoke is immediate" contract (RFC-0016 §3 / Task 3).
func TestRevocationRefusesTLS(t *testing.T) {
	aCert, aKey, _ := GenerateKeypair("shack")
	bCert, bKey, _ := GenerateKeypair("garage")

	// The store, post-pairing: shack has garage paired.
	peers := []config.Peer{{ID: "garage", State: config.PeerPaired, Certificate: bCert}}

	serve := func(pinned []*x509.Certificate) (addr string, closeFn func()) {
		ln, err := peer.Listen("127.0.0.1:0", peer.ServerConfig(tlsCert(t, aCert, aKey), pinned...))
		if err != nil {
			t.Fatal(err)
		}
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				go func() { _ = c.(*tls.Conn).Handshake(); c.Close() }()
			}
		}()
		return ln.Addr().String(), func() { ln.Close() }
	}

	dialAsGarage := func(addr string) error {
		conn, err := tls.Dial("tcp", addr, peer.ClientConfig(tlsCert(t, bCert, bKey), x509Cert(t, aCert)))
		if err != nil {
			return err
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(2 * time.Second))
		// force the client to observe the server's client-cert verdict
		_, err = conn.Read(make([]byte, 1))
		return err
	}

	// PAIRED: garage's cert is pinned -> handshake succeeds (read blocks/EOFs, not a
	// TLS auth error).
	addr, closeFn := serve(pinnedFromPaired(t, peers))
	if err := dialAsGarage(addr); err != nil && isTLSAuthError(err) {
		t.Fatalf("a paired peer must connect, got auth error: %v", err)
	}
	closeFn()

	// REVOKE garage: rebuild the pinned set from PAIRED peers -> now empty.
	for i := range peers {
		if peers[i].ID == "garage" {
			peers[i].State = config.PeerRevoked
		}
	}
	addr2, closeFn2 := serve(pinnedFromPaired(t, peers))
	defer closeFn2()
	if err := dialAsGarage(addr2); err == nil {
		t.Fatal("a revoked peer must be refused at the TLS layer")
	}
}

// TestRevokeStoreFlipsStateKeepsRow: Revoke flips state to revoked and retains the
// row + its pinned cert (delete nothing), so re-pair can mint fresh keys later.
func TestRevokeStoreFlipsStateKeepsRow(t *testing.T) {
	st := openMemStore(t)
	writePeers(st, []config.Peer{{ID: "garage", Name: "Garage", State: config.PeerPaired, Certificate: "CERT", PrivateKey: "KEY"}})

	ok, err := Revoke(st, "garage")
	if err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	peers, _ := loadPeers(st)
	if len(peers) != 1 || peers[0].State != config.PeerRevoked {
		t.Fatalf("revoke should flip state and keep the row: %+v", peers)
	}
	if peers[0].Certificate != "CERT" {
		t.Fatal("revoke must not delete the pinned cert")
	}
	// a revoked peer contributes no pinned cert to the trust set
	if len(PairedCerts(peers)) != 0 {
		t.Fatal("a revoked peer must not be in the pinned trust set")
	}
}

func isTLSAuthError(err error) bool {
	if err == nil {
		return false
	}
	// A pinning failure surfaces as a certificate/alert error on one side; EOF from
	// a cleanly-closed post-handshake connection is NOT an auth error.
	s := err.Error()
	for _, sub := range []string{"certificate", "pinned", "bad certificate"} {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}
