package paths

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// node builds a from/to pair under a temp root. Contents are name -> body; a
// name ending in "/" is a directory.
func node(t *testing.T, legacy, state map[string]string) (from, to, binDir string) {
	t.Helper()
	root := t.TempDir()
	from = filepath.Join(root, "home", "pi-star", "waypoint")
	to = filepath.Join(root, "var", "lib", "waypoint")
	binDir = filepath.Join(root, "usr", "local", "lib", "waypoint", "bin")
	for dir, contents := range map[string]map[string]string{from: legacy, to: state} {
		if contents == nil {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for name, body := range contents {
			p := filepath.Join(dir, name)
			if body == "" && name[len(name)-1] == '/' {
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return from, to, binDir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// The ordinary case: a configured node with the provisioned marker already at
// the destination, which is the state every real node is in.
func TestMigrateMergesIntoTheExistingStateDir(t *testing.T) {
	from, to, binDir := node(t,
		map[string]string{"config.db": "store", "events.db": "events", "etc/": "", "tls/": ""},
		map[string]string{"provisioned": "marker"})

	m, err := migrate(from, to, binDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !m.Performed || len(m.Moved) != 4 {
		t.Fatalf("migration = %+v, want 4 entries moved", m)
	}
	if got := read(t, filepath.Join(to, "config.db")); got != "store" {
		t.Errorf("config.db = %q, want %q", got, "store")
	}
	// The marker that decides whether the node re-runs first-boot setup must be
	// untouched — it was already where it belongs.
	if got := read(t, filepath.Join(to, "provisioned")); got != "marker" {
		t.Errorf("provisioned = %q, want it left alone", got)
	}
	for _, d := range []string{"etc", "tls"} {
		if fi, err := os.Stat(filepath.Join(to, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s/ did not arrive as a directory: %v", d, err)
		}
	}
	// The old location is now a symlink, which is what keeps the installed
	// node's systemd units resolving.
	fi, err := os.Lstat(from)
	if err != nil {
		t.Fatalf("lstat %s: %v", from, err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink after migration", from)
	}
	if target, _ := os.Readlink(from); target != to {
		t.Errorf("symlink target = %q, want %q", target, to)
	}
}

// The property the updater's revert path depends on (RFC-0014): a binary with
// the old paths compiled in still finds the store after the migration.
func TestMigrateLeavesTheOldPathsResolving(t *testing.T) {
	from, to, binDir := node(t, map[string]string{"config.db": "store", "etc/": ""}, map[string]string{"provisioned": "m"})
	if _, err := migrate(from, to, binDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got := read(t, filepath.Join(from, "config.db")); got != "store" {
		t.Errorf("reading through the legacy path gave %q, want %q", got, "store")
	}
	if err := os.WriteFile(filepath.Join(from, "etc", "MMDVM-Host.ini"), []byte("x"), 0o600); err != nil {
		t.Fatalf("writing through the legacy path: %v", err)
	}
	if _, err := os.Stat(filepath.Join(to, "etc", "MMDVM-Host.ini")); err != nil {
		t.Errorf("a write through the legacy path did not land in %s: %v", to, err)
	}
}

// File modes carry meaning here: config.db and DMRGateway.ini are 0600 because
// they hold secrets.
func TestMigratePreservesModes(t *testing.T) {
	from, to, binDir := node(t, map[string]string{"config.db": "store"}, nil)
	if _, err := migrate(from, to, binDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	fi, err := os.Stat(filepath.Join(to, "config.db"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("config.db mode = %o, want 600", fi.Mode().Perm())
	}
}

func TestMigrateIsANoOpOnAFreshNode(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "absent"), filepath.Join(root, "state")
	m, err := migrate(from, to, filepath.Join(root, "bin"))
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if m.Performed {
		t.Errorf("migration = %+v, want nothing done", m)
	}
	if _, err := os.Lstat(to); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a fresh node must not get a state dir created for it: %v", err)
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	from, to, binDir := node(t, map[string]string{"config.db": "store"}, map[string]string{"provisioned": "m"})
	if _, err := migrate(from, to, binDir); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	m, err := migrate(from, to, binDir)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if m.Performed {
		t.Errorf("second run = %+v, want a no-op", m)
	}
	if got := read(t, filepath.Join(to, "config.db")); got != "store" {
		t.Errorf("config.db = %q after a second run", got)
	}
}

// A power loss part-way leaves some entries moved and no symlink. The next start
// has to finish the job rather than trip over its own half-done work.
func TestMigrateResumesAnInterruptedRun(t *testing.T) {
	from, to, binDir := node(t,
		map[string]string{"config.db": "store", "etc/": ""},
		map[string]string{"provisioned": "m", "events.db": "events"}) // events.db already moved
	m, err := migrate(from, to, binDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !m.Performed || len(m.Moved) != 2 {
		t.Fatalf("migration = %+v, want the 2 remaining entries moved", m)
	}
	if got := read(t, filepath.Join(to, "events.db")); got != "events" {
		t.Errorf("the already-moved entry was disturbed: %q", got)
	}
}

// An emptied source still needs the symlink, or the units stop resolving.
func TestMigrateSymlinksAnEmptyLegacyDir(t *testing.T) {
	from, to, binDir := node(t, map[string]string{}, map[string]string{"provisioned": "m"})
	m, err := migrate(from, to, binDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !m.Performed || len(m.Moved) != 0 {
		t.Fatalf("migration = %+v, want the symlink and no moves", m)
	}
	fi, err := os.Lstat(from)
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("%s is not a symlink: %v", from, err)
	}
}

func TestMigrateRefusesAConflictAndChangesNothing(t *testing.T) {
	from, to, binDir := node(t,
		map[string]string{"config.db": "the real one", "etc/": "", "tls/": ""},
		map[string]string{"provisioned": "m", "config.db": "a stray", "tls/": ""})

	m, err := migrate(from, to, binDir)
	var conflict *ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("migrate error = %v, want a ConflictError", err)
	}
	if m.Performed {
		t.Errorf("migration = %+v, want nothing done", m)
	}
	if len(conflict.Entries) != 2 || conflict.Entries[0] != "config.db" || conflict.Entries[1] != "tls" {
		t.Errorf("conflict entries = %v, want every collision reported", conflict.Entries)
	}
	// Both sides intact: a refusal must leave the node exactly as it was.
	if got := read(t, filepath.Join(from, "config.db")); got != "the real one" {
		t.Errorf("source config.db = %q", got)
	}
	if got := read(t, filepath.Join(to, "config.db")); got != "a stray" {
		t.Errorf("destination config.db = %q", got)
	}
	if _, err := os.Stat(filepath.Join(from, "etc")); err != nil {
		t.Errorf("a non-conflicting entry was moved anyway: %v", err)
	}
}

func TestMigrateRefusesANonDirectorySource(t *testing.T) {
	root := t.TempDir()
	from, to := filepath.Join(root, "waypoint"), filepath.Join(root, "state")
	if err := os.WriteFile(from, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate(from, to, filepath.Join(root, "bin")); err == nil {
		t.Fatal("migrate accepted a regular file as the source tree")
	}
}

// The EXDEV fallback, exercised directly: /home and /var can be separate mounts
// on a development box, and a rename across them fails outright.
func TestCopyTreeHandlesTheCrossFilesystemCase(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "src"), filepath.Join(root, "dst")
	if err := os.MkdirAll(filepath.Join(src, "etc", "dstar"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "etc", "DMRGateway.ini"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("DMRGateway.ini", filepath.Join(src, "etc", "link.ini")); err != nil {
		t.Fatal(err)
	}
	if err := copyTree(src, dst); err != nil {
		t.Fatalf("copyTree: %v", err)
	}
	if got := read(t, filepath.Join(dst, "etc", "DMRGateway.ini")); got != "secret" {
		t.Errorf("copied file = %q", got)
	}
	fi, err := os.Stat(filepath.Join(dst, "etc", "DMRGateway.ini"))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("copy did not preserve 0600: %v %v", fi.Mode().Perm(), err)
	}
	li, err := os.Lstat(filepath.Join(dst, "etc", "link.ini"))
	if err != nil || li.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("symlink was not copied as a symlink: %v", err)
	}
	if fi, err := os.Stat(filepath.Join(dst, "etc", "dstar")); err != nil || !fi.IsDir() {
		t.Errorf("empty directory was not copied: %v", err)
	}
}

// bin/ is the one entry that leaves the state tree. Both the old path (which the
// installed unit's ExecStart names) and BinDir (which the updater's compiled-in
// -update-binary default names) must end up at the same file, or the node runs
// one binary and updates another.
func TestMigrateMovesBinOutOfTheStateTree(t *testing.T) {
	from, to, binDir := node(t, map[string]string{"config.db": "store"}, map[string]string{"provisioned": "m"})
	if err := os.MkdirAll(filepath.Join(from, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, exe := range []string{"waypointd", "waypoint-bus"} {
		if err := os.WriteFile(filepath.Join(from, "bin", exe), []byte(exe), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := migrate(from, to, binDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The real location, which is what the updater swaps.
	if got := read(t, filepath.Join(binDir, "waypointd")); got != "waypointd" {
		t.Errorf("%s/waypointd = %q", binDir, got)
	}
	// The bus daemon rides along — waypoint-bus@.service names the same old dir.
	if got := read(t, filepath.Join(binDir, "waypoint-bus")); got != "waypoint-bus" {
		t.Errorf("%s/waypoint-bus = %q", binDir, got)
	}
	// bin/ must not have landed in the state tree.
	fi, err := os.Lstat(filepath.Join(to, "bin"))
	if err != nil {
		t.Fatalf("lstat %s/bin: %v", to, err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("%s/bin is a real directory; the binary should not live in the state tree", to)
	}
	// The path the installed ExecStart uses, through both symlinks.
	if got := read(t, filepath.Join(from, "bin", "waypointd")); got != "waypointd" {
		t.Errorf("the legacy ExecStart path resolved to %q", got)
	}
	exe, err := os.Stat(filepath.Join(binDir, "waypointd"))
	if err != nil {
		t.Fatal(err)
	}
	if exe.Mode().Perm() != 0o755 {
		t.Errorf("waypointd mode = %o, want 755 — a binary that arrives non-executable is a node that will not start", exe.Mode().Perm())
	}
}

// A crash between moving bin/ and creating the link would otherwise leave no
// trace for a later run to act on: the source entry is gone, so nothing would
// recreate the link and the installed ExecStart would stay broken.
func TestMigrateRelinksBinAfterAnInterruptedRun(t *testing.T) {
	from, to, binDir := node(t, map[string]string{"config.db": "store"}, map[string]string{"provisioned": "m"})
	// bin/ already moved; the link was never made.
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "waypointd"), []byte("waypointd"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := migrate(from, to, binDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got := read(t, filepath.Join(from, "bin", "waypointd")); got != "waypointd" {
		t.Errorf("the legacy ExecStart path resolved to %q after a resumed run", got)
	}
}

// A database separated from its write-ahead log opens at its last checkpoint and
// silently drops the commits still in the log, so the log has to move first: that
// way an interrupted run fails loudly and the next one reunites the pair.
func TestMigrateMovesSQLiteSidecarsBeforeTheirDatabase(t *testing.T) {
	from, to, binDir := node(t, map[string]string{
		"config.db": "store", "config.db-wal": "log", "config.db-shm": "index",
		"events.db": "events", "events.db-wal": "elog",
	}, map[string]string{"provisioned": "m"})

	m, err := migrate(from, to, binDir)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pos := map[string]int{}
	for i, name := range m.Moved {
		pos[name] = i
	}
	for _, db := range []string{"config.db", "events.db"} {
		for _, suffix := range []string{"-wal", "-shm"} {
			side := db + suffix
			if _, ok := pos[side]; !ok {
				continue
			}
			if pos[side] > pos[db] {
				t.Errorf("%s moved after %s; an interrupted run would drop the log", side, db)
			}
		}
	}
	if got := read(t, filepath.Join(to, "config.db-wal")); got != "log" {
		t.Errorf("config.db-wal = %q", got)
	}
}
