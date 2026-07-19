# RFC-0008: The MQTT-Native Status Pipeline

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #5 (MQTT-native status pipeline — no log scraping, ever)
- Depends on: RFC-0004 (the event hub is the seam every status consumer subscribes to), the existing MQTT consumer (`internal/mqtt`, MMDVM-Host's `<name>/json` data plane)

## Summary

Waypoint derives **all** live status from structured events — never from log
scraping. Today the hub carries a stream of events (voice start/end, link, mode)
fed only by the MQTT consumer, and the dashboard rebuilds its own view from that
stream in browser JS. This RFC adds the missing middle: a **status aggregator**
that folds the event stream into an authoritative, self-healing `Status` model;
exposes it as `GET /api/status` and over a **WebSocket**; and republishes it,
normalized, onto retained **`waypoint/status/#`** MQTT topics for Home Assistant
and other consumers. The dashboard becomes a consumer of that public API — there
are no private endpoints.

The design's load-bearing idea is **self-healing by watchdog**. Every "dashboard
lies" bug in the incumbents (TX timer counts forever — Pi-Star #117; stuck "M17
Listening" — #155; "Not Linked" while linked — #156/#171; green on a rejected DMR
login — #89) has the same root cause: a UI state that was *entered* by one log
line and needed a *second* log line to leave, and the second line never came (the
daemon died, the log rotated, the regex missed). The aggregator never depends on a
closing event arriving: a transmission that is not refreshed **expires on a
timer**, and a gateway's liveness is **probed**, not inferred from a message that
may never be sent. Truth is a function of time, not of a hoped-for event.

The acceptance (#5): kill or restart any gateway while watching the dashboard and
status reflects the truth within 2 s, from structured events only — provable
because there is no log-file reader anywhere in the pipeline to disable.

## Motivation

Requirement #5 is a P0 and its "why" is a catalogue of incumbent
`dashboard-lies` bugs, all tracing to log parsing. The incumbents' status is a
regex over a rolling text log: it is racy (a line can be read half-written),
lossy (rotation drops the closing line), and brittle (a daemon version bumps its
log format and the parser silently breaks). The failure mode is always a *stuck*
state — the dashboard latches "transmitting" or "linked" and never clears,
because clearing required a specific log line that never arrived.

Waypoint already made the first half of the fix: the status source is
MMDVM-Host's MQTT JSON data plane (`internal/mqtt`), not a log. But an *event
stream* is not *status*: the dashboard currently folds the stream into state in
client JavaScript, which means (a) every browser recomputes it, (b) there is no
server-side truth to serve to a non-browser consumer (Home Assistant, #123/#141's
begged-for API), and (c) nothing expires a stuck state — a missed `end` event
strands the client's "transmitting" exactly like the incumbents' missed log line.
This RFC puts the fold on the server, makes it self-healing, and publishes the
result.

## Design

### The `Status` model

A single `Status` value is the node's authoritative live state, held by the
aggregator and served verbatim:

```go
type Status struct {
    Mode     string             // active mode, or "IDLE"
    TX       *Transmission      // the current keyed-up transmission, or nil when idle
    Networks map[string]Link    // per-network/reflector link state
    Feed     Feed               // the MMDVM-Host MQTT feed itself (connected?)
    UpdatedAt time.Time
}
type Transmission struct {
    Mode, Slotmode, Source, Dest, Network string
    Direction string   // "rf" | "network"
    StartedAt time.Time
    expiresAt time.Time // watchdog deadline (not serialized)
}
type Link struct { Up bool; Detail string; Since time.Time }
type Feed struct { Connected bool; Since time.Time; Detail string }
```

Everything in `Status` is derived; nothing is a secret (no passwords transit the
status plane), so the whole value is safe to serve and to publish.

### The aggregator: a pure fold + a watchdog

`internal/status` provides an `Aggregator` that subscribes to the hub (the same
seam the SSE handler and the RFC-0004 persister use) and maintains the `Status`:

- **Pure fold.** `apply(status, event) status` is a pure function — every hub
  event type maps to a state transition (a `rf_voice_start` sets `TX` and
  `Mode`; a `*_voice_end` clears them; a `link` sets a `Networks[name]`; a `mode`
  event sets `Mode`). Purity makes the whole state machine table-testable with no
  clock and no I/O.
- **Watchdog self-heal.** `TX.expiresAt` is set on every start/refresh to
  `now + txTTL`. A ticker (≈1 s) calls `expire(now)`: any `TX` past its deadline
  is cleared to idle **as if a `lost` event had arrived**, and the cleared state
  is emitted. `txTTL` is the mode's transmit-timeout ceiling plus a margin (a real
  transmission cannot outlive the modem's own timeout), so a *stranded* TX — the
  daemon died mid-transmission, no `end` will ever come — self-clears within one
  tick of its deadline instead of counting forever (#117/#155 fixed by
  construction, not by hoping for a closing event).
- **Emit on change.** When a fold or an expiry changes the `Status`, the
  aggregator publishes a `status` snapshot (to the hub for the stream, and to the
  republisher for MQTT). Unchanged events don't churn the topics.

The aggregator holds the only mutable copy; readers get a value copy under a
mutex, so `GET /api/status` and the WebSocket never race the fold.

### Gateway liveness: probed, not inferred

"Kill/restart any gateway → truth within 2 s" is about **link/liveness**, and the
honest, log-free source for *is this gateway process alive* is the supervisor that
already owns the systemd units (architecture.md), not a message the dying daemon
might not send. A **liveness probe** polls `systemctl is-active` for the gateway
units on a sub-2 s cadence (the same `systemctlRun` seam the apply path uses) and
emits a `link`-class event when a unit transitions active↔inactive. So a gateway
that is killed shows **not running** within one poll (< 2 s), and one that is
restarted shows running again — both from structured supervisor state, zero log
reads. This is a distinct, truthful signal from network-link state: "gateway not
running" and "gateway running but not linked" are different rows, and the model
carries both. Per-reflector *link* truth (linked to which room) is filled by the
gateway/MMDVM-Host MQTT link topics as the May-2026 data plane exposes them — the
aggregator already folds `link` events, so that is data, not new plumbing.

The probe cadence is a flag (default 1 s) so the 2 s acceptance has margin and an
operator on constrained hardware can relax it.

### `waypoint/status/#` — the normalized republish

A republisher (a hub subscriber, live mode only — it needs the broker) publishes
each status change to **retained** MQTT topics under `waypoint/status/`:

```
waypoint/status/mode          "DMR"        (retained; last mode)
waypoint/status/tx            {json}       (the Transmission, or "" when idle)
waypoint/status/network/<n>   {json Link}  (per network/reflector)
waypoint/status/gateway/<u>   "active"     (per gateway unit liveness)
waypoint/status/feed          {json Feed}
```

Retained so a Home Assistant that (re)starts reads current state immediately with
zero YAML — the requirement's HA-friendliness, and the substrate the #9
MQTT-discovery follow-up publishes *on top of* (it adds discovery config topics;
this RFC owns the state topics). The topic prefix is a flag. Republishing is
best-effort: a broker hiccup never blocks the aggregator (it is downstream of the
hub, like the persister).

### API: snapshot + WebSocket

- **`GET /api/status`** returns the current `Status` as JSON — the server-side
  truth any consumer polls or renders. Behind the session wall like every route;
  no secret is ever in `Status`.
- **`GET /api/ws`** is a WebSocket (gorilla/websocket, already a transitive dep)
  that, on connect, sends the current `Status` then streams every subsequent hub
  event and `status` snapshot as JSON frames — the bidirectional-capable transport
  the architecture names, so a client gets both the live event tail and the
  derived status over one socket. The existing SSE `/api/events` stays for
  backward compatibility (the dashboard migrates incrementally); both are pure
  hub subscribers, so neither is privileged.

### No log scraping — structurally, not by policy

The acceptance says "verified by disabling all log file reads." Waypoint passes
this trivially: **there is no log-file reader in the status path.** Status flows
`MQTT consumer → hub → aggregator → API/republish` and `supervisor probe → hub`.
No component opens `/var/log`. A CI grep-guard asserts the status packages import
no file-log reader, so the property can't regress silently.

## The status contract (test harness)

CI enforces these as release-blocking properties:

1. **Fold correctness.** Table-driven over every event type → the exact `Status`
   transition (start sets TX+mode; end clears to idle; link toggles a network;
   mode sets mode). Pure, no clock.
2. **Self-heal (the #117/#155 fix).** A `rf_voice_start` with no matching end,
   advanced past `txTTL`, expires to idle on the next `expire(now)` — asserted with
   an injected clock, so "the timer never counts forever" is a test, not a hope.
3. **No false expiry.** A transmission refreshed within `txTTL` is not expired; a
   normal start→end never trips the watchdog.
4. **Liveness within the window (the #5 acceptance).** With a faked `systemctl`
   flipping a unit to inactive, the probe emits gateway-down and `Status` reflects
   it within one poll interval; flipping back emits gateway-up. (Faked systemctl,
   like the existing apply tests.)
5. **Snapshot + WS shape.** `GET /api/status` returns the aggregator's value
   byte-stably; a WebSocket client receives an initial `Status` frame then live
   frames; no secret field exists in the serialized form.
6. **Republish topic scheme.** A status change publishes the specified retained
   topics with the specified payloads (asserted against a fake publisher), and an
   idle→idle no-op publishes nothing.
7. **No-log-scraping guard.** The status packages reference no log-file reader
   (grep-guard test), so the "structured events only" property is enforced.

## Alternatives considered

- **Keep folding status in client JS (status quo).** Rejected — it is three of the
  bug's four consequences (every browser recomputes, no server truth for HA/API,
  no expiry so a missed event strands the client). Status belongs on the server,
  once, self-healing.
- **Infer gateway death from an MQTT "goodbye" / last-will.** Attractive but
  fragile: a hard-killed daemon may never send a will, and not every gateway
  publishes one. The supervisor already knows the unit state authoritatively; the
  probe is the honest source. (An MQTT last-will, when a daemon sends one, is just
  another `link` event the fold consumes — additive, not the foundation.)
- **A longer TX watchdog tied to a real end event only.** Rejected — that is the
  incumbent bug. The watchdog must be time-bounded independent of any closing
  event; `txTTL` = the modem's own timeout ceiling makes a stranded TX
  indistinguishable-in-outcome from a clean one.
- **SSE only (no WebSocket).** SSE satisfies the streaming need and stays, but #5
  and architecture.md both name WebSocket, and it is a zero-new-dependency add
  (gorilla/websocket is already transitive), so the socket ships now and the
  bidirectional door is open.
- **A TSDB / external status store.** Rejected for the same Pi-Zero reasons as
  RFC-0004: status is a small in-memory value republished to MQTT; no second
  daemon.

## Open questions

1. **Per-reflector link truth.** The aggregator folds `link` events today; the
   richness of per-network "linked to room X" depends on the gateway/MMDVM-Host
   MQTT link topics the May-2026 data plane exposes. As those land they are data
   the fold already accepts — this RFC does not block on them, and the liveness
   probe covers the kill/restart acceptance in the meantime.
2. **`txTTL` per mode.** A single ceiling (the max modem timeout + margin) is
   simplest and safe. A per-mode TTL (DMR slot timeout vs FM timeout) is a refinement
   if a mode's ceiling proves too loose; deferred.
3. **WebSocket auth for non-browser clients.** The socket is behind the session
   wall today (cookie). A bearer-token path for headless API clients is the #123/#141
   "real API" follow-up, tracked with the token-auth work (RFC-0002 lineage).
4. **Republish QoS/retain tuning.** Retained + QoS 0 fits a single-broker LAN. If a
   HA-over-flaky-link case wants QoS 1, it is a publisher flag; not needed for the
   local broker the hotspot runs.
