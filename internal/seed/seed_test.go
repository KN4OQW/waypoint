package seed_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/seed"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

var testKey = privtest.SSHPublicKey(5)

// documentedTOML is the file docs/provisioning.md tells an operator to write.
// Testing against the documented form rather than a minimal one is the point:
// documentation that has drifted from the parser is worse than none.
func documentedTOML(t *testing.T) string {
	t.Helper()
	return `config_version = 1

[system]
hostname = "hs-shack"

[user]
name = "rescue"
password = "$6$rounds=5000$abcdefgh$0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ab"
password_encrypted = true

[ssh]
enabled = true
password_authentication = false
authorized_keys = [
  "` + testKey + `",
]

[wlan]
ssid = "home-network"
password = "correct horse battery"
country = "US"
`
}

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type env struct {
	w      *wizard.Wizard
	fake   *privtest.Fake
	marker string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	e := &env{fake: privtest.NewFake(), marker: filepath.Join(dir, "provisioned")}
	e.w = &wizard.Wizard{
		Prov:         e.fake,
		MarkerPath:   e.marker,
		ProgressPath: filepath.Join(dir, "progress.json"),
		Claimed:      func() bool { return false },
		Logf:         t.Logf,
	}
	return e
}

// Property 1: the documented TOML provisions a node end to end.
func TestDocumentedTOMLProvisions(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", documentedTOML(t))

	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)

	res, err := seed.Apply(context.Background(), e.w, f, path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Provisioned {
		t.Fatal("the seed did not provision the node")
	}
	if !e.w.Provisioned() {
		t.Fatal("the provisioned marker was not written")
	}

	// Everything the file asked for reached the system.
	if e.fake.Hostname != "hs-shack" {
		t.Errorf("hostname = %q", e.fake.Hostname)
	}
	u, ok := e.fake.User("rescue")
	if !ok {
		t.Fatal("the recovery account was not created")
	}
	if !u.Sudo {
		t.Error("the recovery account has no sudo")
	}
	if len(u.Keys) != 1 {
		t.Errorf("%d keys installed, want 1", len(u.Keys))
	}
	if u.PasswordLocked {
		t.Error("an account given a password hash is reported as locked")
	}
	if !e.fake.RootLocked {
		t.Error("root is not locked")
	}
	if e.fake.PasswordAuth {
		t.Error("password authentication is still on despite the file asking for no")
	}
	if e.fake.Joined == nil || e.fake.Joined.SSID != "home-network" {
		t.Errorf("wi-fi join = %+v", e.fake.Joined)
	}
	if !res.WiFiJoined {
		t.Error("the result does not record the join")
	}

	// The marker records what the file chose.
	st, err := provision.Load(e.marker)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "hs-shack" || st.RecoveryUser != "rescue" || !st.RootLocked {
		t.Errorf("marker = %+v", st)
	}
}

// Property 2: the seed does not claim the node.
//
// Provisioning says what the box is; claiming says who administers it. Handing
// the second to whoever wrote a file on the card would make the claim gate
// decorative, so the operator still meets the claim screen.
func TestSeedDoesNotClaim(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", documentedTOML(t))
	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	if _, err := seed.Apply(context.Background(), e.w, f, path); err != nil {
		t.Fatal(err)
	}

	v := e.w.State()
	if v.Mode != "claim" || v.Next != wizard.StepClaim {
		t.Errorf("state after a seed run = %+v, want the claim gate", v)
	}
}

// Property 3: no seed file means the wizard, unchanged. This is the fallback the
// whole default path depends on.
func TestAbsentSeedFallsBackToTheWizard(t *testing.T) {
	dir := t.TempDir()
	if _, err := seed.Find([]string{filepath.Join(dir, "waypoint.toml"),
		filepath.Join(dir, "custom.toml")}); err != seed.ErrAbsent {
		t.Fatalf("Find with no file = %v, want ErrAbsent", err)
	}

	e := newEnv(t)
	if e.w.Provisioned() {
		t.Error("the node reads as provisioned with no seed file")
	}
	if got := e.w.Next(); got != wizard.StepHostname {
		t.Errorf("the wizard starts at %q, want %q", got, wizard.StepHostname)
	}
}

