// Package publicview is the model layer for the opt-in public dashboard: the
// settings that decide what a visitor sees, the suppress list that decides who is
// excluded, and the operator-authored content (links, nets, branding) the page
// renders.
//
// Two rules shape everything here.
//
// The public surface is default-off. A node that has never been configured has
// enabled = false and every public route 404s — not 401, not an empty page. An
// operator opts in deliberately, and until they do the node discloses nothing.
//
// Validation happens on the way in, not on the way out. A URL with a javascript:
// scheme, a retention window of 9000 hours, or a purpose tag nobody defined is
// rejected or clamped at write time, so the read path — which is the one serving
// anonymous traffic — has nothing left to sanitize. The never-public list (daemon
// versions, local and peer IPs, precise coordinates, configuration, control
// affordances, audit internals) is deliberately absent from this package: it is
// enforced by the shape of the response structs the HTTP layer serializes, where
// a reflect-based field audit can prove it, rather than by a toggle an operator
// could flip the wrong way.
package publicview

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Retention bounds (D6). The public last-heard list, the counters, and the map
// marker age are all bounded by one operator-set window; admin views are not.
//
// The floor is 1 h rather than 0 because "public view on, retention 0" is a
// configuration that looks enabled and shows nothing, which reads as a bug from
// the outside. Turning the modules off is the way to show nothing.
const (
	MinRetentionHours     = 1
	MaxRetentionHours     = 168 // one week
	DefaultRetentionHours = 24
)

// PurposeTags are the predefined tags a node may advertise (D5). The set is
// closed: an operator picks from it, and anything else they want to say goes in
// the free-text field, which is sanitized at render rather than trusted here.
// Keeping it closed is what lets a future directory aggregate nodes by purpose
// without normalizing a thousand spellings of "emcomm".
var PurposeTags = []string{
	"personal_hotspot",
	"club_net",
	"emcomm",
	"net_control",
	"experimental",
	"demo_event",
}

// ErrUnknownTag, ErrBadURL, and ErrBadCallsign are the write-time rejections.
// They are distinct errors because the settings panel maps each to a different
// message, and because a test asserting "javascript: is refused" should fail loudly
// if the refusal ever becomes a generic one.
var (
	ErrUnknownTag  = errors.New("publicview: unknown purpose tag")
	ErrBadURL      = errors.New("publicview: link URL must be http or https")
	ErrBadCallsign = errors.New("publicview: not a callsign")
	ErrBadGrid     = errors.New("publicview: not a Maidenhead grid square")
)

// Settings is the public dashboard's configuration: one master switch and the
// per-field disclosure toggles beneath it.
//
// Every Show* field is read by the HTTP layer as "may this field appear in a
// response at all" — a field whose toggle is off is omitted from the JSON, not
// merely hidden by the page's JavaScript (D2). That distinction is the difference
// between a disclosure control and a decoration.
type Settings struct {
	Enabled bool `json:"enabled"`

	// Reach card — "how to reach this node". On by default once public view is on,
	// because a public node page that does not say what frequency to use has no
	// reason to exist.
	ShowFreq      bool `json:"show_freq"`
	ShowCCTS      bool `json:"show_cc_ts"`
	ShowTalkgroup bool `json:"show_talkgroup"`
	ShowMode      bool `json:"show_mode"`

	// Activity.
	ShowStatus    bool `json:"show_status"`
	ShowCounters  bool `json:"show_counters"`
	ShowLastHeard bool `json:"show_lastheard"`

	// ShowGrid is off by default. It is the one reach-card field that says where
	// the operator is, so it is opted into rather than out of.
	ShowGrid bool `json:"show_grid"`

	ShowPowerLine bool `json:"show_power_line"`
	ShowLinks     bool `json:"show_links"`
	ShowNets      bool `json:"show_nets"`
	ShowMap       bool `json:"show_map"`
	ShowQR        bool `json:"show_qr"`

	// RetentionHours bounds every public activity read. Out-of-range values are
	// clamped rather than rejected: this arrives from a slider, and a slider that
	// errors at its own extremes is a worse experience than one that stops.
	RetentionHours int `json:"retention_hours"`

	PowerLine       string   `json:"power_line"`
	PurposeTags     []string `json:"purpose_tags"`
	PurposeFreetext string   `json:"purpose_freetext"`

	// GridOverride lets an operator publish a grid square that is not derived from
	// the node's configured coordinates — the deliberate blur for someone who wants
	// to advertise their county without their driveway. Empty means "derive it".
	GridOverride string `json:"grid_override"`
}

