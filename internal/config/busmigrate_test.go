package config

import (
	"strings"
	"testing"
)

// --- Retired bridge renderers, resurrected from git 9f15099 as fixtures --------
//
// RFC-0003 §6.5 requires each migrated bus to render semantically equivalent to
// what the retired MMDVM_CM bridge renderer produced. Rather than re-derive the
// expected output, these are the exact renderers removed in commit 9f15099
// (internal/config/render.go), copied verbatim with a `retired` prefix. The
// migration-equivalence test renders through them and compares parsed fields.

const (
	retiredDMRMasterPort   = "62031"
	retiredCrossDMRIds     = "/usr/local/etc/DMRIds.dat"
	retiredCrossNXDNIds    = "/usr/local/etc/NXDN.csv"
	retiredYSFNetworkPort  = "42000"
	retiredYSF2DMRYSFLocal = "42013"
	retiredYSF2NXDNYSFLoc  = "42014"
	retiredNXDN2DMRNXDNLoc = "42022"
	retiredNXDNNetworkPort = "14050"
)

func retiredBridgeInfo(b *strings.Builder, m *Model) {
	sect(b, "Info",
		kv("RXFrequency", m.Modem.RXFreqHz),
		kv("TXFrequency", m.Modem.TXFreqHz),
		kv("Power", def(m.General.Power, "1")),
		kv("Location", def(m.General.Location, "Waypoint")),
		kv("Description", "Waypoint"),
	)
}

func retiredBridgeLog(b *strings.Builder, root string) {
	sect(b, "Log",
		kv("DisplayLevel", "1"), kv("FileLevel", "0"), kv("FilePath", "/tmp"), kv("FileRoot", root),
	)
}

func retiredDMRMasterNet(b *strings.Builder, dmrID, master, password, tg, options string) {
	lines := []string{
		kv("Id", dmrID), kv("StartupDstId", def(tg, "9990")), kv("StartupPC", "0"),
		kv("Address", master), kv("Port", retiredDMRMasterPort), kv("Jitter", "500"),
		kv("Password", password),
	}
	if strings.TrimSpace(options) != "" {
		lines = append(lines, kv("Options", options))
	}
	lines = append(lines, kv("Debug", "0"))
	sect(b, "DMR Network", lines...)
}

func retiredRenderYSF2DMR(m *Model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	retiredBridgeInfo(&b, m)
	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign), kv("Suffix", "ND"),
		kv("DstAddress", "127.0.0.1"), kv("DstPort", retiredYSFNetworkPort),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", retiredYSF2DMRYSFLocal),
		kb("EnableWiresX", true), kb("RemoteGateway", false), kv("Daemon", "0"),
	)
	retiredDMRMasterNet(&b, firstNonEmpty(m.YSF2DMR.DMRId, m.DMR.ID, m.General.ID),
		m.YSF2DMR.Master, m.YSF2DMR.Password, m.YSF2DMR.TG, m.YSF2DMR.Options)
	sect(&b, "DMR Id Lookup", kv("File", retiredCrossDMRIds), kv("Time", "24"))
	retiredBridgeLog(&b, "YSF2DMR")
	return b.String()
}

func retiredRenderDMR2YSF(m *Model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign),
		kv("GatewayAddress", "127.0.0.1"), kv("GatewayPort", ysfMMDVMGatewayPort),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", ysfMMDVMLocalPort),
		kv("FCSRooms", fcsRoomsPath), kv("Daemon", "0"), kv("Debug", "0"),
	)
	sect(&b, "DMR Network",
		kv("Id", firstNonEmpty(m.DMR2YSF.DMRId, m.DMR.ID, m.General.ID)),
		kv("RptAddress", "127.0.0.1"), kv("RptPort", def(m.DMRNet.LocalPort, "62032")),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("DefaultDstTG", def(m.DMR2YSF.DefaultTG, "9")), kv("Debug", "0"),
	)
	sect(&b, "DMR Id Lookup", kv("File", retiredCrossDMRIds), kv("Time", "24"))
	retiredBridgeLog(&b, "DMR2YSF")
	return b.String()
}

