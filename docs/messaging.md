# Text messages: sending DMR SMS from your node

Your node can send a text message to a radio over its own RF, and keep a record of
the ones that cross it in either direction. Nothing upstream is involved — no
BrandMeister, no reflector, no internet. If your radio is in range of the hotspot,
it can be texted.

It is **off by default**, and there is a radio-side setting you have to get right
before any of it works. Both are covered below, in the order that will save you the
most time.

---

## Before you start: three things have to be true

**Your radio's channel must use `M-SMS` format with `APRS Receive` switched OFF.**

This is the one that catches everybody, and it costs nothing to check first. On
Anytone and BTECH firmware, APRS receive and SMS are mutually exclusive. With APRS
receive on, the radio decodes your message, lights up as though something arrived,
and silently discards it. There is no error and nothing in any log. If messages
"nearly work" — the radio reacts but shows nothing — this is why.

**Your node needs a DMR ID.** `Settings → General → Station Identity → DMR ID` is
the one almost everybody wants; `Settings → Modes → DMR → DMR ID override` is a
per-mode override that most nodes leave blank. If you do not know your ID, fill in
your callsign on the General tab and press **Find my ID** — it reads the DMR ID
table the node already downloads and offers whatever is issued to that callsign.

**The message relay has to be on.** See the next section.

---

## Turning it on

**Settings → Modes → DMR → TEXT MESSAGES → Message relay enabled**, then **Apply**.

Applying restarts MMDVM-Host and DMRGateway, so expect a few seconds of dead air.

### Why it is off by default

With the relay on, Waypoint sits in the path of every DMR frame your node carries.
That is what lets it originate a message at all — MMDVM-Host only accepts network
frames from one address, so the only way in is to *be* that address.

The cost is that restarting Waypoint now interrupts DMR. Measured on the bench:
about **3.4 seconds**, recovering by itself with no intervention. Voice quality
through the relay is unchanged — 0% packet loss and 0.0% BER, identical to the same
node with the relay off.

If you do not use messaging, leave it off and nothing about your node changes.

---

## Sending

**Messages** in the sidebar. Enter the destination DMR ID, type, and send.

The counter beside the button is your remaining length. The limit is **123 UTF‑16
code units**, which is 123 characters of ordinary text — but an emoji or any other
character outside the Basic Multilingual Plane costs two. Over-length is refused
before anything is sent, and the error tells you the limit.

A message does not go out the instant you press send. It waits for the timeslot to
be quiet, because MMDVM-Host *discards* a network frame that arrives during
somebody's transmission rather than holding it — a message thrown at a busy channel
is silently lost. Waiting is the only way to be sure it is not.

If the channel is busy the message goes back in the queue and tries again, up to
five times over about five minutes, and messages behind it still get their turn.
Only after that does it give up.

### What the states mean

| State | Meaning |
|---|---|
| `queued` | Accepted, waiting for a clear timeslot. |
| `sending` | Bursts are going out, about a second and a half for a short message. |
| `sent` | Every burst was transmitted. |
| `received` | An inbound message. |
| `failed` | It will not be sent. The row says why. |

**`sent` does not mean delivered.** Unconfirmed DMR data carries no acknowledgement
of any kind, so once the last burst leaves the antenna there is nothing more the
node can learn. The radio may have been switched off, out of range, or set up
wrong. Anything claiming "delivered" would be inventing it.

---

## Receiving

Messages arrive on their own — there is nothing to switch on beyond the relay.
Anything crossing the node in either direction is recorded and appears in the list.

That includes your own radio's outgoing messages, because they cross the node on
their way to the network. On a hotspot everything you see is your own traffic. On a
repeater carrying other people's, it is not; worth knowing if you run one. Messages
are never published — they appear in no public page or public API response, and
there is no setting that adds them.

---

## Two kinds of radio on one frequency

DMR short data has two formats in the wild, and which one a radio uses is a channel
setting on the radio, not something the network decides:

| Radio setting | What it is |
|---|---|
| **M-SMS** | Motorola TMS. What your node sends by default. |
| **DMR Standard** | The ETSI format. Same radios, different channel setting. |

A message in the wrong format is received, decoded and discarded with nothing
shown — the same symptom as the APRS problem above.

You do not have to configure this. **The node learns it**: every message a radio
sends announces its format, so that is remembered and used when replying. A club
with a mixture of radios works without anybody setting anything. A radio nobody has
heard from yet gets M-SMS.

Private messages are addressed to a single radio, so a mixed fleet does not
interfere with itself. A **talkgroup** message is one transmission in one format,
so only radios using that format will display it. There is no way around that
without transmitting twice.

---

## When it does not work

**The radio lights up but shows nothing.** APRS Receive is on, or the channel is in
the wrong SMS format. Check the radio first — this is by far the most common cause.

**A message says `failed` with "the DMR message relay is not running".** The relay
is off, or Apply has not been pressed since you turned it on.

**A message says `failed` with "the channel never went quiet".** Something is
occupying your timeslot continuously for five minutes. On a simplex node that is
usually a busy static talkgroup: the node cannot hear your radio while it is
transmitting a talkgroup's traffic, which also stops you keying up. Thin out the
static talkgroups on your network's own portal — BrandMeister calls it SelfCare.
Waypoint cannot see that list from here.

**Nothing arrives and nothing appears in the list.** Check the DMR link on the
dashboard first. If the node is not carrying DMR at all, messages are not the
problem.

---

## What this does not do

Hytera's proprietary short-data format is not supported. Neither is confirmed
(acknowledged) data, position reports, or carrying text across a
[bus](https://github.com/KN4OQW/waypoint/issues/85) to another node or a mesh
radio.

The ETSI format is built from a captured radio transmission and reproduces it byte
for byte, but this node has never yet sent one *to* a radio. M-SMS is the one
proven on the air.

Driving this from a script or a club bot: **[Text messages API](https://github.com/KN4OQW/waypoint/wiki/Text-messaging-API)**.
