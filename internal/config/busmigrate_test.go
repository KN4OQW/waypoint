package config

import (
	"strings"
	"testing"
)

// Migration (RFC-0003 §4): the dormant bridge sections seed valid bus rows, with
// credentials resolved to Networks[] and warnings when they cannot be.

func TestSeedFromYSF2DMR(t *testing.T) {
	m := &Model{
		Networks: []Network{{Name: "BM_3102", Address: "3102.master.brandmeister.network"}},
		YSF2DMR:  YSF2DMR{Enable: true, Master: "3102.master.brandmeister.network", TG: "91"},
	}
	buses, atts, warnings, ok := m.SeedBusesFromBridges()
	if !ok {
		t.Fatalf("expected migration to seed a bus, warnings=%v", warnings)
	}
	if len(warnings) != 0 {
		t.Fatalf("a matching network should produce no warning, got %v", warnings)
	}
	if len(buses) != 1 || !buses[0].Enabled {
		t.Fatalf("want one enabled bus, got %+v", buses)
	}
	var dmr, ysf *Attachment
	for i := range atts {
		switch atts[i].Mode {
		case ModeDMR:
			dmr = &atts[i]
		case ModeYSF:
			ysf = &atts[i]
		}
	}
	if dmr == nil || ysf == nil {
		t.Fatalf("want DMR and YSF attachments, got %+v", atts)
	}
	if dmr.CredentialsRef != "BM_3102" {
		t.Fatalf("DMR attachment should resolve to the matching network, got %q", dmr.CredentialsRef)
	}
	if dmr.DefaultTG != "91" {
		t.Fatalf("DMR default TG should carry the bridge TG, got %q", dmr.DefaultTG)
	}
	// The seeded result must itself be valid (never seed an unstartable bus).
	if err := ValidateBuses(buses, atts, m.Networks); err != nil {
		t.Fatalf("seeded buses failed validation: %v", err)
	}
}

func TestSeedWarnsOnUnmatchedMaster(t *testing.T) {
	m := &Model{YSF2DMR: YSF2DMR{Enable: true, Master: "some.master.example", TG: "9"}}
	buses, atts, warnings, ok := m.SeedBusesFromBridges()
	if !ok {
		t.Fatal("migration should still proceed without a matching network")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "create that network first") {
		t.Fatalf("want a create-the-network-first warning, got %v", warnings)
	}
	for _, a := range atts {
		if a.Mode == ModeDMR && a.CredentialsRef != "" {
			t.Fatalf("unmatched master must leave credentials_ref blank, got %q", a.CredentialsRef)
		}
	}
	_ = buses
}

func TestSeedFoldsBridgesOntoOneBus(t *testing.T) {
	// Three bridges that share DMR must fold into ONE bus — DMR cannot be on two.
	m := &Model{
		Networks: []Network{{Name: "BM", Address: "m.example"}},
		YSF2DMR:  YSF2DMR{Enable: true, Master: "m.example", TG: "91"},
		NXDN2DMR: NXDN2DMR{Enable: true, Master: "m.example", TG: "91", NXDNTG: "20"},
		DMR2NXDN: DMR2NXDN{Enable: true, NXDNId: "12345"},
	}
	buses, atts, _, ok := m.SeedBusesFromBridges()
	if !ok {
		t.Fatal("expected a seeded bus")
	}
	if len(buses) != 1 {
		t.Fatalf("bridges sharing a mode must fold into one bus, got %d", len(buses))
	}
	seen := map[Mode]int{}
	for _, a := range atts {
		seen[a.Mode]++
	}
	if seen[ModeDMR] != 1 || seen[ModeYSF] != 1 || seen[ModeNXDN] != 1 {
		t.Fatalf("each mode must appear exactly once, got %v", seen)
	}
	if err := ValidateBuses(buses, atts, m.Networks); err != nil {
		t.Fatalf("folded bus must be valid: %v", err)
	}
}

func TestSeedNothingToMigrate(t *testing.T) {
	m := &Model{}
	_, _, warnings, ok := m.SeedBusesFromBridges()
	if ok {
		t.Fatal("an empty model has nothing to migrate")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "No cross-mode bridge") {
		t.Fatalf("want a nothing-to-migrate warning, got %v", warnings)
	}
}

func TestSeedIdempotent(t *testing.T) {
	m := &Model{
		Buses:   []Bus{{ID: migratedBusID, Name: "Migrated Bus", Enabled: true}},
		YSF2DMR: YSF2DMR{Enable: true, Master: "m.example", TG: "9"},
	}
	_, _, warnings, ok := m.SeedBusesFromBridges()
	if ok {
		t.Fatal("re-running migration with a migrated bus present must not seed again")
	}
	if len(warnings) == 0 || !strings.Contains(warnings[0], "already exists") {
		t.Fatalf("want an already-migrated warning, got %v", warnings)
	}
}

func TestSeedSkipsModeAlreadyAttached(t *testing.T) {
	// DMR is already on another bus; migration must not duplicate it, and warns.
	m := &Model{
		Buses:       []Bus{{ID: "existing", Name: "Existing", Enabled: true}},
		Attachments: []Attachment{{BusID: "existing", Mode: ModeDMR}},
		DMR2YSF:     DMR2YSF{Enable: true, DefaultTG: "9"},
	}
	buses, atts, warnings, ok := m.SeedBusesFromBridges()
	if !ok {
		t.Fatal("expected migration to proceed")
	}
	dmrCount := 0
	for _, a := range atts {
		if a.Mode == ModeDMR {
			dmrCount++
		}
	}
	if dmrCount != 1 {
		t.Fatalf("DMR must not be duplicated across buses, got %d attachments", dmrCount)
	}
	joined := strings.Join(warnings, " ")
	if !strings.Contains(joined, "already attached") {
		t.Fatalf("want an already-attached warning, got %v", warnings)
	}
	if err := ValidateBuses(buses, atts, m.Networks); err != nil {
		t.Fatalf("result must stay valid: %v", err)
	}
}
