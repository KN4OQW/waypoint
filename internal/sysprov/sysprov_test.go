package sysprov

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// --- a scripted system ----------------------------------------------------

// fakeSystem stands in for the commands sysprov shells out to. It is stateful
// rather than a canned-response table: useradd really adds to its account map, so
// the second call to CreateRecoveryUser sees what the first one did. Idempotency
// is the property under test, and a stateless stub cannot exercise it.
type fakeSystem struct {
	mu       sync.Mutex
	users    map[string]*fakeAccount
	groups   map[string][]string
	hostname string
	calls    []recorded
	fail     map[string]error // keyed by command name
	// wifiBlocked models the rfkill soft block a freshly flashed Raspberry Pi OS
	// carries until a regulatory country is set.
	wifiBlocked bool
	// nmRadioOff models NetworkManager's own Wi-Fi switch, which reads "disabled"
	// on a fresh Raspberry Pi OS and is not the same thing as rfkill.
	nmRadioOff bool
}

type fakeAccount struct {
	uid, gid int
	home     string
	shell    string
	// status is passwd -S's second field: P usable, L locked, NP no password.
	status string
}

type recorded struct {
	name  string
	args  []string
	stdin string
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		users:    map[string]*fakeAccount{"root": {uid: 0, gid: 0, home: "/root", shell: "/bin/bash", status: "P"}},
		groups:   map[string][]string{"sudo": {}},
		hostname: "raspberrypi",
		fail:     map[string]error{},
	}
}

func (f *fakeSystem) runner() Runner {
	return func(_ context.Context, stdin []byte, name string, args ...string) (string, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, recorded{name: name, args: args, stdin: string(stdin)})
		if err, ok := f.fail[name]; ok {
			return "", err
		}
		return f.dispatch(name, args, string(stdin))
	}
}

func (f *fakeSystem) dispatch(name string, args []string, stdin string) (string, error) {
	switch name {
	case "getent":
		if len(args) == 2 && args[0] == "passwd" {
			u, ok := f.users[args[1]]
			if !ok {
				return "", fmt.Errorf("getent: not found")
			}
			return fmt.Sprintf("%s:x:%d:%d:,,,:%s:%s\n", args[1], u.uid, u.gid, u.home, u.shell), nil
		}
		if len(args) == 2 && args[0] == "group" {
			m, ok := f.groups[args[1]]
			if !ok {
				return "", fmt.Errorf("getent: not found")
			}
			return fmt.Sprintf("%s:x:27:%s\n", args[1], strings.Join(m, ",")), nil
		}
		return "", fmt.Errorf("getent: unsupported")

	case "useradd":
		user := args[len(args)-1]
		if _, ok := f.users[user]; ok {
			return "", fmt.Errorf("useradd: user exists")
		}
		shell := "/bin/sh"
		for i, a := range args {
			if a == "--shell" && i+1 < len(args) {
				shell = args[i+1]
			}
		}
		f.users[user] = &fakeAccount{uid: 1000 + len(f.users), gid: 1000 + len(f.users),
			home: "/home/" + user, shell: shell, status: "L"}
		return "", nil

	case "usermod":
		user := args[len(args)-1]
		for i, a := range args {
			if a == "--groups" && i+1 < len(args) {
				for _, g := range strings.Split(args[i+1], ",") {
					if !contains(f.groups[g], user) {
						f.groups[g] = append(f.groups[g], user)
					}
				}
			}
		}
		return "", nil

	case "chsh":
		user := args[len(args)-1]
		for i, a := range args {
			if a == "--shell" && i+1 < len(args) && f.users[user] != nil {
				f.users[user].shell = args[i+1]
			}
		}
		return "", nil

	case "passwd":
		if len(args) == 2 && args[0] == "--status" {
			u, ok := f.users[args[1]]
			if !ok {
				return "", fmt.Errorf("passwd: no such user")
			}
			return fmt.Sprintf("%s %s 2026-07-26 0 99999 7 -1\n", args[1], u.status), nil
		}
		if len(args) == 2 && args[0] == "--lock" {
			if u, ok := f.users[args[1]]; ok {
				u.status = "L"
			}
			return "", nil
		}
		return "", fmt.Errorf("passwd: unsupported")

	case "chpasswd":
		user, _, ok := strings.Cut(strings.TrimSpace(stdin), ":")
		if !ok {
			return "", fmt.Errorf("chpasswd: malformed input")
		}
		if u, ok := f.users[user]; ok {
			u.status = "P"
		}
		return "", nil

	case "hostname":
		if len(args) == 1 {
			f.hostname = args[0]
		}
		return f.hostname + "\n", nil

	case "hostnamectl":
		if len(args) == 1 && args[0] == "--static" {
			return f.hostname + "\n", nil
		}
		if len(args) == 2 && args[0] == "set-hostname" {
			f.hostname = args[1]
			return "", nil
		}
		return "", fmt.Errorf("hostnamectl: unsupported")

	case "nmcli":
		return f.nmcli(args)

	case "rfkill":
		// Unblocking is what makes the radio available on a fresh Raspberry Pi OS.
		if len(args) >= 2 && args[0] == "unblock" {
			f.wifiBlocked = false
		}
		return "", nil

	case "iw", "systemctl", "sshd", "sysctl", "nft", "iptables", "ss", "pgrep", "visudo", "userdel":
		return "", nil
	}
	return "", fmt.Errorf("unexpected command %q", name)
}

