package main

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"os/signal"
	"syscall"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/bus/router"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// member.go is the RFC-0016 MEMBER role of waypoint-bus: a node contributing its
// local mode(s) to a bus OWNED by a peer. It binds the same fixed loopback a local
// bus would (so its mode's gateway is fed exactly as on the owner), dials the
// owner over pinned mTLS, and runs the token CLIENT — the owner owns the single
// cluster-wide token (RFC-0016 §4). Local voice is streamed to the owner only when
// the token is held; voice arriving from the owner (already reframed to this
// member's mode by the owner's router) is played out to the local loopback.
//
// The transport, token lifecycle, loop envelope, and reconnect backoff are the
// tested internal/bus/peer package; this file is the socket/lifecycle glue.

// memberEndpoint pairs a local loopback with the resolved frame params for one
// contributed mode.
type memberEndpoint struct {
	mode   config.Mode
	fmode  frames.Mode
	ep     *endpoint
	params frames.Params
}

func runMember(cfgPath, dmridsPath, nodeID, ownerAddr string) {
	mc, err := config.ReadMemberConfig(cfgPath)
	if err != nil {
		log.Fatalf("waypoint-bus: %v", err)
	}
	log.SetPrefix("waypoint-bus[member " + mc.BusID + "]: ")
	if nodeID == "" {
		log.Fatal("waypoint-bus: -node is required for a member config (the id this node presents to the owner)")
	}
	// The rendered config carries the owner's listen port on a 0.0.0.0 host; the
	// member dials the owner's real LAN address, resolved from its pairing record
	// (supplied via -owner-addr on the bench).
	ownerListen := mc.Owner.Listen
	if ownerAddr != "" {
		ownerListen = ownerAddr
	}
	if host, _, _ := net.SplitHostPort(ownerListen); host == "0.0.0.0" || host == "" {
		log.Fatalf("waypoint-bus: owner address %q has no dialable host; pass -owner-addr host:port", ownerListen)
	}

	resolver := loadResolver(dmridsPath)

	// Pinned mTLS: our own peering keypair + the owner's pinned cert (paths the
	// render referenced; no PEM is ever embedded — RFC-0016 §Security posture).
	myCert, err := peer.LoadKeyPair(certPathFor(mc.KeyPath), mc.KeyPath)
	if err != nil {
		log.Fatalf("member: load keypair: %v", err)
	}
	ownerCert, err := peer.LoadPinnedCert(mc.Owner.CertPath)
	if err != nil {
		log.Fatalf("member: load owner cert %s: %v", mc.Owner.CertPath, err)
	}
	tlsCfg := peer.ClientConfig(myCert, ownerCert)

	hang := time.Duration(mc.HangTimeSeconds * float64(time.Second))
	if hang <= 0 {
		hang = config.DefaultBusHangTime
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	h := hub.New()
	go logEvents(h)
	startEventPublisher(ctx, mc.MQTT, mc.BusID, h) // D4: republish events to MQTT (best-effort)

	// Bind each contributed mode's local loopback once; the channel outlives
	// reconnects so replayed/keyed local frames are never lost across a blip.
	localCh := make(chan inbound, 256)
	byMode := make(map[config.Mode]*memberEndpoint, len(mc.Attachments))
	byFMode := make(map[frames.Mode]*memberEndpoint, len(mc.Attachments))
	for _, a := range mc.Attachments {
		lb := loopback{bind: a.Loopback.Bind, peer: a.Loopback.Peer}
		ep, err := openEndpoint(a.Mode, lb)
		if err != nil {
			log.Fatalf("member: open %s loopback: %v", a.Mode, err)
		}
		att, err := router.AttachmentFor(memberAttachmentFrom(a))
		if err != nil {
			log.Fatalf("member: resolve %s params: %v", a.Mode, err)
		}
		me := &memberEndpoint{mode: a.Mode, fmode: att.FMode, ep: ep, params: att.Params}
		byMode[a.Mode] = me
		byFMode[att.FMode] = me
		go ep.recv(ctx, localCh)
		log.Printf("member %s: local %s loopback listen 127.0.0.1:%d peer 127.0.0.1:%d",
			nodeID, a.Mode, lb.bind, lb.peer)
	}

	m := &memberRunner{
		node: nodeID, busID: mc.BusID, owner: ownerListen,
		tlsCfg: tlsCfg, hang: hang, resolver: resolver,
		bufferMs: mc.JitterBufferMs, deadlineMs: mc.DeadlineMs,
		byMode: byMode, byFMode: byFMode, localCh: localCh, hub: h,
		client: peer.NewClient(hang),
	}
	m.run(ctx)

	for _, me := range byMode {
		me.ep.close()
	}
}

// memberRunner is the member's reconnecting session loop and token client.
type memberRunner struct {
	node, busID, owner   string
	tlsCfg               *tls.Config
	hang                 time.Duration
	bufferMs, deadlineMs int // per-peer play-out depths (RFC-0016 §5); 0 ⇒ defaults
	resolver             frames.Resolver
	byMode               map[config.Mode]*memberEndpoint
	byFMode              map[frames.Mode]*memberEndpoint
	localCh              chan inbound
	hub                  *hub.Hub
	client               *peer.Client
}

// run reconnects to the owner until ctx is cancelled, draining local frames while
// disconnected (dropped, not queued — voice is realtime). Each connected session
// runs session() until it drops.
func (m *memberRunner) run(ctx context.Context) {
	bo := peer.DefaultBackoff()
	dial := peer.TLSDialer(m.tlsCfg)
	for {
		if ctx.Err() != nil {
			return
		}
		sess, err := peer.DialWithBackoff(ctx, m.owner, m.node, dial, bo)
		if err != nil {
			return // ctx cancelled
		}
		bo.Reset()
		log.Printf("member %s: connected to owner %s", m.node, m.owner)
		// D4 / RFC-0008 no-latching: a reconnect clears the retained "bus down" state
		// (the publisher republishes an empty retained payload on bus_up).
		m.hub.Publish(hub.Event{Time: time.Now(), Type: "bus_up", Network: m.busID, Detail: "owner online"})
		sess.Send(peer.Message{Type: peer.MsgHello, Hello: &peer.Hello{NodeID: m.node, BusID: m.busID, Role: peer.RoleMember}})
		m.session(ctx, sess)
		if wasActive := m.client.OwnerDisconnected(); wasActive {
			log.Printf("member %s: owner link dropped mid-transmission", m.node)
		}
		memberBusDown(m.hub, m.busID) // RFC-0016 §4: bus down (owner offline); self-clears on reconnect
	}
}

// session drives one connected owner session: local frames -> owner (token-gated),
// owner voice -> the play-out buffer -> local loopback (RFC-0016 §5), token
// grants/denies, and the hang-driven release. Owner voice no longer emits on
// arrival: it schedules through peer.JitterBuffer and a play timer drains it on the
// 20 ms cadence, so LAN jitter is smoothed and a late frame is dropped, not played
// late (P2-3).
func (m *memberRunner) session(ctx context.Context, sess *peer.Session) {
	defer sess.Close()
	ticker := time.NewTicker(releaseTick)
	defer ticker.Stop()

	play := newPlayoutScheduler(m.bufferMs, m.deadlineMs)
	playTimer := time.NewTimer(time.Hour)
	armTimer(playTimer, time.Time{}, false) // start disarmed (drain the initial tick)
	defer playTimer.Stop()
	defer func() {
		if d := play.Dropped(); play.Delivered() > 0 || d > 0 {
			log.Printf("member %s: play-out delivered %d, dropped %d (late past deadline)", m.node, play.Delivered(), d)
		}
	}()
	rearm := func() { at, ok := play.nextPlayAt(); armTimer(playTimer, at, ok) }

	for {
		select {
		case <-ctx.Done():
			return
		case <-sess.Closed():
			return
		case now := <-ticker.C:
			if rel, sid := m.client.Tick(now); rel {
				sess.Send(peer.Message{Type: peer.MsgTokenRelease, Token: &peer.Token{BusID: m.busID, StreamID: sid}})
			}
		case in := <-m.localCh:
			m.onLocal(sess, in)
		case now := <-playTimer.C:
			for _, f := range play.emitDue(now) {
				m.emit(f)
			}
			rearm()
		case msg, ok := <-sess.Recv():
			if !ok {
				return
			}
			m.onOwner(msg, play)
			rearm()
		}
	}
}

// emit plays one due frame out to its local loopback.
func (m *memberRunner) emit(f scheduledFrame) {
	if err := f.me.ep.send(f.out); err != nil {
		log.Printf("member %s: send %s: %v", m.node, f.me.mode, err)
	}
}

// onLocal handles a frame off a local loopback: request the token on key-up, then
// stream to the owner while the token is held (dropping voice while requesting or
// denied). Header/terminator bracket the transmission.
func (m *memberRunner) onLocal(sess *peer.Session, in inbound) {
	f, err := parseFrame(in.mode, in.data)
	if err != nil {
		return
	}
	now := time.Now()
	if f.Kind == frames.KindHeader || (f.Kind == frames.KindVoice) {
		if m.client.LocalKeyup(f.Stream.ID, now) {
			sess.Send(peer.Message{Type: peer.MsgTokenRequest, Token: &peer.Token{BusID: m.busID, StreamID: f.Stream.ID}})
		}
	}
	if !m.client.CanStream() {
		if f.Kind == frames.KindVoice {
			m.client.DropLocalVoice()
		}
		return
	}
	m.client.LocalVoice(now)
	env := peer.Forward(peer.NewEnvelope(m.node, string(in.mode), m.busID)) // origin = here; +1 hop for the owner link
	sess.Send(peer.Message{Type: peer.MsgVoice, Voice: &peer.Voice{Env: env, Frame: f}})
}

// onOwner handles a message from the owner: voice (already reframed to a local
// mode) is scheduled through the play-out buffer for its cadence slot; grants/denies
// advance the token client. Voice is NOT emitted here — the session's play timer
// drains the buffer at the play-out slot (RFC-0016 §5, P2-3).
func (m *memberRunner) onOwner(msg peer.Message, play *playoutScheduler) {
	switch msg.Type {
	case peer.MsgVoice:
		if msg.Voice == nil {
			return
		}
		if ok, reason := peer.AcceptInbound(msg.Voice.Env, m.node, peer.DefaultMaxHops); !ok {
			log.Printf("member %s: dropped a frame from owner (%s)", m.node, reason)
			return
		}
		me := m.byFMode[msg.Voice.Frame.Mode]
		if me == nil {
			return // the owner sent a mode we do not contribute; ignore
		}
		out, err := constructFrame(me.mode, msg.Voice.Frame, me.params, m.resolver)
		if err != nil {
			log.Printf("member %s: construct %s: %v", m.node, me.mode, err)
			return
		}
		// Enqueue for play-out; stragglers from a previous stream (rare) emit now.
		for _, f := range play.schedule(msg.Voice.Frame.Stream.ID, me, out, time.Now()) {
			m.emit(f)
		}
	case peer.MsgTokenGrant:
		if msg.Token != nil {
			m.client.RxGrant(msg.Token.StreamID, time.Now())
		}
	case peer.MsgTokenDeny:
		if msg.Token != nil {
			m.client.RxDeny(msg.Token.StreamID)
			log.Printf("member %s: token denied (bus busy) — local voice dropped", m.node)
		}
	case peer.MsgKeepalive:
		// liveness only
	}
}

// memberAttachmentFrom builds the config.Attachment view of a rendered member
// attachment so the router resolves its mode + params through the shared path.
func memberAttachmentFrom(a config.MemberAttachment) config.Attachment {
	return config.Attachment{
		Mode: a.Mode, Slot: a.Slot, DefaultTG: a.DefaultTG, TGMap: a.TGMap,
		Target: a.Target, WiresXPassthrough: a.WiresXPassthrough,
		ID: a.ID, TG: a.TG, DefaultID: a.DefaultID,
	}
}