func retiredRenderYSF2NXDN(m *Model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	retiredBridgeInfo(&b, m)
	sect(&b, "YSF Network",
		kv("Callsign", m.General.Callsign), kv("Suffix", "ND"),
		kv("DstAddress", "127.0.0.1"), kv("DstPort", retiredYSFNetworkPort),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", retiredYSF2NXDNYSFLoc),
		kb("EnableWiresX", true), kv("Daemon", "0"),
	)
	sect(&b, "NXDN Network",
		kv("Id", m.YSF2NXDN.NXDNId), kv("StartupDstId", def(m.YSF2NXDN.TG, "0")),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", nxdnMMDVMLocalPort),
		kv("DstAddress", "127.0.0.1"), kv("DstPort", nxdnMMDVMGatewayPort), kv("Debug", "0"),
	)
	sect(&b, "NXDN Id Lookup", kv("File", retiredCrossNXDNIds), kv("Time", "24"))
	retiredBridgeLog(&b, "YSF2NXDN")
	return b.String()
}

func retiredRenderDMR2NXDN(m *Model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	sect(&b, "NXDN Network",
		kv("GatewayAddress", "127.0.0.1"), kv("GatewayPort", nxdnMMDVMGatewayPort),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", nxdnMMDVMLocalPort),
		kv("DefaultID", def(m.DMR2NXDN.NXDNId, "65519")), kv("Daemon", "0"),
	)
	sect(&b, "DMR Network",
		kv("Id", firstNonEmpty(m.DMR2NXDN.DMRId, m.DMR.ID, m.General.ID)),
		kv("RptAddress", "127.0.0.1"), kv("RptPort", def(m.DMRNet.LocalPort, "62032")),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", def(m.DMRNet.GatewayPort, "62031")),
		kv("Debug", "0"),
	)
	sect(&b, "DMR Id Lookup", kv("File", retiredCrossDMRIds), kv("Time", "24"))
	sect(&b, "NXDN Id Lookup", kv("File", retiredCrossNXDNIds), kv("Time", "24"))
	retiredBridgeLog(&b, "DMR2NXDN")
	return b.String()
}

func retiredRenderNXDN2DMR(m *Model) string {
	var b strings.Builder
	b.WriteString(generatedHeader)
	retiredBridgeInfo(&b, m)
	sect(&b, "NXDN Network",
		kv("Callsign", m.General.Callsign), kv("TG", def(m.NXDN2DMR.NXDNTG, "20")),
		kv("DstAddress", "127.0.0.1"), kv("DstPort", retiredNXDNNetworkPort),
		kv("LocalAddress", "127.0.0.1"), kv("LocalPort", retiredNXDN2DMRNXDNLoc),
		kv("DefaultID", "65519"), kv("Daemon", "0"),
	)
	retiredDMRMasterNet(&b, firstNonEmpty(m.NXDN2DMR.DMRId, m.DMR.ID, m.General.ID),
		m.NXDN2DMR.Master, m.NXDN2DMR.Password, m.NXDN2DMR.TG, m.NXDN2DMR.Options)
	sect(&b, "DMR Id Lookup", kv("File", retiredCrossDMRIds), kv("Time", "24"))
	sect(&b, "NXDN Id Lookup", kv("File", retiredCrossNXDNIds), kv("Time", "24"))
	retiredBridgeLog(&b, "NXDN2DMR")
	return b.String()
}

// --- Migration equivalence -----------------------------------------------------

// baseBridgeModel is the shared station context every bridge renders against.
func baseBridgeModel() *Model {
	return &Model{
		General: General{Callsign: "KN4OQW", ID: "3180202"},
		Modem:   Modem{RXFreqHz: "433900000", TXFreqHz: "438900000"},
		DMR:     DMR{ID: "3180202"},
		DMRNet:  DMRNet{LocalPort: "62032", GatewayPort: "62031"},
	}
}

