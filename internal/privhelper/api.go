package privhelper

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Provisioner is everything waypointd is allowed to ask the privileged helper to
// do. It is deliberately small and deliberately concrete: no "run this command",
// no "write this file", no escape hatch. Each method names one operation with
// validated arguments, which is what makes the socket a boundary rather than a
// remote shell.
//
// Every method takes a context. These calls run useradd, nmcli, and hostnamectl —
// all of which can block indefinitely on a wedged system — and the caller is
// usually an HTTP handler with a deadline of its own.
//
// # Ordering
//
// Two orderings are contracts, not conventions, and implementations enforce them:
//
//   - CreateRecoveryUser must succeed before LockRoot. Locking root on a node with
//     no other administrative account is how a hotspot becomes a coaster.
//   - A checkpoint must be created before NetJoin if the join is to be revertible.
//     NetJoin does not checkpoint on its own; the caller composes the two, because
//     only the caller knows whether it is on the link it is about to change.
type Provisioner interface {
	// SetHostname renames the host. This is the one setting the wizard refuses to
	// leave at its default: every node answering to raspberrypi.local collides on
	// the same LAN, and the TLS certificate's SANs are minted from the name.
	SetHostname(ctx context.Context, req SetHostnameRequest) (SetHostnameResponse, error)

	// CreateRecoveryUser creates the non-root account that can recover the node
	// when the operator has lost their dashboard credentials. It is the account
	// that makes LockRoot safe.
	CreateRecoveryUser(ctx context.Context, req CreateRecoveryUserRequest) (CreateRecoveryUserResponse, error)

	// InstallSSHKey appends a public key to a user's authorized_keys. Key-based
	// access is the recommended shape for the recovery account — a key the
	// operator already has cannot be forgotten the way a password set once during
	// setup can.
	InstallSSHKey(ctx context.Context, req InstallSSHKeyRequest) (InstallSSHKeyResponse, error)

	// EnableSSH turns the SSH daemon on or off, and optionally sets whether it
	// accepts passwords.
	EnableSSH(ctx context.Context, req EnableSSHRequest) (EnableSSHResponse, error)

	// LockRoot locks the root account's password. There is deliberately no
	// unlock: recovery runs through the recovery user's sudo, and an RPC that can
	// re-enable root login would undo the reason this one exists.
	LockRoot(ctx context.Context, req LockRootRequest) (LockRootResponse, error)

	// APUp raises the setup access point — the way a headless node with no Wi-Fi
	// credentials is reachable at all. Without it, first-boot setup on a node with
	// no Ethernet has no first step.
	APUp(ctx context.Context, req APUpRequest) (APUpResponse, error)

	// APDown tears the setup access point down, normally once the node has joined
	// the operator's network and been confirmed reachable on it.
	APDown(ctx context.Context, req APDownRequest) (APDownResponse, error)

	// NetScan lists the wireless networks the node can see, so the operator picks
	// their network from a list instead of typing its name from memory into a
	// phone keyboard. A mistyped SSID and a wrong password fail identically —
	// the join simply does not associate — so removing the chance to mistype one
	// of them is the difference between an error the operator can act on and a
	// guess.
	NetScan(ctx context.Context, req NetScanRequest) (NetScanResponse, error)

	// NetJoin joins a Wi-Fi network, writing the managed profile and waiting for
	// the association to come up (or not) within the request's timeout.
	NetJoin(ctx context.Context, req NetJoinRequest) (NetJoinResponse, error)

	// NetCheckpointCreate snapshots the current network state and returns a
	// handle. It exists because the operator is usually changing the network over
	// the network: without a checkpoint, one wrong SSID ends the session that
	// would have fixed it.
	NetCheckpointCreate(ctx context.Context, req NetCheckpointCreateRequest) (NetCheckpointCreateResponse, error)

	// NetCheckpointDestroy discards a snapshot, making the change permanent.
	NetCheckpointDestroy(ctx context.Context, req NetCheckpointDestroyRequest) (NetCheckpointDestroyResponse, error)

	// NetCheckpointRollback restores a snapshot, undoing the change.
	NetCheckpointRollback(ctx context.Context, req NetCheckpointRollbackRequest) (NetCheckpointRollbackResponse, error)

	// ListSudoUsers reports the human accounts that can already become root on
	// this node. It exists for second-hand hardware: a board bought from a
	// hamfest, inherited from a silent key, or reflashed onto a card that was not
	// wiped can carry a previous owner's administrator account, and the operator
	// setting it up has no way to know without being told.
	ListSudoUsers(ctx context.Context, req ListSudoUsersRequest) (ListSudoUsersResponse, error)

	// RemoveUser deletes an account and its home directory. It is the other half
	// of ListSudoUsers and is deliberately narrow: it refuses root under any
	// name, refuses an account with running processes, and never forces.
	RemoveUser(ctx context.Context, req RemoveUserRequest) (RemoveUserResponse, error)
}

