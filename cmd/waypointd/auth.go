package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// resetMarkerPaths are the boot-partition reset markers checked at daemon start.
// Waypoint targets Raspberry Pi OS across the Bookworm boot-mount move (/boot →
// /boot/firmware), so both locations are checked. A package var so tests can point
// it at a temp dir. See RFC-0002 "Reset procedure (b)".
var resetMarkerPaths = []string{
	"/boot/waypoint-reset",
	"/boot/firmware/waypoint-reset",
}

// checkResetMarker performs the boot-partition reset path.
//
// The marker triggers a *full* re-provision, not just a claim reset. The two
// reset paths are deliberately different depths, matched to what the person
// running them can already do:
//
//   - `waypointd reset-claim` needs a shell on the box. Whoever has one can
//     already read the config and restart the daemons, so handing the dashboard
//     to a new administrator is the whole job — the node's identity is not in
//     question.
//   - This marker needs the SD card in a reader. That is someone holding the
//     hardware, and the reason they are holding it is almost always that they
//     cannot get in at all: a forgotten password on a node whose root is locked,
//     or a second-hand board with somebody else's setup on it. A claim reset
//     would hand them a dashboard on a node still called someone else's hostname,
//     with someone else's recovery account and someone else's SSH key. The full
//     reset is what that situation actually calls for.
//
// It clears the marker, discards setup progress, and clears the mirror, then
// deletes itself and logs loudly — a security-relevant, auditable event. Marker
// deletion failure is logged and tolerated: it must not silently loop the reset,
// but neither can it abort startup.
func checkResetMarker(as *auth.Store, st *store.Store, markers []string, paths resetPaths, logf func(string, ...any)) (bool, error) {
	for _, p := range markers {
		if _, err := os.Stat(p); err != nil {
			continue // marker absent (or unreadable) — nothing to do for this path
		}
		logf("SECURITY: boot-partition reset marker %s found — returning device to the unclaimed, unprovisioned state", p)
		res, err := resetFull(as, st, paths)
		if err != nil {
			return false, fmt.Errorf("marker reset: %w", err)
		}
		if err := os.Remove(p); err != nil {
			// Loudly surfaced, not fatal: the next boot may re-run the reset, which is
			// harmless (idempotent) but must be visible rather than a silent loop.
			logf("SECURITY: reset marker %s could NOT be deleted (%v) — it will re-trigger on next boot until removed", p, err)
		} else {
			logf("SECURITY: %s; marker %s deleted", res.describe("(daemon store)"), p)
		}
		return true, nil // one marker is enough; do not process the rest
	}
	return false, nil
}

// runResetClaim implements the `waypointd reset-claim` subcommand: it connects to
// the store directly and performs the same wipe as the marker path, for an
// operator who already has a shell on the device. It prints what it did. Returns a
// process exit code.
func runResetClaim(args []string) int {
	fs := flag.NewFlagSet("reset-claim", flag.ExitOnError)
	storePath := fs.String("store", "/home/pi-star/waypoint/config.db", "path to the SQLite configuration store")
	// --full is the deeper reset: it also forgets that the node was ever set up,
	// so the next boot serves the setup wizard. It is opt-in because the common
	// case — an operator handing a working node to somebody else — wants the
	// dashboard reset and nothing else touched.
	full := fs.Bool("full", false, "also clear the provisioned marker so the next boot re-runs first-boot setup")
	markerPath := fs.String("provision-marker", provision.DefaultPath, "path to the provisioned marker (--full only)")
	progressPath := fs.String("setup-progress", wizard.DefaultProgressPath, "path to the setup progress file (--full only)")
	_ = fs.Parse(args)

	st, err := store.Open(*storePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-claim: open store %s: %v\n", *storePath, err)
		return 1
	}
	defer st.Close()
	as, err := auth.NewStore(st)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-claim: prepare auth store: %v\n", err)
		return 1
	}

	paths := resetPaths{Marker: *markerPath, Progress: *progressPath}
	var res resetResult
	if *full {
		res, err = resetFull(as, st, paths)
	} else {
		res, err = resetClaim(as)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reset-claim: %v\n", err)
		return 1
	}

	// Nothing to report is still worth reporting: an operator who ran the wrong
	// command needs to know it did nothing.
	if !res.WasClaimed && res.AdminUser == "" && !res.WasProvisioned {
		if *full {
			fmt.Printf("reset-claim --full: device was already unclaimed and unprovisioned; nothing to clear (store %s)\n", *storePath)
		} else {
			fmt.Printf("reset-claim: device was already unclaimed; store %s left in claim mode\n", *storePath)
		}
		return 0
	}
	fmt.Println(res.describe(*storePath))
	return 0
}

// buildAuth attaches the auth subsystem to the store and applies the boot-marker
// reset before the server starts serving. secureCookie is wired to the TLS story:
// false until the TLS PR flips it (a pre-TLS build must not set Secure).
func buildAuth(st *store.Store, secureCookie bool, paths resetPaths) (*auth.Auth, error) {
	as, err := auth.NewStore(st)
	if err != nil {
		return nil, err
	}
	if _, err := checkResetMarker(as, st, resetMarkerPaths, paths, log.Printf); err != nil {
		// A failed reset is security-relevant; do not silently start claimed.
		return nil, err
	}
	return auth.New(as, auth.Options{SecureCookie: secureCookie}), nil
}
