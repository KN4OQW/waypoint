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
