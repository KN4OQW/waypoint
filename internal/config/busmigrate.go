package config

import (
	"fmt"
	"strings"

	"github.com/KN4OQW/waypoint/internal/store"
)

// Migration from the dormant bridge sections to mode buses (RFC-0003 §4). The
// five retired MMDVM_CM bridge sections (ysf2dmr, dmr2ysf, ysf2nxdn, dmr2nxdn,
// nxdn2dmr) each encode a complete, previously-working attachment PAIR. This
// one-way, operator-invoked seeding reads them and produces buses+attachments.
// It is one-way: the dormant sections are never modified or deleted (RFC-0001
// disable-preserves-data) — they stay as the audit trail.
//
// Secret handling: a fat bridge (YSF2DMR/NXDN2DMR) carried its own DMR-master
// Address+Password. Migration NEVER copies either into a bus row (RFC-0003 §3: a
// bus holds no credential). Instead the DMR attachment's CredentialsRef points at
// the existing Networks[] entry whose Address matches the bridge's Master, reusing
// the secret Waypoint already models once. If no network matches, migration
// returns a warning and leaves CredentialsRef blank — it never mints a credential,
// and the master's password is never surfaced in migration output.

// MigrateBridges builds the candidate buses+attachments that seed from the dormant
// bridge sections. It is a pure function: it reads the model and returns the seed
// plus any operator warnings (unmatched DMR master; a mode that would land on more
// than one bus). It writes nothing. A dormant section that was never configured
// (its zero value) seeds nothing.
func MigrateBridges(m *Model) (buses []Bus, attachments []Attachment, warnings []string) {
	// matchNetwork finds the Networks[] entry whose Address is the bridge Master, so
	// the migrated DMR attachment reuses that credential by name (never by value).
	matchNetwork := func(master string) string {
		master = strings.TrimSpace(master)
		if master == "" {
			return ""
		}
		for _, n := range m.Networks {
			if strings.EqualFold(strings.TrimSpace(n.Address), master) {
				return n.Name
			}
		}
		return ""
	}
	add := func(id, name string, enabled bool, atts ...Attachment) {
		buses = append(buses, Bus{ID: id, Name: name, Enabled: enabled})
		attachments = append(attachments, atts...)
	}
	// fatMaster resolves a fat bridge's credentials_ref and warns when the Master
	// matches no existing network (the password is never named in the warning).
	fatMaster := func(section, master string) string {
		if strings.TrimSpace(master) == "" {
			return ""
		}
		ref := matchNetwork(master)
		if ref == "" {
			warnings = append(warnings, fmt.Sprintf(
				"%s: DMR master %q matches no network — set the DMR attachment's credentials_ref before enabling this bus",
				section, master))
		}
		return ref
	}

	// YSF2DMR (fat bridge): YSF <-> DMR. Target TG = the DMR side's StartupDstId
	// (YSF2DMR.TG); the DMR attachment reuses the master's network by name.
	if m.YSF2DMR != (YSF2DMR{}) {
		ref := fatMaster("ysf2dmr", m.YSF2DMR.Master)
		add("ysf2dmr", "YSF-DMR (migrated)", m.YSF2DMR.Enable,
			Attachment{BusID: "ysf2dmr", Mode: ModeYSF, Target: m.YSF2DMR.TG, WiresXPassthrough: true},
			Attachment{BusID: "ysf2dmr", Mode: ModeDMR, DefaultTG: m.YSF2DMR.TG, CredentialsRef: ref},
		)
	}
	// DMR2YSF (loopback bridge): DMR <-> YSF. DMR-side default TG = DefaultDstTG.
	if m.DMR2YSF != (DMR2YSF{}) {
		add("dmr2ysf", "DMR-YSF (migrated)", m.DMR2YSF.Enable,
			Attachment{BusID: "dmr2ysf", Mode: ModeDMR, DefaultTG: m.DMR2YSF.DefaultTG},
			Attachment{BusID: "dmr2ysf", Mode: ModeYSF},
		)
	}
	// YSF2NXDN (loopback bridge): YSF <-> NXDN. Target NXDN TG = YSF2NXDN.TG; the
	// NXDN id it registers with = NXDNId.
	if m.YSF2NXDN != (YSF2NXDN{}) {
		add("ysf2nxdn", "YSF-NXDN (migrated)", m.YSF2NXDN.Enable,
			Attachment{BusID: "ysf2nxdn", Mode: ModeYSF, Target: m.YSF2NXDN.TG},
			Attachment{BusID: "ysf2nxdn", Mode: ModeNXDN, ID: m.YSF2NXDN.NXDNId, TG: m.YSF2NXDN.TG},
		)
	}
	// DMR2NXDN (loopback bridge): DMR <-> NXDN. NXDN-side default id = NXDNId.
	if m.DMR2NXDN != (DMR2NXDN{}) {
		add("dmr2nxdn", "DMR-NXDN (migrated)", m.DMR2NXDN.Enable,
			Attachment{BusID: "dmr2nxdn", Mode: ModeDMR},
			Attachment{BusID: "dmr2nxdn", Mode: ModeNXDN, DefaultID: m.DMR2NXDN.NXDNId},
		)
	}
	// NXDN2DMR (fat bridge): NXDN <-> DMR. NXDN-side listen TG = NXDNTG; target DMR
	// TG = StartupDstId (NXDN2DMR.TG); the DMR attachment reuses the master by name.
	if m.NXDN2DMR != (NXDN2DMR{}) {
		ref := fatMaster("nxdn2dmr", m.NXDN2DMR.Master)
		add("nxdn2dmr", "NXDN-DMR (migrated)", m.NXDN2DMR.Enable,
			Attachment{BusID: "nxdn2dmr", Mode: ModeNXDN, TG: m.NXDN2DMR.NXDNTG, DefaultID: busNXDNDefaultID},
			Attachment{BusID: "nxdn2dmr", Mode: ModeDMR, DefaultTG: m.NXDN2DMR.TG, CredentialsRef: ref},
		)
	}

	// A mode may live on at most one bus (RFC-0003 §5 rule 3). When several dormant
	// bridges shared a mode (e.g. both DMR2YSF and DMR2NXDN use DMR), seeding them as
	// separate buses collides. Surface it as a warning rather than silently dropping
	// — the operator folds the sharing buses into one before saving, and
	// ApplyBridgeMigration refuses to persist an invalid seed.
	if err := ValidateBuses(buses, attachments, m.Networks); err != nil {
		warnings = append(warnings, "migrated buses overlap on a mode ("+err.Error()+
			"); fold the sharing buses into one before saving")
	}
	return buses, attachments, warnings
}

// ApplyBridgeMigration is the store-level, one-shot hook: it reads the dormant
// bridge sections, seeds buses+attachments, and persists them — but only when the
// result validates and the store has no buses yet (so it can never clobber
// operator-created buses or persist an invalid seed). It returns the migration
// warnings for the operator either way, and leaves the dormant sections untouched.
func ApplyBridgeMigration(s *store.Store, by string) (warnings []string, err error) {
	m, err := Load(s)
	if err != nil {
		return nil, err
	}
	if len(m.Buses) > 0 {
		return []string{"buses already exist; migration is one-shot and was skipped to avoid clobbering them"}, nil
	}
	buses, attachments, warnings := MigrateBridges(m)
	if len(buses) == 0 {
		return warnings, nil // nothing configured to migrate
	}
	if verr := ValidateBuses(buses, attachments, m.Networks); verr != nil {
		return warnings, nil // warning already names the overlap; do not persist an invalid seed
	}
	if err := s.Set("buses", buses, by); err != nil {
		return warnings, err
	}
	if err := s.Set("attachments", attachments, by); err != nil {
		return warnings, err
	}
	return warnings, nil
}
