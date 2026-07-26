// Package provision owns Waypoint's *provisioned* state: the one-time, low-level
// setup a node goes through before it is claimable — the operator picks a real
// hostname (never the shipped default), creates a non-root recovery user (SSH key
// recommended), and the root account is locked.
//
// Provisioning is deliberately a separate gate from claiming (RFC-0002). Claiming
// establishes *who administers* the dashboard; provisioning establishes *what the
// box is* at the OS level, and it must be answerable on a node that was flashed
// with nothing but `dd` — no Raspberry Pi Imager, no boot-partition seed file, no
// vendor-default `pi:raspberry`. Imager 2.x refuses to customise locally-flashed
// images at all, so anything that depends on it is not a first-boot story.
//
// The state of record is a small JSON marker on the rootfs, by default at
// DefaultPath. A file is the right primitive here because the question "has this
// node been set up?" has to be answerable before the config store is open, before
// the network is up, and by a shell script in an initramfs if it comes to that.
// The mirrored boolean in config.db (see Mirror) is a convenience for the API and
// the UI; the file always wins.
//
// Fail direction: any state this package cannot positively confirm reads as
// *needs setup* — an absent marker, a truncated marker, garbage bytes, an
// unreadable file. Re-running the wizard on a healthy node is an inconvenience;
// silently treating an unprovisioned node as done would leave it with a default
// hostname and an unlocked root account forever. The claim gate is the second
// lock behind this one, so "needs setup" is not by itself an open door.
package provision

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// SchemaVersion is the current on-disk marker schema. Bump it when State grows a
// field that an older build would misread; older builds treat a newer marker as
// provisioned-but-unknown (see ErrSchemaNewer) rather than re-running setup,
// because RFC-0017 rollback makes "an older build reads a newer marker" a normal
// event, not a corruption.
const SchemaVersion = 1

// DefaultPath is where the marker lives on a real node. /var/lib is the FHS home
// for state a program keeps across reboots, and it is on the rootfs — an A/B slot
// switch (RFC-0017) carries it, a boot-partition file would not.
const DefaultPath = "/var/lib/waypoint/provisioned"

// fileMode is the marker's permissions. It holds no secrets — a hostname, a
// recovery *username*, and booleans — and being world-readable lets an operator
// and any unprivileged health check answer "is this node set up?" with cat.
const fileMode fs.FileMode = 0o644

// dirMode is the mode for a freshly created DefaultPath parent.
const dirMode fs.FileMode = 0o755

var (
	// ErrNotProvisioned means no marker exists: this node has never completed
	// first-boot setup. It is the expected error on a freshly flashed card, not a
	// failure.
	ErrNotProvisioned = errors.New("provision: no marker; node needs first-boot setup")

	// ErrCorrupt means a marker exists but does not describe a provisioned node —
	// truncated, non-JSON, or missing the fields that only a completed setup
	// writes. Treated exactly like ErrNotProvisioned by IsProvisioned.
	ErrCorrupt = errors.New("provision: marker is corrupt; node needs first-boot setup")

	// ErrSchemaNewer means the marker was written by a newer build than this one.
	// Load still returns the decoded State — the node *is* provisioned, and an
	// older build must not undo that — but the caller should assume fields it does
	// not understand exist, and must not rewrite the marker with Save (doing so
	// would silently downgrade it and drop those fields).
	ErrSchemaNewer = errors.New("provision: marker written by a newer schema")
)

// State is the marker's contents: what the first-boot wizard settled, and when.
//
// It records outcomes, not credentials. The recovery user's password hash lives
// in /etc/shadow where the OS keeps it; this file only remembers that the user
// exists and what it is called, so the dashboard can tell an operator which
// account to SSH in as when they have lost their Waypoint password.
type State struct {
	// SchemaVersion is the schema this marker was written against.
	SchemaVersion int `json:"schema_version"`

	// ProvisionedAt is when setup completed. A marker without it is corrupt —
	// that is what stops an empty `{}` from reading as a provisioned node.
	ProvisionedAt time.Time `json:"provisioned_at"`

	// UpdatedAt is when the marker was last written. It differs from
	// ProvisionedAt once a later step amends the record (a recovery user added
	// after the fact, root locked in a second pass).
	UpdatedAt time.Time `json:"updated_at"`

	// HostnameSet records that the operator chose a hostname rather than keeping
	// the image default. It is tracked separately from Hostname so that a node
	// whose hostname was later changed by other means still reads as "the
	// operator was asked, and answered".
	HostnameSet bool `json:"hostname_set"`

	// Hostname is the name the operator chose at setup time. Advisory: the live
	// hostname is whatever the OS currently reports.
	Hostname string `json:"hostname,omitempty"`

	// RecoveryUser is the non-root account created at setup, empty if none was.
	// This is the account that can reset Waypoint's own credentials when the
	// operator is locked out of the dashboard.
	RecoveryUser string `json:"recovery_user,omitempty"`

	// RootLocked records that the root account's password was locked, so an
	// audit can tell "we locked it" apart from "it was never set".
	RootLocked bool `json:"root_locked"`
}

