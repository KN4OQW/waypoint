package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture builds a locales directory: a base catalog, whatever extra files the
// test names, and a matching index unless the test overrides it.
type fixture struct {
	base  map[string]any
	files map[string]string // filename -> raw contents
	index string            // raw index.json; generated from files when empty
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func raw(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func cat(tag, name string, reviewed bool, msgs map[string]any) map[string]any {
	out := map[string]any{"_meta": map[string]any{"tag": tag, "name": name, "reviewed": reviewed}}
	for k, v := range msgs {
		out[k] = v
	}
	return out
}

func (f fixture) build(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	base := f.base
	if base == nil {
		base = cat("en-US", "English (US)", true, map[string]any{
			"a.one": "One",
			"a.two": "Hello {name}",
		})
	}
	write(t, dir, "en-US.json", raw(t, base))
	for name, content := range f.files {
		write(t, dir, name, content)
	}

	if f.index != "" {
		write(t, dir, "index.json", f.index)
		return dir
	}
	// Derive a correct index from whatever _meta the files declare.
	type lang struct {
		Tag      string `json:"tag"`
		Name     string `json:"name"`
		Reviewed bool   `json:"reviewed"`
	}
	var langs []lang
	entries, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, p := range entries {
		if filepath.Base(p) == "index.json" {
			continue
		}
		var doc struct {
			Meta *lang `json:"_meta"`
		}
		b, _ := os.ReadFile(p)
		if json.Unmarshal(b, &doc) == nil && doc.Meta != nil {
			langs = append(langs, *doc.Meta)
		}
	}
	write(t, dir, "index.json", raw(t, map[string]any{"languages": langs}))
	return dir
}

// check runs the tool and splits what it found.
func check(t *testing.T, dir string) (errs, warns []string) {
	t.Helper()
	findings, err := run(dir)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	for _, f := range findings {
		if f.fatal {
			errs = append(errs, f.file+": "+f.msg)
		} else {
			warns = append(warns, f.file+": "+f.msg)
		}
	}
	return errs, warns
}

func containsAll(t *testing.T, got []string, want ...string) {
	t.Helper()
	joined := strings.Join(got, "\n")
	for _, w := range want {
		if !strings.Contains(joined, w) {
			t.Errorf("no finding mentions %q; got:\n%s", w, joined)
		}
	}
}

func TestCleanCatalogsPass(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": raw(t, cat("de-DE", "Deutsch", true, map[string]any{
			"a.one": "Eins", "a.two": "Hallo {name}",
		})),
	}}.build(t)

	errs, warns := check(t, dir)
	if len(errs) != 0 || len(warns) != 0 {
		t.Errorf("clean catalogs produced findings:\nerrors: %v\nwarnings: %v", errs, warns)
	}
}

// A partial catalog is a normal, useful contribution: warn, never fail.
func TestMissingKeysWarnOnly(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": raw(t, cat("de-DE", "Deutsch", false, map[string]any{"a.one": "Eins"})),
	}}.build(t)

	errs, warns := check(t, dir)
	if len(errs) != 0 {
		t.Errorf("a partial unreviewed catalog must not fail; got errors: %v", errs)
	}
	containsAll(t, warns, "1 of 2 key(s) untranslated", "unreviewed")
}

// ...but claiming to be reviewed while incomplete is a false claim.
func TestReviewedMustBeComplete(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": raw(t, cat("de-DE", "Deutsch", true, map[string]any{"a.one": "Eins"})),
	}}.build(t)

	errs, _ := check(t, dir)
	containsAll(t, errs, "_meta.reviewed is true", "untranslated")
}

func TestPlaceholderParity(t *testing.T) {
	cases := []struct {
		name, value string
		wantIn      []string
	}{
		{"dropped", "Hallo", []string{"dropped {name}", "a.two"}},
		{"renamed", "Hallo {nombre}", []string{"dropped {name}", "invented {nombre}"}},
		{"invented extra", "Hallo {name} {x}", []string{"invented {x}"}},
		{"reordered is fine", "{name}, hallo", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture{files: map[string]string{
				"de-DE.json": raw(t, cat("de-DE", "Deutsch", true, map[string]any{
					"a.one": "Eins", "a.two": tc.value,
				})),
			}}.build(t)

			errs, _ := check(t, dir)
			if tc.wantIn == nil {
				if len(errs) != 0 {
					t.Errorf("reordering placeholders must be allowed; got: %v", errs)
				}
				return
			}
			containsAll(t, errs, tc.wantIn...)
		})
	}
}

func TestExtraKeysFail(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": raw(t, cat("de-DE", "Deutsch", true, map[string]any{
			"a.one": "Eins", "a.two": "Hallo {name}", "a.typo": "Verwaist",
		})),
	}}.build(t)

	errs, _ := check(t, dir)
	containsAll(t, errs, "a.typo", "do not exist in en-US.json")
}

