package provision

import (
	"database/sql"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Mirror is the config.db copy of the provisioned flag: a single boolean in the
// store's meta row, kept alongside auth's claimed_at stamp (RFC-0002) rather than
// in the settings key tree. That placement is deliberate — the settings tree is
// the config read model, compiled into the daemons' INI files and exported in
// profiles (RFC-0001, RFC-0006), and provisioning state is neither config nor
// something an operator should be able to import from someone else's profile.
//
// The marker file is authoritative. This mirror exists so the dashboard and the
// API can answer "is this node provisioned?" from the query they are already
// making, and so an operator reading config.db offline can see the answer. On
// disagreement the file wins; the mirror is a cache, and a stale one is a display
// bug, never a security decision.
type Mirror struct {
	db *sql.DB
}

// NewMirror attaches the provision mirror to the configuration store and ensures
// its column exists. It is idempotent: on an already-migrated store it is a no-op.
func NewMirror(s *store.Store) (*Mirror, error) {
	m := &Mirror{db: s.DB()}
	if err := m.migrate(); err != nil {
		return nil, err
	}
	return m, nil
}

// migrate adds meta.provisioned. meta predates this package, and SQLite has no
// "ADD COLUMN IF NOT EXISTS", so probe the column list first and make a re-run a
// clean no-op rather than a duplicate-column error.
func (m *Mirror) migrate() error {
	has, err := m.hasColumn("meta", "provisioned")
	if err != nil {
		return err
	}
	if !has {
		if _, err := m.db.Exec(`ALTER TABLE meta ADD COLUMN provisioned INTEGER`); err != nil {
			return err
		}
	}
	return nil
}

func (m *Mirror) hasColumn(table, col string) (bool, error) {
	rows, err := m.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return false, err
	}
	defer rows.Close() //nolint:errcheck // rows.Err() below is what reports iteration failures
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// Get returns the mirrored flag. A store that has never been written reads false
// — the same "needs setup" default the absent marker gives.
func (m *Mirror) Get() (bool, error) {
	var v sql.NullInt64
	if err := m.db.QueryRow(`SELECT provisioned FROM meta WHERE id = 1`).Scan(&v); err != nil {
		return false, err
	}
	return v.Valid && v.Int64 != 0, nil
}

// Set writes the mirrored flag.
func (m *Mirror) Set(provisioned bool) error {
	v := 0
	if provisioned {
		v = 1
	}
	_, err := m.db.Exec(`UPDATE meta SET provisioned = ? WHERE id = 1`, v)
	return err
}
