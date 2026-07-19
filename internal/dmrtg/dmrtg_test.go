package dmrtg

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/verifydl"
)

// Property 1: the parser handles the accepted dialects (;, ,, tab, spaces),
// skips comments/blank/malformed lines, dedups, and sorts by numeric ID.
func TestTalkgroupsParse(t *testing.T) {
	const list = `# BrandMeister TGList
3112;Texas Statewide
3120,Florida Statewide
91	Worldwide
9990   Parrot
notanumber;Should be skipped
3112;Duplicate ignored
310;United States;extra;fields
`
	dir := t.TempDir()
	path := filepath.Join(dir, "TGList.txt")
	if err := os.WriteFile(path, []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Talkgroups(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []Talkgroup{
		{"91", "Worldwide"},
		{"310", "United States"}, // trailing ;extra;fields dropped
		{"3112", "Texas Statewide"},
		{"3120", "Florida Statewide"},
		{"9990", "Parrot"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed talkgroups:\n got %+v\nwant %+v", got, want)
	}
}

// Property 2: the resolver maps a known ID to its name, and an unknown ID to ""
// (so the caller falls back to the raw id).
func TestNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TGList.txt")
	os.WriteFile(path, []byte("3112;Texas Statewide\n91;Worldwide\n"), 0o600)
	m, err := Names(path)
	if err != nil {
		t.Fatal(err)
	}
	if m["3112"] != "Texas Statewide" {
		t.Errorf("resolve 3112 = %q, want Texas Statewide", m["3112"])
	}
	if m["999999"] != "" {
		t.Errorf("unknown TG should resolve to empty, got %q", m["999999"])
	}
}

// Property 4: Fetch writes the cache atomically; a failed fetch leaves a prior
// cache intact.
func TestFetchAtomicAndFailureSafe(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "TGList.txt")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("3112;Texas Statewide\n"))
	}))
	defer srv.Close()
	if err := Fetch(context.Background(), srv.URL, path, verifydl.Verify{}); err != nil {
		t.Fatal(err)
	}
	if tgs, _ := Talkgroups(path); len(tgs) != 1 || tgs[0].Name != "Texas Statewide" {
		t.Fatalf("first fetch not cached: %+v", tgs)
	}

	// A failing source (500) must leave the good cache intact.
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if err := Fetch(context.Background(), bad.URL, path, verifydl.Verify{}); err == nil {
		t.Error("Fetch from a 500 source should error")
	}
	if tgs, _ := Talkgroups(path); len(tgs) != 1 || tgs[0].Name != "Texas Statewide" {
		t.Errorf("failed fetch clobbered the cache: %+v", tgs)
	}

	// No temp files left behind.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" || len(e.Name()) > 0 && e.Name()[0] == '.' {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
	_ = time.Second
}
