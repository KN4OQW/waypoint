package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/tlscert"
)

// TestClaimHappensOverHTTPSWithTheChosenHostname is the acceptance test for the
// ordering: the operator names the node, and the certificate they are then asked
// to trust is one that names it.
//
// It runs a real TLS server over the daemon's own handler chain and verifies with
// a real client, because the failure this guards against — a certificate naming
// the host the node booted with rather than the one it now answers to — only
// shows up at handshake time.
func TestClaimHappensOverHTTPSWithTheChosenHostname(t *testing.T) {
	e := newSetupEnv(t)
	certDir := t.TempDir()

	// The daemon starts before the operator has chosen anything, so the first
	// certificate names whatever the node booted as.
	holder := &tlscert.Holder{Dir: certDir, Logf: t.Logf}
	e.s.certs = holder
	if _, err := holder.Ensure(privtest.DefaultHostname); err != nil {
		t.Fatal(err)
	}
	bootSerial := serialOf(t, holder)

	// Wire the hook the daemon wires.
	e.s.wiz.OnHostnameSet = func(_ context.Context, hostname string) {
		if _, err := holder.Ensure(hostname); err != nil {
			t.Errorf("remint for %q: %v", hostname, err)
		}
	}

	// --- the operator names the node ---
	if rec := e.do(t, "POST", "/api/setup/hostname",
		map[string]string{"hostname": e2eHostname}, nil); rec.Code != http.StatusOK {
		t.Fatalf("hostname = %d (%s)", rec.Code, rec.Body.String())
	}

	if holder.Hostname() != e2eHostname {
		t.Fatalf("the certificate is still held for %q", holder.Hostname())
	}
	if serialOf(t, holder) == bootSerial {
		t.Fatal("the certificate was not reminted after the rename")
	}

	// --- finish setup so the claim endpoint opens ---
	if rec := e.do(t, "POST", "/api/setup/user", map[string]string{
		"username": e2eUser, "password": e2ePassword}, nil); rec.Code != http.StatusOK {
		t.Fatalf("user = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "POST", "/api/setup/key",
		map[string]string{"public_key": e2eKey}, nil); rec.Code != http.StatusOK {
		t.Fatalf("key = %d (%s)", rec.Code, rec.Body.String())
	}
	if rec := e.do(t, "POST", "/api/setup/lock", map[string]any{}, nil); rec.Code != http.StatusOK {
		t.Fatalf("lock = %d (%s)", rec.Code, rec.Body.String())
	}

	// --- claim, over real TLS, against the name the operator chose ---
	srv := httptest.NewUnstartedServer(e.handler)
	srv.TLS = holder.TLSConfig()
	// The last check below deliberately offers the wrong SNI and expects the
	// handshake to fail; without this the server logs that rejection as an error
	// and buries the real output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()

	// The client trusts the device certificate and nothing else, and is told to
	// verify the chosen hostname — exactly what a browser does once the operator
	// has accepted the self-signed certificate for that name. A certificate still
	// naming raspberrypi fails here.
	pool := x509.NewCertPool()
	pool.AddCert(leafOf(t, holder))
	client := srv.Client()
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{
		RootCAs:    pool,
		ServerName: e2eHostname + ".local",
		MinVersion: tls.VersionTLS12,
	}

	body := strings.NewReader(`{"username":"admin","password":"a-long-enough-password"}`)
	req, err := http.NewRequest("POST", srv.URL+"/api/claim", body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("claim over HTTPS: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // test
	if resp.StatusCode != http.StatusCreated {
		out, _ := io.ReadAll(resp.Body)
		t.Fatalf("claim = %d (%s)", resp.StatusCode, out)
	}
	if resp.TLS == nil {
		t.Fatal("the claim did not happen over TLS")
	}
	// The name the handshake actually verified.
	if got := resp.TLS.PeerCertificates[0]; got.VerifyHostname(e2eHostname+".local") != nil {
		t.Errorf("the served certificate does not name %s.local", e2eHostname)
	}

	// The bare form works too, since the dashboard is reached by both.
	client.Transport.(*http.Transport).TLSClientConfig.ServerName = e2eHostname
	if _, err := client.Get(srv.URL + "/api/health"); err != nil {
		t.Errorf("GET over https://%s/: %v", e2eHostname, err)
	}

	// And the old name is genuinely gone, not merely additional.
	client.Transport.(*http.Transport).TLSClientConfig.ServerName = privtest.DefaultHostname
	if _, err := client.Get(srv.URL + "/api/health"); err == nil {
		t.Error("the certificate still validates for the host the node booted as")
	}
}

// Property: the HTTP listener 308s rather than 301s.
//
// 301 and 302 permit a client to rewrite the request as a GET. A `POST /api/claim`
// arriving on the HTTP listener would then be replayed as a GET — the operator's
// claim quietly turning into a request for the claim page, with the password
// dropped. 308 preserves the method and the body.
func TestHTTPRedirectIs308AndPreservesTheRequest(t *testing.T) {
	h := httpsRedirect("8073")

	cases := []struct{ method, host, target string }{
		{"GET", "hs-shack.local", "/api/health"},
		{"POST", "hs-shack.local", "/api/claim"},
		{"GET", "hs-shack.local:80", "/"},
		{"PUT", "192.168.1.50", "/api/config/general"},
	}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, "http://"+c.host+c.target, nil)
		req.Host = c.host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusPermanentRedirect {
			t.Errorf("%s %s%s = %d, want 308", c.method, c.host, c.target, rec.Code)
		}
		want := "https://" + strings.TrimSuffix(c.host, ":80") + ":8073" + c.target
		if got := rec.Header().Get("Location"); got != want {
			t.Errorf("%s %s%s -> %q, want %q", c.method, c.host, c.target, got, want)
		}
	}
}

// Property: /api/health is reachable over HTTP — as a redirect, not as a hole.
//
// It is not special-cased out of the redirect. A health endpoint served in the
// clear on the LAN would be the one unencrypted content surface on the box, and
// anything checking it can follow a redirect.
func TestHealthOverHTTPRedirectsRatherThanServing(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "http://hs-shack.local/api/health", nil)
	req.Host = "hs-shack.local"
	httpsRedirect("8073").ServeHTTP(rec, req)

	if rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("GET /api/health over HTTP = %d, want 308", rec.Code)
	}
	if got := rec.Header().Get("Location"); !strings.HasPrefix(got, "https://") {
		t.Errorf("Location = %q, want an https:// target", got)
	}
	// Nothing of substance is served in the clear.
	if body := rec.Body.String(); strings.Contains(body, "\"status\"") {
		t.Errorf("the redirect listener served a health payload in the clear: %q", body)
	}
}

