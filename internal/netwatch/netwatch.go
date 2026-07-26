// Package netwatch is the way back in when the network a node was set up on
// stops existing.
//
// The failure it exists for is ordinary and not recoverable by any other path in
// Waypoint: an operator finishes setup, the node joins their Wi-Fi, and weeks
// later the router is replaced, the SSID is renamed, or the node is carried to a
// hamfest. Nothing is broken — the node boots, the daemons run, the configuration
// is intact — but it is on a network that no longer exists and there is no way to
// tell it about the new one. The only recovery would be to pull the card and
// reflash, losing everything the operator configured.
//
// Instead, a node that has had no route out for long enough raises the setup
// access point again. The wizard is not re-run and the provisioned marker is not
// touched: the node is still set up, still claimed, still holding its config. The
// AP comes back purely so an operator can reach it and hand it a new network.
package netwatch

import (
	"bufio"
	"context"
	"os"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
)

const (
	// DefaultGrace is how long the node tolerates having no route out before it
	// raises the access point.
	//
	// Ten minutes is chosen against the two things that actually happen: a router
	// rebooting after a firmware update, which is back inside three or four, and a
	// node that has genuinely lost its network, which never comes back. Raising the
	// AP for a router reboot would mean an open network appearing in the shack every
	// time the ISP hiccups.
	DefaultGrace = 10 * time.Minute

	// DefaultInterval is how often the route table is checked. Reading
	// /proc/net/route is a few microseconds, so this is set by how quickly the
	// grace period should be measured rather than by cost.
	DefaultInterval = 30 * time.Second

	// RoutePath is the kernel's IPv4 route table.
	RoutePath = "/proc/net/route"
)

// APRaiser is the part of the setup access point netwatch needs.
type APRaiser interface {
	Reraise(ctx context.Context, why captive.Reason) error
}

// Watcher raises the setup access point when the node has been off the network
// for longer than its grace period.
type Watcher struct {
	// AP is raised when the grace period expires.
	AP APRaiser

	// Online reports whether the node has a route out. Nil uses the kernel's
	// route table, ignoring the AP's own interface.
	Online func() bool

	// APInterface is excluded from the route check. Without it the access point's
	// own route would count as connectivity, and a node that raised the AP would
	// immediately conclude it was back online.
	APInterface string

	// Grace and Interval default to the constants above.
	Grace    time.Duration
	Interval time.Duration

	// Now and Logf are injectable for tests.
	Now  func() time.Time
	Logf func(format string, args ...any)

	// offSince is when connectivity was last seen to be gone; zero means the node
	// is online as far as this watcher knows.
	offSince time.Time
	// raised records that the AP has already been raised for this outage, so a
	// continuing outage does not re-raise it every interval.
	raised bool
}

func (w *Watcher) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Watcher) grace() time.Duration {
	if w.Grace > 0 {
		return w.Grace
	}
	return DefaultGrace
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

func (w *Watcher) online() bool {
	if w.Online != nil {
		return w.Online()
	}
	return HasDefaultRoute(RoutePath, w.APInterface)
}

// Check runs one evaluation. It is separate from Run so the whole state machine
// is testable without a clock or a goroutine.
func (w *Watcher) Check(ctx context.Context) {
	now := w.now()

	if w.online() {
		if !w.offSince.IsZero() {
			w.logf("netwatch: the node has a route out again after %s", now.Sub(w.offSince).Round(time.Second))
		}
		// The outage is over. Clearing raised means a *later* outage gets its own
		// grace period and its own AP, rather than the watcher believing it has
		// already dealt with this.
		w.offSince, w.raised = time.Time{}, false
		return
	}

	if w.offSince.IsZero() {
		w.offSince = now
		w.logf("netwatch: no route out; the setup access point comes back if this lasts %s", w.grace())
		return
	}
	if w.raised {
		return
	}
	if now.Sub(w.offSince) < w.grace() {
		return
	}

	w.raised = true
	if w.AP == nil {
		return
	}
	// Nothing here touches the provisioned marker, the config store, or the claim
	// state. The node is still set up and still claimed; it has simply lost the
	// network it was set up on, and the AP is how an operator hands it a new one.
	if err := w.AP.Reraise(ctx, captive.ReasonUpstreamLost); err != nil {
		w.logf("netwatch: could not raise the setup access point after %s offline: %v", w.grace(), err)
		return
	}
	w.logf("netwatch: no route out for %s — the setup access point is back so this node can be given a new network", w.grace())
}

// Run evaluates on every tick until ctx is cancelled. Passing the ticker in keeps
// the loop free of a real clock, which is what lets the grace period be tested in
// microseconds rather than minutes.
func (w *Watcher) Run(ctx context.Context, tick <-chan time.Time) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick:
			w.Check(ctx)
		}
	}
}

// HasDefaultRoute reports whether the kernel has a default route over an
// interface other than excludeIface.
//
// The route table rather than a connectivity probe on purpose. A probe means
// choosing a host to reach, and a node that raised its own access point because
// some third party's server was down would be worse than one that never raised it
// at all. "Is there a way out of here" is a question the kernel can answer without
// any opinion about what is at the other end.
func HasDefaultRoute(path, excludeIface string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close() //nolint:errcheck // read-only

	sc := bufio.NewScanner(f)
	if sc.Scan() { // header row
		_ = sc.Text()
	}
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		// Iface Destination Gateway Flags RefCnt Use Metric Mask …
		if len(fields) < 4 {
			continue
		}
		iface, destination := fields[0], fields[1]
		// 00000000 is 0.0.0.0 — the default route.
		if destination != "00000000" {
			continue
		}
		if excludeIface != "" && iface == excludeIface {
			// The access point's own route is not a way out. Counting it would mean
			// a node that raised the AP immediately decided it was back online and
			// took it down again.
			continue
		}
		return true
	}
	return false
}
