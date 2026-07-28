package privtest

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// SSHPublicKey builds a structurally valid ed25519 authorized_keys line from a
// seed, so tests have real keys — correct algorithm name, correct wire framing,
// decodable base64 — without generating cryptography they never use. Different
// seeds give different keys, which is what the "installing a second key" cases
// need.
func SSHPublicKey(seed byte) string {
	pub := make([]byte, 32)
	for i := range pub {
		pub[i] = seed + byte(i)
	}
	const algo = "ssh-ed25519"
	blob := make([]byte, 0, 4+len(algo)+4+len(pub))
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(algo)))
	blob = append(blob, algo...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(pub)))
	blob = append(blob, pub...)
	return algo + " " + base64.StdEncoding.EncodeToString(blob) + " test@waypoint"
}

// Conform runs the behaviour every Provisioner must exhibit. newProvisioner is
// called once per subtest and must return an implementation in the state a
// freshly flashed node is in.
//
// The suite is the executable half of the interface's documentation: the ordering
// contracts, the idempotency rules, and the error classifications are asserted
// here rather than only described in api.go, so the fake and the real helper
// cannot drift apart on any of them.
func Conform(t *testing.T, newProvisioner func(t *testing.T) privhelper.Provisioner) {
	t.Helper()

	t.Run("rejects invalid arguments", func(t *testing.T) {
		conformInvalidArguments(t, newProvisioner)
	})
	t.Run("hostname refuses the shipped defaults", func(t *testing.T) {
		conformHostnameDefaults(t, newProvisioner)
	})
	t.Run("hostname is idempotent", func(t *testing.T) {
		conformHostnameIdempotent(t, newProvisioner)
	})
	t.Run("root is not locked without a recovery account", func(t *testing.T) {
		conformLockRootOrdering(t, newProvisioner)
	})
	t.Run("recovery account", func(t *testing.T) {
		conformRecoveryUser(t, newProvisioner)
	})
	t.Run("ssh keys", func(t *testing.T) {
		conformSSHKeys(t, newProvisioner)
	})
	t.Run("ssh daemon", func(t *testing.T) {
		conformEnableSSH(t, newProvisioner)
	})
	t.Run("access point", func(t *testing.T) {
		conformAP(t, newProvisioner)
	})
	t.Run("net join", func(t *testing.T) {
		conformNetJoin(t, newProvisioner)
	})
	t.Run("checkpoint lifecycle", func(t *testing.T) {
		conformCheckpoints(t, newProvisioner)
	})
	t.Run("sudo account enumeration", func(t *testing.T) {
		conformListSudoUsers(t, newProvisioner)
	})
	t.Run("account removal refusals", func(t *testing.T) {
		conformRemoveUser(t, newProvisioner)
	})
	t.Run("every method honours a cancelled context", func(t *testing.T) {
		conformContext(t, newProvisioner)
	})
}

// validKey is used wherever a call needs a key but the key is not what is under test.
var validKey = SSHPublicKey(1)