// validate reports whether s describes a completed setup. It is the single
// definition of "this marker counts", applied on the way in (Load) and on the way
// out (Save) so the two can never disagree.
func (s State) validate() error {
	if s.SchemaVersion < 1 {
		return fmt.Errorf("%w: schema_version %d", ErrCorrupt, s.SchemaVersion)
	}
	if s.ProvisionedAt.IsZero() {
		return fmt.Errorf("%w: provisioned_at is unset", ErrCorrupt)
	}
	return nil
}

// IsProvisioned reports whether the node at path has completed first-boot setup.
// It collapses every failure mode to false: no marker, unreadable marker, garbage
// marker, and a marker that parses but describes nothing all mean "run setup".
// Callers that need to tell those apart use Load.
func IsProvisioned(path string) bool {
	_, err := Load(path)
	// A newer schema is still a provisioned node — an older build (post-rollback)
	// must not drop it back into the wizard.
	return err == nil || errors.Is(err, ErrSchemaNewer)
}

// Load reads and validates the marker at path.
//
// It distinguishes the cases IsProvisioned flattens:
//   - ErrNotProvisioned — the file is absent
//   - ErrCorrupt — the file exists but is not a usable marker
//   - ErrSchemaNewer — a valid marker from a newer build; State is returned
//   - any other error — the file could not be read (I/O, permissions); returned
//     unwrapped so a caller can log the real cause rather than report corruption
func Load(path string) (State, error) {
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return State{}, ErrNotProvisioned
	case err != nil:
		return State{}, fmt.Errorf("provision: read %s: %w", path, err)
	}

	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		return State{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if err := st.validate(); err != nil {
		return State{}, err
	}
	if st.SchemaVersion > SchemaVersion {
		return st, fmt.Errorf("%w: marker is v%d, this build knows v%d",
			ErrSchemaNewer, st.SchemaVersion, SchemaVersion)
	}
	return st, nil
}

// Save writes st to path atomically: the marker either has its previous contents
// or its new ones, never a half-written mix, even across a power cut mid-write —
// which for a hotspot on an SD card is a routine event, not an edge case.
//
// It stamps SchemaVersion and UpdatedAt when the caller left them zero, and
// refuses to write a state that Load would reject, so a marker on disk is always
// one this build would honour.
func Save(path string, st State) error {
	if st.SchemaVersion == 0 {
		st.SchemaVersion = SchemaVersion
	}
	if st.SchemaVersion > SchemaVersion {
		return fmt.Errorf("provision: refusing to write a v%d marker from a v%d build",
			st.SchemaVersion, SchemaVersion)
	}
	if st.UpdatedAt.IsZero() {
		st.UpdatedAt = st.ProvisionedAt
	}
	if err := st.validate(); err != nil {
		return err
	}

	// Indented, with a trailing newline: this file gets read by humans over SSH
	// far more often than by the daemon.
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(path, append(b, '\n'))
}

// Clear removes the marker, returning the node to "needs setup".
//
// It is the destructive half of the reset paths, and it is deliberately the only
// way to get there: nothing in the normal running of a node ever calls it. An
// absent marker is success, so a reset run twice is not an error.
func Clear(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("provision: clear %s: %w", path, err)
	}
	return nil
}

// atomicWrite writes data to path via a same-directory temp file and a rename.
//
// The full durability chain matters here and is easy to get half right: fsync the
// temp file so its bytes reach the card, rename (atomic within a directory), then
// fsync the *directory* so the rename itself survives the power cut. Skipping the
// last step is the classic bug — the new file is durable but the directory entry
// pointing at it is not, and the node comes back to the old marker or to nothing.
func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, ".provisioned-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // best-effort cleanup; a no-op once the rename below has consumed it

	if err := tmp.Chmod(fileMode); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
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
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

// syncDir flushes a directory entry so a rename into it is durable.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck // read-only handle; Sync's error is the one that matters
	return d.Sync()
}
