package updater

import (
	"context"
	"testing"
	"time"
)

// fakeSystem records the engine's side effects so the state machine is testable
// without a real filesystem/systemd/http.
type fakeSystem struct {
	live       string // the "installed" version
	rollbackOf string // what BackupCurrent saved (the prior live)
	staged     string // last staged version
	marker     *Marker
	restarts   int
	swapErr    error
	restartErr error
	// health returns this version/ok on each probe (index by call).
	healthSeq []healthResp
	healthIdx int
	now       time.Time
}

type healthResp struct {
	version string
	ok      bool
}

func (f *fakeSystem) StageBinary(data []byte) (string, error) {
	f.staged = string(data) // in tests the "binary" is just its version string
	return "stage:" + f.staged, nil
}
func (f *fakeSystem) BackupCurrent() (string, error) {
	f.rollbackOf = f.live
	return "rollback:" + f.live, nil
}
func (f *fakeSystem) Swap(stagePath string) error {
	if f.swapErr != nil {
		return f.swapErr
	}
	f.live = f.staged
	return nil
}
func (f *fakeSystem) Restart(ctx context.Context) error {
	f.restarts++
	return f.restartErr
}
func (f *fakeSystem) Health(ctx context.Context) (string, bool) {
	if f.healthIdx < len(f.healthSeq) {
		h := f.healthSeq[f.healthIdx]
		f.healthIdx++
		return h.version, h.ok
	}
	return "", false
}
func (f *fakeSystem) Restore(rollbackPath string) error {
	f.live = f.rollbackOf
	return nil
}
func (f *fakeSystem) WriteMarker(m Marker) error { c := m; f.marker = &c; return nil }
func (f *fakeSystem) ReadMarker() (*Marker, error) {
	if f.marker == nil {
		return nil, nil
	}
	c := *f.marker
	return &c, nil
}
func (f *fakeSystem) ClearMarker() error { f.marker = nil; return nil }
func (f *fakeSystem) Now() time.Time     { f.now = f.now.Add(time.Millisecond); return f.now }

func availPlan(v string) Plan { return Plan{Available: true, Version: v} }

