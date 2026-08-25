# Waypoint MQTT topic scheme

Waypoint publishes its live status onto a **stable, documented** set of retained
MQTT topics, and describes them to Home Assistant via MQTT Discovery so entities
appear with **zero YAML**. This document is the contract third-party consumers can
build against.

- Status pipeline & topic design: [RFC-0008](https://github.com/KN4OQW/waypoint/discussions/163)
- Home Assistant discovery: [RFC-0011](https://github.com/KN4OQW/waypoint/discussions/166)

All topics below are **retained** (a late-joining subscriber reads current state
immediately) and are published only in live mode (a `-demo` node has no broker).
The status prefix defaults to `waypoint/status`.

## Where the prefixes are set

The three topic roots on this page — the modem's `Name`, the status prefix, and
the bus prefix — plus the broker address and credentials are **store-owned**
(issue #29). Edit them in the settings UI under **Admin → System**; Apply rewrites
every affected config, restarts the daemons whose file changed, and moves
waypointd's own consumer and status republisher onto the new topics in the same
pass, so nothing is left subscribed to the old root.

The matching command-line flags (`-mqtt-broker`, `-mqtt-name`,
`-status-topic-prefix`, `-bus-topic-prefix`, `-mqtt-user`, `-mqtt-pass`) still
exist as an **override layer**, following RFC-0005 precedence: a flag set
explicitly on the command line wins over the store and logs which store key it is
shadowing; a flag left alone takes the stored value; an absent store row takes the
compiled default. So a packaged unit that pins `-mqtt-broker` keeps working
unchanged, and an operator whose System-tab edit appears not to take effect finds
the reason in the journal rather than in a bug report.

## State topics (`waypoint/status/#`)

| Topic | Payload | Meaning |
|---|---|---|
| `waypoint/status/mode` | string, e.g. `DMR` | Current active mode, or `IDLE`. |
| `waypoint/status/tx` | JSON `{mode,slot,source,dest,network,direction,started_at}` or empty | The transmission on the air; empty payload when idle. |
| `waypoint/status/feed` | JSON `{connected,detail,since}` | Health of the MMDVM-Host MQTT feed everything derives from. |
| `waypoint/status/network/<name>` | JSON `{up,detail,since}` | Per network/reflector link state. `<name>` is topic-sanitized. `up` moves in **both** directions — see below. |
| `waypoint/status/gateway/<name>` | JSON `{up,detail,since}` | Per gateway-daemon liveness (from the supervisor probe). |
| `waypoint/status/availability` | `online` / `offline` | Device availability. `offline` is the connection's MQTT Last-Will, so it flips the moment the node drops. |

`<name>` segments have `/`, `+`, `#`, and whitespace replaced with `_` so they are
safe topic levels.

Status is **self-healing** (RFC-0008): a stranded transmission clears on a
watchdog and a killed gateway shows down within the supervisor probe interval, so
these topics reflect truth rather than a latched last value — no log scraping.

### Network links go down as well as up

`network/<name>` reports `up: false` when a link is lost, not just `up: true`
when one is made. A link stops being claimed on any of three signals:

- an explicit **`link_down`** event naming the network (its `detail` says why);
- **loss of the MMDVM-Host feed** — nothing can re-assert a link with the feed
  gone, and `detail` reads `unconfirmed — MMDVM-Host feed down`;
- the **confirmation watchdog**: a link that *was* being re-confirmed and stops
  being reports `unconfirmed for <window>` (`-link-ttl`, default 3 minutes).

The watchdog applies **only to links something is actively re-checking**. The
supervisor asks DMRGateway every cycle, so its masters are subject to it; a link
with no confirmation source — DAPNET, whose daemon isn't packaged yet — has no
deadline and keeps its last known state, because decaying it to "down" on a timer
would report a failure the timer knows nothing about. That is what lets the
watchdog default to on.

Confirming is not an observable change: `since` is when the link entered its
*current* state, so a re-confirmation neither moves `since` nor republishes the
topic. Only the *absence* of confirmation is ever visible.

A network the operator deletes or disables is **retired** rather than left at its
last verdict: `link_removed` drops it from `networks` and its retained
`network/<name>` topic is cleared with an empty payload, so a deleted network stops
being described instead of trading "still linked" for "still exists". (A Home
Assistant *discovery* config for a retired entity is a separate retained topic and
is not yet cleared — tracked with the RFC-0011 follow-up.)

The event stream carries `link_up`/`link_down`/`link_removed` for this; the original
`link` spelling still means "up" and is still accepted, because `events.db` holds
rows written before the pair existed and `GET /api/history` replays them.

## Home Assistant discovery (`homeassistant/#`)

When `-ha-discovery` is enabled (default **on** in live mode), Waypoint publishes a
retained discovery config for each entity under the HA discovery prefix
(`-ha-discovery-prefix`, default `homeassistant`):

```
<prefix>/<component>/<node>/<object>/config
```

`<node>` is the device id (`-node-id`, default the sanitized hostname). Every
entity shares one **device** so HA groups them under a single hotspot, and every
entity references `waypoint/status/availability` for availability.

| Entity | Component | State topic |
|---|---|---|
| Mode | `sensor` | `waypoint/status/mode` |
| Active transmission | `sensor` | `waypoint/status/tx` (`source → dest`, or `Idle`) |
| MMDVM feed | `binary_sensor` (`connectivity`) | `waypoint/status/feed` |
| Gateway `<name>` | `binary_sensor` (`running`) | `waypoint/status/gateway/<name>` |
| Network `<name>` | `binary_sensor` (`connectivity`) | `waypoint/status/network/<name>` |

Gateway/network entities are published as those names first appear; because the
configs are retained, Home Assistant discovers them whether it connects before or
after the node, and mid-run additions show up on the status change that
introduces them.

### Turning it off / customizing

- `-ha-discovery=false` — publish the state topics but no HA discovery.
- `-ha-discovery-prefix=<prefix>` — match a non-default HA discovery prefix.
- `-node-id=<id>` — pin the device id (keep it stable across reflashes so HA
  keeps the same device).

Discovery is published only to the broker the node already talks to (the store's
broker setting, or `-mqtt-broker` when that flag overrides it); Waypoint never
reaches out to a new host. Changing the status prefix republishes the discovery
configs under the new state topics, so HA follows without manual intervention.

## Topics Waypoint consumes

Everything above is published. Waypoint also **subscribes** to the daemons' own
planes — it is where live status comes from, and there is no log reader anywhere
in the path.

| Topic | Publisher | Used for |
|---|---|---|
| `mmdvm/json` | MMDVM-Host | Per-mode voice traffic → the event stream, last-heard, on-air. |
| `dmr-gateway/json` | DMRGateway | Its own status plane: `{"status":{"message":"Logged into DMR Network: X"}}` and the failed-login/closing counterparts, published the moment a master accepts or refuses. |
| `waypoint/bus/#` | Mode-bus daemons | Bus events, mapped 1:1 (below), plus the one control topic (`talker_alias`). |

### Supervisor events

Two event types come from the resilience supervisor and appear in the event log
and the `/api/history` record like any other:

| Type | Meaning |
|---|---|
| `link_up` / `link_down` | The supervisor's verdict about one upstream attachment, with the reason in `detail`. This is what drives `waypoint/status/network/<name>`. |
| `link_removed` | The network is no longer configured. Retires its row and clears its retained topic. |
| `supervisor_action` | Something it did, or declined to do, about a lost link — `restarted waypoint-dmrgateway.service — BM_3102: the endpoint is unreachable`. `source` is the unit. |

Actions are events rather than log lines because unattended recovery has to be
legible after the fact: an operator who was asleep when the ISP dropped should be
able to read what happened. Declining is recorded too — a supervisor that hits its
restart cap and goes quiet is otherwise indistinguishable from one that fixed the
problem.

A DMRGateway status message becomes a `gateway_status` hub event naming the
network, so it persists and shows in the event log. It is **not** folded into link
state on its own: the resilience supervisor
([#22](https://github.com/KN4OQW/waypoint/issues/22)) weighs it against the
systemd unit state and Waypoint's own endpoint probe, and publishes the resulting
`link_up`/`link_down` verdict. The message is English prose assembled upstream
with string concatenation, so a wording change would silently stop matching —
which is exactly why it is one signal of three and never the authority. Losing it
costs the supervisor its fastest detection path, not its correctness.

## Mode-bus event topics (`waypoint/bus/#`)

Mode buses (RFC-0003) run in their own `waypoint-bus@<id>` processes. The stack's
inter-process event plane is MQTT (RFC-0008), so a bus **republishes its events**
onto the local broker and waypointd's consumer ingests them as ordinary hub
events — RFC-0004 persistence, RFC-0008 status, and the dashboard bus badges then
work with no further plumbing. Third-party consumers get bus events for free.

The prefix defaults to `waypoint/bus` and is edited under **Admin → System** (see
[Where the prefixes are set](#where-the-prefixes-are-set)). Each message is one
`hub.Event` as JSON — the **same schema** the SSE/UI layer already speaks, mapped
1:1 with no translation layer.

| Topic | Payload (`hub.Event`) | Retained | Meaning |
|---|---|---|---|
| `waypoint/bus/<id>/bus_voice_start` | `{type,mode,source,dest,network,...}` | no | A transmission started on the bus. |
| `waypoint/bus/<id>/bus_voice_end` | `{type,mode,source,seconds,...}` | no | It ended (with duration). |
| `waypoint/bus/<id>/bus_busy` | `{type,mode,source,network,detail}` | no | A second source was dropped by arbitration (`source` = winning mode; `detail` = "busy: via …"). |
| `waypoint/bus/<id>/bus_down` | `{type,network,detail}` | **yes** | The bus is down (a member's owner went offline). Retained so a reconnecting consumer sees the node still down. |
| `waypoint/bus/<id>/bus_up` | `{type,network,detail}` | no | The bus recovered — **also clears** the retained `bus_down` (an empty retained publish). |
| `waypoint/bus/<id>/peer_connected` | `{type,network,source,mode}` | no | A LAN peer (member) joined the bus (RFC-0016). |
| `waypoint/bus/<id>/peer_disconnected` | `{type,network,source}` | no | A member left. |

**Retention & clear-on-silence (RFC-0008 — truth, not a stuck value).** Only the
"down" state is retained, so it survives a consumer reconnect; every other event is
a transient the moment it happens. The down-state **never latches**: it is cleared
(an empty retained publish to the `bus_down` topic) when the bus recovers
(`bus_up`), when the bus daemon shuts down cleanly, and when a bus is detached
(the RFC-0003 Addendum A apply path clears the bus's retained topics). A
reconnecting consumer therefore never sees a bus that no longer exists reported as
down.

**Best-effort, never blocking media.** The bus publishes fire-and-forget at QoS 0
from a goroutine draining its in-process hub; the hub drops onto a full subscriber
channel rather than blocking, so a broker hiccup drops events, never voice frames.
The broker address is rendered into each bus config (never hardcoded); mosquitto is
localhost-only.

### The one topic that is not an event: `talker_alias`

| Topic | Payload | Retained | Meaning |
|---|---|---|---|
| `waypoint/bus/<id>/talker_alias` | `{type,stream_id,src_id,name}` | no | Who is talking on a Zello transmission the bus is about to source, so waypointd can put the name on the receiving radio. |

This one is a **control message, not a `hub.Event`**: it carries a DMR stream id
and a DMR id, which `hub.Event` has no field for, and it is not something an
operator reads in the event log. waypointd's consumer branches on the topic before
the event mapping, so it never reaches the hub; a note arriving on any other topic
is refused by the event decoder, and an event body arriving on this one is refused
by the note decoder. It shares the bus prefix (and the bus's one connection) rather
than opening a topic root or a socket of its own.

It exists because the alias can only be injected by one process, and it is not the
one that knows the name (issue #279). The bus knows who is talking — Zello says so
on `on_stream_start` — and cannot deliver it: its DMR attachment is a Homebrew
master that the local DMRGateway logs into, and DMRGateway forwards Talker Alias
only repeater→network (at the pinned `79edbc4`, `CDMRNetwork::clock` has no `DMRA`
case at all, so an alias sent that way is dumped as "Unknown packet from the
master"). MMDVM-Host accepts DMR-network datagrams from exactly one address:port,
which is the DMR relay — so waypointd injects, and the relay's taps run *after* a
datagram is forwarded, which is also the only ordering MMDVM-Host accepts an alias
in. **With the DMR relay switched off there is nowhere to inject**, waypointd
registers no handler, and the notes are dropped; the DMR panel says so.

`name` is a Zello display name and is transmitted verbatim — its case is part of
it, and nothing on the path upper-cases it the way a callsign template would.