// The three NetCheckpoint methods are exactly the shape of netconfig.Checkpoint
// (Create/Destroy/Rollback over an opaque handle), so a thin adapter lets a
// Provisioner back the existing confirm-or-revert Guard without that state machine
// learning anything about this socket.

// --- SetHostname ---------------------------------------------------------

// SetHostnameRequest renames the host.
type SetHostnameRequest struct {
	// Hostname is a single DNS label — no dots. See ValidateHostname.
	Hostname string `json:"hostname"`
	// UpdateHosts rewrites the 127.0.1.1 line in /etc/hosts to match. Leaving it
	// stale is why sudo pauses for ten seconds on a renamed Debian box.
	UpdateHosts bool `json:"update_hosts"`
}

// Validate checks the request.
func (r SetHostnameRequest) Validate() error { return ValidateHostname(r.Hostname) }

// SetHostnameResponse reports the rename.
type SetHostnameResponse struct {
	Previous string `json:"previous"`
	Hostname string `json:"hostname"`
	// Changed is false when the host already had this name, so a retried wizard
	// step is not reported as a change.
	Changed bool `json:"changed"`
}

// --- CreateRecoveryUser --------------------------------------------------

// CreateRecoveryUserRequest creates the recovery account.
type CreateRecoveryUserRequest struct {
	// Username must not be root and must not collide with a system account. See
	// ValidateUsername — and note that name-checking is necessary but not
	// sufficient: an implementation must also refuse any existing account and any
	// name that resolves to uid 0, because "root" is a uid, not a spelling.
	Username string `json:"username"`

	// Password is optional and, when empty, produces a key-only account: the
	// password is locked and the account is reachable only with an SSH key. That
	// is the recommended shape, and the wizard should default to it — a key the
	// operator already holds cannot be forgotten the way a password typed once
	// during setup can.
	//
	// When set, it crosses the socket in the clear. That is acceptable only
	// because the socket is AF_UNIX, mode 0660, on tmpfs — it never touches the
	// network and never touches the SD card. It must never be logged; String
	// below redacts it, and implementations must not format the raw struct.
	Password string `json:"password,omitempty"`

	// PasswordHash is a pre-computed crypt(3) string, as `openssl passwd -6` or
	// Raspberry Pi Imager produce. It is the better form for a password that has
	// to be written down somewhere — a boot-partition file, a provisioning
	// template — because the file then holds a hash rather than the operator's
	// actual password. Mutually exclusive with Password.
	PasswordHash string `json:"password_hash,omitempty"`

	// Sudo grants passwordless-free sudo via the sudo group. The recovery user
	// needs it — an account that cannot become root cannot recover a node whose
	// root is locked.
	Sudo bool `json:"sudo"`

	// SSHKey, when set, is installed in one step with the account so a key-only
	// user is never briefly created without its key.
	SSHKey string `json:"ssh_key,omitempty"`
}

