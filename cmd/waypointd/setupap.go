package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/netwatch"
	"github.com/KN4OQW/waypoint/internal/setupap"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// apRaiseTimeout bounds the first attempt to raise the setup access point.
//
// Every step of a raise can block: firmware load, rfkill, a regulatory-domain
// change, and NetworkManager activating the profile. Without a bound, a radio
// that never answers leaves the node with no access point and no recorded reason
// — the operator sees the same nothing either way. With one, the attempt fails,
// says so, and the failure reaches /api/setup/state.
const apRaiseTimeout = 90 * time.Second

// apRaiseAttempts and apRaiseBackoff bound the cold-boot retry in
// raiseWithRetry. Four attempts at a backoff that grows 5s, 10s, 15s covers
// about half a minute of a radio arriving late, which is comfortably longer than
// brcmfmac takes to load firmware and NetworkManager takes to notice the result.
const (
	apRaiseAttempts = 4
	apRaiseBackoff  = 5 * time.Second
)

// setupTeardownGrace is how long the setup access point stays up after setup
// completes, so the reply to the request that finished it can actually reach the
// browser that sent it. Generous on purpose: the cost of waiting is a few seconds
// of an access point nobody is using, and the cost of not waiting is an operator
// looking at a spinner on a node that finished successfully.
const setupTeardownGrace = 8 * time.Second

// setupPanelLinger keeps the completion screen on a wired LCD after the radio has
// gone, for somebody standing at the node rather than holding the phone.
const setupPanelLinger = 90 * time.Second

// raiseWithRetry brings the setup access point up, retrying a cold-boot race.
//
// waypointd starts early, and on a fresh boot the pieces a raise depends on may
// not be there yet: brcmfmac is still loading firmware, wlan0 has not appeared,
// or NetworkManager is still enumerating devices and has no opinion about the
// radio. Every one of those resolves within seconds on its own. A single attempt
// turns a transient into a permanent condition — the node has no setup network
// for the rest of the boot, over a race it would have won a moment later. That
// is the difference between the bench, where the board had been up for hours
// before anything was asked of it, and a node someone has just plugged in.
//
// The retries are bounded and the last error is kept: a radio that is genuinely
// absent or blocked must still end up reported rather than retried forever.
func (s *server) raiseWithRetry(ctx context.Context, ctrl *captive.Controller, ssid string) error {
	var err error
	for attempt := 1; attempt <= apRaiseAttempts; attempt++ {
		raise, cancel := context.WithTimeout(ctx, apRaiseTimeout)
		err = ctrl.Up(raise)
		cancel()
		if err == nil {
			bootLogAndPrint("setup access point %q is up (attempt %d); join it and browse to http://%s/",
				ssid, attempt, captive.DefaultAddress)
			s.apErrMu.Lock()
			s.apErr = ""
			s.apErrMu.Unlock()
			return nil
		}

		// Recorded on every attempt, not just the last: an operator refreshing
		// /api/setup/state during a slow boot should see what is being tried
		// rather than an empty field.
		s.apErrMu.Lock()
		s.apErr = err.Error()
		s.apErrMu.Unlock()
		bootLogAndPrint("attempt %d of %d to raise the setup access point (%s) failed: %v",
			attempt, apRaiseAttempts, ssid, err)

		if attempt == apRaiseAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt) * apRaiseBackoff):
		}
	}

	bootLogf("the node has NO setup network. Reach it over Ethernet at https://<address>/ , " +
		"or write a waypoint.toml to this partition (see docs/provisioning.md) to set it up without one.")
	return err
}

