package sysprov

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// ProtectedUser is an account RemoveUser must refuse regardless of anything the
// system says. The wizard sets it to the recovery account it just created, so a
// caller cannot delete the way back in it was in the middle of building.
//
// It is a field rather than a request parameter on purpose: a request parameter
// is something the caller supplies, and the whole point is that the caller must
// not be able to leave it out.
func (s *System) SetProtectedUser(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.protected = name
}

// ListSudoUsers reports the human accounts that can already become root.
//
// This exists for second-hand hardware. A board bought at a hamfest, inherited
// from a silent key, or reflashed onto a card somebody forgot to wipe can carry a
// previous owner's administrator account — often with an SSH key still in it,
// which is standing passwordless access to a node the new operator believes is
// theirs. Nothing else in setup would ever mention it.
func (s *System) ListSudoUsers(ctx context.Context, req privhelper.ListSudoUsersRequest) (privhelper.ListSudoUsersResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.ListSudoUsersResponse{}, err
	}
	defer s.mu.Unlock()

	members, err := s.sudoGroupMembers(ctx)
	if err != nil {
		return privhelper.ListSudoUsersResponse{}, err
	}
	covered := s.sudoersCovered()

	var out []privhelper.SudoUser
	for _, name := range members {
		u, found, err := s.lookupUser(ctx, name)
		if err != nil || !found {
			continue
		}
		// System accounts are filtered out, not listed and marked. An operator
		// scanning this screen should see only accounts a person logs in as; the
		// sudo group's own machinery appearing next to a checkbox would be an
		// invitation to break the node.
		if u.UID < privhelper.MinHumanUID {
			continue
		}
		locked := s.passwordLocked(ctx, name)
		out = append(out, privhelper.SudoUser{
			Username:       u.Name,
			UID:            u.UID,
			Home:           u.Home,
			Shell:          u.Shell,
			HasPassword:    s.hasPassword(ctx, name) || locked,
			PasswordLocked: locked,
			SSHKeys:        s.countAuthorizedKeys(u),
			NOPASSWDSudo:   covered[u.Name],
			LoginShell:     !nologinShells[u.Shell],
		})
	}
	return privhelper.ListSudoUsersResponse{Users: out}, nil
}

// RemoveUser deletes an account.
//
// Every refusal below is a way an operator could destroy the node they are in the
// middle of setting up, and none of them is recoverable from the wizard:
//
//   - uid 0 under any name. The username validator already excludes the string
//     "root"; this catches the account somebody created as "admin" with uid 0.
//   - the account this setup just created. Deleting it after root has been locked
//     leaves nothing that can administer the node.
//   - an account with running processes. userdel would either refuse or, with -f,
//     delete the account out from under a live session and leave orphaned files
//     owned by a uid that will be reallocated to somebody else. There is no -f
//     here at all.
func (s *System) RemoveUser(ctx context.Context, req privhelper.RemoveUserRequest) (privhelper.RemoveUserResponse, error) {
	if err := s.enter(ctx, req); err != nil {
		return privhelper.RemoveUserResponse{}, err
	}
	defer s.mu.Unlock()

	if s.protected != "" && req.Username == s.protected {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"refusing to remove %q: it is the recovery account this setup created, and root is locked behind it", req.Username)
	}

	u, found, err := s.lookupUser(ctx, req.Username)
	if err != nil {
		return privhelper.RemoveUserResponse{}, internalf("look up %q: %v", req.Username, err)
	}
	if !found {
		// Absent is success. The caller wanted the account gone; it is gone.
		return privhelper.RemoveUserResponse{Username: req.Username, Removed: false}, nil
	}
	if u.UID == 0 {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeInvalidArgument,
			"%q is uid 0 — refusing to remove root under another name", req.Username)
	}
	if u.UID < privhelper.MinHumanUID {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeInvalidArgument,
			"%q is a system account (uid %d); removing it would break whatever runs as it", req.Username, u.UID)
	}
	if busy, detail := s.userIsBusy(ctx, req.Username); busy {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"%q has running processes (%s); log that session out and try again", req.Username, detail)
	}

	args := []string{req.Username}
	if req.RemoveHome {
		args = append([]string{"--remove"}, args...)
	}
	if _, err := s.run(ctx, nil, "userdel", args...); err != nil {
		return privhelper.RemoveUserResponse{}, internalf("remove %q: %v", req.Username, err)
	}
	s.logf("sysprov: removed account %q (uid %d, home removed: %v)", req.Username, u.UID, req.RemoveHome)

	resp := privhelper.RemoveUserResponse{
		Username: req.Username, Removed: true, HomeRemoved: req.RemoveHome,
	}
	// The sudoers drop-in is pruned last. A name left in it would grant
	// passwordless root to whoever the name is next allocated to — the same uid
	// reuse problem, one layer up.
	pruned, err := s.pruneSudoers(ctx, req.Username)
	if err != nil {
		return resp, err
	}
	resp.SudoersPruned = pruned
	return resp, nil
}

