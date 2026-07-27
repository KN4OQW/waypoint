// Package p25hosts fetches and caches the P25 reflector (talkgroup) hostlist
// that both P25Gateway and the settings-page talkgroup picker consume. The
// pinned P25Gateway parses P25Hosts.json (a { "reflectors": [...] } document
// from the public register, each entry keyed by a numeric talkgroup
// "designator"), so waypointd downloads that file to a managed path and serves
// a slimmed-down list to the UI.
package p25hosts

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

// hostfiles.kn4oqw.com leads, with refcheck.radio kept as a fallback in case it
// returns — it served this same JSON shape. Pi-Star is deliberately NOT a fallback
// here: it serves the classic text format, and the pinned gateway parses this file
// as JSON, so pointing at it would leave the gateway with no reflectors at all.
// The conversion happens at publish time instead (internal/hostconv, #138).
const DefaultURL = "https://hostfiles.kn4oqw.com/P25Hosts.json,https://hostfiles.refcheck.radio/P25Hosts.json"

// Reflector is the slice of a hostlist entry the picker needs. Designator is the
// P25 talkgroup number the user actually links to.
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
	body, _, err := hostsrc.Download(ctx, hostsrc.P25Hosts, urls, verifydl.Verify{UserAgent: "Waypoint P25 hostlist"})
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".p25hosts-*.tmp")
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

func (e *httpError) Error() string { return "p25hosts: HTTP " + http.StatusText(e.code) }

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
	hostsrc.SetEntries(hostsrc.P25Hosts, len(doc.Reflectors))
	return doc.Reflectors, nil
}

// Run fetches the hostlist once at startup and then every interval until ctx is
// canceled. Fetch failures are logged, not fatal — a hotspot may be briefly
// offline, and the cached file keeps working.
func Run(ctx context.Context, urls []string, path string, interval time.Duration) {
	hostsrc.Register(hostsrc.P25Hosts, "P25 reflector list")
	if wrote, err := hostsrc.Restore(hostsrc.P25Hosts, path); err != nil {
		log.Printf("p25hosts: could not write the shipped list: %v", err)
	} else if wrote {
		log.Printf("p25hosts: seeded %s from the shipped copy", path)
	}
	fetch := func() {
		if err := Fetch(ctx, urls, path); err != nil {
			log.Printf("p25hosts: fetch failed (using cached list if present): %v", err)
		} else {
			log.Printf("p25hosts: updated %s", path)
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
