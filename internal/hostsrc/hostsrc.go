// Package hostsrc is where hostlists come from, and what to do when they don't
// arrive.
//
// Every reflector, master and talkgroup list Waypoint shows is a downloaded
// artifact. Each was pinned to exactly one URL, with no fallback, no shipped
// copy, and a failure the UI swallowed — so when the upstream went away the
// pickers were simply empty, with nothing on screen saying why (#138). Six of the
// seven lists resolved to a single third-party host, which made one server the
// single point of failure for five modes.
//
// Three things fix that, and they are the same three for every list:
//
//   - Order, not a URL. A list has an ordered set of sources and the first one
//     that answers and verifies wins, so a dead primary costs a retry, not a
//     feature.
//   - A floor. Every list ships with a copy in the binary. A node that has never
//     reached the internet still has masters and reflectors to pick from, which
//     matters most on a first boot in a shack with no wifi yet.
//   - A record. Each attempt is recorded — which source answered, when it last
//     succeeded, what the last error was, whether the node is serving the shipped
//     copy. Status(), and the API on top of it, is what lets the UI say "this list
//     is from the shipped copy, the last refresh failed at 14:02" instead of
//     showing an empty dropdown.
//
// Verification is unchanged: downloads still go through verifydl, so a signed
// list is still checked against its key before it can replace a cache.
package hostsrc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// Status describes one list's supply: enough for an operator to tell a list that
// is merely old from one that never arrived.
type Status struct {
	// Name is the stable list id, e.g. "dmr_hosts". It is what the API keys on.
	Name string `json:"name"`
	// Label is the human name for the UI, e.g. "DMR master list".
	Label string `json:"label"`
	// Source is the URL that last supplied the cache, or "seed" when the node is
	// serving the copy shipped in the binary. Empty before the first attempt.
	Source string `json:"source"`
	// LastSuccess is when a download last replaced the cache. Zero if never.
	LastSuccess time.Time `json:"last_success"`
	// LastAttempt is when a refresh last ran, successful or not.
	LastAttempt time.Time `json:"last_attempt"`
	// LastError is the most recent failure, cleared by a success. It names every
	// source tried, so "the list is empty" is always answerable.
	LastError string `json:"last_error,omitempty"`
	// FromSeed reports that the current cache came from the shipped copy rather
	// than a download.
	FromSeed bool `json:"from_seed"`
	// Entries is how many rows the current cache parses to. Zero with no error is
	// the "downloaded fine but is empty" case, which is worth showing differently
	// from a failure.
	Entries int `json:"entries"`
}

// Stale reports whether the list has not refreshed within want. A list that has
// never succeeded is stale by definition.
func (s Status) Stale(want time.Duration) bool {
	return s.LastSuccess.IsZero() || time.Since(s.LastSuccess) > want
}

var (
	mu       sync.RWMutex
	statuses = map[string]*Status{}
)

// Register declares a list before any fetch runs, so the API reports every list
// the node knows about rather than only those that have been tried.
func Register(name, label string) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := statuses[name]; !ok {
		statuses[name] = &Status{Name: name, Label: label}
	}
}

func with(name string, fn func(*Status)) {
	mu.Lock()
	defer mu.Unlock()
	s, ok := statuses[name]
	if !ok {
		s = &Status{Name: name, Label: name}
		statuses[name] = s
	}
	fn(s)
}

// SetEntries records how many rows the cache currently parses to. Callers report
// this after parsing, since only they know the shape of their list.
func SetEntries(name string, n int) { with(name, func(s *Status) { s.Entries = n }) }

// Report returns one list's status.
func Report(name string) (Status, bool) {
	mu.RLock()
	defer mu.RUnlock()
	s, ok := statuses[name]
	if !ok {
		return Status{}, false
	}
	return *s, true
}

// All returns every registered list's status, ordered by name so the API output
// is stable.
func All() []Status {
	mu.RLock()
	out := make([]Status, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, *s)
	}
	mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Split parses a source list from a flag value. Sources are separated by commas
// or whitespace so a deployment can pass several, and order is preserved because
// order is the policy: first to answer wins.
func Split(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n'
	}) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// ErrNoSources is returned when a list is configured with no URLs at all, which
// disables downloading rather than being an error the refresher should retry.
var ErrNoSources = errors.New("hostsrc: no sources configured")

// Download tries each source in order and returns the first body that fetches and
// verifies, along with the URL that supplied it. Every source is tried before
// giving up, and the returned error names each failure, so a log line or an API
// consumer can see that the primary is dead rather than just that "it failed".
//
// A per-source signature URL is derived from that source, not the first one — a
// fallback host must be able to serve its own .minisig.
func Download(ctx context.Context, name string, urls []string, v verifydl.Verify) ([]byte, string, error) {
	with(name, func(s *Status) { s.LastAttempt = time.Now() })
	if len(urls) == 0 {
		return nil, "", ErrNoSources
	}
	var fails []string
	for _, u := range urls {
		sv := v
		if sv.HasPubKey && sv.SigURL == "" {
			sv.SigURL = u + ".minisig"
		}
		body, err := verifydl.Download(ctx, u, sv)
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s: %v", u, err))
			continue
		}
		with(name, func(s *Status) {
			s.Source, s.LastSuccess, s.LastError, s.FromSeed = u, time.Now(), "", false
		})
		return body, u, nil
	}
	err := fmt.Errorf("all %d source(s) failed: %s", len(urls), strings.Join(fails, "; "))
	with(name, func(s *Status) { s.LastError = err.Error() })
	return nil, "", err
}

// Restore writes the shipped copy of a list to path when there is no usable cache
// there, and reports whether it wrote one. It is called before the first download
// so that a node which has never had a working connection still has a list to
// show; a cache that already exists is never overwritten, because a stale
// download always beats the shipped copy.
//
// A list with no shipped copy is not an error: it simply has no floor, and its
// status will say so until a download succeeds.
func Restore(name, path string) (bool, error) {
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		return false, nil
	}
	body, ok := Seed(name)
	if !ok {
		return false, nil
	}
	if err := os.MkdirAll(dir(path), 0o755); err != nil {
		return false, err
	}
	tmp := path + ".seed.tmp"
	if err := os.WriteFile(tmp, body, 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return false, err
	}
	with(name, func(s *Status) {
		s.FromSeed, s.Source = true, "seed"
	})
	return true, nil
}

func dir(path string) string {
	if i := strings.LastIndexByte(path, '/'); i > 0 {
		return path[:i]
	}
	return "."
}