func (f *fakeSystem) nmcli(args []string) (string, error) {
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "DEVICE,TYPE device"):
		return "eth0:ethernet\nwlan0:wifi\nlo:loopback\n", nil
	case strings.Contains(joined, "DEVICE,STATE device"):
		// The radio reads as usable. The blocked case — a freshly flashed Pi, where
		// this says "unavailable" — is exercised by TestAPUpUnblocksABlockedRadio.
		if f.wifiBlocked || f.nmRadioOff {
			return "eth0:connected\nwlan0:unavailable\nlo:connected\n", nil
		}
		return "eth0:connected\nwlan0:disconnected\nlo:connected\n", nil
	case strings.Contains(joined, "connection show --active"):
		return "", nil
	case strings.HasPrefix(joined, "-g IP4.ADDRESS"):
		return "192.168.1.50/24\n", nil
	case joined == "radio wifi on":
		// NetworkManager's own switch, separate from rfkill. Both have to be off
		// for the device to become available.
		f.nmRadioOff = false
		return "", nil
	case strings.HasPrefix(joined, "-g connection.id connection show "):
		// waitForProfile polls this until NetworkManager reports the profile.
		// Answering immediately keeps the unit suite from waiting out the real
		// ingest timeout on every join.
		return args[len(args)-1] + "\n", nil
	}
	return "", nil
}

// argvContains reports whether any recorded command carried s as an argument —
// the check that a secret never reached /proc/<pid>/cmdline.
func (f *fakeSystem) argvContains(s string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		for _, a := range c.args {
			if strings.Contains(a, s) {
				return c.name, true
			}
		}
	}
	return "", false
}

func (f *fakeSystem) ran(name string, argSubstr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.name == name && strings.Contains(strings.Join(c.args, " "), argSubstr) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// newSystem wires a System to a fake set of commands and a temporary root. The
// temporary root means the file-writing half of every method — hosts, sshd
// drop-ins, authorized_keys, NM keyfiles — is genuinely exercised.
func newSystem(t *testing.T) (*System, *fakeSystem) {
	t.Helper()
	fs := newFakeSystem()
	s := &System{Run: fs.runner(), Root: t.TempDir(), handles: map[string]string{}}
	return s, fs
}

func (s *System) read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(s.path(p))
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func testKey(seed byte, comment string) string {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = seed + byte(i)
	}
	const algo = "ssh-ed25519"
	blob := binary.BigEndian.AppendUint32(nil, uint32(len(algo)))
	blob = append(blob, algo...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(pub)))
	blob = append(blob, pub...)
	return algo + " " + base64.StdEncoding.EncodeToString(blob) + " " + comment
}

