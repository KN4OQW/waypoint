package main

import (
	"context"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/KN4OQW/waypoint/internal/config"
	"github.com/KN4OQW/waypoint/internal/mqtt"
	"github.com/KN4OQW/waypoint/internal/netwatch"
	"github.com/KN4OQW/waypoint/internal/supervisor"
)

// supervisor.go wires the network-resilience supervisor (#22) to the rest of the
// daemon. Every signal it consumes already exists — the liveness probe's unit
// state, the aggregator's transmission state, netwatch's route check — so this is
// assembly, not new plumbing. The judgement is in internal/supervisor.

// supervisorOptions are the operator-facing knobs, all with defaults nobody should
// need to touch.
type supervisorOptions struct {
	Interval      time.Duration
	Remediate     bool
	MaxRestarts   int
	RestartWindow time.Duration
	// Commander asks the gateway daemons about their own links. Nil leaves the
	// supervisor on announcements and probes alone — slower to notice a link that
	// went away quietly, but not wrong.
	Commander *mqtt.Commander
}

// dmrLinkState asks DMRGateway which of its masters are still connected, and maps
// its positional answer onto network names.
//
// The daemon replies "xlx:n/a net1:conn net2:disc" with no names in it, so the
// slots are matched against the order the renderer wrote the sections in
// (config.DMRNetworkOrder, which is derived beside the renderer for exactly this
// reason). A reply naming more slots than the model knows about means the running
// daemon is working from an older config than the store holds — mid-apply, most
// likely — so the extra slots are ignored rather than guessed at.
func (s *server) dmrLinkState(cmd *mqtt.Commander) map[string]supervisor.Tri {
	m, err := config.Load(s.store)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reply, ok := cmd.Ask(ctx, config.MQTTNameDMRGateway, "status")
	if !ok {
		return nil // no answer is no news
	}
	slots := supervisor.ParseDMRGatewayStatusReply(reply)
	names := m.DMRNetworkOrder()
	out := make(map[string]supervisor.Tri, len(slots))
	for i, state := range slots {
		if i < len(names) {
			out[names[i]] = state
		}
	}
	return out
}

// runSupervisor starts the supervisor for live mode. Remediate arms restarts; with
// it off the supervisor still probes, still publishes honest link state, and logs
// what it would have done, which is the whole detection path exercised on a real
// node with nothing acted upon.
func (s *server) runSupervisor(ctx context.Context, opts supervisorOptions) {
	sup := &supervisor.Supervisor{
		Interval:      opts.Interval,
		Remediate:     opts.Remediate,
		MaxRestarts:   opts.MaxRestarts,
		RestartWindow: opts.RestartWindow,
		Hub:           s.hub,
		Prober:        &supervisor.Prober{},
		// Jitter from the shared source: nodes that fail together must not retry
		// together. Seeded per-process by the runtime, so two hotspots on one ISP
		// do not share a schedule.
		Policy: supervisor.DefaultPolicy(rand.Float64),
		Signals: supervisor.Signals{
			Attachments: s.supervisedAttachments,
			// The kernel's route table, excluding the setup AP's own interface —
			// the same question netwatch asks, and for the same reason: a probe
			// against a third party's host would make their outage look like ours.
			WANUp: func() bool {
				return netwatch.HasDefaultRoute(netwatch.RoutePath, s.setupAPIface)
			},
			TXActive:   func() bool { return s.agg != nil && s.agg.Snapshot().TX != nil },
			UnitActive: s.unitLiveness,
			LinkState: func() map[string]supervisor.Tri {
				if opts.Commander == nil {
					return nil
				}
				return s.dmrLinkState(opts.Commander)
			},
			Restart: func(unit string) error {
				out, err := systemctlRun("restart", unit)
				if err != nil {
					return errWithOutput(err, out)
				}
				return nil
			},
		},
	}
	sup.Run(ctx)
}

// supervisedAttachments re-derives the supervised set from the store on every
// tick, so adding or removing a network takes effect at the next evaluation
// rather than at the next daemon restart.
func (s *server) supervisedAttachments() []supervisor.Attachment {
	m, err := config.Load(s.store)
	if err != nil {
		log.Printf("supervisor: cannot read the config store: %v", err)
		return nil
	}
	return supervisor.Attachments(m)
}

// unitLiveness reads a unit's state from the status aggregator rather than
// shelling out again: the liveness probe already polls systemd every second and
// files the answer under the same key. Unknown until that probe has reached the
// unit — a gateway nobody has looked at yet is not a dead one.
func (s *server) unitLiveness(unit string) supervisor.Tri {
	if s.agg == nil {
		return supervisor.TriUnknown
	}
	l, ok := s.agg.Snapshot().Gateways[friendlyUnit(unit)]
	if !ok {
		return supervisor.TriUnknown
	}
	if l.Up {
		return supervisor.TriYes
	}
	return supervisor.TriNo
}

func errWithOutput(err error, out []byte) error {
	if len(strings.TrimSpace(string(out))) == 0 {
		return err
	}
	return &unitError{err: err, out: strings.TrimSpace(string(out))}
}

type unitError struct {
	err error
	out string
}

func (e *unitError) Error() string { return e.err.Error() + ": " + e.out }
func (e *unitError) Unwrap() error { return e.err }
