package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// listen stands in for systemd's notification socket.
func listen(t *testing.T) (*net.UnixConn, string) {
	t.Helper()
	// Short path: a Unix socket address is capped at 108 bytes and t.TempDir's
	// names are long.
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "n.sock")

	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Skipf("no unixgram support here: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn, path
}

func read(t *testing.T, conn *net.UnixConn) string {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 256)
	n, _, err := conn.ReadFromUnix(buf)
	if err != nil {
		t.Fatalf("no datagram arrived: %v", err)
	}
	return string(buf[:n])
}

// Property 1: the state lines systemd expects actually reach the socket.
func TestNotifyReachesTheSocket(t *testing.T) {
	conn, path := listen(t)
	t.Setenv(notifySocketEnv, path)

	if !Enabled() {
		t.Fatal("Enabled = false with NOTIFY_SOCKET set")
	}
	if err := Ready(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn); got != "READY=1" {
		t.Errorf("Ready sent %q", got)
	}
	if err := Watchdog(); err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn); got != "WATCHDOG=1" {
		t.Errorf("Watchdog sent %q", got)
	}
	if err := Status("serving 3 gateways"); err != nil {
		t.Fatal(err)
	}
	if got := read(t, conn); got != "STATUS=serving 3 gateways" {
		t.Errorf("Status sent %q", got)
	}
}

// Property 2: with no socket everything is a silent no-op.
//
// Running this daemon by hand, under a test, or in a container is supported, and
// none of those set NOTIFY_SOCKET. A notification path that errored there would
// turn a normal way of running the daemon into a stream of log noise.
func TestNoSocketIsANoOp(t *testing.T) {
	t.Setenv(notifySocketEnv, "")
	if Enabled() {
		t.Error("Enabled = true with no socket")
	}
	for name, fn := range map[string]func() error{"Ready": Ready, "Watchdog": Watchdog} {
		if err := fn(); err != nil {
			t.Errorf("%s with no socket = %v, want nil", name, err)
		}
	}
	if err := Status("x"); err != nil {
		t.Errorf("Status with no socket = %v", err)
	}
}

// Property 3: the ping interval is half of WatchdogSec.
//
// systemd kills the service if a ping does not arrive within the full interval,
// so pinging at the interval itself would make every scheduling hiccup on a
// loaded Pi Zero a restart. Half is the documented margin.
func TestWatchdogInterval(t *testing.T) {
	t.Setenv(watchdogPIDEnv, "")

	t.Setenv(watchdogUsecEnv, "30000000") // 30s
	if got := WatchdogInterval(); got != 15*time.Second {
		t.Errorf("interval = %v, want 15s (half of WatchdogSec)", got)
	}

	for _, unset := range []string{"", "0", "-1", "not-a-number"} {
		t.Setenv(watchdogUsecEnv, unset)
		if got := WatchdogInterval(); got != 0 {
			t.Errorf("WATCHDOG_USEC=%q gave %v, want 0", unset, got)
		}
	}
}

// Property 4: a child that inherited the environment does not answer on its
// parent's behalf. WATCHDOG_PID names the process the watchdog is for, and a
// forked helper pinging for a wedged parent would keep it alive forever.
func TestWatchdogPIDGuard(t *testing.T) {
	t.Setenv(watchdogUsecEnv, "30000000")

	t.Setenv(watchdogPIDEnv, strconv.Itoa(os.Getpid()))
	if got := WatchdogInterval(); got != 15*time.Second {
		t.Errorf("interval = %v for our own pid, want 15s", got)
	}

	t.Setenv(watchdogPIDEnv, strconv.Itoa(os.Getpid()+1))
	if got := WatchdogInterval(); got != 0 {
		t.Errorf("interval = %v for another process's pid, want 0", got)
	}
}
