package config

import (
	"fmt"
	"strings"
)

// This file is RFC-0003 §4's migration: seed the bus sections from the dormant
// cross-mode bridge sections the bus architecture retired. A saved YSF2DMR /
// DMR2YSF / YSF2NXDN / DMR2NXDN / NXDN2DMR each carried a complete, working
// attachment pair; migration folds them into buses so the operator's old routing
// survives the redesign. It is one-way and additive: the bridge sections stay
// dormant (RFC-0001 disable-preserves-data), and nothing here deletes them.
//
// A mode may live on at most one bus (§5 rule 3), so bridges that share a mode
// MUST fold into one bus — three reframe modes cannot spread across separate
// buses without colliding on a mode. The reframe set {DMR,YSF,NXDN} is always a
// valid bus (§2), so the safe, always-valid migration is a SINGLE bus carrying
// every reframe mode any configured bridge mentions, each mode's params taken
// from the first bridge that carries that side. This matches the RFC's note that
// the two DMR-master bridges "may fold into the same bus if the operator wants
// DMR/YSF/NXDN to interoperate".

// migratedBusID is the stable id the seeded bus uses, so re-running migration is
// idempotent (an existing migrated bus is left alone, not duplicated).
const migratedBusID = "migrated"

// SeedBusesFromBridges builds the bus + attachment rows that reproduce the
// station's dormant cross-mode bridges (RFC-0003 §4), plus any operator-facing
// warnings (e.g. a DMR-master bridge whose master has no matching Networks[]
// entry, so its credentials_ref cannot be resolved). It does not mutate the
// model; the caller persists the result through SetBuses/SetAttachments (which
// re-validate). Returns ok=false with a warning when there is nothing to migrate
// or a migrated bus already exists.
func (m *Model) SeedBusesFromBridges() (buses []Bus, attachments []Attachment, warnings []string, ok bool) {
	for _, b := range m.Buses {
		if b.ID == migratedBusID {
			return nil, nil, []string{"A migrated bus already exists; the bridge settings were left untouched. Delete the migrated bus first to re-run migration."}, false
		}
	}

	// Collect the reframe modes each configured bridge implies, and the params for
	// each mode's attachment. "Configured" = enabled or carrying any setting, so a
	// saved-but-disabled bridge still migrates (its bus is created disabled).
	var (
		haveDMR, haveYSF, haveNXDN bool
		anyEnabled                 bool
		dmrDefaultTG               string
		dmrCredsMaster             string // DMR master address to resolve to a Networks[] entry
		nxdnID, nxdnTG, nxdnDefID  string
	)

	consider := func(enable, present bool) bool {
		if enable {
			anyEnabled = true
		}
		return present
	}

	// YSF2DMR: YSF <-> a DMR master. DMR side rides the local gateway; the master
	// becomes a credentials_ref. TG is the DMR-side target talkgroup.
	if b := m.YSF2DMR; consider(b.Enable, b.Enable || b.Master != "" || b.TG != "") {
		haveYSF, haveDMR = true, true
		setFirst(&dmrDefaultTG, b.TG)
		setFirst(&dmrCredsMaster, b.Master)
	}
	// DMR2YSF: DMR (local gateway, no secret) <-> YSF.
	if b := m.DMR2YSF; consider(b.Enable, b.Enable || b.DefaultTG != "") {
		haveDMR, haveYSF = true, true
		setFirst(&dmrDefaultTG, b.DefaultTG)
	}
	// YSF2NXDN: YSF <-> NXDN.
	if b := m.YSF2NXDN; consider(b.Enable, b.Enable || b.NXDNId != "" || b.TG != "") {
		haveYSF, haveNXDN = true, true
		setFirst(&nxdnID, b.NXDNId)
		setFirst(&nxdnTG, b.TG)
	}
	// DMR2NXDN: DMR (local gateway) <-> NXDN.
	if b := m.DMR2NXDN; consider(b.Enable, b.Enable || b.NXDNId != "") {
		haveDMR, haveNXDN = true, true
		setFirst(&nxdnDefID, b.NXDNId)
	}
	// NXDN2DMR: NXDN <-> a DMR master.
	if b := m.NXDN2DMR; consider(b.Enable, b.Enable || b.Master != "" || b.TG != "") {
		haveNXDN, haveDMR = true, true
		setFirst(&dmrDefaultTG, b.TG)
		setFirst(&dmrCredsMaster, b.Master)
		setFirst(&nxdnTG, b.NXDNTG)
	}

	if !haveDMR && !haveYSF && !haveNXDN {
		return nil, nil, []string{"No cross-mode bridge settings were found to migrate."}, false
	}

	bus := Bus{ID: migratedBusID, Name: "Migrated Bus", Enabled: anyEnabled}

	// Resolve the DMR credentials_ref by matching the bridge master to a Networks[]
	// entry (by address, RFC-0003 §3/§4). No match is not fatal — the attachment is
	// still created without a ref, and the operator is warned to create the network.
	dmrCredsRef := ""
	if dmrCredsMaster != "" {
		dmrCredsRef = m.networkNameForAddress(dmrCredsMaster)
		if dmrCredsRef == "" {
			warnings = append(warnings, fmt.Sprintf(
				"No Networks[] entry matches DMR master %q — create that network first, then re-run migration (the migrated bus's DMR attachment has no credential until then).", dmrCredsMaster))
		}
	}

	var seeded []Attachment
	if haveDMR {
		seeded = append(seeded, Attachment{
			BusID: bus.ID, Mode: ModeDMR, CredentialsRef: dmrCredsRef,
			Slot: "2", DefaultTG: dmrDefaultTG,
		})
	}
	if haveYSF {
		seeded = append(seeded, Attachment{BusID: bus.ID, Mode: ModeYSF})
	}
	if haveNXDN {
		seeded = append(seeded, Attachment{
			BusID: bus.ID, Mode: ModeNXDN,
			ID: nxdnID, TG: nxdnTG, DefaultID: nxdnDefID,
		})
	}

	// Merge with the existing rows the operator may already have, never duplicating
	// a mode already attached somewhere (that would fail validation).
	buses = append(append([]Bus(nil), m.Buses...), bus)
	attachments = mergeAttachments(m.Attachments, seeded, &warnings)
	return buses, attachments, warnings, true
}

