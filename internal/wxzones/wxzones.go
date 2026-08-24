// Package wxzones is the county table behind the Weather panel's county picker.
//
// The weather broadcast subscribes by SAME code — the six digits a NOAA weather
// radio is programmed with — and before this package existed an operator typed
// them in by hand. That is the wrong shape for the one field in the feature that
// decides *where* the node's alerts come from: a wrong digit is a silent failure
// in both directions, either no alerts at all or, worse, tornado warnings for a
// county four states away read out over a talkgroup. Nothing downstream can tell
// a typo from a deliberate choice, because 013121 is as valid a code as 012113.
//
// So the code is chosen from a list rather than typed, and this package is the
// list.
//
// # It ships whole, and never downloads
//
// The table is embedded (seed/counties.txt, ~95 KB, refreshed by
// cmd/wxzoneseed). There is deliberately no runtime refresh and no lookup
// service, which is the opposite of how the reflector hostlists work.
//
// Two reasons, and the second is the binding one:
//
//   - It barely changes. Counties are created, renamed and merged a handful of
//     times a decade — the last batch was Alaska's borough reorganisations and
//     Connecticut's planning regions. A list that moves that slowly does not need
//     a network at all; a release does the job.
//   - Privacy is a merge gate (GOVERNANCE.md principle 2, CLAUDE.md). A county
//     lookup is the single most location-revealing request a Waypoint node could
//     make — the query *is* "where am I" — and adding one to save a maintainer a
//     `go run` at release time is a bad trade. Nothing here opens a socket, and
//     the tests below pin that the package's whole surface is pure.
//
// The consequence is honest and worth stating: a county created after the
// release a node is running will not be in its picker. WXCounty carries the name
// and state alongside the code for the same class of reason, so a code that is
// no longer in the table still displays as the county it was chosen as.
package wxzones

import (
	_ "embed"
	"sort"
	"strings"
	"sync"
)

//go:embed seed/counties.txt
var seedCounties string

// County is one row of the table: a monitored area the alert feed can be
// subscribed to. The field set is exactly config.WXCounty's, because that is what
// the panel stores when one is picked — see Search's contract about copying all
// of it and not just the code.
type County struct {
	// SAME is the six-digit code the subscription is built from: P SS CCC, P=0
	// for a whole county, SS the state FIPS, CCC the county FIPS.
	SAME string `json:"same"`
	// UGC is the NWS Universal Geographic Code for the same area (FLC113). It is
	// what the alert itself is addressed to; carried so a stored county can be
	// matched against a CAP message without a second lookup.
	UGC string `json:"ugc"`
	// Name is the NWS name for the area, which is not always the county's legal
	// name — "Baltimore City", "Mainland Monroe", "Oahu in Honolulu". The NWS
	// name is the right one to show, because it is the one that will appear in
	// the alert text the operator hears read out.
	Name string `json:"name"`
	// State is the two-letter postal abbreviation, including territories.
	State string `json:"state"`
	// WFO is the Weather Forecast Office (CWA) that issues for the area, shown as
	// provenance. Seventeen counties span two offices; the table carries the
	// first the zone record lists. Nothing routes on this.
	WFO string `json:"wfo"`
}

// Label is the county as an operator reads it: "Santa Rosa, FL".
func (c County) Label() string {
	if c.State == "" {
		return c.Name
	}
	return c.Name + ", " + c.State
}

var (
	once     sync.Once
	all      []County          // in shipped order, which is ascending SAME
	bySAME   map[string]County // exact-code lookup
	searchOf []string          // normalized name, index-aligned with all
	squashOf []string          // same, with spaces removed; see squash()
)

