package updater

import (
	"os"
	"path/filepath"
	"testing"
)

// The OS glue is thin, but the swap/backup/restore file dance and the marker
// round-trip are worth pinning against a real temp filesystem.
func TestOSSystemSwapAndRestore(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "waypointd")
	if err := os.WriteFile(bin, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &OSSystem{BinaryPath: bin, MarkerPath: filepath.Join(dir, "update.marker")}

	// Stage the new binary + back up the current one, then swap.
	stage, err := s.StageBinary([]byte("NEW"))
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := s.BackupCurrent()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Swap(stage); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, bin); got != "NEW" {
		t.Fatalf("after swap binary = %q, want NEW", got)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Errorf("stage path should be gone after rename, stat err = %v", err)
	}

	// Restore the rollback → back to OLD.
	if err := s.Restore(rollback); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, bin); got != "OLD" {
		t.Fatalf("after restore binary = %q, want OLD", got)
	}
}

func TestOSSystemMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := &OSSystem{MarkerPath: filepath.Join(dir, "update.marker")}

	// No marker → nil, no error.
	if m, err := s.ReadMarker(); err != nil || m != nil {
		t.Fatalf("empty marker: m=%+v err=%v", m, err)
	}
	want := Marker{Version: "1.4.0", Rollback: "/x.rollback", BootCount: 1}
	if err := s.WriteMarker(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ReadMarker()
	if err != nil || got == nil || *got != want {
		t.Fatalf("round-trip: got=%+v err=%v want=%+v", got, err, want)
	}
	if err := s.ClearMarker(); err != nil {
		t.Fatal(err)
	}
	if m, err := s.ReadMarker(); err != nil || m != nil {
		t.Fatalf("after clear: m=%+v err=%v", m, err)
	}
	// Clearing an absent marker is not an error (idempotent boot path).
	if err := s.ClearMarker(); err != nil {
		t.Errorf("clear of absent marker: %v", err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The real store restore, including the part that is easy to get wrong: SQLite's
// -wal and -shm belong to the file being replaced, and a -wal left from the
// migrated database would be replayed onto the restored one on the next open —
// silently undoing the restore.
func TestOSSystemRestoreStore(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "config.db")
	backup := filepath.Join(dir, "config.db.pre-v1")

	for path, content := range map[string]string{
		live:          "migrated-v2",
		live + "-wal": "stale wal from the migrated database",
		live + "-shm": "stale shm",
		backup:        "pristine-v1",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	s := &OSSystem{StorePath: live}
	if err := s.RestoreStore(backup); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(live)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "pristine-v1" {
		t.Errorf("live store = %q, want the pre-migration copy", got)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(live + suffix); !os.IsNotExist(err) {
			t.Errorf("%s%s survived the restore; it would be replayed onto the restored database", live, suffix)
		}
	}
	// The copy is left in place: recovery must be repeatable if the node still fails.
	if _, err := os.Stat(backup); err != nil {
		t.Errorf("pre-migration copy consumed by the restore: %v", err)
	}
}

// Without a store path there is nothing to restore to, and silently succeeding
// would report a clean revert on a node that cannot open its store.
func TestOSSystemRestoreStoreWithoutPathFails(t *testing.T) {
	s := &OSSystem{}
	if err := s.RestoreStore("/x/config.db.pre-v1"); err == nil {
		t.Error("RestoreStore with no StorePath silently succeeded")
	}
}

// RecordStoreBackup is the handshake from the migrating build to the revert path.
func TestRecordStoreBackup(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "update.marker")

	// No marker: no update in flight, so nothing to annotate and not an error.
	if err := RecordStoreBackup(marker, "/x/config.db.pre-v1"); err != nil {
		t.Errorf("annotating a non-existent marker should be a no-op: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("RecordStoreBackup invented a marker where no update was in flight")
	}

	// With one in flight, the copy is recorded and the rest of the marker survives —
	// BootCount especially, since the boot check already incremented it.
	s := &OSSystem{MarkerPath: marker}
	if err := s.WriteMarker(Marker{Version: "1.4.0", Rollback: "/x/waypointd.rollback", BootCount: 1}); err != nil {
		t.Fatal(err)
	}
	if err := RecordStoreBackup(marker, "/x/config.db.pre-v1"); err != nil {
		t.Fatal(err)
	}
	m, err := s.ReadMarker()
	if err != nil || m == nil {
		t.Fatalf("ReadMarker: %+v %v", m, err)
	}
	if m.StoreBackup != "/x/config.db.pre-v1" {
		t.Errorf("StoreBackup = %q, want the pre-migration copy", m.StoreBackup)
	}
	if m.Version != "1.4.0" || m.Rollback != "/x/waypointd.rollback" || m.BootCount != 1 {
		t.Errorf("annotation clobbered the rest of the marker: %+v", m)
	}
}
