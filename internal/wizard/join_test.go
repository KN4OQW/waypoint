package wizard_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

// fakeAP records the handover so a test can assert the order of operations, not
// just the end state. The order is the design here: a join that lowers the AP
// after attempting to associate would never work on a single-radio Pi.
type fakeAP struct {
	mu     sync.Mutex
	events []string
	upErr  error
}

func (f *fakeAP) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, s)
}

func (f *fakeAP) log() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *fakeAP) DownForJoin(context.Context) error { f.record("down"); return nil }
func (f *fakeAP) Commit(context.Context) error      { f.record("commit"); return nil }
func (f *fakeAP) Reraise(_ context.Context, _ captive.Reason) error {
	if f.upErr != nil {
		f.record("reraise-failed")
		return f.upErr
	}
	f.record("reraise")
	return nil
}

// joinEnv wires a wizard to the in-memory helper and a recording access point.
type joinEnv struct {
	w    *wizard.Wizard
	fake *privtest.Fake
	ap   *fakeAP
	logs *strings.Builder
	mu   sync.Mutex
}

func newJoinEnv(t *testing.T) *joinEnv {
	t.Helper()
	e := &joinEnv{fake: privtest.NewFake(), ap: &fakeAP{}, logs: &strings.Builder{}}
	e.w = &wizard.Wizard{
		Prov:         e.fake,
		MarkerPath:   t.TempDir() + "/provisioned",
		ProgressPath: t.TempDir() + "/setup-progress.json",
		AP:           e.ap,
		Claimed:      func() bool { return false },
		Logf: func(format string, args ...any) {
			e.mu.Lock()
			defer e.mu.Unlock()
			t.Logf(format, args...)
		},
	}
	return e
}

// checkpointsOutstanding counts checkpoints the helper still holds. A join that
// leaves one behind has pinned the pre-join network state indefinitely.
func (e *joinEnv) checkpointsOutstanding() int {
	created := len(e.fake.CallsTo(privhelper.MethodNetCheckpointCreate))
	destroyed := len(e.fake.CallsTo(privhelper.MethodNetCheckpointDestroy))
	rolled := len(e.fake.CallsTo(privhelper.MethodNetCheckpointRollback))
	return created - destroyed - rolled
}

// Property 1: a good passphrase commits. The checkpoint is destroyed, the AP is
// spent, and the join persists.
func TestJoinCommitsOnSuccess(t *testing.T) {
	e := newJoinEnv(t)

	got, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery", Country: "US", TimeoutSeconds: 30})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected || got.IPv4 == "" {
		t.Fatalf("result = %+v", got)
	}
	if e.fake.Joined == nil || e.fake.Joined.SSID != "home-network" {
		t.Errorf("the helper did not join: %+v", e.fake.Joined)
	}

	// The radio is handed over before the association and spent after it — in that
	// order, because a single-radio Pi cannot be an AP and a station at once.
	if want := []string{"down", "commit"}; !equal(e.ap.log(), want) {
		t.Errorf("AP handover = %v, want %v", e.ap.log(), want)
	}
	if n := e.checkpointsOutstanding(); n != 0 {
		t.Errorf("%d checkpoints outstanding after a committed join", n)
	}
	if len(e.fake.CallsTo(privhelper.MethodNetCheckpointDestroy)) != 1 {
		t.Error("the checkpoint was not destroyed on success")
	}
	if len(e.fake.CallsTo(privhelper.MethodNetCheckpointRollback)) != 0 {
		t.Error("a successful join rolled back")
	}
}

// Property 2: a bad passphrase rolls back and the access point comes back.
//
// This is the acceptance case that matters most. The operator is on a phone
// connected to the setup AP, typing their Wi-Fi password from memory. Getting it
// wrong has to leave them exactly where they were, not with a node that has
// dropped the AP and failed to join anything.
func TestJoinRestoresTheAPOnABadPassphrase(t *testing.T) {
	e := newJoinEnv(t)
	e.fake.Errs[privhelper.MethodNetJoin] = privhelper.Errorf(privhelper.CodeConflict,
		"could not associate: authentication failed")

	got, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "wrong passphrase"})
	if err == nil {
		t.Fatal("a failed join reported success")
	}
	if got.Connected {
		t.Error("Connected = true on a failed join")
	}

	if want := []string{"down", "reraise"}; !equal(e.ap.log(), want) {
		t.Fatalf("AP handover = %v, want %v — the operator has nowhere to retry from", e.ap.log(), want)
	}
	if len(e.fake.CallsTo(privhelper.MethodNetCheckpointRollback)) != 1 {
		t.Error("the checkpoint was not rolled back")
	}
	if n := e.checkpointsOutstanding(); n != 0 {
		t.Errorf("%d checkpoints outstanding after a rolled-back join", n)
	}
	// And the wizard is still where it was: a failed join is not a failed setup.
	if e.w.Provisioned() {
		t.Error("a failed join marked the node provisioned")
	}
}

