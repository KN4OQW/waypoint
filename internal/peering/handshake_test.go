package peering

import (
	"testing"
	"time"
)

func mkID(t *testing.T, node string) Identity {
	t.Helper()
	cert, key, err := GenerateKeypair(node)
	if err != nil {
		t.Fatal(err)
	}
	return Identity{NodeID: node, Name: node, CertPEM: cert, KeyPEM: key}
}

var hsT0 = time.Unix(1_700_000_000, 0)

// run drives a full exchange between an initiator and a responder with optional
// fault injection, returning both handshakes for assertions. `drop` names a step
// after which delivery stops ("" = no drop). `respCode` is what the responder
// enters ("" defaults to the real code).
func runExchange(t *testing.T, drop, respCode string, confirmAt time.Time) (*Handshake, *Handshake) {
	t.Helper()
	a := mkID(t, "shack")
	b := mkID(t, "garage")

	ha, req, err := Initiate(a, hsT0)
	if err != nil {
		t.Fatal(err)
	}
	if drop == "request" {
		return ha, nil
	}
	hb, resp, err := Respond(b, req, hsT0)
	if err != nil {
		t.Fatal(err)
	}
	if drop == "response" {
		return ha, hb // initiator never learns the peer cert
	}
	if _, err := ha.Step(resp, hsT0); err != nil {
		t.Fatal(err)
	}

	// both operators act: initiator confirms (its code), responder enters the code
	code := ha.Code()
	if respCode != "" {
		code = respCode
	}
	aOut, _ := ha.Confirm("", confirmAt)
	if drop == "confirm-a" {
		// A's tag never reaches B; deliver B's to A only
		bOut, _ := hb.Confirm(code, confirmAt)
		for _, m := range bOut {
			ha.Step(m, confirmAt)
		}
		return ha, hb
	}
	bOut, _ := hb.Confirm(code, confirmAt)
	if drop == "confirm-b" {
		for _, m := range aOut {
			hb.Step(m, confirmAt)
		}
		return ha, hb // B's tag never reaches A
	}
	// deliver both confirms
	for _, m := range aOut {
		hb.Step(m, confirmAt)
	}
	for _, m := range bOut {
		ha.Step(m, confirmAt)
	}
	return ha, hb
}

func assertNoResidue(t *testing.T, h *Handshake, who string) {
	t.Helper()
	if h == nil {
		return
	}
	if _, ok := h.Result(); ok {
		t.Fatalf("%s: a failed/incomplete handshake left a pinned result (trust residue)", who)
	}
	if h.Phase() == PhasePaired {
		t.Fatalf("%s: reached Paired unexpectedly", who)
	}
}

