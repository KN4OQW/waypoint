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

// peering.go is the thin socket/lifecycle shell that plugs the tested
// internal/bus/peer transport (wire protocol, token state machine, loop
// prevention, jitter buffer, mTLS) into the daemon. All protocol logic lives in
// that package and is unit-tested without sockets; this file only establishes
// pinned mTLS sessions from the rendered peering config, runs the token/keepalive
// lifecycle, and surfaces "bus down" as a hub event.
//
// The media fan-out coupling — feeding a member's voice into the router as a
// remote source and re-emitting router output back to members — lands with the
// router's remote-attachment awareness (the next increment); this file already
// carries the frames over the wire and enforces the envelope/loop rules, so that
// step is purely wiring. The bench smoke (Prompt 13) exercises the live path.

// ownerPeering runs the owner side of a peered bus: a pinned mTLS listener that
// accepts paired members, one token Server, and per-session bridges. It never
// blocks the local fan-out — each session is independent, and a slow/dead member
// degrades only its own link (peer.Session backpressure).
type ownerPeering struct {
	busID string
	node  string
	srv   *peer.Server
	hub   *hub.Hub
}

// startOwnerPeering starts the listener + accept loop for a bus whose rendered
// config carries a peering block. Certs come from the peering dir the render
// referenced by PATH (loaded here, never embedded — RFC-0016 §Security posture).
func startOwnerPeering(ctx context.Context, bp *config.BusPeering, busID, node string, h *hub.Hub, hang time.Duration) error {
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
	op := &ownerPeering{busID: busID, node: node, srv: peer.NewServer(hang), hub: h}
	log.Printf("peering: owner listening on %s for %d paired member(s)", bp.Listen, len(pinned))

	go func() {
		for s := range peer.AcceptSessions(ctx, ln) {
			go op.serve(ctx, s)
		}
	}()
	return nil
}

// serve bridges one accepted member session: Hello identifies it, token requests
// are arbitrated, voice is loop-checked, and a disconnect reclaims the token.
func (op *ownerPeering) serve(ctx context.Context, s *peer.Session) {
	defer s.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.Closed():
			if s.Peer() != "" {
				op.srv.MemberDisconnected(s.Peer(), time.Now())
			}
			return
		case m, ok := <-s.Recv():
			if !ok {
				return
			}
			op.handle(s, m)
		}
	}
}

func (op *ownerPeering) handle(s *peer.Session, m peer.Message) {
	now := time.Now()
	switch m.Type {
	case peer.MsgHello:
		if m.Hello != nil {
			s.SetPeer(m.Hello.NodeID)
			log.Printf("peering: member %s connected", m.Hello.NodeID)
		}
	case peer.MsgTokenRequest:
		if op.srv.RequestFromMember(s.Peer(), now) == peer.Granted {
			s.Send(peer.Message{Type: peer.MsgTokenGrant, Token: m.Token})
		} else {
			s.Send(peer.Message{Type: peer.MsgTokenDeny, Token: m.Token})
		}
	case peer.MsgTokenRelease:
		op.srv.ReleaseFromMember(s.Peer())
	case peer.MsgVoice:
		if m.Voice == nil {
			return
		}
		if ok, reason := peer.AcceptInbound(m.Voice.Env, op.node, peer.DefaultMaxHops); !ok {
			log.Printf("peering: dropped a frame (%s)", reason)
			return
		}
		op.srv.MemberActivity(s.Peer(), now)
		// Media fan-out into the router lands with the router's remote-attachment
		// awareness; the envelope + token are already enforced here.
	case peer.MsgKeepalive:
		// liveness only
	}
}

// memberBusDown publishes the RFC-0016 §4 "bus down: owner offline" hub event so
// the dashboard (Prompt 12) can show it.
func memberBusDown(h *hub.Hub, busID string) {
	h.Publish(hub.Event{Time: time.Now(), Type: "bus_down", Network: busID, Detail: "owner offline"})
}

// certPathFor derives a node's peering certificate path from its key path
// (node.key -> node.crt in the same dir; the render references the key, the cert
// is its sibling).
func certPathFor(keyPath string) string {
	return strings.TrimSuffix(keyPath, ".key") + ".crt"
}
