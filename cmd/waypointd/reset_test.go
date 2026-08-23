package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/auth"
	"github.com/KN4OQW/waypoint/internal/provision"
	"github.com/KN4OQW/waypoint/internal/store"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// resetEnv is a claimed, provisioned node: the state both reset paths act on.
type resetEnv struct {
	as    *auth.Store
	store *store.Store
	paths resetPaths
	dir   string
}

func newResetEnv(t *testing.T) *resetEnv {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	as, err := auth.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := auth.HashPassword("a-long-enough-password")
	if err != nil {
		t.Fatal(err)
	}
	if err := as.Claim("kn4oqw", rec, time.Now()); err != nil {
		t.Fatal(err)
	}
	// The session belongs to the account that was just claimed. A session with no
	// owner is not a state the daemon can produce any more, and writing one here
	// would test a shape that cannot occur.
	acct, ok, err := as.AccountByUsername("kn4oqw")
	if err != nil || !ok {
		t.Fatalf("reading back the claimed account: ok=%v err=%v", ok, err)
	}
	if err := as.CreateSession("hash-1", acct.ID, time.Now(), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	e := &resetEnv{as: as, store: st, dir: dir, paths: resetPaths{
		Marker:   filepath.Join(dir, "provisioned"),
		Progress: filepath.Join(dir, "setup-progress.json"),
	}}
	e.provision(t)
	return e
}

// provision puts the node in the state a completed setup leaves behind.
func (e *resetEnv) provision(t *testing.T) {
	t.Helper()
	now := time.Now().UTC()
	if err := provision.Save(e.paths.Marker, provision.State{
		ProvisionedAt: now, UpdatedAt: now,
		HostnameSet: true, Hostname: "hs-shack", RecoveryUser: "rescue", RootLocked: true,
	}); err != nil {
		t.Fatal(err)
	}
	m, err := provision.NewMirror(e.store)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Set(true); err != nil {
		t.Fatal(err)
	}
	// A stale progress file, as an interrupted setup would leave.
	if err := os.WriteFile(e.paths.Progress, []byte(`{"schema_version":1,"hostname":"hs-shack"}`), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (e *resetEnv) mirrored(t *testing.T) bool {
	t.Helper()
	m, err := provision.NewMirror(e.store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.Get()
	if err != nil {
		t.Fatal(err)
	}
	return got
}

// Property 1: the claim-only reset hands the dashboard to a new administrator and
// touches nothing else.
//
// The node keeps its identity: still provisioned, still named, still holding the
// recovery account and the locked root. Whoever ran this already had a shell —
// the node's identity was never in question, only who administers it.
func TestResetClaimLeavesProvisioningAlone(t *testing.T) {
	e := newResetEnv(t)

	res, err := resetClaim(e.as)
	if err != nil {
		t.Fatal(err)
	}
	if !res.WasClaimed || res.AdminUser != "kn4oqw" || res.Sessions != 1 {
		t.Errorf("result = %+v", res)
	}
	if res.Full {
		t.Error("a claim reset reported itself as full")
	}

	if claimed, _ := e.as.IsClaimed(); claimed {
		t.Error("still claimed")
	}
	if n, _ := e.as.SessionCount(); n != 0 {
		t.Errorf("%d sessions survived", n)
	}

	// The provisioning half is untouched, which is the whole point.
	if !provision.IsProvisioned(e.paths.Marker) {
		t.Error("a claim-only reset cleared the provisioned marker")
	}
	st, err := provision.Load(e.paths.Marker)
	if err != nil {
		t.Fatal(err)
	}
	if st.Hostname != "hs-shack" || st.RecoveryUser != "rescue" || !st.RootLocked {
		t.Errorf("the marker was altered: %+v", st)
	}
	if !e.mirrored(t) {
		t.Error("a claim-only reset cleared the config.db mirror")
	}
	if _, err := os.Stat(e.paths.Progress); err != nil {
		t.Error("a claim-only reset removed the setup progress file")
	}
}

// Property 2: the full reset also forgets that the node was ever set up, so the
// next boot serves the wizard and raises the setup access point.
func TestResetFullClearsProvisioning(t *testing.T) {
	e := newResetEnv(t)

	res, err := resetFull(e.as, e.store, e.paths)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Full || !res.WasProvisioned || !res.WasClaimed {
		t.Errorf("result = %+v", res)
	}

	if claimed, _ := e.as.IsClaimed(); claimed {
		t.Error("still claimed")
	}
	if provision.IsProvisioned(e.paths.Marker) {
		t.Error("the node still reads as provisioned")
	}
	if e.mirrored(t) {
		t.Error("the config.db mirror still says provisioned")
	}
	// Progress goes too: resuming into the middle of a setup describing an account
	// and a hostname this reset just disowned would be worse than starting over.
	if _, err := os.Stat(e.paths.Progress); !os.IsNotExist(err) {
		t.Error("the setup progress file survived a full reset")
	}

	// And the node really would run the wizard again.
	w := &wizard.Wizard{MarkerPath: e.paths.Marker, ProgressPath: e.paths.Progress,
		Claimed: func() bool { return false }}
	if w.Provisioned() {
		t.Fatal("the wizard still reports the node as provisioned")
	}
	if got := w.Next(); got != wizard.StepHostname {
		t.Errorf("the wizard resumes at %q, want %q — a full reset must start over", got, wizard.StepHostname)
	}
}

// Property 3: both resets are idempotent. An operator who runs one twice, or a
// boot marker that could not be deleted and re-triggers, must not fail.
func TestResetsAreIdempotent(t *testing.T) {
	e := newResetEnv(t)

	for i := 0; i < 3; i++ {
		if _, err := resetFull(e.as, e.store, e.paths); err != nil {
			t.Fatalf("full reset run %d: %v", i+1, err)
		}
	}
	for i := 0; i < 3; i++ {
		if _, err := resetClaim(e.as); err != nil {
			t.Fatalf("claim reset run %d: %v", i+1, err)
		}
	}
	if provision.IsProvisioned(e.paths.Marker) {
		t.Error("the node reads as provisioned after repeated resets")
	}
}

// Property 4: a full reset on a node that was never set up reports honestly
// rather than claiming to have cleared something.
func TestResetFullOnAFreshNode(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	as, err := auth.NewStore(st)
	if err != nil {
		t.Fatal(err)
	}

	res, err := resetFull(as, st, resetPaths{
		Marker: filepath.Join(dir, "provisioned"), Progress: filepath.Join(dir, "progress.json")})
	if err != nil {
		t.Fatal(err)
	}
	if res.WasClaimed || res.WasProvisioned || res.AdminUser != "" {
		t.Errorf("result = %+v, want nothing cleared", res)
	}
}

// Property 5: the descriptions tell the operator which depth ran and what was
// left alone. Someone who reaches for the wrong one needs to see it from the
// output, not from the node's behaviour a reboot later.
func TestResetDescriptions(t *testing.T) {
	claim := resetResult{WasClaimed: true, AdminUser: "kn4oqw", Sessions: 2}.describe("/tmp/config.db")
	if !strings.Contains(claim, "claim mode") || strings.Contains(claim, "--full") {
		t.Errorf("claim description = %q", claim)
	}
	if !strings.Contains(claim, "kn4oqw") || !strings.Contains(claim, "2 session") {
		t.Errorf("the claim description does not say what was cleared: %q", claim)
	}

	full := resetResult{WasClaimed: true, AdminUser: "kn4oqw", Sessions: 2,
		WasProvisioned: true, Full: true}.describe("/tmp/config.db")
	for _, want := range []string{"--full", "provisioned marker", "setup wizard", "access point"} {
		if !strings.Contains(full, want) {
			t.Errorf("the full description does not mention %q: %q", want, full)
		}
	}
	// And it is explicit about what it did NOT undo, because an operator expecting
	// their hostname back would otherwise be surprised.
	for _, want := range []string{"hostname", "recovery account", "root"} {
		if !strings.Contains(full, want) {
			t.Errorf("the full description does not say %q is left alone: %q", want, full)
		}
	}
}

// Property 6: the boot marker performs the full reset, not the claim-only one.
//
// This is the split that matters. Whoever holds the SD card is almost always
// someone who cannot get in at all — a forgotten password on a node with root
// locked, or a second-hand board carrying somebody else's setup. Handing them a
// dashboard on a node still called someone else's hostname, with someone else's
// recovery account and SSH key, would not be a recovery.
func TestBootMarkerTriggersAFullReset(t *testing.T) {
	e := newResetEnv(t)

	marker := filepath.Join(e.dir, "waypoint-reset")
	if err := os.WriteFile(marker, []byte("reset"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs syncBuffer
	did, err := checkResetMarker(e.as, e.store, []string{marker}, e.paths, logs.logf)
	if err != nil {
		t.Fatal(err)
	}
	if !did {
		t.Fatal("the marker was present but no reset ran")
	}

	if claimed, _ := e.as.IsClaimed(); claimed {
		t.Error("still claimed after the marker reset")
	}
	if provision.IsProvisioned(e.paths.Marker) {
		t.Error("the boot marker performed a claim-only reset; it must re-provision")
	}
	if e.mirrored(t) {
		t.Error("the config.db mirror still says provisioned")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("the boot marker was not deleted, so it would re-trigger every boot")
	}
	if !strings.Contains(logs.String(), "SECURITY") {
		t.Errorf("the reset was not logged loudly:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "unprovisioned") {
		t.Errorf("the log does not say the node was un-provisioned:\n%s", logs.String())
	}
}