// Validate checks the request.
func (r CreateRecoveryUserRequest) Validate() error {
	if err := ValidateUsername(r.Username); err != nil {
		return err
	}
	if r.Password != "" && r.PasswordHash != "" {
		return Errorf(CodeInvalidArgument,
			"give either a password or a password hash for %q, not both", r.Username)
	}
	if r.Password != "" {
		if err := ValidatePassword(r.Password); err != nil {
			return err
		}
	}
	if r.PasswordHash != "" {
		if err := ValidatePasswordHash(r.PasswordHash); err != nil {
			return err
		}
	}
	if r.SSHKey != "" {
		if err := ValidateSSHKey(r.SSHKey); err != nil {
			return err
		}
	}
	// A user with neither a password nor a key cannot log in at all, which makes
	// it useless as a recovery account — exactly the failure this account exists
	// to prevent, so it is refused rather than silently created.
	if r.Password == "" && r.PasswordHash == "" && r.SSHKey == "" {
		return Errorf(CodeInvalidArgument,
			"recovery user %q would have neither a password nor an SSH key and could never log in", r.Username)
	}
	return nil
}

// String redacts the password so the request can be logged. Implementations must
// use this rather than formatting the struct.
func (r CreateRecoveryUserRequest) String() string {
	pw := "none"
	switch {
	case r.Password != "":
		pw = "[REDACTED]"
	case r.PasswordHash != "":
		// A hash is not the operator's password, but it is still the credential an
		// offline cracker would work on, so it does not go in a log either.
		pw = "[REDACTED hash]"
	}
	key := "none"
	if r.SSHKey != "" {
		key = "set"
	}
	return fmt.Sprintf("CreateRecoveryUser{username:%q password:%s sudo:%v ssh_key:%s}",
		r.Username, pw, r.Sudo, key)
}

// CreateRecoveryUserResponse reports the created account.
type CreateRecoveryUserResponse struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	Home     string `json:"home"`
	// Created is false when the account already existed and was left alone.
	Created bool `json:"created"`
	// PasswordLocked reports a key-only account.
	PasswordLocked bool `json:"password_locked"`
	Sudo           bool `json:"sudo"`
	// SSHKeyFingerprint is set when a key was installed alongside the account.
	SSHKeyFingerprint string `json:"ssh_key_fingerprint,omitempty"`
}

// --- InstallSSHKey -------------------------------------------------------

// InstallSSHKeyRequest adds a public key to a user's authorized_keys.
type InstallSSHKeyRequest struct {
	Username  string `json:"username"`
	PublicKey string `json:"public_key"`
	// Replace overwrites authorized_keys instead of appending. Appending is the
	// default because an operator adding a second laptop should not lose the first.
	Replace bool `json:"replace"`
}

// Validate checks the request.
func (r InstallSSHKeyRequest) Validate() error {
	if err := ValidateUsername(r.Username); err != nil {
		return err
	}
	return ValidateSSHKey(r.PublicKey)
}

// InstallSSHKeyResponse reports the installed key.
type InstallSSHKeyResponse struct {
	Username string `json:"username"`
	// Fingerprint is the OpenSSH SHA256 form, for display next to the key so an
	// operator can confirm it is the one they meant.
	Fingerprint string `json:"fingerprint"`
	// Added is false when the key was already present — installing twice is a
	// no-op, not a duplicate line.
	Added bool `json:"added"`
	// KeyCount is how many keys the file holds afterwards.
	KeyCount int `json:"key_count"`
}

// --- EnableSSH -----------------------------------------------------------

// EnableSSHRequest turns the SSH daemon on or off.
type EnableSSHRequest struct {
	Enabled bool `json:"enabled"`
	// PasswordAuth is tri-state: nil leaves sshd's current setting alone. It is a
	// pointer rather than a bool because "false" and "don't touch it" are
	// different requests, and conflating them would silently disable password
	// logins on every call that only meant to start the service.
	PasswordAuth *bool `json:"password_auth,omitempty"`
}

