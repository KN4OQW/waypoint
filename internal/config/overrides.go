package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The override layer (RFC-0005 / issue #2). A rendered INI is a compiled output
// of the store (RFC-0001); the override layer is the human escape hatch that
// merges hand-edited drop-in fragments *last* into that output — after the store,
// after the renderer, and never touched by an update. The merge is a pure,
// line-preserving function of (rendered base, fragments): the renderer's own
// output is authoritative for every line a fragment does not name, so an update
// that re-renders and re-merges lands on byte-identical bytes (the #2 guarantee).

// unsetToken is the literal value that deletes a rendered key outright (RFC-0001
// "deletion is expressible"): `Key=!unset` removes Key from the rendered output,
// suppressing a rendered default rather than replacing its value.
const unsetToken = "!unset"

// Fragment is one parsed override drop-in file (`overrides.d/<daemon>.d/NN-name.conf`).
// Ops preserve file order; precedence among fragments is lexical filename order
// (later filename wins), handled by applying LoadFragments' sorted slice in turn.
type Fragment struct {
	Name     string   // base filename, for lexical ordering + provenance in the report
	Ops      []fragOp // in file order
	Warnings []string // malformed lines, surfaced not silently dropped ("no silent caps")
}

// fragOp is a single override directive: set Section/Key to Value, or (Unset)
// delete Section/Key from the rendered output.
type fragOp struct {
	section string
	key     string
	value   string
	unset   bool
}

// Applied is one override that actually changed the rendered base — the unit the
// Overrides UI panel and GET /api/overrides report. It carries the provenance
// (Source) RFC-0001 asked the override model to hold (`disk` today; a `ui` origin
// is reserved for UI-authored fragments feeding the same merge).
type Applied struct {
	Daemon  string `json:"daemon"`  // "mmdvm", "dmrgateway", …
	Section string `json:"section"` // INI section, e.g. "DMR"
	Key     string `json:"key"`
	Old     string `json:"old"`    // rendered value; "" when the key was added
	New     string `json:"new"`    // effective value; "" when unset
	Unset   bool   `json:"unset"`  // the override deleted a rendered key
	Added   bool   `json:"added"`  // key/section not present in the rendered base
	Source  string `json:"source"` // winning fragment filename — the provenance
}

// ParseFragment parses one override fragment in the daemons' INI dialect. A key
// whose value is the literal token !unset becomes a delete op. Comment (`#`/`;`)
// and blank lines are ignored; a non-comment line with no `=` outside any section
// context is recorded as a warning rather than silently dropped.
func ParseFragment(name, content string) Fragment {
	f := Fragment{Name: name}
	cur := ""
	for _, raw := range strings.Split(content, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			f.Warnings = append(f.Warnings, name+": ignoring malformed line (no '='): "+line)
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			val = val[1 : len(val)-1]
		}
		if key == "" {
			f.Warnings = append(f.Warnings, name+": ignoring line with empty key: "+line)
			continue
		}
		f.Ops = append(f.Ops, fragOp{section: cur, key: key, value: val, unset: val == unsetToken})
	}
	return f
}

// LoadFragments reads every `*.conf` in dir in lexical filename order (the
// NN-name.conf precedence convention). A missing directory is not an error — the
// override layer is inert until an operator creates a fragment. Warnings from
// malformed lines are returned flattened for the caller to log.
func LoadFragments(dir string) ([]Fragment, []string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // lexical precedence: later filename wins
	var frags []Fragment
	var warnings []string
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, nil, err
		}
		f := ParseFragment(n, string(b))
		frags = append(frags, f)
		warnings = append(warnings, f.Warnings...)
	}
	return frags, warnings, nil
}

// ApplyOverrides merges fragments into a rendered INI base and returns the merged
// text plus the applied-report. It is pure: same base + fragments ⇒ byte-identical
// output and the same report. The merge is line-preserving — the base's sections,
// key order, and header comments are kept; a fragment edits only what it names.
// Fragments apply in slice order, so a lexically-later fragment (or a later line
// within one fragment) wins for a repeated section/key.
func ApplyOverrides(daemon, base string, frags []Fragment) (string, []Applied) {
	doc := parseOverrideDoc(base)
	orig := doc.snapshot() // rendered values, before any override, for the report's Old/Added

	winners := map[string]string{} // section\x00key (lowercased) -> winning fragment name
	for _, f := range frags {
		for _, op := range f.Ops {
			doc.apply(op)
			winners[secKey(op.section, op.key)] = f.Name
		}
	}

	out := doc.render()
	report := diffReport(daemon, orig, doc.snapshot(), winners)
	return out, report
}

// --- ordered INI document (line-preserving) ---

type oiniEntry struct {
	kv    bool   // true: a Key=Value line; false: a verbatim comment/blank line
	raw   string // verbatim line when !kv
	key   string // original spelling when kv
	value string
}

type oiniSection struct {
	lead    []string // blank/comment lines immediately before this section's header
	header  string   // e.g. "[General]"
	name    string   // e.g. "General"
	entries []oiniEntry
}

type oiniDoc struct {
	preamble   []string // lines before the first section header (whole-file header comments)
	sections   []*oiniSection
	trailingNL bool // the base ended with a newline (renderer output always does)
}

