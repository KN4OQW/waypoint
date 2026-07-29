// Command localecheck validates the UI's message catalogs.
//
// A catalog-only pull request gets no code review — that is the whole point of
// issue #23, and docs/translations.md promises it. This tool is therefore the
// review: everything a maintainer would otherwise have to notice by eye is
// checked here, so a translator gets told what is wrong by CI in seconds
// instead of waiting for a human to spot it, or worse, not spot it.
//
// Checked, in the order an author cares about:
//
//   - the file is valid JSON, with no duplicate keys (encoding/json silently
//     keeps the last of a repeated key, so this walks tokens instead)
//   - _meta is present, its tag equals the filename, its name is non-empty
//   - the catalog is flat: every value outside _meta is a string
//   - no key that does not exist in en-US (a typo'd key is dead weight that
//     renders nothing and no one ever notices)
//   - placeholders match en-US exactly, per string — FATAL, because it is the
//     most direct way a translation breaks the rendered page
//   - a catalog claiming _meta.reviewed is complete
//   - index.json lists exactly the catalogs on disk, with matching _meta
//
// Missing keys are reported but do NOT fail: lookup falls back to en-US key by
// key, so a partial catalog renders correctly, and a translator who does twenty
// strings has helped. The runbook proposed failing a catalog missing more than
// 20% of keys "to keep seeds honest"; that is replaced by the reviewed rule
// above, which draws the line where it actually matters. An unreviewed catalog
// is allowed to be as partial as it likes and says so in the picker; a reviewed
// one is a claim that somebody read every string, and that claim is checked.
//
// Usage:
//
//	go run ./tools/localecheck [-dir ui/static/locales]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	indexName = "index.json"
	baseName  = "en-US.json"
)

// placeholderRE must stay in step with the one in ui/static/i18n.js. A
// placeholder the runtime substitutes but this tool does not see is a
// substitution nobody checks.
var placeholderRE = regexp.MustCompile(`\{([A-Za-z0-9_]+)\}`)

type meta struct {
	Name     string `json:"name"`
	Tag      string `json:"tag"`
	Reviewed bool   `json:"reviewed"`
}

type catalog struct {
	file     string
	meta     *meta
	messages map[string]string
}

// finding is one thing wrong. fatal ones fail the build; the rest are printed so
// an author can see them without being blocked by them.
type finding struct {
	file  string
	msg   string
	fatal bool
}

func (f finding) String() string {
	kind := "warning"
	if f.fatal {
		kind = "error"
	}
	return fmt.Sprintf("%s: %s: %s", kind, f.file, f.msg)
}

func main() {
	dir := flag.String("dir", "ui/static/locales", "directory holding the locale catalogs")
	flag.Parse()

	findings, err := run(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "localecheck:", err)
		os.Exit(1)
	}

	fatal := 0
	for _, f := range findings {
		if f.fatal {
			fatal++
		}
		// GitHub renders these as annotations on the run; they read fine as
		// plain text anywhere else.
		fmt.Printf("::%s file=%s::%s\n", map[bool]string{true: "error", false: "warning"}[f.fatal], f.file, f.msg)
	}

	switch {
	case fatal > 0:
		fmt.Fprintf(os.Stderr, "\nlocalecheck: %d error(s), %d warning(s)\n", fatal, len(findings)-fatal)
		fmt.Fprintln(os.Stderr, "See docs/translations.md for what each rule is for.")
		os.Exit(1)
	case len(findings) > 0:
		fmt.Printf("\nlocalecheck: no errors, %d warning(s)\n", len(findings))
	default:
		fmt.Println("localecheck: all catalogs are consistent with en-US.")
	}
}

func run(dir string) ([]finding, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, err
	}

	var findings []finding
	cats := map[string]*catalog{} // filename -> catalog
	for _, path := range paths {
		name := filepath.Base(path)
		if name == indexName {
			continue
		}
		c, fs := load(path)
		findings = append(findings, fs...)
		if c != nil {
			cats[name] = c
		}
	}

	base := cats[baseName]
	if base == nil {
		return nil, fmt.Errorf("%s is missing or unreadable; it is the base every other catalog is checked against", filepath.Join(dir, baseName))
	}

	names := make([]string, 0, len(cats))
	for n := range cats {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, n := range names {
		if n == baseName {
			continue
		}
		findings = append(findings, compare(base, cats[n])...)
	}
	findings = append(findings, checkIndex(dir, cats)...)
	return findings, nil
}

