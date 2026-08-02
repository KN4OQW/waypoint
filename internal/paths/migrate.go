package paths

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// Migrate relocates a pre-0.3 state tree from LegacyStateDir to StateDir and
// leaves a symlink behind at the old location. It is a no-op on a node that has
// already migrated and on a freshly flashed one, so it is safe to call on every
// start, which is how it is called.
//
// # Why the symlink is the point
//
// An installed node's systemd units name LegacyStateDir, and nothing can change
// that: the units are shipped by the image, the RFC-0014 updater delivers a
// binary and nothing else, and the waypoint-stack .debs ship daemons with no unit
// files at all. So waypointd is the only component that reaches an installed
// node, and it cannot rewrite the eleven ExecStart lines that will keep pointing
// here for the rest of that node's life. The symlink is what makes them keep
// resolving.
//
// It also makes the migration free to fail forward. The updater health-gates a
// new binary and restores the previous one when the check fails (RFC-0014); that
// previous binary has the old paths compiled in, and they resolve through the
// symlink to the migrated tree. A bare move would have left a reverted node
// looking at a directory that no longer existed, which would have made this
// migration the one thing the updater could not undo.
//
// # Why it is a merge and not a move
//
// StateDir is not new. internal/provision has always kept the provisioned marker
// there, and internal/wizard the setup progress file, so every node being
// migrated already has both directories. Migrate therefore moves LegacyStateDir's
// entries into StateDir one at a time rather than renaming the tree wholesale —
// and it deliberately never touches the two files already at the destination, so
// the marker that decides whether a node re-runs first-boot setup is not in the
// blast radius at all.
//
// # Crash behaviour
//
// The per-entry renames are individually atomic but the sequence is not. A power
// loss part-way leaves some entries moved, the rest in place and no symlink yet;
// the next start finds exactly that state and finishes the job, because an entry
// already moved is simply no longer in the source. The cost of the interrupted
// boot is that the gateways may read INI paths that have moved out from under
// them for that one boot. That is recoverable and self-healing, which the
// alternative — a window where the provisioned marker is missing and the node
// re-runs its setup wizard — is not.
// # Why bin/ is special
//
// The old tree kept the waypointd binary inside itself, at <state>/bin. BinDir is
// no longer under StateDir, so bin/ is the one entry that does not simply move
// across — it goes to BinDir, and StateDir/bin becomes a symlink to it. Both
// names then resolve to the same inode, which they have to: the installed unit's
// ExecStart reaches the binary by the old path, while the updater's compiled-in
// -update-binary default names the new one. If those two disagreed, the node
// would run one file and update another, and updates would stop working on
// exactly the nodes that need them.
func Migrate() (Migration, error) { return migrate(LegacyStateDir, StateDir, BinDir) }

// Migration reports what Migrate did, for the caller to log. A zero Migration
// means there was nothing to do.
type Migration struct {
	Performed bool     // entries were moved and/or the symlink was created
	Moved     []string // entry names relocated from From to To, in the order moved
	Copied    bool     // the move crossed a filesystem boundary and fell back to copying
	From      string
	To        string
}

// String renders the migration as the one log line waypointd emits for it.
func (m Migration) String() string {
	if !m.Performed {
		return "no state migration needed"
	}
	how := "moved"
	if m.Copied {
		how = "copied (crossed a filesystem boundary)"
	}
	return fmt.Sprintf("%s %d entr%s from %s to %s and left a compatibility symlink behind: %s",
		how, len(m.Moved), plural(len(m.Moved)), m.From, m.To, strings.Join(m.Moved, ", "))
}

func plural(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}