// --- hostname -------------------------------------------------------------

// Property 1: the rename updates /etc/hosts as well, and re-running it changes
// nothing. The hosts line is not a nicety — a Debian box whose 127.0.1.1 entry
// names the old host is the one where sudo pauses for ten seconds.
func TestSetHostname(t *testing.T) {
	s, fs := newSystem(t)
	if err := os.MkdirAll(s.path("/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	const hosts = "127.0.0.1\tlocalhost\n127.0.1.1\traspberrypi\n::1\tip6-localhost\n"
	if err := os.WriteFile(s.path(etcHosts), []byte(hosts), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := s.SetHostname(context.Background(), privhelper.SetHostnameRequest{
		Hostname: "hs-shack", UpdateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Changed || got.Previous != "raspberrypi" || got.Hostname != "hs-shack" {
		t.Fatalf("response = %+v", got)
	}
	if fs.hostname != "hs-shack" {
		t.Errorf("the system hostname is %q", fs.hostname)
	}
	if h := s.read(t, etcHosts); !strings.Contains(h, "127.0.1.1\ths-shack") {
		t.Errorf("/etc/hosts was not updated:\n%s", h)
	} else if strings.Contains(h, "raspberrypi") {
		t.Errorf("/etc/hosts still names the old host:\n%s", h)
	} else if !strings.Contains(h, "127.0.0.1\tlocalhost") || !strings.Contains(h, "ip6-localhost") {
		t.Errorf("unrelated /etc/hosts lines were disturbed:\n%s", h)
	}

	// Re-running the same step is a no-op, not an error and not a second edit.
	again, err := s.SetHostname(context.Background(), privhelper.SetHostnameRequest{
		Hostname: "hs-shack", UpdateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Error("re-running SetHostname reported a change")
	}
}

// Property 2: aliases an operator added to the 127.0.1.1 line survive the rename.
func TestSetHostnamePreservesHostsAliases(t *testing.T) {
	s, _ := newSystem(t)
	if err := os.MkdirAll(s.path("/etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.path(etcHosts),
		[]byte("127.0.1.1\traspberrypi dashboard shack-old\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := s.SetHostname(context.Background(), privhelper.SetHostnameRequest{
		Hostname: "hs-shack", UpdateHosts: true}); err != nil {
		t.Fatal(err)
	}
	h := s.read(t, etcHosts)
	for _, want := range []string{"hs-shack", "dashboard", "shack-old"} {
		if !strings.Contains(h, want) {
			t.Errorf("%q missing from the rewritten hosts line:\n%s", want, h)
		}
	}
}

// Property 3: with no /etc/hosts at all — which is not normal, but is what a
// minimal container looks like — the line is created rather than the call failing.
func TestSetHostnameCreatesHostsFile(t *testing.T) {
	s, _ := newSystem(t)
	if _, err := s.SetHostname(context.Background(), privhelper.SetHostnameRequest{
		Hostname: "hs-shack", UpdateHosts: true}); err != nil {
		t.Fatal(err)
	}
	if h := s.read(t, etcHosts); !strings.Contains(h, "hs-shack") {
		t.Errorf("hosts = %q", h)
	}
}

// --- accounts -------------------------------------------------------------

// Property 4: creating the recovery account is idempotent, and the password never
// appears in a command line. /proc/<pid>/cmdline is readable by other local users,
// so a password passed as an argument is a password every local account can read.
func TestCreateRecoveryUser(t *testing.T) {
	s, fs := newSystem(t)
	ctx := context.Background()
	const pw = "correct horse battery"

	got, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", Password: pw, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created || got.UID == 0 || !got.Sudo {
		t.Fatalf("response = %+v", got)
	}
	if got.PasswordLocked {
		t.Error("an account created with a password is reported as locked")
	}
	if cmd, leaked := fs.argvContains(pw); leaked {
		t.Errorf("the password reached %s's argv, where /proc exposes it", cmd)
	}
	if !fs.ran("chpasswd", "") {
		t.Error("chpasswd was not used to set the password")
	}
	if !contains(fs.groups[sudoGroup], "rescue") {
		t.Error("the account is not in the sudo group")
	}

	// Re-running converges rather than failing on "user already exists".
	again, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", Password: pw, Sudo: true})
	if err != nil {
		t.Fatalf("re-running CreateRecoveryUser: %v", err)
	}
	if again.Created {
		t.Error("Created = true the second time")
	}
	if again.UID != got.UID {
		t.Errorf("uid changed between runs: %d then %d", got.UID, again.UID)
	}
}

// Property 5: a key-only account has its password locked and its key installed in
// the same call, so it is never briefly reachable without either.
func TestCreateRecoveryUserKeyOnly(t *testing.T) {
	s, fs := newSystem(t)
	key := testKey(1, "operator@laptop")

	got, err := s.CreateRecoveryUser(context.Background(), privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: key, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.PasswordLocked {
		t.Error("a key-only account is not password-locked")
	}
	if !strings.HasPrefix(got.SSHKeyFingerprint, "SHA256:") {
		t.Errorf("fingerprint = %q", got.SSHKeyFingerprint)
	}
	if !fs.ran("passwd", "--lock rescue") {
		t.Error("passwd --lock was not run for the key-only account")
	}
	if ak := s.read(t, "/home/rescue/.ssh/authorized_keys"); !strings.Contains(ak, key) {
		t.Errorf("authorized_keys = %q", ak)
	}
	fi, err := os.Stat(s.path("/home/rescue/.ssh/authorized_keys"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("authorized_keys mode = %o, want 600 — sshd refuses a laxer one", fi.Mode().Perm())
	}
}

// Property 6: an account that exists but cannot log in is repaired. A recovery
// account with /usr/sbin/nologin is a name in a file, not a way back in.
func TestCreateRecoveryUserRepairsNologinShell(t *testing.T) {
	s, fs := newSystem(t)
	fs.users["rescue"] = &fakeAccount{uid: 1001, gid: 1001, home: "/home/rescue",
		shell: "/usr/sbin/nologin", status: "L"}

	got, err := s.CreateRecoveryUser(context.Background(), privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: testKey(2, "op"), Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Created {
		t.Error("Created = true for an account that already existed")
	}
	if fs.users["rescue"].shell != defaultShell {
		t.Errorf("shell = %q, want %q", fs.users["rescue"].shell, defaultShell)
	}
}

// Property 7: a name that resolves to uid 0 is refused, whatever it is spelled.
// The static deny list in privhelper cannot see this; only the system can.
func TestCreateRecoveryUserRefusesUIDZero(t *testing.T) {
	s, fs := newSystem(t)
	fs.users["toor"] = &fakeAccount{uid: 0, gid: 0, home: "/root", shell: "/bin/bash", status: "P"}

	_, err := s.CreateRecoveryUser(context.Background(), privhelper.CreateRecoveryUserRequest{
		Username: "toor", Password: "correct horse", Sudo: true})
	if privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
		t.Fatalf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeInvalidArgument)
	}
}

// Property 8: the same key pasted twice is one key. Comparison is by fingerprint,
// so re-pasting it with a different comment does not produce what looks to the
// operator like two authorized laptops.
func TestInstallSSHKeyDeduplicates(t *testing.T) {
	s, fs := newSystem(t)
	ctx := context.Background()
	fs.users["rescue"] = &fakeAccount{uid: 1001, gid: 1001, home: "/home/rescue",
		shell: defaultShell, status: "L"}

	first, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: testKey(1, "operator@laptop")})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Added || first.KeyCount != 1 {
		t.Fatalf("first install = %+v", first)
	}

	// Same key, different comment.
	dup, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: testKey(1, "operator@phone")})
	if err != nil {
		t.Fatal(err)
	}
	if dup.Added || dup.KeyCount != 1 {
		t.Errorf("re-installing the same key = %+v, want not added and one key", dup)
	}

	second, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: testKey(9, "second@laptop")})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Added || second.KeyCount != 2 {
		t.Errorf("a genuinely different key = %+v, want added and two keys", second)
	}

	if _, err := s.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "ghost", PublicKey: testKey(3, "x")}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("unknown user: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeNotFound)
	}
}