func conformInvalidArguments(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	cases := []struct {
		name string
		call func(privhelper.Provisioner) error
	}{
		{"empty hostname", func(p privhelper.Provisioner) error {
			_, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{})
			return err
		}},
		{"hostname with a leading hyphen", func(p privhelper.Provisioner) error {
			_, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "-shack"})
			return err
		}},
		{"dotted hostname", func(p privhelper.Provisioner) error {
			_, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "shack.local"})
			return err
		}},
		{"root as the recovery user", func(p privhelper.Provisioner) error {
			_, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
				Username: "root", Password: "correct horse", Sudo: true})
			return err
		}},
		{"recovery user with no way in", func(p privhelper.Provisioner) error {
			_, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{Username: "rescue"})
			return err
		}},
		{"password containing a newline", func(p privhelper.Provisioner) error {
			_, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
				Username: "rescue", Password: "hunter22\nroot:owned"})
			return err
		}},
		{"multi-line ssh key", func(p privhelper.Provisioner) error {
			_, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
				Username: "rescue", PublicKey: validKey + "\n" + validKey})
			return err
		}},
		{"ssh key with a command option", func(p privhelper.Provisioner) error {
			_, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
				Username: "rescue", PublicKey: `command="/bin/sh" ` + validKey})
			return err
		}},
		{"over-long ssid", func(p privhelper.Provisioner) error {
			_, err := p.APUp(ctx, privhelper.APUpRequest{SSID: strings.Repeat("x", 33)})
			return err
		}},
		{"short wifi passphrase", func(p privhelper.Provisioner) error {
			_, err := p.NetJoin(ctx, privhelper.NetJoinRequest{SSID: "home", PSK: "short"})
			return err
		}},
		{"checkpoint with no window", func(p privhelper.Provisioner) error {
			_, err := p.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{})
			return err
		}},
		{"empty checkpoint handle", func(p privhelper.Provisioner) error {
			_, err := p.NetCheckpointRollback(ctx, privhelper.NetCheckpointRollbackRequest{})
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call(newP(t))
			if got := privhelper.CodeOf(err); got != privhelper.CodeInvalidArgument {
				t.Errorf("code = %q (err %v), want %q", got, err, privhelper.CodeInvalidArgument)
			}
		})
	}
}

func conformHostnameDefaults(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	// Casing varies because an operator retyping the default will not match it
	// exactly, and a case-sensitive check would let "Raspberrypi" straight through.
	for _, name := range []string{"raspberrypi", "RaspberryPi", "RASPBERRYPI", "waypoint", "Waypoint", "localhost"} {
		t.Run(name, func(t *testing.T) {
			p := newP(t)
			_, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: name})
			if got := privhelper.CodeOf(err); got != privhelper.CodeInvalidArgument {
				t.Errorf("SetHostname(%q) code = %q (err %v), want %q — the default name must be refused",
					name, got, err, privhelper.CodeInvalidArgument)
			}
		})
	}
}

func conformHostnameIdempotent(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	first, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "hs-shack", UpdateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Changed {
		t.Error("first SetHostname reported no change")
	}
	if first.Hostname != "hs-shack" {
		t.Errorf("Hostname = %q, want %q", first.Hostname, "hs-shack")
	}
	if first.Previous == "" {
		t.Error("Previous is empty; the caller cannot tell the operator what it was called before")
	}

	second, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "hs-shack", UpdateHosts: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.Changed {
		t.Error("re-setting the same hostname reported a change; a retried wizard step must be a no-op")
	}
}

func conformLockRootOrdering(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	// The contract: locking root on a node with no other administrative account
	// makes it unrecoverable, so it is refused rather than obeyed.
	if _, err := p.LockRoot(ctx, privhelper.LockRootRequest{}); privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("LockRoot before any recovery account: code = %q (err %v), want %q",
			privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}

	if _, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: validKey, Sudo: true}); err != nil {
		t.Fatal(err)
	}

	locked, err := p.LockRoot(ctx, privhelper.LockRootRequest{})
	if err != nil {
		t.Fatalf("LockRoot after a recovery account: %v", err)
	}
	if !locked.Locked || !locked.Changed {
		t.Errorf("LockRoot = %+v, want locked and changed", locked)
	}

	again, err := p.LockRoot(ctx, privhelper.LockRootRequest{})
	if err != nil {
		t.Fatalf("second LockRoot: %v", err)
	}
	if !again.Locked || again.Changed {
		t.Errorf("second LockRoot = %+v, want locked and unchanged", again)
	}
}

