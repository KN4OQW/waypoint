// waypoint-bus is the per-bus hub process RFC-0003's waypoint-bus@<id>.service
// runs. It reads one rendered bus config, binds each attached mode's fixed
// loopback endpoint, and fans voice frames between the attachments through the
// pure frame/router layer — enforcing §5's four loop-prevention rules. It holds
// no credentials and does no transcoding: the AMBE+2 family reframes (RFC-0003
// §2), so there is no vocoder here under any circumstances.
//
// It is a thin I/O shell around internal/bus/router (the tested state machine):
// this file only moves bytes between UDP sockets and the router, brackets the
// lifecycle, and logs. All the loop-prevention logic lives in the router and is
// unit-tested without sockets.
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
	nodeID := flag.String("node", "", "this node's peering id (RFC-0016; used as the frame envelope origin for loop prevention)")
	flag.Parse()

	// systemd/journald stamps its own timestamps; keep our lines clean and prefixed.
	log.SetFlags(0)
	if *cfgPath == "" {
		log.Fatal("waypoint-bus: -config is required")
	}

	bc, err := config.ReadBusConfig(*cfgPath)
	if err != nil {
		log.Fatalf("waypoint-bus: %v", err)
	}
	log.SetPrefix(fmt.Sprintf("waypoint-bus[%s]: ", bc.Bus.ID))

	rcfg, err := router.FromBusConfig(bc)
	if err != nil {
		log.Fatalf("resolve config: %v", err)
	}

	resolver := loadResolver(*dmridsPath)

	// The bus emits ordinary hub events (RFC-0004): here we subscribe and log them,
	// which is also the daemon's inbound-traffic visibility for the bench smoke.
	h := hub.New()
	go logEvents(h)
	bus := router.New(rcfg, h)

	paramsByMode := make(map[config.Mode]frames.Params, len(rcfg.Attachments))
	for _, a := range rcfg.Attachments {
		paramsByMode[a.Mode] = a.Params
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	frameCh := make(chan inbound, 256)
	eps := make(map[config.Mode]*endpoint, len(rcfg.Attachments))
	for _, a := range rcfg.Attachments {
		lb, err := loopbackFor(a.Mode)
		if err != nil {
			log.Fatalf("%v", err)
		}
		ep, err := openEndpoint(a.Mode, lb)
		if err != nil {
			log.Fatalf("open %s loopback: %v", a.Mode, err)
		}
		eps[a.Mode] = ep
		go ep.recv(ctx, frameCh)
		log.Printf("attached %s: listen 127.0.0.1:%d, peer 127.0.0.1:%d", a.Mode, lb.bind, lb.peer)
	}

	// RFC-0016 LAN peering: if the rendered config carries a peering block, this
	// bus is a peered owner — start the pinned mTLS listener + token protocol. A
	// peering failure is logged, never fatal: the local bus keeps running.
	if bc.Peering != nil {
		if err := startOwnerPeering(ctx, bc.Peering, bc.Bus.ID, *nodeID, h, rcfg.HangTime); err != nil {
			log.Printf("peering: not started: %v", err)
		}
	}

	ticker := time.NewTicker(releaseTick)
	defer ticker.Stop()
	log.Printf("bus %q up: %d attachments, hang %s", bc.Bus.Name, len(eps), rcfg.HangTime)

	for {
		select {
		case <-ctx.Done():
			log.Printf("shutdown: releasing token, closing %d sockets", len(eps))
			bus.ForceRelease(time.Now())
			for _, ep := range eps {
				ep.close()
			}
			if d := bus.Dropped(); d > 0 {
				log.Printf("dropped %d frames to arbitration over this run", d)
			}
			return

		case <-ticker.C:
			bus.MaybeRelease(time.Now())

		case in := <-frameCh:
			handleFrame(bus, eps, paramsByMode, resolver, in)
		}
	}
}

// handleFrame parses one inbound datagram, runs it through the router, and emits
// the results to the destination sockets. A malformed packet is dropped.
func handleFrame(bus *router.Bus, eps map[config.Mode]*endpoint, params map[config.Mode]frames.Params, r frames.Resolver, in inbound) {
	f, err := parseFrame(in.mode, in.data)
	if err != nil {
		return // hostile/short/unsupported UDP payload: drop, never crash
	}
	// Per-transmission visibility for the smoke test and the field: log the header
	// and terminator, not every 20 ms voice frame.
	switch f.Kind {
	case frames.KindHeader:
		log.Printf("inbound %s header src=%d dst=%d stream=%08x", in.mode, f.SrcID, f.DstID, f.Stream.ID)
	case frames.KindTerminator:
		log.Printf("inbound %s terminator src=%d dst=%d stream=%08x", in.mode, f.SrcID, f.DstID, f.Stream.ID)
	}

	for _, em := range bus.Ingest(in.mode, f, time.Now()) {
		out, err := constructFrame(em.Dst, em.Frame, params[em.Dst], r)
		if err != nil {
			log.Printf("construct %s: %v", em.Dst, err)
			continue
		}
		if ep := eps[em.Dst]; ep != nil {
			if err := ep.send(out); err != nil {
				log.Printf("send %s: %v", em.Dst, err)
			}
		}
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
		}
	}
}
