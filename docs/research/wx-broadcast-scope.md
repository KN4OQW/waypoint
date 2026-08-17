# Weather alert broadcast — scope

Supersedes the bot-framework approach for this feature. Everything here is
**outbound only**, which is the point: every failure this series hit was in the
receive path, and transmission touches none of it.

Feed details below are verified against the NWS_EMQX repo
(`docs/DNS-AND-NPM.md`, `docs/CONTEXT-TRANSFER.md`), not taken from the runbook.

## Shape

```
mqtt.wxalerts.org ──▶ waypointd ──▶ routing policy ──┬─▶ SMS to TG(s)
   (subscribe)         (ingest)      (county+class)   └─▶ voice announce, same TG
```

An operator picks counties and which classes of hazard matter. An alert arrives,
is matched against that, and produces one group-addressed SMS to the configured
talkgroup plus a queued voice announcement on the same talkgroup.

## The feed, verified

```
URL       wss://mqtt.wxalerts.org/mqtt      port 443, MQTT v5 over websockets
Username  wxalerts
Password  wxalerts
```

Read-only: subscribe is permitted on `wxalerts/nws/v1/#`, publish is denied and
enforced by the broker. The credential is public in NWS_EMQX's own docs — it is a
read-only account on public NWS data, and the ACL is what keeps it safe. That
means no credential exchange and nothing secret to store, though the store should
still carry them as editable fields rather than constants.

### Topics we use

```
wxalerts/nws/v1/same/{same_code}/{etn}
```

`{same_code}` is the six-digit SAME/FIPS code a weather radio is programmed with
(`PSSCCC`) — Santa Rosa County FL is `012113`. **Subscribe with the trailing
`/#`**: the ETN is the last level precisely because these are retained, and one
county can be under a tornado warning and a flood warning at once. Without that
level the second overwrites the first.

One alert appears on one topic per county it covers; the payload is identical on
all of them and carries the full `same` array. So a warning over three monitored
counties arrives three times and must be de-duplicated — key on `vtec`, else
`id`.

Marine and offshore zones have no county behind them and never get a SAME code,
so they appear only on the office tree. A county-driven node will not see them,
which is correct for this feature.

### Payload fields that matter here

```jsonc
{
  "vtec": "KMOB.TO.W.0012.2026",   // null for API alerts without VTEC
  "event": "Tornado Warning",
  "phenomena": "TO",                // null if no VTEC
  "significance": "W",              // W warning, A watch, Y advisory, S statement
  "action": "NEW",                  // NEW CON EXT EXA EXB CAN EXP UPG
  "status": "active",               // active | expired | cancelled
  "severity": "Extreme",            // Extreme | Severe | Moderate | Minor
  "ends":     "2026-08-11T20:45:00+00:00",
  "expires":  "2026-08-11T20:45:00+00:00",
  "same": ["012113", "001003"],
  "headline": "TORNADO WARNING IN EFFECT UNTIL 345 PM CDT",
  "instruction": "TAKE COVER NOW! Move to a basement…"
}
```

`expires` is CAP's update deadline, **not** the end of the hazard. Use `ends`,
and trust `status` and tombstones.

### Two behaviours that will transmit hundreds of messages if missed

**1. Retained delivery.** Subscribing gets every currently-live hazard
immediately — NWS_EMQX measures 1,600–2,300 messages in the first second on a
broad subscription. A county-scoped subscription is far smaller, but it is still
"every alert in effect right now", arriving at connect and again after any
reconnect.

On a node that **transmits**, announcing that burst is not a cosmetic bug. It is
a string of SMS and voice transmissions for hazards the operator already knows
about, on every reconnect, over the air.

The MQTT retain flag is the discriminator and it is per-message: `retained=true`
is state sync, announce nothing; `retained=false` is live. This must be verified
against the actual client on a live connection before any transmit path is wired
up — a client that does not surface the flag per message makes the whole feature
unsafe.

**2. Tombstones.** A hazard that ends gets a zero-length retained publish on
every topic it appeared on. Treat an empty payload as "over" and clear it. Never
announce one.

## Operator configuration

- **Counties.** A list of SAME codes. May span states and WFOs. The existing
  runbook plan for a county lookup (`internal/wxzones`, `api.weather.gov`
  metadata, on-demand, cached) still applies — the operator should pick counties
  from a list, not type FIPS codes.
