package peering

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/store"
)

// manager.go is the thin orchestration around the pure handshake: it moves
// handshake Messages over a plain-TCP bootstrap channel (the certificates are
// public and the code is out-of-band, so the bootstrap needs no prior trust),
// registers pending sessions for the operator to confirm, and — only on a verified
// Result — writes the paired peer to the store. It is a shell; all the security
// lives in handshake.go.

// Pending is a pairing awaiting operator action, surfaced to the dashboard.
type Pending struct {
	SID         string `json:"sid"`
	Role        string `json:"role"` // "initiator" | "responder"
	PeerNode    string `json:"peer_node,omitempty"`
	PeerName    string `json:"peer_name,omitempty"`
	Code        string `json:"code,omitempty"`        // the initiator's displayed code (responder shows "" until entered)
	Fingerprint string `json:"fingerprint,omitempty"` // the peer cert fingerprint once exchanged (out-of-band check)
	Addr        string `json:"addr,omitempty"`
}

// Manager owns this node's pairing identity and its in-flight sessions.
type Manager struct {
	self     Identity
	nodeID   string
	nodeName string
	endpoint string // the peering endpoint advertised/stored for paired peers (host:port)
	store    *store.Store
	disc     Discovery
	now      func() time.Time

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	mu   sync.Mutex // guards hs (touched by the read loop and by operator Confirm/Cancel)
	hs   *Handshake
	conn net.Conn
	r    *bufio.Reader // one reader per conn so read-ahead is not lost between messages
	role Role
	addr string // peer host:port (for the store row)
}

// NewManager builds a manager. self is this node's peering keypair identity;
// endpoint is the host:port peers reach this node's transport on (stored on a
// paired row). disc may be nil (discovery disabled; manual host:port still works).
func NewManager(st *store.Store, self Identity, nodeID, nodeName, endpoint string, disc Discovery) *Manager {
	return &Manager{
		self: self, nodeID: nodeID, nodeName: nodeName, endpoint: endpoint,
		store: st, disc: disc, now: time.Now, sessions: map[string]*session{},
	}
}

// Serve runs the bootstrap listener until ctx is cancelled: it accepts incoming
// pairing requests (the responder side).
func (m *Manager) Serve(ctx context.Context, ln net.Listener) {
	go func() { <-ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go m.handleIncoming(conn)
	}
}

// InitiatePairing dials a peer's bootstrap endpoint, starts the initiator
// handshake, and returns the session id + the short code to display. The response
// and confirmation happen asynchronously; the operator later calls ConfirmPairing.
func (m *Manager) InitiatePairing(hostPort string) (sid, code string, err error) {
	conn, err := net.DialTimeout("tcp", hostPort, 5*time.Second)
	if err != nil {
		return "", "", err
	}
	hs, req, err := Initiate(m.identity(), m.now())
	if err != nil {
		conn.Close()
		return "", "", err
	}
	if err := writeMsg(conn, req); err != nil {
		conn.Close()
		return "", "", err
	}
	s := &session{hs: hs, conn: conn, r: bufio.NewReader(conn), role: Initiator, addr: hostPort}
	m.register(s)
	go m.readLoop(s)
	return hs.SID(), hs.Code(), nil
}

func (m *Manager) handleIncoming(conn net.Conn) {
	r := bufio.NewReader(conn)
	req, err := readMsg(r, conn)
	if err != nil || req.Kind != KindRequest {
		conn.Close()
		return
	}
	hs, resp, err := Respond(m.identity(), req, m.now())
	if err != nil {
		conn.Close()
		return
	}
	if err := writeMsg(conn, resp); err != nil {
		conn.Close()
		return
	}
	s := &session{hs: hs, conn: conn, r: r, role: Responder, addr: conn.RemoteAddr().String()}
	m.register(s)
	go m.readLoop(s)
}

// readLoop drives one session's inbound messages through the handshake and
// persists on a verified Result.
func (m *Manager) readLoop(s *session) {
	defer s.conn.Close()
	for {
		msg, err := readMsg(s.r, s.conn)
		if err != nil {
			m.finish(s)
			return
		}
		s.mu.Lock()
		out, _ := s.hs.Step(msg, m.now())
		s.mu.Unlock()
		for _, o := range out {
			writeMsg(s.conn, o)
		}
		if m.persistIfDone(s) {
			return
		}
	}
}

