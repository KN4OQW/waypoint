// Package sysprov implements privhelper.Provisioner against a real Debian system.
//
// It is the only place in Waypoint that runs useradd, passwd, hostnamectl, or
// nmcli. Everything it does is idempotent and re-runnable: a wizard step that
// times out on the operator's phone and gets retried must converge, not fail on
// "user already exists" or append a second SSH key line. Every method therefore
// reads the current state first and reports Changed honestly.
//
// Two rules shape the implementation.
//
// Nothing secret goes in argv. /proc/<pid>/cmdline is readable by other local
// users, so a password reaches chpasswd on stdin and a Wi-Fi PSK reaches
// NetworkManager through a 0600 keyfile — never as a command-line argument.
//
// Nothing is edited in place. Files this package owns are written whole through a
// temp file and a rename, with a header naming Waypoint as the writer, so a power
// cut during setup leaves the previous file rather than half of a new one, and an
// operator reading /etc can tell what put it there.
package sysprov

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/KN4OQW/waypoint/internal/netconfig"
	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// Runner executes a command and returns its standard output.
//
// stdin, when non-nil, is written to the command and the pipe is closed. It is
// the reason this is not netconfig.Runner: handing chpasswd a password on stdin
// rather than in argv is the difference between a secret and a secret every local
// user can read out of /proc.
type Runner func(ctx context.Context, stdin []byte, name string, args ...string) (stdout string, err error)

// System is the real Provisioner.
//
// A single mutex serialises every method. These operations mutate shared system
// state — /etc/passwd, the hostname, NetworkManager's profile set — and two
// concurrent wizard submissions interleaving inside useradd is not a race worth
// having a subtle answer for.
type System struct {
	// Run executes commands. Nil means the real one.
	Run Runner
	// Root prefixes every file path this package writes. Empty means /. Tests
	// point it at a temp directory so file-writing behaviour is exercised without
	// a container; on a node it is always empty.
	Root string
	// Logf receives progress lines. Nil discards them.
	Logf func(format string, args ...any)

	mu sync.Mutex
	// protected is an account RemoveUser refuses regardless of the system's
	// opinion — the recovery account the caller is in the middle of creating.
	protected string

	// checkpoint is created lazily so a System that never touches the network
	// never touches NetworkManager's directory either.
	cpOnce sync.Once
	cp     netconfig.Checkpoint
	// handles maps the handles handed to callers onto the backend's own, so a
	// caller can never pass an arbitrary string through to the backend.
	handles map[string]string
	nextCP  int
}

var _ privhelper.Provisioner = (*System)(nil)

// New returns a System that acts on the running machine.
func New() *System {
	return &System{Run: ExecRunner, handles: map[string]string{}}
}

// ExecRunner runs a command for real. It captures stderr separately and folds it
// into the error, because "useradd exited 1" without useradd's own message is a
// support ticket rather than a diagnosis.
func ExecRunner(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	// A helper inherits the daemon's environment; commands run with a predictable
	// locale so parsed output is not translated out from under us.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")

	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errBuf.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if msg != "" {
			return out.String(), fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return out.String(), fmt.Errorf("%s: %w", name, err)
	}
	return out.String(), nil
}

func (s *System) run(ctx context.Context, stdin []byte, name string, args ...string) (string, error) {
	r := s.Run
	if r == nil {
		r = ExecRunner
	}
	return r(ctx, stdin, name, args...)
}

func (s *System) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// path resolves a system path under Root.
func (s *System) path(p string) string {
	if s.Root == "" {
		return p
	}
	return filepath.Join(s.Root, p)
}

// enter is the preamble every method shares: honour the context, re-validate the
// request, take the lock. The re-validation is not redundant with the server's —
// a System can also be called in-process, and a validator that only runs on one
// path is a validator that will eventually be bypassed.
func (s *System) enter(ctx context.Context, req privhelper.Validator) error {
	if err := ctx.Err(); err != nil {
		return privhelper.Errorf(privhelper.CodeInternal, "%v", err)
	}
	if err := req.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	return nil
}

