package lcd_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/lcd"
)

// Every frame must be exactly the panel's geometry. The device writes rows
// verbatim, so a short line leaves the previous frame's tail on the glass and a
// long one wraps into the next row's DDRAM — both of which read as a fault in
// the panel rather than in this code.
func TestFramesAreAlwaysExactGeometry(t *testing.T) {
	cases := map[string]lcd.SetupStatus{
		"ap up":       {SSID: "Waypoint-Setup-9E10", APUp: true, Open: true, PortalURL: "http://10.42.0.1/"},
		"ap up wpa2":  {SSID: "Waypoint-Setup-9E10", APUp: true, PortalURL: "http://10.42.0.1/"},
		"ap failed":   {Err: "no wireless interface", IP: "172.16.50.13"},
		"ap failed 2": {Err: "no wireless interface"},
		"starting":    {},
		"long ssid":   {SSID: strings.Repeat("x", 200), APUp: true, PortalURL: "http://10.42.0.1/"},
	}
	for name, st := range cases {
		t.Run(name, func(t *testing.T) {
			frames := lcd.SetupFrames(st, lcd.SetupRows, lcd.SetupCols)
			if len(frames) == 0 {
				t.Fatal("no frames rendered; the panel would show a stale screen forever")
			}
			for i, f := range frames {
				if len(f) != lcd.SetupRows {
					t.Fatalf("frame %d has %d rows, want %d", i, len(f), lcd.SetupRows)
				}
				for r, line := range f {
					if len(line) != lcd.SetupCols {
						t.Errorf("frame %d row %d is %d cols (%q), want %d", i, r, len(line), line, lcd.SetupCols)
					}
				}
			}
		})
	}
}

// The SSID is the one thing an operator cannot proceed without, so it has to
// actually appear rather than being truncated away by a label.
func TestTheSSIDIsShownWhenTheAPIsUp(t *testing.T) {
	st := lcd.SetupStatus{SSID: "Waypoint-Setup-9E10", APUp: true, PortalURL: "http://10.42.0.1/"}
	if !framesContain(lcd.SetupFrames(st, lcd.SetupRows, lcd.SetupCols), "Waypoint-Setup") {
		t.Error("no frame shows the SSID")
	}
}

// An open setup network is a real, if temporary, exposure. The operator types a
// new account's password over it, so being told is not optional.
func TestAnOpenNetworkIsCalledOut(t *testing.T) {
	open := lcd.SetupStatus{SSID: "S", APUp: true, Open: true, PortalURL: "http://10.42.0.1/"}
	if !framesContain(lcd.SetupFrames(open, lcd.SetupRows, lcd.SetupCols), "OPEN") {
		t.Error("an open setup network is not called out on the panel")
	}
	closed := lcd.SetupStatus{SSID: "S", APUp: true, PortalURL: "http://10.42.0.1/"}
	if framesContain(lcd.SetupFrames(closed, lcd.SetupRows, lcd.SetupCols), "OPEN") {
		t.Error("a WPA2 setup network is described as open")
	}
}

// The failure case is the reason this screen earns its place: a node with no
// access point is otherwise silent. It must say that the wireless setup failed
// AND where to go instead.
func TestAFailedAPSaysSoAndOffersAWayIn(t *testing.T) {
	withIP := lcd.SetupFrames(lcd.SetupStatus{Err: "rfkill", IP: "172.16.50.13"}, lcd.SetupRows, lcd.SetupCols)
	if !framesContain(withIP, "FAILED") {
		t.Error("a failed AP does not say so")
	}
	if !framesContain(withIP, "172.16.50.13") {
		t.Error("the wired address is not offered as the way in")
	}

	noIP := lcd.SetupFrames(lcd.SetupStatus{Err: "rfkill"}, lcd.SetupRows, lcd.SetupCols)
	if !framesContain(noIP, "ethernet") {
		t.Error("with no address at all, the panel does not tell the operator what to do")
	}
}

// Once setup is done the setup network is being taken down, so the panel must
// stop advertising it. Leaving the old frame up tells somebody standing at the
// node to join a network that no longer exists — which is what it did.
func TestCompletionOutranksTheAccessPoint(t *testing.T) {
	// Done wins even while the AP is still technically up during the teardown
	// grace, because that is exactly the window somebody would misread.
	st := lcd.SetupStatus{SSID: "Waypoint-Setup-9E10", APUp: true, Done: true, IP: "172.16.50.13"}
	frames := lcd.SetupFrames(st, lcd.SetupRows, lcd.SetupCols)
	if framesContain(frames, "Waypoint-Setup") {
		t.Error("the panel still advertises the setup SSID after setup completed")
	}
	if !framesContain(frames, "complete") {
		t.Error("the panel does not say setup is complete")
	}
	if !framesContain(frames, "172.16.50.13") {
		t.Error("the panel does not say where the node moved to")
	}
}

