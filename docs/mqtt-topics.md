# Waypoint MQTT topics — Home Assistant integration

Waypoint publishes normalized hotspot status under the `waypoint/status/#`
namespace and, when the Home Assistant integration is enabled, an MQTT-discovery
bundle so Home Assistant picks the hotspot up as a device **with zero YAML** (issue
#9). Everything below is published to the **same broker** waypointd consumes the
MMDVM-Host feed from (`-mqtt-broker`, default `127.0.0.1:1883`) — point Home
Assistant at that broker.

Enable it in the web UI under **Settings → Station Settings → Home Assistant**
(off by default). The publisher reads its config at daemon start; toggling it takes
effect on the next `waypointd` restart.

`<node>` below is the station callsign, lowercased with any character outside
`[a-z0-9_-]` removed (e.g. `KN4OQW` → `kn4oqw`, `W1AW/3` → `w1aw3`); a blank
callsign falls back to `waypoint`.

## Topics

| Topic | Retained | Payload | Purpose |
|---|---|---|---|
| `waypoint/status/<node>/state` | yes | JSON status blob (below) | Live hotspot status; every HA entity reads one field from it |
| `waypoint/status/<node>/availability` | yes | `online` / `offline` | Device availability; `offline` is the MQTT Last Will, so HA marks entities unavailable if waypointd drops |
| `<prefix>/device/<node>/config` | yes | HA device-discovery bundle | Announces the device + all entities to Home Assistant (`<prefix>` defaults to `homeassistant`) |

The publisher also subscribes to `<prefix>/status` (Home Assistant's own birth
message) and re-announces when Home Assistant restarts, so a retained discovery
that was missed is re-sent.

## State payload

`waypoint/status/<node>/state` carries a single JSON object; each Home Assistant
entity extracts one field via a `value_template`:

```json
{
  "last_heard": "KN4OQW",
  "last_target": "TG 91",
  "last_mode": "DMR",
  "last_time": "2026-07-19T12:00:03Z",
  "active": "OFF",
  "current_mode": "DMR",
  "network": "linked",
  "last_ber": 0.5,
  "last_rssi": -70,
  "last_duration": 3.4
}
```

| Field | Meaning |
|---|---|
| `last_heard` | Callsign/ID of the most recent transmitter |
| `last_target` | Talkgroup or destination callsign of the last transmission |
| `last_mode` | Mode of the last transmission (DMR, YSF, …) |
| `last_time` | RFC-3339 timestamp of the last transmission |
| `active` | `ON` while a transmission is keyed up, else `OFF` |
| `current_mode` | The mode MMDVM-Host is currently in |
| `network` | Last network link status |
| `last_ber` | Bit error rate of the last transmission (%) |
| `last_rssi` | Signal strength of the last transmission (dBm) |
| `last_duration` | Duration of the last transmission (seconds) |

## Home Assistant entities

The discovery bundle exposes these entities under one HA device (`Waypoint
<callsign>`), each with a stable `unique_id` of `waypoint_<node>_<entity>`:

| Entity | Type | Source field |
|---|---|---|
| Last Heard | sensor | `last_heard` |
| Last Target | sensor | `last_target` |
| Last Mode | sensor | `last_mode` |
| Last Heard Time | sensor (timestamp) | `last_time` |
| Activity | binary_sensor (running) | `active` |
| Current Mode | sensor | `current_mode` |
| Network | sensor | `network` |
| Last BER | sensor (%, diagnostic) | `last_ber` |
| Last RSSI | sensor (dBm, diagnostic) | `last_rssi` |
| Last Duration | sensor (s, diagnostic) | `last_duration` |