// Property 9: an operator's own authorized_keys entries survive an install, even
// ones this package would refuse on the way in. Rewriting the file is not licence
// to drop a key that is already trusted.
func TestInstallSSHKeyPreservesExistingEntries(t *testing.T) {
	s, fs := newSystem(t)
	fs.users["rescue"] = &fakeAccount{uid: 1001, gid: 1001, home: "/home/rescue",
		shell: defaultShell, status: "L"}
	akDir := s.path("/home/rescue/.ssh")
	if err := os.MkdirAll(akDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const restricted = `from="10.0.0.1",no-pty ` + "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample theirs"
	if err := os.WriteFile(filepath.Join(akDir, "authorized_keys"),
		[]byte(restricted+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.InstallSSHKey(context.Background(), privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: testKey(4, "new@laptop")}); err != nil {
		t.Fatal(err)
	}
	ak := s.read(t, "/home/rescue/.ssh/authorized_keys")
	if !strings.Contains(ak, restricted) {
		t.Errorf("the operator's own entry was dropped:\n%s", ak)
	}
}

// --- root -----------------------------------------------------------------

// Property 10: root is not locked while nothing else can log in. This is the
// contract that keeps a wizard bug from producing a node with no way in — and the
// check is "can this account actually log in", not "is it in the sudo group",
// because a locked, keyless member of the sudo group is not a way in.
func TestLockRootRefusesWithoutAUsableRecoveryAccount(t *testing.T) {
	s, fs := newSystem(t)
	ctx := context.Background()

	_, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
	if privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("no accounts at all: code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}

	// A sudo member that is locked and has no key is still not a way in.
	fs.users["rescue"] = &fakeAccount{uid: 1001, gid: 1001, home: "/home/rescue",
		shell: defaultShell, status: "L"}
	fs.groups[sudoGroup] = []string{"rescue"}
	_, err = s.LockRoot(ctx, privhelper.LockRootRequest{})
	if privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("locked keyless sudo member: code = %q (err %v), want %q",
			privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}
	if fs.users["root"].status == "L" {
		t.Fatal("root was locked despite the refusal")
	}
}

// Property 11: with a usable recovery account, LockRoot locks the password AND
// tells sshd to refuse root. Locking the password alone is not enough — a root
// login with an SSH key bypasses it entirely.
func TestLockRoot(t *testing.T) {
	s, fs := newSystem(t)
	ctx := context.Background()

	if _, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: testKey(1, "op"), Sudo: true}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Locked || !got.Changed {
		t.Fatalf("response = %+v", got)
	}
	if fs.users["root"].status != "L" {
		t.Errorf("root's password status is %q, want L", fs.users["root"].status)
	}
	drop := s.read(t, rootLoginDropIn)
	if !strings.Contains(drop, "PermitRootLogin no") {
		t.Errorf("the sshd drop-in does not refuse root logins:\n%s", drop)
	}
	if !strings.Contains(drop, "do NOT edit") {
		t.Errorf("the drop-in has no managed header:\n%s", drop)
	}

	again, err := s.LockRoot(ctx, privhelper.LockRootRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Locked || again.Changed {
		t.Errorf("second LockRoot = %+v, want locked and unchanged", again)
	}
}

// --- sshd -----------------------------------------------------------------

// Property 12: the drop-in is written where sshd will actually read it, and a nil
// PasswordAuth leaves it alone rather than adopting an opinion.
func TestEnableSSH(t *testing.T) {
	s, _ := newSystem(t)
	ctx := context.Background()

	no := false
	got, err := s.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true, PasswordAuth: &no})
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordAuth {
		t.Error("PasswordAuth = true after asking for no")
	}
	body := s.read(t, passwordAuthDropIn)
	if !strings.Contains(body, "PasswordAuthentication no") {
		t.Errorf("drop-in =\n%s", body)
	}

	// nil leaves the file untouched — a call that only meant to start the service
	// must not silently re-enable password logins.
	if _, err := s.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if body2 := s.read(t, passwordAuthDropIn); body2 != body {
		t.Errorf("a nil PasswordAuth rewrote the drop-in:\n%s", body2)
	}

	// And re-asking for the same thing reports no change.
	again, err := s.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true, PasswordAuth: &no})
	if err != nil {
		t.Fatal(err)
	}
	if again.Changed {
		t.Error("re-applying the same setting reported a change")
	}
}

