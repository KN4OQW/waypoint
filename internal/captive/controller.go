package captive

import (
	"context"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// DefaultAssociateWindow is how long the setup AP waits for somebody to turn up.
//
// Thirty minutes covers a node powered on before the operator is ready and a
// first boot that spends a while expanding the filesystem. After that the AP is
// an open network broadcasting from an unattended box, which is worth having for
// a setup session and not worth having indefinitely — so it goes down and stays
// down until the next boot.
const DefaultAssociateWindow = 30 * time.Minute

// Reason describes why the AP came down; it is logged and surfaced in status.
type Reason string

const (
	// ReasonJoined — the node reached the operator's network, so the setup AP has
	// done its job. It goes down immediately rather than lingering as a second,
	// open network beside the real one.
	ReasonJoined Reason = "joined the operator's network"
	// ReasonNobodyCame — the associate window expired with no client.
	ReasonNobodyCame Reason = "nobody associated within the window"
	// ReasonSetupComplete — provisioning finished.
	ReasonSetupComplete Reason = "setup completed"
	// ReasonShutdown — the daemon is stopping.
	ReasonShutdown Reason = "waypointd is stopping"
	// ReasonHandingOver — the AP is down only for the length of a join attempt.
	// A single-radio Pi cannot be an access point and a station at the same time,
	// so the AP has to give the radio up before the node can try the operator's
	// network. This reason never spends the AP: the whole point is that a failed
	// join gets it back.
	ReasonHandingOver Reason = "handing the radio over for a network join"
	// ReasonUpstreamLost — netwatch re-raised the AP because the node has had no
	// route out for long enough that the operator has probably lost the node.
	ReasonUpstreamLost Reason = "the upstream network went away"
)

// Controller owns the setup access point's lifetime.
//
// The AP is a means, not a feature: it exists so an operator can reach a node
// that has no other network, and every rule here is about making sure it stops
// existing as soon as that is no longer true.
type Controller struct {
	// Prov raises and lowers the AP.
	Prov privhelper.Provisioner
	// SSID and PSK describe the network. An empty PSK is an open network, which
	// is the default (see internal/setupap).
	SSID string
	PSK  string
	// Interface and AddressCIDR are optional; empty lets the helper choose.
	Interface   string
	AddressCIDR string
	// Country is the regulatory domain, if the operator's is known.
	Country string

	// Window is how long to wait for a client. Zero means DefaultAssociateWindow.
	Window time.Duration
	// Now and Logf are injectable for tests.
	Now  func() time.Time
	Logf func(format string, args ...any)

	mu      sync.Mutex
	up      bool
	spent   bool // down for good until the next boot
	seen    bool // a client has been observed
	raised  time.Time
	lastDwn Reason
}

func (c *Controller) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func (c *Controller) window() time.Duration {
	if c.Window > 0 {
		return c.Window
	}
	return DefaultAssociateWindow
}

func (c *Controller) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// Up raises the access point.
//
// It refuses once the AP has been spent — taken down because nobody came, or
// because the node joined a real network. Bringing it back would undo the reason
// it went down, and the next boot is a deliberate act by someone with physical
// access, which is the right bar for re-opening an unauthenticated surface.
func (c *Controller) Up(ctx context.Context) error {
	c.mu.Lock()
	if c.spent {
		c.mu.Unlock()
		return privhelper.Errorf(privhelper.CodeConflict,
			"the setup access point was taken down (%s) and will not come back until the node is rebooted", c.lastDwn)
	}
	if c.up {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if _, err := c.Prov.APUp(ctx, privhelper.APUpRequest{
		SSID:        c.SSID,
		Passphrase:  c.PSK,
		Interface:   c.Interface,
		Country:     c.Country,
		AddressCIDR: c.AddressCIDR,
	}); err != nil {
		return err
	}

	c.mu.Lock()
	c.up, c.raised = true, c.now()
	c.mu.Unlock()

	protection := "open"
	if c.PSK != "" {
		protection = "WPA2"
	}
	c.logf("captive: setup access point %q up (%s); it comes down on a network join, on setup completion, or after %s with nobody associated",
		c.SSID, protection, c.window())
	return nil
}

// spendingReasons are the ways the AP goes down for good. Everything else is a
// pause it can come back from — which is the distinction a join depends on.
func spends(why Reason) bool {
	switch why {
	case ReasonShutdown, ReasonHandingOver:
		return false
	}
	return true
}

// Down takes the AP down. Whether it can come back depends on why (see spends).
func (c *Controller) Down(ctx context.Context, why Reason) error {
	c.mu.Lock()
	wasUp := c.up
	c.up = false
	c.lastDwn = why
	if spends(why) {
		c.spent = true
	}
	c.mu.Unlock()

	if !wasUp {
		return nil
	}
	if _, err := c.Prov.APDown(ctx, privhelper.APDownRequest{Interface: c.Interface}); err != nil {
		return err
	}
	c.logf("captive: setup access point down — %s", why)
	return nil
}

// DownForJoin lowers the AP so the radio is free for a station association, in a
// way it can be brought back from. It is half of the confirm-or-revert handover:
// Reraise is the other half.
func (c *Controller) DownForJoin(ctx context.Context) error {
	return c.Down(ctx, ReasonHandingOver)
}

// Reraise brings the AP back after a failed join or a lost upstream.
//
// It clears the "a client was here" flag deliberately. The operator whose join
// failed is on a phone that just lost this network; the associate window has to
// start again from the moment the AP comes back, or a node that has been sitting
// unattended would tear it straight back down.
func (c *Controller) Reraise(ctx context.Context, why Reason) error {
	c.mu.Lock()
	if c.spent {
		c.mu.Unlock()
		return privhelper.Errorf(privhelper.CodeConflict,
			"the setup access point was taken down (%s) and will not come back until the node is rebooted", c.lastDwn)
	}
	c.seen = false
	c.mu.Unlock()

	if err := c.Up(ctx); err != nil {
		return err
	}
	c.logf("captive: setup access point back up — %s", why)
	return nil
}

// Commit spends the AP after a join the caller has confirmed. It is the point of
// no return in the handover: from here the node is on the operator's network and
// the setup AP does not come back without a reboot.
func (c *Controller) Commit(ctx context.Context) error {
	return c.Down(ctx, ReasonJoined)
}

// NoteClient records that a client is using the AP. It is called from the
// portal's HTTP handlers, which is the honest signal: an associated station that
// never asks for anything is a phone that joined by accident, and the window
// exists to catch exactly that case.
func (c *Controller) NoteClient() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seen && c.up {
		c.logf("captive: a client is using the setup access point; the %s window no longer applies", c.window())
	}
	c.seen = true
}

// Status is what the controller reports to the rest of the daemon.
type Status struct {
	Up       bool      `json:"up"`
	Spent    bool      `json:"spent"`
	SSID     string    `json:"ssid,omitempty"`
	Open     bool      `json:"open"`
	Client   bool      `json:"client_seen"`
	RaisedAt time.Time `json:"raised_at,omitzero"`
	Reason   Reason    `json:"last_down_reason,omitempty"`
}

// Status reports the AP's state.
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{
		Up: c.up, Spent: c.spent, SSID: c.SSID, Open: c.PSK == "",
		Client: c.seen, RaisedAt: c.raised, Reason: c.lastDwn,
	}
}

// Run watches the associate window and takes the AP down if nobody arrives.
//
// It returns when the AP is down for any reason, so the caller can stop the DNS
// responder and the portal listener alongside it.
func (c *Controller) Run(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			// A shutdown does not spend the AP: the node is going away, and the
			// next boot should raise it again if setup is still unfinished.
			_ = c.Down(context.WithoutCancel(ctx), ReasonShutdown)
			return
		case <-tick:
			c.mu.Lock()
			seen, up, raised := c.seen, c.up, c.raised
			c.mu.Unlock()
			if !up {
				return // taken down by a join or by setup completing
			}
			if seen {
				continue // somebody is here; the window no longer applies
			}
			// The deadline is measured from when the AP actually came up, not from
			// when this loop started. They are usually the same instant, but a
			// supervisor that starts Run late would otherwise grant the AP a fresh
			// thirty minutes it has already spent broadcasting.
			if deadline := raised.Add(c.window()); !c.now().Before(deadline) {
				if err := c.Down(ctx, ReasonNobodyCame); err != nil {
					c.logf("captive: could not take the setup access point down: %v", err)
				}
				return
			}
		}
	}
}