// load reads and validates one catalog in isolation.
func load(path string) (*catalog, []finding) {
	name := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []finding{{name, err.Error(), true}}
	}

	var findings []finding
	if dup, err := duplicateKeys(raw); err != nil {
		return nil, []finding{{name, "is not valid JSON: " + err.Error(), true}}
	} else if len(dup) > 0 {
		// encoding/json keeps the last value silently, so without this the
		// earlier translation just vanishes with no sign anything happened.
		findings = append(findings, finding{name, fmt.Sprintf("duplicate key(s) %s — JSON keeps only the last, so an earlier translation is being discarded", strings.Join(dup, ", ")), true})
	}

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, []finding{{name, "is not a JSON object: " + err.Error(), true}}
	}

	c := &catalog{file: name, messages: map[string]string{}}
	for k, v := range doc {
		if k == "_meta" {
			var m meta
			if err := json.Unmarshal(v, &m); err != nil {
				findings = append(findings, finding{name, `"_meta" is not an object of the expected shape: ` + err.Error(), true})
				continue
			}
			c.meta = &m
			continue
		}
		var s string
		if err := json.Unmarshal(v, &s); err != nil {
			findings = append(findings, finding{name, fmt.Sprintf("%q is not a string — catalogs are flat, and a nested object renders nothing", k), true})
			continue
		}
		c.messages[k] = s
	}

	switch {
	case c.meta == nil:
		findings = append(findings, finding{name, `is missing the reserved "_meta" object`, true})
	default:
		want := strings.TrimSuffix(name, ".json")
		if c.meta.Tag != want {
			findings = append(findings, finding{name, fmt.Sprintf("_meta.tag is %q but the filename says %q — a catalog is fetched by its filename, so they must match", c.meta.Tag, want), true})
		}
		if strings.TrimSpace(c.meta.Name) == "" {
			findings = append(findings, finding{name, "_meta.name is empty — it is the native-language name the picker shows", true})
		}
	}
	return c, findings
}

// duplicateKeys walks the token stream, because encoding/json will not tell us.
func duplicateKeys(raw []byte) ([]string, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	var dups []string
	var walk func(prefix string) error
	walk = func(prefix string) error {
		seen := map[string]bool{}
		for {
			tok, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := tok.(json.Delim); ok && d == '}' {
				return nil
			}
			key, ok := tok.(string)
			if !ok {
				return fmt.Errorf("expected an object key, got %v", tok)
			}
			full := key
			if prefix != "" {
				full = prefix + "." + key
			}
			if seen[key] {
				dups = append(dups, full)
			}
			seen[key] = true

			val, err := dec.Token()
			if err != nil {
				return err
			}
			if d, ok := val.(json.Delim); ok {
				switch d {
				case '{':
					if err := walk(full); err != nil {
						return err
					}
				case '[':
					if err := skipArray(dec); err != nil {
						return err
					}
				}
			}
		}
	}

	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, fmt.Errorf("top level is not an object")
	}
	// No io.EOF tolerance: reaching the end of input before the closing brace
	// means the object was never terminated, which is invalid JSON and worth
	// saying so rather than letting a vaguer error downstream describe it.
	if err := walk(""); err != nil {
		return nil, err
	}
	return dups, nil
}

func skipArray(dec *json.Decoder) error {
	depth := 1
	for depth > 0 {
		tok, err := dec.Token()
		if err != nil {
			return err
		}
		if d, ok := tok.(json.Delim); ok {
			switch d {
			case '[', '{':
				depth++
			case ']', '}':
				depth--
			}
		}
	}
	return nil
}

