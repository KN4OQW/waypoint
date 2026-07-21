package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// BusConfig is the on-disk config one waypoint-bus@<id> process reads at startup:
// a single enabled bus plus its attachments and the hang time, flattened out of
// the store so the daemon needs no store/sqlite dependency. RenderTargets emits
// one of these per enabled bus (RFC-0003 §4: "the N endpoints are rows inside
// that target's rendered config"); the daemon is a pure consumer of it.
//
// It is deliberately the same Bus/Attachment shape the store validates
// (ValidateBuses) so what renders is exactly what was validated at attach time —
// no second schema to drift. Prompt 4 ships the reader; the RenderTargets writer
// that produces the file is wired alongside the model/render work (this daemon
// does not render its own input).
type BusConfig struct {
	Bus         Bus          `json:"bus"`
	Attachments []Attachment `json:"attachments"`

	// HangTimeSeconds is the arbitration hang: the token holder keeps the bus for
	// this long after its last voice frame before another source may key up
	// (RFC-0003 §5 rule 2). Zero means use DefaultBusHangTime.
	HangTimeSeconds float64 `json:"hang_time_seconds,omitempty"`

	// Peering is the owner-side LAN-peering block (RFC-0016), present only when this
	// bus has at least one active (paired) remote attachment. Absent on a purely
	// local bus, so a bus without peering renders byte-identically to Phase 1.
	Peering *BusPeering `json:"peering,omitempty"`

	// Loopbacks is the per-mode loopback each local attachment binds, keyed by mode
	// (RFC-0003 Addendum A). Present when the hand-off gives an attachment a
	// non-stock port — a DMR attachment's reserved multiplex port. The daemon binds
	// the loopback named here in preference to the fixed per-mode default, so it
	// never claims a port MMDVM-Host or a live gateway owns. Absent ⇒ the daemon
	// uses the stock per-mode loopback (a purely displacing bus needs no override).
	Loopbacks map[string]BusLoopback `json:"loopbacks,omitempty"`
}

// DefaultBusHangTime is the fallback voice hang when the rendered config leaves
// HangTimeSeconds unset. It matches the few-seconds hang the juribeparada/MMDVM_CM
// gateways and DMRGateway apply to a network transmission (RFC-0003 §5 rule 2:
// "the same hang the CM tools expose") — long enough to bridge inter-syllable
// gaps, short enough that the bus frees quickly for the other side to reply.
const DefaultBusHangTime = 3 * time.Second

// HangTime resolves the effective hang, applying DefaultBusHangTime when unset or
// non-positive.
func (c BusConfig) HangTime() time.Duration {
	if c.HangTimeSeconds <= 0 {
		return DefaultBusHangTime
	}
	return time.Duration(c.HangTimeSeconds * float64(time.Second))
}

// Validate checks a loaded BusConfig is internally coherent and startable: the
// bus is enabled, every attachment belongs to this bus, and the attached mode set
// is a legal reframe-tier bus (reusing ValidateBuses, the same rules the store
// enforced at attach time). It does NOT re-check credentials against Networks[]
// (the daemon has no store); credential resolution already happened at save time.
func (c BusConfig) Validate() error {
	if c.Bus.ID == "" {
		return fmt.Errorf("bus config has an empty bus id")
	}
	if !c.Bus.Enabled {
		return fmt.Errorf("bus %q is disabled; no daemon should run for it", c.Bus.ID)
	}
	for _, a := range c.Attachments {
		if a.BusID != c.Bus.ID {
			return fmt.Errorf("attachment for %s belongs to bus %q, not this config's bus %q",
				modeLabel(a.Mode), a.BusID, c.Bus.ID)
		}
	}
	// Reuse the attach-time validator for the mode-set rules (§2/§5). Networks is
	// nil: a rendered config carries no credentials_ref that wasn't already
	// resolved, and every non-blank ref would spuriously fail here.
	stripped := make([]Attachment, len(c.Attachments))
	for i, a := range c.Attachments {
		a.CredentialsRef = ""
		stripped[i] = a
	}
	if err := ValidateBuses([]Bus{c.Bus}, stripped, nil); err != nil {
		return err
	}
	// A bus needs at least two endpoints to hub. For a PEERED bus (RFC-0016) the
	// second endpoint may be a member's mode contributed over the peer link, which
	// lives in the Peering block, not Attachments — so count both. A member is a
	// real bus endpoint (its voice reframes to the local modes and back), so a bus
	// with one local mode + one member is a valid two-endpoint hub.
	endpoints := len(c.Attachments)
	if c.Peering != nil {
		endpoints += len(c.Peering.Members)
	}
	if endpoints < 2 {
		return fmt.Errorf("bus %q has %d endpoint(s) (%d local + %d peer member(s)); a bus needs at least 2 to hub",
			c.Bus.ID, endpoints, len(c.Attachments), peeringMemberCount(c.Peering))
	}
	return nil
}

// peeringMemberCount is the number of member modes on a bus's peering block (0
// when unpeered) — a small helper so the error message is precise.
func peeringMemberCount(bp *BusPeering) int {
	if bp == nil {
		return 0
	}
	return len(bp.Members)
}

// ReadBusConfig loads and validates a rendered bus config file. This is the
// reader the daemon imports (RFC-0003 Prompt 4): parse -> Validate -> a config
// guaranteed startable, or a descriptive error and no daemon.
func ReadBusConfig(path string) (BusConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return BusConfig{}, err
	}
	var c BusConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return BusConfig{}, fmt.Errorf("parse bus config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return BusConfig{}, fmt.Errorf("invalid bus config %s: %w", path, err)
	}
	return c, nil
}

// Marshal renders a BusConfig to the canonical JSON form ReadBusConfig accepts.
// It exists so the render/test side can produce fixtures against the same schema
// the daemon reads (round-trip parity, RFC-0003 §6.2).
func (c BusConfig) Marshal() ([]byte, error) {
	return json.MarshalIndent(c, "", "  ")
}
