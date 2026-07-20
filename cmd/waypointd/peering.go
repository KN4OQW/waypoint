package main

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/peering"
)

// peering.go wires the RFC-0016 pairing manager into waypointd and exposes the
// pairing API. Every route below registers on the same mux the auth Gate wraps, so
// each is session-walled identically to every other config route (RFC-0002).

// initPeering loads (or mints) this node's peering keypair from the peering dir,
// starts the bootstrap pairing listener + mDNS advertisement, and stores the
// Manager on the server. It is best-effort: a failure logs and leaves s.peering
// nil (the API then reports peering unavailable), never crashing the daemon.
func (s *server) initPeering(ctx context.Context, peeringDir, bootstrapAddr string) {
	name := s.stationCallsign()
	self, err := loadOrMintKeypair(peeringDir, name)
	if err != nil {
		log.Printf("peering: keypair: %v (pairing disabled)", err)
		return
	}
	nodeID, err := loadOrMintNodeID(peeringDir)
	if err != nil {
		log.Printf("peering: node id: %v (pairing disabled)", err)
		return
	}
	ln, err := net.Listen("tcp", bootstrapAddr)
	if err != nil {
		log.Printf("peering: bootstrap listen %s: %v (pairing disabled)", bootstrapAddr, err)
		return
	}
	m := peering.NewManager(s.store, self, nodeID, name, ln.Addr().String(), peering.NewMDNS())
	go m.Serve(ctx, ln)
	s.peering = m
	// Advertise the peering endpoint (instance = node name). Best-effort.
	if stop, err := peering.NewMDNS().Advertise(name, portInt(ln.Addr().String()), []string{"node=" + nodeID}); err == nil {
		go func() { <-ctx.Done(); stop() }()
	}
	log.Printf("peering: node %q (id %s) listening for pairing on %s", name, nodeID, ln.Addr().String())
}

func (s *server) stationCallsign() string {
	m, err := config.Load(s.store)
	if err != nil || m.General.Callsign == "" {
		return "waypoint-node"
	}
	return m.General.Callsign
}

// --- API handlers ------------------------------------------------------------

// peeringDiscover: GET /api/peering/discover — mDNS browse results (convenience;
// manual host:port always works).
func (s *server) peeringDiscover(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	found, err := s.peering.Discover(r.Context(), 2*time.Second)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, found)
}

// peeringInitiate: POST /api/peering/initiate {host, port} — start pairing,
// returns {sid, code} to display.
func (s *server) peeringInitiate(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		Host string `json:"host"`
		Port string `json:"port"`
		Addr string `json:"addr"` // optional pre-joined host:port
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	addr := req.Addr
	if addr == "" {
		addr = net.JoinHostPort(req.Host, req.Port)
	}
	sid, code, err := s.peering.InitiatePairing(addr)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]string{"sid": sid, "code": code})
}

// peeringConfirm: POST /api/peering/confirm {sid, code}.
func (s *server) peeringConfirm(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct{ SID, Code string }
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.peering.ConfirmPairing(req.SID, req.Code); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// peeringCancel: POST /api/peering/cancel {sid}.
func (s *server) peeringCancel(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct{ SID string }
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	_ = s.peering.CancelPairing(req.SID)
	w.WriteHeader(http.StatusNoContent)
}

// peeringPending: GET /api/peering/pending — sessions awaiting operator action.
func (s *server) peeringPending(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, s.peering.Pending())
}

// peeringPeers: GET /api/peering/peers — the paired/revoked peer rows, REDACTED
// (fingerprints visible, cert/key never) via the config View's PeerView.
func (s *server) peeringPeers(w http.ResponseWriter, r *http.Request) {
	m, err := config.Load(s.store)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, m.View(s.storePath).Peers)
}

// peeringRevoke: POST /api/peering/revoke {peer_id} — immediate local revoke.
func (s *server) peeringRevoke(w http.ResponseWriter, r *http.Request) {
	if s.peering == nil {
		http.Error(w, "peering unavailable", http.StatusServiceUnavailable)
		return
	}
	var req struct {
		PeerID string `json:"peer_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ok, err := s.peering.Revoke(req.PeerID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, "no such peer", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- keypair / node id persistence ------------------------------------------

func loadOrMintKeypair(dir, name string) (peering.Identity, error) {
	certPath := filepath.Join(dir, "node.crt")
	keyPath := filepath.Join(dir, "node.key")
	cert, cerr := os.ReadFile(certPath)
	key, kerr := os.ReadFile(keyPath)
	if cerr == nil && kerr == nil {
		return peering.Identity{CertPEM: string(cert), KeyPEM: string(key)}, nil
	}
	certPEM, keyPEM, err := peering.GenerateKeypair(name)
	if err != nil {
		return peering.Identity{}, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return peering.Identity{}, err
	}
	if err := os.WriteFile(certPath, []byte(certPEM), 0o644); err != nil {
		return peering.Identity{}, err
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		return peering.Identity{}, err
	}
	return peering.Identity{CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

func loadOrMintNodeID(dir string) (string, error) {
	p := filepath.Join(dir, "node.id")
	if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
		return string(b), nil
	}
	id, err := peering.NewNodeID()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return id, os.WriteFile(p, []byte(id), 0o644)
}

func portInt(addr string) int {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	p, _ := net.LookupPort("tcp", portStr)
	return p
}

func decodeJSON(r *http.Request, v any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, v)
}
