// Package hostfile implements the override layer's host-file hooks (RFC-0005 /
// issue #2) for the text hostlists (DMR_Hosts.txt, M17Hosts.txt). A hostlist is a
// downloaded artifact the refresher overwrites on every update; the hooks are the
// operator's local entries that must survive that. For a hostlist at PATH:
//
//	PATH.prepend.d/*   concatenated, lexical order, BEFORE the downloaded base
//	PATH               the downloaded/cached base (the refresher writes this)
//	PATH.append.d/*    concatenated, lexical order, AFTER the base
//
// This is the first-class replacement for Pi-Star's P25HostsLocal grievance
// (open since 2018): the refresher never writes into the hook directories, it
// only concatenates them, so local masters/reflectors are never clobbered.
package hostfile

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PrependDir and AppendDir name the hook directories for a hostlist path.
func PrependDir(path string) string { return path + ".prepend.d" }
func AppendDir(path string) string  { return path + ".append.d" }

// HasHooks reports whether either hook directory holds at least one file — i.e.
// whether Assemble would change the base.
func HasHooks(path string) bool {
	return len(readHookDir(PrependDir(path))) > 0 || len(readHookDir(AppendDir(path))) > 0
}

// Assemble concatenates prepend parts, the base, and append parts. When there are
// no hook parts it returns the base unchanged (byte-identical) so a hostlist with
// no local entries is written exactly as downloaded. When hooks are present, each
// part is newline-terminated before the next is appended, so a hook file missing
// a trailing newline cannot merge its last line into the following one.
func Assemble(prepend [][]byte, base []byte, appendParts [][]byte) []byte {
	if len(prepend) == 0 && len(appendParts) == 0 {
		return base
	}
	var buf bytes.Buffer
	writePart := func(p []byte) {
		if len(p) == 0 {
			return
		}
		buf.Write(p)
		if p[len(p)-1] != '\n' {
			buf.WriteByte('\n')
		}
	}
	for _, p := range prepend {
		writePart(p)
	}
	writePart(base)
	for _, p := range appendParts {
		writePart(p)
	}
	return buf.Bytes()
}

// WriteWithHooks writes base to path, wrapped in whatever prepend/append hook
// files exist, atomically (temp + rename in the target directory). With no hooks
// the bytes written are exactly base. The temp-prefix names the caller (e.g.
// "dmrhosts") only for debuggability.
func WriteWithHooks(path string, base []byte, tmpPrefix string) error {
	content := Assemble(readParts(PrependDir(path)), base, readParts(AppendDir(path)))
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+tmpPrefix+"-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// readHookDir lists the regular files in a hook directory in lexical order. A
// missing directory yields nothing (the hook is simply not in use).
func readHookDir(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue // skip nested dirs and dot-files (editor swap files, .gitkeep)
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func readParts(dir string) [][]byte {
	var parts [][]byte
	for _, n := range readHookDir(dir) {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue // an unreadable hook file is skipped, not fatal to the refresh
		}
		parts = append(parts, b)
	}
	return parts
}
