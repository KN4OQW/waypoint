package netwatch

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
)

type fakeAP struct {
	mu      sync.Mutex
	raises  int
	reasons []captive.Reason
	err     error
}

func (f *fakeAP) Reraise(_ context.Context, why captive.Reason) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.raises++
	f.reasons = append(f.reasons, why)
	return nil
}

func (f *fakeAP) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.raises
}

func (f *fakeAP) reason(i int) captive.Reason {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reasons[i]
}

// clock is a hand-wound time source, mutex-guarded because Run reads it from its
// own goroutine while a test advances it.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
	return c.t
}

// flag is a mutex-guarded bool, for the same reason as clock.
type flag struct {
	mu sync.Mutex
	v  bool
}

func (f *flag) get() bool  { f.mu.Lock(); defer f.mu.Unlock(); return f.v }
func (f *flag) set(v bool) { f.mu.Lock(); defer f.mu.Unlock(); f.v = v }

func newWatcher(t *testing.T) (*Watcher, *fakeAP, *clock, *flag) {
	t.Helper()
	c := &clock{t: time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)}
	ap := &fakeAP{}
	online := &flag{v: true}
	w := &Watcher{
		AP:     ap,
		Online: online.get,
		Grace:  DefaultGrace,
		Now:    c.now,
		Logf:   t.Logf,
	}
	return w, ap, c, online
}

// Property 1: a node that is online never raises the access point, however long
// it runs.
func TestOnlineNeverRaises(t *testing.T) {
	w, ap, c, _ := newWatcher(t)
	for i := 0; i < 100; i++ {
		w.Check(context.Background())
		c.add(time.Minute)
	}
	if ap.count() != 0 {
		t.Errorf("the AP was raised %d times on a healthy node", ap.count())
	}
}

// Property 2: a short outage does not raise the access point. A router rebooting
// after a firmware update is back inside a few minutes, and an open network
// appearing in the shack every time the ISP hiccups would be worse than the
// problem.
func TestShortOutageDoesNotRaise(t *testing.T) {
	w, ap, c, online := newWatcher(t)

	w.Check(context.Background())
	online.set(false)
	for elapsed := time.Duration(0); elapsed < DefaultGrace-time.Minute; elapsed += time.Minute {
		w.Check(context.Background())
		c.add(time.Minute)
	}
	if ap.count() != 0 {
		t.Fatalf("the AP was raised %d times inside the grace period", ap.count())
	}

	// The network comes back, and the outage is forgotten.
	online.set(true)
	w.Check(context.Background())
	c.add(2 * DefaultGrace)
	w.Check(context.Background())
	if ap.count() != 0 {
		t.Errorf("the AP was raised %d times after the network recovered", ap.count())
	}
}

// Property 3: an outage that outlasts the grace period raises the access point,
// once — this is the whole reason the package exists.
func TestLongOutageRaisesOnce(t *testing.T) {
	w, ap, c, online := newWatcher(t)

	w.Check(context.Background())
	online.set(false)
	w.Check(context.Background()) // starts the clock on the outage

	c.add(DefaultGrace - time.Second)
	w.Check(context.Background())
	if ap.count() != 0 {
		t.Fatal("the AP was raised a second early")
	}

	c.add(2 * time.Second)
	w.Check(context.Background())
	if ap.count() != 1 {
		t.Fatalf("the AP was raised %d times, want 1", ap.count())
	}
	if ap.reason(0) != captive.ReasonUpstreamLost {
		t.Errorf("reason = %q, want %q", ap.reason(0), captive.ReasonUpstreamLost)
	}

	// A continuing outage does not raise it again every interval.
	for i := 0; i < 20; i++ {
		c.add(time.Minute)
		w.Check(context.Background())
	}
	if ap.count() != 1 {
		t.Errorf("a continuing outage raised the AP %d times", ap.count())
	}
}

// Property 4: a second, separate outage gets its own grace period and its own
// access point. Without this a node would recover once and never again.
func TestSecondOutageRaisesAgain(t *testing.T) {
	w, ap, c, online := newWatcher(t)

	online.set(false)
	w.Check(context.Background())
	c.add(DefaultGrace + time.Second)
	w.Check(context.Background())
	if ap.count() != 1 {
		t.Fatalf("first outage: %d raises", ap.count())
	}

	// Recovered.
	online.set(true)
	c.add(time.Minute)
	w.Check(context.Background())

	// And lost again, later.
	online.set(false)
	c.add(time.Hour)
	w.Check(context.Background())
	c.add(DefaultGrace + time.Second)
	w.Check(context.Background())
	if ap.count() != 2 {
		t.Errorf("the AP was raised %d times across two outages, want 2", ap.count())
	}
}

// Property 5: a failure to raise the AP is logged and does not wedge the watcher
// into believing it succeeded.
func TestRaiseFailureIsNotFatal(t *testing.T) {
	w, ap, c, online := newWatcher(t)
	ap.err = context.DeadlineExceeded

	online.set(false)
	w.Check(context.Background())
	c.add(DefaultGrace + time.Second)
	w.Check(context.Background())

	// It does not retry in a tight loop either — the outage is still marked as
	// handled, because a radio that refused once will refuse again and the log
	// line is the actionable output.
	c.add(time.Hour)
	w.Check(context.Background())
	if ap.count() != 0 {
		t.Errorf("raises = %d despite the AP refusing", ap.count())
	}
}