func TestHandshakeMatrix(t *testing.T) {
	t.Run("happy path pins matching certs on both sides", func(t *testing.T) {
		ha, hb := runExchange(t, "", "", hsT0.Add(time.Second))
		ra, oka := ha.Result()
		rb, okb := hb.Result()
		if !oka || !okb {
			t.Fatalf("both sides should be paired (a=%v b=%v)", oka, okb)
		}
		// each pinned the OTHER's cert, and both agree on fingerprints
		if ra.NodeID != "garage" || rb.NodeID != "shack" {
			t.Fatalf("wrong peers pinned: a->%s b->%s", ra.NodeID, rb.NodeID)
		}
		if ra.CertPEM == "" || rb.CertPEM == "" || ra.Fingerprint == "" {
			t.Fatal("paired result missing cert/fingerprint")
		}
	})

	t.Run("wrong code fails with no residue", func(t *testing.T) {
		ha, hb := runExchange(t, "", "000000", hsT0.Add(time.Second))
		// (the generated code is 6 random digits; "000000" almost surely differs)
		if ha.Code() == "000000" {
			t.Skip("astronomically unlucky code collision")
		}
		assertNoResidue(t, ha, "initiator")
		assertNoResidue(t, hb, "responder")
		if ha.Phase() != PhaseFailed || hb.Phase() != PhaseFailed {
			t.Fatalf("wrong code should fail both: a=%v b=%v", ha.Phase(), hb.Phase())
		}
	})

	t.Run("expired code fails with no residue", func(t *testing.T) {
		ha, hb := runExchange(t, "", "", hsT0.Add(CodeExpiry+time.Second))
		assertNoResidue(t, ha, "initiator")
		assertNoResidue(t, hb, "responder")
	})

	// A dropped message leaves the STUCK side (the one that never received a valid
	// peer tag) with no residue; a side that DID verify a valid tag legitimately
	// pairs (a lost confirm cannot un-pin a correct verification). request/response
	// drops strand both sides.
	drops := []struct {
		step  string
		stuck []string // sides that must have no residue
	}{
		{"request", []string{"initiator", "responder"}},
		{"response", []string{"initiator", "responder"}},
		{"confirm-a", []string{"responder"}}, // A's tag lost -> B stuck; A got B's tag
		{"confirm-b", []string{"initiator"}}, // B's tag lost -> A stuck; B got A's tag
	}
	for _, d := range drops {
		t.Run("drop at "+d.step+" leaves the stuck side no residue", func(t *testing.T) {
			ha, hb := runExchange(t, d.step, "", hsT0.Add(time.Second))
			if ha != nil {
				ha.Tick(hsT0.Add(CodeExpiry + time.Second))
			}
			if hb != nil {
				hb.Tick(hsT0.Add(CodeExpiry + time.Second))
			}
			for _, side := range d.stuck {
				if side == "initiator" {
					assertNoResidue(t, ha, "initiator")
				} else {
					assertNoResidue(t, hb, "responder")
				}
			}
		})
	}

	t.Run("cancel leaves no residue", func(t *testing.T) {
		a := mkID(t, "shack")
		ha, _, _ := Initiate(a, hsT0)
		ha.Cancel()
		assertNoResidue(t, ha, "initiator")
		if ha.Phase() != PhaseFailed {
			t.Fatal("cancel should fail the handshake")
		}
	})

	t.Run("a MITM swapping the responder cert is rejected", func(t *testing.T) {
		a := mkID(t, "shack")
		b := mkID(t, "garage")
		evil := mkID(t, "garage") // same node id, different cert (a substitution)

		ha, req, _ := Initiate(a, hsT0)
		hb, _, _ := Respond(b, req, hsT0)
		// A is told garage's cert is the EVIL one (MITM substitution on the wire)
		ha.Step(Message{Kind: KindResponse, SID: req.SID, NodeID: "garage", NodeName: "garage", CertPEM: evil.CertPEM}, hsT0)

		code := ha.Code()
		aOut, _ := ha.Confirm("", hsT0.Add(time.Second))
		bOut, _ := hb.Confirm(code, hsT0.Add(time.Second))
		for _, m := range aOut {
			hb.Step(m, hsT0.Add(time.Second))
		}
		for _, m := range bOut {
			ha.Step(m, hsT0.Add(time.Second))
		}
		// transcripts differ (A hashed evil cert, B hashed its real cert) -> both fail
		assertNoResidue(t, ha, "initiator")
		assertNoResidue(t, hb, "responder")
	})
}

// TestHandshakeSIDIsolation: a message for a different session id is ignored, so
// simultaneous mutual initiations (two sessions) never cross-talk.
func TestHandshakeSIDIsolation(t *testing.T) {
	a := mkID(t, "shack")
	ha, _, _ := Initiate(a, hsT0)
	out, err := ha.Step(Message{Kind: KindResponse, SID: "other-session", NodeID: "x", CertPEM: a.CertPEM}, hsT0)
	if err != nil || out != nil {
		t.Fatal("a message for another session must be ignored")
	}
	if ha.Phase() != PhaseAwaitResponse {
		t.Fatal("cross-session message must not advance the handshake")
	}
}
