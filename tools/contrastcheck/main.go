// Command contrastcheck validates the UI's colour palette against WCAG 2.1 AA.
//
// The axe job in .github/workflows/a11y.yml is the real accessibility gate, but
// it needs a built daemon, a Chromium download and a few minutes. Almost every
// contrast regression this project has actually had was a palette edit — a token
// nudged a few points and taking some unrelated panel below 4.5:1 with it — and
// that class of bug is decidable from the stylesheet alone, in milliseconds, with
// no browser. This tool is that check, in the same spirit as tools/localecheck:
// tell the author what is wrong in CI in seconds rather than after a scan.
//
// It does NOT replace the axe run. It knows only about colour pairs that are
// declared in the palette blocks; it cannot see a hard-coded literal in a JS
// template, a computed blend, or any non-colour rule. A clean run here means the
// palette is sound, not that the UI is accessible.
//
// What it checks:
//
//   - Every pair in the table below clears 4.5:1, in each of the three accent
//     themes times both display modes.
//   - The palette blocks in index.html and settings.html agree. They are
//     duplicated in the two shells, and a fix applied to one and not the other
//     is the most likely way for this to regress. See #121, where the light-mode
//     accent had to be corrected in both.
//
// Every pair carries the CSS rule it rests on, because a contrast assertion that
// does not name the rule producing it cannot be reviewed and cannot be corrected
// when the rule moves. Pairs whose surface could not be determined from the
// stylesheet are deliberately absent rather than guessed: an element with
// `background: transparent` takes its surface from a DOM ancestor, and inventing
// one produces findings that fire on a UI that is fine. Where such a pair is
// listed here, the ancestor is named in the evidence and was confirmed in the
// browser.
//
// Usage:
//
//	go run ./tools/contrastcheck [-dir ui/static] [-strict]
package main