// Property 1: happy path — the new version comes up healthy, gets committed
// (marker cleared), and is live.
func TestApplyConfirms(t *testing.T) {
	f := &fakeSystem{live: "1.0.0", healthSeq: []healthResp{{"1.4.0", true}}}
	out, err := Apply(context.Background(), availPlan("1.4.0"), []byte("1.4.0"), f, time.Second, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Confirmed || out.Version != "1.4.0" {
		t.Fatalf("expected confirmed 1.4.0, got %+v", out)
	}
	if f.live != "1.4.0" {
		t.Errorf("live = %q, want 1.4.0", f.live)
	}
	if f.marker != nil {
		t.Errorf("marker not cleared after confirm: %+v", f.marker)
	}
}

// Property 3 (the #13 acceptance, health path): the new version never becomes
// healthy → the prior binary is restored, restarted, marker cleared.
func TestApplyRevertsOnUnhealthy(t *testing.T) {
	f := &fakeSystem{live: "1.0.0", healthSeq: []healthResp{{"", false}, {"1.0.0", true}}} // never reports 1.4.0
	out, err := Apply(context.Background(), availPlan("1.4.0"), []byte("1.4.0"), f, 5*time.Millisecond, time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reverted {
		t.Fatalf("expected revert, got %+v", out)
	}
	if f.live != "1.0.0" {
		t.Errorf("after revert live = %q, want 1.0.0 (prior)", f.live)
	}
	if f.marker != nil {
		t.Errorf("marker not cleared after revert: %+v", f.marker)
	}
	if f.restarts < 2 {
		t.Errorf("expected a restart for the new version and one for the revert, got %d", f.restarts)
	}
}

// Property 5: a swap failure leaves the old binary live and no phantom marker.
func TestApplySwapFailureIsClean(t *testing.T) {
	f := &fakeSystem{live: "1.0.0", swapErr: context.DeadlineExceeded}
	_, err := Apply(context.Background(), availPlan("1.4.0"), []byte("1.4.0"), f, time.Second, time.Millisecond)
	if err == nil {
		t.Fatal("expected swap error")
	}
	if f.live != "1.0.0" {
		t.Errorf("live changed despite swap failure: %q", f.live)
	}
	if f.marker != nil {
		t.Errorf("phantom marker left after swap failure: %+v", f.marker)
	}
}

// Apply refuses a non-available plan.
func TestApplyRefusesNoUpdate(t *testing.T) {
	f := &fakeSystem{live: "1.0.0"}
	if _, err := Apply(context.Background(), Plan{Reason: "already up to date"}, nil, f, time.Second, time.Millisecond); err == nil {
		t.Error("Apply ran with no available update")
	}
}

// Property 4 (the #13 acceptance, power-loss path): a pending marker at the boot
// limit reverts on boot; below the limit it increments and waits.
func TestBootCheck(t *testing.T) {
	// At the limit (an earlier boot already tried and did not confirm) → revert.
	f := &fakeSystem{live: "1.4.0-bad", rollbackOf: "1.0.0", marker: &Marker{Version: "1.4.0", Rollback: "rollback:1.0.0", BootCount: 1}}
	out, err := BootCheck(f, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Reverted {
		t.Fatalf("expected boot revert, got %+v", out)
	}
	if f.live != "1.0.0" {
		t.Errorf("boot revert live = %q, want 1.0.0 (prior)", f.live)
	}
	if f.marker != nil {
		t.Error("marker not cleared after boot revert")
	}

	// Below the limit → increment, don't revert (the new version gets a chance).
	f2 := &fakeSystem{live: "1.4.0", marker: &Marker{Version: "1.4.0", Rollback: "r", BootCount: 0}}
	out, err = BootCheck(f2, 1)
	if err != nil || out.Reverted {
		t.Fatalf("first boot should not revert: out=%+v err=%v", out, err)
	}
	if f2.marker == nil || f2.marker.BootCount != 1 {
		t.Errorf("boot count not incremented: %+v", f2.marker)
	}

	// No marker → no-op.
	f3 := &fakeSystem{live: "1.4.0"}
	if out, err := BootCheck(f3, 1); err != nil || out.Reverted {
		t.Errorf("no-marker boot check should be a no-op: %+v %v", out, err)
	}
}

// Property 6: PlanUpdate decisions.
func TestPlanUpdate(t *testing.T) {
	m := Manifest{Version: "1.4.0", MinVersion: "1.0.0", Artifacts: map[string]Artifact{"linux/arm64": {URL: "u"}}}
	if p := PlanUpdate("1.2.0", m, "linux/arm64"); !p.Available || p.Version != "1.4.0" {
		t.Errorf("newer version should be available: %+v", p)
	}
	if p := PlanUpdate("1.4.0", m, "linux/arm64"); p.Available {
		t.Errorf("equal version should not update: %+v", p)
	}
	if p := PlanUpdate("1.5.0", m, "linux/arm64"); p.Available {
		t.Errorf("older manifest should not update: %+v", p)
	}
	if p := PlanUpdate("1.2.0", m, "linux/amd64"); p.Available || p.Reason == "" {
		t.Errorf("missing artifact should be refused with a reason: %+v", p)
	}
	// min_version above current → refused.
	m2 := Manifest{Version: "2.0.0", MinVersion: "1.9.0", Artifacts: map[string]Artifact{"linux/arm64": {URL: "u"}}}
	if p := PlanUpdate("1.2.0", m2, "linux/arm64"); p.Available || p.Reason == "" {
		t.Errorf("min_version above current should refuse: %+v", p)
	}
	// dev build → treated as older, updates.
	if p := PlanUpdate("abc123-dev", m, "linux/arm64"); !p.Available {
		t.Errorf("dev build should update: %+v", p)
	}
}