// --- network --------------------------------------------------------------

// Property 13: the Wi-Fi secret goes into a 0600 keyfile, never into an argument
// vector. This is the reason this package writes NetworkManager profiles itself
// instead of calling `nmcli device wifi connect … password …`.
func TestNetJoinKeepsThePSKOutOfArgv(t *testing.T) {
	s, fs := newSystem(t)
	const psk = "correct horse battery staple"

	got, err := s.NetJoin(context.Background(), privhelper.NetJoinRequest{
		SSID: "home network", PSK: psk, Country: "US", TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if cmd, leaked := fs.argvContains(psk); leaked {
		t.Errorf("the PSK reached %s's argv, where /proc exposes it", cmd)
	}
	if !got.Connected || got.Profile == "" {
		t.Fatalf("response = %+v", got)
	}

	path := filepath.Join(s.path(nmKeyfileDir), got.Profile+".nmconnection")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "psk="+psk) {
		t.Errorf("the profile does not carry the PSK:\n%s", b)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("profile mode = %o, want 600 — NetworkManager refuses a laxer one", fi.Mode().Perm())
	}
}

// Property 14: an SSID becomes a filename, so it is sanitised into one. Path
// traversal through a network name is made structurally impossible rather than
// merely unlikely.
func TestProfileNameSanitisesTheSSID(t *testing.T) {
	cases := map[string]string{
		"home":            "waypoint-home",
		"home network":    "waypoint-home_network",
		"../../etc/shine": "waypoint-______etc_shine",
		"a/b":             "waypoint-a_b",
		"Wi-Fi_2":         "waypoint-Wi-Fi_2",
	}
	for ssid, want := range cases {
		if got := profileName(ssid); got != want {
			t.Errorf("profileName(%q) = %q, want %q", ssid, got, want)
		}
		if strings.ContainsAny(profileName(ssid), "/.") {
			t.Errorf("profileName(%q) = %q still contains a path character", ssid, profileName(ssid))
		}
	}
}

// Property 15: the access point comes up, is idempotent, and comes back down.
func TestAPUpDown(t *testing.T) {
	s, _ := newSystem(t)
	ctx := context.Background()
	req := privhelper.APUpRequest{SSID: "waypoint-setup", Passphrase: "setup12345",
		Country: "US", AddressCIDR: "192.168.66.1/24"}

	got, err := s.APUp(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Active || got.Interface != "wlan0" {
		t.Fatalf("response = %+v", got)
	}
	body, err := os.ReadFile(filepath.Join(s.path(nmKeyfileDir), apProfile+".nmconnection"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=ap", "method=shared", "address1=192.168.66.1/24", "band=bg"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the AP profile is missing %q:\n%s", want, body)
		}
	}

	down, err := s.APDown(ctx, privhelper.APDownRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if down.Active {
		t.Error("the AP is still active after APDown")
	}
	// The profile survives: an operator whose join fails needs the AP back, and
	// re-raising a profile that is still there is one call rather than a rebuild.
	if _, err := os.Stat(filepath.Join(s.path(nmKeyfileDir), apProfile+".nmconnection")); err != nil {
		t.Errorf("APDown deleted the profile: %v", err)
	}
}

// Property 16: a checkpoint handle is single-use, and one this process never
// issued is not found. Handles crossing the socket are looked up in a map this
// process owns, so no string a client sends reaches the checkpoint backend.
func TestCheckpointHandles(t *testing.T) {
	s, _ := newSystem(t)
	ctx := context.Background()

	made, err := s.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{TimeoutSeconds: 90})
	if err != nil {
		t.Fatal(err)
	}
	if made.Handle == "" || made.ExpiresAt.IsZero() {
		t.Fatalf("response = %+v", made)
	}

	if _, err := s.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{
		Handle: "../../etc/passwd"}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("a made-up handle: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeNotFound)
	}
	if _, err := s.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{Handle: made.Handle}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{
		Handle: made.Handle}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("a consumed handle: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeNotFound)
	}
}

// Property 17: every method re-validates its request even when called in
// process, so a validator that the socket path enforces is not one an in-process
// caller can skip.
func TestMethodsRevalidate(t *testing.T) {
	s, _ := newSystem(t)
	ctx := context.Background()

	if _, err := s.SetHostname(ctx, privhelper.SetHostnameRequest{
		Hostname: "raspberrypi"}); privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
		t.Errorf("SetHostname: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeInvalidArgument)
	}
	if _, err := s.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "root", Password: "correct horse"}); privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
		t.Errorf("CreateRecoveryUser: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeInvalidArgument)
	}
	if _, err := s.NetJoin(ctx, privhelper.NetJoinRequest{
		SSID: "home", PSK: "short"}); privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
		t.Errorf("NetJoin: code = %q, want %q", privhelper.CodeOf(err), privhelper.CodeInvalidArgument)
	}
}

