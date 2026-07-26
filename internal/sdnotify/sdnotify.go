// Package sdnotify speaks systemd's service-readiness protocol.
//
// It is a dozen lines of datagram writing rather than a dependency because that
// is all the protocol is: a Unix datagram socket named in $NOTIFY_SOCKET, and
// newline-separated key=value lines written to it. Pulling in a library for that
// would add a supply-chain surface to a daemon whose whole update story is about
// verifying what it runs.
//
// The point of using it at all is WatchdogSec. A hotspot that has wedged — the
// HTTP listener alive but the event loop stuck, the classic shape of a deadlock —
// looks identical to a healthy one from the outside, and systemd's Restart= will
// never fire because the process has not exited. A watchdog ping from inside the
// loop is the only thing that can tell those apart, and the only thing that gets
// the node back on the air without somebody power-cycling it.
package sdnotify

import (
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// notifySocketEnv is where systemd names the notification socket.
const notifySocketEnv = "NOTIFY_SOCKET"

// watchdogEnv carries the WatchdogSec= interval, in microseconds.
const (
	watchdogUsecEnv = "WATCHDOG_USEC"
	watchdogPIDEnv  = "WATCHDOG_PID"
)

// Enabled reports whether this process was started by systemd with notification
// enabled. Everything below is a no-op when it is not, so a daemon run by hand or
// under a test harness behaves identically minus the pings.
func Enabled() bool { return socketPath() != "" }

func socketPath() string { return os.Getenv(notifySocketEnv) }

// Notify sends one state line. An unset socket is success, not an error: running
// outside systemd is a supported way to run this daemon.
func Notify(state string) error {
	path := socketPath()
	if path == "" {
		return nil
	}
	// A leading '@' means an abstract socket, which Go spells with a leading NUL.
	if strings.HasPrefix(path, "@") {
		path = "\x00" + path[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer conn.Close() //nolint:errcheck // datagram; nothing to flush
	_, err = conn.Write([]byte(state))
	return err
}

// Ready tells systemd the daemon is up. With Type=notify it is what unblocks
// units ordered After= this one, so it belongs after the listeners are bound, not
// at the top of main.
func Ready() error { return Notify("READY=1") }

// Watchdog sends one keep-alive.
func Watchdog() error { return Notify("WATCHDOG=1") }

// Status sets the one-line status systemctl shows, which is where an operator
// looks first.
func Status(s string) error { return Notify("STATUS=" + s) }

// WatchdogInterval returns the ping interval systemd expects, or zero when the
// watchdog is off.
//
// It returns half of WatchdogSec, which is the documented margin: systemd kills
// the service if a ping does not arrive within the full interval, so pinging at
// the interval itself would make every scheduling hiccup a restart.
func WatchdogInterval() time.Duration {
	raw := os.Getenv(watchdogUsecEnv)
	if raw == "" {
		return 0
	}
	usec, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || usec <= 0 {
		return 0
	}
	// WATCHDOG_PID, when set, names the process the watchdog is meant for. A child
	// that inherited the environment must not answer on its parent's behalf.
	if pid := os.Getenv(watchdogPIDEnv); pid != "" {
		if want, err := strconv.Atoi(pid); err == nil && want != os.Getpid() {
			return 0
		}
	}
	return time.Duration(usec) * time.Microsecond / 2
}
