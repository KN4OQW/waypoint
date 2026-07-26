package provision

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)

func good() State {
	return State{
		ProvisionedAt: t0,
		UpdatedAt:     t0,
		HostnameSet:   true,
		Hostname:      "hs-shack",
		RecoveryUser:  "rescue",
		RootLocked:    true,
	}
}

// Property 1: a saved marker round-trips, and every field survives.
func TestSaveLoadRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "provisioned")
	want := good()
	if err := Save(p, want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load after Save: %v", err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d (Save must stamp it)", got.SchemaVersion, SchemaVersion)
	}
	if !got.ProvisionedAt.Equal(want.ProvisionedAt) || !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("timestamps = %v/%v, want %v/%v", got.ProvisionedAt, got.UpdatedAt, want.ProvisionedAt, want.UpdatedAt)
	}
	if got.Hostname != want.Hostname || !got.HostnameSet {
		t.Errorf("hostname = %q/%v, want %q/true", got.Hostname, got.HostnameSet, want.Hostname)
	}
	if got.RecoveryUser != want.RecoveryUser {
		t.Errorf("RecoveryUser = %q, want %q", got.RecoveryUser, want.RecoveryUser)
	}
	if !got.RootLocked {
		t.Error("RootLocked = false, want true")
	}
	if !IsProvisioned(p) {
		t.Error("IsProvisioned = false after a successful Save")
	}
}

// Property 2: no marker means needs-setup, and says so specifically.
func TestAbsentMarkerNeedsSetup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "provisioned")

	if IsProvisioned(p) {
		t.Error("IsProvisioned = true with no marker on disk")
	}
	_, err := Load(p)
	if !errors.Is(err, ErrNotProvisioned) {
		t.Errorf("Load = %v, want ErrNotProvisioned", err)
	}
	// A freshly flashed card is not a corruption report.
	if errors.Is(err, ErrCorrupt) {
		t.Error("an absent marker must not report as corrupt")
	}
}

