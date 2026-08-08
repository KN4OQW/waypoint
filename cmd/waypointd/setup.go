package main

import (
	"context"
	"log"
	"net/http"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/seed"
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
	// SeedPaths are the boot-partition provisioning files, searched in order.
	// Empty means seed.DefaultPaths; a single empty string disables the fast path.
	SeedPaths []string
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

	prov := s.prov
	if prov == nil {
		prov = privhelper.NewClient(opts.Socket)
	}
	w := &wizard.Wizard{
		Prov:         prov,
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
	// The certificate is reminted when the operator names the node. Doing it here
	// rather than inside the wizard keeps the wizard free of any opinion about
	// TLS, and means a node set up over Ethernet gets the same treatment as one
	// set up over the access point.
	// The same remint serves the ongoing rename through /api/network/host/apply,
	// so both paths share one implementation rather than drifting apart.
	if s.certs != nil {
		w.OnHostnameSet = func(_ context.Context, hostname string) {
			s.remintCertFor(hostname)
		}
	}

	s.wiz = w

	// The seed runs before anything reports on state, so a node that provisions
	// itself from the boot partition never announces that it needs a wizard and
	// never raises an access point for one.
	s.applySeed(opts)

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

// applySeed runs the boot-partition fast path, if there is one.
//
// Every failure here falls back to the wizard rather than stopping the daemon.
// The operator who wrote the file is not watching this boot; a node that refuses
// to come up because of a typo in a TOML is a node they cannot reach to fix the
// typo, while one that quietly serves the wizard is one they can.
func (s *server) applySeed(opts setupOptions) {
	paths := opts.SeedPaths
	if len(paths) == 1 && paths[0] == "" {
		return // explicitly disabled
	}

	path, err := seed.Find(paths)
	if err != nil {
		return // the normal case: no file, so the wizard it is
	}
	f, err := seed.Load(path)
	if err != nil {
		log.Printf("waypointd: %v — falling back to the setup wizard", err)
		return
	}
	log.Printf("waypointd: provisioning from %s (%s)", path, f.Describe())

	res, err := seed.Apply(context.Background(), s.wiz, f, path)
	if err != nil {
		log.Printf("waypointd: %s could not be applied (%v) — falling back to the setup wizard", path, err)
		return
	}
	if !res.Provisioned {
		log.Printf("waypointd: %s ignored — this node is already provisioned", path)
		return
	}
	if res.WiFiError != nil {
		// Not fatal, but the operator needs to know: the node is set up and may
		// have no way to be reached until netwatch raises the access point.
		log.Printf("waypointd: SETUP COMPLETE but the wi-fi join from %s failed: %v", path, res.WiFiError)
	}
	if err := seed.Consume(path); err != nil {
		// The file holds a wi-fi passphrase, and an account password unless the
		// operator used a hash. Leaving it readable on the card is worth shouting
		// about.
		log.Printf("waypointd: WARNING: %v — it still holds the credentials it carried", err)
	}
	log.Printf("waypointd: provisioned from the boot partition; no setup access point will be raised. "+
		"Claim the node over https://%s.local/", f.System.Hostname)
}

func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
