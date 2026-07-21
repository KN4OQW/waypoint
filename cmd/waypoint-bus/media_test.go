package main

import (
	"net"
	"testing"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/bus/router"
	"github.com/KN4OQW/waypoint/internal/config"
)

// media_test.go covers the RFC-0016 media-coupling glue this increment adds to the
// daemon: the owner re-emitting router output to a member (with loop prevention +
// hop increment) and the member gating local voice on the owner token. The pure
// transport/loop/token logic is tested in internal/bus/peer; these tests exercise
// the daemon wiring over an in-memory session pair (no TLS, no UDP).

func sessionPair(t *testing.T, memberID, ownerID string) (member, owner *peer.Session) {
	a, b := net.Pipe()
	member = peer.NewSession(a, ownerID, 0) // the member's session TO the owner
	owner = peer.NewSession(b, memberID, 0) // the owner's session TO the member
	member.Start(0)
	owner.Start(0)
	t.Cleanup(func() { member.Close(); owner.Close() })
	return member, owner
}

func cw(b byte) []byte {
	out := make([]byte, frames.AMBEBytes)
	for i := range out {
		out[i] = b
	}
	return out
}

func ysfVoiceFrame(stream uint32) frames.Frame {
	ambe := make([][]byte, 5)
	for i := range ambe {
		ambe[i] = cw(byte(i + 1))
	}
	return frames.Frame{Mode: frames.ModeYSF, Kind: frames.KindVoice, SrcID: 3180202, DstID: 9,
		Stream: frames.Stream{ID: stream}, AMBE: ambe}
}

// recvNonKeepalive reads the next non-keepalive message or fails after a timeout.
func recvNonKeepalive(t *testing.T, s *peer.Session) peer.Message {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case m, ok := <-s.Recv():
			if !ok {
				t.Fatal("session closed before a message arrived")
			}
			if m.Type == peer.MsgKeepalive {
				continue
			}
			return m
		case <-deadline:
			t.Fatal("timed out waiting for a message")
		}
	}
}

// expectNoVoice asserts no MsgVoice arrives within the window (control messages
// are allowed and skipped).
func expectNoVoice(t *testing.T, s *peer.Session, d time.Duration) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case m, ok := <-s.Recv():
			if !ok {
				return
			}
			if m.Type == peer.MsgVoice {
				t.Fatalf("unexpected voice frame delivered (loop prevention should have suppressed it): %+v", m.Voice.Env)
			}
		case <-deadline:
			return
		}
	}
}

// TestOwnerEmitToMembersLoopPrevention: the owner forwards a router emission to a
// member with the hop count incremented, but NEVER back toward the frame's own
// origin node+attachment (RFC-0016 §5).
func TestOwnerEmitToMembersLoopPrevention(t *testing.T) {
	ownerToMember, memberInbox := sessionPair(t, "garage", "shack")
	io := &busIO{
		node: "shack", busID: "A",
		byMode: map[config.Mode][]*memberLink{
			config.ModeYSF: {{node: "garage", mode: config.ModeYSF, attachment: "ysf", sess: ownerToMember}},
		},
	}
	em := router.Emission{Dst: config.ModeYSF, FMode: frames.ModeYSF, Frame: ysfVoiceFrame(1)}

	// A local (shack/dmr) source's YSF emission is delivered to the garage member,
	// hop incremented for the link it crossed.
	io.emitToMembers(em, peer.NewEnvelope("shack", "dmr", "A"))
	got := recvNonKeepalive(t, memberInbox)
	if got.Type != peer.MsgVoice {
		t.Fatalf("expected voice, got %s", got.Type)
	}
	if got.Voice.Env.OriginNode != "shack" || got.Voice.Env.HopCount != 1 {
		t.Fatalf("envelope not forwarded correctly: %+v", got.Voice.Env)
	}

	// A garage/ysf-origin frame must never be re-emitted toward garage/ysf.
	io.emitToMembers(em, peer.NewEnvelope("garage", "ysf", "A"))
	expectNoVoice(t, memberInbox, 200*time.Millisecond)
}

