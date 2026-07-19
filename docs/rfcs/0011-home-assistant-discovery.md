# RFC-0011: Home Assistant MQTT Discovery

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #9 (last-heard database + Home-Assistant-friendly MQTT topics — the **HA-discovery half**; the persistence half shipped in RFC-0004)
- Depends on: RFC-0008 (the status pipeline publishes the retained `waypoint/status/#` state topics this RFC makes HA-discoverable) and RFC-0004 (the persistent last-heard store)

## Summary

Make the hotspot appear in Home Assistant with **zero YAML**. RFC-0008 already
republishes the node's live status onto retained `waypoint/status/#` topics; this
RFC adds the **MQTT Discovery** layer on top — retained config messages under
`homeassistant/…/config` that describe each entity and point HA at the existing
state topics — plus an **availability** signal via an MQTT Last-Will so HA shows
the node offline when it drops. The result: connect a Waypoint node to the same
broker Home Assistant watches and its mode, active transmission, feed health, and
per-gateway/per-network liveness show up as entities automatically, grouped under
one device. The topic scheme is **documented and stable** (`docs/mqtt-topics.md`)
so third-party consumers can build against it without reverse-engineering.

## Motivation

Requirement #9 has two halves. The persistence half — a durable last-heard /
per-station history — shipped in RFC-0004 (the `events.db` store and
`GET /api/history`). The remaining half is its acceptance: **"HA MQTT discovery
picks up hotspot status entities with zero YAML."** Two independent WPSD→Home
Assistant monitor projects already exist, each re-implementing status scraping and
hand-written HA YAML — proof of demand and proof of the gap. Waypoint is uniquely
placed to close it cleanly because RFC-0008 already publishes a **normalized,
retained** status plane; HA discovery is a thin, well-specified publisher on top,
not a new data source.

The design constraint is HA's discovery contract, which is fixed and simple: a
retained JSON config at `<prefix>/<component>/<node>/<object>/config` naming a
`state_topic`, a `device`, and (optionally) a `value_template` and
`availability_topic`. Everything here is producing those messages correctly and
keeping them in sync as gateways/networks appear.

## Design

### The state topics it builds on (RFC-0008, unchanged)

```
waypoint/status/mode           "DMR"                 (string; retained)
waypoint/status/tx             {Transmission json}   ("" when idle; retained)
waypoint/status/feed           {connected,detail,since} (retained)
waypoint/status/network/<n>    {up,detail,since}     (retained)
waypoint/status/gateway/<n>    {up,detail,since}     (retained)
```

This RFC adds one:

```
waypoint/status/availability   "online" | "offline"  (retained; "offline" is the LWT)
```

### Availability via Last-Will