// Property: the captive portal stays plain HTTP.
//
// Every operating system's connectivity probe is an http:// URL, and redirecting
// one to a self-signed certificate produces a warning interstitial inside the
// captive sheet — where there is frequently no way to click through. TLS is
// enforced on the LAN side, where the operator has a real browser.
func TestPortalIsNotRedirectedToHTTPS(t *testing.T) {
	e := newSetupEnv(t)
	portal := &captive.Portal{Wizard: e.handler, Logf: t.Logf}
	h := portal.Handler()

	for _, path := range []string{"/", "/generate_204", "/hotspot-detect.html"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "http://10.42.0.1"+path, nil)
		req.RemoteAddr = "10.42.0.51:1000"
		h.ServeHTTP(rec, req)

		if loc := rec.Header().Get("Location"); strings.HasPrefix(loc, "https://") {
			t.Errorf("%s redirected the captive client to %q; the sheet cannot clear a TLS warning", path, loc)
		}
	}
	// The portal's own redirect target is http, on the AP address.
	if u := portal.PortalURL(); !strings.HasPrefix(u, "http://") {
		t.Errorf("PortalURL = %q, want an http:// target", u)
	}
}

// --- helpers --------------------------------------------------------------

func leafOf(t *testing.T, h *tlscert.Holder) *x509.Certificate {
	t.Helper()
	cert, err := h.GetCertificate(nil)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	return leaf
}

func serialOf(t *testing.T, h *tlscert.Holder) string {
	t.Helper()
	return leafOf(t, h).SerialNumber.String()
}
