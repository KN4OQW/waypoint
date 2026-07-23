package config

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Connection profiles (RFC-0006 / issue #3). A profile is a named snapshot of the
// mode-and-network subset of the config store — "what this node connects to and
// how". Activating one writes that whole subset back atomically and re-renders, so
// one action swaps the node's entire connection setup. Device identity, modem
// calibration, the LCD panel, station policy, and auth are deliberately NOT part
// of a profile, so a switch can never change the callsign, lose calibration, or
// lock the operator out (RFC-0001: "switching can't brick access or lose
// calibration").

// profileSections is the mode/network namespace a profile captures — the single
// source of truth for what a profile contains. Everything else in the model is
// profileExcluded; a test asserts the two partition Model.sections() with no gap
// or overlap, so a new store section forces a conscious in/out choice.
var profileSections = []string{
	"modes",
	"dmr", "dmrnet", "networks", "routes",
	"ysf", "ysfgw", "p25", "p25gw", "nxdn", "nxdngw",
	"dstar", "dstargw", "m17", "m17gw", "pocsag", "fm",
	"ysf2dmr", "dmr2ysf", "ysf2nxdn", "dmr2nxdn", "nxdn2dmr",
	// Mode buses (RFC-0003) are connection topology — "what this node connects to
	// and how" — and supersede the cross-mode bridges above, so they are captured
	// by a profile like the bridges they replace.
	"buses", "attachments",
}

// profileExcluded is every store section a profile must NOT touch: device
// identity + RF frequencies (general), modem calibration (modem), the display and
// physical LCD panel, and station retention policy (history). Auth is not a config
// section at all (its own tables), so it needs no entry here.
// Bus LAN peering (RFC-0016) is excluded: peers[] carries node-specific mTLS
// secrets and this-node identity (a pinned peer cert and this node's peering key),
// which must never travel in a portable profile, and remote_attachments[]
// references those peers, so it would dangle if carried without them. Peering is
// re-established per node by pairing, not by importing a profile.
var profileExcluded = []string{"general", "modem", "display", "lcd", "history", "update", "peers", "remote_attachments", "peering"}

// profileSecretFields registers the secret-bearing fields per section — shared by
// export scrub and activate reconcile so the two can never drift. The values are
// the labels reported in a profile's `sensitive` list.
var profileSecretFields = map[string]string{
	"networks": "networks[].password",
	"dstargw":  "dstargw.ircddb_password",
	"pocsag":   "pocsag.auth_key",
	"ysf2dmr":  "ysf2dmr.password",
	"nxdn2dmr": "nxdn2dmr.password",
}

// Fingerprint is the hardware context a profile was captured on (RFC-0001's
// export-format fingerprint block). It travels with an export so importing onto a
// differently-tuned board can warn. Board family and TCXO frequency join this
// when hardware detection (#18) lands — the schema reserves the room.
type Fingerprint struct {
	RXFreqHz  string `json:"rx_freq_hz,omitempty"`
	TXFreqHz  string `json:"tx_freq_hz,omitempty"`
	ModemPort string `json:"modem_port,omitempty"`
}

// Profile is a named snapshot of the profile-namespace sections. Sections holds
// each captured section's raw JSON (with real secrets for a locally-saved
// profile; scrubbed for an export artifact). Sensitive lists the secret keys that
// were scrubbed (export/import only).
type Profile struct {
	Name        string                     `json:"name"`
	CreatedAt   string                     `json:"created_at,omitempty"`
	UpdatedAt   string                     `json:"updated_at,omitempty"`
	Fingerprint Fingerprint                `json:"fingerprint"`
	Sensitive   []string                   `json:"sensitive,omitempty"`
	Sections    map[string]json.RawMessage `json:"sections"`
}

// profileData is the JSON blob stored in the profiles table's data column (the
// name and timestamps are columns, not part of the blob).
type profileData struct {
	Fingerprint Fingerprint                `json:"fingerprint"`
	Sensitive   []string                   `json:"sensitive,omitempty"`
	Sections    map[string]json.RawMessage `json:"sections"`
}

// CaptureProfile snapshots the current store's profile-namespace sections into a
// named Profile, with the real secrets (it never leaves the device). It is a pure
// projection — no render, no restart. A section absent from the store is captured
// as absent, not forced to a zero value.
func CaptureProfile(s *store.Store, name string) (*Profile, error) {
	p := &Profile{Name: name, Sections: map[string]json.RawMessage{}}
	for _, sec := range profileSections {
		raw, ok, err := s.Get(sec)
		if err != nil {
			return nil, err
		}
		if ok {
			p.Sections[sec] = raw
		}
	}
	var mo Modem
	if _, err := s.GetInto("modem", &mo); err != nil {
		return nil, err
	}
	p.Fingerprint = Fingerprint{RXFreqHz: mo.RXFreqHz, TXFreqHz: mo.TXFreqHz, ModemPort: mo.Port}
	return p, nil
}

// ActivateProfile writes a profile's sections back to the store in one
// transaction (atomic: all sections or none), reconciling secrets against the
// store's current values (a blank secret in the profile keeps the stored one — an
// imported profile can never blank a working password). Sections outside the
// profile namespace are never written. The caller re-renders + restarts (the same
// path as apply) after this returns.
func ActivateProfile(s *store.Store, p *Profile, by string) error {
	values := make(map[string]json.RawMessage, len(p.Sections))
	for sec, raw := range p.Sections {
		rec, err := reconcileSecret(s, sec, raw)
		if err != nil {
			return fmt.Errorf("profile %q section %q: %w", p.Name, sec, err)
		}
		values[sec] = rec
	}
	return s.SetMany(values, by)
}

// reconcileSecret returns section's raw with blank secrets replaced by the store's
// current value (the blank-keep convention, internal/config/networks.go). Sections
// with no secret pass through unchanged.
func reconcileSecret(s *store.Store, section string, raw json.RawMessage) (json.RawMessage, error) {
	switch section {
	case "networks":
		var incoming []Network
		if err := json.Unmarshal(raw, &incoming); err != nil {
			return nil, err
		}
		var current []Network
		if _, err := s.GetInto("networks", &current); err != nil {
			return nil, err
		}
		prior := make(map[string]string, len(current))
		for _, n := range current {
			prior[n.Name] = n.Password
		}
		for i := range incoming {
			if incoming[i].Password == "" {
				incoming[i].Password = prior[incoming[i].Name]
			}
		}
		return json.Marshal(incoming)
	case "dstargw":
		var in DStarGateway
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.IRCDDBPassword == "" {
			var cur DStarGateway
			if _, err := s.GetInto("dstargw", &cur); err != nil {
				return nil, err
			}
			in.IRCDDBPassword = cur.IRCDDBPassword
		}
		return json.Marshal(in)
	case "pocsag":
		var in POCSAG
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.AuthKey == "" {
			var cur POCSAG
			if _, err := s.GetInto("pocsag", &cur); err != nil {
				return nil, err
			}
			in.AuthKey = cur.AuthKey
		}
		return json.Marshal(in)
	case "ysf2dmr":
		var in YSF2DMR
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Password == "" {
			var cur YSF2DMR
			if _, err := s.GetInto("ysf2dmr", &cur); err != nil {
				return nil, err
			}
			in.Password = cur.Password
		}
		return json.Marshal(in)
	case "nxdn2dmr":
		var in NXDN2DMR
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Password == "" {
			var cur NXDN2DMR
			if _, err := s.GetInto("nxdn2dmr", &cur); err != nil {
				return nil, err
			}
			in.Password = cur.Password
		}
		return json.Marshal(in)
	default:
		return raw, nil
	}
}

// Export returns a copy of the profile with every secret scrubbed and the scrubbed
// keys named in Sensitive — the artifact safe to write to a file or share. No
// secret string survives (asserted by test).
func (p *Profile) Export() *Profile {
	out := &Profile{
		Name:        p.Name,
		CreatedAt:   p.CreatedAt,
		UpdatedAt:   p.UpdatedAt,
		Fingerprint: p.Fingerprint,
		Sections:    make(map[string]json.RawMessage, len(p.Sections)),
	}
	var sensitive []string
	for sec, raw := range p.Sections {
		scrubbed, keys := scrubSecret(sec, raw)
		out.Sections[sec] = scrubbed
		sensitive = append(sensitive, keys...)
	}
	sort.Strings(sensitive)
	out.Sensitive = sensitive
	return out
}

// scrubSecret blanks the secret fields of a section, returning the scrubbed raw
// and the labels of any secret that was actually set. A section with no secret
// (or no secret value) passes through unchanged.
func scrubSecret(section string, raw json.RawMessage) (json.RawMessage, []string) {
	label := profileSecretFields[section]
	switch section {
	case "networks":
		var arr []Network
		if json.Unmarshal(raw, &arr) != nil {
			return raw, nil
		}
		had := false
		for i := range arr {
			if arr[i].Password != "" {
				arr[i].Password = ""
				had = true
			}
		}
		b, _ := json.Marshal(arr)
		return b, labelIf(had, label)
	case "dstargw":
		var v DStarGateway
		if json.Unmarshal(raw, &v) != nil {
			return raw, nil
		}
		had := v.IRCDDBPassword != ""
		v.IRCDDBPassword = ""
		b, _ := json.Marshal(v)
		return b, labelIf(had, label)
	case "pocsag":
		var v POCSAG
		if json.Unmarshal(raw, &v) != nil {
			return raw, nil
		}
		had := v.AuthKey != ""
		v.AuthKey = ""
		b, _ := json.Marshal(v)
		return b, labelIf(had, label)
	case "ysf2dmr":
		var v YSF2DMR
		if json.Unmarshal(raw, &v) != nil {
			return raw, nil
		}
		had := v.Password != ""
		v.Password = ""
		b, _ := json.Marshal(v)
		return b, labelIf(had, label)
	case "nxdn2dmr":
		var v NXDN2DMR
		if json.Unmarshal(raw, &v) != nil {
			return raw, nil
		}
		had := v.Password != ""
		v.Password = ""
		b, _ := json.Marshal(v)
		return b, labelIf(had, label)
	default:
		return raw, nil
	}
}

func labelIf(had bool, label string) []string {
	if had && label != "" {
		return []string{label}
	}
	return nil
}

// IsActive reports whether a profile's captured setup already matches the live
// store — comparing secrets-scrubbed so a restored password never flips the flag.
// Honest by construction: a hand-edit to any profile section makes it read
// inactive (the live config no longer matches the snapshot).
func IsActive(s *store.Store, p *Profile) (bool, error) {
	current, err := CaptureProfile(s, p.Name)
	if err != nil {
		return false, err
	}
	a := current.Export().Sections
	b := p.Export().Sections
	if len(a) != len(b) {
		return false, nil
	}
	for sec, av := range a {
		bv, ok := b[sec]
		if !ok || !jsonEqual(av, bv) {
			return false, nil
		}
	}
	return true, nil
}

// jsonEqual compares two raw JSON values structurally (key order / whitespace
// insensitive).
func jsonEqual(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	xa, _ := json.Marshal(x)
	ya, _ := json.Marshal(y)
	return string(xa) == string(ya)
}

// --- profiles table (config.db, via store.DB(), like the auth subsystem) ---

// InitProfiles creates the profiles table if it does not exist. Called once at
// startup.
func InitProfiles(s *store.Store) error {
	_, err := s.DB().Exec(`CREATE TABLE IF NOT EXISTS profiles (
  name       TEXT PRIMARY KEY,
  data       TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
)`)
	return err
}

// SaveProfile upserts a profile row, preserving the original created_at on update.
func SaveProfile(s *store.Store, p *Profile) error {
	data, err := json.Marshal(profileData{Fingerprint: p.Fingerprint, Sensitive: p.Sensitive, Sections: p.Sections})
	if err != nil {
		return err
	}
	at := time.Now().UTC().Format(time.RFC3339)
	_, err = s.DB().Exec(
		`INSERT INTO profiles(name, data, created_at, updated_at) VALUES(?, ?, ?, ?)
		 ON CONFLICT(name) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		p.Name, string(data), at, at)
	return err
}

// GetProfile loads a profile by name, or (nil, nil) if absent.
func GetProfile(s *store.Store, name string) (*Profile, error) {
	var data, created, updated string
	err := s.DB().QueryRow(`SELECT data, created_at, updated_at FROM profiles WHERE name = ?`, name).
		Scan(&data, &created, &updated)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var pd profileData
	if err := json.Unmarshal([]byte(data), &pd); err != nil {
		return nil, err
	}
	return &Profile{
		Name: name, CreatedAt: created, UpdatedAt: updated,
		Fingerprint: pd.Fingerprint, Sensitive: pd.Sensitive, Sections: pd.Sections,
	}, nil
}

// ListProfiles returns every stored profile, name-sorted.
func ListProfiles(s *store.Store) ([]*Profile, error) {
	rows, err := s.DB().Query(`SELECT name, data, created_at, updated_at FROM profiles ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Profile
	for rows.Next() {
		var name, data, created, updated string
		if err := rows.Scan(&name, &data, &created, &updated); err != nil {
			return nil, err
		}
		var pd profileData
		if err := json.Unmarshal([]byte(data), &pd); err != nil {
			return nil, err
		}
		out = append(out, &Profile{
			Name: name, CreatedAt: created, UpdatedAt: updated,
			Fingerprint: pd.Fingerprint, Sensitive: pd.Sensitive, Sections: pd.Sections,
		})
	}
	return out, rows.Err()
}

// DeleteProfile removes a profile, reporting whether a row existed. It never
// touches the live config.
func DeleteProfile(s *store.Store, name string) (bool, error) {
	res, err := s.DB().Exec(`DELETE FROM profiles WHERE name = ?`, name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ProfileExists reports whether a profile of that name is stored (for import
// collision handling).
func ProfileExists(s *store.Store, name string) (bool, error) {
	var one int
	err := s.DB().QueryRow(`SELECT 1 FROM profiles WHERE name = ?`, name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}
