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
	"sort"
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

	// ScanResult overrides what NetScan reports, so a test can drive an empty
	// list — the case where the radio is busy running the access point and has
	// nothing cached, which the wizard has to handle without stranding the
	// operator on a form with no manual entry.
	ScanResult *privhelper.NetScanResponse
	// Scanned counts NetScan calls, so a test can assert the wizard is not
	// re-scanning on every render.
	Scanned int

	// Clock is the time source, injectable so checkpoint expiries are assertable.
	Clock func() time.Time

	// BusyUsers marks accounts RemoveUser must refuse as having running
	// processes, so a caller's handling of that refusal can be exercised.
	BusyUsers map[string]bool

	protected   string
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
		BusyUsers:    map[string]bool{},
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
		PasswordLocked: req.Password == "" && req.PasswordHash == "",
	}
	if req.SSHKey != "" {
		u.Keys = append(u.Keys, req.SSHKey)
	}
	f.nextUID++
	f.Users[u.Name] = u
	// RecoveryUser records the first account created, for tests that want to name
	// it. It is deliberately NOT what unblocks LockRoot — see hasUsableAdminLocked.
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
	// "Is there a usable administrator" rather than "was one ever created": an
	// account can be removed after the fact, and a fake that remembered the first
	// name it saw would let a caller lock root behind an account that is gone.
	// This mirrors the real hasUsableRecoveryAccount.
	if !f.hasUsableAdminLocked() {
		return privhelper.LockRootResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"refusing to lock root: no recovery account exists, so nothing could administer this node afterwards")
	}
	changed := !f.RootLocked
	f.RootLocked = true
	return privhelper.LockRootResponse{Locked: true, Changed: changed}, nil
}

// hasUsableAdminLocked reports whether some non-root sudo account could actually
// log in. The mutex is already held.
func (f *Fake) hasUsableAdminLocked() bool {
	for _, u := range f.Users {
		if !u.Sudo || u.UID == 0 {
			continue
		}
		if !u.PasswordLocked || len(u.Keys) > 0 {
			return true
		}
	}
	return false
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

// NetScan reports whatever ScanResult holds, defaulting to a small plausible
// list. The default includes an open network, because "the operator picked an
// open network" is a case the wizard has to render a warning for and a fake that
// only ever returns WPA2 would never exercise it.
func (f *Fake) NetScan(ctx context.Context, req privhelper.NetScanRequest) (privhelper.NetScanResponse, error) {
	if err := f.enter(ctx, privhelper.MethodNetScan, req); err != nil {
		return privhelper.NetScanResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scanned++
	if f.ScanResult != nil {
		return *f.ScanResult, nil
	}
	return privhelper.NetScanResponse{
		Cached: !req.Rescan,
		Networks: []privhelper.ScanNetwork{
			{SSID: "DeathStar", Signal: 82, Security: "WPA2"},
			{SSID: "hamshack-2g", Signal: 61, Security: "WPA2"},
			{SSID: "GuestWiFi", Signal: 40, Security: "open"},
		},
	}, nil
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

// ListSudoUsers reports the accounts the Fake believes can become root.
//
// Only accounts with Sudo and a uid at or above the human floor: the point of the
// call is prior *administrator* accounts, and a fake that also returned system
// accounts would let a caller's filtering bug pass unnoticed.
func (f *Fake) ListSudoUsers(ctx context.Context, req privhelper.ListSudoUsersRequest) (privhelper.ListSudoUsersResponse, error) {
	if err := f.enter(ctx, privhelper.MethodListSudoUsers, req); err != nil {
		return privhelper.ListSudoUsersResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []privhelper.SudoUser
	for _, u := range f.Users {
		if !u.Sudo || u.UID < privhelper.MinHumanUID {
			continue
		}
		out = append(out, privhelper.SudoUser{
			Username: u.Name, UID: u.UID, Home: u.Home, Shell: "/bin/bash",
			HasPassword:    !u.PasswordLocked,
			PasswordLocked: u.PasswordLocked,
			SSHKeys:        len(u.Keys),
			NOPASSWDSudo:   u.PasswordLocked, // matches the real rule: key-only accounts get the drop-in
			LoginShell:     true,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return privhelper.ListSudoUsersResponse{Users: out}, nil
}

// ProtectedUser is an account RemoveUser must refuse. It stands in for the
// wizard's own recovery account, which the real implementation is told about by
// the caller.
func (f *Fake) ProtectUser(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.protected = name
}

// RemoveUser deletes an account, refusing the same things the real one refuses.
func (f *Fake) RemoveUser(ctx context.Context, req privhelper.RemoveUserRequest) (privhelper.RemoveUserResponse, error) {
	if err := f.enter(ctx, privhelper.MethodRemoveUser, req); err != nil {
		return privhelper.RemoveUserResponse{}, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	if req.Username == f.protected {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"refusing to remove %q: it is the recovery account this setup just created", req.Username)
	}
	u, ok := f.Users[req.Username]
	if !ok {
		// Absent is success: the caller wanted it gone and it is.
		return privhelper.RemoveUserResponse{Username: req.Username, Removed: false}, nil
	}
	if u.UID == 0 {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeInvalidArgument,
			"%q is uid 0 — refusing to remove root under another name", req.Username)
	}
	if f.BusyUsers[req.Username] {
		return privhelper.RemoveUserResponse{}, privhelper.Errorf(privhelper.CodeConflict,
			"%q has running processes; log it out and try again", req.Username)
	}
	delete(f.Users, req.Username)
	if f.RecoveryUser == req.Username {
		f.RecoveryUser = ""
	}
	return privhelper.RemoveUserResponse{
		Username: req.Username, Removed: true,
		HomeRemoved: req.RemoveHome, SudoersPruned: u.PasswordLocked,
	}, nil
}