// load parses the embedded table once. A malformed line is skipped rather than
// fatal: the table is generated and reviewed as a diff, so the realistic failure
// is a trailing blank or a comment, not a corrupt row, and a package that
// panicked at init would take the whole dashboard down with it.
func load() {
	once.Do(func() {
		lines := strings.Split(seedCounties, "\n")
		all = make([]County, 0, len(lines))
		bySAME = make(map[string]County, len(lines))
		searchOf = make([]string, 0, len(lines))
		squashOf = make([]string, 0, len(lines))
		for _, ln := range lines {
			ln = strings.TrimSpace(ln)
			if ln == "" || strings.HasPrefix(ln, "#") {
				continue
			}
			f := strings.Split(ln, "|")
			if len(f) != 5 {
				continue
			}
			c := County{SAME: f[0], UGC: f[1], Name: f[2], State: f[3], WFO: f[4]}
			if len(c.SAME) != 6 || c.Name == "" {
				continue
			}
			if _, dup := bySAME[c.SAME]; dup {
				continue
			}
			all = append(all, c)
			bySAME[c.SAME] = c
			// The haystack a query's name tokens are matched against. The state
			// is deliberately NOT folded in here: it is matched separately, as
			// the whole abbreviation only, so "fl" narrows to Florida rather
			// than also hitting every county with "fl" inside its name.
			n := normalize(c.Name)
			searchOf = append(searchOf, n)
			squashOf = append(squashOf, squash(n))
		}
	})
}

// Count is how many counties the shipped table holds.
func Count() int {
	load()
	return len(all)
}

// Lookup resolves an exact SAME code. It is what turns the six digits already in
// a saved configuration back into a name for the panel to show.
func Lookup(same string) (County, bool) {
	load()
	c, ok := bySAME[strings.TrimSpace(same)]
	return c, ok
}

// All returns the whole table in shipped order. The slice is a copy: callers
// serialize it and one that could sort the package's own storage in place would
// change what every later search matched.
func All() []County {
	load()
	out := make([]County, len(all))
	copy(out, all)
	return out
}

// normalize folds a name or query to the form both sides of a match are compared
// in: lowercase, and punctuation reduced to spaces.
//
// The punctuation rule is the whole reason this is not strings.Contains. County
// names carry apostrophes and periods that nobody types the same way twice —
// "St. Mary's", "Prince George's", "O'Brien", "DeKalb" vs "De Kalb" — and an
// operator searching for their own county should not have to guess which. Folding
// both sides means "st marys", "St. Mary's" and "stmarys" all reach the same row.
func normalize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == ',':
			b.WriteByte(' ')
		default:
			// Apostrophes and periods are DROPPED, not spaced: "St. Mary's"
			// becomes "st marys", so the word boundary a reader sees is the one
			// the matcher sees.
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// squash removes the spaces normalize() left, so a name matches however the
// operator spaces it.
//
// This exists because of a real row, not a hypothetical. Six states have a
// DeKalb County and the NWS table does not spell it the same way twice: one word
// in Alabama, Georgia and Missouri, two words in Illinois, Indiana and
// Tennessee. Matching on words alone, "dekalb" found three of them and "de kalb"
// found the other three, and an operator in De Kalb County, Illinois searching
// the way their neighbours in Georgia would got nothing at all. Comparing both
// sides with the spaces taken out makes the spelling stop mattering.
func squash(s string) string { return strings.ReplaceAll(s, " ", "") }

// Search returns counties matching a free-text query, best first, capped at
// limit (<=0 means no cap).
//
// The query is matched token by token, and EVERY token must match something —
// "santa rosa fl" is a narrowing search, not a widening one. A token matches
// when it is:
//
//   - a prefix of the SAME code or the UGC ("0121", "flc113"), or
//   - the state abbreviation exactly ("fl"), or
//   - a prefix of any word of the name ("san", "rosa").
//
// Prefix rather than substring on words is deliberate. An operator types the
// start of the name they are looking for; substring matching on a three-letter
// token turns a 3,269-row table into a list of everything containing "ana" and
// buries the county they wanted.
//
// Ranking is by how early and how completely the query lands on the name, and
// ties break on the SAME code so the order is stable — the same query returns the
// same list in the same order every time, which the pure-render rule in CLAUDE.md
// wants and a picker that reshuffles under the operator's cursor would break.
func Search(query string, limit int) []County {
	load()
	q := normalize(query)
	if q == "" {
		return capped(All(), limit)
	}
	tokens := strings.Fields(q)

	type scored struct {
		c County
		s int
	}
	var hits []scored
	for i, c := range all {
		score, ok := match(tokens, c, searchOf[i], squashOf[i], q)
		if !ok {
			continue
		}
		hits = append(hits, scored{c, score})
	}
	// Score first, then alphabetically within a score, then the code as the final
	// tie-break so the order is total and the same query always returns the same
	// list -- which the pure-render rule in CLAUDE.md wants, and which a picker
	// that reshuffled under the operator's cursor would break.
	//
	// Alphabetical, specifically, and not "shortest name first". Length was the
	// tie-break at first, to put an exact "Monroe" above "Mainland Monroe". It
	// does that, but the band above already does it better -- an exact name match
	// outscores a partial one by 300 -- and length wrecks the common case:
	// searching "fl" ties all 67 Florida counties on score, and shortest-first
	// opened the list with Bay, Lee, Clay, Gulf. Alphabetical is what an operator
	// scanning for their own county expects to read.
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].s != hits[j].s {
			return hits[i].s > hits[j].s
		}
		if hits[i].c.State != hits[j].c.State {
			return hits[i].c.State < hits[j].c.State
		}
		if hits[i].c.Name != hits[j].c.Name {
			return hits[i].c.Name < hits[j].c.Name
		}
		return hits[i].c.SAME < hits[j].c.SAME
	})
	out := make([]County, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.c)
	}
	return capped(out, limit)
}

