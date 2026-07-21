// waypoint-bus is the per-bus hub process RFC-0003's waypoint-bus@<id>.service
// runs. It reads one rendered bus config, binds each attached mode's fixed
// loopback endpoint, and fans voice frames between the attachments through the
// pure frame/router layer — enforcing §5's four loop-prevention rules. It holds
// no credentials and does no transcoding: the AMBE+2 family reframes (RFC-0003
// §2), so there is no vocoder here under any circumstances.
//
// It is a thin I/O shell around internal/bus/router (the tested state machine):
// this file only moves bytes between UDP sockets (and, for a peered bus, mTLS peer
// sessions) and the router, brackets the lifecycle, and logs. All the loop and
// arbitration logic lives in the router/peer packages and is unit-tested without
// sockets. One binary runs both roles: an OWNER bus config, or a member config
// (RFC-0016) dispatched to member.go.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os/signal"
	"syscall"
	"time"

	"github.com/KN4OQW/waypoint/internal/bus/frames"
	"github.com/KN4OQW/waypoint/internal/bus/peer"
	"github.com/KN4OQW/waypoint/internal/bus/router"
	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrids"
	"github.com/KN4OQW/waypoint/internal/hub"
)

// releaseTick is how often the run loop checks whether the token holder has gone
// silent past the hang time (RFC-0003 §5 rule 2) — frequent enough that release
// is prompt, cheap enough to be free.
const releaseTick = 250 * time.Millisecond

func main() {
	cfgPath := flag.String("config", "", "path to the rendered bus config JSON (required)")
	dmridsPath := flag.String("dmrids", "/usr/local/etc/DMRIds.dat", "shared DMR/NXDN id<->callsign table")
	nodeID := flag.String("node", "", "this node's peering id (RFC-0016; the frame envelope origin for loop prevention, and the id this node presents to a bus owner)")
	ownerAddr := flag.String("owner-addr", "", "member role only: override the owner's dial address (host:port). The rendered member config carries the owner's listen port but a 0.0.0.0 host; the member resolves the owner's LAN address from its pairing record — supplied here for the bench")
	flag.Parse()

	// systemd/journald stamps its own timestamps; keep our lines clean and prefixed.
	log.SetFlags(0)
	if *cfgPath == "" {
		log.Fatal("waypoint-bus: -config is required")
	}

	// One binary, two roles (RFC-0016): a member config dials a remote owner; an
	// owner config binds the local loopbacks and (if peered) accepts members.
	role, err := config.ConfigRole(*cfgPath)
	if err != nil {
		log.Fatalf("waypoint-bus: %v", err)
	}
	if role == "member" {
		runMember(*cfgPath, *dmridsPath, *nodeID, *ownerAddr)
		return
	}
	runOwner(*cfgPath, *dmridsPath, *nodeID)
}

