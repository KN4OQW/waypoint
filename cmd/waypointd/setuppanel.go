package main

import (
	"context"
	"log"
	"net"
	"os"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/lcd"
	"github.com/KN4OQW/waypoint/internal/lcd/hd44780"
)

// runSetupPanel drives a character LCD through first-boot setup, if one is wired.
//
// The panel is normally an operator-configured device — bus and address come out
// of the config, and nothing opens one that has not been declared. That rule
// cannot hold here: a node that has never been set up has no config and nobody
// to write one, and it is exactly then that a display has something worth saying.
// A freshly flashed board whose access point failed has no login (the image ships
// no account), no serial console (the modem owns the UART) and no network. Two
// lines of text are the difference between an operator seeing a dead board and
// seeing the reason it is dead.
//
// So this probes for a panel rather than being told about one, and it is
// strictly best-effort: no panel, an unreadable bus, or a display that falls off
// the bus mid-run are all non-events for the daemon.
func (s *server) runSetupPanel(ctx context.Context) {
	found, err := hd44780.Detect(nil)
	if err != nil {
		// Debug-level in spirit: no panel is the common case, and on a board with
		// I2C disabled there is not even a bus to look at. One quiet line so the
		// journal explains a display that stayed dark when one *was* wired.
		log.Printf("waypointd: no setup display detected (%v); setup is web-only", err)
		return
	}
	dev, err := hd44780.Open(found.Bus, found.Addr)
	if err != nil {
		log.Printf("waypointd: setup display at %s answered a probe but would not open: %v", found, err)
		return
	}
	log.Printf("waypointd: setup display on %s — showing setup status until the node is set up", found)
	bootLogf("setup display found on %s", found)

	if err := lcd.RunSetupScreen(ctx, dev, s.setupPanelStatus, time.NewTicker(lcd.SetupDwell()).C, log.Printf); err != nil &&
		err != context.Canceled {
		log.Printf("waypointd: setup display stopped: %v", err)
	}
}

// setupPanelStatus snapshots what the panel shows. It reads the same access-point
// state the wizard reports over HTTP, so the glass and the web UI cannot disagree
// about whether the node has a setup network.
func (s *server) setupPanelStatus() lcd.SetupStatus {
	st := lcd.SetupStatus{PortalURL: captive.DefaultAddress.String()}
	if s.wiz != nil {
		if s.wiz.APStatus != nil {
			if ap := s.wiz.APStatus(); ap != nil {
				st.SSID, st.APUp, st.Open, st.Err = ap.SSID, ap.Up, ap.Open, ap.Error
			}
		}
		// Read from the marker rather than tracking a flag here, so the glass and
		// the wizard cannot disagree about whether setup is finished.
		st.Done = s.wiz.Provisioned()
		st.Hostname = hostnameOrEmpty()
	}
	// The wired address matters most in the failure case: when there is no setup
	// network, this is the only way back into the node.
	st.IP = hostIPv4(net.InterfaceAddrs)
	return st
}

// hostnameOrEmpty is the node's hostname for the panel's "where did it go" line.
// An unreadable hostname is not worth an error path here — the panel falls back
// to the address, and to generic advice if it has neither.
func hostnameOrEmpty() string {
	h, err := os.Hostname()
	if err != nil {
		return ""
	}
	return h
}