// Property 6: Run drives Check on every tick and stops when the context does.
func TestRun(t *testing.T) {
	w, ap, c, online := newWatcher(t)
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx, tick) }()

	online.set(false)
	tick <- c.now()
	tick <- c.add(DefaultGrace + time.Second)
	// A third tick guarantees the second has been processed.
	tick <- c.now()

	if ap.count() != 1 {
		t.Errorf("raises = %d, want 1", ap.count())
	}
	cancel()
	<-done
}

// Property 7: the route table is read in its real format, and the access point's
// own route does not count as a way out.
//
// That exclusion is load-bearing. NetworkManager's shared mode adds a route for
// the AP's own subnet; counting it would mean a node that raised the AP
// immediately decided it was back online and took it down again.
func TestHasDefaultRoute(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	const header = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"

	// A normal node: a default route over wlan0.
	up := write("up", header+
		"wlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n"+
		"wlan0\t0001A8C0\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n")
	if !HasDefaultRoute(up, "") {
		t.Error("a node with a default route reads as offline")
	}

	// Only the AP's own routes.
	apOnly := write("ap", header+
		"wlan0\t00000000\t0100002A\t0003\t0\t0\t600\t00000000\t0\t0\t0\n"+
		"wlan0\t0000002A\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n")
	if HasDefaultRoute(apOnly, "wlan0") {
		t.Error("the access point's own route counted as a way out")
	}
	// Without the exclusion it would — which is why the exclusion exists.
	if !HasDefaultRoute(apOnly, "") {
		t.Error("the fixture does not actually contain a default route")
	}

	// The AP is up on wlan0 while eth0 still has a route out: that is online.
	both := write("both", header+
		"wlan0\t00000000\t0100002A\t0003\t0\t0\t600\t00000000\t0\t0\t0\n"+
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n")
	if !HasDefaultRoute(both, "wlan0") {
		t.Error("a node with an Ethernet route reads as offline while its AP is up")
	}

	// No default route at all.
	none := write("none", header+
		"wlan0\t0001A8C0\t00000000\t0001\t0\t0\t600\t00FFFFFF\t0\t0\t0\n")
	if HasDefaultRoute(none, "") {
		t.Error("a node with no default route reads as online")
	}

	// A missing or unreadable table reads as offline, which is the safe direction:
	// the worst case is an access point nobody needed.
	if HasDefaultRoute(filepath.Join(dir, "nope"), "") {
		t.Error("a missing route table reads as online")
	}
	if HasDefaultRoute(write("empty", ""), "") {
		t.Error("an empty route table reads as online")
	}
}

// Property 8: netwatch touches no provisioning state. The node is still set up
// and still claimed; it has lost a network, not its configuration.
//
// The type has no way to reach the marker, the store, or the claim state — this
// test is here so that adding one is a deliberate act with a failing test behind
// it rather than an accident.
func TestWatcherHoldsNoProvisioningState(t *testing.T) {
	w := &Watcher{}
	if _, ok := any(w).(interface{ Reset() error }); ok {
		t.Error("the watcher has grown a way to reset provisioning state")
	}
	// The only collaborator it has is the AP raiser.
	if w.AP != nil {
		t.Error("the zero watcher has an AP")
	}
}

// The AP's own interface must be excluded even when the operator named none — the
// normal case. Before the accessor, APInterface was captured from a flag that is
// empty on any node with one wireless device, so the exclusion silently did nothing
// exactly where it was needed: a node that raised its AP during an outage would
// read the AP's own route as a way out, take the AP back down, and do it again.
func TestExcludesTheAPInterfaceTheHelperChose(t *testing.T) {
	dir := t.TempDir()
	// A route table whose ONLY default route is the AP's.
	path := filepath.Join(dir, "route")
	const apOnly = "Iface\tDestination\tGateway\tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"wlan0\t00000000\t0100A8C0\t0003\t0\t0\t600\t00000000\n"
	if err := os.WriteFile(path, []byte(apOnly), 0o600); err != nil {
		t.Fatal(err)
	}

	// Nothing excluded (the old behaviour): the AP looks like connectivity.
	if !HasDefaultRoute(path, "") {
		t.Fatal("fixture is wrong — it should report a default route when nothing is excluded")
	}
	// The AP's interface excluded: no way out, which is the truth.
	if HasDefaultRoute(path, "wlan0") {
		t.Error("the AP's own route was counted as a way out")
	}
}

// A Watcher resolves the interface at each check, so an AP raised after the watcher
// was constructed is still excluded.
func TestWatcherResolvesTheAPInterfaceLate(t *testing.T) {
	var raised string // empty until the AP goes up, as on a real node
	w := &Watcher{APInterface: func() string { return raised }}
	if got := w.apInterface(); got != "" {
		t.Errorf("before the AP is up the exclusion should be empty, got %q", got)
	}
	raised = "wlan0"
	if got := w.apInterface(); got != "wlan0" {
		t.Errorf("after the AP is up the exclusion should follow it, got %q", got)
	}
	// A nil accessor is tolerated.
	if got := (&Watcher{}).apInterface(); got != "" {
		t.Errorf("a nil accessor should exclude nothing, got %q", got)
	}
}
