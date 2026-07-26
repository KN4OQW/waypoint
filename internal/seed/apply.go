package seed

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/KN4OQW/waypoint/internal/wizard"
)

// Result reports what a seed run did, for the log and for the caller deciding
// whether to raise the access point.
type Result struct {
	Path        string
	Provisioned bool
	Hostname    string
	User        string
	Keys        int
	WiFiJoined  bool
	// WiFiError is set when the join failed. It is not fatal — see Apply.
	WiFiError error
	// Consumed reports whether the file was removed afterwards.
	Consumed bool
}

// Apply drives the wizard through the seed file.
//
// It calls the same steps the interactive flow calls, in the same order, for the
// same reason a fast path should never be a second implementation: every
// validator, the layered recovery-account verification before the root lock, the
// idempotent convergence, and the marker write all live in those steps. A seed
// path that wrote the marker itself would be a provisioning route with none of
// the guards, reachable by anyone who can write to a FAT partition.
//
// The order is hostname, account, keys, Wi-Fi, lock. Wi-Fi comes before the lock
// so a node that joins successfully is reachable the moment it finishes, and the
// lock comes last for the same reason it does in the wizard: it is the step that
// makes the recovery account load-bearing.
func Apply(ctx context.Context, w *wizard.Wizard, f *File, path string) (Result, error) {
	res := Result{Path: path, Hostname: f.System.Hostname, User: f.User.Name, Keys: len(f.Keys())}

	if w.Provisioned() {
		// Already set up. The file is stale — a card reflashed onto a node that had
		// been through setup, or a seed left behind after a run that could not
		// remove it. Applying it would rename a live node out from under its
		// operator.
		return res, nil
	}

	keys := f.Keys()
	var firstKey string
	if len(keys) > 0 {
		firstKey = keys[0]
	}

	if _, err := w.SetHostname(ctx, wizard.HostnameRequest{Hostname: f.System.Hostname}); err != nil {
		return res, fmt.Errorf("hostname: %w", err)
	}

	user := wizard.UserRequest{Username: f.User.Name, SSHKey: firstKey}
	if f.User.Password != "" {
		if f.User.PasswordEncrypted {
			user.PasswordHash = f.User.Password
		} else {
			user.Password = f.User.Password
		}
	}
	if _, err := w.CreateUser(ctx, user); err != nil {
		return res, fmt.Errorf("recovery account: %w", err)
	}

	// The first key went in with the account; the rest follow. A file listing a
	// laptop and a phone should end with both, not with whichever was written first.
	for i, k := range keys[min(1, len(keys)):] {
		if _, err := w.InstallKey(ctx, wizard.KeyRequest{PublicKey: k}); err != nil {
			return res, fmt.Errorf("authorized_keys[%d]: %w", i+1, err)
		}
	}
	if len(keys) == 0 {
		// A password-only account still has to clear the key step.
		if _, err := w.InstallKey(ctx, wizard.KeyRequest{Skip: true}); err != nil {
			return res, fmt.Errorf("key step: %w", err)
		}
	}

	if f.HasWiFi() {
		// A failed join is deliberately not fatal. The node may be on Ethernet, and
		// refusing to finish provisioning over a mistyped Wi-Fi password would leave
		// it unprovisioned — which on a node with no other network means it raises
		// the access point and waits for somebody who is not there. Finishing and
		// letting netwatch raise the AP later is the recoverable outcome.
		if _, err := w.JoinNetwork(ctx, wizard.JoinRequest{
			SSID:    f.WLAN.SSID,
			PSK:     f.WLAN.Password,
			Hidden:  f.WLAN.Hidden,
			Country: f.WLAN.Country,
		}); err != nil {
			res.WiFiError = err
		} else {
			res.WiFiJoined = true
		}
	}

	if _, err := w.Lock(ctx, wizard.LockRequest{PasswordAuth: f.PasswordAuth()}); err != nil {
		return res, fmt.Errorf("lock: %w", err)
	}
	res.Provisioned = true
	return res, nil
}

// Consume removes the seed file.
//
// It is removed rather than kept because of what is in it: a plaintext Wi-Fi
// passphrase and, unless the operator used a hash, a plaintext account password —
// on a FAT partition that anybody who picks up the card can read, indefinitely.
// The node has already taken everything it needed.
//
// Failure is logged, not fatal. The provisioned marker is written by then, so the
// next boot skips the seed regardless; a file that could not be deleted is a
// disclosure worth shouting about, not a reason to fail a completed setup.
func Consume(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("seed: remove %s: %w", path, err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