import (
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// minRatio is the WCAG 2.1 AA threshold for normal-size text. Every pair in the
// table is normal-size: the smallest, .nav-head at 9px, is nowhere near the
// 18.66px/24px the large-text 3:1 allowance requires, so there is no second
// threshold to track.
const minRatio = 4.5

// A surface is how a background is arrived at: a token, optionally with further
// translucent tokens composited over it, innermost last. `.nav-item.on` paints
// var(--accent-soft) over the aside's var(--side), so the text inside it sits on
// the blend, which is what axe measures and what a reader sees.
type surface struct {
	base   string   // opaque token the stack bottoms out on
	layers []string // translucent tokens painted over base, in paint order
}

func surf(base string, layers ...string) surface { return surface{base: base, layers: layers} }

// A pair is one contrast assertion, and the rule that makes it true.
type pair struct {
	ink     string  // token used as the text colour
	surface surface // what that text sits on
	rule    string  // the CSS selector(s) producing the pair
	// evidence names where the ink and the surface each come from. Where the
	// element does not paint its own background, it names the ancestor that does.
	evidence string
}

// The checked pairs. Each was read out of ui/static/settings.html and confirmed
// against the running UI on 2026-08-02; the ratios quoted in #121's analysis come
// from the same pass. Line numbers are omitted on purpose — they rot — the
// selector is the durable reference.
var pairs = []pair{
	{
		ink: "--faint", surface: surf("--side"),
		rule:     ".nav-head, .theme-head",
		evidence: "color: var(--faint); both sit inside `aside`, which paints background: var(--side)",
	},
	{
		ink: "--dim", surface: surf("--side"),
		rule:     ".nav-item .sub",
		evidence: "color: var(--dim); .nav-item paints background: transparent, so the surface is the aside's var(--side)",
	},
	{
		ink: "--dim", surface: surf("--side", "--accent-soft"),
		rule:     ".nav-item.on .sub",
		evidence: "color: var(--dim); .nav-item.on paints background: var(--accent-soft) over the aside's var(--side)",
	},
	{
		ink: "--muted", surface: surf("--tag-bg"),
		rule:     ".nav-item .tag",
		evidence: "color: var(--muted); background: var(--tag-bg), both on the same rule",
	},
	{
		ink: "--accent", surface: surf("--swatch-bg"),
		rule:     ".pill.on",
		evidence: "color: var(--accent); button.pill paints background: transparent, and every .pill.on sits in a .toggle-row, which paints background: var(--swatch-bg)",
	},
	{
		ink: "--accent", surface: surf("--field"),
		rule:     ".row .accent",
		evidence: "color: var(--accent) overriding the color: var(--ink) on `.row input, .row select`, which paint background: var(--field)",
	},
	{
		ink: "--accent", surface: surf("--panel"),
		rule:     ".stat .v.accent",
		evidence: "color: var(--accent); the enclosing .card paints background: var(--panel)",
	},
	{
		ink: "--accent", surface: surf("--input-bg"),
		rule:     ".btn.accent",
		evidence: "color: var(--accent); background: var(--input-bg), both on the same rule",
	},
	{
		ink: "--bg", surface: surf("--accent"),
		rule:     ".btn.primary",
		evidence: "color: var(--bg); background: var(--accent), both on the same rule",
	},
	{
		ink: "--ink", surface: surf("--field"),
		rule:     ".row input, .row select",
		evidence: "color: var(--ink); background: var(--field), both on the same rule",
	},
	{
		ink: "--ink-body", surface: surf("--side"),
		rule:     ".nav-item .label",
		evidence: "color: var(--ink-body); .nav-item paints background: transparent, so the surface is the aside's var(--side)",
	},
	{
		ink: "--muted", surface: surf("--swatch-bg"),
		rule:     ".toggle-row .name .sub",
		evidence: "color: var(--muted); .toggle-row paints background: var(--swatch-bg)",
	},
}

// The variant axis. An empty theme is the base :root, which is phosphor; an empty
// mode is dark. Both are the product defaults under RFC-0009, and both are
// expressed in the CSS as the absence of an attribute rather than a value, which
// is why they are spelled "" here and not by name.
var (
	themes = []string{"", "amber", "ice"}
	modes  = []string{"", "light"}
)

func themeName(t string) string {
	if t == "" {
		return "phosphor"
	}
	return t
}

func modeName(m string) string {
	if m == "" {
		return "dark"
	}
	return m
}

// variantLabel names a palette block for a human. The base block's key is the
// empty string, which prints as nothing and reads as a formatting bug.
func variantLabel(v string) string {
	if v == "" {
		return "base"
	}
	return v
}

func main() {
	dir := flag.String("dir", filepath.Join("ui", "static"), "directory holding the UI shells")
	strict := flag.Bool("strict", false, "exit non-zero on any violation")
	flag.Parse()

	shells := []string{"index.html", "settings.html"}
	palettes := map[string]map[string]map[string]string{} // shell -> variant -> token -> value

	for _, s := range shells {
		path := filepath.Join(*dir, s)
		src, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "contrastcheck: %v\n", err)
			os.Exit(2)
		}
		palettes[s] = parseRootBlocks(string(src))
		if len(palettes[s]) == 0 {
			fmt.Fprintf(os.Stderr, "contrastcheck: %s: no :root blocks found\n", path)
			os.Exit(2)
		}
	}

	problems := 0

	// The two shells must declare the same palette. Checked before the ratios so
	// that a divergence is reported as a divergence, rather than as the same
	// contrast failure twice with different numbers.
	problems += compareShells(palettes[shells[0]], palettes[shells[1]], shells[0], shells[1])

	// Ratios are checked against settings.html, which carries every panel. The
	// equality check above is what makes that sufficient for index.html too.
	base := palettes["settings.html"]
	for _, theme := range themes {
		for _, mode := range modes {
			resolved := resolve(base, theme, mode)
			for _, p := range pairs {
				ink, ok := resolved[p.ink]
				if !ok {
					fmt.Printf("MISSING %s/%s  %s: token %s is not declared\n", themeName(theme), modeName(mode), p.rule, p.ink)
					problems++
					continue
				}
				bg, err := compositeSurface(resolved, p.surface)
				if err != nil {
					fmt.Printf("MISSING %s/%s  %s: %v\n", themeName(theme), modeName(mode), p.rule, err)
					problems++
					continue
				}
				fg, err := parseColor(ink)
				if err != nil {
					fmt.Printf("MISSING %s/%s  %s: %s: %v\n", themeName(theme), modeName(mode), p.rule, p.ink, err)
					problems++
					continue
				}
				// Text is composited onto its surface too: a translucent ink would
				// otherwise be scored at a contrast it never actually renders at.
				got := ratio(over(fg, bg), bg)
				if got < minRatio {
					fmt.Printf("FAIL %s/%s  %s\n", themeName(theme), modeName(mode), p.rule)
					fmt.Printf("       %.2f:1, needs %.1f:1 — %s %s on %s %s\n",
						got, minRatio, p.ink, hex(over(fg, bg)), describe(p.surface), hex(bg))
					fmt.Printf("       %s\n", p.evidence)
					problems++
				}
			}
		}
	}

	total := len(pairs) * len(themes) * len(modes)
	fmt.Printf("\n%d pair(s) checked across %d theme(s) × %d mode(s); %d problem(s).\n",
		total, len(themes), len(modes), problems)
	if problems > 0 {
		if *strict {
			fmt.Fprintln(os.Stderr, "\nContrast gate FAILED.")
			os.Exit(1)
		}
		fmt.Println("\nReporting only (-strict not set); not failing the build.")
	}
}

