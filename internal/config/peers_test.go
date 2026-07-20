package config

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/store"
)

// paired/pending/revoked peer fixtures.
func peer(id string, st PeerState) Peer {
	return Peer{ID: id, Name: id, Host: "10.0.0." + id, Port: "9000", State: st}
}

// --- Extended validity matrix (RFC-0016), the Phase-1 table + remote rows ------

func TestValidateRemoteAttachmentsMatrix(t *testing.T) {
	buses := []Bus{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}
	peers := []Peer{peer("p1", PeerPaired), peer("p2", PeerPaired), peer("p3", PeerPaired)}

	cases := []struct {
		name      string
		local     []Attachment
		remote    []RemoteAttachment
		wantOK    bool
		reasonHas string
	}{
		{
			name:   "local DMR + remote YSF on a paired peer is valid",
			local:  []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote: []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeYSF}},
			wantOK: true,
		},
		{
			name:   "local DMR + remote DMR (a different node) is valid — a two-node DMR link",
			local:  []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote: []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeDMR}},
			wantOK: true,
		},
		{
			name:      "local DMR + remote P25 unions to a transcode set — refused",
			local:     []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeP25}},
			reasonHas: "transcode tier not available",
		},
		{
			name:      "local DMR + remote D-Star has no converter — refused",
			local:     []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeDStar}},
			reasonHas: "no converter for D-Star<->DMR",
		},
		{
			name:      "a peer contributing the same mode twice to a bus is refused",
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeDMR}, {BusID: "a", PeerID: "p1", Mode: ModeDMR}},
			reasonHas: "already contributes mode DMR to bus",
		},
		{
			name:      "a peer's mode on two buses is refused (one loopback per node)",
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeDMR}, {BusID: "b", PeerID: "p1", Mode: ModeDMR}},
			reasonHas: "attached to more than one bus",
		},
		{
			name:      "unknown bus is refused",
			remote:    []RemoteAttachment{{BusID: "zzz", PeerID: "p1", Mode: ModeDMR}},
			reasonHas: `unknown bus "zzz"`,
		},
		{
			name:      "unknown peer is refused",
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "ghost", Mode: ModeDMR}},
			reasonHas: `unknown peer "ghost"`,
		},
		{
			name:      "three participating nodes on one bus exceeds the v1 cap",
			local:     []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote:    []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeYSF}, {BusID: "a", PeerID: "p2", Mode: ModeNXDN}},
			reasonHas: "spans 3 nodes",
		},
		{
			name:   "two nodes (local + one peer) is within the cap",
			local:  []Attachment{{BusID: "a", Mode: ModeDMR}},
			remote: []RemoteAttachment{{BusID: "a", PeerID: "p1", Mode: ModeYSF}},
			wantOK: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateRemoteAttachments(buses, c.local, c.remote, peers)
			if c.wantOK {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected refusal containing %q, got nil", c.reasonHas)
			}
			if !strings.Contains(err.Error(), c.reasonHas) {
				t.Fatalf("reason %q missing %q", err.Error(), c.reasonHas)
			}
		})
	}
}

func TestValidatePeers(t *testing.T) {
	if err := ValidatePeers([]Peer{peer("p1", PeerPaired), peer("p1", PeerPending)}); err == nil || !strings.Contains(err.Error(), "duplicate peer id") {
		t.Fatalf("duplicate id should be refused, got %v", err)
	}
	if err := ValidatePeers([]Peer{{ID: "p1", State: "bogus"}}); err == nil || !strings.Contains(err.Error(), "invalid state") {
		t.Fatalf("bad state should be refused, got %v", err)
	}
	if err := ValidatePeers([]Peer{peer("p1", PeerRevoked)}); err != nil {
		t.Fatalf("a revoked peer is a valid (retained) row: %v", err)
	}
}

// --- Secret shape: no private key or full cert ever leaves via a read API ------

func TestPeerSecretShape(t *testing.T) {
	const (
		secretCert = "-----BEGIN CERTIFICATE-----SECRETCERTBYTES-----END CERTIFICATE-----"
		secretKey  = "-----BEGIN PRIVATE KEY-----SECRETKEYBYTES-----END PRIVATE KEY-----"
	)
	m := &Model{Peers: []Peer{{
		ID: "p1", Name: "shack", State: PeerPaired,
		Fingerprint: "AB:CD:EF:12", Certificate: secretCert, PrivateKey: secretKey,
	}}}
	raw, err := json.Marshal(m.View(""))
	if err != nil {
		t.Fatal(err)
	}
	js := string(raw)
	for _, secret := range []string{secretCert, secretKey, "SECRETCERTBYTES", "SECRETKEYBYTES", "PRIVATE KEY"} {
		if strings.Contains(js, secret) {
			t.Fatalf("read API leaked a peer secret: %q appears in the view", secret)
		}
	}
	// The fingerprint and has_* flags MUST be present.
	if !strings.Contains(js, "AB:CD:EF:12") || !strings.Contains(js, `"has_certificate":true`) || !strings.Contains(js, `"has_key":true`) {
		t.Fatalf("view should expose fingerprint + has_* flags, got %s", js)
	}
}