func conformRecoveryUser(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	// The recommended shape: a key, no password, so the account is reachable only
	// with something the operator already holds.
	got, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: validKey, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Created {
		t.Error("Created = false for a new account")
	}
	if !got.PasswordLocked {
		t.Error("PasswordLocked = false for a key-only account")
	}
	if !got.Sudo {
		t.Error("Sudo = false; a recovery account that cannot become root cannot recover the node")
	}
	if got.UID <= 0 {
		t.Errorf("UID = %d, want a real uid", got.UID)
	}
	if got.Home == "" {
		t.Error("Home is empty")
	}
	if !strings.HasPrefix(got.SSHKeyFingerprint, "SHA256:") {
		t.Errorf("SSHKeyFingerprint = %q, want an OpenSSH SHA256 fingerprint", got.SSHKeyFingerprint)
	}

	// Creating it again is a no-op, not a failure and not a second account.
	again, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: validKey, Sudo: true})
	if err != nil {
		t.Fatal(err)
	}
	if again.Created {
		t.Error("Created = true for an account that already existed")
	}
	if again.UID != got.UID {
		t.Errorf("UID changed on re-create: %d then %d", got.UID, again.UID)
	}
}

func conformSSHKeys(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	if _, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: validKey}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("InstallSSHKey for an unknown user: code = %q (err %v), want %q",
			privhelper.CodeOf(err), err, privhelper.CodeNotFound)
	}

	if _, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", Password: "correct horse battery", Sudo: true}); err != nil {
		t.Fatal(err)
	}

	first, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{Username: "rescue", PublicKey: validKey})
	if err != nil {
		t.Fatal(err)
	}
	if !first.Added {
		t.Error("Added = false for a key that was not present")
	}
	if !strings.HasPrefix(first.Fingerprint, "SHA256:") {
		t.Errorf("Fingerprint = %q, want an OpenSSH SHA256 fingerprint", first.Fingerprint)
	}
	if first.KeyCount != 1 {
		t.Errorf("KeyCount = %d, want 1", first.KeyCount)
	}

	// Installing the same key twice must not produce a duplicate line.
	dup, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{Username: "rescue", PublicKey: validKey})
	if err != nil {
		t.Fatal(err)
	}
	if dup.Added {
		t.Error("Added = true for a key that was already installed")
	}
	if dup.KeyCount != 1 {
		t.Errorf("KeyCount = %d after a duplicate install, want 1", dup.KeyCount)
	}

	// A second, different key is appended: adding a laptop must not evict a phone.
	second, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: SSHPublicKey(9)})
	if err != nil {
		t.Fatal(err)
	}
	if !second.Added || second.KeyCount != 2 {
		t.Errorf("second key: added=%v count=%d, want true/2", second.Added, second.KeyCount)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Error("two different keys produced the same fingerprint")
	}

	// Replace collapses back to one.
	repl, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
		Username: "rescue", PublicKey: validKey, Replace: true})
	if err != nil {
		t.Fatal(err)
	}
	if repl.KeyCount != 1 {
		t.Errorf("KeyCount = %d after Replace, want 1", repl.KeyCount)
	}
}

func conformEnableSSH(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	no := false
	off, err := p.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true, PasswordAuth: &no})
	if err != nil {
		t.Fatal(err)
	}
	if !off.Enabled || off.PasswordAuth {
		t.Errorf("EnableSSH = %+v, want enabled with password auth off", off)
	}

	// nil PasswordAuth means "leave it alone" — the distinction the pointer exists
	// for. A call that only meant to (re)start the service must not silently turn
	// password logins back on.
	keep, err := p.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if keep.PasswordAuth {
		t.Error("a nil PasswordAuth re-enabled password authentication")
	}
}

