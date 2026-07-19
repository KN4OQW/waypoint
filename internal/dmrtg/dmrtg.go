// Package dmrtg fetches and caches the DMR talkgroup-name list that resolves DMR
// talkgroup numbers to human names (RFC-0010 / issue #8) — the piece Pi-Star #9
// has wanted since 2018. It mirrors internal/dmrhosts: fetch a text list to a
// cached file atomically, parse it tolerantly, and serve it to the dashboard and
// the DMR routing picker. DMR is the one mode with no name source in the tree
// (P25/NXDN reflectors already carry names, YSF/M17/D-Star are named reflectors).
package dmrtg

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// DefaultURL is a maintained DMR talkgroup list (the WPSD/w0chp lineage, same host
// as the DMR master list). Overridable by flag; a deployment can point the cache
// path straight at an existing TGList file.
const DefaultURL = "https://hostfiles.w0chp.net/TGList_BM.txt"

// Talkgroup is one DMR talkgroup: its number and human name.
type Talkgroup struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Fetch downloads the talkgroup list to path atomically (temp + rename). When a
// trusted key is configured (RFC-0013), the download is verified against its
// <url>.minisig signature before it replaces the cache — a tampered list is
// rejected and the previous cache is kept. A failed fetch always leaves any
// previously-cached file intact, so a brief outage never wipes the names.
func Fetch(ctx context.Context, url, path string, v verifydl.Verify) error {
	v.UserAgent = "Waypoint DMR talkgroup list"
	if v.HasPubKey && v.SigURL == "" {
		v.SigURL = url + ".minisig"
	}
	body, err := verifydl.Download(ctx, url, v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dmrtg-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Talkgroups reads the cached list and returns the parsed talkgroups, sorted by
// numeric ID. The parser is format-tolerant: each non-comment line is
// "<number><sep><name…>" where sep is ';', ',', tab, or a run of spaces — covering
// the BrandMeister/WPSD TGList and groups.txt dialects. Malformed lines (no
// number, or no name) are skipped, never fatal.
func Talkgroups(path string) ([]Talkgroup, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Talkgroup
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "//") {
			continue
		}
		id, name := splitTG(line)
		if id == "" || name == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, Talkgroup{ID: id, Name: name})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ai, _ := strconv.Atoi(out[i].ID)
		aj, _ := strconv.Atoi(out[j].ID)
		return ai < aj
	})
	return out, nil
}

// splitTG parses one "<number><sep><name>" line into (id, name). The id is the
// leading run of digits; the name is the remainder past the first separator,
// trimmed. A line whose first field is not numeric yields no id.
func splitTG(line string) (id, name string) {
	// Find the leading digit run.
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 {
		return "", ""
	}
	id = line[:i]
	rest := line[i:]
	// Skip the separator(s): ; , tab, or spaces.
	rest = strings.TrimLeft(rest, "; ,\t")
	name = strings.TrimSpace(rest)
	// Some dialects carry trailing fields after another separator; keep only the
	// name segment up to the next ';' (the common "TG;Name;extra" shape).
	if k := strings.IndexByte(name, ';'); k >= 0 {
		name = strings.TrimSpace(name[:k])
	}
	return id, name
}

// Names returns a TG-number → name map for resolving identifiers. A number absent
// from the map resolves to "" so the caller falls back to the raw id.
func Names(path string) (map[string]string, error) {
	tgs, err := Talkgroups(path)
	if err != nil {
		return nil, err
	}
	m := make(map[string]string, len(tgs))
	for _, t := range tgs {
		m[t.ID] = t.Name
	}
	return m, nil
}

// Run fetches the list once at startup and then every interval until ctx is
// canceled. Fetch failures (including a verification failure, RFC-0013) are
// logged, not fatal — the cached file keeps working.
func Run(ctx context.Context, url, path string, interval time.Duration, v verifydl.Verify) {
	fetch := func() {
		if err := Fetch(ctx, url, path, v); err != nil {
			log.Printf("dmrtg: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("dmrtg: updated %s", path)
		}
	}
	fetch()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			fetch()
		}
	}
}
