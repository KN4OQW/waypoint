package stackupdate

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// fakeSystem records the engine's side effects and scripts the health gate, so the
// state machine and the revert decision are tested without apt or systemd.
type fakeSystem struct {
	installed    map[string]string
	healthSeq    []bool // scripted Healthy results; the last entry repeats
	healthDetail string
	onHealthy    func(units []string) // observes the unit set the gate actually probes

	// pool models what the apt repo actually carries, as "name=version" keys. It
	// is the thing #221 was about: the engine assumed the previous versions were
	// still here and they were not. A nil pool means "everything is available",
	// so the tests that predate the pre-flight keep their original meaning.
	pool map[string]bool

	installCalls [][]PkgVer
	checkCalls   [][]PkgVer
	stopCalls    [][]string
	startCalls   [][]string
	history      [][]HistoryRow

	installErr error
	stopErr    error
	startErr   error
}

// has reports whether the modelled pool carries this exact version.
func (f *fakeSystem) has(p PkgVer) bool {
	return f.pool == nil || f.pool[p.Package+"="+p.Version]
}

func (f *fakeSystem) CheckInstallable(_ context.Context, pkgs []PkgVer) error {
	f.checkCalls = append(f.checkCalls, append([]PkgVer(nil), pkgs...))
	for _, p := range pkgs {
		if !f.has(p) {
			// Mirrors apt's own wording, which is what an operator sees in the log.
			return fmt.Errorf("E: Version '%s' for '%s' was not found", p.Version, p.Package)
		}
	}
	return nil
}

func (f *fakeSystem) InstalledVersions(_ context.Context, pkgs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pkgs {
		out[p] = f.installed[p]
	}
	return out, nil
}

func (f *fakeSystem) Install(_ context.Context, pkgs []PkgVer) error {
	f.installCalls = append(f.installCalls, pkgs)
	if f.installErr != nil {
		return f.installErr
	}
	// A version the pool does not carry cannot be installed — the same failure the
	// revert path hit on the bench Pi, so an opted-in run reaches it for real.
	for _, p := range pkgs {
		if !f.has(p) {
			return fmt.Errorf("E: Version '%s' for '%s' was not found", p.Version, p.Package)
		}
	}
	for _, p := range pkgs { // model the install so InstalledVersions reflects it
		f.installed[p.Package] = p.Version
	}
	return nil
}

func (f *fakeSystem) StopServices(_ context.Context, units []string) error {
	f.stopCalls = append(f.stopCalls, units)
	return f.stopErr
}

func (f *fakeSystem) StartServices(_ context.Context, units []string) error {
	f.startCalls = append(f.startCalls, units)
	return f.startErr
}

func (f *fakeSystem) Healthy(_ context.Context, units []string) (bool, string) {
	if f.onHealthy != nil {
		f.onHealthy(units)
	}
	if len(f.healthSeq) == 0 {
		return true, ""
	}
	v := f.healthSeq[0]
	if len(f.healthSeq) > 1 {
		f.healthSeq = f.healthSeq[1:]
	}
	if v {
		return true, ""
	}
	return false, f.healthDetail
}

func (f *fakeSystem) RecordHistory(rows []HistoryRow) error {
	cp := append([]HistoryRow(nil), rows...)
	f.history = append(f.history, cp)
	return nil
}

func fastTimings() Timings {
	return Timings{SettleDelay: 0, PollInterval: 0, MaxPolls: 10, ConfirmChecks: 2}
}

// --- ParseUpgradable (the apt-output parsing) ---

func TestParseUpgradable(t *testing.T) {
	out := `Listing...
waypoint-mmdvmhost/bookworm 0~gitNEW+wp1 armhf [upgradable from: 0~gitOLD+wp1]
waypoint-dmrgateway/bookworm 0~gitB+wp2 armhf [upgradable from: 0~gitB+wp1]
libc6/stable 2.36-9 armhf [upgradable from: 2.36-8]
waypoint-p25parrot/bookworm 0~gitP+wp1 all [upgradable from: 0~gitP+wp0]
waypoint-mmdvmhost/bookworm 0~gitNEW+wp1 armhf
`
	got := ParseUpgradable(out)
	want := []Update{
		{Package: "waypoint-mmdvmhost", From: "0~gitOLD+wp1", To: "0~gitNEW+wp1"},
		{Package: "waypoint-dmrgateway", From: "0~gitB+wp1", To: "0~gitB+wp2"},
		{Package: "waypoint-p25parrot", From: "0~gitP+wp0", To: "0~gitP+wp1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ParseUpgradable:\n got %#v\nwant %#v", got, want)
	}
}