func conformAP(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	down, err := p.APDown(ctx, privhelper.APDownRequest{})
	if err != nil {
		t.Fatalf("APDown with no AP running: %v", err)
	}
	if down.Changed || down.Active {
		t.Errorf("APDown with no AP = %+v, want inactive and unchanged", down)
	}

	up, err := p.APUp(ctx, privhelper.APUpRequest{
		SSID: "waypoint-setup", Passphrase: "setup12345", Country: "US", AddressCIDR: "192.168.66.1/24"})
	if err != nil {
		t.Fatal(err)
	}
	if !up.Active || !up.Changed {
		t.Errorf("APUp = %+v, want active and changed", up)
	}
	if up.Interface == "" {
		t.Error("APUp returned no interface; the caller cannot tell the operator where to look")
	}

	same, err := p.APUp(ctx, privhelper.APUpRequest{
		SSID: "waypoint-setup", Passphrase: "setup12345", Country: "US", AddressCIDR: "192.168.66.1/24"})
	if err != nil {
		t.Fatal(err)
	}
	if same.Changed {
		t.Error("raising an identical AP reported a change")
	}

	off, err := p.APDown(ctx, privhelper.APDownRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if off.Active || !off.Changed {
		t.Errorf("APDown = %+v, want inactive and changed", off)
	}
}

func conformNetJoin(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	got, err := p.NetJoin(ctx, privhelper.NetJoinRequest{
		SSID: "home-network", PSK: "correct horse battery", Country: "US", TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected {
		t.Error("Connected = false on a successful join")
	}
	if got.Profile == "" {
		t.Error("Profile is empty; a later teardown cannot tell which profile it owns")
	}
	if got.Interface == "" {
		t.Error("Interface is empty")
	}

	// An open network is a valid join, not a validation failure.
	if _, err := p.NetJoin(ctx, privhelper.NetJoinRequest{SSID: "open-guest"}); err != nil {
		t.Errorf("joining an open network: %v", err)
	}
}

func conformCheckpoints(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	made, err := p.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{TimeoutSeconds: 90})
	if err != nil {
		t.Fatal(err)
	}
	if made.Handle == "" {
		t.Fatal("Handle is empty")
	}
	if made.ExpiresAt.IsZero() {
		t.Error("ExpiresAt is zero; the caller cannot show the operator how long they have")
	}

	// Two live checkpoints must not collide on a handle.
	other, err := p.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{TimeoutSeconds: 90})
	if err != nil {
		t.Fatal(err)
	}
	if other.Handle == made.Handle {
		t.Error("two checkpoints share a handle")
	}

	if _, err := p.NetCheckpointRollback(ctx, privhelper.NetCheckpointRollbackRequest{
		Handle: "cp-does-not-exist"}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("rollback of an unknown handle: code = %q (err %v), want %q",
			privhelper.CodeOf(err), err, privhelper.CodeNotFound)
	}

	if _, err := p.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{Handle: made.Handle}); err != nil {
		t.Fatal(err)
	}
	// A handle is consumed by Destroy — a second call is not a silent success,
	// because "I destroyed it" and "it was never there" mean different things to a
	// caller reconciling state.
	if _, err := p.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{
		Handle: made.Handle}); privhelper.CodeOf(err) != privhelper.CodeNotFound {
		t.Errorf("second destroy: code = %q (err %v), want %q",
			privhelper.CodeOf(err), err, privhelper.CodeNotFound)
	}

	rolled, err := p.NetCheckpointRollback(ctx, privhelper.NetCheckpointRollbackRequest{Handle: other.Handle})
	if err != nil {
		t.Fatal(err)
	}
	if !rolled.RolledBack {
		t.Error("RolledBack = false")
	}
}