// TestMemberGatesLocalVoiceOnToken: the member requests the token on key-up, drops
// local voice while it does not hold it, and streams (with a fresh cross-peer
// envelope) once granted.
func TestMemberGatesLocalVoiceOnToken(t *testing.T) {
	memberToOwner, ownerInbox := sessionPair(t, "garage", "shack")
	m := &memberRunner{node: "garage", busID: "A", client: peer.NewClient(2 * time.Second)}

	data, err := frames.ConstructYSF(ysfVoiceFrame(0x55), frames.Params{}, nil)
	if err != nil {
		t.Fatalf("construct YSF: %v", err)
	}
	sid := func() uint32 {
		f, err := frames.ParseYSF(data)
		if err != nil {
			t.Fatalf("parse YSF: %v", err)
		}
		return f.Stream.ID
	}()

	// Key-up before a grant: a token request is sent, the voice frame is dropped.
	m.onLocal(memberToOwner, inbound{mode: config.ModeYSF, data: data})
	req := recvNonKeepalive(t, ownerInbox)
	if req.Type != peer.MsgTokenRequest {
		t.Fatalf("expected a token request on key-up, got %s", req.Type)
	}
	expectNoVoice(t, ownerInbox, 150*time.Millisecond)
	if m.client.Dropped() == 0 {
		t.Fatal("voice before the grant should be counted as dropped")
	}

	// Grant arrives: the next local frame streams to the owner with a hop-1 envelope
	// originating at the member.
	m.client.RxGrant(sid, time.Now())
	m.onLocal(memberToOwner, inbound{mode: config.ModeYSF, data: data})
	v := recvNonKeepalive(t, ownerInbox)
	if v.Type != peer.MsgVoice {
		t.Fatalf("expected voice after grant, got %s", v.Type)
	}
	if v.Voice.Env.OriginNode != "garage" || v.Voice.Env.OriginAttachment != "ysf" || v.Voice.Env.HopCount != 1 {
		t.Fatalf("member envelope wrong: %+v", v.Voice.Env)
	}
}

// TestOwnerAnswerTokenArbitratesWithRouter: a member request is granted when the
// router token is free and denied when a local mode holds it.
func TestOwnerAnswerTokenArbitratesWithRouter(t *testing.T) {
	ownerToMember, memberInbox := sessionPair(t, "garage", "shack")
	cfg := router.Config{ID: "A", Name: "A", HangTime: 2 * time.Second}
	dmr, _ := router.AttachmentFor(config.Attachment{Mode: config.ModeDMR})
	ysf, _ := router.AttachmentFor(config.Attachment{Mode: config.ModeYSF})
	cfg.Attachments = []router.Attachment{dmr, ysf}
	bus := router.New(cfg, nil)

	io := &busIO{
		node: "shack", busID: "A",
		byMode: map[config.Mode][]*memberLink{
			config.ModeYSF: {{node: "garage", mode: config.ModeYSF, attachment: "ysf", sess: ownerToMember}},
		},
	}

	// Token free -> granted.
	io.answerToken(bus, tokenReq{node: "garage", streamID: 7, sess: ownerToMember})
	if g := recvNonKeepalive(t, memberInbox); g.Type != peer.MsgTokenGrant {
		t.Fatalf("free token should grant, got %s", g.Type)
	}

	// Local DMR now holds the token; a member request is denied.
	bus.Ingest(config.ModeDMR, ysfLikeDMRHeader(), time.Now())
	if h, held := bus.Holder(); !held || h != config.ModeDMR {
		t.Fatalf("DMR should hold the token, got holder=%q held=%v", h, held)
	}
	io.answerToken(bus, tokenReq{node: "garage", streamID: 8, sess: ownerToMember})
	if d := recvNonKeepalive(t, memberInbox); d.Type != peer.MsgTokenDeny {
		t.Fatalf("held token should deny, got %s", d.Type)
	}
}

func ysfLikeDMRHeader() frames.Frame {
	return frames.Frame{Mode: frames.ModeDMR, Kind: frames.KindHeader, SrcID: 3180202, DstID: 91,
		Stream: frames.Stream{ID: 0x99}}
}