// DefaultSettings is what a node that has never been configured looks like, and
// what the table's column defaults encode. It exists so tests and the settings
// panel have one authority for "off" rather than two that can disagree.
func DefaultSettings() Settings {
	return Settings{
		Enabled:        false,
		ShowFreq:       true,
		ShowCCTS:       true,
		ShowTalkgroup:  true,
		ShowMode:       true,
		ShowStatus:     true,
		ShowCounters:   true,
		ShowLastHeard:  true,
		ShowGrid:       false,
		ShowPowerLine:  true,
		ShowLinks:      true,
		ShowNets:       true,
		ShowMap:        true,
		ShowQR:         true,
		RetentionHours: DefaultRetentionHours,
		PurposeTags:    []string{},
	}
}

// Link is an operator-supplied external listing (club site, RepeaterBook entry).
// Nothing is scraped or auto-discovered; an operator types these in.
type Link struct {
	ID        int64  `json:"id"`
	Label     string `json:"label"`
	URL       string `json:"url"`
	SortOrder int    `json:"sort_order"`
}

// Net is one scheduled net or event. The schedule is free text rather than a
// structured recurrence rule because "Thursdays 20:00 local, except the first of
// the month" is what clubs actually publish, and a rule engine that cannot express
// it would be worse than a sentence. The columns are chosen so an iCal export
// could be added later without a migration.
type Net struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	ScheduleText string `json:"schedule_text"`
	Target       string `json:"target"`
	Note         string `json:"note"`
	SortOrder    int    `json:"sort_order"`
}

// Branding is the operator's identity block. The three fields have three different
// trust levels, and the packages that render them differ accordingly: LogoPath is a
// server-written path to a re-encoded raster, NarrativeMarkdown is rendered and
// then sanitized, and CustomHTML is served verbatim into a sandboxed iframe and
// never into the parent document.
type Branding struct {
	LogoPath          string `json:"logo_path"`
	NarrativeMarkdown string `json:"narrative_markdown"`
	CustomHTML        string `json:"custom_html"`
}

// Store reads and writes the public view's tables. It shares the configuration
// store's single connection, so its writes serialize with config writes rather
// than contending for the file lock — the same arrangement the auth subsystem uses
// for its credential tables.
//
// Unlike auth, this Store creates nothing: its tables come from the schema ladder,
// so their existence is a function of the schema version rather than of whether
// this constructor has happened to run.
type Store struct {
	db *sql.DB
}

// New attaches the public view model to the configuration store.
func New(s *store.Store) *Store { return &Store{db: s.DB()} }

// NewWithDB is New for callers that already hold the database handle, such as the
// migration tests.
func NewWithDB(db *sql.DB) *Store { return &Store{db: db} }

// Settings returns the current configuration. The row is seeded by the schema, so
// a missing row is a corrupted database rather than a first-run condition; it is
// reported as such instead of being papered over with defaults, because silently
// serving defaults for a store that has lost its settings could turn a disclosure
// toggle back on without anyone deciding to.
func (s *Store) Settings() (Settings, error) {
	var (
		v    Settings
		tags string
	)
	err := s.db.QueryRow(`
SELECT enabled, show_freq, show_cc_ts, show_talkgroup, show_mode,
       show_status, show_counters, show_lastheard, show_grid,
       show_power_line, show_links, show_nets, show_map, show_qr,
       retention_hours, power_line, purpose_tags, purpose_freetext, grid_override
  FROM public_view_settings WHERE id = 1`).Scan(
		&v.Enabled, &v.ShowFreq, &v.ShowCCTS, &v.ShowTalkgroup, &v.ShowMode,
		&v.ShowStatus, &v.ShowCounters, &v.ShowLastHeard, &v.ShowGrid,
		&v.ShowPowerLine, &v.ShowLinks, &v.ShowNets, &v.ShowMap, &v.ShowQR,
		&v.RetentionHours, &v.PowerLine, &tags, &v.PurposeFreetext, &v.GridOverride)
	if errors.Is(err, sql.ErrNoRows) {
		return Settings{}, errors.New("publicview: settings row missing — store was not created or migrated by this build")
	}
	if err != nil {
		return Settings{}, err
	}
	if err := json.Unmarshal([]byte(tags), &v.PurposeTags); err != nil {
		return Settings{}, fmt.Errorf("publicview: purpose_tags is not a JSON array: %w", err)
	}
	if v.PurposeTags == nil {
		v.PurposeTags = []string{}
	}
	return v, nil
}

