// Package ysfhosts fetches and caches the YSF reflector hostlist that both
// YSFGateway and the settings-page reflector picker consume. The pinned
// YSFGateway parses YSFHosts.json (a { "reflectors": [...] } document from the
// public register), so waypointd downloads that file to a managed path and
// serves a slimmed-down list to the UI.
package ysfhosts

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// DefaultURL is the g4klx-endorsed source for the pre-built JSON hostlist.
const DefaultURL = "https://hostfiles.refcheck.radio/YSFHosts.json"

// Reflector is the slice of a hostlist entry the picker needs.
type Reflector struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Country     string `json:"country"`
}

type hostsDoc struct {
	Reflectors []Reflector `json:"reflectors"`
}

// upperNames uppercases each reflector's "name" while preserving every other
// field of the record (designator, ipv4, port, …) so the gateways still get a
// complete hostlist. This is the WPSD "UPPERCASE Hostfiles" transform — a
// fetch-time rewrite, not a daemon INI key. A parse failure returns the bytes
// unchanged: never corrupt the cached list over a cosmetic option.
func upperNames(body []byte) []byte {
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		return body
	}
	refs, ok := doc["reflectors"].([]any)
	if !ok {
		return body
	}
	for _, r := range refs {
		if m, ok := r.(map[string]any); ok {
			if n, ok := m["name"].(string); ok {
				m["name"] = strings.ToUpper(n)
			}
		}
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return body
	}
	return out
}

// Fetch downloads the hostlist to path atomically (temp + rename). A failed
// fetch leaves any previously-cached file intact. When upper is set the
// reflector names are uppercased before caching (WPSD "UPPERCASE Hostfiles").
func Fetch(ctx context.Context, url, path string, upper bool) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Waypoint YSF hostlist")
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
	if upper {
		body = upperNames(body)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ysfhosts-*.tmp")
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

func (e *httpError) Error() string { return "ysfhosts: HTTP " + http.StatusText(e.code) }

// Reflectors reads the cached hostlist and returns the entries, sorted by
// country then name for a usable picker.
func Reflectors(path string) ([]Reflector, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc hostsDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	sort.SliceStable(doc.Reflectors, func(i, j int) bool {
		if doc.Reflectors[i].Country != doc.Reflectors[j].Country {
			return doc.Reflectors[i].Country < doc.Reflectors[j].Country
		}
		return doc.Reflectors[i].Name < doc.Reflectors[j].Name
	})
	return doc.Reflectors, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working. upper is read each cycle so
// toggling "UPPERCASE Hostfiles" takes effect on the next refresh; nil means
// never uppercase.
func Run(ctx context.Context, url, path string, interval time.Duration, upper func() bool) {
	fetch := func() {
		up := upper != nil && upper()
		if err := Fetch(ctx, url, path, up); err != nil {
			log.Printf("ysfhosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("ysfhosts: updated %s", path)
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