func TestParseUpgradableEmpty(t *testing.T) {
	if got := ParseUpgradable("Listing...\n"); len(got) != 0 {
		t.Fatalf("expected no updates, got %#v", got)
	}
}

// --- PlanFrom: unit mapping, dedup, stable order, availability ---

func TestPlanFromUnitsAndOrder(t *testing.T) {
	// Deliberately unsorted; a parrot and the metapackage carry no unit.
	updates := []Update{
		{Package: "waypoint-p25parrot", From: "1", To: "2"}, // no unit
		{Package: "waypoint-mmdvmhost", From: "1", To: "2"}, // waypoint-mmdvm.service
		{Package: "waypoint-stack", From: "0.1.0", To: "0.2.0"},
		{Package: "waypoint-dmrgateway", From: "1", To: "2"},
	}
	p := PlanFrom(updates)
	if !p.Available {
		t.Fatal("expected Available")
	}
	// Packages sorted for determinism.
	wantNames := []string{"waypoint-dmrgateway", "waypoint-mmdvmhost", "waypoint-p25parrot", "waypoint-stack"}
	if !reflect.DeepEqual(p.PackageNames(), wantNames) {
		t.Fatalf("package order: got %v want %v", p.PackageNames(), wantNames)
	}
	wantUnits := []string{"waypoint-dmrgateway.service", "waypoint-mmdvm.service"}
	if !reflect.DeepEqual(p.Units, wantUnits) {
		t.Fatalf("units: got %v want %v", p.Units, wantUnits)
	}
}

func TestPlanFromEmptyIsUpToDate(t *testing.T) {
	if p := PlanFrom(nil); p.Available {
		t.Fatalf("empty plan should not be available: %+v", p)
	}
}

// --- Apply: happy path confirms and commits ---

func TestApplyConfirms(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{"waypoint-mmdvmhost": "0~old+wp1", "waypoint-dmrgateway": "0~old+wp1"},
		healthSeq: []bool{true}, // healthy immediately, repeats -> sustains
	}
	plan := PlanFrom([]Update{
		{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1"},
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"},
	})
	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Confirmed || out.Reverted {
		t.Fatalf("expected Confirmed, got %+v", out)
	}
	// Exactly one install, of the target versions.
	if len(f.installCalls) != 1 {
		t.Fatalf("expected 1 install (targets only), got %d: %v", len(f.installCalls), f.installCalls)
	}
	if !reflect.DeepEqual(f.installCalls[0], plan.Targets()) {
		t.Fatalf("install got %v want targets %v", f.installCalls[0], plan.Targets())
	}
	// Services stopped then restarted.
	if len(f.stopCalls) != 1 || len(f.startCalls) != 1 {
		t.Fatalf("expected one stop and one start, got stop=%v start=%v", f.stopCalls, f.startCalls)
	}
	// History: pending then confirmed.
	if len(f.history) != 2 || f.history[0][0].Result != ResultPending || f.history[1][0].Result != ResultConfirmed {
		t.Fatalf("history results: %+v", f.history)
	}
}

// --- Apply: unhealthy new version reverts to the previous versions ---

