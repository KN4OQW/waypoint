//go:build integration

// This file holds the integration test: sysprov driving the real useradd,
// passwd, chsh, and hostnamectl against a real Debian filesystem. It is behind a
// build tag because it is genuinely destructive — it creates an account and locks
// root — and behind two further interlocks because "destructive" and "running on
// the developer's laptop" must never coincide.
//
// Run it with testdata/integration/run.sh, which builds this as a test binary and
// executes it inside a throwaway Debian container.

package sysprov_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/sysprov"
)

const (
	testHostname = "hs-integration"
	testUser     = "rescue"
	// A structurally valid ed25519 key. It is a real key line as far as sshd is
	// concerned; nothing here ever authenticates with it.
	testKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBBaJfnHiJdOKTLOpUZ0PVMFVDlZJmb8nOA7C8vJx1Xk waypoint-integration"
)

// guard refuses to run outside a disposable container.
//
// The test locks the root account of whatever machine it runs on. The env var
// alone is not enough of a fence for that — a stray `go test -tags integration`
// with the variable exported would be a very bad afternoon — so a container
// marker is required as well, and overriding that is a separate, deliberate act.
func guard(t *testing.T) {
	t.Helper()
	if os.Getenv("WAYPOINT_SYSPROV_INTEGRATION") != "1" {
		t.Skip("set WAYPOINT_SYSPROV_INTEGRATION=1 to run the destructive integration test")
	}
	if os.Geteuid() != 0 {
		t.Skip("the integration test needs root")
	}
	if !inContainer() && os.Getenv("WAYPOINT_SYSPROV_ALLOW_HOST") != "1" {
		t.Fatal("refusing to run outside a container: this test creates an account and locks root. " +
			"Use testdata/integration/run.sh, or set WAYPOINT_SYSPROV_ALLOW_HOST=1 on a machine you are willing to lose.")
	}
}

func inContainer() bool {
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(marker); err == nil {
			return true
		}
	}
	return false
}