- **What gets sent.** Two axes exist in the payload and they are not the same
  thing:
  - `significance` — W warning, A watch, Y advisory, S statement. Coarse, VTEC.
  - `phenomena` + `significance` — `TO`/`W` is precisely "Tornado Warning".
  - `severity` — Extreme/Severe/Moderate/Minor. CAP's own judgement.

  The panel should present **class rows (W/A/Y/S) with per-event overrides**, so
  an operator can say "all warnings, plus tornado watches, but not flood
  advisories" without learning VTEC. That is the runbook's D4 matrix and it
  survives unchanged.
- **Talkgroup(s).** Default 9. The operator may specify others — note this is
  plural, so the store carries a list and one alert may produce several
  transmissions.
- **Announce actions.** Default `["NEW"]`. `CON` updates would otherwise
  re-transmit the same hazard every few minutes.

## Delivery

For each alert that passes the policy:

1. **SMS**, group-addressed to each configured talkgroup. Text is built from
   `event` + `ends` (local) + a truncated `headline`.
2. **Voice**, queued for the same talkgroup, decoupled through the existing
   announce-job seam so a missing TTS transport degrades to a logged skip rather
   than a failure.

### What is already proven for this path

- **Outbound SMS synthesis works.** `internal/dmrdata` builds the bursts,
  `internal/dmrshim` injects them, and the encode/decode pair round-trips. There
  is a committed fixture decoded off the air.
- **Group data reaches RF.** MMDVM-Host applies `CDMRAccessControl` only in the
  RF→network direction; `CDMRSlot::writeNetwork` has no access-control call at
  all, and its data-header branch reads the group flag purely for the short LC
  and the log line. So a group-addressed data transmission is not blocked by the
  host. See `bot-data-plane.md` §E.

- **Group-addressed SMS displays on the radio. Confirmed on the bench
  2026-08-17.** This was the largest open risk, because it depends on the
  receiving radio's RX group list rather than on anything Waypoint controls, and
  a negative would have forced delivery to fan out to last-heard stations
  individually.

  `POST /api/messages {"dmr_id":9,"text":...,"group":true}` on a node with
  ColorCode 1 and both slots enabled produced a message on a BTECH 6X2 Pro with
  TG 9 in its RX group list, and the node recorded `state: sent`. The whole
  outbound chain ran: API, message service, burst synthesis, shim injection,
  MMDVM-Host, RF, display. That is the weather delivery path with different text
  in it, so the delivery layer can be built on group addressing.

  Not tested: a talkgroup **absent** from the radio's RX list, which is the case
  that decides what an operator must tell their users to configure. Worth ten
  minutes before the feature ships.

### What is NOT proven, and gates the feature

- **Slot selection.** MMDVM-Host drops frames on a disabled slot before anything
  logs it (`DMRNetwork.cpp:147-154`), and simplex suppresses slot 1 outright.
  The transmit path must choose its slot from the rendered configuration and
  refuse visibly when neither is available. `messageSlot` in
  `cmd/waypointd/messages.go` already does the first half.

## Gating and compliance

- **RF idle gating.** Reuse the existing per-slot gate in `internal/messages`
  rather than writing another: idle counted from the later of the last burst and
  the moment the tap attached, and zero idle time when not observing. Both
  properties were learned the hard way and are commented as such.
- **Queued alerts whose `ends` has passed are dropped**, with an event saying so.
  An alert announced after it expired is worse than one not announced.
- **Broadcast reaches everyone on the frequency with no opt-out.** On a hotspot
  that is the operator. On a repeater it is not, and the default must stay off
  with the panel saying plainly what it does.
- Everything transmits through the one existing path, so the compliance kill
  switch and station-identification requirements cover it by construction rather
  than by remembering to.

## What this drops from the runbook

F1–F6 (the bot framework), and W5's subscriber model, geometry matching and
inbound command grammar. All of it depended on receiving short data, which is
the subject of KN4OQW/waypoint#263.

W1–W4 survive nearly unchanged: the store sections, the county lookup and panel,
the ingest, and the routing matrix. W5 shrinks to a delivery layer over the
`broadcast` channel that D4 already defines.
