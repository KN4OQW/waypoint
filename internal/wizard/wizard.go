// Package wizard is waypointd's first-boot setup flow: the steps a node walks
// before it can be claimed, and the gate that serves them instead of the
// dashboard until they are done.
//
// # Why there is a wizard at all
//
// A Waypoint node used to be configured at flash time, through Raspberry Pi
// Imager's advanced options. Imager 2.x no longer offers those for a locally
// flashed image, so a node that arrives as a .img.xz has no hostname the operator
// chose, no account, and an unlocked root. Setup has to happen on the node, in a
// browser, over the setup access point — which means waypointd has to be able to
// tell "this node has never been set up" from "this node is waiting to be
// claimed", and serve a different surface for each.
//
// # The two gates
//
// Provisioning and claiming are different questions and they nest:
//
//	unprovisioned  -> the setup wizard (this package)
//	provisioned, unclaimed -> the claim flow (RFC-0002)
//	provisioned, claimed -> the dashboard
//
// Provisioning says what the box *is* at the OS level; claiming says who
// administers it. The wizard gate sits outside the auth gate, so before setup is
// finished the auth gate never sees a request at all.
//
// # Resuming
//
// Every step writes its progress before it returns, so a wizard interrupted at
// any point — a dropped Wi-Fi association mid-setup is the normal case, since the
// operator is often changing the very network they are connected over — resumes at
// the first incomplete step rather than starting again. Progress lives in its own
// file; the provisioned marker (internal/provision) is written once, at the end,
// and its presence is what flips the gate.
package wizard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/captive"
	"github.com/KN4OQW/waypoint/internal/privhelper"
	"github.com/KN4OQW/waypoint/internal/provision"
)

// Step is one stage of setup.
type Step string

const (
	// StepHostname names the node. It is first because the operator is reaching
	// the box by address at this point and everything afterwards — the certificate
	// SANs, the mDNS name, the dashboard title — is derived from the answer.
	StepHostname Step = "hostname"
	// StepUser creates the recovery account.
	StepUser Step = "user"
	// StepKey installs an SSH key on it. Skippable when the account has a
	// password, recommended otherwise.
	StepKey Step = "key"
	// StepLock locks root and settles the SSH policy. It is last because it is the
	// step that makes the recovery account load-bearing.
	StepLock Step = "lock"
	// StepClaim is the existing RFC-0002 claim. The wizard does not perform it —
	// it reports it as the next thing to do once provisioning is complete, and the
	// claim gate takes over from there.
	StepClaim Step = "claim"
	// StepDone means there is nothing left; the dashboard is the surface.
	StepDone Step = "done"
)

// Steps is the flow in order.
var Steps = []Step{StepHostname, StepUser, StepKey, StepLock, StepClaim}

// ProgressSchemaVersion is the on-disk version of the progress file.
const ProgressSchemaVersion = 1

// DefaultProgressPath is where in-flight setup progress lives. It sits beside the
// provisioned marker but is deliberately a different file: progress is transient
// and is deleted when setup completes, while the marker is durable and terminal.
// Folding them together would mean a half-finished wizard left a marker that
// provision.IsProvisioned had to be taught to disbelieve.
const DefaultProgressPath = "/var/lib/waypoint/setup-progress.json"