// Validate checks the request. There is nothing to reject: both fields are
// booleans. It exists so every request type answers the same interface and a
// server can validate uniformly.
func (r EnableSSHRequest) Validate() error { return nil }

// EnableSSHResponse reports the daemon's state.
type EnableSSHResponse struct {
	Enabled      bool `json:"enabled"`
	PasswordAuth bool `json:"password_auth"`
	Changed      bool `json:"changed"`
}

// --- LockRoot ------------------------------------------------------------

// LockRootRequest locks the root password. It has no fields: there is one way to
// do this and no way to undo it.
type LockRootRequest struct{}

// Validate checks the request.
func (r LockRootRequest) Validate() error { return nil }

// LockRootResponse reports the lock.
type LockRootResponse struct {
	Locked bool `json:"locked"`
	// Changed is false when root was already locked.
	Changed bool `json:"changed"`
}

// --- APUp / APDown -------------------------------------------------------

// APUpRequest raises the setup access point.
type APUpRequest struct {
	SSID string `json:"ssid"`
	// Passphrase, when empty, brings up an open network. Open is a defensible
	// default for a setup AP that exists for minutes and serves an HTTPS page with
	// its own claim gate, but it is a choice the caller makes explicitly.
	Passphrase string `json:"passphrase,omitempty"`
	// Interface, when empty, lets the implementation pick the wireless device.
	Interface string `json:"interface,omitempty"`
	// Channel, when zero, lets the implementation choose. 2.4 GHz only: the
	// operator's phone has to see it, and a Pi Zero W has no 5 GHz radio.
	Channel int `json:"channel,omitempty"`
	// Country is the two-letter regulatory domain. Without it the radio is
	// restricted to the world-safe subset, which on some hardware means no AP.
	Country string `json:"country,omitempty"`
	// AddressCIDR is the address the node takes on the AP, e.g. 192.168.66.1/24.
	// Empty means the implementation's default.
	AddressCIDR string `json:"address_cidr,omitempty"`
}

// Validate checks the request.
func (r APUpRequest) Validate() error {
	if err := ValidateSSID(r.SSID); err != nil {
		return err
	}
	if r.Passphrase != "" {
		if err := ValidateWiFiPassphrase(r.Passphrase); err != nil {
			return err
		}
	}
	if err := validateCountry(r.Country); err != nil {
		return err
	}
	if r.Channel != 0 && (r.Channel < 1 || r.Channel > 14) {
		return Errorf(CodeInvalidArgument, "AP channel %d is outside the 2.4 GHz range (1–14)", r.Channel)
	}
	if r.AddressCIDR != "" {
		if err := validateCIDR(r.AddressCIDR); err != nil {
			return err
		}
	}
	return nil
}

// String redacts the passphrase so the request can be logged.
func (r APUpRequest) String() string {
	pass := "open"
	if r.Passphrase != "" {
		pass = "[REDACTED]"
	}
	return fmt.Sprintf("APUp{ssid:%q passphrase:%s iface:%q channel:%d country:%q addr:%q}",
		r.SSID, pass, r.Interface, r.Channel, r.Country, r.AddressCIDR)
}

// APUpResponse reports the running access point.
type APUpResponse struct {
	SSID        string `json:"ssid"`
	Interface   string `json:"interface"`
	AddressCIDR string `json:"address_cidr"`
	Active      bool   `json:"active"`
	Changed     bool   `json:"changed"`
}

// APDownRequest tears the setup access point down.
type APDownRequest struct {
	// Interface, when empty, means whichever interface the AP is on.
	Interface string `json:"interface,omitempty"`
}

// Validate checks the request.
func (r APDownRequest) Validate() error { return nil }

// APDownResponse reports the teardown.
type APDownResponse struct {
	Active bool `json:"active"`
	// Changed is false when no AP was running.
	Changed bool `json:"changed"`
}

// --- NetJoin -------------------------------------------------------------

