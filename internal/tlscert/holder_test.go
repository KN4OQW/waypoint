package tlscert

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"testing"
	"time"
)

func newHolder(t *testing.T) *Holder {
	t.Helper()
	return &Holder{
		Dir:     t.TempDir(),
		Now:     func() time.Time { return time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC) },
		HostIPs: func() []net.IP { return []net.IP{net.ParseIP("192.168.1.50")} },
		Logf:    t.Logf,
	}
}

func leaf(t *testing.T, h *Holder) *x509.Certificate {
	t.Helper()
	cert, err := h.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	l, err := leafOf(*cert)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// Property 1: the certificate names the hostname the operator chose, in both the
// bare and the mDNS form.
//
// Both, because a node is reached by both: `https://hs-shack/` on a network with
// a working DNS search, `https://hs-shack.local/` everywhere else — and the
// dashboard tells the operator to use the second one.
func TestEnsureNamesTheChosenHostname(t *testing.T) {
	h := newHolder(t)
	regenerated, err := h.Ensure("hs-shack")
	if err != nil {
		t.Fatal(err)
	}
	if !regenerated {
		t.Error("the first Ensure did not generate a certificate")
	}

	l := leaf(t, h)
	if err := l.VerifyHostname("hs-shack"); err != nil {
		t.Errorf("the certificate does not name hs-shack: %v", err)
	}
	if err := l.VerifyHostname("hs-shack.local"); err != nil {
		t.Errorf("the certificate does not name hs-shack.local: %v", err)
	}
	if err := l.VerifyHostname("192.168.1.50"); err != nil {
		t.Errorf("the certificate does not name the node's address: %v", err)
	}
}

// Property 2: the key is ECDSA P-256.
//
// Pinned by a test rather than left to the generator: the certificate is the
// operator's trust anchor for the life of the node, and quietly acquiring a
// different key type — or an RSA key on a Pi Zero, where the handshake cost is
// visible — is the kind of change that would otherwise pass review unnoticed.
func TestKeyIsECDSAP256(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("hs-shack"); err != nil {
		t.Fatal(err)
	}
	l := leaf(t, h)

	pub, ok := l.PublicKey.(*ecdsa.PublicKey)
	if !ok {
		t.Fatalf("public key is %T, want *ecdsa.PublicKey", l.PublicKey)
	}
	if pub.Curve != elliptic.P256() {
		t.Errorf("curve = %v, want P-256", pub.Curve.Params().Name)
	}
	if l.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		t.Errorf("signature algorithm = %v, want ECDSA-SHA256", l.SignatureAlgorithm)
	}
}

// Property 3: a certificate that already names the hostname is not regenerated.
//
// This is the acceptance criterion "no cert regen after first LAN boot", and it
// matters beyond tidiness: a new certificate on every boot means a new trust
// prompt on every boot, which trains an operator to click through certificate
// warnings — the opposite of what a pinned self-signed certificate is for.
func TestEnsureDoesNotRegenerateForTheSameHostname(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("hs-shack"); err != nil {
		t.Fatal(err)
	}
	first := leaf(t, h).SerialNumber.String()
	firstPEM := readFile(t, certPath(h.Dir))

	// Same process, again.
	for i := 0; i < 3; i++ {
		regenerated, err := h.Ensure("hs-shack")
		if err != nil {
			t.Fatal(err)
		}
		if regenerated {
			t.Fatalf("Ensure regenerated on call %d", i+2)
		}
	}

	// And a fresh holder over the same directory — which is what the next boot is.
	next := &Holder{Dir: h.Dir, Now: h.Now, HostIPs: h.HostIPs, Logf: t.Logf}
	regenerated, err := next.Ensure("hs-shack")
	if err != nil {
		t.Fatal(err)
	}
	if regenerated {
		t.Fatal("the next boot regenerated the certificate; the operator would re-trust every reboot")
	}
	if got := leaf(t, next).SerialNumber.String(); got != first {
		t.Errorf("serial changed across a restart: %s then %s", first, got)
	}
	if readFile(t, certPath(h.Dir)) != firstPEM {
		t.Error("the certificate file was rewritten")
	}
}