// encoding/json keeps the last of a repeated key without complaint, so the
// earlier translation disappears silently. That is what the token walk is for.
func TestDuplicateKeysFail(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": `{"_meta":{"tag":"de-DE","name":"Deutsch","reviewed":false},` +
			`"a.one":"Eins","a.one":"Uno","a.two":"Hallo {name}"}`,
	}}.build(t)

	errs, _ := check(t, dir)
	containsAll(t, errs, "duplicate key", "a.one")
}

func TestDuplicateInsideMeta(t *testing.T) {
	dir := fixture{files: map[string]string{
		"de-DE.json": `{"_meta":{"tag":"de-DE","tag":"de-DE","name":"Deutsch","reviewed":false},` +
			`"a.one":"Eins","a.two":"Hallo {name}"}`,
	}}.build(t)

	errs, _ := check(t, dir)
	containsAll(t, errs, "duplicate key", "_meta.tag")
}

func TestStructuralProblems(t *testing.T) {
	cases := []struct {
		name, file, content string
		wantIn              []string
	}{
		{"malformed JSON", "de-DE.json", `{"_meta":{"tag":"de-DE"},`, []string{"not valid JSON"}},
		{"tag does not match filename", "de-DE.json",
			`{"_meta":{"tag":"de-AT","name":"Deutsch","reviewed":false},"a.one":"Eins","a.two":"Hallo {name}"}`,
			[]string{"de-AT", "filename says"}},
		{"empty name", "de-DE.json",
			`{"_meta":{"tag":"de-DE","name":"  ","reviewed":false},"a.one":"Eins","a.two":"Hallo {name}"}`,
			[]string{"_meta.name is empty"}},
		{"no _meta", "de-DE.json", `{"a.one":"Eins","a.two":"Hallo {name}"}`, []string{"_meta"}},
		{"nested value", "de-DE.json",
			`{"_meta":{"tag":"de-DE","name":"Deutsch","reviewed":false},"a.one":{"deep":"Eins"},"a.two":"Hallo {name}"}`,
			[]string{"not a string", "flat"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture{files: map[string]string{tc.file: tc.content}}.build(t)
			errs, _ := check(t, dir)
			containsAll(t, errs, tc.wantIn...)
		})
	}
}

func TestIndexFreshness(t *testing.T) {
	de := raw(t, cat("de-DE", "Deutsch", false, map[string]any{"a.one": "Eins", "a.two": "Hallo {name}"}))
	en := `{"tag":"en-US","name":"English (US)","reviewed":true}`

	cases := []struct {
		name, index string
		wantIn      []string
	}{
		{"catalog absent from the index", `{"languages":[` + en + `]}`,
			[]string{`does not list "de-DE"`, "go generate"}},
		{"index lists a catalog that is gone",
			`{"languages":[` + en + `,{"tag":"de-DE","name":"Deutsch","reviewed":false},{"tag":"zz-ZZ","name":"Ghost","reviewed":false}]}`,
			[]string{`lists "zz-ZZ"`, "go generate"}},
		{"hand-edited display name",
			`{"languages":[` + en + `,{"tag":"de-DE","name":"German","reviewed":false}]}`,
			[]string{"describes", "German", "go generate"}},
		{"stale reviewed flag",
			`{"languages":[` + en + `,{"tag":"de-DE","name":"Deutsch","reviewed":true}]}`,
			[]string{"reviewed=true", "go generate"}},
		{"index is not JSON", `{"languages":`, []string{"not valid JSON", "go generate"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fixture{files: map[string]string{"de-DE.json": de}, index: tc.index}.build(t)
			errs, _ := check(t, dir)
			containsAll(t, errs, tc.wantIn...)
		})
	}
}

// Without en-US there is nothing to check against, and reporting 700 "extra key"
// findings against an empty base would be actively misleading.
func TestMissingBaseIsHardFailure(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "de-DE.json", raw(t, cat("de-DE", "Deutsch", false, map[string]any{"a.one": "Eins"})))
	write(t, dir, "index.json", `{"languages":[{"tag":"de-DE","name":"Deutsch","reviewed":false}]}`)

	if _, err := run(dir); err == nil {
		t.Fatal("run accepted a directory with no en-US.json; want an error")
	}
}

// The regex must match what ui/static/i18n.js substitutes, or the gate checks
// something the runtime does not do.
func TestPlaceholderExtraction(t *testing.T) {
	got := placeholders("{a} plain {b_2} {Ccc} {not-a-name} { spaced } {}")
	want := []string{"a", "b_2", "Ccc"}
	if len(got) != len(want) {
		t.Fatalf("extracted %v, want exactly %v", got, want)
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("did not extract %q", w)
		}
	}
}