// Property 18: a radio that is rfkill-blocked is unblocked before the access
// point is raised.
//
// This is the state every freshly flashed Raspberry Pi OS is in: the radio is
// soft-blocked pending a regulatory country, and the world domain permits no
// 2.4 GHz channel it could beacon on. NetworkManager then reports the device
// unavailable, falls back to any other device, and rejects it — surfacing as a
// message about eth0 for a problem with the radio. Found on a Pi 3B running the
// v-nova image, where it meant the setup access point could never come up on a
// fresh flash.
func TestAPUpUnblocksABlockedRadio(t *testing.T) {
	s, fs := newSystem(t)
	fs.wifiBlocked = true
	fs.nmRadioOff = true

	got, err := s.APUp(context.Background(), privhelper.APUpRequest{SSID: "Waypoint-Setup-9E10"})
	if err != nil {
		t.Fatalf("APUp on a blocked radio: %v", err)
	}
	if !got.Active {
		t.Errorf("response = %+v", got)
	}
	if !fs.ran("rfkill", "unblock wifi") {
		t.Error("the radio was never unblocked")
	}
	// A domain has to be set too: unblocking alone leaves the world domain, which
	// permits no channel to beacon on.
	if !fs.ran("iw", "reg set US") {
		t.Error("no regulatory domain was set; the radio would have no permitted channels")
	}
	// The one that is easy to miss: rfkill and the domain can both be right and the
	// device still unavailable until NetworkManager's own switch is on.
	if !fs.ran("nmcli", "radio wifi on") {
		t.Error("NetworkManager's Wi-Fi switch was never enabled")
	}
	// And the AP is pinned to a channel legal in every 2.4 GHz domain.
	body, err := os.ReadFile(filepath.Join(s.path(nmKeyfileDir), apProfile+".nmconnection"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "channel=6") {
		t.Errorf("the AP profile does not pin a channel:\n%s", body)
	}
}

// Property 19: a radio that stays blocked produces an error naming the radio,
// not one naming Ethernet.
func TestAPUpReportsAStuckRadio(t *testing.T) {
	s, fs := newSystem(t)
	fs.wifiBlocked = true
	fs.nmRadioOff = true
	fs.fail["rfkill"] = errFakeRfkill // unblocking does not take

	_, err := s.APUp(context.Background(), privhelper.APUpRequest{SSID: "Waypoint-Setup-9E10"})
	if privhelper.CodeOf(err) != privhelper.CodeUnsupported {
		t.Fatalf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeUnsupported)
	}
	for _, want := range []string{"rfkill", "regulatory", "wlan0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not mention %q: %v", want, err)
		}
	}
}

var errFakeRfkill = fmt.Errorf("rfkill: operation not permitted")
