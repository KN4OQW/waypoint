package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

var peeringPaths = Paths{BusConfigDir: "/etc/wp", PeeringDir: "/etc/wp/peering"}

// A shack-owned Bus A: local DMR + garage's YSF over peering (RFC-0016).
func peeringModel() *Model {
	return &Model{
		Buses:       []Bus{{ID: "A", Name: "Bus A", Enabled: true}},
		Attachments: []Attachment{{BusID: "A", Mode: ModeDMR, Slot: "2", DefaultTG: "91"}},
		Peers: []Peer{{
			ID: "garage", Name: "Garage", Host: "10.0.0.20", Port: "42500",
			State: PeerPaired, Fingerprint: "AB:CD:EF:01",
			Certificate: "-----BEGIN CERTIFICATE-----MIISECRETCERT-----END CERTIFICATE-----",
			PrivateKey:  "-----BEGIN PRIVATE KEY-----MIISECRETKEY-----END PRIVATE KEY-----",
		}},
		RemoteAttachments: []RemoteAttachment{{BusID: "A", PeerID: "garage", Mode: ModeYSF, Target: "ROOM"}},
	}
}

func ownerPath() string { return filepath.Join(peeringPaths.BusConfigDir, "waypoint-bus-A.json") }
func memberPath() string {
	return filepath.Join(peeringPaths.BusConfigDir, "waypoint-bus-A-member-garage.json")
}

func renderByPath(t *testing.T, m *Model, path string) (string, bool) {
	t.Helper()
	for _, tg := range m.RenderTargets(peeringPaths) {
		if tg.Path == path {
			return tg.Render(m), true
		}
	}
	return "", false
}

func TestPeeringOwnerAndMemberRender(t *testing.T) {
	m := peeringModel()

	owner, ok := renderByPath(t, m, ownerPath())
	if !ok {
		t.Fatal("no owner bus target")
	}
	var bc BusConfig
	if err := json.Unmarshal([]byte(owner), &bc); err != nil {
		t.Fatalf("owner config is not valid JSON: %v\n%s", err, owner)
	}
	if bc.Peering == nil {
		t.Fatal("owner bus config missing the peering block")
	}
	if bc.Peering.DeadlineMs != 60 || bc.Peering.JitterBufferMs != 40 {
		t.Fatalf("deadline/jitter should default to 60/40, got %d/%d", bc.Peering.DeadlineMs, bc.Peering.JitterBufferMs)
	}
	if bc.Peering.KeyPath != "/etc/wp/peering/node.key" {
		t.Fatalf("owner key path = %q", bc.Peering.KeyPath)
	}
	if len(bc.Peering.Members) != 1 {
		t.Fatalf("want 1 member row, got %d", len(bc.Peering.Members))
	}
	mem := bc.Peering.Members[0]
	if mem.PeerID != "garage" || mem.Mode != ModeYSF || mem.Endpoint != "10.0.0.20:42500" || mem.Target != "ROOM" {
		t.Fatalf("member row wrong: %+v", mem)
	}
	if mem.CertPath != "/etc/wp/peering/peer-garage.crt" {
		t.Fatalf("member cert path = %q", mem.CertPath)
	}

	member, ok := renderByPath(t, m, memberPath())
	if !ok {
		t.Fatal("no member target")
	}
	var mc MemberBusConfig
	if err := json.Unmarshal([]byte(member), &mc); err != nil {
		t.Fatalf("member config is not valid JSON: %v\n%s", err, member)
	}
	if mc.Role != "member" || mc.BusID != "A" {
		t.Fatalf("member role/bus wrong: %+v", mc)
	}
	if mc.Owner.Listen != "0.0.0.0:42500" || mc.Owner.CertPath != "/etc/wp/peering/owner-A.crt" {
		t.Fatalf("member owner block wrong: %+v", mc.Owner)
	}
	if mc.KeyPath != "/etc/wp/peering/node.key" {
		t.Fatalf("member key path = %q", mc.KeyPath)
	}
	if len(mc.Attachments) != 1 || mc.Attachments[0].Mode != ModeYSF {
		t.Fatalf("member attachments wrong: %+v", mc.Attachments)
	}
	if lb := mc.Attachments[0].Loopback; lb.Bind != 4200 || lb.Peer != 3200 {
		t.Fatalf("member YSF loopback wrong: %+v", lb)
	}
}

func TestPeeringNoPEMOrKeyInRender(t *testing.T) {
	m := peeringModel()
	var all strings.Builder
	for _, tg := range m.RenderTargets(peeringPaths) {
		all.WriteString(tg.Render(m))
	}
	out := all.String()
	for _, forbidden := range []string{"BEGIN CERTIFICATE", "PRIVATE KEY", "MIISECRETCERT", "MIISECRETKEY"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("rendered output leaked key material: %q present", forbidden)
		}
	}
	// The fingerprint (public, viewable) and the cert PATH are expected.
	if !strings.Contains(out, "AB:CD:EF:01") || !strings.Contains(out, "peer-garage.crt") {
		t.Fatalf("expected the fingerprint + cert path in the render, got:\n%s", out)
	}
}