func TestApplyRevertsOnUnhealthy(t *testing.T) {
	f := &fakeSystem{
		installed:    map[string]string{"waypoint-mmdvmhost": "0~old+wp1"},
		healthSeq:    []bool{false}, // never healthy -> never sustains -> revert
		healthDetail: "waypoint-mmdvm.service SubState=auto-restart (modem not open)",
	}
	plan := PlanFrom([]Update{{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~bad+wp1"}})
	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Reverted || out.Confirmed {
		t.Fatalf("expected Reverted, got %+v", out)
	}
	// Two installs: the target, then the previous version (the revert).
	if len(f.installCalls) != 2 {
		t.Fatalf("expected install(target) then install(previous), got %v", f.installCalls)
	}
	wantRevert := []PkgVer{{Package: "waypoint-mmdvmhost", Version: "0~old+wp1"}}
	if !reflect.DeepEqual(f.installCalls[1], wantRevert) {
		t.Fatalf("revert install got %v want %v", f.installCalls[1], wantRevert)
	}
	if f.installed["waypoint-mmdvmhost"] != "0~old+wp1" {
		t.Fatalf("expected rollback to old version, installed=%v", f.installed)
	}
	// History: pending then reverted; the reason is carried.
	if len(f.history) != 2 || f.history[1][0].Result != ResultReverted {
		t.Fatalf("history results: %+v", f.history)
	}
	if out.Reason == "" {
		t.Fatal("expected a revert reason")
	}
}

// --- Apply: a failed install reverts (before any health gate) ---

func TestApplyRevertsOnInstallFailure(t *testing.T) {
	f := &fakeSystem{
		installed:  map[string]string{"waypoint-dmrgateway": "0~old+wp1"},
		installErr: context.DeadlineExceeded,
	}
	plan := PlanFrom([]Update{{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"}})
	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	// installErr makes the FIRST install fail; revert then reinstalls the previous
	// version (which the fake lets succeed because installErr is sticky — so assert
	// the failure surfaced instead).
	if err == nil && !out.Reverted {
		t.Fatalf("expected revert or error on install failure, got %+v (err %v)", out, err)
	}
}

// --- Apply: the revert targets are pre-flighted before anything moves (#221) ---

// bench221 reproduces the bench Pi at the moment #221 was observed: waypoint-stack
// 0.1.0 installed, and a pool that carries only the 0.2.0 being offered. The
// update installs fine, the health gate fails, and the revert to 0.1.0 has nowhere
// to go.
func bench221() (*fakeSystem, Plan) {
	f := &fakeSystem{
		installed:    map[string]string{"waypoint-stack": "0.1.0"},
		pool:         map[string]bool{"waypoint-stack=0.2.0": true}, // 0.1.0 is gone
		healthSeq:    []bool{false},                                 // waypoint-mmdvm.service is not active
		healthDetail: "waypoint-mmdvm.service is not active",
	}
	return f, PlanFrom([]Update{{Package: "waypoint-stack", From: "0.1.0", To: "0.2.0"}})
}

// TestApplyRefusesWhenRevertTargetIsNotInThePool is the #221 regression. Before the
// pre-flight this ran the whole update, failed the gate, and died in REVERT FAILED
// with the node stranded on 0.2.0. Now it must refuse having changed nothing.
func TestApplyRefusesWhenRevertTargetIsNotInThePool(t *testing.T) {
	f, plan := bench221()

	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err == nil {
		t.Fatalf("an update whose revert target is not installable must be refused, got %+v", out)
	}
	if out.Confirmed || out.Reverted {
		t.Fatalf("a refused update must report neither confirmed nor reverted, got %+v", out)
	}
	// The refusal has to say what is wrong, and carry apt's own words for it.
	if !strings.Contains(err.Error(), "rolled back") {
		t.Errorf("the refusal should explain that rollback is impossible, got %q", err)
	}
	if !strings.Contains(err.Error(), "Version '0.1.0' for 'waypoint-stack' was not found") {
		t.Errorf("the refusal should carry apt's reason, got %q", err)
	}
	// Nothing moved: this is the whole point of pre-flighting rather than
	// discovering the dead end after a successful install.
	if len(f.installCalls) != 0 {
		t.Errorf("a refused update must install nothing, got %v", f.installCalls)
	}
	if len(f.stopCalls) != 0 || len(f.startCalls) != 0 {
		t.Errorf("a refused update must not touch services, stop=%v start=%v", f.stopCalls, f.startCalls)
	}
	if f.installed["waypoint-stack"] != "0.1.0" {
		t.Errorf("the node must stay on its installed version, got %v", f.installed)
	}
	// And it leaves no phantom in-flight row in the audit trail.
	if len(f.history) != 0 {
		t.Errorf("a refused update must record no history, got %+v", f.history)
	}
}

// TestApplyPreflightsThePreviousVersionsNotTheTargets pins *what* is checked. The
// question is whether the way back is reachable, so the pre-flight must resolve the
// installed versions — checking the targets would always pass and prove nothing.
func TestApplyPreflightsThePreviousVersionsNotTheTargets(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{"waypoint-mmdvmhost": "0~old+wp1", "waypoint-dmrgateway": "0~old+wp1"},
		healthSeq: []bool{true},
	}
	plan := PlanFrom([]Update{
		{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1"},
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"},
	})
	if _, err := Apply(context.Background(), plan, f, fastTimings(), Policy{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(f.checkCalls) != 1 {
		t.Fatalf("expected exactly one pre-flight, got %d: %v", len(f.checkCalls), f.checkCalls)
	}
	want := []PkgVer{
		{Package: "waypoint-dmrgateway", Version: "0~old+wp1"},
		{Package: "waypoint-mmdvmhost", Version: "0~old+wp1"},
	}
	if !reflect.DeepEqual(f.checkCalls[0], want) {
		t.Fatalf("pre-flight checked %v, want the installed versions %v", f.checkCalls[0], want)
	}
}

// TestApplyPreflightsBeforeInstalling pins the *ordering*: the check has to precede
// the install, not merely happen. A pre-flight after the install would reproduce
// #221 exactly.
func TestApplyPreflightsBeforeInstalling(t *testing.T) {
	f, plan := bench221()
	f.healthSeq = []bool{true} // healthy, so only the pre-flight can stop this

	_, _ = Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if len(f.checkCalls) == 0 {
		t.Fatal("Apply never pre-flighted the revert targets")
	}
	if len(f.installCalls) != 0 {
		t.Fatal("the pre-flight must run before the install, not after it")
	}
}

// TestApplyProceedsWhenOperatorOptsOut: allow_unrevertable is the escape hatch for a
// node that must take an update the pool cannot roll back. The update runs, and the
// outcome says plainly that it ran without a way back.
func TestApplyProceedsWhenOperatorOptsOut(t *testing.T) {
	f, plan := bench221()
	f.healthSeq = []bool{true} // comes up healthy, so it confirms

	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{AllowUnrevertable: true})
	if err != nil {
		t.Fatalf("an opted-in update must proceed, got %v", err)
	}
	if !out.Confirmed {
		t.Fatalf("expected Confirmed, got %+v", out)
	}
	if !out.Unrevertable {
		t.Error("an update that ran with no way back must say so in the outcome")
	}
	if f.installed["waypoint-stack"] != "0.2.0" {
		t.Errorf("the update should have been applied, got %v", f.installed)
	}
}

// TestApplyOptedOutStillFailsToRevert is the honest cost of the escape hatch, and
// the reason it is off by default: opting past the pre-flight does not conjure a
// pool. This is #221's exact outcome, reached deliberately instead of by surprise.
func TestApplyOptedOutStillFailsToRevert(t *testing.T) {
	f, plan := bench221() // health gate fails

	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{AllowUnrevertable: true})
	if err == nil {
		t.Fatalf("the revert had nowhere to go; expected REVERT FAILED, got %+v", out)
	}
	if !strings.Contains(err.Error(), "REVERT FAILED") {
		t.Errorf("expected REVERT FAILED, got %q", err)
	}
	if !out.Unrevertable {
		t.Error("the outcome must attribute the failed revert to the missing pool")
	}
	if f.installed["waypoint-stack"] != "0.2.0" {
		t.Errorf("the node is stranded on the new version, as predicted: %v", f.installed)
	}
}

// TestApplySkipsPreflightForFreshInstalls: a package with no installed version has
// no revert target, so there is nothing to prove and nothing to refuse. Without
// this, a plan containing a brand-new package could never be applied.
func TestApplySkipsPreflightForFreshInstalls(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{"waypoint-dapnetgateway": ""}, // not installed yet
		pool:      map[string]bool{"waypoint-dapnetgateway=0~new+wp1": true},
		healthSeq: []bool{true},
	}
	plan := PlanFrom([]Update{{Package: "waypoint-dapnetgateway", From: "", To: "0~new+wp1"}})

	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err != nil {
		t.Fatalf("a fresh install has no revert target and must not be refused: %v", err)
	}
	if !out.Confirmed {
		t.Fatalf("expected Confirmed, got %+v", out)
	}
	if out.Unrevertable {
		t.Error("nothing to revert to is not the same as no way back")
	}
}

// --- gate: sustained health defeats a flap ---

func TestGateNeedsSustainedHealth(t *testing.T) {
	// healthy, healthy, FLAP, then healthy x3 -> confirms only after 3 consecutive.
	f := &fakeSystem{
		installed: map[string]string{"waypoint-mmdvmhost": "0~old+wp1"},
		healthSeq: []bool{true, true, false, true, true, true},
	}
	plan := PlanFrom([]Update{{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1"}})
	tm := Timings{MaxPolls: 8, ConfirmChecks: 3}
	out, err := Apply(context.Background(), plan, f, tm, Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Confirmed {
		t.Fatalf("a flap that then sustains 3 polls should confirm, got %+v", out)
	}
}

func TestGateRevertsWhenNeverSustained(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{"waypoint-mmdvmhost": "0~old+wp1"},
		healthSeq: []bool{true, false, true, false, true, false}, // never 2 in a row
	}
	plan := PlanFrom([]Update{{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1"}})
	tm := Timings{MaxPolls: 6, ConfirmChecks: 2}
	out, err := Apply(context.Background(), plan, f, tm, Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Reverted {
		t.Fatalf("health that never sustains should revert, got %+v", out)
	}
}

// --- version-pair bookkeeping ---

func TestHistoryRowsPrefersInstalledFrom(t *testing.T) {
	prev := map[string]string{"waypoint-mmdvmhost": "0~actual+wp1"}
	updates := []Update{{Package: "waypoint-mmdvmhost", From: "0~stale+wp1", To: "0~new+wp1"}}
	rows := historyRows(prev, updates, ResultConfirmed)
	if len(rows) != 1 || rows[0].From != "0~actual+wp1" || rows[0].To != "0~new+wp1" || rows[0].Result != ResultConfirmed {
		t.Fatalf("historyRows used the wrong From/To/Result: %+v", rows)
	}
}

func TestRevertSetSkipsUninstalled(t *testing.T) {
	prev := map[string]string{"waypoint-mmdvmhost": "0~old+wp1", "waypoint-newpkg": ""}
	got := revertSet(prev, []string{"waypoint-mmdvmhost", "waypoint-newpkg"})
	want := []PkgVer{{Package: "waypoint-mmdvmhost", Version: "0~old+wp1"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("revertSet got %v want %v", got, want)
	}
}

// --- History table round-trip (store bookkeeping) ---

func TestHistoryStoreRoundTrip(t *testing.T) {
	s, err := store.Open(":memory:")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()
	h, err := NewHistory(s)
	if err != nil {
		t.Fatalf("NewHistory: %v", err)
	}
	h.now = func() time.Time { return time.Date(2026, 7, 22, 4, 0, 0, 0, time.UTC) }
	rows := []HistoryRow{
		{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1", Result: ResultConfirmed},
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1", Result: ResultConfirmed},
	}
	if err := h.Insert(rows); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := h.Recent(10)
	if err != nil {
		t.Fatalf("Recent: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(got))
	}
	if got[0].At != "2026-07-22T04:00:00Z" || got[0].Result != ResultConfirmed {
		t.Fatalf("round-trip row wrong: %+v", got[0])
	}
}

// --- quiet-window / auto-apply decision logic ---

func TestWithinQuietWindow(t *testing.T) {
	utc := time.UTC
	at := func(h, m int) time.Time { return time.Date(2026, 7, 22, h, m, 0, 0, utc) }
	cases := []struct {
		now   time.Time
		quiet string
		want  bool
	}{
		{at(4, 0), "04:00", true},   // exactly at the window start
		{at(4, 30), "04:00", true},  // inside the hour
		{at(4, 59), "04:00", true},  // last minute of the window
		{at(5, 0), "04:00", false},  // window closed
		{at(3, 59), "04:00", false}, // before the window
		{at(4, 0), "bad", false},    // unparseable quiet time
	}
	for _, c := range cases {
		if got := WithinQuietWindow(c.now, c.quiet); got != c.want {
			t.Errorf("WithinQuietWindow(%s, %q) = %v, want %v", c.now.Format("15:04"), c.quiet, got, c.want)
		}
	}
}

func TestDueForAutoApply(t *testing.T) {
	utc := time.UTC
	now := time.Date(2026, 7, 22, 4, 5, 0, 0, utc)
	yesterday := time.Date(2026, 7, 21, 4, 5, 0, 0, utc)
	earlierToday := time.Date(2026, 7, 22, 4, 1, 0, 0, utc)

	if !DueForAutoApply(now, yesterday, "04:00", true, true) {
		t.Error("should be due: on, available, in-window, last applied yesterday")
	}
	if DueForAutoApply(now, earlierToday, "04:00", true, true) {
		t.Error("should NOT be due: already applied earlier today")
	}
	if DueForAutoApply(now, time.Time{}, "04:00", false, true) {
		t.Error("should NOT be due: auto-apply off")
	}
	if DueForAutoApply(now, time.Time{}, "04:00", true, false) {
		t.Error("should NOT be due: nothing available")
	}
	if DueForAutoApply(time.Date(2026, 7, 22, 6, 0, 0, 0, utc), time.Time{}, "04:00", true, true) {
		t.Error("should NOT be due: outside the quiet window")
	}
	if !DueForAutoApply(now, time.Time{}, "04:00", true, true) {
		t.Error("should be due: never applied before")
	}
}

// --- the health gate only requires the modem host when the node runs one ---

// TestGateRequiresMMDVMWhenTheNodeRunsOne keeps the original safety property: an
// update must not leave the modem host down, so on a node that runs MMDVMHost the
// gate covers its unit even when the update touched no package that backs it.
func TestGateRequiresMMDVMWhenTheNodeRunsOne(t *testing.T) {
	var gated [][]string
	f := &fakeSystem{
		installed: map[string]string{"waypoint-dmrgateway": "0~old+wp1"},
		healthSeq: []bool{true},
	}
	f.onHealthy = func(units []string) { gated = append(gated, units) }

	plan := PlanFrom([]Update{{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"}})
	plan.RequireMMDVM = true
	if _, err := Apply(context.Background(), plan, f, fastTimings(), Policy{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(gated) == 0 {
		t.Fatal("the gate never probed")
	}
	if !contains(gated[0], MMDVMUnit) {
		t.Errorf("the gate must cover %q on a node that runs it; got %v", MMDVMUnit, gated[0])
	}
}

// TestGateSkipsMMDVMWhenTheNodeRunsNone is the regression for the interaction with
// mode-gated rendering: a node with every mode off (or no modem port) correctly
// runs no modem host, and gating on a unit that is meant to be stopped would fail
// every stack update on it forever — the same "always reverts" failure #221 was
// investigated through.
func TestGateSkipsMMDVMWhenTheNodeRunsNone(t *testing.T) {
	var gated [][]string
	f := &fakeSystem{
		installed: map[string]string{"waypoint-dmrgateway": "0~old+wp1"},
		healthSeq: []bool{true},
	}
	f.onHealthy = func(units []string) { gated = append(gated, units) }

	plan := PlanFrom([]Update{{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"}})
	plan.RequireMMDVM = false
	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Confirmed {
		t.Fatalf("an update on a node with no modem host must be able to confirm, got %+v", out)
	}
	if len(gated) == 0 {
		t.Fatal("the gate never probed")
	}
	if contains(gated[0], MMDVMUnit) {
		t.Errorf("the gate must not require %q on a node that does not run it; got %v", MMDVMUnit, gated[0])
	}
}

// An update that DOES touch waypoint-mmdvmhost gates on its unit regardless: the
// unit is in the plan on its own merit, not because RequireMMDVM added it.
func TestGateCoversMMDVMWhenTheUpdateTouchesIt(t *testing.T) {
	var gated [][]string
	f := &fakeSystem{
		installed: map[string]string{"waypoint-mmdvmhost": "0~old+wp1"},
		healthSeq: []bool{true},
	}
	f.onHealthy = func(units []string) { gated = append(gated, units) }

	plan := PlanFrom([]Update{{Package: "waypoint-mmdvmhost", From: "0~old+wp1", To: "0~new+wp1"}})
	plan.RequireMMDVM = false // even so
	if _, err := Apply(context.Background(), plan, f, fastTimings(), Policy{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !contains(gated[0], MMDVMUnit) {
		t.Errorf("an update to waypoint-mmdvmhost must gate on its unit; got %v", gated[0])
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// --- RunUnits: the node's shape decides what is restarted and gated on ---

// The failure this pins, measured on the bench node 2026-08-24: a DMR-only node
// took a twelve-package stack update, every package installed, and the gate then
// reported "waypoint-dgidgateway.service is not active" and tried to revert. It
// was not active because YSF/DG-ID is switched off, so waypointd never renders
// DGIdGateway.ini and the daemon exits(1) with "Couldn't open the .ini file".
// Restarting and gating on a unit the node is not meant to run made the update
// unpassable on any node short of one running every mode.
func TestApplyDoesNotStartOrGateUnitsTheNodeDoesNotRun(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{
			"waypoint-dmrgateway":  "0~old+wp1",
			"waypoint-dgidgateway": "0~old+wp1",
		},
		healthSeq: []bool{true},
	}
	var gated []string
	f.onHealthy = func(units []string) { gated = append([]string(nil), units...) }

	plan := PlanFrom([]Update{
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"},
		{Package: "waypoint-dgidgateway", From: "0~old+wp1", To: "0~new+wp1"},
	})
	// What config.RunningUnits answers on a DMR-only node: the DG-ID gateway is
	// upgraded but is not meant to be running.
	plan.RestrictToRunning([]string{"waypoint-dmrgateway.service"})

	out, err := Apply(context.Background(), plan, f, fastTimings(), Policy{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !out.Confirmed || out.Reverted {
		t.Fatalf("expected the update to confirm, got %+v", out)
	}
	// Stopping is still unconditional: a unit must not be left running off a
	// half-replaced binary just because this node does not normally run it.
	// PlanFrom sorts by package name, so the DG-ID gateway leads.
	wantStop := []string{"waypoint-dgidgateway.service", "waypoint-dmrgateway.service"}
	if len(f.stopCalls) != 1 || !reflect.DeepEqual(f.stopCalls[0], wantStop) {
		t.Fatalf("stop got %v, want the full affected set %v", f.stopCalls, wantStop)
	}
	wantStart := []string{"waypoint-dmrgateway.service"}
	if len(f.startCalls) != 1 || !reflect.DeepEqual(f.startCalls[0], wantStart) {
		t.Fatalf("start got %v, want only the units this node runs %v", f.startCalls, wantStart)
	}
	if !reflect.DeepEqual(gated, wantStart) {
		t.Fatalf("health gate probed %v, want only %v", gated, wantStart)
	}
}

// The clean case beside the failure case: a node that DOES run every affected
// unit still restarts and gates on every one of them. A fix that quietly stopped
// restarting real gateways would pass the test above and fail here.
func TestApplyStartsAndGatesEveryUnitTheNodeDoesRun(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{
			"waypoint-dmrgateway":  "0~old+wp1",
			"waypoint-dgidgateway": "0~old+wp1",
		},
		healthSeq: []bool{true},
	}
	var gated []string
	f.onHealthy = func(units []string) { gated = append([]string(nil), units...) }

	plan := PlanFrom([]Update{
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"},
		{Package: "waypoint-dgidgateway", From: "0~old+wp1", To: "0~new+wp1"},
	})
	plan.RestrictToRunning([]string{"waypoint-dmrgateway.service", "waypoint-dgidgateway.service"})

	if _, err := Apply(context.Background(), plan, f, fastTimings(), Policy{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	want := []string{"waypoint-dgidgateway.service", "waypoint-dmrgateway.service"}
	if len(f.startCalls) != 1 || !reflect.DeepEqual(f.startCalls[0], want) {
		t.Fatalf("start got %v, want %v", f.startCalls, want)
	}
	if !reflect.DeepEqual(gated, want) {
		t.Fatalf("health gate probed %v, want %v", gated, want)
	}
}

// A caller that never narrows the plan keeps the older behaviour — everything
// affected is restarted and gated on. PlanFrom must not leave RunUnits empty, or
// an unnarrowed plan would install packages and start nothing.
func TestPlanFromRunUnitsDefaultsToEveryAffectedUnit(t *testing.T) {
	plan := PlanFrom([]Update{
		{Package: "waypoint-dmrgateway", From: "a", To: "b"},
		{Package: "waypoint-p25parrot", From: "a", To: "b"}, // no unit; contributes none
	})
	if !reflect.DeepEqual(plan.RunUnits, plan.Units) {
		t.Fatalf("RunUnits %v, want it to default to Units %v", plan.RunUnits, plan.Units)
	}
	if len(plan.RunUnits) != 1 {
		t.Fatalf("RunUnits %v, want just the DMR gateway's unit", plan.RunUnits)
	}
}

func TestRestrictToRunning(t *testing.T) {
	for _, tc := range []struct {
		name    string
		running []string
		want    []string
	}{
		// Plan order, not the caller's: PlanFrom sorts by package name.
		{"keeps the running subset in plan order", []string{"waypoint-dmrgateway.service", "waypoint-dgidgateway.service"},
			[]string{"waypoint-dgidgateway.service", "waypoint-dmrgateway.service"}},
		{"drops units the node does not run", []string{"waypoint-dmrgateway.service"},
			[]string{"waypoint-dmrgateway.service"}},
		// A node with every mode off runs no gateway, and that is a real answer,
		// not a missing one: there is nothing to restart or gate on.
		{"a node that runs nothing keeps nothing", nil, []string{}},
		// Units the node runs but this update did not touch are not added — the
		// plan only ever restarts what it changed.
		{"never adds an untouched unit", []string{"waypoint-dmrgateway.service", "waypoint-m17gateway.service"},
			[]string{"waypoint-dmrgateway.service"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plan := PlanFrom([]Update{
				{Package: "waypoint-dmrgateway", From: "a", To: "b"},
				{Package: "waypoint-dgidgateway", From: "a", To: "b"},
			})
			before := append([]string(nil), plan.Units...)
			plan.RestrictToRunning(tc.running)
			if !reflect.DeepEqual(plan.RunUnits, tc.want) {
				t.Fatalf("RunUnits = %v, want %v", plan.RunUnits, tc.want)
			}
			if !reflect.DeepEqual(plan.Units, before) {
				t.Fatalf("Units was narrowed to %v; the stop set must stay %v", plan.Units, before)
			}
		})
	}
}

// --- a failed revert must not leave the node's services down ---

// Before this, revert() returned as soon as the downgrade install failed, without
// reaching StartServices. On the bench node 2026-08-24 that took the radio off
// the air: every gateway including waypoint-dmrgateway.service sat dead until an
// operator applied config by hand. The packages are wherever the failed install
// left them either way — but a node running the wrong versions still beats a
// silent one.
func TestRevertFailureStillStartsTheServicesItStopped(t *testing.T) {
	f := &fakeSystem{
		installed: map[string]string{
			"waypoint-dmrgateway":  "0~old+wp1",
			"waypoint-dgidgateway": "0~old+wp1",
		},
		healthSeq:    []bool{false}, // never sustains -> revert
		healthDetail: "waypoint-dmrgateway.service is not active",
		// The pool carries the new versions but not the old ones, so the revert's
		// install fails exactly the way the bench node's did.
		pool: map[string]bool{
			"waypoint-dmrgateway=0~new+wp1":  true,
			"waypoint-dgidgateway=0~new+wp1": true,
		},
	}
	plan := PlanFrom([]Update{
		{Package: "waypoint-dmrgateway", From: "0~old+wp1", To: "0~new+wp1"},
		{Package: "waypoint-dgidgateway", From: "0~old+wp1", To: "0~new+wp1"},
	})
	plan.RestrictToRunning([]string{"waypoint-dmrgateway.service"})

	// AllowUnrevertable, or the pre-flight would refuse before any of this runs —
	// which is the whole reason a revert can fail at all.
	_, err := Apply(context.Background(), plan, f, fastTimings(), Policy{AllowUnrevertable: true})
	if err == nil || !strings.Contains(err.Error(), "REVERT FAILED") {
		t.Fatalf("expected a REVERT FAILED error, got %v", err)
	}
	// Two stops and two starts: Apply's own pair around the install, then revert's
	// stop and the restart that must follow it. One start means the second stop
	// was never undone — the node left silent, which is the bug.
	if len(f.stopCalls) != 2 {
		t.Fatalf("expected Apply's stop and revert's stop, got %d: %v", len(f.stopCalls), f.stopCalls)
	}
	if len(f.startCalls) != 2 {
		t.Fatalf("expected a restart after the failed revert, got %d start call(s): %v", len(f.startCalls), f.startCalls)
	}
	last := f.startCalls[len(f.startCalls)-1]
	if want := []string{"waypoint-dmrgateway.service"}; !reflect.DeepEqual(last, want) {
		t.Fatalf("restarted %v after the failed revert, want the units this node runs %v", last, want)
	}
	// The audit trail still says the revert failed, so an operator is not told
	// the node is fine merely because its services came back.
	lastHist := f.history[len(f.history)-1]
	if lastHist[0].Result != ResultRevertFailed {
		t.Fatalf("history result %q, want %q", lastHist[0].Result, ResultRevertFailed)
	}
}
