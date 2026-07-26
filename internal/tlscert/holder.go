package tlscert

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

// FallbackHostname is the name a node answers to before the operator has chosen
// one, and the name the certificate falls back to when the OS reports none.
const FallbackHostname = "waypoint"

// Holder owns the device certificate for the life of the daemon and can replace
// it without a restart.
//
// It exists because of an ordering problem that only shows up on a node set up
// from scratch. The certificate's SANs are minted from the hostname, and on first
// boot the hostname is still `raspberrypi` — the operator has not chosen one yet.
// A certificate minted at daemon start therefore names a host that is about to
// stop existing, and the operator who finishes setup and browses to
// `https://hs-shack.local/` gets a name-mismatch warning on a node that just told
// them that address.
//
// So the certificate is (re)generated *after* the hostname is chosen, and served
// through GetCertificate so the running listener picks the new one up on the next
// handshake. The operator meets the self-signed trust prompt once, on the name
// they will keep, instead of once on the wrong name and again on the right one.
type Holder struct {
	// Dir is where cert.pem and key.pem live.
	Dir string
	// Now and HostIPs are injectable for tests.
	Now     func() time.Time
	HostIPs func() []net.IP
	// Logf receives one line per generation — a rare, auditable event.
	Logf func(format string, args ...any)

	mu       sync.RWMutex
	cert     *tls.Certificate
	hostname string
}

// NewHolder returns a holder over dir.
func NewHolder(dir string) *Holder {
	return &Holder{Dir: dir}
}

func (h *Holder) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Holder) ips() []net.IP {
	if h.HostIPs != nil {
		return h.HostIPs()
	}
	return hostIPs()
}

func (h *Holder) logf(format string, args ...any) {
	if h.Logf != nil {
		h.Logf(format, args...)
	}
}

// Ensure guarantees the held certificate names hostname, generating a new one
// only when the current one does not.
//
// The "only when" is the whole contract. Regenerating on every start would mean a
// fresh trust prompt on every reboot, which trains an operator to click through
// certificate warnings — the opposite of what a pinned self-signed cert is for.
// It reports whether it generated, so the caller can say so in the log rather
// than leaving a silent re-trust to be discovered in a browser.
func (h *Holder) Ensure(hostname string) (regenerated bool, err error) {
	hostname = normalizeHostname(hostname)

	h.mu.Lock()
	defer h.mu.Unlock()

	// An in-memory certificate that already covers the name needs no disk access
	// at all — this is the path every call after the first one takes.
	if h.cert != nil && h.hostname == hostname {
		return false, nil
	}

	if cert, err := load(h.Dir); err == nil {
		if Covers(cert, hostname) {
			h.cert, h.hostname = &cert, hostname
			return false, nil
		}
		h.logf("tlscert: the device certificate does not name %q; minting one that does", hostname)
	}

	cert, err := create(h.Dir, h.now(), hostname, h.ips())
	if err != nil {
		return false, err
	}
	h.cert, h.hostname = &cert, hostname
	h.logf("tlscert: device certificate minted for %q (SANs %s) — browsers will ask to trust it once",
		hostname, strings.Join(sansOf(cert), ", "))
	return true, nil
}

// EnsureDefault is Ensure with the OS hostname.
func (h *Holder) EnsureDefault() (bool, error) {
	return h.Ensure(OSHostname())
}

// GetCertificate satisfies tls.Config.GetCertificate, so a certificate replaced
// mid-run is picked up on the next handshake without restarting the listener.
func (h *Holder) GetCertificate(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.cert == nil {
		return nil, fmt.Errorf("tlscert: no device certificate has been generated yet")
	}
	return h.cert, nil
}

// Hostname reports the name the held certificate was minted for.
func (h *Holder) Hostname() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.hostname
}

// TLSConfig returns a config that serves through this holder.
func (h *Holder) TLSConfig() *tls.Config {
	return &tls.Config{
		GetCertificate: h.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
}

// Covers reports whether a certificate names hostname in a way a browser will
// accept — both the bare name and its mDNS form.
//
// Both, because a node is reached by both. `https://hs-shack/` is what an
// operator types on a network with working DNS search; `https://hs-shack.local/`
// is what works everywhere else and what the dashboard tells them to use. A
// certificate covering only one of them produces a warning on the other.
func Covers(cert tls.Certificate, hostname string) bool {
	hostname = normalizeHostname(hostname)
	leaf, err := leafOf(cert)
	if err != nil {
		return false
	}
	if leaf.VerifyHostname(hostname) != nil {
		return false
	}
	return leaf.VerifyHostname(hostname+".local") == nil
}

// OSHostname returns the machine's hostname, or the fallback when it has none.
func OSHostname() string {
	host, err := os.Hostname()
	if err != nil {
		return FallbackHostname
	}
	return normalizeHostname(host)
}

// normalizeHostname reduces a name to the single label the certificate is minted
// for. An operator who types `hs-shack.local` means the host `hs-shack`; minting
// for the dotted form would produce SANs for `hs-shack.local.local`.
func normalizeHostname(hostname string) string {
	h := strings.TrimSpace(strings.ToLower(hostname))
	h = strings.TrimSuffix(h, ".")
	if i := strings.IndexByte(h, '.'); i > 0 {
		h = h[:i]
	}
	if h == "" {
		return FallbackHostname
	}
	return h
}

// load reads the persisted pair without generating anything.
func load(dir string) (tls.Certificate, error) {
	cp, kp := certPath(dir), keyPath(dir)
	if !fileExists(cp) || !fileExists(kp) {
		return tls.Certificate{}, os.ErrNotExist
	}
	return tls.LoadX509KeyPair(cp, kp)
}

// leafOf parses the leaf certificate. tls.LoadX509KeyPair does not populate
// Leaf, so a freshly loaded pair has to be parsed before it can be inspected.
func leafOf(cert tls.Certificate) (*x509.Certificate, error) {
	if cert.Leaf != nil {
		return cert.Leaf, nil
	}
	if len(cert.Certificate) == 0 {
		return nil, fmt.Errorf("tlscert: certificate has no chain")
	}
	return x509.ParseCertificate(cert.Certificate[0])
}

// sansOf renders a certificate's names for a log line.
func sansOf(cert tls.Certificate) []string {
	leaf, err := leafOf(cert)
	if err != nil {
		return nil
	}
	out := append([]string(nil), leaf.DNSNames...)
	for _, ip := range leaf.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}
