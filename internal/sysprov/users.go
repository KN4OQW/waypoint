package sysprov

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

const (
	// sudoGroup is Debian's administrator group. Membership in it is what makes
	// the recovery account able to recover anything.
	sudoGroup = "sudo"
	// sudoersDropIn grants the recovery account passwordless sudo when — and only
	// when — it has no password to type. See ensureRecoverySudoers.
	sudoersDropIn = "/etc/sudoers.d/010-waypoint-recovery"

	// defaultShell is the recovery account's shell. It must be a real one: an
	// account with /usr/sbin/nologin cannot be SSH'd into, which is the entire
	// purpose of this account.
	defaultShell = "/bin/bash"
)

// nologinShells are the shells that mean "this account may not log in". An
// existing account carrying one is repaired with chsh rather than left as a
// recovery account nobody can use.
var nologinShells = map[string]bool{
	"/usr/sbin/nologin": true,
	"/sbin/nologin":     true,
	"/bin/false":        true,
	"/usr/bin/false":    true,
	"":                  true,
}

// CreateRecoveryUser creates the account that keeps the node reachable after root
// is locked.
//
// It converges rather than failing: an account that already exists is repaired —
// its shell made loginable, its sudo membership ensured, its key installed — and
// reported with Created false. A retried wizard step must not be an error, and it
// must not produce a second account.
func (s *System) CreateRecoveryUser(ctx context.Context, req privhelper.CreateRecoveryUserRequest) (privhelper.CreateRecoveryUserResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.CreateRecoveryUserResponse{}, err
	}
	defer s.mu.Unlock()

	existing, found, err := s.lookupUser(ctx, req.Username)
	if err != nil {
		return privhelper.CreateRecoveryUserResponse{}, internalf("look up %q: %v", req.Username, err)
	}
	// The name check in privhelper cannot see uid 0 under another name. This can,
	// and it is the check that actually matters: an account named anything at all
	// with uid 0 is root.
	if found && existing.UID == 0 {
		return privhelper.CreateRecoveryUserResponse{}, privhelper.Errorf(privhelper.CodeInvalidArgument,
			"%q is uid 0 — the recovery account must not be root under another name", req.Username)
	}

	resp := privhelper.CreateRecoveryUserResponse{Username: req.Username}
	if !found {
		args := []string{"--create-home", "--shell", defaultShell,
			"--comment", "Waypoint recovery account", req.Username}
		if _, err := s.run(ctx, nil, "useradd", args...); err != nil {
			return resp, internalf("create user %q: %v", req.Username, err)
		}
		resp.Created = true
		s.logf("sysprov: created recovery account %q", req.Username)

		existing, found, err = s.lookupUser(ctx, req.Username)
		if err != nil || !found {
			return resp, internalf("user %q was created but cannot be looked up: %v", req.Username, err)
		}
	} else if nologinShells[existing.Shell] {
		// An account that exists but cannot log in is not a recovery account.
		if _, err := s.run(ctx, nil, "chsh", "--shell", defaultShell, req.Username); err != nil {
			return resp, internalf("set shell for %q: %v", req.Username, err)
		}
		s.logf("sysprov: gave %q a login shell (was %q)", req.Username, existing.Shell)
		existing.Shell = defaultShell
	}
	resp.UID, resp.Home = existing.UID, existing.Home

	if req.Sudo {
		// usermod -aG is additive, so re-running it is a no-op rather than a
		// membership reset — the -a is load-bearing.
		if _, err := s.run(ctx, nil, "usermod", "--append", "--groups", sudoGroup, req.Username); err != nil {
			return resp, internalf("add %q to %s: %v", req.Username, sudoGroup, err)
		}
		resp.Sudo = true
	}

	switch {
	case req.PasswordHash != "":
		// -e takes the value as an already-hashed crypt string, so a provisioning
		// file can carry a hash instead of the operator's actual password. Still
		// stdin: the hash is what an offline cracker would work on, and argv is
		// world-readable through /proc.
		line := []byte(req.Username + ":" + req.PasswordHash + "\n")
		if _, err := s.run(ctx, line, "chpasswd", "--encrypted"); err != nil {
			return resp, internalf("set the password hash for %q: %v", req.Username, err)
		}
	case req.Password != "":
		// stdin, not argv: a password on a command line is readable out of
		// /proc/<pid>/cmdline by any local user for as long as the process lives.
		line := []byte(req.Username + ":" + req.Password + "\n")
		if _, err := s.run(ctx, line, "chpasswd"); err != nil {
			return resp, internalf("set the password for %q: %v", req.Username, err)
		}
	default:
		// Key-only: the password is locked, so the account is reachable only with
		// the key the operator already holds.
		if _, err := s.run(ctx, nil, "passwd", "--lock", req.Username); err != nil {
			return resp, internalf("lock the password for %q: %v", req.Username, err)
		}
	}
	resp.PasswordLocked = s.passwordLocked(ctx, req.Username)

	if req.Sudo {
		if err := s.ensureRecoverySudoers(ctx, req.Username, resp.PasswordLocked); err != nil {
			return resp, err
		}
	}

	if req.SSHKey != "" {
		key, err := s.installKey(existing, req.SSHKey, false)
		if err != nil {
			return resp, err
		}
		resp.SSHKeyFingerprint = key.Fingerprint
	}
	return resp, nil
}