// compareShells reports tokens the two shells declare differently.
//
// Only tokens declared in *both* shells are a problem. The first version of this
// check treated a token present in one shell and absent in the other as drift
// too, and it was wrong: it fired on ten tokens that are simply scoped. The five
// --input-* and --pill-* tokens live in settings.html because the dashboard has
// no inputs and no pills, and a token a shell never reads cannot put anything out
// of contrast there. The hazard this check exists for is the one from #121 — a
// palette value corrected in one shell and forgotten in the other — and that only
// arises where both declare it.
//
// A missing *block* is still a problem: if one shell lacked the whole
// [data-mode="light"] rule, light mode would be broken there rather than scoped.
func compareShells(a, b map[string]map[string]string, nameA, nameB string) int {
	problems := 0
	variants := map[string]bool{}
	for v := range a {
		variants[v] = true
	}
	for v := range b {
		variants[v] = true
	}
	var names []string
	for v := range variants {
		names = append(names, v)
	}
	sort.Strings(names)

	for _, v := range names {
		av, bv := a[v], b[v]
		if av == nil {
			fmt.Printf("DIVERGED %s: block present in %s, absent in %s\n", variantLabel(v), nameB, nameA)
			problems++
			continue
		}
		if bv == nil {
			fmt.Printf("DIVERGED %s: block present in %s, absent in %s\n", variantLabel(v), nameA, nameB)
			problems++
			continue
		}
		var tn []string
		for t := range av {
			if _, both := bv[t]; both {
				tn = append(tn, t)
			}
		}
		sort.Strings(tn)
		for _, t := range tn {
			if av[t] != bv[t] {
				fmt.Printf("DIVERGED %s %s: %s has %q, %s has %q\n", variantLabel(v), t, nameA, av[t], nameB, bv[t])
				problems++
			}
		}
	}
	return problems
}

// rootBlock matches a :root rule and captures its selector and body. Only :root
// is considered: those are the palette blocks, and a token declared anywhere else
// is scoped to a component and not part of the theme axis.
var rootBlock = regexp.MustCompile(`(?s):root([^{]*)\{(.*?)\}`)

var declRe = regexp.MustCompile(`(--[A-Za-z0-9\-]+)\s*:\s*([^;]+);`)

// parseRootBlocks returns variant key -> token -> value. The variant key is the
// normalised selector suffix, e.g. "" for the base :root, "light" for
// :root[data-mode="light"], "light+amber" for the combined block.
func parseRootBlocks(src string) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, m := range rootBlock.FindAllStringSubmatch(src, -1) {
		key := variantKey(m[1])
		if out[key] == nil {
			out[key] = map[string]string{}
		}
		for _, d := range declRe.FindAllStringSubmatch(m[2], -1) {
			out[key][d[1]] = strings.TrimSpace(d[2])
		}
	}
	return out
}

var attrRe = regexp.MustCompile(`\[data-(mode|theme)="([^"]+)"\]`)

func variantKey(sel string) string {
	var mode, theme string
	for _, m := range attrRe.FindAllStringSubmatch(sel, -1) {
		if m[1] == "mode" {
			mode = m[2]
		} else {
			theme = m[2]
		}
	}
	switch {
	case mode != "" && theme != "":
		return mode + "+" + theme
	case mode != "":
		return mode
	case theme != "":
		return theme
	}
	return ""
}