// Progress is what the wizard has done so far.
type Progress struct {
	SchemaVersion int    `json:"schema_version"`
	Hostname      string `json:"hostname,omitempty"`
	RecoveryUser  string `json:"recovery_user,omitempty"`
	// UserHasPassword records whether the account can be logged into without a
	// key. It is what decides whether the key step may be skipped.
	UserHasPassword bool `json:"user_has_password"`
	// KeyFingerprint is the installed key's OpenSSH fingerprint, so the operator
	// can confirm on the summary screen that it is the one they meant.
	KeyFingerprint string    `json:"key_fingerprint,omitempty"`
	KeySkipped     bool      `json:"key_skipped,omitempty"`
	RootLocked     bool      `json:"root_locked,omitempty"`
	StartedAt      time.Time `json:"started_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// done reports whether a step has been completed.
func (p Progress) done(s Step) bool {
	switch s {
	case StepHostname:
		return p.Hostname != ""
	case StepUser:
		return p.RecoveryUser != ""
	case StepKey:
		return p.KeyFingerprint != "" || p.KeySkipped
	case StepLock:
		return p.RootLocked
	}
	return false
}

// Completed lists the finished steps in order.
func (p Progress) Completed() []Step {
	var out []Step
	for _, s := range Steps {
		if p.done(s) {
			out = append(out, s)
		}
	}
	return out
}

// ErrOutOfOrder is returned when a step is submitted before the one it depends
// on. It carries the step the wizard is actually waiting for, so a client that
// has lost its place — a reloaded browser, a resumed session — is told where to
// go rather than merely refused.
type ErrOutOfOrder struct {
	Submitted Step
	Expected  Step
}

func (e *ErrOutOfOrder) Error() string {
	return fmt.Sprintf("setup step %q cannot run yet; the next step is %q", e.Submitted, e.Expected)
}

// APController is the part of the setup access point the join sequence needs.
// It is an interface so the wizard depends on the behaviour rather than on the
// radio, and so a test can drive the whole handover without one.
type APController interface {
	// DownForJoin frees the radio for a station association, reversibly.
	DownForJoin(ctx context.Context) error
	// Reraise brings the AP back after a failed join.
	Reraise(ctx context.Context, why captive.Reason) error
	// Commit spends the AP after a join that has been verified.
	Commit(ctx context.Context) error
}

// Wizard drives the flow.
type Wizard struct {
	// Prov is the privileged helper. Every system change goes through it; this
	// package never touches /etc itself, which is the whole point of the split.
	Prov privhelper.Provisioner

	// MarkerPath is the provisioned marker (internal/provision).
	MarkerPath string
	// ProgressPath is the in-flight progress file.
	ProgressPath string

	// Mirror, when set, receives the provisioned boolean at the end so the API can
	// answer from config.db. Optional: the marker is authoritative either way.
	Mirror *provision.Mirror

	// Claimed reports whether the device has been claimed (RFC-0002). It decides
	// whether the step after provisioning is claim or done.
	Claimed func() bool

	// AP is the setup access point, when there is one. The join sequence has to
	// drive it rather than merely notify it: a single-radio Pi cannot be an access
	// point and a station at the same time, so the AP must give the radio up
	// before the join and take it back if the join fails.
	AP APController

	// OnComplete is called once provisioning finishes, so the AP can come down
	// for good and the setup session can close.
	OnComplete func(ctx context.Context)

	// Now and Logf are injectable for tests.
	Now  func() time.Time
	Logf func(format string, args ...any)

	// One operator drives setup, but a reloaded browser can double-submit a step.
	// Serialising means the second submission sees the first one's result rather
	// than racing it through the helper.
	mu sync.Mutex
}

func (w *Wizard) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now().UTC()
}

func (w *Wizard) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

func (w *Wizard) markerPath() string {
	if w.MarkerPath == "" {
		return provision.DefaultPath
	}
	return w.MarkerPath
}

func (w *Wizard) progressPath() string {
	if w.ProgressPath == "" {
		return DefaultProgressPath
	}
	return w.ProgressPath
}

// Provisioned reports whether setup is finished. It reads the marker rather than
// any state this package holds, so a wizard instance that was restarted — or a
// second one in another process — agrees with the first.
func (w *Wizard) Provisioned() bool {
	return provision.IsProvisioned(w.markerPath())
}

// Load reads the progress file. A missing or unreadable file means "nothing done
// yet": the fail direction is to repeat work, which every step tolerates, rather
// than to skip work, which would leave a node half set up.
func (w *Wizard) Load() Progress {
	b, err := os.ReadFile(w.progressPath())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			w.logf("wizard: cannot read %s (starting from the beginning): %v", w.progressPath(), err)
		}
		return Progress{SchemaVersion: ProgressSchemaVersion, StartedAt: w.now()}
	}
	var p Progress
	if err := json.Unmarshal(b, &p); err != nil || p.SchemaVersion > ProgressSchemaVersion {
		w.logf("wizard: %s is unusable (starting from the beginning): %v", w.progressPath(), err)
		return Progress{SchemaVersion: ProgressSchemaVersion, StartedAt: w.now()}
	}
	return p
}

// save writes progress atomically.
func (w *Wizard) save(p Progress) error {
	p.SchemaVersion = ProgressSchemaVersion
	if p.StartedAt.IsZero() {
		p.StartedAt = w.now()
	}
	p.UpdatedAt = w.now()

	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	path := w.progressPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".setup-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // no-op once the rename consumes it
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Next returns the step the wizard is waiting for.
func (w *Wizard) Next() Step {
	if w.Provisioned() {
		if w.Claimed != nil && w.Claimed() {
			return StepDone
		}
		return StepClaim
	}
	return next(w.Load())
}

// next is the state machine: the first step that has not been completed.
func next(p Progress) Step {
	for _, s := range Steps {
		if s == StepClaim {
			break
		}
		if !p.done(s) {
			return s
		}
	}
	return StepClaim
}

// View is what a client needs to render the wizard.
type View struct {
	Mode      string `json:"mode"` // "setup", "claim", or "done"
	Next      Step   `json:"next"`
	Steps     []Step `json:"steps"`
	Completed []Step `json:"completed"`

	Hostname        string `json:"hostname,omitempty"`
	RecoveryUser    string `json:"recovery_user,omitempty"`
	UserHasPassword bool   `json:"user_has_password"`
	KeyFingerprint  string `json:"key_fingerprint,omitempty"`
	// KeySkippable tells the UI whether it may offer "skip" on the key step. It is
	// false for a key-only account, where skipping would leave an account nobody
	// can log in as.
	KeySkippable bool `json:"key_skippable"`

	Provisioned bool `json:"provisioned"`
	Claimed     bool `json:"claimed"`
}

// State returns the current view.
func (w *Wizard) State() View {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state()
}

func (w *Wizard) state() View {
	claimed := w.Claimed != nil && w.Claimed()
	if provisioned := w.Provisioned(); provisioned {
		// Setup is finished, and from here the endpoint is reachable without
		// authentication for as long as the daemon runs. It therefore reports only
		// which surface to show — never the hostname, the recovery account's name,
		// or the installed key's fingerprint, all of which are useful to someone
		// deciding how to attack the box and useless to anyone else.
		v := View{Steps: Steps, Completed: Steps[:len(Steps)-1], Provisioned: true, Claimed: claimed}
		if claimed {
			v.Mode, v.Next, v.Completed = "done", StepDone, Steps
		} else {
			v.Mode, v.Next = "claim", StepClaim
		}
		return v
	}

	p := w.Load()
	provisioned := false

	v := View{
		Steps:           Steps,
		Completed:       p.Completed(),
		Hostname:        p.Hostname,
		RecoveryUser:    p.RecoveryUser,
		UserHasPassword: p.UserHasPassword,
		KeyFingerprint:  p.KeyFingerprint,
		KeySkippable:    p.UserHasPassword,
		Provisioned:     provisioned,
		Claimed:         claimed,
	}
	v.Mode, v.Next = "setup", next(p)
	return v
}

// expect enforces the ordering. A step may be re-submitted once it is complete —
// a browser that reloads mid-request must not be told it is out of order — but it
// may not be submitted before its predecessors.
func (w *Wizard) expect(p Progress, s Step) error {
	if p.done(s) {
		return nil
	}
	if got := next(p); got != s {
		return &ErrOutOfOrder{Submitted: s, Expected: got}
	}
	return nil
}
