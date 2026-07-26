package main

import (
	"crypto/tls"
	"log"
	"net"
	"net/http"
	"time"

	"golang.org/x/crypto/acme/autocert"

	"github.com/KN4OQW/waypoint/internal/tlscert"
)

// tlsOptions configures how the daemon serves (RFC-0012 / issue #11).
type tlsOptions struct {
	enabled      bool            // serve HTTPS (false => plaintext, e.g. behind a TLS-terminating proxy)
	certDir      string          // where the self-signed device cert lives
	httpsPort    string          // the HTTPS listen port, for building redirect targets
	redirectAddr string          // HTTP listener that 308s to HTTPS ("" disables it)
	certs        *tlscert.Holder // the device certificate, replaceable after the hostname is chosen
	acmeDomain   string          // when set, use Let's Encrypt instead of the self-signed cert
	acmeEmail    string
	acmeDir      string
}

// listenAndServe starts the daemon's listeners: HTTPS on srv.Addr when TLS is
// enabled (self-signed device cert, or Let's Encrypt when a domain is set), plus
// an optional HTTP listener that redirects to HTTPS. It blocks on the main
// listener. With TLS disabled it serves plaintext (the reverse-proxy escape hatch).
func listenAndServe(srv *http.Server, o tlsOptions) error {
	if !o.enabled {
		return srv.ListenAndServe()
	}

	var redirectHandler http.Handler = httpsRedirect(o.httpsPort)

	if o.acmeDomain != "" {
		// Let's Encrypt: a browser-trusted cert, no trust prompt. autocert serves the
		// HTTP-01 challenge on the redirect listener and otherwise redirects to HTTPS.
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(o.acmeDomain),
			Cache:      autocert.DirCache(o.acmeDir),
			Email:      o.acmeEmail,
		}
		srv.TLSConfig = m.TLSConfig()
		srv.TLSConfig.MinVersion = tls.VersionTLS12
		redirectHandler = m.HTTPHandler(redirectHandler)
	} else {
		// Served through the holder rather than a fixed Certificates slice, so a
		// certificate reminted after the operator chooses a hostname is picked up
		// on the next handshake instead of at the next restart.
		holder := o.certs
		if holder == nil {
			holder = tlscert.NewHolder(o.certDir)
		}
		if _, err := holder.EnsureDefault(); err != nil {
			return err
		}
		srv.TLSConfig = holder.TLSConfig()
	}

	if o.redirectAddr != "" {
		go func() {
			rs := &http.Server{
				Addr:              o.redirectAddr,
				Handler:           redirectHandler,
				ReadHeaderTimeout: 5 * time.Second,
			}
			if err := rs.ListenAndServe(); err != nil {
				log.Printf("http-redirect listener stopped: %v", err)
			}
		}()
	}

	// Certificates live in TLSConfig, so the file arguments are empty.
	return srv.ListenAndServeTLS("", "")
}

// httpsRedirect answers every request with a 308 to the https:// form of the same
// host + path, applying the HTTPS port when it is non-default. It serves nothing
// else, so it is never an unencrypted content surface (RFC-0012).
//
// 308 rather than 301 because this fronts an API, not a site. 301 and 302 permit
// a client to rewrite the request as a GET, so a `POST /api/claim` arriving on the
// HTTP listener would be redirected and then replayed as a GET — the operator's
// claim silently turning into a request for the claim page, with the password
// dropped on the floor. 308 preserves the method and the body.
//
// This handler is mounted only on the LAN redirect listener. The captive portal
// on the setup access point stays plain HTTP on purpose: every operating system's
// connectivity probe is an http:// URL, and redirecting one to a self-signed
// certificate produces a warning interstitial inside the captive sheet, where
// there is frequently no way to click through.
func httpsRedirect(httpsPort string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		target := "https://" + host
		if httpsPort != "" && httpsPort != "443" {
			target += ":" + httpsPort
		}
		target += r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	}
}
