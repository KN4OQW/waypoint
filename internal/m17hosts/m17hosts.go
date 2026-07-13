// Package m17hosts fetches and caches the M17 reflector hostlist that both
// M17Gateway and the settings-page reflector picker consume. Unlike the
// YSF/P25/NXDN registers this one is a SPACE/TAB-delimited text file (M17Gateway
// Reflectors.cpp strtok on " \t\r\n": name, address, port) — NOT JSON — so
// waypointd downloads that file verbatim to a managed path the gateway reads,
// and parses the same columns to serve a slimmed list to the UI.
package m17hosts

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultURL is the g4klx-endorsed source for the M17 hostlist (the same
// register as YSF/P25/NXDN; M17HostsUpdate.sh downloads exactly this file).
const DefaultURL = "https://hostfiles.refcheck.radio/M17Hosts.txt"

// Reflector is the slice of a hostlist entry the picker needs. Name is the M17
// reflector the user links to; a module letter (A–Z) is appended at link time.
type Reflector struct {
	Name    string `json:"name"`
	Address string `json:"address"`
}

// Fetch downloads the hostlist to path atomically (temp + rename). A failed
// fetch leaves any previously-cached file intact.
func Fetch(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	// M17Gateway's own updater sends this UA; the register expects it.
	req.Header.Set("User-Agent", "M17Gateway - G4KLX")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &httpError{resp.StatusCode}
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".m17hosts-*.tmp")
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

type httpError struct{ code int }

func (e *httpError) Error() string { return "m17hosts: HTTP " + http.StatusText(e.code) }

// Reflectors reads the cached hostlist and returns the entries, sorted by name
// for a usable picker. Lines are "<name> <address> <port>"; comment lines (#)
// and short lines are skipped, matching M17Gateway's own parser.
func Reflectors(path string) ([]Reflector, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var refs []Reflector
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		refs = append(refs, Reflector{Name: f[0], Address: f[1]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(refs, func(i, j int) bool { return refs[i].Name < refs[j].Name })
	return refs, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working.
func Run(ctx context.Context, url, path string, interval time.Duration) {
	fetch := func() {
		if err := Fetch(ctx, url, path); err != nil {
			log.Printf("m17hosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("m17hosts: updated %s", path)
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