// runOwner is the bus-owner role: the Phase-1 local loopback fan-out, plus (when
// the rendered config carries a peering block) the RFC-0016 owner side — member
// modes join the same router, member voice is injected, and router output for a
// member's mode is re-emitted to that member over its mTLS session.
func runOwner(cfgPath, dmridsPath, nodeID string) {
	bc, err := config.ReadBusConfig(cfgPath)
	if err != nil {
		log.Fatalf("waypoint-bus: %v", err)
	}
	log.SetPrefix(fmt.Sprintf("waypoint-bus[%s]: ", bc.Bus.ID))

	rcfg, err := router.FromBusConfig(bc)
	if err != nil {
		log.Fatalf("resolve config: %v", err)
	}

	// RFC-0016: a peered bus adds each member's mode to the SAME router, so a
	// member's voice reframes to the local modes and vice versa exactly as a local
	// attachment would — the only difference is its I/O is a peer link, tracked in
	// remoteModes so the run loop sends its emissions over the wire, not a loopback.
	remoteModes := make(map[config.Mode]bool)
	if bc.Peering != nil {
		for _, mem := range bc.Peering.Members {
			if remoteModes[mem.Mode] {
				log.Printf("peering: %s already contributed by another member; the router keys by mode, so a second member of the same mode is not yet supported — skipping %s", mem.Mode, mem.PeerID)
				continue
			}
			att, err := router.AttachmentFor(memberAttachment(mem))
			if err != nil {
				log.Fatalf("peering member %s: %v", mem.PeerID, err)
			}
			rcfg.Attachments = append(rcfg.Attachments, att)
			remoteModes[mem.Mode] = true
		}
	}

	resolver := loadResolver(dmridsPath)

	h := hub.New()
	go logEvents(h)
	bus := router.New(rcfg, h)

	io := &busIO{
		node:        nodeID,
		busID:       bc.Bus.ID,
		params:      make(map[config.Mode]frames.Params, len(rcfg.Attachments)),
		resolver:    resolver,
		eps:         make(map[config.Mode]*endpoint),
		remoteModes: remoteModes,
		byMode:      make(map[config.Mode][]*memberLink),
	}
	for _, a := range rcfg.Attachments {
		io.params[a.Mode] = a.Params
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	frameCh := make(chan inbound, 256)
	for _, a := range rcfg.Attachments {
		if remoteModes[a.Mode] {
			continue // a member's mode has no local loopback; it rides the peer link
		}
		lb, err := loopbackFor(a.Mode)
		if err != nil {
			log.Fatalf("%v", err)
		}
		ep, err := openEndpoint(a.Mode, lb)
		if err != nil {
			log.Fatalf("open %s loopback: %v", a.Mode, err)
		}
		io.eps[a.Mode] = ep
		go ep.recv(ctx, frameCh)
		log.Printf("attached %s: listen 127.0.0.1:%d, peer 127.0.0.1:%d", a.Mode, lb.bind, lb.peer)
	}

	// RFC-0016 LAN peering: if the rendered config carries a peering block, this bus
	// is a peered owner — start the pinned mTLS listener + the member coupling. A
	// peering failure is logged, never fatal: the local bus keeps running.
	var ctl *ownerCtl
	if bc.Peering != nil {
		ctl = newOwnerCtl(nodeID, bc.Bus.ID, bc.Peering.Members, frameCh, h)
		if err := startOwnerPeering(ctx, bc.Peering, ctl); err != nil {
			log.Printf("peering: not started: %v", err)
			ctl = nil
		}
	}

	ticker := time.NewTicker(releaseTick)
	defer ticker.Stop()
	log.Printf("bus %q up: %d local attachment(s), %d member mode(s), hang %s",
		bc.Bus.Name, len(io.eps), len(remoteModes), rcfg.HangTime)

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutdown: releasing token, closing %d sockets", len(io.eps))
			bus.ForceRelease(time.Now())
			for _, ep := range io.eps {
				ep.close()
			}
			if d := bus.Dropped(); d > 0 {
				log.Printf("dropped %d frames to arbitration over this run", d)
			}
			return

		case <-ticker.C:
			bus.MaybeRelease(time.Now())

		case ml := <-ctlJoin(ctl):
			io.byMode[ml.mode] = append(io.byMode[ml.mode], ml)

		case node := <-ctlLeave(ctl):
			io.removeMember(node)

		case tr := <-ctlToken(ctl):
			io.answerToken(bus, tr)

		case in := <-frameCh:
			io.handleFrame(bus, in)
		}
	}
}

// busIO holds the owner run loop's I/O state: the local loopback endpoints, the
// connected member links (owned by the run loop, so no locks), and the render's
// resolver/params. handleFrame is the one place frames turn into emissions.
type busIO struct {
	node        string
	busID       string
	params      map[config.Mode]frames.Params
	resolver    frames.Resolver
	eps         map[config.Mode]*endpoint
	remoteModes map[config.Mode]bool
	byMode      map[config.Mode][]*memberLink // members contributing each mode
}

// handleFrame runs one inbound frame (local loopback datagram or injected member
// voice) through the router and dispatches the emissions: local modes to their
// loopback sockets, remote (member) modes back over the peer link with the
// cross-peer envelope forwarded and loop-checked (§5).
func (io *busIO) handleFrame(bus *router.Bus, in inbound) {
	var f frames.Frame
	var env peer.Envelope
	if in.frame != nil {
		f = *in.frame
		env = *in.env // a member-origin frame carries its own envelope
	} else {
		var err error
		f, err = parseFrame(in.mode, in.data)
		if err != nil {
			return // hostile/short/unsupported UDP payload: drop, never crash
		}
		env = peer.NewEnvelope(io.node, string(in.mode), io.busID) // entered the cluster here
	}

	switch f.Kind {
	case frames.KindHeader:
		log.Printf("inbound %s header src=%d dst=%d stream=%08x", in.mode, f.SrcID, f.DstID, f.Stream.ID)
	case frames.KindTerminator:
		log.Printf("inbound %s terminator src=%d dst=%d stream=%08x", in.mode, f.SrcID, f.DstID, f.Stream.ID)
	}

	for _, em := range bus.Ingest(in.mode, f, time.Now()) {
		if io.remoteModes[em.Dst] {
			io.emitToMembers(em, env)
			continue
		}
		out, err := constructFrame(em.Dst, em.Frame, io.params[em.Dst], io.resolver)
		if err != nil {
			log.Printf("construct %s: %v", em.Dst, err)
			continue
		}
		if ep := io.eps[em.Dst]; ep != nil {
			if err := ep.send(out); err != nil {
				log.Printf("send %s: %v", em.Dst, err)
			}
		}
	}
}

