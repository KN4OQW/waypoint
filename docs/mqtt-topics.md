# Waypoint MQTT topic scheme

Waypoint publishes its live status onto a **stable, documented** set of retained
MQTT topics, and describes them to Home Assistant via MQTT Discovery so entities
appear with **zero YAML**. This document is the contract third-party consumers can
build against.

- Status pipeline & topic design: [RFC-0008](rfcs/0008-status-pipeline.md)
- Home Assistant discovery: [RFC-0011](rfcs/0011-home-assistant-discovery.md)

All topics below are **retained** (a late-joining subscriber reads current state
immediately) and are published only in live mode (a `-demo` node has no broker).
The status prefix defaults to `waypoint/status` (`-status-topic-prefix`).

## State topics (`waypoint/status/#`)

| Topic | Payload | Meaning |
|---|---|---|
| `waypoint/status/mode` | string, e.g. `DMR` | Current active mode, or `IDLE`. |
| `waypoint/status/tx` | JSON `{mode,slot,source,dest,network,direction,started_at}` or empty | The transmission on the air; empty payload when idle. |
| `waypoint/status/feed` | JSON `{connected,detail,since}` | Health of the MMDVM-Host MQTT feed everything derives from. |
| `waypoint/status/network/<name>` | JSON `{up,detail,since}` | Per network/reflector link state. `<name>` is topic-sanitized. |
| `waypoint/status/gateway/<name>` | JSON `{up,detail,since}` | Per gateway-daemon liveness (from the supervisor probe). |
| `waypoint/status/availability` | `online` / `offline` | Device availability. `offline` is the connection's MQTT Last-Will, so it flips the moment the node drops. |

`<name>` segments have `/`, `+`, `#`, and whitespace replaced with `_` so they are
safe topic levels.

Status is **self-healing** (RFC-0008): a stranded transmission clears on a
watchdog and a killed gateway shows down within the supervisor probe interval, so
these topics reflect truth rather than a latched last value — no log scraping.

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

Discovery is published only to the broker the node already talks to
(`-mqtt-broker`); Waypoint never reaches out to a new host.
