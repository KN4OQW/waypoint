// Package privtest provides an in-memory Provisioner and the conformance suite
// every implementation of it must pass.
//
// It is a separate package for the same reason net/http/httptest is: the
// conformance suite imports testing, and nothing that imports testing should be
// reachable from the shipped daemon. The fake lives here too so a test needs one
// import to get both.
package privtest

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/privhelper"
)

// DefaultHostname is what a Fake starts out called: the name Raspberry Pi OS Lite
// ships and the Waypoint image does not override. Starting here rather than at ""
// means a test that forgets to set a hostname exercises the same state a real
// freshly flashed node is in.
const DefaultHostname = "raspberrypi"

// FakeUser is an account the Fake believes exists.
type FakeUser struct {
	Name           string
	UID            int
	Home           string
	Sudo           bool
	PasswordLocked bool
	Keys           []string
}

// FakeAP is the access point the Fake believes is up.
type FakeAP struct {
	SSID        string
	Interface   string
	AddressCIDR string
	HasPassword bool
}

// FakeJoin is the network the Fake believes it joined.
type FakeJoin struct {
	SSID      string
	Interface string
	Profile   string
	IPv4      string
}

// Call is one recorded invocation. The request is stored as the concrete request
// type, so a test can type-assert it and assert on fields.
type Call struct {
	Method  privhelper.Method
	Request any
}

// Fake is an in-memory Provisioner.
//
// It is a fake rather than a stub: it validates every request exactly as a real
// implementation must, enforces the ordering contracts (no LockRoot without a
// recovery user), and models enough state to answer idempotently — a second
// SetHostname to the same name reports Changed false, a second InstallSSHKey of
// the same key reports Added false. A test that passes against this fake is
// therefore testing the caller's behaviour against the real contract, not against
// a shape that always says yes.
//
// The zero value is not usable; call NewFake.
type Fake struct {
	mu sync.Mutex

	// Observable state. Read it after the calls under test have returned.
	Hostname     string
	HostsUpdated bool
	Users        map[string]*FakeUser
	RecoveryUser string
	SSHEnabled   bool
	PasswordAuth bool
	RootLocked   bool
	AP           *FakeAP
	Joined       *FakeJoin

	// Calls records every invocation in order, including ones that failed
	// validation, so a test can assert what the caller attempted.
	Calls []Call

	// Errs injects a failure for a method. The Fake still validates and records
	// the call, then returns this instead of acting — the way to exercise a
	// caller's error path without a real broken system.
	Errs map[privhelper.Method]error

	// NetJoinResult, when set, is returned by NetJoin instead of the fake's own
	// success. It exists for the outcomes that are not simply "worked" or
	// "errored" — an association that came up with no DHCP lease is the one that
	// matters, because it looks like success to everything except the operator
	// trying to reach the node afterwards.
	NetJoinResult *privhelper.NetJoinResponse

	// Clock is the time source, injectable so checkpoint expiries are assertable.
	Clock func() time.Time

	checkpoints map[string]time.Time
	nextUID     int
	nextHandle  int
}

// NewFake returns a Fake in the state a freshly flashed node is in: the default
// hostname, no accounts, SSH enabled with passwords accepted (which is what the
// image ships), root not yet locked, no AP, no network joined.
func NewFake() *Fake {
	return &Fake{
		Hostname:     DefaultHostname,
		Users:        map[string]*FakeUser{},
		SSHEnabled:   true,
		PasswordAuth: true,
		Errs:         map[privhelper.Method]error{},
		Clock:        time.Now,
		checkpoints:  map[string]time.Time{},
		nextUID:      1000,
	}
}

var _ privhelper.Provisioner = (*Fake)(nil)

// enter performs the checks every method shares: honour the context, validate the
// request, record the call, and apply any injected failure. It deliberately
// validates before recording nothing — the call is recorded either way, because a
// test asserting "the caller sent a bad hostname" needs to see the attempt.
func (f *Fake) enter(ctx context.Context, m privhelper.Method, req privhelper.Validator) error {
	if err := ctx.Err(); err != nil {
		return privhelper.Errorf(privhelper.CodeInternal, "%s: %v", m, err)
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, Call{Method: m, Request: req})
	injected := f.Errs[m]
	f.mu.Unlock()

	if err := req.Validate(); err != nil {
		return err
	}
	return injected
}

// CallsTo returns the recorded calls to one method.
func (f *Fake) CallsTo(m privhelper.Method) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Call
	for _, c := range f.Calls {
		if c.Method == m {
			out = append(out, c)
		}
	}
	return out
}

// User returns a copy of an account the Fake believes exists.
func (f *Fake) User(name string) (FakeUser, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.Users[name]
	if !ok {
		return FakeUser{}, false
	}
	cp := *u
	cp.Keys = append([]string(nil), u.Keys...)
	return cp, true
}

