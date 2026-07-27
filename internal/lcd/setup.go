package lcd

import (
	"context"
	"strings"
	"time"
)

// The setup screen is a different job from the Renderer above it.
//
// The Renderer paints an operator's configured pages from live hub events, and
// needs a config, a page set and a running stack to be worth anything. None of
// those exist on a node that has never been set up — which is precisely when a
// panel has something urgent to say. A freshly flashed board with no access
// point is otherwise mute: no login, no serial console, no network. Two lines of
// text are the difference between "it is broken" and "it is on 10.42.0.1".
//
// So this is deliberately independent: no config, no hub, no tokens. It renders
// one small state into whole frames and rotates them.

// SetupRows and SetupCols are the geometry the setup screen assumes.
//
// A panel found by probing has not told anyone its size — the operator has not
// configured it yet, and HD44780 has no way to be asked. 16x2 is the safe floor:
// it is the smallest panel in the field, and the 4-bit init sequence drives a
// 20x4 in 2-line mode too, so a bigger panel renders this correctly in its top
// two rows rather than not at all.
const (
	SetupRows = 2
	SetupCols = 16
)

// setupDwell is how long each frame holds. Long enough to read a SSID without
// waiting through a cycle to re-read one you missed.
const setupDwell = 4 * time.Second

// SetupStatus is what the setup screen knows. It is a snapshot, passed by value:
// the caller owns the live state and this package never reaches back into it.
type SetupStatus struct {
	// SSID and APUp describe the setup access point.
	SSID string
	APUp bool
	// Open reports an unprotected setup network, which the operator should know
	// is temporary.
	Open bool
	// Err is why the access point is not up, if it is not.
	Err string
	// IP is the node's address on a wired network, when it has one. On a node
	// whose radio failed this is the entire remaining route in.
	IP string
	// PortalURL is where the wizard is served over the access point.
	PortalURL string
	// Done reports that setup finished. It outranks everything else: once the
	// access point is going away, the SSID on the glass is an instruction to join
	// a network that no longer exists.
	Done bool
	// Hostname is what the operator named the node, used to tell them where it
	// went once the setup network is gone.
	Hostname string
}

// SetupFrames renders the status into whole frames, each exactly rows lines of
// cols columns. It is pure: same status, same frames, no clock and no device.
//
// The ordering is by what an operator needs first. When the access point is up
// that is the network name, because they cannot do anything until they have
// joined it. When it is not, it is the fact that setup is unreachable over
// wireless, because otherwise they will keep looking for an SSID that is never
// going to appear.
func SetupFrames(st SetupStatus, rows, cols int) [][]string {
	var frames [][]string
	add := func(lines ...string) { frames = append(frames, fitFrame(lines, rows, cols)) }

	switch {
	case st.Done:
		// First, because it contradicts everything else the panel could say. The
		// setup network is going away; anyone still reading the old screen would be
		// trying to join it.
		add("Setup complete", "Setup wifi is off")
		switch {
		case st.IP != "":
			add("Now reach me at", st.IP)
		case st.Hostname != "":
			add("Now reach me at", st.Hostname+".local")
		default:
			add("Connect me to", "your network")
		}
	case st.APUp:
		add("Waypoint setup", "Join wifi:")
		add("Setup network:", st.SSID)
		if st.Open {
			// Said plainly rather than softened: it is an open network, and the
			// operator is about to type a password for a new account onto it.
			add("Network is OPEN", "until setup ends")
		}
		add("Then browse to", urlForPanel(st.PortalURL))
	case st.Err != "":
		add("SETUP WIFI", "FAILED TO START")
		if st.IP != "" {
			add("Reach me here:", st.IP)
		} else {
			// No AP and no address is the genuinely stuck case. The card is the
			// only remaining channel, so the panel points at it.
			add("No network yet.", "Plug in ethernet")
		}
		add("Why: see", "setup log on card")
	default:
		add("Waypoint", "starting up...")
	}

	if len(frames) == 0 { // unreachable today; a frame set is never empty by contract
		add("Waypoint")
	}
	return frames
}

// urlForPanel strips the scheme so the address fits a 16-column panel. Nobody
// needs to be told it is http when the alternative does not exist here.
func urlForPanel(u string) string {
	u = strings.TrimPrefix(u, "http://")
	u = strings.TrimPrefix(u, "https://")
	return strings.TrimSuffix(u, "/")
}

// fitFrame pads or truncates lines into exactly rows x cols. Truncation rather
// than scrolling: the setup screen's job is to be glanceable, and a marquee that
// has to be watched to the end is the opposite of that.
func fitFrame(lines []string, rows, cols int) []string {
	out := make([]string, rows)
	for i := range out {
		var s string
		if i < len(lines) {
			s = asciiOnly(lines[i])
		}
		if len(s) > cols {
			s = s[:cols]
		}
		out[i] = s + strings.Repeat(" ", cols-len(s))
	}
	return out
}

// asciiOnly replaces anything the HD44780 character ROM will not render. A
// hostname or SSID can contain arbitrary bytes and a panel showing mojibake
// reads as a broken panel.
func asciiOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r < 0x7f {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('?')
	}
	return b.String()
}

// RunSetupScreen paints the setup status on dev until ctx is canceled, rotating
// through the frames. status is called for each frame so the display follows the
// access point coming up or failing without being restarted.
//
// A write error is logged and retried on the next frame rather than ending the
// loop: a panel on a marginal connection must never be the reason the daemon
// stops doing anything else, and the failure it is displaying is more important
// than the display.
func RunSetupScreen(ctx context.Context, dev LCDDevice, status func() SetupStatus, tick <-chan time.Time, logf func(string, ...any)) error {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	defer dev.Close()

	if err := dev.Init(SetupRows, SetupCols); err != nil {
		return err
	}
	if err := dev.Clear(); err != nil {
		return err
	}

	var last []string
	frame := 0
	paint := func() {
		frames := SetupFrames(status(), SetupRows, SetupCols)
		if len(frames) == 0 {
			return
		}
		lines := frames[frame%len(frames)]
		frame++
		for i, line := range lines {
			// Only changed rows are written, as the renderer does: the I2C bus is
			// slow and a full repaint of an unchanged screen is visible as flicker.
			if i < len(last) && last[i] == line {
				continue
			}
			if err := dev.WriteLine(i, line); err != nil {
				logf("lcd: setup screen write failed on row %d: %v", i, err)
				last = nil // force a full repaint next time
				return
			}
		}
		last = lines
	}

	paint()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick:
			paint()
		}
	}
}

// SetupDwell is the frame interval callers should tick at.
func SetupDwell() time.Duration { return setupDwell }