The status MQTT publisher (RFC-0008's `mqtt.Publisher`) gains a **Last-Will**:
`waypoint/status/availability` = `offline` (retained), registered before connect,
and it publishes `online` (retained) on every (re)connect. Every discovered entity
references this as its `availability_topic`, so a node that loses power or network
flips all its HA entities to *unavailable* within the broker's keepalive — the
truthful, log-free availability signal, consistent with RFC-0008's "truth, not a
latched value" principle. One availability topic covers the whole device.

### Discovery config messages

A pure function `DiscoveryConfigs(status, opts) []Discovery` returns the retained
config messages for the current status. Each message is
`{Topic, Payload}` where Topic is `<prefix>/<component>/<node>/<object>/config`
(default prefix `homeassistant`) and Payload is the HA entity JSON. All entities
carry the same **device** block so HA groups them under one hotspot:

```json
"device": {
  "identifiers": ["waypoint_<node>"],
  "name": "Waypoint <node>",
  "manufacturer": "Waypoint",
  "model": "MMDVM hotspot",
  "sw_version": "<daemon version>"
}
```

The entity set:

| Entity | Component | State topic | Notes |
|---|---|---|---|
| **Mode** | `sensor` | `…/mode` | current mode or IDLE |
| **Active transmission** | `sensor` | `…/tx` | `value_template` → `source → dest`, or "Idle" on the empty payload |
| **Feed connected** | `binary_sensor` | `…/feed` | `value_template` `{{ value_json.connected }}`, `device_class: connectivity` |
| **Gateway `<n>`** | `binary_sensor` | `…/gateway/<n>` | one per gateway, `{{ value_json.up }}`, `device_class: running` |
| **Network `<n>`** | `binary_sensor` | `…/network/<n>` | one per network/reflector, `{{ value_json.up }}`, `device_class: connectivity` |

Each config also carries a stable `unique_id` (`waypoint_<node>_<object>`), a human
`name`, and the shared `availability_topic`/`payload_available`. The static
entities (mode, tx, feed) are always present; the per-gateway/per-network entities
are emitted for whatever names the status currently holds — which is why the set
is computed from the live `Status`, not hard-coded.

### Keeping discovery in sync

Gateways and networks appear over time (the supervisor probe and MQTT link events
surface them). The discovery publisher subscribes to the status aggregator's
`OnChange` (the same seam the RFC-0008 republisher uses) and, whenever the status
changes, publishes any discovery config whose **topic it has not already
published** (tracked in a small seen-set). Config messages are **retained**, so:

- HA started *before* the node → it receives the config the moment Waypoint
  publishes it.
- HA started *after* → the retained config is delivered on HA's subscribe.
- A new gateway/network mid-run → its config publishes on the status change that
  introduces it.

Re-publishing a retained config with an identical payload is idempotent, so the
seen-set is an efficiency, not a correctness requirement. Entities are **not**
removed when a gateway/network disappears from the status (a stopped gateway
should read *unavailable/off*, not vanish); tidying stale entities on config
change is an Open question.

### Configuration

HA discovery is **on by default in live mode** — that is the "zero YAML" promise —
behind flags for the operator who wants it off or customized:

- `-ha-discovery` (default `true`): publish discovery + availability.
- `-ha-discovery-prefix` (default `homeassistant`): HA's discovery prefix.
- `-node-id` (default the OS hostname, sanitized): the device identifier and
  topic node segment; stable across restarts so HA keeps the same device.

Publishing to `homeassistant/#` only happens on the broker the node already talks
to (the local MMDVM-Host broker, or wherever `-mqtt-broker` points); it never
reaches out anywhere new. In demo mode nothing is published (no broker).

### Documentation

`docs/mqtt-topics.md` documents the full scheme — the `waypoint/status/#` state
topics, the availability topic, and the discovery layer — as a stable contract, so
the two existing WPSD→HA projects (and any new consumer) can target Waypoint
directly. The requirement asks for a *documented* scheme; this is it.

## The contract (test harness)

Automated (Go):

1. **Payload shape.** `DiscoveryConfigs` for a status with a TX, a feed, two
   gateways, and a network produces exactly the expected config topics, each a
   valid HA config JSON with the right `state_topic`, `unique_id`, `device`
   block, `availability_topic`, and (for binary sensors) `value_template` +
   `payload_on/off`.
2. **Object-id safety.** A gateway/network name with spaces or MQTT wildcards
   (`+`, `#`, `/`) is sanitized into a safe object-id and topic segment, matching
   the state-topic sanitization RFC-0008 already applies.
3. **Idle transmission.** The TX sensor's `value_template` renders "Idle" for the
   empty retained payload and `source → dest` for a real one (asserted by
   inspecting the template string; HA evaluates it, but the branch is explicit).
4. **Sync/seen-set.** Given a sequence of status changes that introduce a new
   gateway, the publisher emits that gateway's config exactly once (retained
   idempotence aside) and never drops a static entity.
5. **Availability.** The publisher registers the `offline` Last-Will on the
   availability topic and publishes `online` retained on connect (asserted at the
   client-options / publish-call level with a fake).

Manual (the #9 acceptance): with a real broker + Home Assistant, a Waypoint node's
mode, transmission, feed, gateways, and networks appear as entities under one
device with **no YAML**, and go *unavailable* when the node is stopped.

## Alternatives considered

- **Ship HA YAML / a blueprint the user pastes in.** Rejected — it is the status
  quo the two existing projects already do, and the acceptance is explicitly
  *zero YAML*. Discovery is the modern, maintenance-free path.
- **A separate HA-only publisher process / add-on.** Rejected — discovery is a few
  retained messages; a whole add-on (the incumbent pattern) is disproportionate.
  Waypoint publishes its own discovery from the daemon that already owns the
  status plane.
- **Bake discovery into the RFC-0008 republisher.** Rejected as *coupling* —
  republishing state and describing entities are separate concerns with separate
  toggles (an operator may want the state topics without HA discovery). They share
  the one MQTT publisher/connection but are distinct publishers over it.
- **Per-entity availability topics.** Rejected — one device-level availability via
  the connection Last-Will is simpler and matches how a hotspot actually fails
  (the whole node drops, not one entity). Per-entity liveness is already the
  gateway/network binary sensors' *state*.
- **Derive `node-id` from a random UUID.** Rejected — a stable id (hostname) keeps
  HA's device/entity ids constant across restarts; a UUID that changes on reflash
  would orphan the old device in HA.

## Open questions

1. **Stale-entity cleanup.** When a gateway/network is removed from the config
   (not just stopped), its retained discovery config lingers and HA keeps a
   now-meaningless entity. Publishing an empty retained payload to its config
   topic removes it in HA; wiring that to a genuine removal signal (vs. a
   transient absence) is deferred — v1 leaves entities in place (they read
   unavailable/off), which is the safe default.
2. **Last-heard as HA entities.** #9 pairs last-heard with HA. This RFC exposes
   *current* status; surfacing the persistent last-heard (RFC-0004) as HA sensors
   (e.g. "last heard callsign") is a natural follow-up that publishes from the
   same store, out of scope here to keep the discovery contract tight.
3. **Node-id source.** Default is the hostname; using the store's `device_id`
   (RFC-0001/0002 meta) would tie the HA device to the claimed identity. Deferred
   until the device-id surface is finalized; the flag lets an operator pin it now.
4. **Discovery prefix collisions.** If two Waypoint nodes share a broker, distinct
   `-node-id`s keep their devices separate; documented, not enforced.
