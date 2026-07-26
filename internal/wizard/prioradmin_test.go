package wizard_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
	"github.com/KN4OQW/waypoint/internal/wizard"
)

type adminEnv struct {
	w    *wizard.Wizard
	fake *privtest.Fake
}

func newAdminEnv(t *testing.T) *adminEnv {
	t.Helper()
	dir := t.TempDir()
	e := &adminEnv{fake: privtest.NewFake()}
	e.w = &wizard.Wizard{
		Prov:         e.fake,
		MarkerPath:   filepath.Join(dir, "provisioned"),
		ProgressPath: filepath.Join(dir, "progress.json"),
		Claimed:      func() bool { return false },
		Logf:         t.Logf,
	}
	return e
}

// seedPriorOwner puts an inherited administrator account on the node, as a
// second-hand board would carry.
func (e *adminEnv) seedPriorOwner(t *testing.T, name string, key string) {
	t.Helper()
	req := privhelper.CreateRecoveryUserRequest{Username: name, Sudo: true}
	if key != "" {
		req.SSHKey = key
	} else {
		req.Password = "their old password"
	}
	if _, err := e.fake.CreateRecoveryUser(context.Background(), req); err != nil {
		t.Fatal(err)
	}
}

// setupThrough runs the wizard up to and including the recovery account.
func (e *adminEnv) setupThroughUser(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.w.SetHostname(ctx, wizard.HostnameRequest{Hostname: "hs-shack"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.CreateUser(ctx, wizard.UserRequest{
		Username: "rescue", SSHKey: privtest.SSHPublicKey(1)}); err != nil {
		t.Fatal(err)
	}
}

// Property 1: prior administrators are listed with what an operator needs to
// judge them — most importantly whether they carry an SSH key, which is standing
// passwordless access to a node the operator believes is theirs.
func TestPriorAdminsAreListedWithCredentialSummaries(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.seedPriorOwner(t, "their-friend", "")
	e.setupThroughUser(t)

	got, err := e.w.PriorAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]wizard.PriorAdmin{}
	for _, a := range got {
		byName[a.Username] = a
	}
	if len(got) != 2 {
		t.Fatalf("%d prior admins listed, want 2: %+v", len(got), got)
	}

	keyed := byName["previous-owner"]
	if keyed.SSHKeys != 1 {
		t.Errorf("previous-owner reports %d keys, want 1", keyed.SSHKeys)
	}
	if !keyed.Reachable {
		t.Error("an account with an SSH key is not reported as reachable")
	}
	if !strings.Contains(keyed.Summary, "SSH key") {
		t.Errorf("summary does not mention the key: %q", keyed.Summary)
	}

	pw := byName["their-friend"]
	if !strings.Contains(pw.Summary, "password set") {
		t.Errorf("summary does not mention the password: %q", pw.Summary)
	}
}

// Property 2: the account this wizard just created is never offered for removal.
//
// A screen with a checkbox next to the recovery account, on the step immediately
// before root is locked behind it, would be a bug with a bricked node at the end.
// The filtering happens where the list is built, not in the UI.
func TestOwnRecoveryAccountIsNeverOffered(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.setupThroughUser(t)

	got, err := e.w.PriorAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range got {
		if a.Username == "rescue" {
			t.Fatalf("the wizard's own recovery account was offered for removal: %+v", a)
		}
	}
	if len(got) != 1 || got[0].Username != "previous-owner" {
		t.Errorf("listed %+v, want only the inherited account", got)
	}

	// And asking for it by name is refused even so, in case a client sends one.
	outcomes, err := e.w.RemovePriorAdmins(context.Background(),
		wizard.RemovePriorAdminsRequest{Usernames: []string{"rescue"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || outcomes[0].Removed {
		t.Fatalf("outcomes = %+v, want a refusal", outcomes)
	}
	if !strings.Contains(outcomes[0].Error, "recovery account") {
		t.Errorf("the refusal does not say why: %q", outcomes[0].Error)
	}
	if _, ok := e.fake.User("rescue"); !ok {
		t.Fatal("the recovery account was removed")
	}
}

// Property 3: nothing is removed unless it is named. The UI defaults every box to
// unchecked, and the empty submission is the normal one.
func TestNothingIsRemovedByDefault(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.setupThroughUser(t)

	outcomes, err := e.w.RemovePriorAdmins(context.Background(), wizard.RemovePriorAdminsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 0 {
		t.Errorf("outcomes = %+v for an empty submission", outcomes)
	}
	if _, ok := e.fake.User("previous-owner"); !ok {
		t.Error("an account was removed without being ticked")
	}
}

// Property 4: a ticked account is removed, and disappears from the list.
func TestTickedAccountsAreRemoved(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.seedPriorOwner(t, "their-friend", "")
	e.setupThroughUser(t)

	outcomes, err := e.w.RemovePriorAdmins(context.Background(),
		wizard.RemovePriorAdminsRequest{Usernames: []string{"previous-owner"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcomes) != 1 || !outcomes[0].Removed || outcomes[0].Error != "" {
		t.Fatalf("outcomes = %+v", outcomes)
	}
	if _, ok := e.fake.User("previous-owner"); ok {
		t.Error("the account is still there")
	}
	// The one that was not ticked is untouched.
	if _, ok := e.fake.User("their-friend"); !ok {
		t.Error("an unticked account was removed")
	}

	after, err := e.w.PriorAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 || after[0].Username != "their-friend" {
		t.Errorf("after removal the list is %+v", after)
	}
}

// Property 5: a removal that fails is reported per-account and does not block
// setup.
//
// This step sits between "I have a recovery account" and "root is locked", which
// is the worst place to strand an operator. An account with a live session must
// not leave them there.
func TestRemovalFailuresDoNotBlockSetup(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.seedPriorOwner(t, "stubborn", "")
	e.setupThroughUser(t)
	e.fake.BusyUsers["stubborn"] = true

	outcomes, err := e.w.RemovePriorAdmins(context.Background(),
		wizard.RemovePriorAdminsRequest{Usernames: []string{"previous-owner", "stubborn"}})
	if err != nil {
		t.Fatalf("one stubborn account failed the whole call: %v", err)
	}
	byName := map[string]wizard.RemovalOutcome{}
	for _, o := range outcomes {
		byName[o.Username] = o
	}
	if !byName["previous-owner"].Removed {
		t.Error("the removable account was not removed")
	}
	if byName["stubborn"].Removed {
		t.Error("the busy account was reported as removed")
	}
	if !strings.Contains(byName["stubborn"].Error, "running processes") {
		t.Errorf("the failure does not say why: %q", byName["stubborn"].Error)
	}
	if strings.Contains(byName["stubborn"].Error, "privhelper:") {
		t.Errorf("the failure still carries its wire prefix: %q", byName["stubborn"].Error)
	}

	// And setup finishes regardless.
	ctx := context.Background()
	if _, err := e.w.InstallKey(ctx, wizard.KeyRequest{PublicKey: privtest.SSHPublicKey(2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.Lock(ctx, wizard.LockRequest{}); err != nil {
		t.Fatalf("setup could not finish after a failed removal: %v", err)
	}
	if !e.w.Provisioned() {
		t.Error("the node was not provisioned")
	}
}

// Property 6: removal is refused before a recovery account exists. Deleting the
// only administrator on the node before creating a replacement is the ordering
// that makes it unadministrable.
func TestRemovalRefusedBeforeARecoveryAccountExists(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))

	_, err := e.w.RemovePriorAdmins(context.Background(),
		wizard.RemovePriorAdminsRequest{Usernames: []string{"previous-owner"}})
	if privhelper.CodeOf(err) != privhelper.CodeConflict {
		t.Fatalf("code = %q (err %v), want %q", privhelper.CodeOf(err), err, privhelper.CodeConflict)
	}
	if _, ok := e.fake.User("previous-owner"); !ok {
		t.Error("the account was removed anyway")
	}
}

// Property 7: the HTTP surface serves the list with the copy that gives it
// meaning, and reports per-account outcomes with 200.
func TestPriorAdminHTTP(t *testing.T) {
	e := newAdminEnv(t)
	e.seedPriorOwner(t, "previous-owner", privtest.SSHPublicKey(9))
	e.setupThroughUser(t)
	h := e.w.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/setup/prior-admins", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d (%s)", rec.Code, rec.Body.String())
	}
	var listed struct {
		Admins []wizard.PriorAdmin `json:"admins"`
		Notice string              `json:"notice"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Admins) != 1 {
		t.Errorf("listed %+v", listed.Admins)
	}
	// The copy has to say what these accounts are, what they can do, and that
	// reflashing is the only certainty.
	for _, want := range []string{"administrator", "previous owner", "SSH key", "reflash"} {
		if !strings.Contains(strings.ToLower(listed.Notice), strings.ToLower(want)) {
			t.Errorf("the notice does not mention %q: %q", want, listed.Notice)
		}
	}
	if !strings.Contains(listed.Notice, "unless you tick it") {
		t.Errorf("the notice does not say removal is opt-in: %q", listed.Notice)
	}

	body, _ := json.Marshal(wizard.RemovePriorAdminsRequest{Usernames: []string{"previous-owner"}})
	req := httptest.NewRequest("POST", "/api/setup/prior-admins", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d (%s)", rec.Code, rec.Body.String())
	}
	var removed struct {
		Outcomes []wizard.RemovalOutcome `json:"outcomes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &removed); err != nil {
		t.Fatal(err)
	}
	if len(removed.Outcomes) != 1 || !removed.Outcomes[0].Removed {
		t.Errorf("outcomes = %+v", removed.Outcomes)
	}
}

// Property 8: a node with no prior administrators lists nothing, which is what a
// freshly flashed board looks like and the case the screen should stay out of the
// way for.
func TestFreshNodeHasNoPriorAdmins(t *testing.T) {
	e := newAdminEnv(t)
	e.setupThroughUser(t)

	got, err := e.w.PriorAdmins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("a fresh node listed %+v", got)
	}
}
