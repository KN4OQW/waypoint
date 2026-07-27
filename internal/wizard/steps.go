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
	// Remint the certificate now rather than at the next restart. The operator is
	// about to be told to browse to this name, and a certificate still naming the
	// old one would greet them with a mismatch warning on the address the node
	// itself just gave them.
	if w.OnHostnameSet != nil {
		w.OnHostnameSet(ctx, got.Hostname)
	}
	return w.state(), nil
}

// UserRequest creates the recovery account.
type UserRequest struct {
	Username string `json:"username"`
	// Password is optional. Leaving it empty produces a key-only account, which is
	// the recommended shape and requires SSHKey to be set.
	Password string `json:"password,omitempty"`
	// PasswordHash is a pre-computed crypt(3) string, used instead of Password by
	// callers that must write the credential down somewhere — the boot-partition
	// seed file (internal/seed) is the reason it exists.
	PasswordHash string `json:"password_hash,omitempty"`
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
		Username:     req.Username,
		Password:     req.Password,
		PasswordHash: req.PasswordHash,
		SSHKey:       req.SSHKey,
		Sudo:         true,
	})
	if err != nil {
		return w.state(), err
	}

	p.RecoveryUser = got.Username
	p.UserHasPassword = !got.PasswordLocked
	p.UserSudo = got.Sudo
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

// PriorAdmin is an administrator account that existed before this setup ran.
type PriorAdmin struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	// Summary is the credential state in a line the operator can act on.
	Summary string `json:"summary"`
	// Reachable reports whether somebody could actually log in as it. An account
	// with a key or a usable password is a live way into this node; one with
	// neither is a name in a file, and the difference decides how alarmed the
	// operator should be.
	Reachable bool `json:"reachable"`
	SSHKeys   int  `json:"ssh_keys"`
}

// PriorAdmins lists administrator accounts other than the one this setup created.
//
// It exists for second-hand hardware. A board bought at a hamfest, inherited from
// a silent key, or reflashed onto a card nobody wiped can carry a previous
// owner's account — often with an SSH key still in it, which is standing
// passwordless access to a node the new operator believes is theirs. Nothing else
// in setup would ever mention it.
//
// The account this wizard just created is filtered out here rather than left for
// the UI to exclude. A screen offering the operator a checkbox to delete the
// recovery account they are in the middle of creating would be a bug with a
// bricked node at the end of it, and "the UI does not render that one" is not
// where that guarantee belongs.
func (w *Wizard) PriorAdmins(ctx context.Context) ([]PriorAdmin, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.priorAdmins(ctx)
}

func (w *Wizard) priorAdmins(ctx context.Context) ([]PriorAdmin, error) {
	got, err := w.Prov.ListSudoUsers(ctx, privhelper.ListSudoUsersRequest{})
	if err != nil {
		return nil, err
	}
	ours := w.Load().RecoveryUser

	var out []PriorAdmin
	for _, u := range got.Users {
		if u.Username == ours {
			continue
		}
		out = append(out, PriorAdmin{
			Username:  u.Username,
			UID:       u.UID,
			Summary:   u.Summary(),
			Reachable: u.Reachable(),
			SSHKeys:   u.SSHKeys,
		})
	}
	return out, nil
}

// RemovePriorAdminsRequest names accounts the operator ticked for removal.
type RemovePriorAdminsRequest struct {
	// Usernames are the accounts to remove. The UI defaults every box to
	// unchecked, so an empty list is the normal submission.
	Usernames []string `json:"usernames"`
}

// RemovalOutcome is what happened to one account.
type RemovalOutcome struct {
	Username string `json:"username"`
	Removed  bool   `json:"removed"`
	// Error is the reason it was not removed, phrased for the operator.
	Error string `json:"error,omitempty"`
}