// Property 3: anything that is not a usable marker reads as needs-setup. The
// empty-object and null cases are the ones that matter: they are valid JSON, so
// only validate() stops them from reading as a provisioned node.
func TestCorruptMarkerNeedsSetup(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"garbage", "\x00\x01not json at all"},
		{"empty file", ""},
		{"truncated json", `{"schema_version":1,"provisioned_at":"2026-`},
		{"json null", `null`},
		{"empty object", `{}`},
		{"no schema version", `{"provisioned_at":"2026-07-26T12:00:00Z"}`},
		{"no provisioned_at", `{"schema_version":1,"hostname":"hs-shack"}`},
		{"zero provisioned_at", `{"schema_version":1,"provisioned_at":"0001-01-01T00:00:00Z"}`},
		{"wrong type", `["not","an","object"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "provisioned")
			if err := os.WriteFile(p, []byte(tc.body), 0o644); err != nil {
				t.Fatal(err)
			}
			if IsProvisioned(p) {
				t.Errorf("IsProvisioned = true for a %s marker", tc.name)
			}
			if _, err := Load(p); !errors.Is(err, ErrCorrupt) {
				t.Errorf("Load = %v, want ErrCorrupt", err)
			}
		})
	}
}

// Property 4: a marker from a newer build still reads as provisioned. An A/B
// rollback (RFC-0017) puts an older waypointd in front of a newer marker; dropping
// a working node into the setup wizard because of that would be a self-inflicted
// outage, so Load reports the mismatch but hands back the state.
func TestNewerSchemaStaysProvisioned(t *testing.T) {
	p := filepath.Join(t.TempDir(), "provisioned")
	raw, err := json.Marshal(map[string]any{
		"schema_version":  SchemaVersion + 1,
		"provisioned_at":  t0,
		"updated_at":      t0,
		"hostname_set":    true,
		"hostname":        "hs-shack",
		"something_newer": "a field this build has never heard of",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := Load(p)
	if !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("Load = %v, want ErrSchemaNewer", err)
	}
	if st.Hostname != "hs-shack" {
		t.Errorf("Load returned no usable state alongside ErrSchemaNewer: %+v", st)
	}
	if !IsProvisioned(p) {
		t.Error("IsProvisioned = false for a newer-schema marker; a rollback would re-run setup")
	}
	// And this build must not write that marker back down to its own schema.
	if err := Save(p, st); err == nil {
		t.Error("Save accepted a newer-schema state; it would silently drop unknown fields")
	}
}

// Property 5: Save is atomic — a failed write leaves the previous marker exactly
// as it was, rather than truncating it or leaving a partial file behind.
func TestSaveFailureLeavesPreviousMarkerIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions; cannot force a write failure this way")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "provisioned")

	first := good()
	if err := Save(p, first); err != nil {
		t.Fatal(err)
	}

	// Read-only directory: the temp file cannot be created, so the rename never
	// happens. The old marker is untouched because it was never opened for writing.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	second := good()
	second.Hostname = "clobbered"
	if err := Save(p, second); err == nil {
		t.Fatal("Save into a read-only directory returned nil error")
	}

	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("previous marker is no longer loadable after a failed Save: %v", err)
	}
	if got.Hostname != first.Hostname {
		t.Errorf("Hostname = %q, want %q — a failed Save modified the marker", got.Hostname, first.Hostname)
	}
}

// Property 6: Save leaves no temp files behind, on success or on failure. A
// wizard that retries would otherwise litter /var/lib/waypoint on every attempt.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "provisioned")

	for i := 0; i < 5; i++ {
		st := good()
		st.UpdatedAt = t0.Add(time.Duration(i) * time.Minute)
		if err := Save(p, st); err != nil {
			t.Fatal(err)
		}
	}
	// A state Save must reject, to exercise the early-return paths.
	if err := Save(p, State{}); err == nil {
		t.Fatal("Save accepted a zero State")
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".provisioned-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
	if len(ents) != 1 {
		t.Errorf("directory holds %d entries, want just the marker", len(ents))
	}
}

// Property 7: a reader concurrent with writers never observes a torn marker. This
// is the property the temp-file-plus-rename exists for; a plain truncate-and-write
// would fail it.
func TestConcurrentSaveNeverTears(t *testing.T) {
	p := filepath.Join(t.TempDir(), "provisioned")
	if err := Save(p, good()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			st := good()
			// Vary the length so a torn write would be visible rather than
			// coincidentally overwriting identical bytes.
			st.Hostname = "hs-" + strings.Repeat("x", i%40)
			if err := Save(p, st); err != nil {
				t.Errorf("Save: %v", err)
				break
			}
		}
		close(stop)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := Load(p); err != nil {
				t.Errorf("concurrent Load saw a torn marker: %v", err)
				return
			}
		}
	}()

	wg.Wait()
}

// Property 8: Save creates its parent directory, so first-boot setup works on a
// node where /var/lib/waypoint does not exist yet, and the marker is readable by
// unprivileged health checks.
func TestSaveCreatesDirAndSetsMode(t *testing.T) {
	p := filepath.Join(t.TempDir(), "var", "lib", "waypoint", "provisioned")
	if err := Save(p, good()); err != nil {
		t.Fatal(err)
	}

	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != fileMode.Perm() {
		t.Errorf("marker mode = %o, want %o", got, fileMode.Perm())
	}
	if !IsProvisioned(p) {
		t.Error("IsProvisioned = false after Save into a created directory")
	}
}

// Property 9: Save fills the fields a caller may reasonably leave zero, and
// refuses the ones only a real setup can supply.
func TestSaveDefaultsAndRejections(t *testing.T) {
	dir := t.TempDir()

	// SchemaVersion and UpdatedAt default; ProvisionedAt does not.
	p := filepath.Join(dir, "defaults")
	if err := Save(p, State{ProvisionedAt: t0}); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
	if !got.UpdatedAt.Equal(t0) {
		t.Errorf("UpdatedAt = %v, want it defaulted to ProvisionedAt %v", got.UpdatedAt, t0)
	}

	// A marker with no completion time would read back as corrupt, so Save must
	// not write it in the first place.
	p2 := filepath.Join(dir, "no-timestamp")
	if err := Save(p2, State{HostnameSet: true}); !errors.Is(err, ErrCorrupt) {
		t.Errorf("Save without ProvisionedAt = %v, want ErrCorrupt", err)
	}
	if _, err := os.Stat(p2); !errors.Is(err, os.ErrNotExist) {
		t.Error("a rejected Save still created the marker file")
	}
}

// Property 10: an unreadable marker is not reported as corruption — the operator
// needs to see the real cause — but it still reads as needs-setup.
func TestUnreadableMarkerIsNotCorruption(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores file permissions")
	}
	p := filepath.Join(t.TempDir(), "provisioned")
	if err := Save(p, good()); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })

	_, err := Load(p)
	if err == nil {
		t.Fatal("Load of an unreadable marker returned nil error")
	}
	if errors.Is(err, ErrCorrupt) || errors.Is(err, ErrNotProvisioned) {
		t.Errorf("Load = %v; an I/O failure must surface as itself", err)
	}
	if IsProvisioned(p) {
		t.Error("IsProvisioned = true for a marker it could not read")
	}
}