// NetJoinRequest joins a Wi-Fi network.
// --- NetScan -------------------------------------------------------------

// NetScanRequest asks what wireless networks the node can see.
type NetScanRequest struct {
	// Interface is optional; empty lets the implementation choose.
	Interface string `json:"interface,omitempty"`
	// Rescan asks for a fresh sweep rather than the supplicant's cached results.
	//
	// It defaults to false, and during setup it must stay false. A node running
	// the setup access point is using its only radio to do so; asking brcmfmac to
	// leave the channel and sweep the band would interrupt the network the
	// operator is currently connected over — to fetch a list for a form they are
	// looking at right now. The cache NetworkManager built before the access point
	// went up is what the wizard wants.
	Rescan bool `json:"rescan,omitempty"`
}

// Validate accepts anything. Both fields are optional, neither reaches a shell —
// the interface name is passed as an argv element to nmcli — and an interface
// that does not exist is reported by nmcli far more usefully than a regex here
// could. APUpRequest treats its own Interface field the same way. This exists so
// the type satisfies the same contract as its siblings.
func (r NetScanRequest) Validate() error { return nil }

// ScanNetwork is one visible wireless network.
type ScanNetwork struct {
	SSID string `json:"ssid"`
	// Signal is 0-100 as NetworkManager reports it.
	Signal int `json:"signal"`
	// Security is a short description ("WPA2", "open", …) so the wizard can warn
	// before an operator types a password onto an open network.
	Security string `json:"security,omitempty"`
	// InUse marks the network this node is already on.
	InUse bool `json:"in_use,omitempty"`
}

// Open reports a network with no protection.
func (n ScanNetwork) Open() bool { return n.Security == "" || n.Security == "open" }

// NetScanResponse is what the node can see, strongest first.
type NetScanResponse struct {
	Networks []ScanNetwork `json:"networks"`
	// Cached reports that these came from the supplicant's existing results
	// rather than a fresh sweep, which is the normal case while the setup access
	// point holds the radio. The wizard says so rather than implying the list is
	// live.
	Cached bool `json:"cached,omitempty"`
}

type NetJoinRequest struct {
	SSID string `json:"ssid"`
	// PSK, when empty, joins an open network.
	PSK string `json:"psk,omitempty"`
	// Hidden drives a scan-ssid join for a network that does not beacon.
	Hidden    bool   `json:"hidden"`
	Country   string `json:"country,omitempty"`
	Interface string `json:"interface,omitempty"`
	// TimeoutSeconds bounds the wait for association. Zero means the
	// implementation's default. It is separate from the context deadline because
	// it is the operator-visible "how long before we call this a failure", and it
	// wants to be shorter than the HTTP timeout above it.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
}

// Validate checks the request.
func (r NetJoinRequest) Validate() error {
	if err := ValidateSSID(r.SSID); err != nil {
		return err
	}
	if r.PSK != "" {
		if err := ValidateWiFiPassphrase(r.PSK); err != nil {
			return err
		}
	}
	if err := validateCountry(r.Country); err != nil {
		return err
	}
	if r.TimeoutSeconds < 0 {
		return Errorf(CodeInvalidArgument, "timeout_seconds %d is negative", r.TimeoutSeconds)
	}
	return nil
}

// String redacts the PSK so the request can be logged.
func (r NetJoinRequest) String() string {
	psk := "open"
	if r.PSK != "" {
		psk = "[REDACTED]"
	}
	return fmt.Sprintf("NetJoin{ssid:%q psk:%s hidden:%v iface:%q country:%q timeout:%ds}",
		r.SSID, psk, r.Hidden, r.Interface, r.Country, r.TimeoutSeconds)
}

// NetJoinResponse reports the join.
type NetJoinResponse struct {
	SSID      string `json:"ssid"`
	Interface string `json:"interface"`
	// Connected is the whole answer: the profile was written and the association
	// came up within the timeout.
	Connected bool `json:"connected"`
	// IPv4 is the address acquired, when one was.
	IPv4 string `json:"ipv4,omitempty"`
	// Profile is the managed connection profile written, so a later teardown
	// knows what it owns.
	Profile string `json:"profile,omitempty"`
}