func TestPeeringPureRenderDeterministic(t *testing.T) {
	m := peeringModel()
	a := m.RenderTargets(peeringPaths)
	b := m.RenderTargets(peeringPaths)
	if len(a) != len(b) {
		t.Fatalf("target count not stable: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Unit != b[i].Unit {
			t.Fatalf("target %d differs: %+v vs %+v", i, a[i], b[i])
		}
		if a[i].Render(m) != b[i].Render(m) {
			t.Fatalf("target %d render not byte-identical", i)
		}
	}
}

func TestPeeringUnrelatedEditInvariance(t *testing.T) {
	m := peeringModel()
	before, _ := renderByPath(t, m, ownerPath())
	// An unrelated edit (a network) must not change the bus/peering render.
	m.Networks = append(m.Networks, Network{Name: "BM", Address: "x"})
	after, _ := renderByPath(t, m, ownerPath())
	if before != after {
		t.Fatalf("unrelated edit changed the peering render:\n--before--\n%s\n--after--\n%s", before, after)
	}
}

func TestPeeringOneMemberTargetPerMembership(t *testing.T) {
	m := peeringModel()
	// add a second paired peer contributing NXDN to the same bus
	m.Peers = append(m.Peers, Peer{ID: "spare", Name: "Spare", Host: "10.0.0.30", Port: "42500", State: PeerPaired})
	m.RemoteAttachments = append(m.RemoteAttachments, RemoteAttachment{BusID: "A", PeerID: "spare", Mode: ModeNXDN})
	// (bus A now spans 3 nodes — over the validator cap — but the RENDERER is not the
	// validator; it renders whatever is stored. The cap is enforced at write time.)

	owners, members := 0, 0
	for _, tg := range m.RenderTargets(peeringPaths) {
		switch {
		case tg.Path == ownerPath():
			owners++
		case strings.Contains(tg.Path, "-member-"):
			members++
			if tg.Unit != "" {
				t.Fatalf("member target must have empty Unit (runs on the peer), got %q", tg.Unit)
			}
		}
	}
	if owners != 1 {
		t.Fatalf("want exactly 1 owner target for bus A, got %d", owners)
	}
	if members != 2 {
		t.Fatalf("want one member target per membership (2), got %d", members)
	}
}

func TestPeeringRevokedPeerRendersNothing(t *testing.T) {
	m := peeringModel()
	// baseline: peering present + a member target
	if _, ok := renderByPath(t, m, memberPath()); !ok {
		t.Fatal("expected a member target while paired")
	}

	// revoke the peer — the model keeps every row (delete nothing)
	m.Peers[0].State = PeerRevoked
	if len(m.RemoteAttachments) != 1 || len(m.Peers) != 1 {
		t.Fatal("revocation must not delete any rows")
	}

	owner, _ := renderByPath(t, m, ownerPath())
	var bc BusConfig
	json.Unmarshal([]byte(owner), &bc)
	if bc.Peering != nil {
		t.Fatalf("a revoked peer's remote attachment must render no peering block, got %+v", bc.Peering)
	}
	if _, ok := renderByPath(t, m, memberPath()); ok {
		t.Fatal("a revoked peer must contribute no member target")
	}

	// and the owner bus config is now byte-identical to a purely-local render
	local := &Model{Buses: m.Buses, Attachments: m.Attachments}
	localOut, _ := renderByPath(t, local, ownerPath())
	if owner != localOut {
		t.Fatalf("a bus with only a revoked peer should render like a local bus:\n--peered--\n%s\n--local--\n%s", owner, localOut)
	}
}

func TestPeeringDisableReEnableByteIdentity(t *testing.T) {
	m := peeringModel()
	before, _ := renderByPath(t, m, ownerPath())

	m.Buses[0].Enabled = false
	if _, ok := renderByPath(t, m, ownerPath()); ok {
		t.Fatal("a disabled bus should contribute no target")
	}

	m.Buses[0].Enabled = true
	after, _ := renderByPath(t, m, ownerPath())
	if before != after {
		t.Fatalf("disable/re-enable was not byte-identical:\n--before--\n%s\n--after--\n%s", before, after)
	}
}

// Round-trip: the owner config with peering rows parses back through the Phase-2
// reader's shape (BusConfig) with no semantic loss.
func TestPeeringOwnerConfigRoundTrip(t *testing.T) {
	m := peeringModel()
	owner, _ := renderByPath(t, m, ownerPath())
	var bc BusConfig
	if err := json.Unmarshal([]byte(owner), &bc); err != nil {
		t.Fatal(err)
	}
	re, err := json.MarshalIndent(bc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(re)) != strings.TrimSpace(owner) {
		t.Fatalf("owner config did not round-trip:\n--in--\n%s\n--out--\n%s", owner, re)
	}
}
