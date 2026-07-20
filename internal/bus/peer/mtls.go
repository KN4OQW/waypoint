package peer

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
)

// mtls.go is the thin TLS shell (RFC-0016 decision 2 / §Security posture). Trust
// is PINNED to the paired peer's certificate — the pairing record IS the trust
// root, NOT a CA pool. TLS 1.3 only, mutual auth. The peering keypair is separate
// from the RFC-0012 HTTPS device cert (loaded from PeeringDir), so rotating one
// never touches the other. These builders are pure functions of already-loaded
// certs; the daemon loads the files (0600) the Prompt-9 render referenced by path.

// ClientConfig builds the dialing side's TLS config: present our peering cert and
// pin the server to the paired peer's certificate.
func ClientConfig(myCert tls.Certificate, pinnedPeer *x509.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:            tls.VersionTLS13,
		MaxVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{myCert},
		InsecureSkipVerify:    true, // we verify by pinning below, not by CA chain
		VerifyPeerCertificate: pinVerifier(pinnedPeer),
	}
}

// ServerConfig builds the listening side's TLS config: present our peering cert,
// require a client cert, and pin it to one of the paired peers' certificates.
func ServerConfig(myCert tls.Certificate, pinnedPeers ...*x509.Certificate) *tls.Config {
	return &tls.Config{
		MinVersion:            tls.VersionTLS13,
		MaxVersion:            tls.VersionTLS13,
		Certificates:          []tls.Certificate{myCert},
		ClientAuth:            tls.RequireAnyClientCert,
		VerifyPeerCertificate: pinVerifier(pinnedPeers...),
	}
}

// pinVerifier returns a VerifyPeerCertificate that accepts iff the peer presented
// EXACTLY one of the pinned certificates (byte-equal DER). No CA chain, no name
// check — the pairing exchange established this trust (RFC-0016 §3).
func pinVerifier(pinned ...*x509.Certificate) func([][]byte, [][]*x509.Certificate) error {
	wants := make([][]byte, 0, len(pinned))
	for _, c := range pinned {
		if c != nil {
			wants = append(wants, c.Raw)
		}
	}
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return errors.New("peer: no certificate presented")
		}
		for _, w := range wants {
			if bytes.Equal(rawCerts[0], w) {
				return nil
			}
		}
		return errors.New("peer: presented certificate is not the pinned pairing certificate")
	}
}

// LoadKeyPair loads this node's peering certificate + private key from the files
// the render referenced (PeeringDir/node.crt, PeeringDir/node.key). It is a plain
// PEM keypair load; the crypto is standard, the trust is the pinning above.
func LoadKeyPair(certPath, keyPath string) (tls.Certificate, error) {
	c, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("peer: load peering keypair: %w", err)
	}
	return c, nil
}

// LoadPinnedCert loads a single pinned peer certificate (PeeringDir/peer-<id>.crt
// or owner-<bus>.crt) as an *x509.Certificate for VerifyPeerCertificate.
func LoadPinnedCert(path string) (*x509.Certificate, error) {
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseCertPEM(pemBytes)
}

func parseCertPEM(pemBytes []byte) (*x509.Certificate, error) {
	for {
		var blk *pem.Block
		blk, pemBytes = pem.Decode(pemBytes)
		if blk == nil {
			return nil, errors.New("peer: no CERTIFICATE block in PEM")
		}
		if blk.Type == "CERTIFICATE" {
			return x509.ParseCertificate(blk.Bytes)
		}
	}
}