// --- Store round-trip + secret preservation ------------------------------------

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestSetPeersRoundTripAndSecretPreserve(t *testing.T) {
	st := newStore(t)
	// initial write with secrets
	body := `[{"id":"p1","name":"shack","host":"10.0.0.9","port":"9000","state":"paired","fingerprint":"FP1","certificate":"CERT1","private_key":"KEY1"}]`
	if err := SetPeers(st, []byte(body), "test"); err != nil {
		t.Fatal(err)
	}
	var got []Peer
	if _, err := st.GetInto("peers", &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Certificate != "CERT1" || got[0].PrivateKey != "KEY1" || got[0].Fingerprint != "FP1" {
		t.Fatalf("round-trip lost fields: %+v", got)
	}

	// a blank-secret rewrite (e.g. the UI editing the name) preserves the stored secrets
	if err := SetPeers(st, []byte(`[{"id":"p1","name":"garage","state":"paired","fingerprint":"FP1"}]`), "test"); err != nil {
		t.Fatal(err)
	}
	got = nil
	st.GetInto("peers", &got)
	if got[0].Name != "garage" {
		t.Fatalf("name edit lost: %+v", got[0])
	}
	if got[0].Certificate != "CERT1" || got[0].PrivateKey != "KEY1" {
		t.Fatalf("blank-secret write must preserve stored cert/key, got %+v", got[0])
	}
}

func TestRePairMintsFreshMaterial(t *testing.T) {
	st := newStore(t)
	// paired with material, then revoked (material retained while revoked)
	SetPeers(st, []byte(`[{"id":"p1","name":"n","state":"paired","certificate":"OLDCERT","private_key":"OLDKEY"}]`), "test")
	if err := SetPeers(st, []byte(`[{"id":"p1","name":"n","state":"revoked"}]`), "test"); err != nil {
		t.Fatal(err)
	}
	var got []Peer
	st.GetInto("peers", &got)
	if got[0].Certificate != "OLDCERT" {
		t.Fatalf("revoke should retain the row's material, got %+v", got[0])
	}
	// re-pair with BLANK secrets: the old material must NOT be carried forward
	if err := SetPeers(st, []byte(`[{"id":"p1","name":"n","state":"paired"}]`), "test"); err != nil {
		t.Fatal(err)
	}
	got = nil
	st.GetInto("peers", &got)
	if got[0].Certificate == "OLDCERT" || got[0].PrivateKey == "OLDKEY" {
		t.Fatalf("re-pairing a revoked peer must mint fresh material, not reuse it; got %+v", got[0])
	}
	if got[0].State != PeerPaired {
		t.Fatalf("state should be paired after re-pair, got %s", got[0].State)
	}
}

// --- SetRemoteAttachments add-time pairing gate --------------------------------

func seedBusAndPeer(t *testing.T, st *store.Store, peerState PeerState) {
	t.Helper()
	if err := st.Set("buses", []Bus{{ID: "a", Name: "A", Enabled: true}}, "test"); err != nil {
		t.Fatal(err)
	}
	if err := SetPeers(st, []byte(`[{"id":"p1","name":"peer","state":"`+string(peerState)+`"}]`), "test"); err != nil {
		t.Fatal(err)
	}
}

func TestSetRemoteAttachmentsRequiresPairedForNew(t *testing.T) {
	st := newStore(t)
	seedBusAndPeer(t, st, PeerPending)
	err := SetRemoteAttachments(st, []byte(`[{"bus_id":"a","peer_id":"p1","mode":"ysf"}]`), "test")
	if err == nil || !strings.Contains(err.Error(), "not paired") {
		t.Fatalf("a new remote attachment on a pending peer must be refused, got %v", err)
	}

	// pair the peer, now it is accepted
	SetPeers(st, []byte(`[{"id":"p1","name":"peer","state":"paired"}]`), "test")
	if err := SetRemoteAttachments(st, []byte(`[{"bus_id":"a","peer_id":"p1","mode":"ysf","target":"ROOM"}]`), "test"); err != nil {
		t.Fatalf("a paired peer's remote attachment should be accepted: %v", err)
	}
}

// --- Revocation preserves rows + dormants dependents, deletes nothing ----------

func TestRevocationPreservesRowsAndDormantsRemoteAttachments(t *testing.T) {
	st := newStore(t)
	seedBusAndPeer(t, st, PeerPaired)
	if err := SetRemoteAttachments(st, []byte(`[{"bus_id":"a","peer_id":"p1","mode":"ysf"}]`), "test"); err != nil {
		t.Fatal(err)
	}

	// revoke the peer — must succeed, retaining both the peer row and its attachment
	if err := SetPeers(st, []byte(`[{"id":"p1","name":"peer","state":"revoked"}]`), "test"); err != nil {
		t.Fatalf("revocation should succeed even with a dependent remote attachment: %v", err)
	}

	var peers []Peer
	st.GetInto("peers", &peers)
	if len(peers) != 1 || peers[0].State != PeerRevoked {
		t.Fatalf("revoked peer row must be retained as revoked, got %+v", peers)
	}
	var remote []RemoteAttachment
	st.GetInto("remote_attachments", &remote)
	if len(remote) != 1 {
		t.Fatalf("revocation must delete NO remote attachment, got %d", len(remote))
	}

	// the dependent is reported dormant with a clear reason
	states := RemoteAttachmentStates(peers, remote)
	if len(states) != 1 || states[0].Active || !strings.Contains(states[0].Reason, "not paired") {
		t.Fatalf("dependent should be dormant with a reason, got %+v", states)
	}

	// a still-stored (grandfathered) remote attachment survives a re-save even though
	// its peer is now revoked — revocation didn't make the section un-writable
	if err := SetRemoteAttachments(st, []byte(`[{"bus_id":"a","peer_id":"p1","mode":"ysf"}]`), "test"); err != nil {
		t.Fatalf("re-saving an existing revoked-peer attachment must be allowed: %v", err)
	}
}
