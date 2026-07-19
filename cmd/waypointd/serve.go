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
	enabled      bool   // serve HTTPS (false => plaintext, e.g. behind a TLS-terminating proxy)
	certDir      string // where the self-signed device cert lives
	httpsPort    string // the HTTPS listen port, for building redirect targets
	redirectAddr string // HTTP listener that 301s to HTTPS ("" disables it)
	acmeDomain   string // when set, use Let's Encrypt instead of the self-signed cert
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
		cert, err := tlscert.LoadOrCreateDefault(o.certDir)
		if err != nil {
			return err
		}
		srv.TLSConfig = &tls.Config{
			Certificates: []tls.Certificate{cert},
			MinVersion:   tls.VersionTLS12,
		}
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

// httpsRedirect answers every request with a 301 to the https:// form of the same
// host + path, applying the HTTPS port when it is non-default. It serves nothing
// else, so it is never an unencrypted content surface (RFC-0012).
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
		http.Redirect(w, r, target, http.StatusMovedPermanently)
	}
}
