package publicview

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrNotFound is returned by the update and delete paths when the row is gone.
// It is distinct so the settings panel can answer 404 rather than 500 for a
// second click on a delete button.
var ErrNotFound = errors.New("publicview: no such row")

// ---------------------------------------------------------------------------
// Suppress list (D8)
// ---------------------------------------------------------------------------

// Suppressed returns every suppressed base callsign, sorted. The list is small by
// construction — it holds the handful of people who have asked to be left off one
// node's page — so it is read whole and compared in memory rather than joined
// against on every query.
func (s *Store) Suppressed() ([]string, error) {
	rows, err := s.db.Query(`SELECT callsign FROM public_suppress_list ORDER BY callsign`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SuppressSet returns the suppress list as a lookup set keyed by base callsign.
// The public read paths take it once per request and test every candidate against
// it, which is what keeps suppression from becoming a per-row query.
func (s *Store) SuppressSet() (map[string]bool, error) {
	list, err := s.Suppressed()
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, c := range list {
		set[c] = true
	}
	return set, nil
}

// AddSuppressed normalizes and inserts a callsign. Adding one that is already
// there succeeds: the operator's intent is "this call must not appear", and that
// is already true.
func (s *Store) AddSuppressed(callsign string) (string, error) {
	c, err := ValidateCallsign(callsign)
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec(
		`INSERT INTO public_suppress_list(callsign, added_at) VALUES(?, ?)
		 ON CONFLICT(callsign) DO NOTHING`,
		c, time.Now().UTC().Format(time.RFC3339))
	return c, err
}

// RemoveSuppressed drops a callsign from the list, normalizing first so an
// operator can remove "n4abc-7" the same way they added it.
func (s *Store) RemoveSuppressed(callsign string) error {
	c := NormalizeCallsign(callsign)
	if c == "" {
		return fmt.Errorf("%w: %q", ErrBadCallsign, callsign)
	}
	res, err := s.db.Exec(`DELETE FROM public_suppress_list WHERE callsign = ?`, c)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("%w: %s", ErrNotFound, c)
	}
	return nil
}

// ---------------------------------------------------------------------------
// External links (D5)
// ---------------------------------------------------------------------------

// Links returns the operator's external listing links in display order.
func (s *Store) Links() ([]Link, error) {
	rows, err := s.db.Query(`SELECT id, label, url, sort_order FROM public_links ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Link{}
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.Label, &l.URL, &l.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// AddLink validates the URL scheme and inserts, returning the new row's id.
func (s *Store) AddLink(l Link) (int64, error) {
	label, u, err := checkLink(l)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO public_links(label, url, sort_order) VALUES(?, ?, ?)`, label, u, l.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateLink rewrites a link, revalidating the URL. An edit is not a lesser act
// than an insert — a link that started life as https:// can be edited to
// javascript:, and the check has to be on both paths or it is on neither.
func (s *Store) UpdateLink(l Link) error {
	label, u, err := checkLink(l)
	if err != nil {
		return err
	}
	return affectOne(s.db.Exec(`UPDATE public_links SET label = ?, url = ?, sort_order = ? WHERE id = ?`,
		label, u, l.SortOrder, l.ID))
}

// DeleteLink removes a link.
func (s *Store) DeleteLink(id int64) error {
	return affectOne(s.db.Exec(`DELETE FROM public_links WHERE id = ?`, id))
}

func checkLink(l Link) (label, u string, err error) {
	label = strings.TrimSpace(l.Label)
	if label == "" {
		return "", "", fmt.Errorf("%w: link label", ErrRequired)
	}
	u, err = ValidateURL(l.URL)
	if err != nil {
		return "", "", err
	}
	return label, u, nil
}

// ---------------------------------------------------------------------------
// Scheduled nets (D5)
// ---------------------------------------------------------------------------

// Nets returns the scheduled nets in display order.
func (s *Store) Nets() ([]Net, error) {
	rows, err := s.db.Query(`SELECT id, name, schedule_text, target, note, sort_order FROM public_nets ORDER BY sort_order, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below reports iteration failures
	out := []Net{}
	for rows.Next() {
		var n Net
		if err := rows.Scan(&n.ID, &n.Name, &n.ScheduleText, &n.Target, &n.Note, &n.SortOrder); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// AddNet inserts a scheduled net, returning the new row's id.
func (s *Store) AddNet(n Net) (int64, error) {
	if err := checkNet(&n); err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO public_nets(name, schedule_text, target, note, sort_order) VALUES(?, ?, ?, ?, ?)`,
		n.Name, n.ScheduleText, n.Target, n.Note, n.SortOrder)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateNet rewrites a scheduled net.
func (s *Store) UpdateNet(n Net) error {
	if err := checkNet(&n); err != nil {
		return err
	}
	return affectOne(s.db.Exec(
		`UPDATE public_nets SET name = ?, schedule_text = ?, target = ?, note = ?, sort_order = ? WHERE id = ?`,
		n.Name, n.ScheduleText, n.Target, n.Note, n.SortOrder, n.ID))
}

// DeleteNet removes a scheduled net.
func (s *Store) DeleteNet(id int64) error {
	return affectOne(s.db.Exec(`DELETE FROM public_nets WHERE id = ?`, id))
}

func checkNet(n *Net) error {
	n.Name = strings.TrimSpace(n.Name)
	n.ScheduleText = strings.TrimSpace(n.ScheduleText)
	n.Target = strings.TrimSpace(n.Target)
	n.Note = strings.TrimSpace(n.Note)
	if n.Name == "" {
		return fmt.Errorf("%w: net name", ErrRequired)
	}
	if n.ScheduleText == "" {
		return fmt.Errorf("%w: net schedule", ErrRequired)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Branding (D4)
// ---------------------------------------------------------------------------

// Branding returns the operator's identity block. A null logo_path — no logo has
// been uploaded — reads back as the empty string.
func (s *Store) Branding() (Branding, error) {
	var (
		b    Branding
		logo sql.NullString
	)
	err := s.db.QueryRow(`SELECT logo_path, narrative_markdown, custom_html FROM branding WHERE id = 1`).
		Scan(&logo, &b.NarrativeMarkdown, &b.CustomHTML)
	if errors.Is(err, sql.ErrNoRows) {
		return Branding{}, errors.New("publicview: branding row missing — store was not created or migrated by this build")
	}
	if err != nil {
		return Branding{}, err
	}
	b.LogoPath = logo.String
	return b, nil
}

// SaveBranding writes the whole identity block.
//
// Nothing is sanitized here, and that is deliberate rather than an omission.
// The narrative is stored as the Markdown the operator typed and sanitized when it
// is rendered to HTML, because sanitizing the input would corrupt the source an
// operator edits next. The custom HTML is stored verbatim because its whole
// contract is to be served verbatim into a sandboxed iframe that cannot reach the
// parent origin — sanitizing it would make the sandbox pointless and the feature
// useless at the same time.
func (s *Store) SaveBranding(b Branding) error {
	var logo any
	if b.LogoPath != "" {
		logo = b.LogoPath
	}
	_, err := s.db.Exec(
		`UPDATE branding SET logo_path = ?, narrative_markdown = ?, custom_html = ? WHERE id = 1`,
		logo, b.NarrativeMarkdown, b.CustomHTML)
	return err
}

// SetLogoPath writes only the logo path, so an upload does not have to read and
// rewrite the narrative and custom HTML around it.
func (s *Store) SetLogoPath(path string) error {
	var logo any
	if path != "" {
		logo = path
	}
	_, err := s.db.Exec(`UPDATE branding SET logo_path = ? WHERE id = 1`, logo)
	return err
}

// affectOne turns an Exec result into ErrNotFound when it changed nothing.
func affectOne(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}