// InstallSSHKey adds a public key to a user's authorized_keys.
func (s *System) InstallSSHKey(ctx context.Context, req privhelper.InstallSSHKeyRequest) (privhelper.InstallSSHKeyResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.InstallSSHKeyResponse{}, err
	}
	defer s.mu.Unlock()

	u, found, err := s.lookupUser(ctx, req.Username)
	if err != nil {
		return privhelper.InstallSSHKeyResponse{}, internalf("look up %q: %v", req.Username, err)
	}
	if !found {
		return privhelper.InstallSSHKeyResponse{},
			privhelper.Errorf(privhelper.CodeNotFound, "no such user %q", req.Username)
	}
	return s.installKey(u, req.PublicKey, req.Replace)
}

// installKey does the authorized_keys work for both callers. The mutex is already
// held.
//
// Keys are compared by fingerprint, not by line: the same key pasted twice with
// different comments is the same key, and appending it a second time would leave
// the operator staring at what looks like two authorized laptops.
func (s *System) installKey(u passwdEntry, pub string, replace bool) (privhelper.InstallSSHKeyResponse, error) {
	pub = strings.TrimSpace(pub)
	fp, err := privhelper.Fingerprint(pub)
	if err != nil {
		return privhelper.InstallSSHKeyResponse{}, err
	}
	resp := privhelper.InstallSSHKeyResponse{Username: u.Name, Fingerprint: fp}

	sshDir := filepath.Join(s.path(u.Home), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		return resp, internalf("create %s: %v", sshDir, err)
	}
	// sshd refuses to read a key file it does not trust the ownership of, so both
	// the directory and the file must belong to the account, not to root.
	if err := os.Chown(sshDir, u.UID, u.GID); err != nil && !os.IsPermission(err) {
		return resp, internalf("chown %s: %v", sshDir, err)
	}
	if err := os.Chmod(sshDir, 0o700); err != nil {
		return resp, internalf("chmod %s: %v", sshDir, err)
	}

	path := filepath.Join(sshDir, "authorized_keys")
	var kept []string
	present := false
	if !replace {
		b, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			return resp, internalf("read %s: %v", path, err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			// Existing lines are kept verbatim, including entries this package
			// would reject on the way in — an operator's own key carrying a from=
			// option is theirs, and rewriting the file is not licence to drop it.
			if line == pub {
				present = true
			} else if existing, err := privhelper.Fingerprint(line); err == nil && existing == fp {
				present = true
			}
			kept = append(kept, line)
		}
	}
	if !present {
		kept = append(kept, pub)
	}

	resp.Added = !present
	resp.KeyCount = countKeys(kept)
	return s.finishKeys(path, u, kept, resp)
}

