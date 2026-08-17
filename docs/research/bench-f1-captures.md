# F1 bench captures — run procedure

The three sections of the bot data plane investigation that need a hand on the
radio: §B (inbound M-SMS), §C (the confirmed-data question) and §D (900999
positions). See [bot-data-plane.md](bot-data-plane.md) for why each is being
asked and what turns on the answer.

Everything else in F1 is settled from source and needs nothing here.

---

## Before you start

**Verified on the bench 2026-08-17**, so these are observations rather than
assumptions:

- `tcpdump` **is** installed (`/usr/bin/tcpdump`). An older note said apt was
  restricted and nothing on the box could capture packets; that is no longer
  true, and the AF_PACKET-sniffer workaround it recommended is unnecessary.
- `sqlite3` is installed, at `/usr/bin/sqlite3`.
- ssh is **password auth only** — `rescue@172.16.50.13`. There is no key.
- The **relay is already running** and has been since Aug 12, so the loopback is
  in its shimmed wiring right now:

  | | port | who binds it |
  |---|---|---|
  | MMDVM-Host `LocalPort` | 62032 | MMDVM-Host |
  | relay host-facing | 62033 | waypointd |
  | relay gateway-facing | 62034 | waypointd |
  | DMRGateway `LocalPort` | 62031 | DMRGateway |

- **Every burst therefore appears on the wire twice**, once per leg. This is
  confirmed, not predicted — a probe capture showed each frame as both
  `62032->62033` and `62034->62031`. Use `-from 62032` for the radio→network
  leg and `-from 62031` for network→radio.

### The two settings that decide whether any of this works

- **`Slot1=0`, `Slot2=1`** in the rendered `MMDVM-Host.ini`. TS1 network traffic
  is dropped by MMDVM-Host before anything logs it (`DMRNetwork.cpp:147-154`).
  **Everything must happen on timeslot 2.** Put the SMS test channel on TS2.
- Station is `KN4OQW` / `3180202`, colour code 2, `Duplex=1`.

---

## The tool

```sh
# from a waypoint worktree, on the workstation
export BENCH_PASS='...'
tools/dmrdcapture/bench-capture.sh <label> [seconds]
```

It starts tcpdump on the node, prints **KEY THE RADIO NOW**, waits, pulls the
pcap back to `capture/<label>.pcap`, prints every frame it saw, and writes
`capture/<label>.txt` in the committed fixture format.

Read the summary before trusting the fixture. It prints one line per frame:

```
62032->62033 ts2 private 3180202 -> 9000001 data type 0x6
```

`data type 0x6` is `DT_DATA_HEADER`, `0x7` is `DT_RATE_12_DATA`, `0x3` is a
preamble CSBK. A capture of a text message should be preambles, then one
header, then blocks. If you see `voice`, the radio sent a voice call and the
capture missed the message.

### Reading an empty capture

"No DMRD frames matched" has two completely different causes and they look
identical from the summary. Tell them apart by the file size:

| pcap size | Meaning |
|---|---|
| 24 bytes | tcpdump captured nothing at all — the filter, the interface or sudo is wrong |
| larger, but no DMRD | the loopback is alive and **nothing was transmitted** |

The second case is not an empty wire. The two daemons keep the link up on their
own: MMDVM-Host sends a 119-byte `DMRC` config and DMRGateway answers a 4-byte
`DMRP` ping, one pair every 10 seconds, each appearing on both legs. Measured
2026-08-17 — an idle 180 s window held exactly 72 packets and no DMRD.

So a capture of ~8 KB with no DMRD means the tooling worked and the radio did
not transmit inside the window. Re-run and key the radio; do not go looking at
the GPS configuration yet.

That keepalive pair is also a free transparency check: every `DMRC` appearing as
both `62032->62033` and `62034->62031` is the relay forwarding byte-for-byte.

---

## §B — inbound M-SMS to a bot-range ID — DONE 2026-08-17

Captured, committed as `internal/dmrdata/testdata/capture-radio-tms.txt`, and
asserted by `TestReassembleRecordedCaptures`. Five preamble CSBKs, one data
header, six rate-1/2 blocks; decodes clean with every error counter at zero.
**§C fell out of it: UNCONFIRMED data.** The procedure below is kept for
re-running it.

**What this settles:** that the existing codec parses a radio-originated TMS
message, and it produces the fixture F3's golden test needs. The tree already
has an inbound ETSI/DMR-Standard capture from this radio
(`internal/dmrdata/testdata/capture-radio-etsi.txt`) and 42 stored inbound TMS
messages **from BrandMeister** — but nothing radio-originated in M-SMS, which
is the dialect the bot path will mostly meet.

### Radio setup (BTECH DMR-6X2 Pro)

Record every one of these in the fixture header; they change the answer and
they are codeplug fields, not protocol constants.

| Setting | Value | Why |
|---|---|---|
| Channel timeslot | **TS2** | `Slot1=0`; TS1 is dropped silently |
| Channel colour code | **2** | must match the node |
| SMS Format | **M-SMS** | this is the dialect being captured |
| APRS Receive | **OFF** | keeps beacons out of the capture |
| Confirmed Data | **record whatever it is** | this is the §C variable |