// ConflictError reports that both locations hold an entry of the same name, so
// Migrate cannot tell which copy is authoritative. Nothing has been moved when
// this is returned.
//
// This does not arise from any state Waypoint itself produces — the two
// directories' contents are disjoint by construction — so it means somebody put
// files there by hand. The caller must treat it as fatal rather than picking a
// side: choosing the destination would silently strand a configured node on an
// empty store, and choosing the source would mean running with paths that the
// compiled-in defaults no longer name.
type ConflictError struct {
	Entries  []string
	From, To string
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("cannot migrate %s to %s: both hold %s. "+
		"Waypoint never creates that overlap, so these were placed by hand. "+
		"Move or delete the unwanted copy under %s and restart",
		e.From, e.To, strings.Join(e.Entries, ", "), e.To)
}

// binEntry is the one entry name whose destination is not under StateDir.
const binEntry = "bin"

func migrate(from, to, binDir string) (Migration, error) {
	if from == to {
		// True for every build made before the move landed, where the two
		// constants are the same string. Without this the conflict check below
		// would compare the directory against itself and refuse on every entry.
		return Migration{}, nil
	}
	fi, err := os.Lstat(from)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return Migration{}, nil // freshly flashed: the old location was never created
	case err != nil:
		return Migration{}, fmt.Errorf("stat %s: %w", from, err)
	case fi.Mode()&fs.ModeSymlink != 0:
		return Migration{}, nil // already migrated
	case !fi.IsDir():
		return Migration{}, fmt.Errorf("%s is neither a directory nor a symlink; refusing to migrate", from)
	}

	entries, err := os.ReadDir(from)
	if err != nil {
		return Migration{}, fmt.Errorf("read %s: %w", from, err)
	}
	if err := os.MkdirAll(to, 0o755); err != nil {
		return Migration{}, fmt.Errorf("create %s: %w", to, err)
	}

	// All-or-nothing: find every collision before moving anything, so a refusal
	// leaves the node exactly as it was rather than half-migrated.
	var conflicts []string
	for _, e := range entries {
		switch _, err := os.Lstat(dest(to, binDir, e.Name())); {
		case err == nil:
			conflicts = append(conflicts, e.Name())
		case !errors.Is(err, fs.ErrNotExist):
			return Migration{}, fmt.Errorf("stat %s: %w", dest(to, binDir, e.Name()), err)
		}
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return Migration{}, &ConflictError{Entries: conflicts, From: from, To: to}
	}

	m := Migration{Performed: true, From: from, To: to}
	for _, e := range sidecarsFirst(entries) {
		src, dst := filepath.Join(from, e.Name()), dest(to, binDir, e.Name())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return m, fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
		}
		err := os.Rename(src, dst)
		if isCrossDevice(err) {
			// /home and /var are separate mounts on this box. Copy, then drop the
			// source — slower and non-atomic per entry, but the all-or-nothing
			// precheck above still holds and an interrupted run still resumes.
			m.Copied = true
			err = copyTree(src, dst)
			if err == nil {
				err = os.RemoveAll(src)
			}
		}
		if err != nil {
			// Partial by construction; report what did move so the log tells the
			// operator where the tree actually is.
			return m, fmt.Errorf("relocate %s to %s: %w", src, dst, err)
		}
		m.Moved = append(m.Moved, e.Name())
	}

	// Point StateDir/bin at BinDir. Done unconditionally rather than only when
	// this run moved bin/, because the gap between the rename above and the link
	// is precisely where a crash would otherwise strand a node: bin/ gone from
	// the source, so no later run would see it, and no link to reach it by.
	if err := linkBin(to, binDir); err != nil {
		return m, err
	}

	// Durability before the symlink claims the move happened: fsync the
	// destination directory so its new entries survive a power loss that lands
	// between here and the symlink.
	if err := fsyncDir(to); err != nil {
		return m, fmt.Errorf("sync %s: %w", to, err)
	}
	if err := os.Remove(from); err != nil {
		return m, fmt.Errorf("remove the now-empty %s: %w", from, err)
	}
	if err := os.Symlink(to, from); err != nil {
		return m, fmt.Errorf("link %s to %s: %w", from, to, err)
	}
	if err := fsyncDir(filepath.Dir(from)); err != nil {
		return m, fmt.Errorf("sync %s: %w", filepath.Dir(from), err)
	}
	return m, nil
}

