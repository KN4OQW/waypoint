package netconfig

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// errCreate is a sentinel Create failure for the composite unwind test.
var errCreate = errors.New("checkpoint create failed")

// The keyfile checkpoint restores exactly the pre-apply managed set: a changed
// file reverts, a file the apply added is removed, and a hand-made profile is
// never touched.
func TestKeyfileCheckpointRollback(t *testing.T) {
	dir := t.TempDir()
	// Pre-apply managed profile + a foreign one.
	lan := filepath.Join(dir, FileName("lan"))
	if err := os.WriteFile(lan, []byte("[connection]\nid=waypoint-lan\nORIGINAL\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "hand-made.nmconnection")
	if err := os.WriteFile(foreign, []byte("[connection]\nid=hand-made\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var reloads int
	cp := NewKeyfileCheckpoint(dir, func(name string, args ...string) (string, error) {
		if name == "nmcli" && strings.Join(args, " ") == "connection reload" {
			reloads++
		}
		return "", nil
	})

	handle, err := cp.Create(90 * time.Second)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Simulate the apply: mutate lan and add a new managed profile.
	if err := os.WriteFile(lan, []byte("[connection]\nid=waypoint-lan\nCHANGED\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(dir, FileName("vpn"))
	if err := os.WriteFile(added, []byte("[connection]\nid=waypoint-vpn\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cp.Rollback(handle); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	// lan restored to ORIGINAL, added profile removed, foreign untouched, NM reloaded.
	if b, _ := os.ReadFile(lan); !strings.Contains(string(b), "ORIGINAL") {
		t.Errorf("lan not restored: %q", b)
	}
	if _, err := os.Stat(added); !os.IsNotExist(err) {
		t.Error("apply-added managed profile should be removed on rollback")
	}
	if _, err := os.Stat(foreign); err != nil {
		t.Error("foreign profile must survive rollback")
	}
	if reloads != 1 {
		t.Errorf("rollback should reload NM once, got %d", reloads)
	}
}

// Destroy discards the snapshot so a later rollback of the same handle is a no-op
// error (nothing to restore), proving the change was made permanent.
func TestKeyfileCheckpointDestroy(t *testing.T) {
	dir := t.TempDir()
	cp := NewKeyfileCheckpoint(dir, nil)
	handle, err := cp.Create(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Destroy(handle); err != nil {
		t.Fatal(err)
	}
	if err := cp.Rollback(handle); err == nil {
		t.Fatal("rollback after destroy should fail — the snapshot is gone")
	}
}

// The native backend builds the correct busctl CheckpointCreate call and parses
// NM's object-path reply into the handle, then threads it through Destroy/Rollback.
func TestNMCheckpointArgs(t *testing.T) {
	var calls [][]string
	run := func(name string, args ...string) (string, error) {
		calls = append(calls, append([]string{name}, args...))
		if len(args) > 4 && args[4] == "CheckpointCreate" {
			return `o "/org/freedesktop/NetworkManager/Checkpoint/3"`, nil
		}
		return "", nil
	}
	cp := NewNMCheckpoint(run)

	handle, err := cp.Create(90 * time.Second)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if handle != "/org/freedesktop/NetworkManager/Checkpoint/3" {
		t.Fatalf("handle = %q", handle)
	}
	create := strings.Join(calls[0], " ")
	// Armed timeout is the confirm window (90s) + nmRollbackGrace (30s) = 120s; flags = 3.
	for _, want := range []string{"busctl call", nmBusName, "CheckpointCreate", "aouu", "0 120 3"} {
		if !strings.Contains(create, want) {
			t.Errorf("CheckpointCreate call missing %q:\n%s", want, create)
		}
	}

	if err := cp.Rollback(handle); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls[1], " "); !strings.Contains(got, "CheckpointRollback o /org/freedesktop/NetworkManager/Checkpoint/3") {
		t.Errorf("Rollback call = %s", got)
	}

	if err := cp.Destroy(handle); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(calls[2], " "); !strings.Contains(got, "CheckpointDestroy o /org/freedesktop/NetworkManager/Checkpoint/3") {
		t.Errorf("Destroy call = %s", got)
	}
}

// The composite rolls back / destroys every backend, and Create unwinds cleanly
// if a later backend fails.
func TestCompositeCheckpoint(t *testing.T) {
	a, b := &fakeCheckpoint{}, &fakeCheckpoint{}
	c := NewCompositeCheckpoint(a, b)
	h, err := c.Create(time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if a.created != 1 || b.created != 1 {
		t.Fatalf("both backends should checkpoint: a=%d b=%d", a.created, b.created)
	}
	if err := c.Rollback(h); err != nil {
		t.Fatal(err)
	}
	if len(a.rolled) != 1 || len(b.rolled) != 1 {
		t.Fatalf("rollback should reach both backends: a=%v b=%v", a.rolled, b.rolled)
	}
	// A second rollback of the same handle fails — it was consumed.
	if err := c.Rollback(h); err == nil {
		t.Fatal("rollback of a consumed composite handle should fail")
	}
}

// If a later backend's Create fails, the composite unwinds the earlier ones so
// nothing is left armed (e.g. NM checkpoint succeeds but the keyfile snapshot errs).
func TestCompositeCreateUnwinds(t *testing.T) {
	good := &fakeCheckpoint{}
	bad := &fakeCheckpoint{createErr: errCreate}
	c := NewCompositeCheckpoint(good, bad)
	if _, err := c.Create(time.Minute); err == nil {
		t.Fatal("composite Create should fail when a backend fails")
	}
	if len(good.destroyed) != 1 {
		t.Fatalf("the earlier backend should be destroyed on unwind, got %v", good.destroyed)
	}
}

func TestParseBusctlObjectPath(t *testing.T) {
	got, err := parseBusctlObjectPath(`o "/org/freedesktop/NetworkManager/Checkpoint/1"` + "\n")
	if err != nil || got != "/org/freedesktop/NetworkManager/Checkpoint/1" {
		t.Fatalf("parse = %q, %v", got, err)
	}
	if _, err := parseBusctlObjectPath("s \"not a path\""); err == nil {
		t.Fatal("a non-object-path reply should error")
	}
}