// managedHeader is written at the top of every file this package owns, so an
// operator finding it in /etc knows what wrote it and that hand edits are lost.
func managedHeader(what string) string {
	return "# Generated by waypoint-provision-helper (" + what + ") — do NOT edit.\n" +
		"# Changes are overwritten the next time provisioning runs.\n"
}

// writeFile writes data to path atomically, creating parent directories.
func (s *System) writeFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".waypoint-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once the rename consumes it
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		// A rename cannot replace a bind mount or cross a filesystem boundary, and
		// both happen to the files this package writes: a container bind-mounts
		// /etc/hostname and /etc/hosts, and an operator can bind-mount either on a
		// real node too. Falling back to a truncating write trades atomicity for
		// working at all — acceptable here because every such file is a single
		// short line, so the window in which it is empty is one write syscall.
		if errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EXDEV) {
			s.logf("sysprov: %s cannot be replaced by rename (%v); writing in place", path, err)
			return writeInPlace(path, data, mode)
		}
		return err
	}
	return nil
}

// writeInPlace overwrites a file without replacing the inode.
func writeInPlace(path string, data []byte, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// writeFileIfChanged writes only when the content differs, so a re-run reports
// Changed false and does not churn the SD card.
func (s *System) writeFileIfChanged(path string, data []byte, mode os.FileMode) (changed bool, err error) {
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, data) {
		return false, nil
	}
	return true, s.writeFile(path, data, mode)
}

// passwdEntry is one line of /etc/passwd, as getent reports it.
type passwdEntry struct {
	Name  string
	UID   int
	GID   int
	Home  string
	Shell string
}

// lookupUser returns the account named, if it exists. It shells to getent rather
// than reading /etc/passwd so accounts from LDAP or systemd-userdb are seen too —
// "the name is not in /etc/passwd" is not the same question as "the name is free".
func (s *System) lookupUser(ctx context.Context, name string) (passwdEntry, bool, error) {
	out, err := s.run(ctx, nil, "getent", "passwd", name)
	if err != nil {
		// getent exits 2 when the key is simply absent, which is not a failure.
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return passwdEntry{}, false, nil
		}
		if strings.TrimSpace(out) == "" {
			return passwdEntry{}, false, nil
		}
		return passwdEntry{}, false, err
	}
	line := strings.TrimSpace(out)
	if line == "" {
		return passwdEntry{}, false, nil
	}
	f := strings.Split(line, ":")
	if len(f) < 7 {
		return passwdEntry{}, false, fmt.Errorf("sysprov: unparseable passwd entry %q", line)
	}
	uid, err := strconv.Atoi(f[2])
	if err != nil {
		return passwdEntry{}, false, fmt.Errorf("sysprov: unparseable uid in %q", line)
	}
	gid, _ := strconv.Atoi(f[3])
	return passwdEntry{Name: f[0], UID: uid, GID: gid, Home: f[5], Shell: f[6]}, true, nil
}

// hasSystemd reports whether this is a running systemd system. The check is the
// canonical one (sd_booted): a container built from a Debian image has systemctl
// on PATH but nothing for it to talk to, and calling it there fails in a way that
// looks like a real problem.
func (s *System) hasSystemd() bool {
	fi, err := os.Stat(s.path("/run/systemd/system"))
	return err == nil && fi.IsDir()
}

// internalf wraps a failed system command as a coded error.
func internalf(format string, args ...any) error {
	return privhelper.Errorf(privhelper.CodeInternal, format, args...)
}

// trimLine returns the first line of out, trimmed.
func trimLine(out string) string {
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		out = out[:i]
	}
	return strings.TrimSpace(out)
}

// containsDirective reports whether a config file body contains a directive,
// ignoring comment lines and leading whitespace.
func containsDirective(body, want string) bool {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.EqualFold(line, want) {
			return true
		}
	}
	return false
}