// --- Provisioner ---------------------------------------------------------

// SetHostname records the new name.
func (f *Fake) SetHostname(ctx context.Context, req privhelper.SetHostnameRequest) (privhelper.SetHostnameResponse, error) {
	if err := f.enter(ctx, privhelper.MethodSetHostname, req); err != nil {
		return privhelper.SetHostnameResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	prev := f.Hostname
	f.Hostname = req.Hostname
	if req.UpdateHosts {
		f.HostsUpdated = true
	}
	return privhelper.SetHostnameResponse{
		Previous: prev,
		Hostname: req.Hostname,
		Changed:  prev != req.Hostname,
	}, nil
}

// CreateRecoveryUser records the account.
func (f *Fake) CreateRecoveryUser(ctx context.Context, req privhelper.CreateRecoveryUserRequest) (privhelper.CreateRecoveryUserResponse, error) {
	if err := f.enter(ctx, privhelper.MethodCreateRecoveryUser, req); err != nil {
		return privhelper.CreateRecoveryUserResponse{}, err
	}

	// Computed before the lock: Fingerprint re-validates, and there is no reason
	// to hold the mutex across it.
	var fp string
	if req.SSHKey != "" {
		var err error
		if fp, err = privhelper.Fingerprint(req.SSHKey); err != nil {
			return privhelper.CreateRecoveryUserResponse{}, err
		}
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.Users[req.Username]; ok {
		return privhelper.CreateRecoveryUserResponse{
			Username: u.Name, UID: u.UID, Home: u.Home,
			Created: false, PasswordLocked: u.PasswordLocked, Sudo: u.Sudo,
		}, nil
	}
	u := &FakeUser{
		Name:           req.Username,
		UID:            f.nextUID,
		Home:           "/home/" + req.Username,
		Sudo:           req.Sudo,
		PasswordLocked: req.Password == "",
	}
	if req.SSHKey != "" {
		u.Keys = append(u.Keys, req.SSHKey)
	}
	f.nextUID++
	f.Users[u.Name] = u
	// The first account created is the recovery user — it is what unblocks LockRoot.
	if f.RecoveryUser == "" {
		f.RecoveryUser = u.Name
	}
	return privhelper.CreateRecoveryUserResponse{
		Username: u.Name, UID: u.UID, Home: u.Home,
		Created: true, PasswordLocked: u.PasswordLocked, Sudo: u.Sudo,
		SSHKeyFingerprint: fp,
	}, nil
}

// InstallSSHKey appends the key to the user's set.
func (f *Fake) InstallSSHKey(ctx context.Context, req privhelper.InstallSSHKeyRequest) (privhelper.InstallSSHKeyResponse, error) {
	if err := f.enter(ctx, privhelper.MethodInstallSSHKey, req); err != nil {
		return privhelper.InstallSSHKeyResponse{}, err
	}
	fp, err := privhelper.Fingerprint(req.PublicKey)
	if err != nil {
		return privhelper.InstallSSHKeyResponse{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.Users[req.Username]
	if !ok {
		return privhelper.InstallSSHKeyResponse{},
			privhelper.Errorf(privhelper.CodeNotFound, "no such user %q", req.Username)
	}
	if req.Replace {
		u.Keys = nil
	}
	for _, k := range u.Keys {
		if k == req.PublicKey {
			return privhelper.InstallSSHKeyResponse{
				Username: u.Name, Fingerprint: fp, Added: false, KeyCount: len(u.Keys),
			}, nil
		}
	}
	u.Keys = append(u.Keys, req.PublicKey)
	return privhelper.InstallSSHKeyResponse{
		Username: u.Name, Fingerprint: fp, Added: true, KeyCount: len(u.Keys),
	}, nil
}

// EnableSSH records the daemon's state.
func (f *Fake) EnableSSH(ctx context.Context, req privhelper.EnableSSHRequest) (privhelper.EnableSSHResponse, error) {
	if err := f.enter(ctx, privhelper.MethodEnableSSH, req); err != nil {
		return privhelper.EnableSSHResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.SSHEnabled != req.Enabled
	f.SSHEnabled = req.Enabled
	if req.PasswordAuth != nil {
		if f.PasswordAuth != *req.PasswordAuth {
			changed = true
		}
		f.PasswordAuth = *req.PasswordAuth
	}
	return privhelper.EnableSSHResponse{
		Enabled: f.SSHEnabled, PasswordAuth: f.PasswordAuth, Changed: changed,
	}, nil
}

// LockRoot locks root, refusing while no recovery account exists.
func (f *Fake) LockRoot(ctx context.Context, req privhelper.LockRootRequest) (privhelper.LockRootResponse, error) {
	if err := f.enter(ctx, privhelper.MethodLockRoot, req); err != nil {
		return privhelper.LockRootResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RecoveryUser == "" {
		return privhelper.LockRootResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: no recovery account exists, so nothing could administer this node afterwards")
	}
	changed := !f.RootLocked
	f.RootLocked = true
	return privhelper.LockRootResponse{Locked: true, Changed: changed}, nil
}

// APUp raises the access point.
func (f *Fake) APUp(ctx context.Context, req privhelper.APUpRequest) (privhelper.APUpResponse, error) {
	if err := f.enter(ctx, privhelper.MethodAPUp, req); err != nil {
		return privhelper.APUpResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	ap := &FakeAP{
		SSID:        req.SSID,
		Interface:   orDefault(req.Interface, "wlan0"),
		AddressCIDR: orDefault(req.AddressCIDR, "192.168.66.1/24"),
		HasPassword: req.Passphrase != "",
	}
	changed := f.AP == nil || *f.AP != *ap
	f.AP = ap
	return privhelper.APUpResponse{
		SSID: ap.SSID, Interface: ap.Interface, AddressCIDR: ap.AddressCIDR,
		Active: true, Changed: changed,
	}, nil
}

// APDown tears the access point down.
func (f *Fake) APDown(ctx context.Context, req privhelper.APDownRequest) (privhelper.APDownResponse, error) {
	if err := f.enter(ctx, privhelper.MethodAPDown, req); err != nil {
		return privhelper.APDownResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	changed := f.AP != nil
	f.AP = nil
	return privhelper.APDownResponse{Active: false, Changed: changed}, nil
}

// NetJoin records the join.
func (f *Fake) NetJoin(ctx context.Context, req privhelper.NetJoinRequest) (privhelper.NetJoinResponse, error) {
	if err := f.enter(ctx, privhelper.MethodNetJoin, req); err != nil {
		return privhelper.NetJoinResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.NetJoinResult != nil {
		out := *f.NetJoinResult
		if out.Connected {
			f.Joined = &FakeJoin{SSID: out.SSID, Interface: out.Interface, Profile: out.Profile, IPv4: out.IPv4}
		}
		return out, nil
	}
	j := &FakeJoin{
		SSID:      req.SSID,
		Interface: orDefault(req.Interface, "wlan0"),
		Profile:   "waypoint-" + req.SSID,
		IPv4:      "192.168.1.50/24",
	}
	f.Joined = j
	return privhelper.NetJoinResponse{
		SSID: j.SSID, Interface: j.Interface, Connected: true,
		IPv4: j.IPv4, Profile: j.Profile,
	}, nil
}

// NetCheckpointCreate hands out a handle.
func (f *Fake) NetCheckpointCreate(ctx context.Context, req privhelper.NetCheckpointCreateRequest) (privhelper.NetCheckpointCreateResponse, error) {
	if err := f.enter(ctx, privhelper.MethodNetCheckpointCreate, req); err != nil {
		return privhelper.NetCheckpointCreateResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextHandle++
	h := fmt.Sprintf("cp-%d", f.nextHandle)
	exp := f.Clock().Add(time.Duration(req.TimeoutSeconds) * time.Second)
	f.checkpoints[h] = exp
	return privhelper.NetCheckpointCreateResponse{Handle: h, ExpiresAt: exp}, nil
}

// NetCheckpointDestroy discards a handle.
func (f *Fake) NetCheckpointDestroy(ctx context.Context, req privhelper.NetCheckpointDestroyRequest) (privhelper.NetCheckpointDestroyResponse, error) {
	if err := f.enter(ctx, privhelper.MethodNetCheckpointDestroy, req); err != nil {
		return privhelper.NetCheckpointDestroyResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.checkpoints[req.Handle]; !ok {
		return privhelper.NetCheckpointDestroyResponse{},
			privhelper.Errorf(privhelper.CodeNotFound, "no such checkpoint %q", req.Handle)
	}
	delete(f.checkpoints, req.Handle)
	return privhelper.NetCheckpointDestroyResponse{Destroyed: true}, nil
}

// NetCheckpointRollback restores and discards a handle.
func (f *Fake) NetCheckpointRollback(ctx context.Context, req privhelper.NetCheckpointRollbackRequest) (privhelper.NetCheckpointRollbackResponse, error) {
	if err := f.enter(ctx, privhelper.MethodNetCheckpointRollback, req); err != nil {
		return privhelper.NetCheckpointRollbackResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.checkpoints[req.Handle]; !ok {
		return privhelper.NetCheckpointRollbackResponse{},
			privhelper.Errorf(privhelper.CodeNotFound, "no such checkpoint %q", req.Handle)
	}
	delete(f.checkpoints, req.Handle)
	// A rollback restores the pre-change network state; for the fake that means
	// forgetting the join the checkpoint was taken to protect against.
	f.Joined = nil
	return privhelper.NetCheckpointRollbackResponse{RolledBack: true}, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
