// Package nxdnhosts fetches and caches the NXDN reflector (talkgroup) hostlist
// that both NXDNGateway and the settings-page talkgroup picker consume. The
// pinned NXDNGateway parses NXDNHosts.json (a { "reflectors": [...] } document
// from the public register, each entry keyed by a numeric talkgroup
// "designator"), so waypointd downloads that file to a managed path and serves
// a slimmed-down list to the UI.
package nxdnhosts

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/KN4OQW/waypoint/internal/hostsrc"
	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// DefaultURL is the g4klx-endorsed source for the pre-built JSON hostlist (the
// same register as YSF/P25; NXDNHostsUpdate.sh downloads exactly this file).
const DefaultURL = "https://hostfiles.kn4oqw.com/NXDNHosts.json,https://hostfiles.refcheck.radio/NXDNHosts.json"

// Reflector is the slice of a hostlist entry the picker needs. Designator is the
// NXDN talkgroup number the user actually links to.
type Reflector struct {
	Designator int    `json:"designator"`
	Name       string `json:"name"`
	Country    string `json:"country"`
	Sponsor    string `json:"sponsor"`
}

type hostsDoc struct {
	Reflectors []Reflector `json:"reflectors"`
}

// Fetch downloads the hostlist to path atomically (temp + rename). A failed
// fetch leaves any previously-cached file intact.
func Fetch(ctx context.Context, urls []string, path string) error {
	body, _, err := hostsrc.Download(ctx, hostsrc.NXDNHosts, urls, verifydl.Verify{UserAgent: "Waypoint NXDN hostlist"})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".nxdnhosts-*.tmp")
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

func (e *httpError) Error() string { return "nxdnhosts: HTTP " + http.StatusText(e.code) }

// Reflectors reads the cached hostlist and returns the entries, sorted by
// country then designator for a usable picker.
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
		return doc.Reflectors[i].Designator < doc.Reflectors[j].Designator
	})
	hostsrc.SetEntries(hostsrc.NXDNHosts, len(doc.Reflectors))
	return doc.Reflectors, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working.
func Run(ctx context.Context, urls []string, path string, interval time.Duration) {
	hostsrc.Register(hostsrc.NXDNHosts, "NXDN reflector list")
	if wrote, err := hostsrc.Restore(hostsrc.NXDNHosts, path); err != nil {
		log.Printf("nxdnhosts: could not write the shipped list: %v", err)
	} else if wrote {
		log.Printf("nxdnhosts: seeded %s from the shipped copy", path)
	}
	fetch := func() {
		if err := Fetch(ctx, urls, path); err != nil {
			log.Printf("nxdnhosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("nxdnhosts: updated %s", path)
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
