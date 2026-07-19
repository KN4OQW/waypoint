package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/ui"
)

// resetMarkerPaths are the boot-partition reset markers checked at daemon start.
// Waypoint targets Raspberry Pi OS across the Bookworm boot-mount move (/boot →
// /boot/firmware), so both locations are checked. A package var so tests can point
// it at a temp dir. See RFC-0002 "Reset procedure (b)".
var resetMarkerPaths = []string{
	"/boot/waypoint-reset",
	"/boot/firmware/waypoint-reset",
}

// checkResetMarker performs the boot-partition reset path: if a marker file is
// present, wipe the admin credential, revoke all sessions, clear claimed_at,
// delete the marker, and log the reset loudly (a security-relevant, auditable
// event). Marker deletion failure is logged and tolerated — it must not silently
// loop the reset, but neither can it abort startup. Returns whether a reset ran.
func checkResetMarker(as *auth.Store, paths []string, logf func(string, ...any)) (bool, error) {
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			continue // marker absent (or unreadable) — nothing to do for this path
		}
		logf("SECURITY: boot-partition reset marker %s found — returning device to the unclaimed state", p)
		if err := as.ResetClaim(); err != nil {
			return false, fmt.Errorf("marker reset: %w", err)
		}
		if err := os.Remove(p); err != nil {
			// Loudly surfaced, not fatal: the next boot may re-run the reset, which is
			// harmless (idempotent) but must be visible rather than a silent loop.
			logf("SECURITY: reset marker %s could NOT be deleted (%v) — it will re-trigger on next boot until removed", p, err)
		} else {
			logf("SECURITY: reset complete — admin credential wiped, all sessions revoked, claim mode restored; marker %s deleted", p)
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

	// Snapshot before-state so the operator sees exactly what was cleared.
	claimed, _ := as.IsClaimed()
	admin, hadAdmin, _ := as.Admin()
	sessions, _ := as.SessionCount()

	if err := as.ResetClaim(); err != nil {
		fmt.Fprintf(os.Stderr, "reset-claim: %v\n", err)
		return 1
	}

	if !claimed && !hadAdmin {
		fmt.Printf("reset-claim: device was already unclaimed; store %s left in claim mode\n", *storePath)
		return 0
	}
	who := ""
	if hadAdmin {
		who = fmt.Sprintf(" (admin %q)", admin.Username)
	}
	fmt.Printf("reset-claim: wiped admin credential%s, revoked %d session(s), cleared claimed_at — device returned to claim mode (store %s)\n",
		who, sessions, *storePath)
	return 0
}

// buildAuth attaches the auth subsystem to the store and applies the boot-marker
// reset before the server starts serving. secureCookie is wired to the TLS story:
// false until the TLS PR flips it (a pre-TLS build must not set Secure).
func buildAuth(st *store.Store, secureCookie bool) (*auth.Auth, error) {
	as, err := auth.NewStore(st)
	if err != nil {
		return nil, err
	}
	if _, err := checkResetMarker(as, resetMarkerPaths, log.Printf); err != nil {
		// A failed reset is security-relevant; do not silently start claimed.
		return nil, err
	}
	return auth.New(as, auth.Options{
		SecureCookie: secureCookie,
		// The pre-auth screen the gate serves at "/" while unclaimed or logged out.
		AuthPage: ui.AuthPage(),
	}), nil
}