### Run

```sh
tools/dmrdcapture/bench-capture.sh sms_rx_to_9000001 60
```

When it prints KEY THE RADIO NOW: send a **private** message to DMR ID
**9000001**, with a short, distinctive body. Use something you will recognise
in a decode and write down exactly what you typed — `F1 SMS TEST` is fine.

Watch the radio's display and **write down what it says**: sent / delivered /
failed, and whether it retries. That observation is §C's evidence, so do not
skip it.

### Then

```sh
# what the node made of it, if the codec already handles this dialect
sshpass -p "$BENCH_PASS" ssh -q -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no rescue@172.16.50.13 \
  'echo "$BENCH_PASS" | sudo -S -p "" sqlite3 -header /var/lib/waypoint/events.db \
   "select id,direction,peer,local,dialect,body from messages order by id desc limit 3;"'
```

A row with `peer=3180202`, `local=9000001` and `dialect=tms` means the codec
decoded it with no work needed and the fixture is confirmation rather than
discovery. **No row** means either the message did not decode or it did not
reach the tap — the pcap tells you which, and that difference is the finding.

Commit the trimmed fixture to `internal/dmrdata/testdata/` following the header
convention already in that directory: what it is, how it was taken, the
codeplug state, what was typed, and the sanitisation note (own RadioID only).

---

## §C — is it confirmed data? — ANSWERED: NO

`Messages: 1`, `Unsupported: 0` on the §B capture. `parseDataHeader` declines
`DPFConfirmedData` outright, so a clean decode is proof of an unconfirmed
transfer. D-F9 is moot, INTERCEPT is unblocked, and F3 needs no ARQ.

Still worth re-running per radio and per channel: **Confirmed Data is a
per-channel setting**, so this answer is about this codeplug, not about every
radio that will ever message a bot.

**This was the question that gated INTERCEPT.** If the radio sends bot-addressed
SMS as confirmed data and we terminate the frame at the shim, we have removed
the only party that could ever answer it — the network never sees it — and the
radio retries against a wall.

Two halves, and the first is free once §B is captured.

### C.1 — read it off the data header

Confirmed vs unconfirmed is the **DPF nibble** in octet 0 of the data header —
`DPFUnconfirmedData = 0x02`, `DPFConfirmedData = 0x03`
(`internal/dmrdata/header.go:30-33`). It is not a "response requested" bit.

Run the capture through the tree's own reassembler:

```sh
go run ./tools/dmrdcapture -in capture/sms_rx_to_9000001.pcap -from 62032 -decode
```

Read the outcome off the stats line:

| Result | Meaning |
|---|---|
| `Messages: 1` and the text you typed | **Unconfirmed.** D-F9 is moot; F3 keeps only the `AckFor(tx) → nil` seam. |
| `Unsupported: 1`, no message | **Confirmed** (or another declined format). D-F9 is live — and see below. |
| `BadCRC` / `Unusable` | the capture is lossy, not the radio's fault; recapture. |
| `NoSync` | a broken transmitter, not a format question. |

**If it comes back `Unsupported`, that is a bigger finding than "we must send an
ACK".** `parseDataHeader` rejects confirmed data outright
(`header.go:78-80`), so the current codec cannot decode a confirmed message at
all — the bot path would need confirmed-data reassembly (per-block CRC-9 and the
ARQ exchange), not just an ACK. That is a substantially larger F3 than the
runbook budgets for, and it should be said out loud before F3 starts rather than
discovered inside it.

Note the existing outbound path states plainly that nothing acknowledges its
messages (`internal/dmrdata/sms.go:29-31`) — but that is evidence about
Waypoint→radio, not radio→Waypoint. Do not let it stand in for this
measurement.

### C.2 — what the radio does when nobody answers

Only needed if C.1 says confirmed.

Today the frame is **not** intercepted: it is forwarded to DMRGateway and on to
BrandMeister, so the radio's behaviour right now is the "network carried it"
case. To observe the intercepted case before intercept exists, take the network
away and repeat §B:

```sh
sshpass -p "$BENCH_PASS" ssh -q -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no rescue@172.16.50.13 \
  'echo "$BENCH_PASS" | sudo -S -p "" systemctl stop waypoint-dmrgateway'
```

Send the same message again and record: how many retries, how far apart, and
what the display finally says. Then start it again:

```sh
sshpass -p "$BENCH_PASS" ssh -q -o PreferredAuthentications=password \
  -o PubkeyAuthentication=no rescue@172.16.50.13 \
  'echo "$BENCH_PASS" | sudo -S -p "" systemctl start waypoint-dmrgateway'
```

This is not the same as intercept — with DMRGateway stopped the relay's
gateway-facing writes fail rather than being suppressed — but the radio cannot
tell the difference, and the radio's behaviour is the thing being measured.

---

## §D — 900999 position beacon

**What this settles:** the payload format of an Anytone GPS-over-DMR report, so
positions can be decoded. The transport half needs nothing new — the relay's
taps already see a copy of every frame and cannot stop one, so passive
observation of 900999 is free and BrandMeister APRS keeps working untouched.