// sidecarsFirst orders SQLite's -wal and -shm files ahead of the database they
// belong to. The three are separate directory entries, so an interrupted run can
// separate them, and which way they separate decides whether the operator loses
// data or sees an error.
//
// A database that arrives without its write-ahead log opens successfully at
// whatever state was last checkpointed, silently discarding every commit still
// in the log — on this tree that is the operator's most recent configuration
// changes. A log that arrives without its database fails loudly instead, and the
// next start reunites them, because the database is still sitting in the source
// where the migration will find it. So the sidecars go first: it turns the bad
// outcome into the recoverable one.
//
// This is not a rare path. A clean `systemctl stop waypointd` leaves both files
// on disk — measured on the bench node, where config.db-wal was still 800 KB
// after the service had stopped — so the ordinary upgrade has a hot log to move,
// not a checkpointed-away one. Every migration goes through this ordering.
func sidecarsFirst(entries []os.DirEntry) []os.DirEntry {
	ordered := append([]os.DirEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return isSQLiteSidecar(ordered[i].Name()) && !isSQLiteSidecar(ordered[j].Name())
	})
	return ordered
}

func isSQLiteSidecar(name string) bool {
	return strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")
}

// dest maps a source entry name to where it belongs. Everything is a StateDir
// sibling except bin/, which leaves the state tree entirely.
func dest(to, binDir, name string) string {
	if name == binEntry {
		return binDir
	}
	return filepath.Join(to, name)
}

// linkBin makes to/bin a symlink to binDir so the old <state>/bin/waypointd path
// — which an installed node's ExecStart still names — resolves to the binary the
// updater now manages. It is idempotent: an existing correct link is left alone,
// and a node whose binDir does not exist yet gets no dangling link.
func linkBin(to, binDir string) error {
	link := filepath.Join(to, binEntry)
	switch fi, err := os.Lstat(link); {
	case err == nil && fi.Mode()&fs.ModeSymlink != 0:
		if target, err := os.Readlink(link); err == nil && target == binDir {
			return nil // already linked
		}
		return fmt.Errorf("%s is a symlink to somewhere other than %s; resolve it by hand", link, binDir)
	case err == nil:
		return fmt.Errorf("%s exists and is not a symlink to %s; resolve it by hand", link, binDir)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("stat %s: %w", link, err)
	}
	if _, err := os.Stat(binDir); errors.Is(err, fs.ErrNotExist) {
		return nil // nothing to point at; this node keeps its binary elsewhere
	} else if err != nil {
		return fmt.Errorf("stat %s: %w", binDir, err)
	}
	if err := os.Symlink(binDir, link); err != nil {
		return fmt.Errorf("link %s to %s: %w", link, binDir, err)
	}
	return nil
}

// isCrossDevice reports whether err is the kernel refusing to rename across a
// mount point (EXDEV), which is the one rename failure that has a fallback.
func isCrossDevice(err error) bool { return errors.Is(err, syscall.EXDEV) }

// copyTree copies a file, directory or symlink at src to dst, preserving modes.
// It is only reached on the EXDEV path.
func copyTree(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case fi.IsDir():
		if err := os.Mkdir(dst, fi.Mode().Perm()); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyTree(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return fsyncDir(dst)
	case !fi.Mode().IsRegular():
		// Sockets and devices are not state; the tree has never held one, and
		// silently skipping something would be worse than saying so.
		return fmt.Errorf("%s is not a regular file, directory or symlink", src)
	}

	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	// The mode matters: config.db and DMRGateway.ini carry secrets and are 0600.
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fi.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// fsyncDir flushes a directory's entries. Renaming a file is atomic but the
// directory entry recording it is not durable until the directory itself is
// synced.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
