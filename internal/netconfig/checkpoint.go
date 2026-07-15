package netconfig

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Checkpoint is the rollback primitive the confirm-or-revert Guard is built on. A
// checkpoint captures the pre-apply network state; the Guard then applies the new
// config and either Destroys the checkpoint (change confirmed, made permanent) or
// Rollbacks it (no confirmation before the deadline, pre-apply state restored).
//
// Two implementations satisfy it, both behind this interface so the Guard's state
// machine (apply.go) is backend-agnostic and testable with a fake:
//
//   - NMCheckpoint wraps NetworkManager's native CheckpointCreate/Destroy/Rollback
//     D-Bus API. This is the preferred backstop: NM snapshots device+connection
//     state and can even auto-roll-back on its own timer if waypointd itself dies.
//   - KeyfileCheckpoint is the portable fallback: it snapshots the waypoint-*
//     keyfiles and, on rollback, restores them and reloads NM. It needs no D-Bus
//     and is fully unit-testable, so it is the default until the native path is
//     validated on the bench NM version (the prompt's "if it proves unusable …
//     fall back to our own stage/timer/rollback of the keyfiles").
//
// In both cases the Guard owns the authoritative server-side timer (apply.go) —
// the rollback never depends on the admin's HTTP session surviving.
type Checkpoint interface {
	// Create snapshots the current network state. timeout is the intended rollback
	// window; a backend may arm its own native backstop from it, but the Guard's
	// timer remains authoritative. Returns an opaque handle for Destroy/Rollback.
	Create(timeout time.Duration) (handle string, err error)
	// Destroy discards the snapshot — the applied change becomes permanent.
	Destroy(handle string) error
	// Rollback restores the snapshot, undoing the applied change.
	Rollback(handle string) error
}

// --- KeyfileCheckpoint: the portable, unit-tested fallback ----------------

// KeyfileCheckpoint snapshots and restores the set of waypoint-*.nmconnection
// files in dir. It touches only managed profiles — a hand-made NM profile is never
// captured or restored — matching the ownership rule the renderer enforces.
//
// It is the default backend: robust, D-Bus-free, and exercised by the Guard's
// state-machine test. Its rollback restores files that the Guard's apply changed
// and reloads NM via run (nmcli connection reload); it deliberately does not
// re-drive device activation, so it recovers a bad *config* apply (the common
// case) rather than a kernel-level link failure, which the native NMCheckpoint
// covers once validated.
type KeyfileCheckpoint struct {
	Dir string
	Run Runner // for `nmcli connection reload` after a restore; nil = skip reload

	snaps map[string]snapshot
}

type snapshot struct {
	files map[string]string // basename -> content of every waypoint-* keyfile at Create
}

// NewKeyfileCheckpoint builds a keyfile checkpoint backend over dir.
func NewKeyfileCheckpoint(dir string, run Runner) *KeyfileCheckpoint {
	return &KeyfileCheckpoint{Dir: dir, Run: run, snaps: map[string]snapshot{}}
}

// Create records the current content of every waypoint-* keyfile under Dir. The
// handle is a monotonic label derived from the snapshot count (deterministic — no
// wall clock, so it is safe under the no-Date.now discipline and reproducible in
// tests).
func (k *KeyfileCheckpoint) Create(_ time.Duration) (string, error) {
	names, err := sortedManagedFiles(k.Dir)
	if err != nil {
		return "", err
	}
	snap := snapshot{files: make(map[string]string, len(names))}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(k.Dir, n))
		if err != nil {
			return "", err
		}
		snap.files[n] = string(b)
	}
	handle := "kf-" + strconv.Itoa(len(k.snaps)+1)
	k.snaps[handle] = snap
	return handle, nil
}

// Destroy forgets a snapshot.
func (k *KeyfileCheckpoint) Destroy(handle string) error {
	delete(k.snaps, handle)
	return nil
}

