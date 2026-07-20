package peering

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// handshake.go is the mutual short-code pairing handshake (RFC-0016 §3) as a PURE
// state machine — no sockets, no store — so the full matrix (happy path, wrong
// code, expiry, simultaneous initiation, drop at each step) is table-tested and
// every failure provably leaves no trust residue (Result stays nil, so the manager
// writes nothing).
//
// Security model. The two nodes exchange certificates over an UNAUTHENTICATED
// channel (a MITM can swap them). The short code, carried out-of-band by the
// operator (shown on the initiator, entered on the responder), keys an HMAC over
// the exchange transcript. Each side sends HMAC(code, domain || transcript) and
// verifies the peer's against its OWN code: if a MITM substituted a certificate
// the transcripts differ, and without the code it cannot forge a valid tag, so the
// confirmation fails and nothing is pinned. Codes matching + certs intact is the
// only path to Paired.

// Role is a node's side of one handshake.
type Role int

const (
	Initiator Role = iota // generated + shows the code
	Responder             // receives the request; the operator enters the code
)

// Phase is the handshake state.
type Phase int

const (
	PhaseAwaitResponse Phase = iota // initiator: sent request, awaiting the peer cert
	PhaseAwaitConfirm               // have both certs; awaiting local confirm and/or the peer's tag
	PhasePaired                     // verified — Result is set
	PhaseFailed                     // any failure; no trust residue
)

// Identity is a node's pairing identity.
type Identity struct {
	NodeID  string
	Name    string
	CertPEM string
	KeyPEM  string // secret; never leaves the node
}

// Result is the successfully-paired peer, ready to write to the store row.
type Result struct {
	NodeID      string
	Name        string
	CertPEM     string
	Fingerprint string
}

// MsgKind tags a handshake message.
type MsgKind string

const (
	KindRequest  MsgKind = "request"  // initiator -> responder: identity + cert
	KindResponse MsgKind = "response" // responder -> initiator: identity + cert
	KindConfirm  MsgKind = "confirm"  // either -> other: the HMAC tag
	KindCancel   MsgKind = "cancel"   // either -> other: abort
)

// Message is one wire message of the handshake (JSON over the bootstrap channel).
type Message struct {
	Kind     MsgKind `json:"kind"`
	SID      string  `json:"sid"`
	NodeID   string  `json:"node_id,omitempty"`
	NodeName string  `json:"node_name,omitempty"`
	CertPEM  string  `json:"cert_pem,omitempty"`
	Tag      string  `json:"tag,omitempty"` // hex HMAC over the transcript
}

// Handshake is one pairing session from one node's perspective.
type Handshake struct {
	role   Role
	local  Identity
	peer   Identity
	sid    string
	code   string // initiator: generated; responder: entered at Confirm
	expiry time.Time

	phase          Phase
	localConfirmed bool
	peerTag        string
	failReason     string
	result         *Result
}

// Initiate starts the initiator's handshake: it mints a session id + short code
// and returns the REQUEST to send. The code is shown on this node's dashboard.
func Initiate(local Identity, now time.Time) (*Handshake, Message, error) {
	sid, err := randomHex(16)
	if err != nil {
		return nil, Message{}, err
	}
	code, err := NewCode()
	if err != nil {
		return nil, Message{}, err
	}
	h := &Handshake{role: Initiator, local: local, sid: sid, code: code, expiry: now.Add(CodeExpiry), phase: PhaseAwaitResponse}
	return h, Message{Kind: KindRequest, SID: sid, NodeID: local.NodeID, NodeName: local.Name, CertPEM: local.CertPEM}, nil
}

// Respond starts the responder's handshake from an incoming REQUEST and returns
// the RESPONSE to send. The dashboard now prompts the operator to enter the code
// shown on the initiator.
func Respond(local Identity, req Message, now time.Time) (*Handshake, Message, error) {
	if req.Kind != KindRequest || req.SID == "" || req.NodeID == "" || req.CertPEM == "" {
		return nil, Message{}, errors.New("peering: malformed pairing request")
	}
	if _, err := certDER(req.CertPEM); err != nil {
		return nil, Message{}, fmt.Errorf("peering: peer request cert: %w", err)
	}
	h := &Handshake{
		role: Responder, local: local, sid: req.SID, expiry: now.Add(CodeExpiry),
		peer:  Identity{NodeID: req.NodeID, Name: req.NodeName, CertPEM: req.CertPEM},
		phase: PhaseAwaitConfirm,
	}
	return h, Message{Kind: KindResponse, SID: req.SID, NodeID: local.NodeID, NodeName: local.Name, CertPEM: local.CertPEM}, nil
}

// Phase / Code / SID / Result expose state for the manager and tests.
func (h *Handshake) Phase() Phase       { return h.phase }
func (h *Handshake) Code() string       { return h.code }
func (h *Handshake) SID() string        { return h.sid }
func (h *Handshake) PeerNode() string   { return h.peer.NodeID }
func (h *Handshake) FailReason() string { return h.failReason }

// Result returns the paired peer once verified; ok is false in every other state
// (including every failure), so a caller writes trust state only on success.
func (h *Handshake) Result() (*Result, bool) { return h.result, h.result != nil }