func sh(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestProvisionsARealSystem runs the setup sequence in order, then runs it again.
// The subtests are deliberately not independent: this is the wizard's actual
// order, and the second pass is the retry an operator's flaky phone connection
// produces.
func TestProvisionsARealSystem(t *testing.T) {
	guard(t)
	ctx := context.Background()
	s := sysprov.New()
	s.Logf = t.Logf

	t.Run("root is not locked before a recovery account exists", func(t *testing.T) {
		_, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
		if privhelper.CodeOf(err) != privhelper.CodeConflict {
			t.Fatalf("code = %q (err %v), want %q — a fresh system has no way back in",
				privhelper.CodeOf(err), err, privhelper.CodeConflict)
		}
		if status := sh(t, "passwd", "--status", "root"); strings.Fields(status)[1] == "L" {
			t.Fatal("root was locked despite the refusal")
		}
	})

	t.Run("hostname", func(t *testing.T) {
		got, err := s.SetHostname(ctx, privhelper.SetHostnameRequest{
			Hostname: testHostname, UpdateHosts: true})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Changed {
			t.Error("Changed = false on the first rename")
		}
		if name := readFile(t, "/etc/hostname"); strings.TrimSpace(name) != testHostname {
			t.Errorf("/etc/hostname = %q, want %q", strings.TrimSpace(name), testHostname)
		}
		hosts := readFile(t, "/etc/hosts")
		if !strings.Contains(hosts, testHostname) {
			t.Errorf("/etc/hosts does not name the host:\n%s", hosts)
		}
		if !strings.Contains(hosts, "127.0.0.1") {
			t.Errorf("/etc/hosts lost its localhost entry:\n%s", hosts)
		}
	})

	t.Run("recovery account", func(t *testing.T) {
		got, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
			Username: testUser, SSHKey: testKey, Sudo: true})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Created || got.UID == 0 {
			t.Fatalf("response = %+v", got)
		}

		// The account exists as far as the system is concerned, not just as far as
		// our return value is concerned.
		entry := sh(t, "getent", "passwd", testUser)
		if !strings.Contains(entry, "/home/"+testUser) {
			t.Errorf("passwd entry = %q", entry)
		}
		if shell := strings.TrimSpace(strings.Split(strings.TrimSpace(entry), ":")[6]); shell == "/usr/sbin/nologin" {
			t.Error("the recovery account cannot log in")
		}
		if status := strings.Fields(sh(t, "passwd", "--status", testUser)); status[1] != "L" {
			t.Errorf("password status = %q, want L for a key-only account", status[1])
		}
		if groups := sh(t, "id", "-nG", testUser); !strings.Contains(groups, "sudo") {
			t.Errorf("groups = %q, want sudo among them", strings.TrimSpace(groups))
		}

		ak := filepath.Join("/home", testUser, ".ssh", "authorized_keys")
		if body := readFile(t, ak); !strings.Contains(body, testKey) {
			t.Errorf("authorized_keys = %q", body)
		}
		fi, err := os.Stat(ak)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("authorized_keys mode = %o, want 600", fi.Mode().Perm())
		}
		// sshd refuses to read a key file it does not trust the ownership of.
		if owner := sh(t, "stat", "-c", "%U", ak); strings.TrimSpace(owner) != testUser {
			t.Errorf("authorized_keys is owned by %q, want %q", strings.TrimSpace(owner), testUser)
		}
	})

	t.Run("sshd drop-in", func(t *testing.T) {
		no := false
		if _, err := s.EnableSSH(ctx, privhelper.EnableSSHRequest{
			Enabled: true, PasswordAuth: &no}); err != nil {
			t.Fatal(err)
		}
		body := readFile(t, "/etc/ssh/sshd_config.d/60-waypoint-passwordauth.conf")
		if !strings.Contains(body, "PasswordAuthentication no") {
			t.Errorf("drop-in =\n%s", body)
		}
		// The real check: sshd itself accepts the assembled configuration. A
		// drop-in that parses in our tests but not in sshd would be discovered
		// after a reboot, with no other way in.
		sh(t, "sshd", "-t")
	})

	t.Run("root is locked", func(t *testing.T) {
		got, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Locked || !got.Changed {
			t.Fatalf("response = %+v", got)
		}
		if status := strings.Fields(sh(t, "passwd", "--status", "root")); status[1] != "L" {
			t.Errorf("root password status = %q, want L", status[1])
		}
		body := readFile(t, "/etc/ssh/sshd_config.d/61-waypoint-rootlogin.conf")
		if !strings.Contains(body, "PermitRootLogin no") {
			t.Errorf("drop-in =\n%s", body)
		}
		// Locking the password is not enough on its own — a key-based root login
		// would bypass it — so confirm sshd's effective setting, not just the file.
		eff := sh(t, "sshd", "-T")
		if !strings.Contains(strings.ToLower(eff), "permitrootlogin no") {
			t.Errorf("sshd's effective PermitRootLogin is not no:\n%s", grepLines(eff, "permitroot"))
		}
	})

	t.Run("the whole sequence is re-runnable", func(t *testing.T) {
		// This is the acceptance criterion that matters most in the field: a
		// wizard step that timed out on the operator's phone gets retried, and
		// every one of these must converge rather than fail.
		host, err := s.SetHostname(ctx, privhelper.SetHostnameRequest{
			Hostname: testHostname, UpdateHosts: true})
		if err != nil || host.Changed {
			t.Errorf("SetHostname re-run: changed=%v err=%v", host.Changed, err)
		}

		user, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
			Username: testUser, SSHKey: testKey, Sudo: true})
		if err != nil {
			t.Errorf("CreateRecoveryUser re-run: %v", err)
		}
		if user.Created {
			t.Error("CreateRecoveryUser re-run reported a new account")
		}

		key, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
			Username: testUser, PublicKey: testKey})
		if err != nil {
			t.Errorf("InstallSSHKey re-run: %v", err)
		}
		if key.Added || key.KeyCount != 1 {
			t.Errorf("InstallSSHKey re-run = %+v, want not added and one key", key)
		}

		lock, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
		if err != nil {
			t.Errorf("LockRoot re-run: %v", err)
		}
		if !lock.Locked || lock.Changed {
			t.Errorf("LockRoot re-run = %+v, want locked and unchanged", lock)
		}
	})

	t.Run("out-of-vocabulary input is refused", func(t *testing.T) {
		bad := []struct {
			name string
			call func() error
		}{
			{"the default hostname", func() error {
				_, err := s.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "raspberrypi"})
				return err
			}},
			{"root as the recovery user", func() error {
				_, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
					Username: "root", Password: "correct horse"})
				return err
			}},
			{"a password carrying a chpasswd injection", func() error {
				_, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
					Username: "rescue2", Password: "hunter22\nroot:owned"})
				return err
			}},
			{"a multi-line ssh key", func() error {
				_, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
					Username: testUser, PublicKey: testKey + "\n" + testKey})
				return err
			}},
		}
		for _, tc := range bad {
			t.Run(tc.name, func(t *testing.T) {
				if code := privhelper.CodeOf(tc.call()); code != privhelper.CodeInvalidArgument {
					t.Errorf("code = %q, want %q", code, privhelper.CodeInvalidArgument)
				}
			})
		}
		// The injection attempt created nothing.
		if out, err := exec.Command("getent", "passwd", "rescue2").Output(); err == nil && len(out) > 0 {
			t.Errorf("the rejected request created an account: %s", out)
		}
	})
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func grepLines(body, substr string) string {
	var out []string
	for _, l := range strings.Split(body, "\n") {
		if strings.Contains(strings.ToLower(l), substr) {
			out = append(out, l)
		}
	}
	return strings.Join(out, "\n")
}