// compare checks one translation against the base.
func compare(base, c *catalog) []finding {
	var findings []finding

	var extra, missing []string
	for k := range c.messages {
		if _, ok := base.messages[k]; !ok {
			extra = append(extra, k)
		}
	}
	for k := range base.messages {
		if _, ok := c.messages[k]; !ok {
			missing = append(missing, k)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)

	if len(extra) > 0 {
		findings = append(findings, finding{c.file, fmt.Sprintf("%d key(s) that do not exist in %s: %s — nothing reads them, so the translation is lost work",
			len(extra), baseName, preview(extra)), true})
	}

	// Placeholder parity, per string. This is the one that actually breaks pages.
	keys := make([]string, 0, len(c.messages))
	for k := range c.messages {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		src, ok := base.messages[k]
		if !ok {
			continue // already reported as extra
		}
		want, got := placeholders(src), placeholders(c.messages[k])
		if added, dropped := diff(got, want), diff(want, got); len(added) > 0 || len(dropped) > 0 {
			var parts []string
			if len(dropped) > 0 {
				parts = append(parts, "dropped "+strings.Join(dropped, " "))
			}
			if len(added) > 0 {
				parts = append(parts, "invented "+strings.Join(added, " "))
			}
			findings = append(findings, finding{c.file, fmt.Sprintf("%s: placeholders %s (English has %s). They may be reordered, never changed",
				k, strings.Join(parts, ", "), strings.Join(sortedKeys(want), " ")), true})
		}
	}

	if len(missing) > 0 {
		pct := len(missing) * 100 / len(base.messages)
		reviewed := c.meta != nil && c.meta.Reviewed
		msg := fmt.Sprintf("%d of %d key(s) untranslated (%d%%): %s", len(missing), len(base.messages), pct, preview(missing))
		if reviewed {
			// "reviewed" is a claim that somebody read every string. If strings
			// are missing, either the claim or the catalog is wrong.
			findings = append(findings, finding{c.file, msg + " — but _meta.reviewed is true, which claims the catalog is complete. Finish it, or set reviewed false", true})
		} else {
			findings = append(findings, finding{c.file, msg + " — these fall back to English, which is fine; the picker marks this catalog unreviewed", false})
		}
	}
	return findings
}

func placeholders(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range placeholderRE.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// diff returns the members of a that are not in b, as "{name}".
func diff(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, "{"+k+"}")
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, "{"+k+"}")
	}
	sort.Strings(out)
	if len(out) == 0 {
		return []string{"none"}
	}
	return out
}

func preview(keys []string) string {
	const n = 6
	if len(keys) <= n {
		return strings.Join(keys, ", ")
	}
	return strings.Join(keys[:n], ", ") + fmt.Sprintf(", … (+%d more)", len(keys)-n)
}

// checkIndex verifies the generated index still describes what is on disk.
//
// This is a semantic check — does the index list these catalogs with these names
// and flags — while the CI generate step re-runs the generator and diffs the
// bytes. They fail for different reasons (a hand-edited display name versus a
// forgotten `go generate`) and both point at the same fix, so both are worth
// having.
func checkIndex(dir string, cats map[string]*catalog) []finding {
	name := indexName
	raw, err := os.ReadFile(filepath.Join(dir, indexName))
	if err != nil {
		return []finding{{name, "could not be read: " + err.Error() + " — run `go generate ./...`", true}}
	}
	var idx struct {
		Languages []struct {
			Tag      string `json:"tag"`
			Name     string `json:"name"`
			Reviewed bool   `json:"reviewed"`
		} `json:"languages"`
	}
	if err := json.Unmarshal(raw, &idx); err != nil {
		return []finding{{name, "is not valid JSON: " + err.Error() + " — it is generated, so run `go generate ./...` rather than editing it", true}}
	}

	listed := map[string]struct {
		name     string
		reviewed bool
	}{}
	for _, l := range idx.Languages {
		listed[l.Tag] = struct {
			name     string
			reviewed bool
		}{l.Name, l.Reviewed}
	}

	var findings []finding
	for file, c := range cats {
		if c.meta == nil {
			continue // already reported
		}
		got, ok := listed[c.meta.Tag]
		if !ok {
			findings = append(findings, finding{name, fmt.Sprintf("does not list %q, which %s provides — run `go generate ./...`", c.meta.Tag, file), true})
			continue
		}
		if got.name != c.meta.Name || got.reviewed != c.meta.Reviewed {
			findings = append(findings, finding{name, fmt.Sprintf("describes %q as (%q, reviewed=%v) but %s says (%q, reviewed=%v) — run `go generate ./...`",
				c.meta.Tag, got.name, got.reviewed, file, c.meta.Name, c.meta.Reviewed), true})
		}
		delete(listed, c.meta.Tag)
	}
	for tag := range listed {
		findings = append(findings, finding{name, fmt.Sprintf("lists %q, but no such catalog is on disk — run `go generate ./...`", tag), true})
	}

	sort.Slice(findings, func(i, j int) bool { return findings[i].msg < findings[j].msg })
	return findings
}