// --- NetCheckpoint* ------------------------------------------------------

// NetCheckpointCreateRequest snapshots the network state.
type NetCheckpointCreateRequest struct {
	// TimeoutSeconds is the intended rollback window. An implementation may arm a
	// native backstop from it (NetworkManager can roll back on its own timer even
	// if waypointd dies), but the caller's timer stays authoritative.
	TimeoutSeconds int `json:"timeout_seconds"`
}

// Validate checks the request.
func (r NetCheckpointCreateRequest) Validate() error {
	if r.TimeoutSeconds <= 0 {
		return Errorf(CodeInvalidArgument,
			"checkpoint timeout_seconds must be positive (a checkpoint with no window is not a rollback)")
	}
	if d := time.Duration(r.TimeoutSeconds) * time.Second; d > MaxCheckpointWindow {
		return Errorf(CodeInvalidArgument,
			"checkpoint timeout %s exceeds the maximum %s", d, MaxCheckpointWindow)
	}
	return nil
}

// MaxCheckpointWindow bounds a rollback window. A checkpoint holds the pre-change
// network state hostage until it is destroyed or rolled back; an hours-long one is
// a stuck node waiting to happen, not a longer safety net.
const MaxCheckpointWindow = 30 * time.Minute

// NetCheckpointCreateResponse carries the handle.
type NetCheckpointCreateResponse struct {
	Handle    string    `json:"handle"`
	ExpiresAt time.Time `json:"expires_at"`
}

// NetCheckpointDestroyRequest discards a snapshot.
type NetCheckpointDestroyRequest struct {
	Handle string `json:"handle"`
}

// Validate checks the request.
func (r NetCheckpointDestroyRequest) Validate() error { return validateHandle(r.Handle) }

// NetCheckpointDestroyResponse reports the discard.
type NetCheckpointDestroyResponse struct {
	Destroyed bool `json:"destroyed"`
}

// NetCheckpointRollbackRequest restores a snapshot.
type NetCheckpointRollbackRequest struct {
	Handle string `json:"handle"`
}

// Validate checks the request.
func (r NetCheckpointRollbackRequest) Validate() error { return validateHandle(r.Handle) }

// NetCheckpointRollbackResponse reports the restore.
type NetCheckpointRollbackResponse struct {
	RolledBack bool `json:"rolled_back"`
}

func validateHandle(h string) error {
	if strings.TrimSpace(h) == "" {
		return Errorf(CodeInvalidArgument, "checkpoint handle is empty")
	}
	if len(h) > 256 {
		return Errorf(CodeInvalidArgument, "checkpoint handle is too long")
	}
	return nil
}

// MinHumanUID is the floor for an account a person logs in as. Debian's adduser
// allocates from 1000 up and reserves everything below for system accounts, so
// this is the line between "somebody's login" and "a daemon's identity" — and the
// filter that keeps ListSudoUsers from offering to delete the sudo group's own
// machinery.
const MinHumanUID = 1000

// --- ListSudoUsers -------------------------------------------------------

// ListSudoUsersRequest enumerates administrator accounts.
type ListSudoUsersRequest struct{}

// Validate checks the request.
func (r ListSudoUsersRequest) Validate() error { return nil }