// Property 3: association without an address is a failure, not a success.
//
// nmcli reports success once the profile is up. A node associated to the right
// SSID with no DHCP lease satisfies that while being completely unreachable —
// the failure an operator discovers ten minutes later, hunting for a node that is
// on their network and answering nothing.
func TestJoinTreatsAMissingLeaseAsFailure(t *testing.T) {
	e := newJoinEnv(t)
	e.fake.NetJoinResult = &privhelper.NetJoinResponse{
		SSID: "home-network", Interface: "wlan0", Connected: true, IPv4: "", Profile: "waypoint-home",
	}

	_, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery"})
	if err == nil {
		t.Fatal("an association with no address reported success")
	}
	if !strings.Contains(err.Error(), "no address") {
		t.Errorf("the error does not explain what went wrong: %v", err)
	}
	if want := []string{"down", "reraise"}; !equal(e.ap.log(), want) {
		t.Errorf("AP handover = %v, want %v", e.ap.log(), want)
	}
	if len(e.fake.CallsTo(privhelper.MethodNetCheckpointRollback)) != 1 {
		t.Error("the checkpoint was not rolled back")
	}
}

// Property 4: a helper that reports Connected false is a failure even with no
// error — the two ways nmcli can say no.
func TestJoinTreatsNotConnectedAsFailure(t *testing.T) {
	e := newJoinEnv(t)
	e.fake.NetJoinResult = &privhelper.NetJoinResponse{SSID: "home-network", Connected: false}

	if _, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network"}); err == nil {
		t.Fatal("an unconnected join reported success")
	}
	if want := []string{"down", "reraise"}; !equal(e.ap.log(), want) {
		t.Errorf("AP handover = %v, want %v", e.ap.log(), want)
	}
}

// Property 5: if the AP cannot be lowered, the join is not attempted and the
// checkpoint is released. Attempting anyway on a single-radio node would fail in
// a much more confusing way.
func TestJoinAbortsIfTheRadioCannotBeFreed(t *testing.T) {
	e := newJoinEnv(t)
	failing := &failingDownAP{err: errors.New("radio busy")}
	e.w.AP = failing

	if _, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery"}); err == nil {
		t.Fatal("the join went ahead without the radio")
	}
	if e.fake.Joined != nil {
		t.Error("a join was attempted despite the AP refusing to come down")
	}
	if len(e.fake.CallsTo(privhelper.MethodNetCheckpointRollback)) != 1 {
		t.Error("the checkpoint was not released")
	}
	if n := e.checkpointsOutstanding(); n != 0 {
		t.Errorf("%d checkpoints outstanding", n)
	}
}

type failingDownAP struct{ err error }

func (f *failingDownAP) DownForJoin(context.Context) error             { return f.err }
func (f *failingDownAP) Commit(context.Context) error                  { return nil }
func (f *failingDownAP) Reraise(context.Context, captive.Reason) error { return nil }

// Property 6: the operator can retry after a failure, and the second attempt
// commits. A one-shot join would mean a typo costs a reflash.
func TestJoinIsRetryableAfterAFailure(t *testing.T) {
	e := newJoinEnv(t)
	e.fake.Errs[privhelper.MethodNetJoin] = privhelper.Errorf(privhelper.CodeConflict, "authentication failed")

	if _, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "wrong"}); err == nil {
		t.Fatal("the first attempt reported success")
	}

	delete(e.fake.Errs, privhelper.MethodNetJoin)
	got, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery"})
	if err != nil {
		t.Fatalf("the retry failed: %v", err)
	}
	if !got.Connected {
		t.Error("the retry did not connect")
	}
	if want := []string{"down", "reraise", "down", "commit"}; !equal(e.ap.log(), want) {
		t.Errorf("AP handover across the retry = %v, want %v", e.ap.log(), want)
	}
	if n := e.checkpointsOutstanding(); n != 0 {
		t.Errorf("%d checkpoints outstanding after a retry", n)
	}
}

// Property 7: a wizard with no access point — a node set up over Ethernet — joins
// without any handover at all.
func TestJoinWithoutAnAP(t *testing.T) {
	e := newJoinEnv(t)
	e.w.AP = nil

	got, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery"})
	if err != nil {
		t.Fatal(err)
	}
	if !got.Connected {
		t.Error("the join did not connect")
	}
	if n := e.checkpointsOutstanding(); n != 0 {
		t.Errorf("%d checkpoints outstanding", n)
	}
}

// Property 8: the checkpoint is created before the radio is handed over, so a
// failure to free the radio is itself covered by the rollback.
func TestJoinCheckpointsBeforeTouchingTheRadio(t *testing.T) {
	e := newJoinEnv(t)
	if _, err := e.w.JoinNetwork(context.Background(), wizard.JoinRequest{
		SSID: "home-network", PSK: "correct horse battery"}); err != nil {
		t.Fatal(err)
	}

	var order []string
	for _, c := range e.fake.Calls {
		switch c.Method {
		case privhelper.MethodNetCheckpointCreate, privhelper.MethodAPDown,
			privhelper.MethodNetJoin, privhelper.MethodNetCheckpointDestroy:
			order = append(order, string(c.Method))
		}
	}
	// APDown is not in this list: the fake AP in this test records it separately.
	want := []string{
		string(privhelper.MethodNetCheckpointCreate),
		string(privhelper.MethodNetJoin),
		string(privhelper.MethodNetCheckpointDestroy),
	}
	if !equal(order, want) {
		t.Errorf("helper call order = %v, want %v", order, want)
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