// Rollback restores the managed keyfiles to their snapshotted state — rewriting
// changed files, recreating deleted ones, and removing any waypoint-* file that
// did not exist at snapshot time — then reloads NM so it re-reads them. Only
// waypoint-* files are ever added, changed, or removed.
func (k *KeyfileCheckpoint) Rollback(handle string) error {
	snap, ok := k.snaps[handle]
	if !ok {
		return fmt.Errorf("netconfig: unknown checkpoint %q", handle)
	}
	// Remove managed files that are not in the snapshot (they were created by the
	// apply we are undoing).
	current, err := sortedManagedFiles(k.Dir)
	if err != nil {
		return err
	}
	for _, n := range current {
		if _, kept := snap.files[n]; !kept {
			if err := os.Remove(filepath.Join(k.Dir, n)); err != nil {
				return err
			}
		}
	}
	// Restore every snapshotted file to its captured content.
	for n, content := range snap.files {
		if _, err := writeAtomicIfChanged(filepath.Join(k.Dir, n), content); err != nil {
			return err
		}
	}
	delete(k.snaps, handle)
	if k.Run != nil {
		if _, err := k.Run("nmcli", "connection", "reload"); err != nil {
			return err
		}
	}
	return nil
}

// --- NMCheckpoint: NetworkManager's native checkpoint API -----------------

// nmBus is the NetworkManager D-Bus destination/interface/object path.
const (
	nmBusName    = "org.freedesktop.NetworkManager"
	nmObjectPath = "/org/freedesktop/NetworkManager"
)

// NMCheckpoint drives NetworkManager's native checkpoint API over D-Bus via
// busctl (systemd's D-Bus CLI, always present on the systemd/NM box — no cgo, no
// extra Go dependency, consistent with how the rest of Waypoint shells out to
// nmcli/systemctl). CheckpointCreate over the empty device list (all devices) with
// a rollback timeout means NM ITSELF will restore the snapshot if it is neither
// destroyed nor rolled back in time — a backstop that survives waypointd dying.
//
// This is the preferred backend once validated on the bench NM version; until
// then the Guard defaults to KeyfileCheckpoint. The exec surface is kept small and
// its argument construction is unit-tested against a fake Runner.
type NMCheckpoint struct {
	Run Runner
}

// NewNMCheckpoint builds the native backend over run (ExecRunner in production).
func NewNMCheckpoint(run Runner) *NMCheckpoint { return &NMCheckpoint{Run: run} }

// Create calls org.freedesktop.NetworkManager.CheckpointCreate(devices=[],
// rollback_timeout=<seconds>, flags=DELETE_NEW_CONNECTIONS|DISCONNECT_NEW_DEVICES).
// An empty device array checkpoints every device. The returned object path is the
// handle.
func (n *NMCheckpoint) Create(timeout time.Duration) (string, error) {
	secs := int(timeout / time.Second)
	// busctl call <dest> <path> <iface> CheckpointCreate <sig> <args...>
	// Method input signature "aouu": ao (object-path array — 0 elements = all
	// devices), u (rollback_timeout, seconds), u (flags). The array is passed as a
	// leading element count (0) followed by no elements.
	out, err := n.Run("busctl", "call", nmBusName, nmObjectPath, nmBusName,
		"CheckpointCreate", "aouu", "0", strconv.Itoa(secs), strconv.Itoa(nmCheckpointFlags))
	if err != nil {
		return "", fmt.Errorf("netconfig: CheckpointCreate: %w", err)
	}
	return parseBusctlObjectPath(out)
}

// Destroy calls CheckpointDestroy(checkpoint) — the change becomes permanent.
func (n *NMCheckpoint) Destroy(handle string) error {
	_, err := n.Run("busctl", "call", nmBusName, nmObjectPath, nmBusName,
		"CheckpointDestroy", "o", handle)
	return err
}

// Rollback calls CheckpointRollback(checkpoint), restoring the snapshot immediately.
func (n *NMCheckpoint) Rollback(handle string) error {
	_, err := n.Run("busctl", "call", nmBusName, nmObjectPath, nmBusName,
		"CheckpointRollback", "o", handle)
	return err
}

// nmCheckpointFlags is DELETE_NEW_CONNECTIONS(0x1) | DISCONNECT_NEW_DEVICES(0x2):
// on rollback, drop connections created after the checkpoint and disconnect
// devices activated after it, so an apply that added a profile is fully undone.
const nmCheckpointFlags = 0x1 | 0x2

// parseBusctlObjectPath extracts the object path from a busctl reply like
// `o "/org/freedesktop/NetworkManager/Checkpoint/1"`. busctl prints the reply
// signature ("o") followed by the value.
func parseBusctlObjectPath(out string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 || fields[0] != "o" {
		return "", fmt.Errorf("netconfig: unexpected CheckpointCreate reply %q", strings.TrimSpace(out))
	}
	return strings.Trim(fields[1], `"`), nil
}
