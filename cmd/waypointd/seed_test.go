package main

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// seedTOML is the documented file, parameterised so a test can vary one field.
func seedTOML(hostname, user, key string) string {
	return `config_version = 1

[system]
hostname = "` + hostname + `"

[user]
name = "` + user + `"

[ssh]
authorized_keys = [ "` + key + `" ]
`
}

// TestSeedProvisionsWithoutRaisingAnAP is the acceptance test: a documented TOML
// provisions the unit, and no access point is raised.
//
// It goes through the daemon's own startup path rather than calling the seed
// package directly, because "no AP raised" is a property of the ordering in
// initSetup/initSetupAP, not of the seed itself.
func TestSeedProvisionsWithoutRaisingAnAP(t *testing.T) {
	e := newSetupEnv(t)
	boot := t.TempDir()
	seedPath := filepath.Join(boot, "waypoint.toml")
	if err := os.WriteFile(seedPath,
		[]byte(seedTOML(e2eHostname, e2eUser, privtest.SSHPublicKey(4))), 0o644); err != nil {
		t.Fatal(err)
	}

	e.s.applySeed(setupOptions{SeedPaths: []string{seedPath}})

	// Provisioned, without anybody touching the wizard.
	if !e.s.wiz.Provisioned() {
		t.Fatal("the seed did not provision the node")
	}
	if e.fake.Hostname != e2eHostname {
		t.Errorf("hostname = %q", e.fake.Hostname)
	}
	if !e.fake.RootLocked {
		t.Error("root is not locked")
	}
	u, ok := e.fake.User(e2eUser)
	if !ok || !u.Sudo || len(u.Keys) != 1 {
		t.Errorf("recovery account = %+v (present %v)", u, ok)
	}

	// The file is gone: it carried credentials onto a FAT partition anyone with
	// the card can read.
	if _, err := os.Stat(seedPath); !os.IsNotExist(err) {
		t.Error("the seed file survived, credentials and all")
	}

	// No access point was ever asked for. This is the criterion.
	if n := len(e.fake.CallsTo("ap_up")); n != 0 {
		t.Errorf("APUp was called %d times on a seeded node", n)
	}
	// And the AP init agrees, because the node now reads as provisioned.
	e.s.initSetupAP(t.Context(), apOptions{Enabled: true, CPUInfo: "/nonexistent", PSKFile: "/nonexistent"})
	if n := len(e.fake.CallsTo("ap_up")); n != 0 {
		t.Errorf("APUp was called %d times after initSetupAP on a seeded node", n)
	}

	// The claim is still required — the seed says what the box is, not who runs it.
	rec := e.do(t, "GET", "/api/config", nil, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/config = %d on a seeded, unclaimed node, want 403", rec.Code)
	}
	if v := e.state(t); v.Mode != "claim" {
		t.Errorf("mode = %q after seeding, want claim", v.Mode)
	}
	rec = e.do(t, "POST", "/api/claim",
		map[string]string{"username": "admin", "password": "a-long-enough-password"}, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim on a seeded node = %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAbsentSeedFallsBackToTheWizard: with no file, nothing changes — the node
// serves the wizard and raises the access point exactly as before.
func TestAbsentSeedLeavesTheWizardInCharge(t *testing.T) {
	e := newSetupEnv(t)
	boot := t.TempDir()

	e.s.applySeed(setupOptions{SeedPaths: []string{filepath.Join(boot, "waypoint.toml")}})

	if e.s.wiz.Provisioned() {
		t.Fatal("the node reads as provisioned with no seed file")
	}
	if got := e.s.wiz.Next(); got != wizard.StepHostname {
		t.Errorf("the wizard starts at %q, want %q", got, wizard.StepHostname)
	}
	if rec := e.do(t, "GET", "/api/config", nil, nil); rec.Code != http.StatusForbidden {
		t.Errorf("GET /api/config = %d, want 403 while unprovisioned", rec.Code)
	}
	if v := e.state(t); v.Mode != "setup" {
		t.Errorf("mode = %q, want setup", v.Mode)
	}
}

// TestBadSeedFallsBackToTheWizard: a file the daemon cannot use must not stop it
// coming up.
//
// The operator who wrote it is not watching this boot. A node that refuses to
// start because of a typo in a TOML is a node they cannot reach to fix the typo;
// one that quietly serves the wizard is one they can.
func TestBadSeedFallsBackToTheWizard(t *testing.T) {
	for name, body := range map[string]string{
		"not toml":         `{"hostname":"hs-shack"}`,
		"default hostname": seedTOML("raspberrypi", e2eUser, privtest.SSHPublicKey(4)),
		"root as the user": seedTOML(e2eHostname, "root", privtest.SSHPublicKey(4)),
		"bad key":          seedTOML(e2eHostname, e2eUser, "not-a-key"),
	} {
		t.Run(name, func(t *testing.T) {
			e := newSetupEnv(t)
			seedPath := filepath.Join(t.TempDir(), "waypoint.toml")
			if err := os.WriteFile(seedPath, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}

			e.s.applySeed(setupOptions{SeedPaths: []string{seedPath}})

			if e.s.wiz.Provisioned() {
				t.Error("an unusable seed provisioned the node")
			}
			if got := e.s.wiz.Next(); got != wizard.StepHostname {
				t.Errorf("the wizard starts at %q, want %q", got, wizard.StepHostname)
			}
			// Nothing was half-applied.
			if e.fake.Hostname != privtest.DefaultHostname {
				t.Errorf("a refused seed still renamed the node to %q", e.fake.Hostname)
			}
			if len(e.fake.Users) != 0 {
				t.Error("a refused seed still created an account")
			}
			// And the file is left alone, so the operator can see what they wrote.
			if _, err := os.Stat(seedPath); err != nil {
				t.Error("a refused seed file was deleted; the operator cannot see their mistake")
			}
		})
	}
}

// TestSeedCanBeDisabled: an empty -provision-seed turns the fast path off, for a
// deployment that does not want a file on the card to be able to provision.
func TestSeedCanBeDisabled(t *testing.T) {
	e := newSetupEnv(t)
	seedPath := filepath.Join(t.TempDir(), "waypoint.toml")
	if err := os.WriteFile(seedPath,
		[]byte(seedTOML(e2eHostname, e2eUser, privtest.SSHPublicKey(4))), 0o644); err != nil {
		t.Fatal(err)
	}

	e.s.applySeed(setupOptions{SeedPaths: []string{""}})

	if e.s.wiz.Provisioned() {
		t.Error("the seed ran despite being disabled")
	}
	if _, err := os.Stat(seedPath); err != nil {
		t.Error("a disabled seed still consumed the file")
	}
}
