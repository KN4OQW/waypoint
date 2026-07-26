package wizard

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/provision"
)

// HostnameRequest names the node.
type HostnameRequest struct {
	Hostname string `json:"hostname"`
}

// SetHostname runs the first step.
//
// It always updates /etc/hosts. That is not a setting worth offering: a Debian
// box whose 127.0.1.1 entry names the old host is the one where sudo pauses for
// ten seconds before every command, and no operator would choose that.
func (w *Wizard) SetHostname(ctx context.Context, req HostnameRequest) (View, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := w.Load()
	if err := w.expect(p, StepHostname); err != nil {
		return w.state(), err
	}
	got, err := w.Prov.SetHostname(ctx, privhelper.SetHostnameRequest{
		Hostname: req.Hostname, UpdateHosts: true})
	if err != nil {
		return w.state(), err
	}

	p.Hostname = got.Hostname
	if err := w.save(p); err != nil {
		return w.state(), err
	}
	w.logf("wizard: hostname set to %q (was %q)", got.Hostname, got.Previous)
	return w.state(), nil
}

// UserRequest creates the recovery account.
type UserRequest struct {
	Username string `json:"username"`
	// Password is optional. Leaving it empty produces a key-only account, which is
	// the recommended shape and requires SSHKey to be set.
	Password string `json:"password,omitempty"`
	// SSHKey, when set, is installed with the account rather than in the following
	// step, so a key-only account is never briefly created without its key.
	SSHKey string `json:"ssh_key,omitempty"`
}

// CreateUser runs the second step.
//
// Sudo is not a choice the wizard offers. The recovery account exists to
// administer a node whose root is locked; one that cannot become root would be
// decoration, and offering the option would only let an operator build that by
// accident.
func (w *Wizard) CreateUser(ctx context.Context, req UserRequest) (View, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := w.Load()
	if err := w.expect(p, StepUser); err != nil {
		return w.state(), err
	}
	got, err := w.Prov.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: req.Username,
		Password: req.Password,
		SSHKey:   req.SSHKey,
		Sudo:     true,
	})
	if err != nil {
		return w.state(), err
	}

	p.RecoveryUser = got.Username
	p.UserHasPassword = !got.PasswordLocked
	if got.SSHKeyFingerprint != "" {
		// The key arrived with the account, so the key step is already satisfied.
		p.KeyFingerprint = got.SSHKeyFingerprint
	}
	if err := w.save(p); err != nil {
		return w.state(), err
	}
	w.logf("wizard: recovery account %q created (uid %d, password locked: %v)",
		got.Username, got.UID, got.PasswordLocked)
	return w.state(), nil
}

// KeyRequest installs an SSH key on the recovery account.
type KeyRequest struct {
	PublicKey string `json:"public_key,omitempty"`
	// Skip moves past the step without installing a key. It is refused for an
	// account with no password, where it would leave nobody able to log in.
	Skip bool `json:"skip,omitempty"`
}

// InstallKey runs the third step.
func (w *Wizard) InstallKey(ctx context.Context, req KeyRequest) (View, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := w.Load()
	if err := w.expect(p, StepKey); err != nil {
		return w.state(), err
	}

	if req.Skip {
		if !p.UserHasPassword {
			return w.state(), privhelper.Errorf(privhelper.CodeConflict,
				"%q has no password, so skipping the key would leave an account nobody can log in as", p.RecoveryUser)
		}
		p.KeySkipped = true
		if err := w.save(p); err != nil {
			return w.state(), err
		}
		w.logf("wizard: SSH key step skipped for %q (password login)", p.RecoveryUser)
		return w.state(), nil
	}

	got, err := w.Prov.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username:  p.RecoveryUser,
		PublicKey: req.PublicKey,
	})
	if err != nil {
		return w.state(), err
	}
	p.KeyFingerprint = got.Fingerprint
	p.KeySkipped = false
	if err := w.save(p); err != nil {
		return w.state(), err
	}
	w.logf("wizard: installed %s for %q", got.Fingerprint, p.RecoveryUser)
	return w.state(), nil
}

// LockRequest finishes setup.
type LockRequest struct {
	// PasswordAuth sets whether sshd accepts passwords. It defaults to false for a
	// key-only account and true otherwise, so the common case needs no answer; a
	// caller that wants the other thing says so.
	PasswordAuth *bool `json:"password_auth,omitempty"`
}

// Lock runs the final step: settle the SSH policy, lock root, and write the
// provisioned marker.
//
// The order is deliberate. SSH is settled first, because the recovery account has
// to be reachable before root stops being; the marker is written last, because it
// is what flips the gate, and flipping it before root is locked would leave a node
// that reports itself provisioned while still carrying an unlocked root.
func (w *Wizard) Lock(ctx context.Context, req LockRequest) (View, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := w.Load()
	if err := w.expect(p, StepLock); err != nil {
		return w.state(), err
	}

	passwordAuth := p.UserHasPassword
	if req.PasswordAuth != nil {
		passwordAuth = *req.PasswordAuth
	}
	if !passwordAuth && p.KeyFingerprint == "" {
		// Refusing passwords on an account with no key is the other way to end up
		// with a node nobody can reach.
		return w.state(), privhelper.Errorf(privhelper.CodeConflict,
			"turning off password authentication would leave %q unreachable: it has no SSH key", p.RecoveryUser)
	}
	if _, err := w.Prov.EnableSSH(ctx, privhelper.EnableSSHRequest{
		Enabled: true, PasswordAuth: &passwordAuth}); err != nil {
		return w.state(), err
	}
	if _, err := w.Prov.LockRoot(ctx, privhelper.LockRootRequest{}); err != nil {
		return w.state(), err
	}

	p.RootLocked = true
	if err := w.save(p); err != nil {
		return w.state(), err
	}

	now := w.now()
	st := provision.State{
		ProvisionedAt: now,
		UpdatedAt:     now,
		HostnameSet:   p.Hostname != "",
		Hostname:      p.Hostname,
		RecoveryUser:  p.RecoveryUser,
		RootLocked:    true,
	}
	if err := provision.Save(w.markerPath(), st); err != nil {
		return w.state(), privhelper.Errorf(privhelper.CodeInternal,
			"the node is set up but the marker could not be written, so setup would run again: %v", err)
	}
	if w.Mirror != nil {
		if err := w.Mirror.Set(true); err != nil {
			// The marker is authoritative; a stale mirror is a display bug, not a
			// reason to fail a completed setup.
			w.logf("wizard: could not mirror the provisioned flag into config.db: %v", err)
		}
	}
	// Progress has served its purpose. Removing it is best-effort: the marker now
	// answers the question, and a leftover progress file changes nothing.
	if err := removeIfExists(w.progressPath()); err != nil {
		w.logf("wizard: could not remove %s: %v", w.progressPath(), err)
	}

	w.logf("wizard: setup complete — %q, recovery account %q, root locked", p.Hostname, p.RecoveryUser)
	return w.state(), nil
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
