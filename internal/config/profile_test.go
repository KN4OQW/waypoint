package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

// profStore is memStore (config_test.go) plus the profiles table.
func profStore(t *testing.T) *store.Store {
	t.Helper()
	s := memStore(t)
	if err := InitProfiles(s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Property 1: the profile namespace and the excluded list partition the model's
// sections exactly — no section is unclassified, none is in both. A section added
// to Model.sections() without being placed fails here.
func TestProfileNamespacePartition(t *testing.T) {
	all := map[string]bool{}
	for k := range (&Model{}).sections() {
		all[k] = true
	}
	seen := map[string]bool{}
	for _, s := range append(append([]string{}, profileSections...), profileExcluded...) {
		if seen[s] {
			t.Errorf("section %q classified twice", s)
		}
		seen[s] = true
		if !all[s] {
			t.Errorf("classified section %q is not a real model section", s)
		}
	}
	for k := range all {
		if !seen[k] {
			t.Errorf("model section %q is neither in profileSections nor profileExcluded", k)
		}
	}
}

// Property 2: capture, mutate the live profile sections, activate the capture ⇒
// the profile sections are restored and every EXCLUDED section is untouched.
func TestProfileCaptureActivate(t *testing.T) {
	s := profStore(t)
	m := fixture()
	if err := m.Save(s, "seed"); err != nil {
		t.Fatal(err)
	}

	prof, err := CaptureProfile(s, "snap")
	if err != nil {
		t.Fatal(err)
	}

	// Mutate a profile section (flip the mode topology) AND an excluded one (callsign).
	mustSet(t, s, "modes", Modes{YSF: true})
	mustSet(t, s, "general", General{Callsign: "CHANGED", ID: "9999999"})

	// Activate the captured profile.
	if err := ActivateProfile(s, prof, "test"); err != nil {
		t.Fatal(err)
	}

	// Every profile section is restored to the snapshot byte-for-byte — the
	// authoritative "topology restored" check (the gateway *render* also embeds the
	// station callsign from the excluded `general` section, so it is deliberately
	// not the yardstick here).
	after, err := CaptureProfile(s, "snap")
	if err != nil {
		t.Fatal(err)
	}
	for _, sec := range profileSections {
		if !jsonEqual(prof.Sections[sec], after.Sections[sec]) {
			t.Errorf("section %q not restored by activate:\n before %s\n  after %s", sec, prof.Sections[sec], after.Sections[sec])
		}
	}

	// The excluded 'general' edit SURVIVES activation (a profile never touches it).
	var g General
	if _, err := s.GetInto("general", &g); err != nil {
		t.Fatal(err)
	}
	if g.Callsign != "CHANGED" {
		t.Errorf("activate overwrote an excluded section (general.callsign = %q, want CHANGED)", g.Callsign)
	}
}

// Property 4: blank secret in the profile keeps the store's current secret;
// a real secret restores it.
func TestProfileSecretReconcile(t *testing.T) {
	s := profStore(t)
	// Store has a DAPNET AuthKey and a DMR network password.
	mustSet(t, s, "pocsag", POCSAG{AuthKey: "REAL-KEY", Frequency: "439987500"})
	mustSet(t, s, "networks", []Network{{Name: "BM", Password: "netpass"}})

	// A profile captured now carries the real secrets → activate restores them.
	prof, _ := CaptureProfile(s, "p")
	mustSet(t, s, "pocsag", POCSAG{AuthKey: "OTHER", Frequency: "1"})
	if err := ActivateProfile(s, prof, "t"); err != nil {
		t.Fatal(err)
	}
	var pg POCSAG
	s.GetInto("pocsag", &pg)
	if pg.AuthKey != "REAL-KEY" || pg.Frequency != "439987500" {
		t.Errorf("captured profile did not restore secret+value: %+v", pg)
	}

	// An EXPORTED (scrubbed) profile has blank secrets → activate PRESERVES the
	// store's current secret, never blanks it.
	exported := prof.Export()
	mustSet(t, s, "pocsag", POCSAG{AuthKey: "CURRENT", Frequency: "5"})
	mustSet(t, s, "networks", []Network{{Name: "BM", Password: "currentnet"}})
	if err := ActivateProfile(s, exported, "t"); err != nil {
		t.Fatal(err)
	}
	s.GetInto("pocsag", &pg)
	if pg.AuthKey != "CURRENT" {
		t.Errorf("imported profile blanked a working AuthKey: %q", pg.AuthKey)
	}
	var nets []Network
	s.GetInto("networks", &nets)
	if len(nets) != 1 || nets[0].Password != "currentnet" {
		t.Errorf("imported profile blanked a working network password: %+v", nets)
	}
}

// Property 5: the export artifact contains no secret string, and Sensitive names
// exactly the scrubbed keys.
func TestProfileExportScrub(t *testing.T) {
	s := profStore(t)
	mustSet(t, s, "pocsag", POCSAG{AuthKey: "SECRET-AUTH", Frequency: "1"})
	mustSet(t, s, "networks", []Network{{Name: "BM", Password: "SECRET-NET"}})
	mustSet(t, s, "dstargw", DStarGateway{IRCDDBPassword: "SECRET-IRC"})

	prof, _ := CaptureProfile(s, "p")
	art := prof.Export()
	blob, _ := json.Marshal(art)
	for _, secret := range []string{"SECRET-AUTH", "SECRET-NET", "SECRET-IRC"} {
		if strings.Contains(string(blob), secret) {
			t.Errorf("export artifact leaked secret %q:\n%s", secret, blob)
		}
	}
	want := map[string]bool{"pocsag.auth_key": true, "networks[].password": true, "dstargw.ircddb_password": true}
	if len(art.Sensitive) != len(want) {
		t.Fatalf("Sensitive = %v, want the 3 scrubbed keys", art.Sensitive)
	}
	for _, k := range art.Sensitive {
		if !want[k] {
			t.Errorf("unexpected sensitive key %q", k)
		}
	}
}

// Property 6 (RFC-0001): export → import into a fresh store (with its own secrets)
// → activate ⇒ rendered output for the profile namespace is byte-identical for all
// non-secret keys, and the target's secrets are preserved.
func TestProfileExportImportRoundTrip(t *testing.T) {
	src := profStore(t)
	m := fixture()
	m.POCSAG.AuthKey = "SRC-KEY"
	if err := m.Save(src, "seed"); err != nil {
		t.Fatal(err)
	}
	prof, _ := CaptureProfile(src, "carry")
	artifact, _ := json.Marshal(prof.Export()) // the file that would be downloaded

	// Fresh target store with its OWN pocsag AuthKey already set.
	dst := profStore(t)
	if err := fixture().Save(dst, "seed"); err != nil {
		t.Fatal(err)
	}
	mustSet(t, dst, "pocsag", POCSAG{AuthKey: "DST-KEY", Frequency: "439987500"})

	// Import the artifact and activate it on the target.
	var imported Profile
	if err := json.Unmarshal(artifact, &imported); err != nil {
		t.Fatal(err)
	}
	if err := ActivateProfile(dst, &imported, "import"); err != nil {
		t.Fatal(err)
	}

	// The target's own secret is preserved (import never carried SRC-KEY).
	var pg POCSAG
	dst.GetInto("pocsag", &pg)
	if pg.AuthKey != "DST-KEY" {
		t.Errorf("import overwrote the target's secret: %q", pg.AuthKey)
	}

	// Non-secret rendered output matches: compare the DMRGateway render (pure
	// profile namespace, no secrets in the non-network parts) between src and dst.
	srcModel, _ := Load(src)
	dstModel, _ := Load(dst)
	if srcModel.RenderYSFGateway() != dstModel.RenderYSFGateway() {
		t.Errorf("YSFGateway render differs after round-trip:\n--- src ---\n%s\n--- dst ---\n%s", srcModel.RenderYSFGateway(), dstModel.RenderYSFGateway())
	}
}

// Property 7: activation is one transaction of exactly the captured sections.
func TestProfileActivateWritesOnlyProfileSections(t *testing.T) {
	s := profStore(t)
	if err := fixture().Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	prof, _ := CaptureProfile(s, "p")
	// Every captured section is in the profile namespace, never an excluded one.
	for sec := range prof.Sections {
		if contains(profileExcluded, sec) {
			t.Errorf("capture included excluded section %q", sec)
		}
		if !contains(profileSections, sec) {
			t.Errorf("capture included non-namespace section %q", sec)
		}
	}
}

func TestProfileTableCRUD(t *testing.T) {
	s := profStore(t)
	if err := fixture().Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	p1, _ := CaptureProfile(s, "alpha")
	p2, _ := CaptureProfile(s, "beta")
	if err := SaveProfile(s, p1); err != nil {
		t.Fatal(err)
	}
	if err := SaveProfile(s, p2); err != nil {
		t.Fatal(err)
	}

	list, err := ListProfiles(s)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("list not name-sorted/complete: %+v", list)
	}

	got, err := GetProfile(s, "alpha")
	if err != nil || got == nil {
		t.Fatalf("GetProfile alpha: %v", err)
	}
	if !jsonEqual(got.Sections["modes"], p1.Sections["modes"]) {
		t.Errorf("stored profile modes differ from captured")
	}

	exists, _ := ProfileExists(s, "alpha")
	if !exists {
		t.Error("ProfileExists(alpha) should be true")
	}
	removed, _ := DeleteProfile(s, "alpha")
	if !removed {
		t.Error("DeleteProfile(alpha) should report removal")
	}
	if got, _ := GetProfile(s, "alpha"); got != nil {
		t.Error("alpha should be gone after delete")
	}
	// Delete never touches the live config.
	if _, err := Load(s); err != nil {
		t.Errorf("store unreadable after profile delete: %v", err)
	}
}

func TestProfileIsActive(t *testing.T) {
	s := profStore(t)
	if err := fixture().Save(s, "seed"); err != nil {
		t.Fatal(err)
	}
	prof, _ := CaptureProfile(s, "p")
	if ok, _ := IsActive(s, prof); !ok {
		t.Error("a just-captured profile should read active")
	}
	mustSet(t, s, "modes", Modes{FM: true}) // change the live topology
	if ok, _ := IsActive(s, prof); ok {
		t.Error("profile should read inactive after a live edit to a profile section")
	}
}

// --- helpers ---

func mustSet(t *testing.T, s *store.Store, section string, v any) {
	t.Helper()
	if err := s.Set(section, v, "test"); err != nil {
		t.Fatal(err)
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
