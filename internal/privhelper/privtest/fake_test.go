package privtest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/privhelper/privtest"
)

// The fake is the first implementation of the contract, so it is the first thing
// the conformance suite runs against. When the real socket-backed helper lands it
// runs the same call.
func TestFakeConforms(t *testing.T) {
	privtest.Conform(t, func(t *testing.T) privhelper.Provisioner {
		return privtest.NewFake()
	})
}

// The sweep in the suite is written by hand; this is what keeps it honest as the
// interface grows.
func TestConformanceCoversEveryMethod(t *testing.T) {
	privtest.AssertCoversEveryMethod(t)
}

// Property: a fresh fake is in the state a freshly flashed node is in, defaults
// and all. A fake that started blank would let a caller's "did you rename it?"
// logic pass without ever being exercised.
func TestFakeStartsAtTheShippedDefaults(t *testing.T) {
	f := privtest.NewFake()
	if f.Hostname != privtest.DefaultHostname {
		t.Errorf("Hostname = %q, want %q", f.Hostname, privtest.DefaultHostname)
	}
	if len(f.Users) != 0 || f.RecoveryUser != "" {
		t.Error("a fresh fake already has accounts")
	}
	if f.RootLocked {
		t.Error("a fresh fake has root already locked")
	}
	if !f.SSHEnabled {
		t.Error("SSH is disabled; the image ships it enabled")
	}
	if f.AP != nil || f.Joined != nil {
		t.Error("a fresh fake is already on a network")
	}
}

// Property: the fake records every attempt, including the ones it rejects. A test
// asserting "the wizard tried to keep the default hostname" needs the rejected
// call to be visible.
func TestFakeRecordsRejectedCalls(t *testing.T) {
	f := privtest.NewFake()
	ctx := context.Background()

	if _, err := f.SetHostname(ctx, privhelper.SetHostnameRequest{Hostname: "raspberrypi"}); err == nil {
		t.Fatal("the default hostname was accepted")
	}
	calls := f.CallsTo(privhelper.MethodSetHostname)
	if len(calls) != 1 {
		t.Fatalf("recorded %d calls, want 1", len(calls))
	}
	req, ok := calls[0].Request.(privhelper.SetHostnameRequest)
	if !ok {
		t.Fatalf("recorded request is %T, want SetHostnameRequest", calls[0].Request)
	}
	if req.Hostname != "raspberrypi" {
		t.Errorf("recorded hostname = %q", req.Hostname)
	}
	// And the rejection did not take effect.
	if f.Hostname != privtest.DefaultHostname {
		t.Errorf("Hostname = %q after a rejected call", f.Hostname)
	}
}

// Property: injected failures let a caller's error path be tested without a real
// broken node. The injected error keeps its code, so the caller's classification
// logic is exercised too.
func TestFakeInjectsFailures(t *testing.T) {
	f := privtest.NewFake()
	f.Errs[privhelper.MethodNetJoin] = privhelper.Errorf(privhelper.CodeUnsupported, "no wireless interface")

	_, err := f.NetJoin(context.Background(), privhelper.NetJoinRequest{SSID: "home"})
	if got := privhelper.CodeOf(err); got != privhelper.CodeUnsupported {
		t.Errorf("code = %q (err %v), want %q", got, err, privhelper.CodeUnsupported)
	}
	if f.Joined != nil {
		t.Error("an injected failure still joined the network")
	}
}

// Property: the recovery account the wizard is meant to create — key-only, sudo —
// lands in the fake's state, and unblocks LockRoot.
func TestFakeRecoveryUserUnblocksLockRoot(t *testing.T) {
	f := privtest.NewFake()
	ctx := context.Background()

	if _, err := f.CreateRecoveryUser(ctx, privhelper.CreateRecoveryUserRequest{
		Username: "rescue", SSHKey: privtest.SSHPublicKey(3), Sudo: true}); err != nil {
		t.Fatal(err)
	}
	u, ok := f.User("rescue")
	if !ok {
		t.Fatal("the account was not created")
	}
	if !u.PasswordLocked {
		t.Error("a key-only account should have its password locked")
	}
	if !u.Sudo {
		t.Error("the recovery account has no sudo")
	}
	if len(u.Keys) != 1 {
		t.Errorf("the account has %d keys, want 1", len(u.Keys))
	}
	if _, err := f.LockRoot(ctx, privhelper.LockRootRequest{}); err != nil {
		t.Fatalf("LockRoot after a recovery account: %v", err)
	}
	if !f.RootLocked {
		t.Error("RootLocked = false after a successful LockRoot")
	}
}

// Property: the fake never leaks a password through its recorded calls, because
// the request type redacts itself. A test failure printing %v on a recorded call
// must not put the operator's password in CI output.
func TestFakeRecordedRequestRedactsSecrets(t *testing.T) {
	f := privtest.NewFake()
	const pw = "correct horse battery staple"

	if _, err := f.CreateRecoveryUser(context.Background(), privhelper.CreateRecoveryUserRequest{
		Username: "rescue", Password: pw, Sudo: true}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.CallsTo(privhelper.MethodCreateRecoveryUser) {
		if s := formatted(c.Request); strings.Contains(s, pw) {
			t.Errorf("a recorded call printed the password: %s", s)
		}
	}
}

func formatted(v any) string {
	if s, ok := v.(interface{ String() string }); ok {
		return s.String()
	}
	return ""
}