// conformListSudoUsers checks the enumeration reports what an operator needs to
// decide with: who can become root, and how reachable each of them is.
func conformListSudoUsers(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()
	p := newP(t)

	// A fresh node has no administrators but root, which the call never reports.
	empty, err := p.ListSudoUsers(ctx, privhelper.ListSudoUsersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range empty.Users {
		if u.Username == "root" || u.UID == 0 {
			t.Errorf("root was listed as a removable administrator: %+v", u)
		}
	}

	if _, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: validKey, Sudo: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "previous-owner", Password: "correct horse battery", Sudo: true}); err != nil {
		t.Fatal(err)
	}

	got, err := p.ListSudoUsers(ctx, privhelper.ListSudoUsersRequest{})
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]privhelper.SudoUser{}
	for _, u := range got.Users {
		byName[u.Username] = u
		if u.UID < privhelper.MinHumanUID {
			t.Errorf("%q has uid %d, below the human floor — a system account was offered for removal",
				u.Username, u.UID)
		}
	}

	key, ok := byName["rescue"]
	if !ok {
		t.Fatalf("the key-only account is missing from %v", got.Users)
	}
	if key.SSHKeys != 1 {
		t.Errorf("rescue reports %d keys, want 1 — a key on an unrecognised account is standing remote access",
			key.SSHKeys)
	}
	if !key.PasswordLocked {
		t.Error("a key-only account does not report its password as locked")
	}
	if !key.Reachable() {
		t.Error("an account with a key reports as unreachable")
	}

	pw, ok := byName["previous-owner"]
	if !ok {
		t.Fatalf("the password account is missing from %v", got.Users)
	}
	if !pw.HasPassword || pw.PasswordLocked {
		t.Errorf("the password account reports %+v", pw)
	}
	if !pw.Reachable() {
		t.Error("an account with a password reports as unreachable")
	}

	// The summary is what the operator actually reads, so it has to say something.
	for _, u := range got.Users {
		if sum := u.Summary(); sum == "" || !strings.Contains(sum, "SSH key") {
			t.Errorf("%q summary is unhelpful: %q", u.Username, sum)
		}
	}
}

// conformRemoveUser checks every refusal. These are the ones that matter: each is
// a way an operator could destroy the node they are setting up, and none of them
// is recoverable from the wizard.
func conformRemoveUser(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx := context.Background()

	t.Run("refuses root by name", func(t *testing.T) {
		p := newP(t)
		_, err := p.RemoveUser(ctx, privhelper.RemoveUserRequest{Username: "root"})
		if privhelper.CodeOf(err) != privhelper.CodeInvalidArgument {
			t.Errorf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeInvalidArgument)
		}
	})

	t.Run("refuses a system account", func(t *testing.T) {
		p := newP(t)
		for _, name := range []string{"daemon", "www-data", "sshd"} {
			if _, err := p.RemoveUser(ctx, privhelper.RemoveUserRequest{Username: name}); err == nil {
				t.Errorf("removing %q was accepted", name)
			}
		}
	})

	t.Run("an absent account is success", func(t *testing.T) {
		p := newP(t)
		got, err := p.RemoveUser(ctx, privhelper.RemoveUserRequest{Username: "never-existed"})
		if err != nil {
			t.Fatalf("removing an absent account: %v", err)
		}
		if got.Removed {
			t.Error("Removed = true for an account that did not exist")
		}
	})

	t.Run("removes a real account", func(t *testing.T) {
		p := newP(t)
		if _, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
			Username: "previous-owner", SSHKey: validKey, Sudo: true}); err != nil {
			t.Fatal(err)
		}
		got, err := p.RemoveUser(ctx, privhelper.RemoveUserRequest{
			Username: "previous-owner", RemoveHome: true})
		if err != nil {
			t.Fatal(err)
		}
		if !got.Removed || !got.HomeRemoved {
			t.Errorf("response = %+v", got)
		}

		// And it is gone from the enumeration, not merely reported as removed.
		list, err := p.ListSudoUsers(ctx, privhelper.ListSudoUsersRequest{})
		if err != nil {
			t.Fatal(err)
		}
		for _, u := range list.Users {
			if u.Username == "previous-owner" {
				t.Error("the removed account is still listed")
			}
		}
	})
}

func conformContext(t *testing.T, newP func(t *testing.T) privhelper.Provisioner) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// Every call is otherwise valid, so a non-nil error can only come from the
	// cancelled context. These calls reach hostnamectl, useradd, and nmcli on a
	// real node; a caller whose HTTP request has gone away must not still be
	// renaming the host.
	for name, call := range allCalls(ctx) {
		p := newP(t)
		if err := call(p); err == nil {
			t.Errorf("%s ignored a cancelled context", name)
		}
	}
}

