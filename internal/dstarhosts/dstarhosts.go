// Package dstarhosts fetches and caches the D-Star reflector hostlist that both
// DStarGateway and the settings-page reflector picker consume. Unlike the YSF/
// P25/NXDN registers (served as ready-made JSON by hostfiles.refcheck.radio),
// DStarGateway has no live download URL upstream: it ships a bundled
// DStar_Hosts.json ({"reflectors":[{name,reflector_type,ipv4,…}]}) and only
// re-reads it locally (HostsFilesManager.cpp). So waypointd downloads that exact
// file from the pinned gateway commit — the source of truth for the format the
// pinned binary parses — to the HostsFiles directory the gateway reads, and
// serves a slimmed-down list to the UI.
package dstarhosts

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// DefaultURL is the pinned DStarGateway commit's bundled hostlist. Pinning the
// SHA (not a branch) keeps the downloaded file byte-compatible with the parser
// in the pinned gateway binary. Bump it in lockstep with the waypoint-stack pin.
const DefaultURL = "https://raw.githubusercontent.com/g4klx/DStarGateway/612f388727a9bb47aaeaae3a89f5abff3152ed93/Data/DStar_Hosts.json"

// Reflector is the slice of a hostlist entry the picker needs. Name is the
// reflector callsign the user links to (e.g. REF001, XRF012, DCS006); Type is
// the protocol family (REF = D-Plus, XRF = DExtra, DCS = DCS).
type Reflector struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// hostEntry mirrors one object in the gateway's DStar_Hosts.json.
type hostEntry struct {
	Name string `json:"name"`
	Type string `json:"reflector_type"`
}

type hostsDoc struct {
	Reflectors []hostEntry `json:"reflectors"`
}

// Fetch downloads the hostlist to path atomically (temp + rename). A failed
// fetch leaves any previously-cached file intact.
func Fetch(ctx context.Context, url, path string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Waypoint D-Star hostlist")
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
	tmp, err := os.CreateTemp(filepath.Dir(path), ".dstarhosts-*.tmp")
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

func (e *httpError) Error() string { return "dstarhosts: HTTP " + http.StatusText(e.code) }

// Reflectors reads the cached hostlist and returns the entries, sorted by type
// then name for a usable picker. Entries with no name are skipped.
func Reflectors(path string) ([]Reflector, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc hostsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	out := make([]Reflector, 0, len(doc.Reflectors))
	for _, e := range doc.Reflectors {
		if e.Name == "" {
			continue
		}
		out = append(out, Reflector{Name: e.Name, Type: e.Type})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Type != out[j].Type {
			return out[i].Type < out[j].Type
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working.
func Run(ctx context.Context, url, path string, interval time.Duration) {
	fetch := func() {
		if err := Fetch(ctx, url, path); err != nil {
			log.Printf("dstarhosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("dstarhosts: updated %s", path)
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
