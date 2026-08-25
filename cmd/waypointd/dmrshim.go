package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/dmrshim"
	"github.com/KN4OQW/waypoint/internal/hub"
	"github.com/KN4OQW/waypoint/internal/status"
)

// The DMR loopback relay's lifecycle inside waypointd.
//
// The relay is a pair of UDP sockets between MMDVM-Host and DMRGateway
// (internal/dmrshim). It is opt-in, its ports come from the store, and both of
// those can change under an Apply — so it is reconciled on a tick rather than
// started once at boot. That also makes it self-healing: a bind that failed
// because the previous relay had not released the port yet is retried a few
// seconds later instead of leaving DMR wired to nothing until the next restart.
//
// # What it reports
//
// The relay appears in the status surface as an ordinary link, using the
// three-state read the link field already carries:
//
//   - up      — running, forwarding, and seeing everything.
//   - unknown — forwarding, but observation has lost something. DMR is fine and
//     a message may have been missed. That is exactly what "unknown" is for, and
//     painting it green or red would both be lies.
//   - down    — asked for and not running, with the bind error as the detail.
//
// A relay that is switched OFF reports nothing at all. An operator who has not
// asked for it should not have a row on their dashboard about it.

// dmrRelayName is the link name the relay reports under.
const dmrRelayName = "DMR Message Relay"

// dmrRelayInterval is how often the desired wiring is compared with the live one.
// The relay changes only on an Apply, so this is a safety net rather than a poll;
// fifteen seconds keeps a failed bind retrying without spending anything.
const dmrRelayInterval = 15 * time.Second

// dmrRelay holds the live relay, if there is one, and the wiring it was built
// from. Everything is behind the mutex: the reconcile loop writes it and the
// message features (later prompts) read it to attach a tap or inject a frame.
type dmrRelay struct {
	mu      sync.Mutex
	shim    *dmrshim.Shim
	cancel  context.CancelFunc
	wiring  config.DMRShim
	lastErr string
}

// shimOrNil returns the live relay, or nil when it is switched off or failed to
// start. Callers must handle nil: a node with the relay off is a supported
// configuration, not a broken one.
func (r *dmrRelay) shimOrNil() *dmrshim.Shim {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shim
}

// stop tears the live relay down and waits for nothing: Run returns on its own
// once the context is cancelled and the sockets are closed.
func (r *dmrRelay) stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopLocked()
}

func (r *dmrRelay) stopLocked() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
	if r.shim != nil {
		r.shim.Close()
		r.shim = nil
	}
	r.wiring = config.DMRShim{}
}

// reconcile brings the live relay in line with want, reporting the state to
// publish. It returns ("", "") when the relay is switched off and there is
// nothing to say.
func (r *dmrRelay) reconcile(ctx context.Context, want config.DMRShim) (state, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if !want.Enabled {
		if r.shim != nil {
			log.Printf("dmr relay: switched off; the DMR loopback is direct again")
		}
		r.stopLocked()
		r.lastErr = ""
		return "", ""
	}

	// A port change is a rebuild. Rewiring a live socket is not a thing UDP offers,
	// and the rendered INIs have already moved, so the old relay is bound to ports
	// neither daemon is speaking to any more.
	if r.shim != nil && r.wiring != want {
		log.Printf("dmr relay: wiring changed; rebuilding on %s / %s", want.HostBind, want.GatewayBind)
		r.stopLocked()
	}

	if r.shim == nil {
		sh, err := dmrshim.New(dmrshim.Config{
			HostBind:    want.HostBind,
			HostPeer:    want.HostPeer,
			GatewayBind: want.GatewayBind,
			GatewayPeer: want.GatewayPeer,
		})
		if err != nil {
			// Say it once per distinct failure. A port held by something else would
			// otherwise fill the journal at four lines a minute forever.
			if msg := err.Error(); msg != r.lastErr {
				r.lastErr = msg
				log.Printf("dmr relay: %v", err)
			}
			return statusDown, fmt.Sprintf("not running: %v", err)
		}
		r.lastErr = ""
		runCtx, cancel := context.WithCancel(ctx)
		r.shim, r.cancel, r.wiring = sh, cancel, want
		go func() { _ = sh.Run(runCtx) }()
		log.Printf("dmr relay: relaying %s <-> %s (MMDVM-Host %s, DMRGateway %s)",
			want.HostBind, want.GatewayBind, want.HostPeer, want.GatewayPeer)
	}

	if degraded, why := r.shim.Degraded(); degraded {
		return statusUnknown, why
	}
	st := r.shim.Stats()
	return statusUp, fmt.Sprintf("relaying (%d to gateway, %d to host, %d injected)",
		st.ForwardedToGateway, st.ForwardedToHost, st.Injected)
}

// Link states, named here so the event producer and the status package cannot
// drift apart on a string literal.
const (
	statusUp      = "up"
	statusDown    = "down"
	statusUnknown = "unknown"
)

// runDMRRelay reconciles the relay until ctx is cancelled, publishing a link
// event whenever the state or the detail changes.
//
// Only on change: the detail carries live counters, and emitting them every
// fifteen seconds would turn a quiet node's event history into a flood of
// "relaying (0, 0, 0)".
func (s *server) runDMRRelay(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = dmrRelayInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	defer s.relay.stop()

	var lastState, lastDetail string
	step := func() {
		m, err := config.Load(s.store)
		if err != nil {
			return // a store we cannot read is the store layer's problem to report
		}
		state, detail := s.relay.reconcile(ctx, m.DMRShim())

		// Talker Alias injection rides the same tick, immediately after, so it is
		// handed the shim reconcile has just built or kept — and so a template
		// change takes effect on the next tick rather than at the next restart.
		s.talkerAlias.reconcile(s.relay.shimOrNil(), talkerAliasTemplate(m), s.identity,
			m.TalkerAliasAnnouncedSourceIDs())

		if state == "" {
			// Switched off. Retract the row rather than leaving a stale verdict on
			// the dashboard, and only if one was ever published.
			if lastState != "" {
				s.publishRelay(status.TypeLinkRemoved, "", "")
				lastState, lastDetail = "", ""
			}
			return
		}
		// The "up" detail carries live counters, so it changes on every cycle. Only
		// a state change is worth an event there; for the states that mean something
		// is wrong, a changed message is worth one too.
		if state == lastState && (state == statusUp || detail == lastDetail) {
			return
		}
		typ := status.TypeLinkUp
		if state == statusDown {
			typ = status.TypeLinkDown
		}
		s.publishRelay(typ, state, detail)
		lastState, lastDetail = state, detail
	}

	step()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			step()
		}
	}
}

func (s *server) publishRelay(typ, state, detail string) {
	if s.hub == nil {
		return
	}
	s.hub.Publish(hub.Event{
		Time:    time.Now().UTC(),
		Type:    typ,
		Network: dmrRelayName,
		State:   state,
		Detail:  detail,
	})
}