// sudoGroupMembers returns the sudo group's member list.
func (s *System) sudoGroupMembers(ctx context.Context) ([]string, error) {
	out, err := s.run(ctx, nil, "getent", "group", sudoGroup)
	if err != nil {
		// No sudo group is not a failure — it is a node with no administrators
		// beyond root, which is exactly what a fresh image looks like.
		return nil, nil
	}
	f := strings.Split(strings.TrimSpace(out), ":")
	if len(f) < 4 {
		return nil, nil
	}
	var members []string
	for _, name := range strings.Split(f[3], ",") {
		if name = strings.TrimSpace(name); name != "" && name != "root" {
			members = append(members, name)
		}
	}
	return members, nil
}

// countAuthorizedKeys counts an account's installed keys.
//
// It is the most important number on the enumeration screen: a key on an account
// the current operator does not recognise is standing, passwordless remote access
// to a node they think is theirs.
func (s *System) countAuthorizedKeys(u passwdEntry) int {
	b, err := os.ReadFile(filepath.Join(s.path(u.Home), ".ssh", "authorized_keys"))
	if err != nil {
		return 0
	}
	return countKeys(strings.Split(string(b), "\n"))
}

// userIsBusy reports whether an account has running processes.
//
// pgrep rather than parsing ps: it is in procps on every Debian, it takes the
// account by name, and it exits 1 for "no matches" rather than printing something
// that has to be told apart from an error.
func (s *System) userIsBusy(ctx context.Context, name string) (bool, string) {
	out, err := s.run(ctx, nil, "pgrep", "-u", name)
	if err != nil {
		return false, "" // exit 1 means no processes
	}
	pids := strings.Fields(out)
	if len(pids) == 0 {
		return false, ""
	}
	if len(pids) == 1 {
		return true, "pid " + pids[0]
	}
	return true, strconv.Itoa(len(pids)) + " processes"
}

// sudoersCovered reports which accounts Waypoint's own drop-in grants
// passwordless sudo, so the enumeration can tell an account this node created
// from one it inherited.
func (s *System) sudoersCovered() map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(s.path(sudoersDropIn))
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if name, _, ok := strings.Cut(line, " "); ok {
			out[name] = true
		}
	}
	return out
}

// pruneSudoers removes an account from Waypoint's sudoers drop-in and re-validates.
//
// Validation is differential, like the sshd and sudoers checks elsewhere: visudo
// can fail for reasons that predate this edit, and a node whose sudo is broken by
// a cleanup step is a node nobody can administer at all — the exact outcome the
// recovery account exists to prevent.
func (s *System) pruneSudoers(ctx context.Context, name string) (bool, error) {
	path := s.path(sudoersDropIn)
	before, err := os.ReadFile(path)
	if err != nil {
		return false, nil // no drop-in, nothing to prune
	}

	var kept []string
	pruned := false
	for _, line := range strings.Split(string(before), "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			if who, _, ok := strings.Cut(trimmed, " "); ok && who == name {
				pruned = true
				continue
			}
		}
		kept = append(kept, line)
	}
	if !pruned {
		return false, nil
	}

	body := strings.Join(kept, "\n")
	if !hasSudoersRule(body) {
		// Nothing left but the header. An empty rule file is harmless but
		// pointless, and leaving one behind makes the next reader wonder what it is
		// for.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return false, internalf("remove %s: %v", sudoersDropIn, err)
		}
		s.logf("sysprov: removed %s — it granted sudo only to %q", sudoersDropIn, name)
		return true, nil
	}
	if _, err := s.writeFileIfChanged(path, []byte(body), 0o440); err != nil {
		return false, internalf("write %s: %v", sudoersDropIn, err)
	}
	if err := s.validateSudoers(ctx, path); err != nil {
		return false, err
	}
	s.logf("sysprov: pruned %q from %s", name, sudoersDropIn)
	return true, nil
}

// hasSudoersRule reports whether a body contains anything but comments.
func hasSudoersRule(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			return true
		}
	}
	return false
}
