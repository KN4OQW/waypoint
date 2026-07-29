// Command genlocaleindex regenerates the UI's locale index from the catalogs on
// disk.
//
// The browser needs to know which languages ship before it can fetch one, and
// it cannot list a directory. So the catalogs' own "_meta" blocks are collected
// into locales/index.json, which the language picker reads.
//
// That index is generated, never hand-edited. This is the mechanism behind
// issue #23's acceptance criterion — "a new language ships from the translation
// platform with zero code review beyond the catalog file". Adding a language is
// a new .json file plus a regenerated index; no source file changes. CI
// regenerates and fails on any diff, so a stale or hand-edited index cannot
// merge.
//
// Run it through go generate:
//
//	go generate ./...
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// indexName is the generated file, skipped when scanning for catalogs.
const indexName = "index.json"

// note travels in the generated file because JSON cannot carry a comment, and
// the first instinct on seeing a wrong display name is to fix it right there.
const note = "Generated from each catalog's _meta block by tools/genlocaleindex. " +
	"Do not edit by hand — run `go generate ./...`."

// meta is the reserved object every catalog carries. Name is the language's own
// name for itself, which is what the picker shows.
type meta struct {
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Reviewed bool   `json:"reviewed"`
}

// language is one entry in the index. It mirrors meta rather than embedding it
// so the index's field order and JSON shape stay this file's decision.
type language struct {
	Tag      string `json:"tag"`
	Name     string `json:"name"`
	Reviewed bool   `json:"reviewed"`
}

type index struct {
	Note      string     `json:"_note"`
	Languages []language `json:"languages"`
}

func main() {
	dir := flag.String("dir", "ui/static/locales", "directory holding the locale catalogs")
	flag.Parse()

	if err := run(*dir); err != nil {
		fmt.Fprintln(os.Stderr, "genlocaleindex:", err)
		os.Exit(1)
	}
}

func run(dir string) error {
	idx, err := build(dir)
	if err != nil {
		return err
	}
	out, err := render(idx)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, indexName), out, 0o644)
}

// build reads every catalog in dir and returns the index they describe. Any
// catalog that fails validation fails the whole run: a half-written index would
// quietly drop a language, which is worse than a red build.
func build(dir string) (index, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return index{}, err
	}

	// Tags are unique by construction: each must equal its own filename, and a
	// directory cannot hold two files of the same name.
	langs := make([]language, 0, len(paths))
	for _, path := range paths {
		if filepath.Base(path) == indexName {
			continue
		}
		l, err := readCatalog(path)
		if err != nil {
			return index{}, err
		}
		langs = append(langs, l)
	}
	if len(langs) == 0 {
		return index{}, fmt.Errorf("no catalogs found in %s", dir)
	}

	// Sorted by tag so the file is a function of its inputs alone — the same
	// catalogs always produce the same bytes, which is what lets CI diff it.
	sort.Slice(langs, func(i, j int) bool { return langs[i].Tag < langs[j].Tag })
	return index{Note: note, Languages: langs}, nil
}

// readCatalog validates one catalog and extracts its index entry. Only _meta is
// examined here; key parity and placeholder checks are a separate gate.
func readCatalog(path string) (language, error) {
	name := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return language{}, err
	}

	var catalog struct {
		Meta *meta `json:"_meta"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return language{}, fmt.Errorf("%s: %w", name, err)
	}
	if catalog.Meta == nil {
		return language{}, fmt.Errorf(`%s: missing the reserved "_meta" object`, name)
	}

	want := strings.TrimSuffix(name, ".json")
	if catalog.Meta.Tag != want {
		return language{}, fmt.Errorf("%s: _meta.tag is %q but the filename says %q — a catalog is fetched by its filename, so they must match", name, catalog.Meta.Tag, want)
	}
	if strings.TrimSpace(catalog.Meta.Name) == "" {
		return language{}, fmt.Errorf("%s: _meta.name is empty — it is the native-language name the picker shows", name)
	}

	return language{Tag: catalog.Meta.Tag, Name: catalog.Meta.Name, Reviewed: catalog.Meta.Reviewed}, nil
}

// render marshals the index with HTML escaping off: native language names are
// text, not markup, and & in the middle of one helps nobody reading the
// diff that is the entire review of a translation PR.
func render(idx index) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(idx); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil // Encode already appends the trailing newline
}
