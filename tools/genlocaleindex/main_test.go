package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops a catalog into dir. Content is raw so the malformed-JSON cases can
// write bytes that no marshaller would produce.
func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// catalog returns a minimal well-formed catalog.
func catalog(tag, name string, reviewed bool) string {
	b, err := json.Marshal(map[string]any{
		"_meta":            map[string]any{"tag": tag, "name": name, "reviewed": reviewed},
		"status.connected": "connected",
	})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func TestBuildValidDirectory(t *testing.T) {
	dir := t.TempDir()
	// Deliberately out of tag order on disk, to prove the index is sorted.
	write(t, dir, "fr-FR.json", catalog("fr-FR", "Français", false))
	write(t, dir, "en-US.json", catalog("en-US", "English (US)", true))

	idx, err := build(dir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if idx.Note == "" {
		t.Error("index carries no _note; a generated file should say so on its face")
	}

	want := []language{
		{Tag: "en-US", Name: "English (US)", Reviewed: true},
		{Tag: "fr-FR", Name: "Français", Reviewed: false},
	}
	if len(idx.Languages) != len(want) {
		t.Fatalf("got %d languages, want %d: %+v", len(idx.Languages), len(want), idx.Languages)
	}
	for i, w := range want {
		if idx.Languages[i] != w {
			t.Errorf("language %d = %+v, want %+v", i, idx.Languages[i], w)
		}
	}
}

// A pre-existing index.json must not be mistaken for a catalog — otherwise the
// second run of the generator fails on the output of the first.
func TestBuildIgnoresTheIndexItself(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "en-US.json", catalog("en-US", "English (US)", true))

	if err := run(dir); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, indexName))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if err := run(dir); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, indexName))
	if err != nil {
		t.Fatalf("re-read index: %v", err)
	}
	if string(first) != string(second) {
		t.Errorf("generator is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestBuildRejects(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		content string
		wantIn  string // substring the error must name, so the message is actionable
	}{
		{
			name:    "tag does not match filename",
			file:    "de-DE.json",
			content: catalog("de-AT", "Deutsch", false),
			wantIn:  "de-AT",
		},
		{
			name:    "malformed JSON",
			file:    "es-ES.json",
			content: `{"_meta": {"tag": "es-ES", "name": "Español",}`,
			wantIn:  "es-ES.json",
		},
		{
			name:    "missing _meta",
			file:    "ja-JP.json",
			content: `{"status.connected": "接続済み"}`,
			wantIn:  "_meta",
		},
		{
			name:    "empty _meta.name",
			file:    "it-IT.json",
			content: catalog("it-IT", "   ", false),
			wantIn:  "_meta.name",
		},
		{
			name:    "catalog is not an object",
			file:    "pt-PT.json",
			content: `["not", "a", "catalog"]`,
			wantIn:  "pt-PT.json",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, "en-US.json", catalog("en-US", "English (US)", true))
			write(t, dir, tc.file, tc.content)

			_, err := build(dir)
			if err == nil {
				t.Fatal("build accepted the catalog; want an error")
			}
			if !strings.Contains(err.Error(), tc.wantIn) {
				t.Errorf("error %q does not mention %q", err, tc.wantIn)
			}
		})
	}
}

// An empty directory is a mis-pointed -dir flag, not a node that ships no
// languages. Writing an empty index would hide the mistake until the picker
// came up blank.
func TestBuildRejectsEmptyDirectory(t *testing.T) {
	if _, err := build(t.TempDir()); err == nil {
		t.Fatal("build accepted a directory with no catalogs; want an error")
	}
}

// The index is read by fetch() in the browser, so it has to be valid JSON of the
// shape i18n.js expects, and it has to end in a newline like every other file in
// the tree.
func TestRenderShape(t *testing.T) {
	out, err := render(index{Note: note, Languages: []language{{Tag: "ja-JP", Name: "日本語", Reviewed: false}}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.HasSuffix(string(out), "\n") {
		t.Error("rendered index does not end in a newline")
	}
	if !strings.Contains(string(out), "日本語") {
		t.Errorf("native name was escaped out of the index:\n%s", out)
	}

	var back index
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatalf("rendered index does not parse: %v\n%s", err, out)
	}
	if len(back.Languages) != 1 || back.Languages[0].Tag != "ja-JP" {
		t.Errorf("round-trip lost the language: %+v", back)
	}
}
