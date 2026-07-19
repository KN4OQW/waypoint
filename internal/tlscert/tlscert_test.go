package tlscert

import (
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)

// Property 1: LoadOrCreate on an empty dir produces a valid self-signed cert with
// the expected SANs, ServerAuth usage, and multi-year validity.
func TestCreate(t *testing.T) {
	dir := t.TempDir()
	cert, err := LoadOrCreate(dir, t0, "hs-shack", []net.IP{net.ParseIP("192.168.1.50")})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}

	// SANs: localhost, waypoint.local, the hostname, and the IP.
	for _, want := range []string{"localhost", "waypoint.local", "hs-shack", "hs-shack.local"} {
		if !contains(leaf.DNSNames, want) {
			t.Errorf("DNS SAN %q missing from %v", want, leaf.DNSNames)
		}
	}
	if !hasIP(leaf.IPAddresses, "192.168.1.50") {
		t.Errorf("IP SAN 192.168.1.50 missing from %v", leaf.IPAddresses)
	}
	if !hasIP(leaf.IPAddresses, "127.0.0.1") {
		t.Errorf("loopback IP SAN missing")
	}

	// Usage + validity.
	if !hasExtUsage(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
		t.Errorf("cert missing ServerAuth ext key usage")
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) < 5*365*24*time.Hour {
		t.Errorf("cert validity too short: %v", leaf.NotAfter.Sub(leaf.NotBefore))
	}
	// It validates against itself (self-signed CA).
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "waypoint.local", CurrentTime: t0.Add(time.Hour)}); err != nil {
		t.Errorf("self-signed cert does not verify against itself: %v", err)
	}
}

// Property 2: a second LoadOrCreate returns the SAME cert (no re-mint), so a
// restart never invalidates the operator's one-time trust.
func TestLoadOrCreateIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir, t0, "n", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := LoadOrCreate(dir, t0.Add(48*time.Hour), "n", nil)
	if err != nil {
		t.Fatal(err)
	}
	l1, _ := x509.ParseCertificate(first.Certificate[0])
	l2, _ := x509.ParseCertificate(second.Certificate[0])
	if l1.SerialNumber.Cmp(l2.SerialNumber) != 0 {
		t.Errorf("second LoadOrCreate re-minted the cert (serials differ) — restart would re-prompt trust")
	}
}

// Property 3: the persisted key file is mode 0600.
func TestKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir, t0, "n", nil); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}
	// The cert (public) is world-readable.
	cfi, _ := os.Stat(filepath.Join(dir, certFile))
	if cfi.Mode().Perm() != 0o644 {
		t.Errorf("cert file mode = %o, want 644", cfi.Mode().Perm())
	}
}

func contains(xs []string, x string) bool {
	for _, s := range xs {
		if s == x {
			return true
		}
	}
	return false
}
func hasIP(ips []net.IP, s string) bool {
	for _, ip := range ips {
		if ip.String() == s {
			return true
		}
	}
	return false
}
func hasExtUsage(us []x509.ExtKeyUsage, want x509.ExtKeyUsage) bool {
	for _, u := range us {
		if u == want {
			return true
		}
	}
	return false
}
