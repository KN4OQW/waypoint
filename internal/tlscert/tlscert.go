// Package tlscert owns Waypoint's device TLS certificate (RFC-0012 / issue #11):
// a per-device, self-signed cert minted once and reused across restarts, so the
// dashboard is HTTPS out of the box and a returning phone re-trusts nothing. The
// cert is the operator's trust anchor — pinned once via the browser's one-time
// self-signed prompt — so it is per-device (never a shared bundled key) and
// long-lived (rotation is a reflash, not a renewal cadence).
package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
	validity = 10 * 365 * 24 * time.Hour // ~10 years: an appliance leaf, not a renewal cadence
)

// LoadOrCreate returns the device certificate, generating and persisting a fresh
// self-signed one under dir the first time (key mode 0600) and loading the same
// one on every subsequent call. now/hostname/interfaceAddrs are injected only so
// tests can pin them; the production call is LoadOrCreateDefault.
func LoadOrCreate(dir string, now time.Time, hostname string, ipAddrs []net.IP) (tls.Certificate, error) {
	cp := filepath.Join(dir, certFile)
	kp := filepath.Join(dir, keyFile)
	if fileExists(cp) && fileExists(kp) {
		cert, err := tls.LoadX509KeyPair(cp, kp)
		if err == nil {
			return cert, nil
		}
		// A corrupt/unreadable pair is regenerated rather than bricking TLS startup;
		// the operator re-trusts once (the old cert was unusable anyway).
	}
	return create(dir, now, hostname, ipAddrs)
}

// LoadOrCreateDefault is LoadOrCreate with the live clock, OS hostname, and the
// host's current non-loopback IPs.
func LoadOrCreateDefault(dir string) (tls.Certificate, error) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "waypoint"
	}
	return LoadOrCreate(dir, time.Now(), host, hostIPs())
}

func create(dir string, now time.Time, hostname string, ipAddrs []net.IP) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: serial: %w", err)
	}

	// DNS SANs: how the box is actually reached. IP SANs let https://<ip>/ validate
	// against the same cert the phone trusted, so the trust prompt is one-time even
	// when the operator connects by address.
	dns := dedup([]string{"localhost", "waypoint.local", hostname, hostname + ".local"})
	ips := append([]net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback}, ipAddrs...)

	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: hostname, Organization: []string{"Waypoint"}},
		NotBefore:             now.Add(-1 * time.Hour), // tolerate mild clock skew on a just-booted Pi
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true, // self-signed: it is its own issuer
		DNSNames:              dns,
		IPAddresses:           dedupIPs(ips),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tlscert: create certificate: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := writeFile(filepath.Join(dir, certFile), certPEM, 0o644); err != nil {
		return tls.Certificate{}, err
	}
	if err := writeFile(filepath.Join(dir, keyFile), keyPEM, 0o600); err != nil { // key is private
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}

// hostIPs returns the host's current non-loopback unicast IPs (v4 and v6).
func hostIPs() []net.IP {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}
	var out []net.IP
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		out = append(out, ip)
	}
	return out
}

func writeFile(path string, data []byte, mode os.FileMode) error {
	// Write via a temp file + rename so a crash never leaves a half-written cert/key.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tlscert-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func dedupIPs(in []net.IP) []net.IP {
	seen := map[string]bool{}
	var out []net.IP
	for _, ip := range in {
		if ip == nil {
			continue
		}
		k := ip.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, ip)
	}
	return out
}