// mergeAttachments appends the seeded rows to the existing ones, skipping any
// mode already attached elsewhere (warning about it) so the result always passes
// ValidateBuses.
func mergeAttachments(existing, seeded []Attachment, warnings *[]string) []Attachment {
	attached := make(map[Mode]string, len(existing))
	for _, a := range existing {
		attached[a.Mode] = a.BusID
	}
	out := append([]Attachment(nil), existing...)
	for _, a := range seeded {
		if bus, dup := attached[a.Mode]; dup {
			*warnings = append(*warnings, fmt.Sprintf(
				"%s is already attached to bus %q, so it was not migrated onto the new bus.", modeLabel(a.Mode), bus))
			continue
		}
		attached[a.Mode] = a.BusID
		out = append(out, a)
	}
	return out
}

// networkNameForAddress finds the Networks[] entry whose address matches a
// bridge master (case-insensitive, trimmed). Returns "" on no match.
func (m *Model) networkNameForAddress(addr string) string {
	want := strings.ToLower(strings.TrimSpace(addr))
	if want == "" {
		return ""
	}
	for _, n := range m.Networks {
		if strings.ToLower(strings.TrimSpace(n.Address)) == want {
			return n.Name
		}
	}
	return ""
}

// setFirst assigns v to *dst only if *dst is still empty (first-writer-wins), so
// the earliest configured bridge defines a shared param.
func setFirst(dst *string, v string) {
	if *dst == "" && v != "" {
		*dst = v
	}
}
