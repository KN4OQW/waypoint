package main

import (
	"log"
	"net/http"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// setupOptions is the first-boot wizard's configuration, gathered from flags.
type setupOptions struct {
	// Enabled turns the wizard on. It is on by default on a node and off in tests
	// that build a server directly, which is why a nil *wizard.Wizard has to mean
	// "no gate" rather than panicking.
	Enabled bool
	// Socket is the privileged helper's Unix socket.
	Socket string
	// Marker is the provisioned marker; Progress is the in-flight progress file.
	Marker   string
	Progress string
}

// initSetup builds the wizard and wires it to the privileged helper.
//
// waypointd is the *unprivileged* end of this: it dials the socket and can ask
// for eleven specific things. It never runs useradd, never writes to /etc, and
// holds no capability the helper does not hand it one call at a time. That is the
// whole reason the helper is a separate process.
func (s *server) initSetup(opts setupOptions, st *store.Store) {
	if !opts.Enabled {
		return
	}
	marker := opts.Marker
	if marker == "" {
		marker = provision.DefaultPath
	}

	w := &wizard.Wizard{
		Prov:         privhelper.NewClient(opts.Socket),
		MarkerPath:   marker,
		ProgressPath: opts.Progress,
		Claimed:      s.Claimed,
		Logf:         log.Printf,
	}
	// The mirror is a convenience for the API; the marker is authoritative, so a
	// store that will not take the column is logged and moved past rather than
	// blocking setup.
	if st != nil {
		if m, err := provision.NewMirror(st); err != nil {
			log.Printf("waypointd: provisioned-flag mirror unavailable (the marker is still authoritative): %v", err)
		} else {
			w.Mirror = m
		}
	}
	s.wiz = w

	if w.Provisioned() {
		log.Printf("waypointd: node is provisioned (marker %s)", marker)
		return
	}
	log.Printf("waypointd: node is NOT provisioned — serving the setup wizard; next step is %q", w.Next())
	log.Printf("waypointd: privileged operations go to the helper at %s", nonEmpty(opts.Socket, privhelper.DefaultSocketPath))
}

// setupGate wraps h in the wizard gate when the wizard is configured.
//
// The nesting is provisioning outside claiming: an unprovisioned node has no
// hostname the operator chose and an unlocked root, so there is nothing there to
// claim yet and the claim gate should not see the request at all.
func (s *server) setupGate(h http.Handler) http.Handler {
	if s.wiz == nil {
		return h
	}
	return s.wiz.Gate(h)
}

func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