// Property 4: the search order is honoured, so a Waypoint file wins over a
// custom.toml written for something else.
func TestFindOrder(t *testing.T) {
	dir := t.TempDir()
	custom := write(t, dir, "custom.toml", documentedTOML(t))
	paths := []string{filepath.Join(dir, "waypoint.toml"), custom}

	got, err := seed.Find(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got != custom {
		t.Errorf("Find = %q, want the custom.toml", got)
	}

	waypointFile := write(t, dir, "waypoint.toml", documentedTOML(t))
	got, err = seed.Find(paths)
	if err != nil {
		t.Fatal(err)
	}
	if got != waypointFile {
		t.Errorf("Find = %q, want waypoint.toml to win", got)
	}
}

// Property 5: an unusable file is refused whole rather than half-applied.
//
// A seed file is written by somebody who is not watching the boot. A partial
// application — hostname set, no account, root unlocked — leaves a node in a state
// nobody chose and nobody is present to notice. Refusing means it falls back to
// the wizard, which is visible and recoverable.
func TestInvalidFilesAreRefusedWhole(t *testing.T) {
	cases := map[string]struct{ body, wants string }{
		"no hostname": {`[user]
name = "rescue"
password = "correct horse"`, "hostname is required"},

		"default hostname": {`[system]
hostname = "raspberrypi"
[user]
name = "rescue"
password = "correct horse"`, "default name"},

		"no user": {`[system]
hostname = "hs-shack"`, "name is required"},

		"root as the user": {`[system]
hostname = "hs-shack"
[user]
name = "root"
password = "correct horse"`, "must not be root"},

		"no credential at all": {`[system]
hostname = "hs-shack"
[user]
name = "rescue"`, "neither a password nor an SSH key"},

		"password hash that is not one": {`[system]
hostname = "hs-shack"
[user]
name = "rescue"
password = "not-a-hash"
password_encrypted = true`, "crypt(3)"},

		"chpasswd injection in the password": {`[system]
hostname = "hs-shack"
[user]
name = "rescue"
password = "hunter22\nroot:owned"`, "line break"},

		"bad ssh key": {`[system]
hostname = "hs-shack"
[user]
name = "rescue"
authorized_keys_placeholder = 1
[ssh]
authorized_keys = ["not-a-key"]`, "authorized_keys[0]"},

		"wlan password with no ssid": {`[system]
hostname = "hs-shack"
[user]
name = "rescue"
password = "correct horse"
[wlan]
password = "correct horse battery"`, "ssid is not"},

		"future config version": {`config_version = 99
[system]
hostname = "hs-shack"
[user]
name = "rescue"
password = "correct horse"`, "not supported"},

		"not toml at all": {`{"hostname": "hs-shack"}`, "not valid TOML"},
	}

	dir := t.TempDir()
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			path := write(t, dir, strings.ReplaceAll(name, " ", "-")+".toml", tc.body)
			_, err := seed.Load(path)
			if err == nil {
				t.Fatal("an unusable file was accepted")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wants)
			}
			if !strings.Contains(err.Error(), filepath.Base(path)) {
				t.Errorf("error does not name the file: %q", err)
			}
		})
	}
}

