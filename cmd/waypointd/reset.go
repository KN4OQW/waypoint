package main

import (
	"fmt"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// resetPaths names the files a reset touches. It is a struct so tests can point
// every one of them at a temp directory, and so a caller cannot pass three
// strings in the wrong order.
type resetPaths struct {
	Marker   string // the provisioned marker (internal/provision)
	Progress string // the in-flight setup progress file (internal/wizard)
}

func (r resetPaths) marker() string {
	if r.Marker == "" {
		return provision.DefaultPath
	}
	return r.Marker
}

func (r resetPaths) progress() string {
	if r.Progress == "" {
		return wizard.DefaultProgressPath
	}
	return r.Progress
}

// resetResult reports what a reset actually cleared, so the operator sees the
// difference between the two depths rather than a generic "done".
type resetResult struct {
	WasClaimed     bool
	AdminUser      string
	Sessions       int
	WasProvisioned bool
	Full           bool
}

// resetClaim returns the device to the unclaimed state: the admin credential is
// wiped, every session revoked, and claimed_at cleared.
//
// The node stays provisioned. Its hostname, its recovery account, and its locked
// root are all untouched — this is "somebody else should administer this box",
// not "this box should forget what it is".
func resetClaim(as *auth.Store) (resetResult, error) {
	res := resetResult{}
	res.WasClaimed, _ = as.IsClaimed()
	if admin, ok, _ := as.Admin(); ok {
		res.AdminUser = admin.Username
	}
	res.Sessions, _ = as.SessionCount()

	if err := as.ResetClaim(); err != nil {
		return res, err
	}
	return res, nil
}

// resetFull is resetClaim plus the provisioning state: the marker is deleted, any
// in-flight setup progress is discarded, and the config.db mirror is cleared. The
// node comes back believing it has never been set up, so the next boot serves the
// wizard and raises the setup access point.
//
// What it deliberately does NOT do is undo any of it on the system. The hostname
// stays, the recovery account stays, root stays locked. Reverting those would
// need the privileged helper, and a reset that ran halfway — hostname reverted,
// root still locked, no account — would be worse than one that reverts nothing.
// Re-running setup is idempotent: it renames the host to whatever the operator
// picks next and converges the account rather than duplicating it.
func resetFull(as *auth.Store, st *store.Store, paths resetPaths) (resetResult, error) {
	res, err := resetClaim(as)
	if err != nil {
		return res, err
	}
	res.Full = true
	res.WasProvisioned = provision.IsProvisioned(paths.marker())

	if err := provision.Clear(paths.marker()); err != nil {
		return res, err
	}
	// Progress goes too, or the wizard would resume into the middle of a setup
	// describing an account and a hostname this reset just disowned.
	if err := wizard.ClearProgress(paths.progress()); err != nil {
		return res, err
	}
	if st != nil {
		m, err := provision.NewMirror(st)
		if err != nil {
			return res, fmt.Errorf("clear the provisioned mirror: %w", err)
		}
		if err := m.Set(false); err != nil {
			return res, fmt.Errorf("clear the provisioned mirror: %w", err)
		}
	}
	return res, nil
}

// describe renders what happened for an operator reading it on a terminal.
func (r resetResult) describe(storePath string) string {
	who := ""
	if r.AdminUser != "" {
		who = fmt.Sprintf(" (admin %q)", r.AdminUser)
	}
	claim := fmt.Sprintf("wiped admin credential%s, revoked %d session(s), cleared claimed_at", who, r.Sessions)
	if !r.Full {
		return fmt.Sprintf("reset-claim: %s — device returned to claim mode (store %s)", claim, storePath)
	}
	prov := "the node was already unprovisioned"
	if r.WasProvisioned {
		prov = "cleared the provisioned marker"
	}
	return fmt.Sprintf("reset-claim --full: %s; %s and discarded any setup progress — "+
		"the next boot serves the setup wizard and raises the setup access point. "+
		"The hostname, the recovery account, and root's lock are left as they are (store %s)",
		claim, prov, storePath)
}