### BLOCKED 2026-08-17 — the beacon does not reach the modem

Ruled out on the Waypoint side by four independent instruments, so nobody needs
to re-run this. The radio is configured for digital APRS, timeslot 2, 310999 as
a **talkgroup**, with a solid GPS fix and a report every 30 s, and the operator
observes it beaconing.

| Instrument | Result |
|---|---|
| Loopback capture, five windows totalling ~20 min | zero data bursts |
| Events DB | no data transmission recorded |
| `mosquitto_sub -t 'mmdvm/#'` for 120 s | **complete silence** |
| Idle 150 s window, ~5 beacon intervals, no PTT | nothing |

The MQTT watch is the decisive one, and it is worth knowing about for any future
"did the modem hear it" question. `MQTTLevel=1` publishes every log line as well
as the JSON, so a transmission the modem heard and *refused* would appear there
even though it reaches neither the loopback nor the events DB — the events bridge
maps only `start`/`end`/`lost`/`timeout`, never `rejected`
(`internal/mqtt/bridge.go:114,129`), and a refused frame is never forwarded to
the network. Silence on `mmdvm/#` therefore means the modem heard nothing at
all, which is a stronger claim than an empty capture can make on its own.

Also positively excluded rather than assumed: there is no `Slot2TGWhiteList` in
the rendered `[DMR]` section, and `validateTGId` returns true unconditionally
when the list is empty (`DMRAccessControl.cpp:96-99`); `SelfOnly=0`; no
`Prefixes`. MMDVM-Host would have accepted a group call to 310999 from anyone.

So the beacon is being transmitted somewhere the hotspot cannot hear it — on the
6X2 Pro, the digital APRS system's transmit channel being a fixed channel rather
than the current one. That is a CPS field and needs Windows or Wine; it is not a
Waypoint problem and must not hold up F2 or F3.

**One thing to fix at the same time:** 310999 is configured as a talkgroup.
BrandMeister's APRS ingest expects a **private** call. Irrelevant to Waypoint —
the observe set matches on destination id whatever the call type — but it is why
the positions would not land on aprs.fi even once the transmit path is fixed.

### Superseded first reading

Three capture windows (3, 5 and 8 minutes) covering six PTT releases with the
beacon set to fire at PTT-end, rate-limited to once per 30 s, and with the radio
moved to a window for a GPS fix. Across both legs every single frame was
`3180202 -> 9990` or `9990 -> 3180202` — the Parrot test calls and their echoes.
Zero data-header, rate-1/2 or preamble bursts, and no other destination ID.

The events DB agrees independently: the PTTs appear as `rf_voice_*` rows and no
data transmission does. That is a real check rather than an absence of evidence,
because MMDVM-Host emits the same `"start"` JSON action for an RF data header as
for voice (`DMRSlot.cpp:468`), so a beacon would have been recorded.

So this is a codeplug problem on the 6X2 Pro, not a hotspot or tooling one. Two
settings must BOTH be right and either alone produces exactly this silence:

- the channel's **APRS Report / Digital APRS** switch is on, and
- the channel selects a **Digital APRS System** — a channel can have reporting
  enabled with no system bound to it.

Quickest discriminator: the radio's manual one-touch position send, which
bypasses both PTT and the 30 s limiter. Nothing on the wire from that, on a
channel with a fix, means the system is not bound to the channel.

**Do §B first.** It needs no GPS and no APRS configuration, it produces the
fixture F3's golden test needs, and it answers §C — which is the question
gating whether INTERCEPT can be built at all. §D is the lower-value half and
should not hold up the visit.

### Radio setup

Configure DMR GPS reporting as for BrandMeister APRS — private call, TS2. Record
the exact codeplug fields, including the report interval and the destination ID.

**Do not take the destination from this document.** The runbook says 900999 and
the operator's radio is set to 310999; nothing in the codebase independently
asserts either, so the ID is whatever the capture shows the radio actually
sending. The observe set is configuration, so the design follows the radio
rather than the other way round — there is no reason to retune a working APRS
setup to match a number no evidence supports.

### Run

```sh
tools/dmrdcapture/bench-capture.sh position_900999 180
```

Beacon at least twice inside the window — two reports from a stationary radio
tell you which bytes are the fix and which are a counter or a timestamp, and
one report tells you nothing.

### Decoding

**Do not assign a field a meaning by inspection.** Match it against a citable
open decoder first — the HBLink3 / KF7EEL `gps_data` lineage decodes Anytone
GPS-over-DMR and is the ground truth to check against. A partial decode with
the unknown bytes mapped is a valid deliverable; a guess is not.

If the format matches nothing citable, stop and report that. §D's stop
condition is explicit about this.

---

## Order

§B first — it produces the capture §C.1 reads, so doing §C first means going
back to the radio. §D is independent and can be done in the same visit.

Expect roughly: §B ten minutes, §C.1 free, §C.2 ten minutes if needed, §D
fifteen. The decoding afterwards is workstation work and does not need the
bench.
