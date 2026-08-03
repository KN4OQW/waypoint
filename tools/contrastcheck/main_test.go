package main

import (
	"math"
	"testing"
)

func mustColor(t *testing.T, s string) rgba {
	t.Helper()
	c, err := parseColor(s)
	if err != nil {
		t.Fatalf("parseColor(%q): %v", s, err)
	}
	return c
}

func TestParseColor(t *testing.T) {
	cases := []struct {
		in         string
		r, g, b, a float64
	}{
		{"#ffffff", 255, 255, 255, 1},
		{"#000000", 0, 0, 0, 1},
		{"#12a35a", 18, 163, 90, 1},
		{"#FFF", 255, 255, 255, 1}, // shorthand, and case-insensitive
		{"  #eef1f6  ", 238, 241, 246, 1},
		{"rgb(31, 119, 201)", 31, 119, 201, 1},
		{"rgba(0,0,0,.25)", 0, 0, 0, 0.25},
		{"rgba(18, 163, 90, 0.12)", 18, 163, 90, 0.12},
	}
	for _, c := range cases {
		got := mustColor(t, c.in)
		if got.r != c.r || got.g != c.g || got.b != c.b || math.Abs(got.a-c.a) > 1e-9 {
			t.Errorf("parseColor(%q) = %+v, want {%v %v %v %v}", c.in, got, c.r, c.g, c.b, c.a)
		}
	}
	for _, bad := range []string{"", "red", "var(--accent)", "#12345", "hsl(120,50%,50%)"} {
		if _, err := parseColor(bad); err == nil {
			t.Errorf("parseColor(%q) succeeded, want an error", bad)
		}
	}
}

// The reference points from WCAG 2.1 SC 1.4.3 itself. If these move, every other
// number this tool prints is wrong.
func TestRatioAnchors(t *testing.T) {
	white, black := mustColor(t, "#ffffff"), mustColor(t, "#000000")
	if got := ratio(black, white); math.Abs(got-21) > 0.01 {
		t.Errorf("black on white = %.4f, want 21", got)
	}
	if got := ratio(white, white); math.Abs(got-1) > 1e-9 {
		t.Errorf("white on white = %.4f, want 1", got)
	}
	// Symmetry: the ratio does not care which is the ink. This is why
	// .btn.primary (--bg on --accent) needs no separate threshold from
	// .pill.on (--accent on --bg).
	if a, b := ratio(black, white), ratio(white, black); a != b {
		t.Errorf("ratio is not symmetric: %.4f vs %.4f", a, b)
	}
}

// The compositing is the part most likely to be subtly wrong, and it is
// checkable against something external: axe-core reported these exact blended
// backgrounds when it scanned .nav-item.on during the #121 analysis. Reproducing
// them here is what licenses this tool to make claims about translucent layers.
func TestCompositeMatchesAxeReportedBlends(t *testing.T) {
	tokens := map[string]string{
		"--side":              "#f3f5f9",
		"--accent-soft-amber": "rgba(154,93,5,0.12)",
		"--accent-soft-ice":   "rgba(31,119,201,0.12)",
	}
	cases := []struct {
		layer string
		want  string
		note  string
	}{
		{"--accent-soft-amber", "#e8e3dc", "axe reported this as the .nav-item.on .sub background in amber/light"},
		{"--accent-soft-ice", "#dae6f3", "axe reported this as the .nav-item.on .sub background in ice/light"},
	}
	for _, c := range cases {
		got, err := compositeSurface(tokens, surf("--side", c.layer))
		if err != nil {
			t.Fatalf("compositeSurface(%s): %v", c.layer, err)
		}
		if hex(got) != c.want {
			t.Errorf("compositeSurface(--side, %s) = %s, want %s (%s)", c.layer, hex(got), c.want, c.note)
		}
	}
}