// ConfirmPairing is the operator confirming a session (initiator: the displayed
// code; responder: the entered code). It sends this side's tag and persists if the
// exchange completes.
func (m *Manager) ConfirmPairing(sid, code string) error {
	s := m.get(sid)
	if s == nil {
		return fmt.Errorf("peering: no such pairing session %q", sid)
	}
	s.mu.Lock()
	out, err := s.hs.Confirm(code, m.now())
	s.mu.Unlock()
	if err != nil {
		return err
	}
	for _, o := range out {
		if err := writeMsg(s.conn, o); err != nil {
			return err
		}
	}
	m.persistIfDone(s)
	return nil
}

// CancelPairing aborts a session (no trust residue) and notifies the peer.
func (m *Manager) CancelPairing(sid string) error {
	s := m.get(sid)
	if s == nil {
		return nil
	}
	s.mu.Lock()
	cancel := s.hs.Cancel()
	s.mu.Unlock()
	writeMsg(s.conn, cancel)
	m.finish(s)
	return nil
}

// Revoke revokes a peer locally (immediate) and best-effort notifies it. The
// peer's next handshake fails the pin regardless of whether the notify arrived.
func (m *Manager) Revoke(peerID string) (bool, error) {
	ok, err := m.storeRevoke(peerID)
	if err != nil || !ok {
		return ok, err
	}
	// best-effort notify is a no-op stub here (the live connection, if any, drops on
	// the next transport handshake against the now-unpinned cert). A dedicated
	// revoke-notify channel can ride the transport later.
	return true, nil
}

// Pending lists the sessions awaiting operator action (for the dashboard).
func (m *Manager) Pending() []Pending {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Pending, 0, len(m.sessions))
	for _, s := range m.sessions {
		s.mu.Lock()
		p := Pending{SID: s.hs.SID(), PeerNode: s.hs.PeerNode(), PeerName: s.hs.PeerName(), Fingerprint: s.hs.PeerFingerprint(), Addr: s.addr}
		if s.role == Initiator {
			p.Role, p.Code = "initiator", s.hs.Code()
		} else {
			p.Role = "responder"
		}
		s.mu.Unlock()
		out = append(out, p)
	}
	return out
}

func (m *Manager) persistIfDone(s *session) bool {
	s.mu.Lock()
	res, ok := s.hs.Result()
	failed := s.hs.Phase() == PhaseFailed
	s.mu.Unlock()
	if ok {
		host, port := splitHostPort(s.addr)
		_ = ApplyPairing(m.store, res, host, port, m.self.KeyPEM)
		m.finish(s)
		return true
	}
	if failed {
		m.finish(s)
		return true
	}
	return false
}

func (m *Manager) identity() Identity {
	return Identity{NodeID: m.nodeID, Name: m.nodeName, CertPEM: m.self.CertPEM, KeyPEM: m.self.KeyPEM}
}
func (m *Manager) register(s *session) { m.mu.Lock(); m.sessions[s.hs.SID()] = s; m.mu.Unlock() }
func (m *Manager) get(sid string) *session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sid]
}
func (m *Manager) finish(s *session) {
	m.mu.Lock()
	delete(m.sessions, s.hs.SID())
	m.mu.Unlock()
	s.conn.Close()
}
func (m *Manager) storeRevoke(peerID string) (bool, error) { return Revoke(m.store, peerID) }

// --- newline-delimited JSON framing over the bootstrap conn -----------------

func writeMsg(c net.Conn, msg Message) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = c.Write(append(b, '\n'))
	return err
}

func readMsg(r *bufio.Reader, c net.Conn) (Message, error) {
	_ = c.SetReadDeadline(time.Now().Add(CodeExpiry + time.Minute))
	line, err := r.ReadBytes('\n')
	if err != nil {
		return Message{}, err
	}
	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return Message{}, err
	}
	return msg, nil
}

func splitHostPort(addr string) (string, int) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return addr, 0
	}
	p, _ := net.LookupPort("tcp", portStr)
	return host, p
}

// Discover browses for peer nodes over mDNS (nil-disc-safe: returns empty).
func (m *Manager) Discover(ctx context.Context, timeout time.Duration) ([]Found, error) {
	if m.disc == nil {
		return nil, nil
	}
	return m.disc.Browse(ctx, timeout)
}