// finishKeys writes the key file with the ownership and mode sshd insists on.
func (s *System) finishKeys(path string, u passwdEntry, lines []string, resp privhelper.InstallSSHKeyResponse) (privhelper.InstallSSHKeyResponse, error) {
	var body strings.Builder
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		body.WriteString(l)
		body.WriteString("\n")
	}
	if err := s.writeFile(path, []byte(body.String()), 0o600); err != nil {
		return resp, internalf("write %s: %v", path, err)
	}
	if err := os.Chown(path, u.UID, u.GID); err != nil && !os.IsPermission(err) {
		return resp, internalf("chown %s: %v", path, err)
	}
	return resp, nil
}

// countKeys counts non-blank, non-comment lines.
func countKeys(lines []string) int {
	n := 0
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" && !strings.HasPrefix(l, "#") {
			n++
		}
	}
	return n
}

// ensureRecoverySudoers gives a key-only recovery account passwordless sudo.
//
// This is not a convenience. On Debian, sudo authenticates the *invoking user*
// with their own password — and a key-only account's password is locked, so it
// has none to give. Without this drop-in the recovery account can SSH in and then
// do nothing: no sudo, no way to reach root on a node whose root is locked. That
// is the exact dead end the recovery account exists to prevent, and it is
// invisible until an operator actually needs it.
//
// An account with a password gets no drop-in and authenticates normally, and any
// drop-in from a previous key-only run is removed — so an operator who adds a
// password later does not silently keep passwordless sudo.
//
// The file is validated before it is trusted. A syntactically bad file in
// /etc/sudoers.d breaks sudo for *everyone*, which on a node with root locked
// means nobody can administer it at all — the worst possible outcome of a step
// meant to guarantee access.
func (s *System) ensureRecoverySudoers(ctx context.Context, user string, passwordLocked bool) error {
	path := s.path(sudoersDropIn)

	if !passwordLocked {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return internalf("remove %s: %v", sudoersDropIn, err)
		}
		return nil
	}

	body := managedHeader("recovery account sudo") +
		"\n# " + user + " has no password (it is reachable only by SSH key), so sudo has\n" +
		"# nothing to authenticate against. Without this it could log in and do nothing.\n" +
		user + " ALL=(ALL) NOPASSWD:ALL\n"

	// 0440: sudo refuses to read a drop-in that is group- or world-writable, and
	// ignores the whole directory if any file in it is.
	changed, err := s.writeFileIfChanged(path, []byte(body), 0o440)
	if err != nil {
		return internalf("write %s: %v", sudoersDropIn, err)
	}
	if !changed {
		return nil
	}
	if err := s.validateSudoers(ctx, path); err != nil {
		return err
	}
	s.logf("sysprov: %s may use sudo without a password (it has none to type)", user)
	return nil
}

// validateSudoers checks the assembled sudo configuration, removing the drop-in
// if it is what broke it.
//
// Differential, like the sshd check: visudo can fail for reasons that predate
// this file, and deleting a valid drop-in over an unrelated fault would leave the
// recovery account unable to sudo on a node with root locked.
func (s *System) validateSudoers(ctx context.Context, path string) error {
	if _, err := s.run(ctx, nil, "visudo", "-c"); err == nil {
		return nil
	}
	saved, readErr := os.ReadFile(path)
	if err := os.Remove(path); err != nil {
		return internalf("visudo rejected the configuration and %s could not be removed: %v", sudoersDropIn, err)
	}
	if _, err := s.run(ctx, nil, "visudo", "-c"); err != nil {
		if readErr == nil {
			if werr := s.writeFile(path, saved, 0o440); werr != nil {
				return internalf("restore %s: %v", sudoersDropIn, werr)
			}
		}
		s.logf("sysprov: visudo -c fails for reasons unrelated to %s (%v); the drop-in was kept", sudoersDropIn, err)
		return nil
	}
	return internalf("the sudo configuration %s produces is invalid, so it was removed", sudoersDropIn)
}

