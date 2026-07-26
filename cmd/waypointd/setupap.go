package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/setupap"
)

// apOptions configures the setup access point and its captive portal.
type apOptions struct {
	Enabled bool
	// CPUInfo and PSKFile are overridable for tests; on a node they are the real
	// paths.
	CPUInfo string
	PSKFile string
	// Interface and Country are optional; empty lets the helper choose.
	Interface string
	Country   string
	// PortalAddr is the HTTP listener the portal is served on, and DNSAddr the
	// DNS responder's. Both are on the AP address so nothing on the operator's
	// real network ever reaches them.
	PortalAddr string
	DNSAddr    string
	// Window is how long to wait for a client before giving up.
	Window time.Duration
	// IdleTimeout releases the one-device setup session.
	IdleTimeout time.Duration
}

// initSetupAP raises the setup access point and serves the captive portal while
// the node is unprovisioned.
//
// It is the answer to the question the wizard cannot answer on its own: a node
// flashed with dd has no network at all, so there is nothing to serve the wizard
// over. The AP exists to close that gap and for nothing else, which is why every
// path here ends in taking it down again.
func (s *server) initSetupAP(ctx context.Context, opts apOptions) {
	if !opts.Enabled || s.wiz == nil {
		return
	}
	if s.wiz.Provisioned() {
		return // nothing to set up; the AP would be an open network for no reason
	}

	ssid := setupap.SSID(nonEmpty(opts.CPUInfo, setupap.CPUInfoPath))
	psk := setupap.ReadPSKFile(nonEmpty(opts.PSKFile, setupap.PSKFilePath))
	if psk.Warning != "" {
		// Loud, because the operator wrote a key file expecting it to do something
		// and it did not. The AP still comes up: unreachable is worse than open.
		log.Printf("waypointd: %s", psk.Warning)
	}

	ctrl := &captive.Controller{
		Prov:      s.wiz.Prov,
		SSID:      ssid,
		PSK:       psk.PSK,
		Interface: opts.Interface,
		Country:   opts.Country,
		Window:    opts.Window,
		Logf:      log.Printf,
	}
	s.ap = ctrl

	if err := ctrl.Up(ctx); err != nil {
		log.Printf("waypointd: could not raise the setup access point (%s): %v", ssid, err)
		return
	}

	lock := &captive.Lock{Idle: opts.IdleTimeout, Logf: log.Printf}
	portal := &captive.Portal{
		Lock:      lock,
		Wizard:    s.wiz.Gate(s.auth.Gate(s.newMux())),
		OnRequest: ctrl.NoteClient,
		Logf:      log.Printf,
	}

	// The AP comes down the moment the node is on the operator's network, and the
	// session lock closes when setup finishes. Both hooks live here rather than in
	// the wizard so the wizard needs to know nothing about radios.
	s.wiz.OnNetworkJoined = func(ctx context.Context) {
		if err := ctrl.Down(ctx, captive.ReasonJoined); err != nil {
			log.Printf("waypointd: could not take the setup access point down after the join: %v", err)
		}
	}
	s.wiz.OnComplete = func(ctx context.Context) {
		lock.Complete()
		if err := ctrl.Down(ctx, captive.ReasonSetupComplete); err != nil {
			log.Printf("waypointd: could not take the setup access point down after setup: %v", err)
		}
	}

	go serveCaptivePortal(ctx, portal, opts)
	go runDNSHijack(ctx, opts)
	go func() {
		tick := time.NewTicker(time.Minute)
		defer tick.Stop()
		ctrl.Run(ctx, tick.C)
	}()
}

// serveCaptivePortal serves the portal over plain HTTP on the AP address.
//
// Plain HTTP, deliberately. Every operating system's connectivity probe is an
// http:// URL, and a self-signed certificate on the address they were redirected
// to produces a warning interstitial inside the captive sheet — where there is
// often no way to click through. The dashboard's HTTPS listener is unaffected;
// this one only exists on the AP segment and only while the AP is up.
func serveCaptivePortal(ctx context.Context, p *captive.Portal, opts apOptions) {
	addr := nonEmpty(opts.PortalAddr, net.JoinHostPort(captive.DefaultAddress.String(), "80"))
	srv := &http.Server{
		Addr:              addr,
		Handler:           p.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("waypointd: captive portal on http://%s/", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		// Binding :80 needs CAP_NET_BIND_SERVICE. waypointd has it today by
		// running as root; when it drops privileges the unit needs the ambient
		// capability, and this is the line that will say so.
		log.Printf("waypointd: captive portal listener on %s: %v", addr, err)
	}
}

// runDNSHijack answers every name with the node, so a client's probe arrives at
// the portal above.
func runDNSHijack(ctx context.Context, opts apOptions) {
	addr := nonEmpty(opts.DNSAddr, net.JoinHostPort(captive.DefaultAddress.String(), "53"))
	d := &captive.DNSResponder{Answer: captive.DefaultAddress, Logf: log.Printf}
	log.Printf("waypointd: captive DNS on %s (every name resolves to this node)", addr)
	if err := d.ListenAndServe(ctx, addr); err != nil {
		log.Printf("waypointd: captive DNS on %s: %v", addr, err)
	}
}

// setupAPStatus reports the access point's state for /api/health.
func (s *server) setupAPStatus() *captive.Status {
	if s.ap == nil {
		return nil
	}
	st := s.ap.Status()
	return &st
}
