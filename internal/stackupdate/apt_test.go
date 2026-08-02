package stackupdate

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// The OS glue's job is command construction, and the Run/Systemctl seams exist so
// that is checkable without apt. These tests exercise Install, which is where the
// held-package contract lives: the image apt-mark holds every waypoint-* package,
// so an install that does not account for holds cannot move the stack at all.

// recordedCmd is one exec the glue attempted.
type recordedCmd struct {
	name string
	args []string
}

// fakeRunner scripts OSSystem.Run: it records every command and answers dpkg-query
// out of `installed`, so the re-hold step sees a realistic package state.
type fakeRunner struct {
	cmds      []recordedCmd
	installed map[string]string // package -> version, as dpkg-query reports it
	failOn    func(name string, args []string) bool
}

func (r *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.cmds = append(r.cmds, recordedCmd{name: name, args: append([]string(nil), args...)})
	if r.failOn != nil && r.failOn(name, args) {
		return []byte("E: something went wrong"), errors.New("exit status 100")
	}
	if name == "dpkg-query" {
		var b strings.Builder
		for _, p := range allPackages {
			if v := r.installed[p]; v != "" {
				fmt.Fprintf(&b, "%s %s\n", p, v)
			}
		}
		return []byte(b.String()), nil
	}
	return nil, nil
}

func (r *fakeRunner) find(name string) (recordedCmd, bool) {
	for _, c := range r.cmds {
		if c.name == name {
			return c, true
		}
	}
	return recordedCmd{}, false
}