// LockRoot locks root's password and tells sshd to refuse root logins.
//
// It refuses while no usable recovery account exists. That check is the whole
// reason this is an RPC and not a shell command: the caller cannot talk the helper
// into making the node unadministrable, however the wizard is driven.
//
// Root's shell is deliberately left alone. Setting it to nologin is a common
// hardening step and it breaks `sudo -i`, which is exactly the path the recovery
// account uses to fix a broken node — the wrong trade for an appliance whose
// design goal is that an operator can always get back in.
func (s *System) LockRoot(ctx context.Context, req privhelper.LockRootRequest) (privhelper.LockRootResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.LockRootResponse{}, err
	}
	defer s.mu.Unlock()

	ok, err := s.hasUsableRecoveryAccount(ctx)
	if err != nil {
		return privhelper.LockRootResponse{}, err
	}
	if !ok {
		return privhelper.LockRootResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: no account in the %s group can log in, so nothing could administer this node afterwards", sudoGroup)
	}

	changed := false
	if !s.passwordLocked(ctx, "root") {
		if _, err := s.run(ctx, nil, "passwd", "--lock", "root"); err != nil {
			return privhelper.LockRootResponse{}, internalf("lock root: %v", err)
		}
		changed = true
		s.logf("sysprov: locked the root password")
	}

	// Locking the password is not enough on its own: a root login with an SSH key
	// bypasses it entirely, so sshd is told to refuse root outright.
	dropChanged, err := s.writeSSHDropIn(ctx, rootLoginDropIn, "root login", "PermitRootLogin no\n")
	if err != nil {
		return privhelper.LockRootResponse{}, err
	}
	return privhelper.LockRootResponse{Locked: true, Changed: changed || dropChanged}, nil
}

// passwordLocked reports whether an account's password is locked, from the status
// letter passwd -S prints: L locked, P usable, NP no password at all.
func (s *System) passwordLocked(ctx context.Context, name string) bool {
	out, err := s.run(ctx, nil, "passwd", "--status", name)
	if err != nil {
		return false
	}
	f := strings.Fields(out)
	if len(f) < 2 {
		return false
	}
	return strings.HasPrefix(f[1], "L")
}

// hasUsableRecoveryAccount reports whether some non-root member of the sudo group
// can actually log in — it has a password, or it has at least one authorized key.
//
// Checking membership alone would not do. An account in the sudo group with a
// locked password and no key is a name in a file, and locking root behind it
// produces a node with no way in at all.
func (s *System) hasUsableRecoveryAccount(ctx context.Context) (bool, error) {
	out, err := s.run(ctx, nil, "getent", "group", sudoGroup)
	if err != nil {
		// No sudo group at all means no recovery account, which is a refusal
		// rather than an error — the caller's next step is to create one.
		return false, nil
	}
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) < 4 {
		return false, nil
	}
	for _, name := range strings.Split(f[3], ",") {
		name = strings.TrimSpace(name)
		if name == "" || name == "root" {
			continue
		}
		u, found, err := s.lookupUser(ctx, name)
		if err != nil || !found || u.UID == 0 || nologinShells[u.Shell] {
			continue
		}
		if !s.passwordLocked(ctx, name) && s.hasPassword(ctx, name) {
			return true, nil
		}
		if s.hasAuthorizedKeys(u) {
			return true, nil
		}
	}
	return false, nil
}

// hasPassword reports whether an account has a password set at all (passwd -S
// prints NP when it does not).
func (s *System) hasPassword(ctx context.Context, name string) bool {
	out, err := s.run(ctx, nil, "passwd", "--status", name)
	if err != nil {
		return false
	}
	f := strings.Fields(out)
	return len(f) >= 2 && f[1] == "P"
}

// hasAuthorizedKeys reports whether an account has at least one key installed.
func (s *System) hasAuthorizedKeys(u passwdEntry) bool {
	b, err := os.ReadFile(filepath.Join(s.path(u.Home), ".ssh", "authorized_keys"))
	if err != nil {
		return false
	}
	return countKeys(strings.Split(string(b), "\n")) > 0
}
