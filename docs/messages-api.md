# Text messages API

A Waypoint node can send a DMR text message to a radio over its own RF, and record
the ones radios send back. This document is for whoever is driving that from
another tool.

This API is **authenticated**, like every other `/api` route that is not the claim
handshake. It is not part of the public view and never will be: a message is
correspondence, and no public route serves one. See
[Public API](public-api.md#what-is-published-and-what-is-not).

---

## Before it works

Three things have to be true, and two of them are not in this API.

**1. The DMR message relay must be on.** waypointd sits between MMDVM-Host and
DMRGateway on the loopback so it can originate a frame; without it there is
nowhere to transmit. It is off by default, because with it on waypointd is in the
path of every DMR frame — see `internal/config/dmrshim.go`. Turn it on with
`dmrnet.shim_enabled` and Apply.

**2. The node needs a DMR ID.** `[DMR] Id`, falling back to `[General] Id`.

**3. The receiving radio must be set up for it.** This is a codeplug requirement
and no amount of hotspot software can satisfy it:

- SMS format **M-SMS** (not "DMR Standard")
- **APRS Receive OFF** on the channel

Anytone and BTECH firmware treats APRS receive and SMS as mutually exclusive. With
APRS receive on, the radio decodes the burst, lights up, and discards the message —
which looks exactly like a message that never arrived. This cost two days to find.

---

## `POST /api/messages`

Queue a message for transmission.

```bash
curl -sS -X POST https://your-node.example/api/messages \
  -H 'Content-Type: application/json' \
  -b cookies.txt \
  -d '{"dmr_id": 3180299, "text": "hello from the shack"}'
```

| Field | Type | Notes |
|---|---|---|
| `dmr_id` | number | The destination: a radio's 24-bit DMR ID, or a talkgroup with `group` set. Required. |
| `text` | string | The message. Up to **123 UTF-16 code units** — a character outside the BMP costs two. |
| `group` | bool | Optional. Send to a talkgroup rather than an individual. |

Answers **202 Accepted** with the stored message. Not 200: it has been recorded and
queued, not transmitted. Transmitting takes air time and may have to wait for the
channel to go quiet.

```json
{
  "id": 41,
  "direction": "out",
  "peer": 3180299,
  "local": 3180202,
  "group": false,
  "text": "hello from the shack",
  "state": "queued",
  "created_at": "2026-08-07T14:22:05.412Z",
  "updated_at": "2026-08-07T14:22:05.412Z"
}
```

### Errors

| Status | When |
|---|---|
| `400` | The request is wrong: no destination, an ID wider than 24 bits, text that is not valid UTF-8, or a field nobody defined. |
| `409` | The node cannot do this yet — no DMR ID configured, or the relay is not running. Nothing about the message was wrong. |
| `413` | The text is longer than the on-air format carries. The body includes `max_units`. |
| `429` | Too many messages are already waiting. The one you sent is recorded as `failed`. |
| `503` | This node has no message store. |

---

## Message states

```
outbound:  queued ──► transmitting ──► sent
              └──────────┴──────────► failed  (with a reason)

inbound:   received
```

**`sent` does not mean delivered.** Unconfirmed DMR data carries no
acknowledgement. `sent` means every burst was emitted; the radio may have been
switched off, out of range, or configured wrong. There is no state past it,
because there is no fact past it.

`failed` always carries a `reason`. The ones worth recognising:

| Reason contains | What happened |
|---|---|
| `relay is not running` | The DMR message relay is off. See above. |
| `has been busy` | The timeslot never went quiet. A message that cannot get a clear channel is put back on the queue and tried again — up to five times, about five minutes — before it fails, so this means the channel was solid for that whole time. On a simplex node it is usually a static talkgroup flooding it; check BrandMeister SelfCare. |
| `burst N of M` | Transmission stopped partway. The receiving radio has a header whose blocks never arrived. |
| `waiting to transmit` | The queue was full. |

---

## `GET /api/messages`

List messages, newest first.

```bash
curl -sS -b cookies.txt \
  'https://your-node.example/api/messages?direction=in&peer=3180299&limit=50'
```

| Parameter | Notes |
|---|---|
| `direction` | `out` or `in`. Omit for both. |
| `peer` | A DMR ID: the destination of outbound messages, the source of inbound ones. This is what groups a conversation. |
| `state` | One of the states above. |
| `limit` | Default 200, clamped to 2000. |

```json
{ "messages": [ { "id": 41, "...": "..." } ] }
```

`messages` is always an array, never `null`.

## `GET /api/messages/{id}`

One message. `404` if there is no such id.

---

## Watching for changes

Every state change publishes a hub event, which reaches the SSE stream
(`GET /api/events`), the WebSocket (`GET /api/ws`), the persistent history
(`GET /api/history`) and the MQTT status plane like any other event.

The event carries the message **id and state, and never the text**. The event log
is republished to MQTT and read by clients that have no business with
correspondence; the id is enough for a client that is entitled to the message to go
and read it.

```json
{ "time": "2026-08-07T14:22:07.118Z", "type": "message_out", "mode": "DMR",
  "source": "3180202", "dest": "3180299", "detail": "message 41 sent" }
```

Types are `message_out` and `message_in`.

The right pattern is the one the rest of this API uses: **REST is authoritative,
the event stream is a poke.** Take the event as a signal to re-read, not as the
data.

---

## Received messages, and the "2" in front of an ID

The node records the text messages that cross it, both ways: what a radio sent
toward the network, and what the network sent back. On a hotspot those are all the
operator's own. On a repeater carrying other people's traffic they are not — worth
knowing if you run one. Nothing here is public; see the note at the top.

If an inbound message shows a sender like `2262993` where you expected `262993`,
that is a **DMRGateway dial prefix, not a fault in the message**. A BrandMeister
network rendered as *non-primary* gets prefix 2 and the rule

```
SrcRewrite4=2,1,2,2000001,999999
```

which maps source id N to 2000000 + N, so 262993 arrives as 2262993 and a reply
must be dialled that way. That is correct behaviour when the node really has
several networks: the prefix is how you say which one you meant.

It is only wrong when BrandMeister is the node's *only* network, where there is no
other network for a catch-all to refer to and no prefix should be needed.
`effectivePrimaryIndex` promotes a sole eligible network to primary even with the
Primary flag clear, so a single-network node renders the catch-all and IDs pass
through untouched. If you see the prefix on a single-network node, the fix is to
re-Apply so the gateway config is regenerated.

---

## Retention

Messages age out on the node's event-history retention window (Station Settings,
default 7 days), pruned nightly along with the event log. A node is not a message
archive.