// emitToMembers sends a router emission for a member mode to each member of that
// mode, honouring §5 loop prevention: never re-emit toward the frame's origin
// node+attachment, and increment the hop count for the link it crosses.
func (io *busIO) emitToMembers(em router.Emission, env peer.Envelope) {
	for _, ml := range io.byMode[em.Dst] {
		if !peer.ShouldEmitTo(env, ml.node, ml.attachment) {
			continue
		}
		ml.sess.Send(peer.Message{Type: peer.MsgVoice, Voice: &peer.Voice{
			Env: peer.Forward(env), Frame: em.Frame,
		}})
	}
}

// answerToken arbitrates a member key-up against the owner's single router token:
// granted iff the token is free or already held by this member's mode, else
// denied (the member drops its local voice and shows busy). The router remains the
// authority — an injected frame is still arbitrated on arrival.
func (io *busIO) answerToken(bus *router.Bus, tr tokenReq) {
	mem := io.memberByNode(tr.node)
	if mem == nil {
		return
	}
	holder, holding := bus.Holder()
	grant := !holding || holder == mem.mode
	msg := peer.Message{Token: &peer.Token{BusID: io.busID, StreamID: tr.streamID}}
	if grant {
		msg.Type = peer.MsgTokenGrant
	} else {
		msg.Type = peer.MsgTokenDeny
	}
	tr.sess.Send(msg)
}

func (io *busIO) memberByNode(node string) *memberLink {
	for _, mls := range io.byMode {
		for _, ml := range mls {
			if ml.node == node {
				return ml
			}
		}
	}
	return nil
}

func (io *busIO) removeMember(node string) {
	for mode, mls := range io.byMode {
		kept := mls[:0]
		for _, ml := range mls {
			if ml.node != node {
				kept = append(kept, ml)
			}
		}
		io.byMode[mode] = kept
	}
	log.Printf("peering: member %s disconnected", node)
}

// ctlJoin/ctlLeave/ctlToken return the control channels, or nil when the bus is
// not peered — a nil channel blocks forever in select, so the run loop's peering
// cases are simply inert on a local-only bus.
func ctlJoin(c *ownerCtl) chan *memberLink {
	if c == nil {
		return nil
	}
	return c.joinCh
}
func ctlLeave(c *ownerCtl) chan string {
	if c == nil {
		return nil
	}
	return c.leaveCh
}
func ctlToken(c *ownerCtl) chan tokenReq {
	if c == nil {
		return nil
	}
	return c.tokenCh
}

// memberAttachment builds the config.Attachment view of a rendered member row so
// the router resolves its mode + translation params through the shared path.
func memberAttachment(m config.BusPeerMember) config.Attachment {
	return config.Attachment{
		Mode: m.Mode, Slot: m.Slot, DefaultTG: m.DefaultTG, TGMap: m.TGMap,
		Target: m.Target, WiresXPassthrough: m.WiresXPassthrough,
		ID: m.ID, TG: m.TG, DefaultID: m.DefaultID,
	}
}

// loadResolver loads the shared DMRIds.dat (the single canonical reader, RFC-0003
// §3). A missing/unreadable file is not fatal: addressing falls back to numeric
// ids, which is better than refusing to start the bus.
func loadResolver(path string) frames.Resolver {
	if path == "" {
		return nil
	}
	t, err := dmrids.Load(path)
	if err != nil {
		log.Printf("dmrids: %v (addressing falls back to numeric ids)", err)
		return nil
	}
	log.Printf("loaded %d DMR ids from %s", t.Len(), path)
	return t
}

// logEvents drains the hub and logs the bus's own events. It runs for the life of
// the process; the subscription is never cancelled because shutdown exits main.
func logEvents(h *hub.Hub) {
	ch, _, _ := h.Subscribe()
	for e := range ch {
		switch e.Type {
		case router.EventBusBusy:
			log.Printf("busy: %s dropped, bus held via %s", e.Mode, e.Source)
		case router.EventBusVoiceStart:
			log.Printf("voice start: %s %s -> %s", e.Mode, e.Source, e.Dest)
		case router.EventBusVoiceEnd:
			log.Printf("voice end: %s %s (%.1fs)", e.Mode, e.Source, e.Seconds)
		case "bus_down":
			log.Printf("bus down: %s (%s)", e.Network, e.Detail)
		}
	}
}