// Step processes an incoming handshake message, returning any messages to send. A
// malformed or unexpected message fails the handshake (leaving no residue) rather
// than panicking.
func (h *Handshake) Step(msg Message, now time.Time) ([]Message, error) {
	if h.terminal() {
		return nil, nil
	}
	if h.expired(now) {
		return nil, nil
	}
	if msg.SID != h.sid {
		return nil, nil // not our session
	}
	switch msg.Kind {
	case KindResponse:
		if h.role != Initiator || h.phase != PhaseAwaitResponse {
			return nil, nil
		}
		if msg.NodeID == "" || msg.CertPEM == "" {
			h.fail("malformed response")
			return nil, nil
		}
		if _, err := certDER(msg.CertPEM); err != nil {
			h.fail("bad response cert")
			return nil, nil
		}
		h.peer = Identity{NodeID: msg.NodeID, Name: msg.NodeName, CertPEM: msg.CertPEM}
		h.phase = PhaseAwaitConfirm
		return nil, nil
	case KindConfirm:
		if h.phase != PhaseAwaitConfirm && h.phase != PhasePaired {
			return nil, nil
		}
		h.peerTag = msg.Tag
		return h.tryFinish(), nil
	case KindCancel:
		h.fail("peer cancelled")
		return nil, nil
	}
	return nil, nil
}

// Confirm is the operator action: the initiator confirms (its generated code is
// used; a non-empty arg must match it, so an operator may re-type on both nodes),
// the responder enters the code. It computes this side's tag and returns the
// CONFIRM to send. It is an error to confirm before both certs are exchanged.
func (h *Handshake) Confirm(code string, now time.Time) ([]Message, error) {
	if h.terminal() {
		return nil, errors.New("peering: handshake already finished")
	}
	if h.expired(now) {
		return nil, errors.New("peering: pairing code expired")
	}
	if h.phase != PhaseAwaitConfirm || h.peer.CertPEM == "" {
		return nil, errors.New("peering: not ready to confirm (certificates not exchanged)")
	}
	switch h.role {
	case Initiator:
		if code != "" && code != h.code {
			return nil, errors.New("peering: entered code does not match the displayed code")
		}
	case Responder:
		if code == "" {
			return nil, errors.New("peering: a code is required")
		}
		h.code = code
	}
	h.localConfirmed = true
	tag := h.tag(h.role) // our own tag, keyed by our code
	out := []Message{{Kind: KindConfirm, SID: h.sid, Tag: tag}}
	return append(out, h.tryFinish()...), nil
}

// Cancel aborts the handshake locally, returning a CANCEL to send. No residue.
func (h *Handshake) Cancel() Message {
	h.fail("cancelled")
	return Message{Kind: KindCancel, SID: h.sid}
}

// Tick fails the handshake if the code has expired. Returns true if it just
// expired.
func (h *Handshake) Tick(now time.Time) bool {
	if !h.terminal() && h.expired(now) {
		h.fail("pairing code expired")
		return true
	}
	return false
}

// tryFinish verifies the peer's tag once BOTH sides have acted (we confirmed AND
// the peer's tag arrived). Success pins the peer cert; a mismatch fails with no
// residue.
func (h *Handshake) tryFinish() []Message {
	if h.phase != PhaseAwaitConfirm || !h.localConfirmed || h.peerTag == "" {
		return nil
	}
	want := h.tag(otherRole(h.role)) // the peer's expected tag, keyed by OUR code
	got, err := hex.DecodeString(h.peerTag)
	wantB, _ := hex.DecodeString(want)
	if err != nil || subtle.ConstantTimeCompare(got, wantB) != 1 {
		h.fail("code mismatch or tampered exchange")
		return nil
	}
	fp, err := Fingerprint(h.peer.CertPEM)
	if err != nil {
		h.fail("peer cert unparseable")
		return nil
	}
	h.result = &Result{NodeID: h.peer.NodeID, Name: h.peer.Name, CertPEM: h.peer.CertPEM, Fingerprint: fp}
	h.phase = PhasePaired
	return nil
}

// tag computes HMAC(code, domain(role) || transcript). The transcript fixes the
// initiator's identity+cert first so both nodes hash identical bytes when there is
// no MITM.
func (h *Handshake) tag(who Role) string {
	var initID, respID, initCert, respCert string
	if h.role == Initiator {
		initID, initCert = h.local.NodeID, h.local.CertPEM
		respID, respCert = h.peer.NodeID, h.peer.CertPEM
	} else {
		initID, initCert = h.peer.NodeID, h.peer.CertPEM
		respID, respCert = h.local.NodeID, h.local.CertPEM
	}
	initDER, _ := certDER(initCert)
	respDER, _ := certDER(respCert)

	mac := hmac.New(sha256.New, []byte(h.code))
	mac.Write([]byte{byte(who)}) // domain separation: initiator's tag vs responder's
	mac.Write([]byte(h.sid))
	mac.Write([]byte(initID))
	mac.Write(initDER)
	mac.Write([]byte(respID))
	mac.Write(respDER)
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *Handshake) fail(reason string) {
	h.phase, h.failReason, h.result = PhaseFailed, reason, nil
}
func (h *Handshake) terminal() bool { return h.phase == PhasePaired || h.phase == PhaseFailed }
func (h *Handshake) expired(now time.Time) bool {
	if now.After(h.expiry) {
		h.fail("pairing code expired")
		return true
	}
	return false
}

func otherRole(r Role) Role {
	if r == Initiator {
		return Responder
	}
	return Initiator
}

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