// migratedBusConfig runs the model through MigrateBridges, renders the one bus
// with the given id, and parses it back. It also asserts the migrated bus is
// itself valid (an invalid seed can never be persisted).
func migratedBusConfig(t *testing.T, m *Model, busID string) *BusConfig {
	t.Helper()
	buses, atts, _ := MigrateBridges(m)
	if err := ValidateBuses(buses, atts, m.Networks); err != nil {
		t.Fatalf("migrated bus is invalid: %v", err)
	}
	seed := &Model{Buses: buses, Attachments: atts, DMRNet: m.DMRNet}
	var target *Bus
	for i := range seed.Buses {
		if seed.Buses[i].ID == busID {
			target = &seed.Buses[i]
		}
	}
	if target == nil {
		t.Fatalf("migration produced no bus %q", busID)
	}
	cfg, err := ParseBusConfig(strings.NewReader(seed.renderBus(*target)))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func attByMode(cfg *BusConfig, mode string) BusAttachment {
	for _, a := range cfg.Attachments {
		if a.Mode == mode {
			return a
		}
	}
	return BusAttachment{}
}

// TestMigrationEquivalenceLoopbackBridges covers the two bridges that already rode
// the MMDVM-Host / DMRGateway loopbacks (DMR2YSF, DMR2NXDN): every endpoint, port
// and translation target must match the retired renderer exactly, because the bus
// hub sits on the identical loopback (RFC-0003 §6.5).
func TestMigrationEquivalenceLoopbackBridges(t *testing.T) {
	t.Run("dmr2ysf", func(t *testing.T) {
		m := baseBridgeModel()
		m.DMR2YSF = DMR2YSF{Enable: true, DMRId: "3180202", DefaultTG: "9"}
		old, _ := ParseINI(strings.NewReader(retiredRenderDMR2YSF(m)))
		cfg := migratedBusConfig(t, m, "dmr2ysf")

		dmr := attByMode(cfg, "dmr")
		if dmr.BindPort != old.Get("DMR Network", "LocalPort") || dmr.PeerPort != old.Get("DMR Network", "RptPort") {
			t.Fatalf("DMR endpoints diverged: bus bind=%s peer=%s; old local=%s rpt=%s",
				dmr.BindPort, dmr.PeerPort, old.Get("DMR Network", "LocalPort"), old.Get("DMR Network", "RptPort"))
		}
		if dmr.DefaultTG != old.Get("DMR Network", "DefaultDstTG") {
			t.Fatalf("DMR default TG diverged: bus=%s old=%s", dmr.DefaultTG, old.Get("DMR Network", "DefaultDstTG"))
		}
		ysf := attByMode(cfg, "ysf")
		if ysf.BindPort != old.Get("YSF Network", "LocalPort") || ysf.PeerPort != old.Get("YSF Network", "GatewayPort") {
			t.Fatalf("YSF endpoints diverged: bus bind=%s peer=%s; old local=%s gw=%s",
				ysf.BindPort, ysf.PeerPort, old.Get("YSF Network", "LocalPort"), old.Get("YSF Network", "GatewayPort"))
		}
	})

	t.Run("dmr2nxdn", func(t *testing.T) {
		m := baseBridgeModel()
		m.DMR2NXDN = DMR2NXDN{Enable: true, DMRId: "3180202", NXDNId: "65520"}
		old, _ := ParseINI(strings.NewReader(retiredRenderDMR2NXDN(m)))
		cfg := migratedBusConfig(t, m, "dmr2nxdn")

		dmr := attByMode(cfg, "dmr")
		if dmr.BindPort != old.Get("DMR Network", "LocalPort") || dmr.PeerPort != old.Get("DMR Network", "RptPort") {
			t.Fatalf("DMR endpoints diverged")
		}
		nxdn := attByMode(cfg, "nxdn")
		if nxdn.BindPort != old.Get("NXDN Network", "LocalPort") || nxdn.PeerPort != old.Get("NXDN Network", "GatewayPort") {
			t.Fatalf("NXDN endpoints diverged: bus bind=%s peer=%s; old local=%s gw=%s",
				nxdn.BindPort, nxdn.PeerPort, old.Get("NXDN Network", "LocalPort"), old.Get("NXDN Network", "GatewayPort"))
		}
		if nxdn.DefaultID != old.Get("NXDN Network", "DefaultID") {
			t.Fatalf("NXDN default id diverged: bus=%s old=%s", nxdn.DefaultID, old.Get("NXDN Network", "DefaultID"))
		}
	})

	t.Run("ysf2nxdn", func(t *testing.T) {
		m := baseBridgeModel()
		m.YSF2NXDN = YSF2NXDN{Enable: true, NXDNId: "12345", TG: "20"}
		old, _ := ParseINI(strings.NewReader(retiredRenderYSF2NXDN(m)))
		cfg := migratedBusConfig(t, m, "ysf2nxdn")

		nxdn := attByMode(cfg, "nxdn")
		// NXDN rode the loopback in the retired renderer too: endpoints match exactly.
		if nxdn.BindPort != old.Get("NXDN Network", "LocalPort") || nxdn.PeerPort != old.Get("NXDN Network", "DstPort") {
			t.Fatalf("NXDN endpoints diverged: bus bind=%s peer=%s; old local=%s dst=%s",
				nxdn.BindPort, nxdn.PeerPort, old.Get("NXDN Network", "LocalPort"), old.Get("NXDN Network", "DstPort"))
		}
		if nxdn.ID != old.Get("NXDN Network", "Id") {
			t.Fatalf("NXDN id diverged: bus=%s old=%s", nxdn.ID, old.Get("NXDN Network", "Id"))
		}
		// Target NXDN TG (retired StartupDstId) is preserved by value (key renamed).
		if nxdn.TG != old.Get("NXDN Network", "StartupDstId") {
			t.Fatalf("NXDN target TG diverged: bus TG=%s old StartupDstId=%s", nxdn.TG, old.Get("NXDN Network", "StartupDstId"))
		}
		// The YSF side normalizes to the MMDVM-Host loopback (the bus hub design),
		// which the retired fat-style renderer did not use — assert the bus uses it.
		ysf := attByMode(cfg, "ysf")
		if ysf.BindPort != ysfMMDVMLocalPort || ysf.PeerPort != ysfMMDVMGatewayPort {
			t.Fatalf("YSF side not on the MMDVM-Host loopback: bind=%s peer=%s", ysf.BindPort, ysf.PeerPort)
		}
		if ysf.Target != "20" {
			t.Fatalf("YSF target TG lost: %q", ysf.Target)
		}
	})
}

// TestMigrationEquivalenceFatBridges covers the fat bridges (YSF2DMR, NXDN2DMR)
// that logged into their own DMR master. The bus intentionally replaces that
// master with the local DMRGateway loopback + a credentials_ref (RFC-0003 §3,
// defect 3), so the DMR upstream endpoint is NOT compared; the target talkgroup
// and modes ARE preserved, and NO master secret ever reaches the bus config.
func TestMigrationEquivalenceFatBridges(t *testing.T) {
	t.Run("ysf2dmr", func(t *testing.T) {
		m := baseBridgeModel()
		m.YSF2DMR = YSF2DMR{Enable: true, DMRId: "3180202", Master: "3102.master.brandmeister.network", Password: "topsecret", TG: "31234"}
		m.Networks = []Network{{Name: "BM_3102", Address: "3102.master.brandmeister.network", Password: "stored"}}
		oldStr := retiredRenderYSF2DMR(m)
		old, _ := ParseINI(strings.NewReader(oldStr))
		if old.Get("DMR Network", "Password") != "topsecret" {
			t.Fatal("fixture sanity: retired renderer should carry the master password")
		}
		cfg := migratedBusConfig(t, m, "ysf2dmr")

		// Modes preserved.
		if attByMode(cfg, "ysf").Mode == "" || attByMode(cfg, "dmr").Mode == "" {
			t.Fatalf("migrated bus lost a mode: %+v", cfg.Attachments)
		}
		// Target TG preserved (retired StartupDstId -> bus DMR DefaultTG).
		dmr := attByMode(cfg, "dmr")
		if dmr.DefaultTG != old.Get("DMR Network", "StartupDstId") {
			t.Fatalf("target TG diverged: bus=%s old=%s", dmr.DefaultTG, old.Get("DMR Network", "StartupDstId"))
		}
		// Credential moved to a ref, never a value.
		if dmr.CredentialsRef != "BM_3102" {
			t.Fatalf("credentials_ref = %q, want BM_3102", dmr.CredentialsRef)
		}
		// WiresX passthrough carried (retired EnableWiresX=1).
		if !attByMode(cfg, "ysf").WiresXPassthrough {
			t.Fatal("WiresX passthrough lost")
		}
		// Credential safety: no master secret in the bus config.
		full := renderMigratedBus(t, m, "ysf2dmr")
		if strings.Contains(full, "topsecret") || strings.Contains(full, "3102.master.brandmeister.network") {
			t.Fatal("bus config leaked the fat-bridge master secret/address")
		}
	})

	t.Run("nxdn2dmr", func(t *testing.T) {
		m := baseBridgeModel()
		m.NXDN2DMR = NXDN2DMR{Enable: true, DMRId: "3180202", Master: "tgif.network", Password: "nxpw", TG: "31234", NXDNTG: "20"}
		m.Networks = []Network{{Name: "TGIF", Address: "tgif.network", Password: "stored"}}
		old, _ := ParseINI(strings.NewReader(retiredRenderNXDN2DMR(m)))
		cfg := migratedBusConfig(t, m, "nxdn2dmr")

		dmr := attByMode(cfg, "dmr")
		if dmr.DefaultTG != old.Get("DMR Network", "StartupDstId") {
			t.Fatalf("target TG diverged: bus=%s old=%s", dmr.DefaultTG, old.Get("DMR Network", "StartupDstId"))
		}
		if dmr.CredentialsRef != "TGIF" {
			t.Fatalf("credentials_ref = %q, want TGIF", dmr.CredentialsRef)
		}
		nxdn := attByMode(cfg, "nxdn")
		if nxdn.TG != old.Get("NXDN Network", "TG") {
			t.Fatalf("NXDN listen TG diverged: bus=%s old=%s", nxdn.TG, old.Get("NXDN Network", "TG"))
		}
		full := renderMigratedBus(t, m, "nxdn2dmr")
		if strings.Contains(full, "nxpw") || strings.Contains(full, "tgif.network") {
			t.Fatal("bus config leaked the fat-bridge master secret/address")
		}
	})
}

func renderMigratedBus(t *testing.T, m *Model, busID string) string {
	t.Helper()
	buses, atts, _ := MigrateBridges(m)
	seed := &Model{Buses: buses, Attachments: atts, DMRNet: m.DMRNet}
	for i := range seed.Buses {
		if seed.Buses[i].ID == busID {
			return seed.renderBus(seed.Buses[i])
		}
	}
	t.Fatalf("no bus %q", busID)
	return ""
}

// TestMigrateUnmatchedMasterWarns: a fat bridge whose Master matches no network
// gets a warning and a blank credentials_ref — migration never mints a credential
// and never names the password.
func TestMigrateUnmatchedMasterWarns(t *testing.T) {
	m := baseBridgeModel()
	m.YSF2DMR = YSF2DMR{Enable: true, Master: "unknown.host", Password: "topsecret", TG: "9"}
	buses, atts, warnings := MigrateBridges(m)
	if len(buses) != 1 {
		t.Fatalf("want 1 migrated bus, got %d", len(buses))
	}
	var dmr Attachment
	for _, a := range atts {
		if a.Mode == ModeDMR {
			dmr = a
		}
	}
	if dmr.CredentialsRef != "" {
		t.Fatalf("unmatched master should leave credentials_ref blank, got %q", dmr.CredentialsRef)
	}
	if len(warnings) == 0 {
		t.Fatal("want a warning for the unmatched master")
	}
	for _, w := range warnings {
		if strings.Contains(w, "topsecret") {
			t.Fatal("warning leaked the master password")
		}
	}
}

// TestMigrateZeroSectionsSeedsNothing: an unconfigured model migrates to no buses.
func TestMigrateZeroSectionsSeedsNothing(t *testing.T) {
	buses, atts, warnings := MigrateBridges(baseBridgeModel())
	if len(buses) != 0 || len(atts) != 0 || len(warnings) != 0 {
		t.Fatalf("zero bridges should seed nothing: buses=%d atts=%d warns=%d", len(buses), len(atts), len(warnings))
	}
}

// TestApplyBridgeMigrationOneShot: the store hook persists a valid seed, is a
// no-op when buses already exist, and never persists an invalid (colliding) seed.
func TestApplyBridgeMigrationOneShot(t *testing.T) {
	s := memStore(t)
	seed := baseBridgeModel()
	seed.DMR2YSF = DMR2YSF{Enable: true, DefaultTG: "9"}
	if err := seed.Save(s, "seed"); err != nil {
		t.Fatal(err)
	}

	warnings, err := ApplyBridgeMigration(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Fatalf("clean single-bridge migration should not warn: %v", warnings)
	}
	m, err := Load(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Buses) != 1 || m.Buses[0].ID != "dmr2ysf" {
		t.Fatalf("migration did not persist the bus: %+v", m.Buses)
	}
	// Dormant section is untouched (one-way migration).
	if !m.DMR2YSF.Enable || m.DMR2YSF.DefaultTG != "9" {
		t.Fatal("migration modified the dormant bridge section")
	}

	// Second run is a no-op (buses now exist).
	w2, err := ApplyBridgeMigration(s, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(w2) == 0 || !strings.Contains(w2[0], "already exist") {
		t.Fatalf("second migration should warn and skip, got %v", w2)
	}
}

// TestMigrateOverlappingModesWarns: two dormant bridges sharing DMR seed colliding
// buses; migration surfaces it (and ApplyBridgeMigration would refuse to persist).
func TestMigrateOverlappingModesWarns(t *testing.T) {
	m := baseBridgeModel()
	m.DMR2YSF = DMR2YSF{Enable: true, DefaultTG: "9"}
	m.DMR2NXDN = DMR2NXDN{Enable: true, NXDNId: "65519"}
	_, _, warnings := MigrateBridges(m)
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "overlap") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want an overlap warning for two DMR-bearing bridges, got %v", warnings)
	}
}
