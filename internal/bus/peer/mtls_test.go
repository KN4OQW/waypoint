package peer

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

// genPeerCert mints a self-signed peering keypair (as the pairing layer will).
func genPeerCert(t *testing.T, cn string) (tls.Certificate, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	parsed, _ := x509.ParseCertificate(der)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: parsed}, parsed
}

// TestMTLSPinnedHandshake: a mutual-TLS 1.3 session establishes only when each
// side presents the certificate the other pinned at pairing; a stranger's cert is
// rejected. A peer Message flows over the established *tls.Conn via Session.
func TestMTLSPinnedHandshake(t *testing.T) {
	shackCert, shackX509 := genPeerCert(t, "shack")
	garageCert, garageX509 := genPeerCert(t, "garage")

	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerConfig(shackCert, garageX509))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// server side: accept, run a Session, expect a Hello
	got := make(chan Message, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		s := NewSession(conn, "garage", 0)
		s.Start(time.Hour)
		select {
		case m := <-s.Recv():
			got <- m
		case <-time.After(2 * time.Second):
		}
	}()

	// client side: dial pinned to the server's cert, send a Hello
	conn, err := tls.Dial("tcp", ln.Addr().String(), ClientConfig(garageCert, shackX509))
	if err != nil {
		t.Fatalf("pinned dial should succeed: %v", err)
	}
	if conn.ConnectionState().Version != tls.VersionTLS13 {
		t.Fatal("session must be TLS 1.3")
	}
	cs := NewSession(conn, "shack", 0)
	cs.Start(time.Hour)
	cs.Send(Message{Type: MsgHello, Hello: &Hello{NodeID: "garage", BusID: "A", Role: RoleMember}})

	select {
	case m := <-got:
		if m.Type != MsgHello || m.Hello.NodeID != "garage" {
			t.Fatalf("unexpected first message: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("server never received the Hello over the pinned session")
	}
	cs.Close()
}

func TestMTLSRejectsUnpinnedCert(t *testing.T) {
	shackCert, shackX509 := genPeerCert(t, "shack")
	garageCert, _ := genPeerCert(t, "garage")
	strangerCert, _ := genPeerCert(t, "stranger")

	// server pins ONLY garage; a stranger presents a different cert
	ln, err := tls.Listen("tcp", "127.0.0.1:0", ServerConfig(shackCert, garageCert.Leaf))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.(*tls.Conn).Handshake()
			c.Close()
		}
	}()

	conn, err := tls.Dial("tcp", ln.Addr().String(), ClientConfig(strangerCert, shackX509))
	if err != nil {
		return // rejected at the handshake — good
	}
	// In TLS 1.3 a client-cert rejection surfaces on the first I/O (the server
	// aborts after verifying the client cert), so a read must fail.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, rerr := conn.Read(make([]byte, 1)); rerr == nil {
		conn.Close()
		t.Fatal("a stranger's (unpinned) certificate must be rejected by the pin")
	}
	conn.Close()
	_ = net.Dialer{}
}