// Property 6: a stale seed file does not re-provision a node that is already set
// up. A card reflashed onto a node that has been through setup, or a file that
// could not be deleted, must not rename a live node out from under its operator.
func TestStaleSeedIsIgnoredOnAProvisionedNode(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", documentedTOML(t))
	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	// Somebody already set this node up, by hand, as something else.
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if err := provision.Save(e.marker, provision.State{
		ProvisionedAt: now, UpdatedAt: now, HostnameSet: true,
		Hostname: "someone-elses-node", RecoveryUser: "theirs", RootLocked: true,
	}); err != nil {
		t.Fatal(err)
	}

	res, err := seed.Apply(context.Background(), e.w, f, path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Provisioned {
		t.Error("the seed re-provisioned an already-provisioned node")
	}
	if e.fake.Hostname != privtest.DefaultHostname {
		t.Errorf("the stale seed renamed a live node to %q", e.fake.Hostname)
	}
	st, _ := provision.Load(e.marker)
	if st.Hostname != "someone-elses-node" {
		t.Errorf("the marker was overwritten: %+v", st)
	}
}

// Property 7: several keys all land. A file listing a laptop and a phone should
// end with both, not with whichever was written first.
func TestAllKeysAreInstalled(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", `[system]
hostname = "hs-shack"
[user]
name = "rescue"
[ssh]
authorized_keys = [
  "`+privtest.SSHPublicKey(1)+`",
  "`+privtest.SSHPublicKey(2)+`",
  "`+privtest.SSHPublicKey(3)+`",
]`)
	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	if _, err := seed.Apply(context.Background(), e.w, f, path); err != nil {
		t.Fatal(err)
	}
	u, _ := e.fake.User("rescue")
	if len(u.Keys) != 3 {
		t.Errorf("%d keys installed, want 3", len(u.Keys))
	}
	// A key-only account gets password authentication turned off without being
	// asked: leaving it on for an account that cannot use a password is an open
	// door with nothing behind it.
	if e.fake.PasswordAuth {
		t.Error("password authentication was left on for a key-only account")
	}
}

// Property 8: a failed Wi-Fi join does not fail provisioning.
//
// Refusing to finish over a mistyped passphrase would leave the node
// unprovisioned — which on a node with no other network means it raises the
// access point and waits for somebody who is not there. Finishing and letting
// netwatch raise the AP later is the recoverable outcome.
func TestWiFiFailureDoesNotBlockProvisioning(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", documentedTOML(t))
	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.fake.Errs[privhelper.MethodNetJoin] = privhelper.Errorf(privhelper.CodeConflict, "authentication failed")

	res, err := seed.Apply(context.Background(), e.w, f, path)
	if err != nil {
		t.Fatalf("a failed join failed the whole seed: %v", err)
	}
	if !res.Provisioned {
		t.Error("the node was not provisioned")
	}
	if res.WiFiJoined || res.WiFiError == nil {
		t.Errorf("the result does not record the failed join: %+v", res)
	}
	if !e.fake.RootLocked {
		t.Error("root was not locked")
	}
}

// Property 9: the file is removed once it has been used.
//
// It holds a Wi-Fi passphrase and, unless the operator used a hash, an account
// password — on a FAT partition anybody who picks up the card can read.
func TestConsumeRemovesTheFile(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", documentedTOML(t))
	if err := seed.Consume(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("the seed file survived")
	}
	// Removing it twice is not an error: the second boot must not fail over it.
	if err := seed.Consume(path); err != nil {
		t.Errorf("a second Consume: %v", err)
	}
}

// Property 10: Describe carries nothing secret. It goes in the boot log, which is
// journald, which is readable.
func TestDescribeRedacts(t *testing.T) {
	dir := t.TempDir()
	path := write(t, dir, "waypoint.toml", `[system]
hostname = "hs-shack"
[user]
name = "rescue"
password = "correct horse battery"
[wlan]
ssid = "home-network"
password = "wifi-secret-passphrase"`)
	f, err := seed.Load(path)
	if err != nil {
		t.Fatal(err)
	}

	got := f.Describe()
	for _, secret := range []string{"correct horse battery", "wifi-secret-passphrase"} {
		if strings.Contains(got, secret) {
			t.Errorf("Describe leaked %q: %s", secret, got)
		}
	}
	for _, want := range []string{"hs-shack", "rescue", "home-network", "password"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe does not mention %q: %s", want, got)
		}
	}
}