// Property 4: a certificate naming the wrong host is replaced.
//
// This is the ordering bug the holder exists for. On first boot the node is still
// called raspberrypi, so a certificate minted at daemon start names a host that is
// about to stop existing — and the operator who finishes setup and browses to the
// name the node just gave them gets a mismatch warning.
func TestEnsureRegeneratesAfterARename(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("raspberrypi"); err != nil {
		t.Fatal(err)
	}
	before := leaf(t, h).SerialNumber.String()

	regenerated, err := h.Ensure("hs-shack")
	if err != nil {
		t.Fatal(err)
	}
	if !regenerated {
		t.Fatal("the certificate was not reminted after the rename")
	}
	l := leaf(t, h)
	if l.SerialNumber.String() == before {
		t.Error("the same certificate was kept")
	}
	if err := l.VerifyHostname("hs-shack.local"); err != nil {
		t.Errorf("the reminted certificate does not name hs-shack.local: %v", err)
	}
	if err := l.VerifyHostname("raspberrypi"); err == nil {
		t.Error("the reminted certificate still names the old host")
	}
	if h.Hostname() != "hs-shack" {
		t.Errorf("Hostname = %q", h.Hostname())
	}

	// A restart after the rename does not regenerate again: one remint per rename,
	// not one per boot.
	next := &Holder{Dir: h.Dir, Now: h.Now, HostIPs: h.HostIPs, Logf: t.Logf}
	if regenerated, err := next.Ensure("hs-shack"); err != nil || regenerated {
		t.Errorf("the boot after a rename regenerated again: %v %v", regenerated, err)
	}
}

// Property 5: a dotted name is reduced to the label the certificate is minted
// for. An operator who types `hs-shack.local` means the host `hs-shack`; minting
// for the dotted form would produce SANs for `hs-shack.local.local`.
func TestEnsureNormalisesTheHostname(t *testing.T) {
	for _, given := range []string{"hs-shack", "HS-Shack", "hs-shack.local", "hs-shack.local.", "  hs-shack  "} {
		h := newHolder(t)
		if _, err := h.Ensure(given); err != nil {
			t.Fatalf("Ensure(%q): %v", given, err)
		}
		l := leaf(t, h)
		if err := l.VerifyHostname("hs-shack.local"); err != nil {
			t.Errorf("Ensure(%q) produced a certificate that does not name hs-shack.local: %v", given, err)
		}
		if err := l.VerifyHostname("hs-shack.local.local"); err == nil {
			t.Errorf("Ensure(%q) produced a doubled .local SAN", given)
		}
	}

	// An empty name falls back rather than producing a certificate for "".
	h := newHolder(t)
	if _, err := h.Ensure(""); err != nil {
		t.Fatal(err)
	}
	if h.Hostname() != FallbackHostname {
		t.Errorf("Hostname = %q, want the fallback", h.Hostname())
	}
}

// Property 6: Covers is the test Ensure trusts, so it has to agree with what a
// TLS client would actually accept.
func TestCovers(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("hs-shack"); err != nil {
		t.Fatal(err)
	}
	cert, err := h.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}

	if !Covers(*cert, "hs-shack") {
		t.Error("Covers = false for the name it was minted for")
	}
	if !Covers(*cert, "hs-shack.local") {
		t.Error("Covers = false for the mDNS form")
	}
	if Covers(*cert, "other-node") {
		t.Error("Covers = true for an unrelated name")
	}
	if Covers(tls.Certificate{}, "hs-shack") {
		t.Error("Covers = true for an empty certificate")
	}
}

// Property 7: a corrupt pair on disk is replaced rather than bricking TLS
// startup. The old one was unusable, so there is nothing to preserve.
func TestEnsureReplacesACorruptPair(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("hs-shack"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certPath(h.Dir), []byte("not a certificate"), 0o644); err != nil {
		t.Fatal(err)
	}

	fresh := &Holder{Dir: h.Dir, Now: h.Now, HostIPs: h.HostIPs, Logf: t.Logf}
	regenerated, err := fresh.Ensure("hs-shack")
	if err != nil {
		t.Fatal(err)
	}
	if !regenerated {
		t.Fatal("a corrupt pair was not replaced")
	}
	if err := leaf(t, fresh).VerifyHostname("hs-shack.local"); err != nil {
		t.Error(err)
	}
}

// Property 8: GetCertificate refuses before anything has been generated, rather
// than handing a listener a nil certificate to panic on.
func TestGetCertificateBeforeEnsure(t *testing.T) {
	h := newHolder(t)
	if _, err := h.GetCertificate(nil); err == nil {
		t.Error("GetCertificate succeeded before Ensure")
	}
}

// Property 9: the config a listener is handed serves through the holder, so a
// remint mid-run reaches the next handshake without a restart.
func TestTLSConfigServesThroughTheHolder(t *testing.T) {
	h := newHolder(t)
	if _, err := h.Ensure("raspberrypi"); err != nil {
		t.Fatal(err)
	}
	cfg := h.TLSConfig()
	if cfg.GetCertificate == nil {
		t.Fatal("the config does not serve through the holder")
	}
	if len(cfg.Certificates) != 0 {
		t.Error("the config pins a fixed certificate, so a remint would not be picked up")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}

	before, err := cfg.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Ensure("hs-shack"); err != nil {
		t.Fatal(err)
	}
	after, err := cfg.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Error("the same certificate was served after a remint")
	}
	l, err := leafOf(*after)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.VerifyHostname("hs-shack.local"); err != nil {
		t.Errorf("the config still serves the old certificate: %v", err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