// match reports whether every token lands, and how well the whole query does.
// Scores are spread widely so a later tweak to one band cannot silently reorder
// another.
func match(tokens []string, c County, name, squashed, whole string) (int, bool) {
	words := strings.Fields(name)
	state := strings.ToLower(c.State)
	same := c.SAME
	ugc := strings.ToLower(c.UGC)
	wholeSquashed := squash(whole)

	// Two ways in, because neither alone covers the DeKalb/De Kalb split in both
	// directions. Per-token matching lets a later token narrow ("dekalb il");
	// matching the whole query with its spaces taken out lets "de kalb" reach a
	// row the table spells as one word.
	allTokens := true
	for _, t := range tokens {
		if !matchToken(t, words, squashed, state, same, ugc) {
			allTokens = false
			break
		}
	}
	if !allTokens && !strings.HasPrefix(squashed, wholeSquashed) {
		return 0, false
	}

	score := 0
	switch {
	case same == whole || ugc == whole:
		score = 1000 // the operator typed the code itself
	case strings.HasPrefix(same, whole) || strings.HasPrefix(ugc, whole):
		score = 900
	case name == whole:
		score = 800 // exact name
	case strings.HasPrefix(name, whole):
		score = 700 // "santa" -> "santa rosa"
	case strings.HasPrefix(squashed, wholeSquashed):
		score = 690 // "dekalb" -> "De Kalb", and "de kalb" -> "DeKalb"
	case strings.HasPrefix(name+" "+state, whole):
		score = 650 // "santa rosa f" -> "santa rosa fl"
	default:
		score = 500
	}

	// A token that IS a state abbreviation outranks the same two letters used as
	// a name prefix, and by more than a whole band.
	//
	// This is not hypothetical tuning: "fl" is both Florida and the start of
	// Floyd, Fleming, Florence and Fluvanna, which exist in eleven other states
	// between them. Without this an operator in Florida types their state and
	// gets Floyd, Georgia first. Excluding name matches outright would be the
	// other obvious fix and is worse — somebody typing "de" for Delaware and
	// somebody typing "de" toward "Deschutes" are both right, and a search that
	// answered only one of them would be wrong half the time instead of ranking.
	for _, t := range tokens {
		if t == state {
			score += 400
			break
		}
	}

	return score, true
}

func matchToken(t string, words []string, squashed, state, same, ugc string) bool {
	if t == state {
		return true
	}
	if strings.HasPrefix(same, t) || strings.HasPrefix(ugc, t) {
		return true
	}
	for _, w := range words {
		if strings.HasPrefix(w, t) {
			return true
		}
	}
	// The spacing-insensitive form, so "dekalb" reaches "De Kalb" and a further
	// token can still narrow it.
	return strings.HasPrefix(squashed, t)
}

func capped(cs []County, limit int) []County {
	if limit > 0 && len(cs) > limit {
		return cs[:limit]
	}
	return cs
}