// protectionOf names the AP's protection for a human reading a log. An empty
// passphrase is an open network, which is the default (see internal/setupap).
func protectionOf(psk string) string {
	if psk == "" {
		return "open"
	}
	return "WPA2"
}

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
	// NetwatchGrace is how long the node tolerates having no route out before the
	// access point comes back; NetwatchInterval is how often that is checked.
	NetwatchGrace    time.Duration
	NetwatchInterval time.Duration
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

	provisioned := s.wiz.Provisioned()

	// Whatever happens below, the wizard can report the access point — including
	// the reason it is not up. An operator on Ethernet would otherwise see a
	// perfectly working node and no hint that the wireless half failed.
	s.wiz.APStatus = func() *wizard.APState {
		st := ctrl.Status()
		out := &wizard.APState{SSID: st.SSID, Up: st.Up, Open: st.Open}
		s.apErrMu.Lock()
		out.Error = s.apErr
		s.apErrMu.Unlock()
		return out
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
	// The setup panel runs for the length of setup and no longer. Once the node is
	// provisioned the operator's own configured pages own the display, and two
	// writers on one HD44780 would interleave into nonsense — so this context is
	// canceled the moment setup completes, releasing the device before the
	// configured renderer opens it.
	panelCtx, stopPanel := context.WithCancel(ctx)

	s.wiz.OnComplete = func(context.Context) {
		lock.Complete()
		// The teardown is scheduled, not immediate, and that ordering is the whole
		// point.
		//
		// The reply to the request that finished setup still has to reach the
		// operator, and on a node set up over the access point it travels over the
		// access point. Taking the radio down inline cut the connection carrying
		// that response: the browser sat on "finishing…" forever with nothing to
		// show, and because setup completion spends the AP it never came back. The
		// node itself was finished and healthy — provisioned, root locked, waiting
		// at the claim gate — and the one surface that could have said so was the
		// one we had just switched off.
		//
		// So: return, let the response flush and the operator read where to go
		// next, and only then give up the radio.
		go func() {
			select {
			case <-time.After(setupTeardownGrace):
			case <-ctx.Done():
				return // the daemon is stopping; shutdown takes the AP down anyway
			}
			if err := ctrl.Down(context.WithoutCancel(ctx), captive.ReasonSetupComplete); err != nil {
				log.Printf("waypointd: could not take the setup access point down after setup: %v", err)
			}
			// The panel keeps the completion screen up for a while after the radio
			// goes, because that screen is how somebody standing at the node learns
			// where it moved to. Releasing the device the instant setup finished
			// left the last setup frame frozen on the glass, still advertising a
			// network that no longer existed.
			select {
			case <-time.After(setupPanelLinger):
			case <-ctx.Done():
			}
			stopPanel()
		}()
	}

	sess := &apSession{ctrl: ctrl, portal: portal, opts: opts}
	s.wiz.AP = sess
	s.apSession = sess

	if !provisioned {
		// A node that has never been set up needs the AP now, and needs the
		// associate window that takes it down again if nobody comes.
		//
		// In its own goroutine, deliberately. Raising the AP means loading brcmfmac
		// firmware, clearing an rfkill block, setting a regulatory domain and
		// waiting for NetworkManager to activate a profile — tens of seconds on a
		// cold boot, and unbounded when the radio is wedged. Inline, that ran before
		// the HTTPS listener was bound and before READY=1: a node reachable over
		// Ethernet served nothing while it waited, and if the wait outlasted
		// systemd's start timeout the daemon was killed and restarted into the same
		// wait, forever. A node whose radio will not come up must still finish
		// starting and be able to say so — which is what apErr is for, and it is
		// worth nothing if the surface that reports it never binds.
		bootLogBanner(Version)
		bootLogf("raising the setup access point %q (%s) on %s", ssid, protectionOf(psk.PSK), nonEmpty(opts.Interface, "the first wireless interface"))
		go s.runSetupPanel(panelCtx)
		go func() {
			if err := s.raiseWithRetry(ctx, ctrl, ssid); err != nil {
				return
			}
			sess.serve(ctx)
			tick := time.NewTicker(time.Minute)
			defer tick.Stop()
			ctrl.Run(ctx, tick.C)
		}()
	}

	// netwatch runs in both states, and the provisioned one is the point: a node
	// that was set up months ago and has just had its network renamed out from
	// under it has no other way back. It re-raises the AP and touches nothing else
	// — the node stays provisioned, stays claimed, and keeps its configuration.
	go func() {
		w := &netwatch.Watcher{
			AP:          sess,
			APInterface: opts.Interface,
			Grace:       opts.NetwatchGrace,
			Logf:        log.Printf,
		}
		interval := opts.NetwatchInterval
		if interval <= 0 {
			interval = netwatch.DefaultInterval
		}
		tick := time.NewTicker(interval)
		defer tick.Stop()
		w.Run(ctx, tick.C)
	}()
}

// apSession ties the access point to the listeners that make it useful.
//
// The portal and the DNS responder bind to the AP's own address, which does not
// exist while the AP is down — so they cannot simply run for the daemon's
// lifetime. They start when the AP goes up and stop when it comes down, which
// also means a re-raise after a failed join or a lost upstream brings the wizard
// back rather than an access point serving nothing.
type apSession struct {
	ctrl   *captive.Controller
	portal *captive.Portal
	opts   apOptions

	mu   sync.Mutex
	stop context.CancelFunc
}

// serve starts the portal and DNS listeners for the current AP.
func (a *apSession) serve(ctx context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop != nil {
		return // already serving
	}
	sctx, cancel := context.WithCancel(ctx)
	a.stop = cancel
	go serveCaptivePortal(sctx, a.portal, a.opts)
	go runDNSHijack(sctx, a.opts)
}

// halt stops them.
func (a *apSession) halt() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop != nil {
		a.stop()
		a.stop = nil
	}
}

// DownForJoin frees the radio, taking the listeners down with it.
func (a *apSession) DownForJoin(ctx context.Context) error {
	if err := a.ctrl.DownForJoin(ctx); err != nil {
		return err
	}
	a.halt()
	return nil
}

// Commit spends the AP after a verified join.
func (a *apSession) Commit(ctx context.Context) error {
	a.halt()
	return a.ctrl.Commit(ctx)
}

// Reraise brings the AP and its listeners back.
func (a *apSession) Reraise(ctx context.Context, why captive.Reason) error {
	if err := a.ctrl.Reraise(ctx, why); err != nil {
		return err
	}
	a.serve(context.WithoutCancel(ctx))
	return nil
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