// Enabled reports whether the public surface is on. It is separated from Settings
// because the gating middleware calls it on every public request and has no use
// for the rest of the row.
func (s *Store) Enabled() (bool, error) {
	var on bool
	err := s.db.QueryRow(`SELECT enabled FROM public_view_settings WHERE id = 1`).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		// Fail closed. A store that cannot say whether the operator opted in has
		// not opted in.
		return false, nil
	}
	return on, err
}

// SaveSettings validates and writes the configuration.
//
// Retention is clamped; tags and the grid override are rejected. The asymmetry is
// deliberate: a number outside its range has an obvious nearest legal value, an
// unrecognized tag key does not, and quietly dropping it would leave the operator
// looking at a panel that does not reflect what was saved.
func (s *Store) SaveSettings(v Settings) error {
	v.RetentionHours = clampRetention(v.RetentionHours)

	tags, err := normalizeTags(v.PurposeTags)
	if err != nil {
		return err
	}
	if v.GridOverride != "" {
		g, err := NormalizeGrid(v.GridOverride)
		if err != nil {
			return err
		}
		v.GridOverride = g
	}
	raw, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`
UPDATE public_view_settings SET
  enabled = ?, show_freq = ?, show_cc_ts = ?, show_talkgroup = ?, show_mode = ?,
  show_status = ?, show_counters = ?, show_lastheard = ?, show_grid = ?,
  show_power_line = ?, show_links = ?, show_nets = ?, show_map = ?, show_qr = ?,
  retention_hours = ?, power_line = ?, purpose_tags = ?, purpose_freetext = ?, grid_override = ?
 WHERE id = 1`,
		v.Enabled, v.ShowFreq, v.ShowCCTS, v.ShowTalkgroup, v.ShowMode,
		v.ShowStatus, v.ShowCounters, v.ShowLastHeard, v.ShowGrid,
		v.ShowPowerLine, v.ShowLinks, v.ShowNets, v.ShowMap, v.ShowQR,
		v.RetentionHours, v.PowerLine, string(raw), v.PurposeFreetext, v.GridOverride)
	return err
}

// clampRetention holds the window inside D6's bounds. A zero value means "not
// set" — it comes from a struct literal that never touched the field — and is
// given the default rather than the floor.
func clampRetention(h int) int {
	switch {
	case h == 0:
		return DefaultRetentionHours
	case h < MinRetentionHours:
		return MinRetentionHours
	case h > MaxRetentionHours:
		return MaxRetentionHours
	}
	return h
}

// normalizeTags rejects unknown keys and removes duplicates, returning a stable
// order so a round-trip through the store does not reorder what the panel shows.
func normalizeTags(in []string) ([]string, error) {
	known := make(map[string]bool, len(PurposeTags))
	for _, t := range PurposeTags {
		known[t] = true
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.TrimSpace(strings.ToLower(t))
		if t == "" {
			continue
		}
		if !known[t] {
			return nil, fmt.Errorf("%w: %q", ErrUnknownTag, t)
		}
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	sort.Strings(out)
	return out, nil
}

// ValidateURL accepts only absolute http and https URLs.
//
// The rejected schemes are the point: javascript: and data: are how a stored link
// becomes script execution in whoever clicks it, and vbscript: is the same trick
// with a longer memory. An allow-list of two schemes is the only version of this
// check that stays correct as new schemes are invented.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("%w: empty", ErrBadURL)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrBadURL, err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
	default:
		return "", fmt.Errorf("%w: scheme %q", ErrBadURL, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: no host", ErrBadURL)
	}
	return u.String(), nil
}
