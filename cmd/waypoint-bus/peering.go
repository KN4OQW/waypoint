package main

import (
	"context"
	"crypto/x509"
	"log"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// peering.go is the owner-side socket/lifecycle shell that plugs the tested
// internal/bus/peer transport (wire protocol, loop prevention, mTLS) into the
// daemon. All protocol logic lives in that package; this file establishes pinned
// mTLS sessions, runs the per-session read loop, and funnels a member's traffic
// into the single run loop over channels (never touching the router directly, so
// the router stays a single-goroutine state machine).
//
// Media coupling (RFC-0016, this increment): a member's accepted voice is pushed
// onto the daemon's frameCh as an injected inbound; the run loop reframes it via
// the SAME router the local loopbacks feed and re-emits router output for a remote
// mode back to that mode's member session(s). The token is the owner's router:
// member key-ups ask via a token request the run loop answers from router state.

// memberLink is one connected member: the node id it presented (== its paired
// peer id), the mode it contributes, and its session. attachment is the mode
// string the cross-peer envelope uses as the origin attachment (loop.go).
type memberLink struct {
	node       string
	mode       config.Mode
	attachment string
	sess       *peer.Session
}

// tokenReq is a member key-up the run loop arbitrates against the router token.
type tokenReq struct {
	node     string
	streamID uint32
	sess     *peer.Session
}

// ownerCtl is the owner-side coupling between the mTLS sessions (many goroutines)
// and the daemon's single run loop. Every field is a channel or immutable, so the
// session goroutines never share mutable state with the run loop.
type ownerCtl struct {
	node    string
	busID   string
	members map[string]config.BusPeerMember // by peer id (== presented node id)

	frameCh chan inbound // injected member voice, merged with local loopback frames
	joinCh  chan *memberLink
	leaveCh chan string
	tokenCh chan tokenReq
	hub     *hub.Hub
}

func newOwnerCtl(node, busID string, members []config.BusPeerMember, frameCh chan inbound, h *hub.Hub) *ownerCtl {
	byID := make(map[string]config.BusPeerMember, len(members))
	for _, m := range members {
		byID[m.PeerID] = m
	}
	return &ownerCtl{
		node: node, busID: busID, members: byID, frameCh: frameCh,
		joinCh:  make(chan *memberLink, 8),
		leaveCh: make(chan string, 8),
		tokenCh: make(chan tokenReq, 16),
		hub:     h,
	}
}

// startOwnerPeering starts the pinned mTLS listener + accept loop for a bus whose
// rendered config carries a peering block. Certs come from the peering dir the
// render referenced by PATH (loaded here, never embedded — RFC-0016 §Security).
func startOwnerPeering(ctx context.Context, bp *config.BusPeering, ctl *ownerCtl) error {
	myCert, err := peer.LoadKeyPair(certPathFor(bp.KeyPath), bp.KeyPath)
	if err != nil {
		return err
	}
	var pinned []*x509.Certificate
	for _, m := range bp.Members {
		c, err := peer.LoadPinnedCert(m.CertPath)
		if err != nil {
			log.Printf("peering: skip member %s (cert %s): %v", m.PeerID, m.CertPath, err)
			continue
		}
		pinned = append(pinned, c)
	}
	ln, err := peer.Listen(bp.Listen, peer.ServerConfig(myCert, pinned...))
	if err != nil {
		return err
	}
	log.Printf("peering: owner listening on %s for %d paired member(s)", bp.Listen, len(pinned))

	go func() {
		for s := range peer.AcceptSessions(ctx, ln) {
			go ctl.serve(ctx, s)
		}
	}()
	return nil
}

// serve bridges one accepted member session: Hello identifies it (and registers
// it with the run loop), token requests are forwarded for arbitration, voice is
// loop-checked and injected, and a disconnect deregisters it.
func (ctl *ownerCtl) serve(ctx context.Context, s *peer.Session) {
	defer s.Close()
	var joined string
	defer func() {
		if joined != "" {
			select {
			case ctl.leaveCh <- joined:
			case <-ctx.Done():
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Closed():
			return
		case m, ok := <-s.Recv():
			if !ok {
				return
			}
			if node := ctl.handle(ctx, s, m); node != "" {
				joined = node
			}
		}
	}
}

// handle processes one message from a member session. It returns the member's
// node id on a successful Hello (so serve records the membership for cleanup).
func (ctl *ownerCtl) handle(ctx context.Context, s *peer.Session, m peer.Message) string {
	switch m.Type {
	case peer.MsgHello:
		if m.Hello == nil {
			return ""
		}
		mem, ok := ctl.members[m.Hello.NodeID]
		if !ok {
			log.Printf("peering: rejecting hello from unknown member %q (not a rendered member of this bus)", m.Hello.NodeID)
			s.Close()
			return ""
		}
		s.SetPeer(m.Hello.NodeID)
		ml := &memberLink{node: m.Hello.NodeID, mode: mem.Mode, attachment: string(mem.Mode), sess: s}
		select {
		case ctl.joinCh <- ml:
		case <-ctx.Done():
			return ""
		}
		log.Printf("peering: member %s connected (contributes %s)", m.Hello.NodeID, mem.Mode)
		return m.Hello.NodeID
	case peer.MsgTokenRequest:
		if m.Token == nil {
			return ""
		}
		select {
		case ctl.tokenCh <- tokenReq{node: s.Peer(), streamID: m.Token.StreamID, sess: s}:
		case <-ctx.Done():
		}
	case peer.MsgTokenRelease:
		// Advisory: the owner's router releases the token on the hang timer once the
		// member's frames stop, so an explicit release is not required for
		// correctness. Logged for visibility.
	case peer.MsgVoice:
		if m.Voice == nil {
			return ""
		}
		mem, ok := ctl.members[s.Peer()]
		if !ok {
			return "" // voice before hello / from an unregistered session: drop
		}
		if ok, reason := peer.AcceptInbound(m.Voice.Env, ctl.node, peer.DefaultMaxHops); !ok {
			log.Printf("peering: dropped a frame from %s (%s)", s.Peer(), reason)
			return ""
		}
		f := m.Voice.Frame
		env := m.Voice.Env
		select {
		case ctl.frameCh <- inbound{mode: mem.Mode, frame: &f, env: &env}:
		case <-ctx.Done():
		}
	case peer.MsgKeepalive:
		// liveness only
	}
	return ""
}

// memberBusDown publishes the RFC-0016 §4 "bus down: owner offline" hub event so
// the dashboard (Prompt 12) can show it. Emitted on the MEMBER side when its owner
// link drops (member.go); kept here alongside the peering events.
func memberBusDown(h *hub.Hub, busID string) {
	h.Publish(hub.Event{Time: time.Now(), Type: "bus_down", Network: busID, Detail: "owner offline"})
}

// certPathFor derives a node's peering certificate path from its key path
// (node.key -> node.crt in the same dir; the render references the key, the cert
// is its sibling).
func certPathFor(keyPath string) string {
	return strings.TrimSuffix(keyPath, ".key") + ".crt"
}
