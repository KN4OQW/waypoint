// Package ysfhosts fetches and caches the YSF reflector hostlist that both
// YSFGateway and the settings-page reflector picker consume. The pinned
// YSFGateway parses YSFHosts.json (a { "reflectors": [...] } document from the
// public register), so waypointd downloads that file to a managed path and
// serves a slimmed-down list to the UI.
package ysfhosts

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/hostsrc"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// hostfiles.kn4oqw.com leads, with refcheck.radio kept as a fallback in case it
// returns — it served this same JSON shape. Pi-Star is deliberately NOT a fallback
// here: it serves the classic text format, and the pinned gateway parses this file
// as JSON, so pointing at it would leave the gateway with no reflectors at all.
// The conversion happens at publish time instead (internal/hostconv, #138).
const DefaultURL = "https://hostfiles.kn4oqw.com/YSFHosts.json,https://hostfiles.refcheck.radio/YSFHosts.json"

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
func Fetch(ctx context.Context, urls []string, path string, upper bool) error {
	body, _, err := hostsrc.Download(ctx, hostsrc.YSFHosts, urls, verifydl.Verify{UserAgent: "Waypoint YSF hostlist"})
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
	hostsrc.SetEntries(hostsrc.YSFHosts, len(doc.Reflectors))
	return doc.Reflectors, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working. upper is read each cycle so
// toggling "UPPERCASE Hostfiles" takes effect on the next refresh; nil means
// never uppercase.
func Run(ctx context.Context, urls []string, path string, interval time.Duration, upper func() bool) {
	// Register and Restore, which every other list's Run does and this one did not:
	// without them the YSF list reported its raw id as its label in the supply API,
	// and — the part that mattered on the air — it never laid the shipped copy down
	// as a floor, so a node that had not yet reached the internet had no reflectors
	// at all rather than the ones the image shipped with.
	hostsrc.Register(hostsrc.YSFHosts, "YSF reflector list")
	if wrote, err := hostsrc.Restore(hostsrc.YSFHosts, path); err != nil {
		log.Printf("ysfhosts: could not write the shipped list: %v", err)
	} else if wrote {
		log.Printf("ysfhosts: seeded %s from the shipped copy", path)
	}
	hostsrc.Every(ctx, hostsrc.YSFHosts, interval, func(ctx context.Context) error {
		up := upper != nil && upper()
		if err := Fetch(ctx, urls, path, up); err != nil {
			log.Printf("ysfhosts: fetch failed (using cached list if present): %v", err)
			return err
		}
		log.Printf("ysfhosts: updated %s", path)
		return nil
	})
}