// RemovePriorAdmins removes the accounts the operator ticked.
//
// Failures are per-account and never block setup. An account with a live session,
// or one userdel refuses for a reason nobody anticipated, must not leave the
// operator stuck on a screen between "I have an account" and "root is locked" —
// the two states this step sits between, and the worst place to be stranded. They
// are reported and setup continues.
func (w *Wizard) RemovePriorAdmins(ctx context.Context, req RemovePriorAdminsRequest) ([]RemovalOutcome, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := w.Load()
	if p.RecoveryUser == "" {
		return nil, privhelper.Errorf(privhelper.CodeConflict,
			"create this node's recovery account before removing anyone else's")
	}

	// Whatever the request says, the account this setup created is never removed.
	// The helper refuses it too; this is the same guarantee stated where the list
	// is built, so neither end depends on the other having remembered.
	allowed, err := w.priorAdmins(ctx)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, a := range allowed {
		known[a.Username] = true
	}

	var out []RemovalOutcome
	for _, name := range req.Usernames {
		if name == p.RecoveryUser {
			out = append(out, RemovalOutcome{Username: name,
				Error: "this is the recovery account setup just created; it was not removed"})
			continue
		}
		if !known[name] {
			out = append(out, RemovalOutcome{Username: name,
				Error: "not an administrator account on this node"})
			continue
		}
		got, err := w.Prov.RemoveUser(ctx, privhelper.RemoveUserRequest{
			Username: name, RemoveHome: true})
		if err != nil {
			w.logf("wizard: could not remove prior administrator %q: %v", name, err)
			out = append(out, RemovalOutcome{Username: name, Error: humanise(err)})
			continue
		}
		w.logf("wizard: removed prior administrator %q", name)
		out = append(out, RemovalOutcome{Username: name, Removed: got.Removed})
	}
	return out, nil
}

// humanise strips a helper error's wire prefix for display.
func humanise(err error) string {
	msg := strings.TrimPrefix(err.Error(), "privhelper: ")
	if i := strings.Index(msg, ": "); i > 0 && strings.HasPrefix(msg, string(privhelper.CodeOf(err))) {
		msg = msg[i+2:]
	}
	return msg
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

	// The recovery account is verified before anything is locked. This is the
	// first of three layers, and it is the one that knows *which* account setup
	// created:
	//
	//  1. here — the account this wizard made exists, can become root, and has a
	//     credential;
	//  2. the helper's LockRoot — some non-root member of the sudo group can
	//     actually log in, checked against the live system rather than this
	//     record;
	//  3. the helper's sudoers drop-in — a key-only account can use sudo at all,
	//     since it has no password for sudo to authenticate against.
	//
	// Any one of them alone leaves a hole. This layer catches "the wizard's own
	// account is not what it thinks it is"; layer 2 catches an account changed out
	// from under the wizard; layer 3 catches the account that can log in and then
	// do nothing.
	if err := verifyRecoveryUser(p); err != nil {
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

// verifyRecoveryUser checks that the account setup created is one that can
// actually recover this node once root is locked.
//
// Every condition here is a way an operator ends up locked out of hardware they
// own, and none of them is visible until they need it — which is why this runs
// before the lock rather than being discovered afterwards.
func verifyRecoveryUser(p Progress) error {
	if p.RecoveryUser == "" {
		return privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: no recovery account was created, so nothing could administer this node afterwards")
	}
	if !p.UserSudo {
		return privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: %q cannot become root, so it could not administer a node with root locked", p.RecoveryUser)
	}
	if !p.UserHasPassword && p.KeyFingerprint == "" {
		return privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: %q has neither a password nor an SSH key, so nobody could log in as it", p.RecoveryUser)
	}
	return nil
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

// VisibleNetworks lists the wireless networks the node can see.
//
// It never rescans. The setup access point is running on the node's only radio,
// and a sweep would drop the network the operator is reading the list over — to
// populate a form they are looking at right now. NetworkManager's cache, built
// before the access point went up, is the list of networks this node can
// actually reach from where it is sitting.
//
// An empty list is not an error. A node whose radio has been in AP mode since
// boot may have nothing cached, and the wizard still has to let the operator
// type a name — including for a network that does not broadcast one.
func (w *Wizard) VisibleNetworks(ctx context.Context) (privhelper.NetScanResponse, error) {
	return w.Prov.NetScan(ctx, privhelper.NetScanRequest{})
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

// ClearProgress deletes an in-flight setup progress file. It is used by the reset
// paths so a full re-provision starts at the first step rather than resuming into
// the middle of a setup that describes an account and a hostname the reset just
// undid.
func ClearProgress(path string) error {
	if path == "" {
		path = DefaultProgressPath
	}
	return removeIfExists(path)
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