// parseOverrideDoc parses rendered INI text into an ordered document that
// reserializes byte-identically when unmodified.
func parseOverrideDoc(text string) *oiniDoc {
	d := &oiniDoc{}
	lines := strings.Split(text, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		// The trailing empty element is the file's final newline, not a blank line.
		d.trailingNL = true
		lines = lines[:n-1]
	}

	var cur *oiniSection
	var pending []string // comment/blank lines awaiting their owner (section header or kv)
	for _, raw := range lines {
		trimmed := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]"):
			s := &oiniSection{header: raw, name: strings.TrimSpace(trimmed[1 : len(trimmed)-1])}
			if cur == nil && len(d.sections) == 0 {
				d.preamble = append(d.preamble, pending...)
			} else {
				s.lead = append(s.lead, pending...)
			}
			pending = nil
			d.sections = append(d.sections, s)
			cur = s
		case trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";"):
			pending = append(pending, raw)
		default:
			eq := strings.IndexByte(raw, '=')
			if eq < 0 {
				pending = append(pending, raw) // not a key line; keep verbatim
				continue
			}
			if cur == nil { // a key before any section header: keep as an anonymous section
				cur = &oiniSection{}
				d.sections = append(d.sections, cur)
			}
			for _, p := range pending { // in-section comments precede their key
				cur.entries = append(cur.entries, oiniEntry{raw: p})
			}
			pending = nil
			cur.entries = append(cur.entries, oiniEntry{kv: true, key: strings.TrimSpace(raw[:eq]), value: strings.TrimSpace(raw[eq+1:])})
		}
	}
	// Trailing comment/blank lines belong to the last section (or the preamble).
	if cur != nil {
		for _, p := range pending {
			cur.entries = append(cur.entries, oiniEntry{raw: p})
		}
	} else {
		d.preamble = append(d.preamble, pending...)
	}
	return d
}

func (d *oiniDoc) render() string {
	var out []string
	out = append(out, d.preamble...)
	for _, s := range d.sections {
		out = append(out, s.lead...)
		if s.header != "" {
			out = append(out, s.header)
		}
		for _, e := range s.entries {
			if e.kv {
				out = append(out, e.key+"="+e.value)
			} else {
				out = append(out, e.raw)
			}
		}
	}
	text := strings.Join(out, "\n")
	if d.trailingNL {
		text += "\n"
	}
	return text
}

func (d *oiniDoc) findSection(name string) *oiniSection {
	for _, s := range d.sections {
		if strings.EqualFold(s.name, name) {
			return s
		}
	}
	return nil
}

// apply mutates the document per one override op (RFC-0005 merge semantics):
// replace an existing key in place, add a key after the section's last key line,
// append a new section at end, or delete a key (!unset).
func (d *oiniDoc) apply(op fragOp) {
	s := d.findSection(op.section)
	if op.unset {
		if s != nil {
			s.removeKey(op.key)
		}
		return
	}
	if s == nil {
		s = &oiniSection{lead: []string{""}, header: "[" + op.section + "]", name: op.section}
		d.sections = append(d.sections, s)
	}
	s.setKey(op.key, op.value)
}

func (s *oiniSection) setKey(key, value string) {
	last := -1
	for i, e := range s.entries {
		if e.kv && strings.EqualFold(e.key, key) {
			s.entries[i].value = value // replace in place, keep position + spelling
			return
		}
		if e.kv {
			last = i
		}
	}
	ins := oiniEntry{kv: true, key: key, value: value}
	if last < 0 { // no existing key line: append at end
		s.entries = append(s.entries, ins)
		return
	}
	// insert right after the last existing key line
	s.entries = append(s.entries[:last+1], append([]oiniEntry{ins}, s.entries[last+1:]...)...)
}

func (s *oiniSection) removeKey(key string) {
	out := s.entries[:0]
	for _, e := range s.entries {
		if e.kv && strings.EqualFold(e.key, key) {
			continue
		}
		out = append(out, e)
	}
	s.entries = out
}

// --- report ---

// snapshot flattens the document to section\x00key (lowercased) -> value for
// diffing, keyed case-insensitively but recording the display spelling.
type snap struct {
	value   string
	section string // display spelling
	key     string
}

func (d *oiniDoc) snapshot() map[string]snap {
	m := map[string]snap{}
	for _, s := range d.sections {
		for _, e := range s.entries {
			if e.kv {
				m[secKey(s.name, e.key)] = snap{value: e.value, section: s.name, key: e.key}
			}
		}
	}
	return m
}

func secKey(section, key string) string {
	return strings.ToLower(section) + "\x00" + strings.ToLower(key)
}

// diffReport derives the applied-report from the rendered base vs the merged
// result: every record corresponds to a real change and every change has exactly
// one record. Order is deterministic (section, then key).
func diffReport(daemon string, orig, final map[string]snap, winners map[string]string) []Applied {
	var out []Applied
	for id, o := range orig {
		f, ok := final[id]
		if !ok { // present in base, gone after merge -> unset
			out = append(out, Applied{Daemon: daemon, Section: o.section, Key: o.key, Old: o.value, Unset: true, Source: winners[id]})
			continue
		}
		if f.value != o.value { // value changed
			out = append(out, Applied{Daemon: daemon, Section: o.section, Key: o.key, Old: o.value, New: f.value, Source: winners[id]})
		}
	}
	for id, f := range final {
		if _, ok := orig[id]; !ok { // added by an override
			out = append(out, Applied{Daemon: daemon, Section: f.section, Key: f.key, New: f.value, Added: true, Source: winners[id]})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !strings.EqualFold(out[i].Section, out[j].Section) {
			return strings.ToLower(out[i].Section) < strings.ToLower(out[j].Section)
		}
		return strings.ToLower(out[i].Key) < strings.ToLower(out[j].Key)
	})
	return out
}
