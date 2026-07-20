// Package peering implements RFC-0016 decision 3: mDNS discovery, the mutual
// short-code pairing handshake, certificate exchange into the Prompt-8 peer store
// rows, and revocation. It is API-complete; the UI lands in Prompt 12.
//
// The security core — the pairing handshake — is a pure state machine (handshake.go)
// with no sockets, exhaustively table-tested. The mDNS layer is behind an interface
// (discovery.go) so tests run without multicast, and the network exchange + store
// writes are a thin shell (manager.go).
package peering

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// GenerateKeypair mints a fresh ECDSA P-256 peering keypair and a self-signed
// certificate for the node (RFC-0016 §Security posture: a keypair SEPARATE from
// RFC-0012's HTTPS device cert, with its own trust anchor established by pairing —
// not a public CA). Returns PEM cert + PEM key for the Prompt-8 store rows.
//
// A fresh keypair per call is the "re-pair mints fresh keys" rule (Prompt 8): a
// caller re-pairing a revoked peer generates a new one rather than reusing the old.
func GenerateKeypair(nodeName string) (certPEM, keyPEM string, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", fmt.Errorf("peering: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "waypoint-peer:" + nodeName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(10 * 365 * 24 * time.Hour), // long-lived; trust is pinning + revocation, not expiry
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return "", "", fmt.Errorf("peering: create cert: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", "", err
	}
	certPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	return certPEM, keyPEM, nil
}

// certDER extracts the DER bytes of the first CERTIFICATE block in a PEM string —
// the canonical form the handshake transcript and the fingerprint hash over.
func certDER(certPEM string) ([]byte, error) {
	rest := []byte(certPEM)
	for {
		var blk *pem.Block
		blk, rest = pem.Decode(rest)
		if blk == nil {
			return nil, errors.New("peering: no CERTIFICATE block in PEM")
		}
		if blk.Type == "CERTIFICATE" {
			if _, err := x509.ParseCertificate(blk.Bytes); err != nil {
				return nil, fmt.Errorf("peering: parse cert: %w", err)
			}
			return blk.Bytes, nil
		}
	}
}

// Fingerprint returns the human-comparable SHA-256 fingerprint of a certificate
// PEM: uppercase hex in colon-separated pairs (the form shown on the pairing
// screen for out-of-band verification, RFC-0016 §3). It is public — safe to
// display and store (the Prompt-8 view surfaces it).
func Fingerprint(certPEM string) (string, error) {
	der, err := certDER(certPEM)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":"), nil
}