// SudoUser is one account that can become root.
type SudoUser struct {
	Username string `json:"username"`
	UID      int    `json:"uid"`
	Home     string `json:"home"`
	Shell    string `json:"shell"`

	// HasPassword reports a usable password; PasswordLocked reports one that is
	// set but locked. They are separate because "locked" and "never set" are
	// different states an operator may want to act on differently.
	HasPassword    bool `json:"has_password"`
	PasswordLocked bool `json:"password_locked"`
	// SSHKeys is how many authorized keys the account carries. A non-zero count on
	// an account the current operator does not recognise is the most important
	// thing on this screen: it is standing, passwordless remote access.
	SSHKeys int `json:"ssh_keys"`
	// NOPASSWDSudo reports that Waypoint's own sudoers drop-in covers this
	// account — so an operator can tell an account this node created from one it
	// inherited.
	NOPASSWDSudo bool `json:"nopasswd_sudo"`
	// LoginShell reports whether the account can be logged into at all.
	LoginShell bool `json:"login_shell"`
}

// Reachable reports whether somebody could actually get in as this account. An
// account with no password and no keys is a name in a file; one with either is a
// way into a node with root-equivalent rights.
func (u SudoUser) Reachable() bool {
	return u.LoginShell && ((u.HasPassword && !u.PasswordLocked) || u.SSHKeys > 0)
}

// Summary renders the credential state in a line an operator can act on.
func (u SudoUser) Summary() string {
	var parts []string
	switch {
	case u.HasPassword && !u.PasswordLocked:
		parts = append(parts, "password set")
	case u.PasswordLocked:
		parts = append(parts, "password locked")
	default:
		parts = append(parts, "no password")
	}
	switch {
	case u.SSHKeys == 1:
		parts = append(parts, "1 SSH key")
	case u.SSHKeys > 1:
		parts = append(parts, fmt.Sprintf("%d SSH keys", u.SSHKeys))
	default:
		parts = append(parts, "no SSH keys")
	}
	if u.NOPASSWDSudo {
		parts = append(parts, "passwordless sudo")
	}
	if !u.LoginShell {
		parts = append(parts, "no login shell")
	}
	if !u.Reachable() {
		parts = append(parts, "not reachable")
	}
	return strings.Join(parts, ", ")
}

// ListSudoUsersResponse carries the accounts found.
type ListSudoUsersResponse struct {
	Users []SudoUser `json:"users"`
}

// --- RemoveUser ----------------------------------------------------------

// RemoveUserRequest deletes an account.
type RemoveUserRequest struct {
	Username string `json:"username"`
	// RemoveHome deletes the home directory too. Default false is deliberate:
	// deleting somebody's files is a bigger act than removing their login, and the
	// caller should have to say so.
	RemoveHome bool `json:"remove_home"`
}

// Validate checks the request. The username rules are the same ones that govern
// creating an account, so a name this build would refuse to create is a name it
// refuses to delete — which keeps "root" out by construction, before the
// implementation's uid check.
func (r RemoveUserRequest) Validate() error { return ValidateUsername(r.Username) }

// RemoveUserResponse reports the deletion.
type RemoveUserResponse struct {
	Username string `json:"username"`
	// Removed is false when the account did not exist, which is success: the
	// caller wanted it gone and it is.
	Removed     bool `json:"removed"`
	HomeRemoved bool `json:"home_removed"`
	// SudoersPruned reports that the account was dropped from Waypoint's sudoers
	// drop-in, so a deleted name cannot come back with root by being recreated.
	SudoersPruned bool `json:"sudoers_pruned"`
}

// Validator is implemented by every request type. A server validates through this
// interface so adding a method cannot accidentally add an unvalidated one.
type Validator interface {
	Validate() error
}

var (
	_ Validator = SetHostnameRequest{}
	_ Validator = CreateRecoveryUserRequest{}
	_ Validator = InstallSSHKeyRequest{}
	_ Validator = EnableSSHRequest{}
	_ Validator = LockRootRequest{}
	_ Validator = APUpRequest{}
	_ Validator = APDownRequest{}
	_ Validator = NetJoinRequest{}
	_ Validator = NetCheckpointCreateRequest{}
	_ Validator = NetCheckpointDestroyRequest{}
	_ Validator = NetCheckpointRollbackRequest{}
	_ Validator = ListSudoUsersRequest{}
	_ Validator = RemoveUserRequest{}
)