func TestCompositeSurfaceErrors(t *testing.T) {
	tokens := map[string]string{
		"--side":  "#f3f5f9",
		"--ghost": "rgba(0,0,0,0.25)",
		"--junk":  "chartreuse",
	}
	// A stack must bottom out on something opaque; otherwise the result depends
	// on a DOM ancestor this tool cannot see, and a guess would produce findings
	// on a UI that is fine.
	if _, err := compositeSurface(tokens, surf("--ghost")); err == nil {
		t.Error("translucent surface base was accepted, want an error")
	}
	if _, err := compositeSurface(tokens, surf("--nope")); err == nil {
		t.Error("undeclared base token was accepted, want an error")
	}
	if _, err := compositeSurface(tokens, surf("--side", "--nope")); err == nil {
		t.Error("undeclared layer token was accepted, want an error")
	}
	if _, err := compositeSurface(tokens, surf("--junk")); err == nil {
		t.Error("unparseable colour was accepted, want an error")
	}
	// An opaque layer replaces what is under it rather than blending with it.
	got, err := compositeSurface(tokens, surf("--side", "--side"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if hex(got) != "#f3f5f9" {
		t.Errorf("opaque layer = %s, want #f3f5f9", hex(got))
	}
}

const fixtureCSS = `
  :root {
    --accent: #35d07f;
    --bg: #05070a;
    --side: #0a0d12;
    --faint: #6b7380;
  }
  :root[data-theme="amber"] { --accent: #f0a935; }
  :root[data-mode="light"] {
    --accent: #12a35a;
    --bg: #eef1f6;
    --side: #f3f5f9;
    --faint: #6b7380;
  }
  :root[data-mode="light"][data-theme="amber"] { --accent: #9a5d05; }
  .not-a-root { --accent: #ff0000; }
`

func TestParseRootBlocks(t *testing.T) {
	p := parseRootBlocks(fixtureCSS)
	for _, want := range []string{"", "amber", "light", "light+amber"} {
		if p[want] == nil {
			t.Fatalf("variant %q missing; got %v", want, keys(p))
		}
	}
	if len(p) != 4 {
		t.Errorf("got %d variants (%v), want 4 — a non-:root rule must not be collected", len(p), keys(p))
	}
	if got := p[""]["--accent"]; got != "#35d07f" {
		t.Errorf("base --accent = %q, want #35d07f", got)
	}
	if got := p["light+amber"]["--accent"]; got != "#9a5d05" {
		t.Errorf("light+amber --accent = %q, want #9a5d05", got)
	}
}

// The cascade order is the whole reason the combined block exists: it is last in
// the stylesheet and wins on specificity. Resolving in any other order silently
// scores amber/light against the wrong accent.
func TestResolveCascadeOrder(t *testing.T) {
	p := parseRootBlocks(fixtureCSS)
	cases := []struct {
		theme, mode, want string
	}{
		{"", "", "#35d07f"},           // phosphor/dark: the base block
		{"amber", "", "#f0a935"},      // theme only
		{"", "light", "#12a35a"},      // mode only
		{"amber", "light", "#9a5d05"}, // the combined block wins over both
		{"ice", "light", "#12a35a"},   // no ice block in the fixture: mode still applies
	}
	for _, c := range cases {
		got := resolve(p, c.theme, c.mode)["--accent"]
		if got != c.want {
			t.Errorf("resolve(theme=%q, mode=%q) --accent = %q, want %q", c.theme, c.mode, got, c.want)
		}
	}
	// Tokens not restated by a variant are inherited from the base.
	if got := resolve(p, "amber", "")["--bg"]; got != "#05070a" {
		t.Errorf("amber/dark --bg = %q, want the base #05070a", got)
	}
}

func TestVariantKey(t *testing.T) {
	cases := map[string]string{
		``:                                      "",
		`[data-theme="amber"]`:                  "amber",
		`[data-mode="light"]`:                   "light",
		`[data-mode="light"][data-theme="ice"]`: "light+ice",
		`[data-theme="ice"][data-mode="light"]`: "light+ice", // order-independent
	}
	for sel, want := range cases {
		if got := variantKey(sel); got != want {
			t.Errorf("variantKey(%q) = %q, want %q", sel, got, want)
		}
	}
}

func TestCompareShells(t *testing.T) {
	a := map[string]map[string]string{"": {"--accent": "#35d07f", "--bg": "#05070a"}}

	// Clean case first: identical palettes must be silent. A gate that fires on
	// a correct configuration is worse than no gate.
	same := map[string]map[string]string{"": {"--accent": "#35d07f", "--bg": "#05070a"}}
	if n := compareShells(a, same, "index.html", "settings.html"); n != 0 {
		t.Errorf("identical palettes reported %d problem(s), want 0", n)
	}

	// A token edited in one shell and not the other — the #121 hazard.
	drifted := map[string]map[string]string{"": {"--accent": "#0e7c45", "--bg": "#05070a"}}
	if n := compareShells(a, drifted, "index.html", "settings.html"); n != 1 {
		t.Errorf("one drifted token reported %d problem(s), want 1", n)
	}

	// A whole block present in one shell only.
	extra := map[string]map[string]string{
		"":      {"--accent": "#35d07f", "--bg": "#05070a"},
		"light": {"--accent": "#12a35a"},
	}
	if n := compareShells(a, extra, "index.html", "settings.html"); n != 1 {
		t.Errorf("a missing block reported %d problem(s), want 1", n)
	}

	// A token declared in one shell only is NOT drift — it is scoping, and it
	// must stay silent. settings.html declares five --input-*/--pill-* tokens
	// that index.html has no use for, because the dashboard has no inputs and no
	// pills; the first version of this check called all ten of those a problem.
	// A token a shell never reads cannot put anything out of contrast there.
	scoped := map[string]map[string]string{"": {
		"--accent": "#35d07f", "--bg": "#05070a", "--pill-bg": "#181d26",
	}}
	if n := compareShells(a, scoped, "index.html", "settings.html"); n != 0 {
		t.Errorf("a token scoped to one shell reported %d problem(s), want 0", n)
	}
	// ...but a scoped token must not mask a real divergence alongside it.
	both := map[string]map[string]string{"": {
		"--accent": "#0e7c45", "--bg": "#05070a", "--pill-bg": "#181d26",
	}}
	if n := compareShells(a, both, "index.html", "settings.html"); n != 1 {
		t.Errorf("drift alongside a scoped token reported %d problem(s), want 1", n)
	}
}

// Every pair must name the rule it rests on and the evidence for it. This is the
// house rule from CLAUDE.md — a check that cannot say what it rests on cannot be
// reviewed — and it is cheap enough to assert mechanically.
func TestEveryPairCitesItsRule(t *testing.T) {
	if len(pairs) == 0 {
		t.Fatal("no pairs declared")
	}
	for _, p := range pairs {
		if p.rule == "" {
			t.Errorf("pair %s on %s has no rule", p.ink, describe(p.surface))
		}
		if p.evidence == "" {
			t.Errorf("pair %s (%s) has no evidence", p.rule, p.ink)
		}
		if p.surface.base == "" {
			t.Errorf("pair %s has no surface base", p.rule)
		}
	}
}

// The gate must pass on a palette that is correct. Built from the values this
// branch lands on, so the check is exercised in the direction that matters:
// reporting nothing when nothing is wrong.
func TestNoFalsePositivesOnACompliantPalette(t *testing.T) {
	tokens := map[string]string{
		"--accent":      "#0e7c45",
		"--accent-soft": "rgba(14,124,69,0.12)",
		"--bg":          "#eef1f6",
		"--panel":       "#ffffff",
		"--field":       "#f5f7fb",
		"--side":        "#f3f5f9",
		"--input-bg":    "#ffffff",
		"--swatch-bg":   "#eef1f6",
		"--tag-bg":      "#f0f3f8",
		"--ink":         "#1a2130",
		"--ink-body":    "#333b49",
		"--faint":       "#5d646f",
		"--dim":         "#5c6471",
		"--muted":       "#5c6472",
	}
	for _, p := range pairs {
		bg, err := compositeSurface(tokens, p.surface)
		if err != nil {
			t.Fatalf("%s: %v", p.rule, err)
		}
		fg, err := parseColor(tokens[p.ink])
		if err != nil {
			t.Fatalf("%s: %s: %v", p.rule, p.ink, err)
		}
		if got := ratio(over(fg, bg), bg); got < minRatio {
			t.Errorf("%s: %.2f:1 on a palette that is supposed to pass (%s on %s)",
				p.rule, got, p.ink, describe(p.surface))
		}
	}
}

func keys(m map[string]map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