// allCalls maps each method to a valid invocation of it, so a suite can sweep the
// whole interface without eleven near-identical blocks.
func allCalls(ctx context.Context) map[privhelper.Method]func(privhelper.Provisioner) error {
	return map[privhelper.Method]func(privhelper.Provisioner) error{
		privhelper.MethodSetHostname: func(p privhelper.Provisioner) error {
			_, err := p.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "hs-shack"})
			return err
		},
		privhelper.MethodCreateRecoveryUser: func(p privhelper.Provisioner) error {
			_, err := p.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
				Username: "rescue", SSHKey: validKey, Sudo: true})
			return err
		},
		privhelper.MethodInstallSSHKey: func(p privhelper.Provisioner) error {
			_, err := p.InstallSSHKey(ctx, privhelper.InstallSSHKeyRequest{
				Username: "rescue", PublicKey: validKey})
			return err
		},
		privhelper.MethodEnableSSH: func(p privhelper.Provisioner) error {
			_, err := p.EnableSSH(ctx, privhelper.EnableSSHRequest{Enabled: true})
			return err
		},
		privhelper.MethodLockRoot: func(p privhelper.Provisioner) error {
			_, err := p.LockRoot(ctx, privhelper.LockRootRequest{})
			return err
		},
		privhelper.MethodAPUp: func(p privhelper.Provisioner) error {
			_, err := p.APUp(ctx, privhelper.APUpRequest{SSID: "waypoint-setup"})
			return err
		},
		privhelper.MethodAPDown: func(p privhelper.Provisioner) error {
			_, err := p.APDown(ctx, privhelper.APDownRequest{})
			return err
		},
		privhelper.MethodNetScan: func(p privhelper.Provisioner) error {
			_, err := p.NetScan(ctx, privhelper.NetScanRequest{})
			return err
		},
		privhelper.MethodNetJoin: func(p privhelper.Provisioner) error {
			_, err := p.NetJoin(ctx, privhelper.NetJoinRequest{SSID: "home-network"})
			return err
		},
		privhelper.MethodNetCheckpointCreate: func(p privhelper.Provisioner) error {
			_, err := p.NetCheckpointCreate(ctx, privhelper.NetCheckpointCreateRequest{TimeoutSeconds: 90})
			return err
		},
		privhelper.MethodNetCheckpointDestroy: func(p privhelper.Provisioner) error {
			_, err := p.NetCheckpointDestroy(ctx, privhelper.NetCheckpointDestroyRequest{Handle: "cp-1"})
			return err
		},
		privhelper.MethodNetCheckpointRollback: func(p privhelper.Provisioner) error {
			_, err := p.NetCheckpointRollback(ctx, privhelper.NetCheckpointRollbackRequest{Handle: "cp-1"})
			return err
		},
		privhelper.MethodListSudoUsers: func(p privhelper.Provisioner) error {
			_, err := p.ListSudoUsers(ctx, privhelper.ListSudoUsersRequest{})
			return err
		},
		privhelper.MethodRemoveUser: func(p privhelper.Provisioner) error {
			_, err := p.RemoveUser(ctx, privhelper.RemoveUserRequest{Username: "previous-owner"})
			return err
		},
		privhelper.MethodEnableModemUART: func(p privhelper.Provisioner) error {
			_, err := p.EnableModemUART(ctx, privhelper.EnableModemUARTRequest{})
			return err
		},
	}
}

// AssertCoversEveryMethod fails if allCalls has drifted from privhelper.Methods.
// It is what stops a twelfth method from being added to the interface and quietly
// skipped by the context sweep above.
func AssertCoversEveryMethod(t *testing.T) {
	t.Helper()
	calls := allCalls(context.Background())
	for _, m := range privhelper.Methods() {
		if _, ok := calls[m]; !ok {
			t.Errorf("method %q has no entry in the conformance sweep", m)
		}
	}
	if len(calls) != len(privhelper.Methods()) {
		t.Errorf("sweep covers %d methods, protocol declares %d", len(calls), len(privhelper.Methods()))
	}
}
