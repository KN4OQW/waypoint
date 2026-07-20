package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/KN4OQW/waypoint/internal/config"
)

// TestPeeringPeersRedacted is the UI-contract security check: the peers list a
// client fetches carries the viewable fingerprint but NEVER the certificate or
// private key (RFC-0002 write-only-secret posture, RFC-0016).
func TestPeeringPeersRedacted(t *testing.T) {
	const (
		secretCert = "-----BEGIN CERTIFICATE-----SECRETPEERCERT-----END CERTIFICATE-----"
		secretKey  = "-----BEGIN PRIVATE KEY-----SECRETNODEKEY-----END PRIVATE KEY-----"
	)
	s := busTestServer(t, &config.Model{Peers: []config.Peer{{
		ID: "garage", Name: "Garage", State: config.PeerPaired,
		Fingerprint: "AB:CD:EF:01", Certificate: secretCert, PrivateKey: secretKey,
	}}})

	rec := httptest.NewRecorder()
	s.peeringPeers(rec, httptest.NewRequest("GET", "/api/peering/peers", nil))
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, secret := range []string{secretCert, secretKey, "SECRETPEERCERT", "SECRETNODEKEY", "PRIVATE KEY"} {
		if strings.Contains(body, secret) {
			t.Fatalf("peers API leaked a secret: %q present in %s", secret, body)
		}
	}
	if !strings.Contains(body, "AB:CD:EF:01") || !strings.Contains(body, `"has_certificate":true`) {
		t.Fatalf("peers API should expose fingerprint + has_certificate, got %s", body)
	}
}

// TestBusesValidateRemoteReasons: the via-peer attach picker's greying reasons come
// straight from the server validator — the exact peering-specific strings, never
// re-derived in JS.
func TestBusesValidateRemoteReasons(t *testing.T) {
	s := busTestServer(t, &config.Model{
		Buses:       []config.Bus{{ID: "A", Name: "Bus A", Enabled: true}},
		Attachments: []config.Attachment{{BusID: "A", Mode: config.ModeDMR}},
		Peers: []config.Peer{
			{ID: "garage", Name: "Garage", State: config.PeerPaired},
			{ID: "spare", Name: "Spare", State: config.PeerPending},
		},
	})

	call := func(ra []config.RemoteAttachment) busValidateResponse {
		body, _ := json.Marshal(busValidateRequest{
			Buses:             []config.Bus{{ID: "A", Enabled: true}},
			Attachments:       []config.Attachment{{BusID: "A", Mode: config.ModeDMR}},
			RemoteAttachments: ra,
		})
		rec := httptest.NewRecorder()
		s.busesValidate(rec, httptest.NewRequest("POST", "/api/buses/validate", bytes.NewReader(body)))
		if rec.Code != 200 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var resp busValidateResponse
		json.Unmarshal(rec.Body.Bytes(), &resp)
		return resp
	}

	// paired peer contributing YSF -> valid
	if r := call([]config.RemoteAttachment{{BusID: "A", PeerID: "garage", Mode: config.ModeYSF}}); !r.OK {
		t.Fatalf("garage YSF should be valid, got %q", r.Reason)
	}
	// unpaired (pending) peer -> "not paired"
	if r := call([]config.RemoteAttachment{{BusID: "A", PeerID: "spare", Mode: config.ModeYSF}}); r.OK || !strings.Contains(r.Reason, "not paired") {
		t.Fatalf("pending peer should be refused with a not-paired reason, got ok=%v reason=%q", r.OK, r.Reason)
	}
	// union transcode set (local DMR + remote P25) -> transcode-tier reason
	if r := call([]config.RemoteAttachment{{BusID: "A", PeerID: "garage", Mode: config.ModeP25}}); r.OK || !strings.Contains(r.Reason, "transcode tier not available") {
		t.Fatalf("remote P25 should union to a transcode refusal, got ok=%v reason=%q", r.OK, r.Reason)
	}
	// unknown peer -> unknown-peer reason
	if r := call([]config.RemoteAttachment{{BusID: "A", PeerID: "ghost", Mode: config.ModeYSF}}); r.OK || !strings.Contains(r.Reason, "unknown peer") {
		t.Fatalf("unknown peer should be refused, got ok=%v reason=%q", r.OK, r.Reason)
	}
}