func (r *fakeRunner) count(name string) int {
	n := 0
	for _, c := range r.cmds {
		if c.name == name {
			n++
		}
	}
	return n
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// TestInstallAllowsChangingHeldPackages is the regression test for the defect that
// made every stack update on an imaged node fail: apt refuses a -y transaction that
// touches a held package unless this flag is given, and the image holds every
// waypoint-* package. Observed on the bench Pi as
//
//	E: Held packages were changed and -y was used without --allow-change-held-packages
//
// which the engine surfaced as "reverted: apt install failed: exit status 100".
func TestInstallAllowsChangingHeldPackages(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd, ok := r.find("apt-get")
	if !ok {
		t.Fatal("no apt-get command was run")
	}
	if !hasArg(cmd.args, "--allow-change-held-packages") {
		t.Errorf("apt-get install must pass --allow-change-held-packages (every stack package is held); got %v", cmd.args)
	}
}

// TestInstallPinsExactVersions guards the D2 promise: the engine installs named
// versions and never lets apt widen the transaction into an upgrade.
func TestInstallPinsExactVersions(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-mmdvmhost": "0~gitNEW+wp1"}}
	s := &OSSystem{Run: r.run}

	err := s.Install(context.Background(), []PkgVer{
		{Package: "waypoint-mmdvmhost", Version: "0~gitNEW+wp1"},
		{Package: "waypoint-dmrgateway", Version: "0~gitOLD+wp1"},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd, _ := r.find("apt-get")
	for _, want := range []string{"install", "-y", "--allow-downgrades", "--no-install-recommends",
		"waypoint-mmdvmhost=0~gitNEW+wp1", "waypoint-dmrgateway=0~gitOLD+wp1"} {
		if !hasArg(cmd.args, want) {
			t.Errorf("missing %q in %v", want, cmd.args)
		}
	}
	for _, never := range []string{"upgrade", "dist-upgrade", "full-upgrade"} {
		if hasArg(cmd.args, never) {
			t.Errorf("apt-get must never be given %q; got %v", never, cmd.args)
		}
	}
}

// TestInstallRestoresHolds covers the sting in the flag above: apt CLEARS the hold
// on each package it was thereby allowed to change. Left alone, every successful
// update would strip the protection off one more package, so the hold is re-asserted
// after the install.
func TestInstallRestoresHolds(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{
		"waypoint-stack":      "0.2.0",
		"waypoint-dmrgateway": "0~git79edbc4+wp1",
	}}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd, ok := r.find("apt-mark")
	if !ok {
		t.Fatal("apt-mark hold was never run; the install would leave packages unheld")
	}
	if len(cmd.args) == 0 || cmd.args[0] != "hold" {
		t.Fatalf("expected `apt-mark hold …`, got %v", cmd.args)
	}
	for _, want := range []string{"waypoint-stack", "waypoint-dmrgateway"} {
		if !hasArg(cmd.args, want) {
			t.Errorf("installed package %q was not re-held; got %v", want, cmd.args)
		}
	}
}

// TestInstallHoldsDependencyItDidNotName is the waypoint-dapnetgateway case. The
// 0.2.0 metapackage added a dependency, so apt pulled in a package that appears in
// no PkgVer the caller could have passed — and a newly installed package arrives
// unheld. The re-hold works off what is installed, not off the transaction.
func TestInstallHoldsDependencyItDidNotName(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{
		"waypoint-stack":         "0.2.0",
		"waypoint-dapnetgateway": "0~git5527546+wp1", // pulled in as a new dependency
	}}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd, ok := r.find("apt-mark")
	if !ok {
		t.Fatal("apt-mark hold was never run")
	}
	if !hasArg(cmd.args, "waypoint-dapnetgateway") {
		t.Errorf("a dependency apt pulled in must still be held; got %v", cmd.args)
	}
}

// TestInstallDoesNotHoldUninstalledPackages keeps the re-hold honest: only packages
// dpkg reports as installed are named, so apt-mark is never asked to hold a package
// this node does not have.
func TestInstallDoesNotHoldUninstalledPackages(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cmd, ok := r.find("apt-mark")
	if !ok {
		t.Fatal("apt-mark hold was never run")
	}
	for _, absent := range []string{"waypoint-m17gateway", "waypoint-p25parrot"} {
		if hasArg(cmd.args, absent) {
			t.Errorf("%q is not installed and must not be held; got %v", absent, cmd.args)
		}
	}
}

// TestInstallSurvivesFailedRehold: the packages are already installed correctly by
// the time the hold is restored, so a failure there is logged loudly rather than
// failing the install — reverting a good stack over a bookkeeping step would be the
// worse outcome.
func TestInstallSurvivesFailedRehold(t *testing.T) {
	r := &fakeRunner{
		installed: map[string]string{"waypoint-stack": "0.2.0"},
		failOn:    func(name string, _ []string) bool { return name == "apt-mark" },
	}
	var logged []string
	s := &OSSystem{Run: r.run, Logf: func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err != nil {
		t.Fatalf("a failed re-hold must not fail the install, got %v", err)
	}
	if len(logged) == 0 {
		t.Fatal("a failed re-hold must be logged; it leaves the stack unprotected")
	}
	if !strings.Contains(logged[0], "unprotected") {
		t.Errorf("the warning should say what is at stake, got %q", logged[0])
	}
}

// TestInstallFailureSkipsRehold: a transaction that never happened has no holds to
// restore, and the error must reach the caller so the engine reverts.
func TestInstallFailureSkipsRehold(t *testing.T) {
	r := &fakeRunner{
		installed: map[string]string{"waypoint-stack": "0.1.0"},
		failOn:    func(name string, _ []string) bool { return name == "apt-get" },
	}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.2.0"}}); err == nil {
		t.Fatal("a failed apt-get install must return an error")
	}
	if r.count("apt-mark") != 0 {
		t.Errorf("no re-hold should follow a failed install; ran %d", r.count("apt-mark"))
	}
}

// --- CheckInstallable: the pre-flight's command construction (#221) ---

// TestCheckInstallableSimulatesAndChangesNothing: the pre-flight's whole value is
// that it answers "could this be installed?" without installing it. A missing
// --simulate would make Apply install the *previous* versions before installing
// the new ones.
func TestCheckInstallableSimulatesAndChangesNothing(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	s := &OSSystem{Run: r.run}

	err := s.CheckInstallable(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.1.0"}})
	if err != nil {
		t.Fatalf("CheckInstallable: %v", err)
	}
	cmd, ok := r.find("apt-get")
	if !ok {
		t.Fatal("no apt-get command was run")
	}
	if !hasArg(cmd.args, "--simulate") {
		t.Errorf("the pre-flight must not change the system; got %v", cmd.args)
	}
	if hasArg(cmd.args, "-y") {
		t.Errorf("a simulation has nothing to assume yes to; got %v", cmd.args)
	}
	// It asks about the exact pinned version — that is the question.
	if !hasArg(cmd.args, "waypoint-stack=0.1.0") {
		t.Errorf("the pre-flight must name the exact version; got %v", cmd.args)
	}
	// Simulating changes nothing, so there are no holds to restore.
	if r.count("apt-mark") != 0 {
		t.Errorf("a simulation must not touch hold state; ran apt-mark %d time(s)", r.count("apt-mark"))
	}
}

// TestCheckInstallableMatchesInstallResolution: a simulation run under different
// constraints than the real install would prove the wrong thing — it could pass
// while the install it is vouching for fails, which is exactly the guarantee #221
// needs. Every resolver-affecting flag must therefore match Install's.
func TestCheckInstallableMatchesInstallResolution(t *testing.T) {
	pkgs := []PkgVer{{Package: "waypoint-stack", Version: "0.1.0"}}

	ri := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.1.0"}}
	if err := (&OSSystem{Run: ri.run}).Install(context.Background(), pkgs); err != nil {
		t.Fatalf("Install: %v", err)
	}
	rc := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	if err := (&OSSystem{Run: rc.run}).CheckInstallable(context.Background(), pkgs); err != nil {
		t.Fatalf("CheckInstallable: %v", err)
	}
	install, _ := ri.find("apt-get")
	check, _ := rc.find("apt-get")

	// --allow-downgrades: the revert is a downgrade, so a pre-flight without it
	// would reject a target the revert could actually install.
	// --allow-change-held-packages: every stack package is held (#220).
	for _, flag := range []string{"--allow-downgrades", "--allow-change-held-packages", "--no-install-recommends"} {
		if !hasArg(install.args, flag) {
			t.Errorf("Install lost %q — the pre-flight parity check is now meaningless; got %v", flag, install.args)
		}
		if !hasArg(check.args, flag) {
			t.Errorf("the pre-flight must resolve under Install's %q; got %v", flag, check.args)
		}
	}
}

// TestCheckInstallableReportsAptsReason: the refusal an operator reads is only
// actionable if it carries apt's own words for what is missing.
func TestCheckInstallableReportsAptsReason(t *testing.T) {
	r := &fakeRunner{
		installed: map[string]string{"waypoint-stack": "0.2.0"},
		failOn:    func(name string, _ []string) bool { return name == "apt-get" },
	}
	s := &OSSystem{Run: r.run}

	err := s.CheckInstallable(context.Background(), []PkgVer{{Package: "waypoint-stack", Version: "0.1.0"}})
	if err == nil {
		t.Fatal("an unresolvable version must be reported as an error")
	}
	if !strings.Contains(err.Error(), "E: something went wrong") {
		t.Errorf("the error should carry apt's output, got %q", err)
	}
}

// TestCheckInstallableEmptyRunsNothing — no revert targets (every planned package is
// a fresh install) is trivially installable, and must not become an apt call whose
// "install nothing" result could fail for unrelated reasons.
func TestCheckInstallableEmptyRunsNothing(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	s := &OSSystem{Run: r.run}

	if err := s.CheckInstallable(context.Background(), nil); err != nil {
		t.Fatalf("CheckInstallable(nil): %v", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("expected no commands, got %v", r.cmds)
	}
}

// TestInstallEmptyRunsNothing — an empty target set is a no-op, not an apt call that
// would resolve to "install nothing" and touch the hold state anyway.
func TestInstallEmptyRunsNothing(t *testing.T) {
	r := &fakeRunner{installed: map[string]string{"waypoint-stack": "0.2.0"}}
	s := &OSSystem{Run: r.run}

	if err := s.Install(context.Background(), nil); err != nil {
		t.Fatalf("Install(nil): %v", err)
	}
	if len(r.cmds) != 0 {
		t.Errorf("expected no commands, got %v", r.cmds)
	}
}