// With no address yet, the hostname is the next best answer to "where did it go".
func TestCompletionFallsBackToTheHostname(t *testing.T) {
	st := lcd.SetupStatus{Done: true, Hostname: "kn4oqw-hs"}
	if !framesContain(lcd.SetupFrames(st, lcd.SetupRows, lcd.SetupCols), "kn4oqw-hs.local") {
		t.Error("with no IP, the panel does not offer the .local name")
	}
}

// Non-ASCII must not reach the panel: the HD44780 ROM would render it as
// garbage, and a screen of garbage reads as broken hardware.
func TestNonASCIIIsReplaced(t *testing.T) {
	frames := lcd.SetupFrames(lcd.SetupStatus{SSID: "café—ssid", APUp: true}, lcd.SetupRows, lcd.SetupCols)
	for _, f := range frames {
		for _, line := range f {
			for i := 0; i < len(line); i++ {
				if line[i] < 0x20 || line[i] >= 0x7f {
					t.Fatalf("byte %#x reached the panel in %q", line[i], line)
				}
			}
		}
	}
}

// recorder is a fake panel that records what was written. The screen paints from
// its own goroutine while the test inspects it, so every field is behind the
// mutex — a fake that races is a test that reports the wrong thing.
type recorder struct {
	mu         sync.Mutex
	rows, cols int
	lines      map[int]string
	writes     int
	closed     bool
	failNext   bool
}

func (r *recorder) Init(rows, cols int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows, r.cols = rows, cols
	return nil
}

func (r *recorder) Clear() error { return nil }

func (r *recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func (r *recorder) WriteLine(row int, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext {
		r.failNext = false
		return errFake
	}
	if r.lines == nil {
		r.lines = map[int]string{}
	}
	r.lines[row] = text
	r.writes++
	return nil
}

func (r *recorder) writeCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.writes
}

func (r *recorder) wasClosed() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed
}

type fakeErr struct{}

func (fakeErr) Error() string { return "panel fell off the bus" }

var errFake = fakeErr{}

// The screen must follow live state: the AP coming up after the screen started
// has to replace "starting up" without the loop being restarted.
func TestTheScreenFollowsLiveState(t *testing.T) {
	dev := &recorder{}
	var up atomic.Bool
	status := func() lcd.SetupStatus {
		if !up.Load() {
			return lcd.SetupStatus{}
		}
		return lcd.SetupStatus{SSID: "Waypoint-Setup-9E10", APUp: true, PortalURL: "http://10.42.0.1/"}
	}

	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	done := make(chan error, 1)
	go func() { done <- lcd.RunSetupScreen(ctx, dev, status, tick, nil) }()

	// The first paint happens before any tick, so a panel is never blank while
	// waiting out the first dwell.
	waitFor(t, func() bool { return dev.writeCount() > 0 })
	if !strings.Contains(joined(dev), "starting") {
		t.Fatalf("first paint = %q, want the starting-up screen", joined(dev))
	}

	up.Store(true)
	for i := 0; i < 6 && !strings.Contains(joined(dev), "Waypoint-Setup"); i++ {
		tick <- time.Now()
	}
	if !strings.Contains(joined(dev), "Waypoint-Setup") {
		t.Errorf("the screen never showed the SSID after the AP came up; last = %q", joined(dev))
	}

	cancel()
	if err := <-done; err != context.Canceled {
		t.Errorf("RunSetupScreen returned %v, want context.Canceled", err)
	}
	if !dev.wasClosed() {
		t.Error("the device was not closed")
	}
}

// A panel that fails mid-write must not stop the loop. The node is frequently
// displaying the reason it is broken, and losing the daemon over a flaky ribbon
// cable would be a strictly worse outcome than a missed frame.
func TestAWriteFailureDoesNotStopTheLoop(t *testing.T) {
	dev := &recorder{failNext: true}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tick := make(chan time.Time)
	done := make(chan error, 1)
	status := func() lcd.SetupStatus { return lcd.SetupStatus{SSID: "S", APUp: true} }
	go func() { done <- lcd.RunSetupScreen(ctx, dev, status, tick, nil) }()

	tick <- time.Now()
	tick <- time.Now()
	if dev.writeCount() == 0 {
		t.Error("the loop stopped after a write error instead of retrying")
	}
	cancel()
	<-done
}

func joined(r *recorder) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for i := 0; i < 4; i++ {
		b.WriteString(r.lines[i])
		b.WriteString("|")
	}
	return b.String()
}

func framesContain(frames [][]string, want string) bool {
	for _, f := range frames {
		if strings.Contains(strings.Join(f, "\n"), want) {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("timed out waiting for the panel to be painted")
}
