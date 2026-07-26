package wizard

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
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
	if w.OnComplete != nil {
		w.OnComplete(ctx)
	}
	return w.state(), nil
}

// JoinRequest puts the node on the operator's Wi-Fi network.
type JoinRequest struct {
	SSID string `json:"ssid"`
	// PSK is empty for an open network.
	PSK string `json:"psk,omitempty"`
	// Hidden drives a scan-ssid join for a network that does not beacon.
	Hidden bool `json:"hidden,omitempty"`
	// Country is the regulatory domain, two letters.
	Country string `json:"country,omitempty"`
	// TimeoutSeconds bounds the wait for association; zero means the helper's
	// default.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// JoinResult reports the outcome alongside the wizard's state.
type JoinResult struct {
	View      View   `json:"state"`
	SSID      string `json:"ssid"`
	Connected bool   `json:"connected"`
	IPv4      string `json:"ipv4,omitempty"`
	Interface string `json:"interface,omitempty"`
}

// JoinNetwork puts the node on the operator's network under a confirm-or-revert
// checkpoint.
//
// It is not an ordered step. A node on Ethernet never needs it, and one being set
// up over the setup access point needs it at whatever point the operator has their
// Wi-Fi password to hand — making it step N would mean refusing the operator who
// wants to do it first and the one who wants to do it last.
//
// The sequence is the whole design, and every stage of it is there because of a
// specific way this goes wrong:
//
//  1. Checkpoint the current network state. The operator is changing the network
//     they are connected over; without this, one wrong passphrase ends the session
//     that would have fixed it.
//  2. Lower the access point. A single-radio Pi cannot be an AP and a station at
//     once, so the radio has to be given up before the join can even be attempted.
//     This lowering is reversible — it does not spend the AP.
//  3. Join, and wait.
//  4. Verify. Association is not success: a node associated to the right SSID with
//     no DHCP lease is on the network and unreachable, which looks like a working
//     join right up until the operator tries to find it.
//  5. On success, destroy the checkpoint and spend the AP. On any failure, roll the
//     checkpoint back and raise the AP again, so the operator has somewhere to
//     retry from rather than a node that has vanished.
func (w *Wizard) JoinNetwork(ctx context.Context, req JoinRequest) (JoinResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	cp, err := w.Prov.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{
		TimeoutSeconds: int(joinCheckpointWindow.Seconds()),
	})
	if err != nil {
		return JoinResult{View: w.state()}, err
	}

	// The AP goes down before the join, and comes back on every failure path
	// below. Doing this after the checkpoint means a failure here is still
	// covered by it.
	if w.AP != nil {
		if err := w.AP.DownForJoin(ctx); err != nil {
			w.rollback(ctx, cp.Handle)
			return JoinResult{View: w.state()}, err
		}
	}

	got, joinErr := w.Prov.NetJoin(ctx, privhelper.NetJoinRequest{
		SSID:           req.SSID,
		PSK:            req.PSK,
		Hidden:         req.Hidden,
		Country:        req.Country,
		TimeoutSeconds: req.TimeoutSeconds,
	})
	if failure := verifyJoin(req.SSID, got, joinErr); failure != nil {
		w.rollback(ctx, cp.Handle)
		w.reraise(ctx)
		return JoinResult{View: w.state(), SSID: req.SSID}, failure
	}

	if _, err := w.Prov.NetCheckpointDestroy(ctx,
		privhelper.NetCheckpointDestroyRequest{Handle: cp.Handle}); err != nil {
		// The join is verified; a checkpoint left behind is untidy, not dangerous.
		w.logf("wizard: could not destroy checkpoint %s after a verified join: %v", cp.Handle, err)
	}
	w.logf("wizard: joined %q on %s (%s)", got.SSID, got.Interface, got.IPv4)

	// The node is on the operator's network, so the setup access point has done
	// its job. This is the point of no return: it does not come back without a
	// reboot.
	if w.AP != nil {
		if err := w.AP.Commit(ctx); err != nil {
			w.logf("wizard: could not take the setup access point down after the join: %v", err)
		}
	}
	return JoinResult{
		View: w.state(), SSID: got.SSID, Connected: true,
		IPv4: got.IPv4, Interface: got.Interface,
	}, nil
}

// verifyJoin decides whether a join actually worked, returning nil when it did.
//
// The address check is the one that matters. nmcli reports success once the
// profile is up, and a node associated to the right SSID with no DHCP lease
// satisfies that while being completely unreachable — the failure an operator
// discovers ten minutes later, looking for a node that is on their network and
// answering nothing.
func verifyJoin(ssid string, got privhelper.NetJoinResponse, joinErr error) error {
	if joinErr != nil {
		return joinErr
	}
	if !got.Connected {
		return privhelper.Errorf(privhelper.CodeConflict,
			"could not associate with %q; the previous network settings were restored", ssid)
	}
	if strings.TrimSpace(got.IPv4) == "" {
		return privhelper.Errorf(privhelper.CodeConflict,
			"associated with %q but no address was assigned before the timeout, so the node would have been unreachable; the previous network settings were restored", ssid)
	}
	return nil
}

// rollback restores the pre-join network state.
func (w *Wizard) rollback(ctx context.Context, handle string) {
	if _, err := w.Prov.NetCheckpointRollback(ctx,
		privhelper.NetCheckpointRollbackRequest{Handle: handle}); err != nil {
		w.logf("wizard: could not roll back checkpoint %s after a failed join: %v", handle, err)
	}
}

// reraise brings the setup access point back so the operator has somewhere to
// retry from.
func (w *Wizard) reraise(ctx context.Context) {
	if w.AP == nil {
		return
	}
	if err := w.AP.Reraise(ctx, captive.ReasonHandingOver); err != nil {
		// Worth shouting about: the join failed and the way back in did not come
		// back, which is the one combination that strands the operator.
		w.logf("wizard: THE JOIN FAILED AND THE SETUP ACCESS POINT DID NOT COME BACK: %v", err)
	}
}

// joinCheckpointWindow is how long the pre-join network state is held. It only
// has to outlast the association attempt plus the operator noticing it failed.
const joinCheckpointWindow = 3 * time.Minute

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