// resolve overlays the palette blocks the way the cascade does: base, then the
// accent theme, then the display mode, then the mode+theme block. The order
// matters and mirrors the stylesheet's own, where the combined selector is last
// and wins on specificity.
func resolve(p map[string]map[string]string, theme, mode string) map[string]string {
	out := map[string]string{}
	apply := func(key string) {
		for k, v := range p[key] {
			out[k] = v
		}
	}
	apply("")
	if theme != "" {
		apply(theme)
	}
	if mode != "" {
		apply(mode)
	}
	if mode != "" && theme != "" {
		apply(mode + "+" + theme)
	}
	return out
}

type rgba struct {
	r, g, b float64
	a       float64
}

var (
	hexRe  = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)
	rgbaRe = regexp.MustCompile(`^rgba?\(\s*([0-9.]+)\s*,\s*([0-9.]+)\s*,\s*([0-9.]+)\s*(?:,\s*([0-9.]+)\s*)?\)$`)
)

func parseColor(s string) (rgba, error) {
	s = strings.TrimSpace(s)
	if m := hexRe.FindStringSubmatch(s); m != nil {
		h := m[1]
		if len(h) == 3 {
			h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
		}
		v, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			return rgba{}, err
		}
		return rgba{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff), 1}, nil
	}
	if m := rgbaRe.FindStringSubmatch(s); m != nil {
		f := func(x string) float64 { v, _ := strconv.ParseFloat(x, 64); return v }
		a := 1.0
		if m[4] != "" {
			a = f(m[4])
		}
		return rgba{f(m[1]), f(m[2]), f(m[3]), a}, nil
	}
	return rgba{}, fmt.Errorf("cannot parse colour %q", s)
}

// over composites fg onto an opaque bg (simple source-over; bg is always opaque
// here because every surface stack bottoms out on an opaque token).
func over(fg, bg rgba) rgba {
	if fg.a >= 1 {
		return rgba{fg.r, fg.g, fg.b, 1}
	}
	return rgba{
		fg.a*fg.r + (1-fg.a)*bg.r,
		fg.a*fg.g + (1-fg.a)*bg.g,
		fg.a*fg.b + (1-fg.a)*bg.b,
		1,
	}
}

// compositeSurface resolves a surface stack to the single opaque colour that a
// reader actually sees behind the text.
func compositeSurface(tokens map[string]string, s surface) (rgba, error) {
	baseVal, ok := tokens[s.base]
	if !ok {
		return rgba{}, fmt.Errorf("token %s is not declared", s.base)
	}
	cur, err := parseColor(baseVal)
	if err != nil {
		return rgba{}, fmt.Errorf("%s: %w", s.base, err)
	}
	if cur.a < 1 {
		return rgba{}, fmt.Errorf("surface base %s is translucent (%s); a stack must bottom out on an opaque token", s.base, baseVal)
	}
	for _, layer := range s.layers {
		lv, ok := tokens[layer]
		if !ok {
			return rgba{}, fmt.Errorf("token %s is not declared", layer)
		}
		lc, err := parseColor(lv)
		if err != nil {
			return rgba{}, fmt.Errorf("%s: %w", layer, err)
		}
		cur = over(lc, cur)
	}
	return cur, nil
}

func describe(s surface) string {
	if len(s.layers) == 0 {
		return s.base
	}
	return strings.Join(s.layers, " over ") + " over " + s.base
}

func hex(c rgba) string {
	r := func(v float64) int { return int(math.Round(math.Max(0, math.Min(255, v)))) }
	return fmt.Sprintf("#%02x%02x%02x", r(c.r), r(c.g), r(c.b))
}

// relLuminance and ratio implement WCAG 2.1 SC 1.4.3 exactly as written, so the
// numbers here can be compared against axe's without a fudge factor. Confirmed
// against axe-core's own output on the #121 scan: the blends it reported for
// .nav-item.on (#dae6f3 ice, #e8e3dc amber) reproduce to the byte.
func relLuminance(c rgba) float64 {
	f := func(v float64) float64 {
		v /= 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*f(c.r) + 0.7152*f(c.g) + 0.0722*f(c.b)
}

func ratio(a, b rgba) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}
