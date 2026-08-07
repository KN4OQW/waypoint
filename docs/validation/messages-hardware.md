# Direct DMR SMS — hardware validation

Bench `kn4oqw` (Raspberry Pi 3, armv7l, 172.16.50.13), DMR ID 3180202, colour code
2, **slot 2 only**, duplex RX 438.800 / TX 433.800. Radio: BTECH 6X2 Pro, channel
SMS format **M-SMS**, **APRS Receive OFF**. BrandMeister master
`3103.master.brandmeister.network`, sole DMR network, primary.

Run 2026-08-07 20:38–21:05 UTC against the merged codec, relay, store, API and
inbound capture, plus the messages page from the then-open UI branch.

Every numbered item of the plan is below with what was measured. Two items did
not go the way the plan assumed; both are written up as findings rather than
smoothed over.

---

## Setup and restoration

Snapshotted before anything changed — `sqlite3 .backup` for both databases, never
a bare `cp`, because the store is WAL:

```
/home/rescue/prompt7-snapshot/
  config.db      (sqlite3 .backup)
  events.db      (sqlite3 .backup)
  etc/*.ini      (7 rendered configs)
  waypointd.prev (the binary that was running)
```

Restored afterwards and verified, not assumed:

```
dmrnet snapshot: {"gateway_address":"","gateway_port":"","jitter":"","local_port":"","mode_hang":"","slot1":false,"slot2":true}
dmrnet now     : {"gateway_address":"","gateway_port":"","jitter":"","local_port":"","mode_hang":"","slot1":false,"slot2":true}
identical: True

MMDVM-Host.ini: identical to snapshot
DMRGateway.ini: identical to snapshot
services: waypointd active, waypoint-mmdvm active, waypoint-dmrgateway active
```

The node is left on the **newer binary** with the relay **off**, which renders
byte-identically to the old one. `waypointd.prev` is kept for rollback.

### The upgrade is a no-op until you opt in

Worth recording on its own, because it is the availability claim the relay's
default rests on. After installing the new binary and restarting, with
`shim_enabled` untouched:

```
127.0.0.1:62031  DMRGateway
127.0.0.1:62032  MMDVM-Host
(no relay lines in the journal)
```

Unchanged wiring, no new sockets, no relay.

### Switching it on

`shim_enabled: true` in the store, then Apply:

```
MMDVM-Host  [DMR Network]  LocalPort=62032   GatewayPort=62033
DMRGateway  [General]      RptPort=62034     LocalPort=62031

127.0.0.1:62031  DMRGateway
127.0.0.1:62032  MMDVM-Host
127.0.0.1:62033  waypointd
127.0.0.1:62034  waypointd

dmr relay: relaying 127.0.0.1:62033 <-> 127.0.0.1:62034 (MMDVM-Host 127.0.0.1:62032, DMRGateway 127.0.0.1:62031)
```

Each daemon kept the port it BINDS and changed only the port it SENDS TO, which is
what the renderer promised. BrandMeister then re-logged in **through** the relay —
the Homebrew handshake survives it:

```
waypoint/status/network/BM_3103_United_States {"up":true,"state":"up","detail":"logged in"}
waypoint/status/network/DMR_Message_Relay     {"up":true,"state":"up","detail":"relaying (…)"}
```

Relayed round trip on the loopback, showing the full path and the cost:

```
20:43:07.685751  127.0.0.1.62032 > 127.0.0.1.62033: UDP, length 119
20:43:07.685840  127.0.0.1.62034 > 127.0.0.1.62031: UDP, length 119   <- forwarded, +89 µs
20:43:07.689938  127.0.0.1.62031 > 127.0.0.1.62034: UDP, length 4
20:43:07.690000  127.0.0.1.62033 > 127.0.0.1.62032: UDP, length 4     <- forwarded, +62 µs
```

**Relay latency: 60–90 µs.** Lengths preserved exactly.

---

## 1. Outbound — PASS

`POST /api/messages`, message displayed on the radio. Confirmed visually by the
operator in both cases.

| | 1 block | max length |
|---|---|---|
| Text | `WAYPOINT TEST 1` (15 units) | 123 units |
| Blocks on air | 6 × `Data 1/2` | 24 × `Data 1/2` |
| Inter-burst pacing | ~61 ms | ~61 ms |
| POST → `sent` | **1.33 s** | **2.05 s** |
| Displayed on the radio | yes | yes, including the trailing `END123` |

```
20:44:05.988 DMR Slot 2, received network data header from KN4OQW to KN4OQW, 6 blocks
20:44:06.049 DMR Slot 2, Data 1/2      (×6)
20:44:06.354 DMR Slot 2, ended network data transmission
```

The pacing figure is the one worth keeping: ~61 ms measured against a 60 ms
`BurstInterval`, so the sender is emitting at the slot period and not faster.

The operator reading back `END123` — the last six characters of the 123-unit
message — is what proves the whole body arrived, not just the start of it.

MMDVM-Host's debug dump caught the first burst on the wire, which lets the
preamble be checked byte for byte against what the codec builds:

```
20:44:05.444 Preamble CSBK
0000:  BD 00 80 10 30 86 AA 30 86 AA 73 0B
20:44:05.444 DMR Slot 2, received network Data Preamble CSBK (16 to follow) from KN4OQW to KN4OQW
```

`BD` is `0x80 | CSBKO 0x3D`; `80` is the data-follows bit with the individual-call
bit clear; `10` is 16 PDUs to follow, which is 9 preambles + 1 data header + 6
body blocks; `30 86 AA` twice is 3180202 as destination then source; `73 0B` is the
CRC-CCITT under the CSBK mask. Every field is what `buildPreambleCSBK` emits.

**The length bound was also exercised for real.** A 124-unit first attempt was
refused before anything was stored:

```
HTTP 413
{"error":"dmrdata: message text too long",
 "hint":"the limit is in UTF-16 code units, so a character outside the BMP costs two",
 "max_units":123}
```

---

## 2. Inbound and coexistence — PASS

One action tested both halves: the radio sent `ECHO` to 262993.

**Captured and stored**, sub-millisecond after the transmission ended:

```
20:53:25.366 DMR Slot 2, received RF data header from KN4OQW to 262993, 5 blocks
20:53:25.663 DMR Slot 2, Data 1/2      (×5)
20:53:25.664 DMR Slot 2, ended RF data transmission

id 3   3180202 -> 262993   'ECHO'   received   2026-08-07T20:53:25.664Z
```

`Peer` is the source and `Local` the addressed destination, as designed for a
message merely crossing the node.

**It still reached BrandMeister**, which is the coexistence half. BM replied 385 ms
later, and a reply is proof of delivery that no packet capture improves on:

```
20:53:26.049 DMR Slot 2, received network data header from 262995 to KN4OQW, 19 blocks
```

29 inbound messages were captured over the session in total, including BM's whole
backlog, with **zero** checksum failures, orphans or no-sync bursts.

Two things fell out of this that were not the point of the test:

- **The ids are clean.** `262995`, not `2262995`. The 2-prefix investigation
  folded into #246 concluded the prefix was a dial-prefix rewrite that
  `effectivePrimaryIndex` had already fixed; this is that conclusion holding on
  live traffic.
- **BM's SMS service answers from 262995**, not the 262993 you send to.

Visible in the UI, read from the live node:

`docs/validation/img/messages-bench.png`

---

## 3. Voice regression through the relay — PASS

Parrot 9990 round trip with the relay in the path.

```
20:57:49.300 DMR Slot 2, received RF voice header from KN4OQW to 9990
20:57:55.601 DMR Slot 2, received RF end of voice transmission from KN4OQW to 9990, 6.1 seconds, BER: 0.1%
20:57:58.784 DMR Slot 2, received network voice header from 9990 to KN4OQW
20:58:04.855 DMR Slot 2, received network end of voice transmission from 9990 to KN4OQW, 6.2 seconds, 0% packet loss, BER: 0.0%
```

**0% packet loss, BER 0.0%** on the network side — identical to the pre-shim
baseline on record (2026-08-05). Audio played back normally.

Frame accounting from a loopback capture of the same transmission:

```
MMDVM-Host -> relay : 104
relay -> DMRGateway : 104
DMRGateway -> relay : 105
relay -> MMDVM-Host : 104
```

The 105/104 is a capture-boundary artefact, not a drop: every `62031 > 62034` is
followed 86–90 µs later by its `62033 > 62032` forward, and the capture file ends
between the last such pair. MMDVM-Host's own `0% packet loss` is the decisive
check — a relay that dropped a voice frame would have shown up there.

---

## 4. Channel discipline — PASS, after a first run that found a real problem

### First run — FAILED, and the failure was informative

A message posted mid-transmission stayed `queued` for the full 60 s
`MaxChannelWait` and then failed:

```
state: failed
reason: 'timeslot 2 has been busy for 1m0s; the channel never went quiet
         (a static talkgroup on a simplex node will do this)'
held: 60.07 s
```

The discipline did its job — **nothing was put on the busy slot** — and the reason
was accurate. What made the channel busy for over a minute is the interesting
part:

```
21:00:12.209 received RF end of voice transmission from KN4OQW to 9990, 37.4 seconds
21:00:12.389 received network Data Preamble CSBK (20 to follow) from 262995 to KN4OQW
21:00:12.511 received network data header from 262995 to KN4OQW, 19 blocks
...
21:00:13.777 received network Data Preamble CSBK (12 to follow) from 262995 to KN4OQW
...
21:00:55.391 ended network data transmission
```

A 37.4 s transmission, and the moment it ended BrandMeister released its backlog
of SMS replies back to back until 21:00:55. Slot 2 was legitimately busy for more
than a minute.

**Two findings, both real:**

1. **`MaxChannelWait` of 60 s is too short.** An ordinary sequence — a long
   transmission followed by a queued reply — exceeds it.
2. **Timing out is terminal, and it should not be.** The message is marked
   `failed` and the operator's text is simply gone. A message that never reached
   the air is exactly the case that should be retryable.

Neither is fixed here; this is an evidence PR. Both are worth a follow-up.

Incidentally confirmed by the same log: **BrandMeister**, not MMDVM-Host, was
holding those replies — its first preamble arrives 180 ms after the RF
transmission ends.

### Second run — PASS

Same test on a quiet channel:

| Time | Event |
|---|---|
| 21:02:22.536 | RF voice header, KN4OQW → 262995 (operator keys up) |
| 21:02:30.396 | `POST /api/messages` → **queued** |
| 21:02:33.516 | RF end of voice transmission, 10.8 s (operator unkeys) |
| 21:02:34.539 | Network data header transmitted, 7 blocks |
| 21:02:34.964 | Ended |

Held **3.9 s** while the slot was busy, transmitted **1.02 s** after it cleared —
the 400 ms idle window plus poll granularity. Nothing was transmitted into the
active call.

---

## 5. Fail-open — MEASURED, and it does not behave as the plan assumed

The plan expected voice and BM to continue via pass-through when the relay's data
plane dies. **They do not, and cannot**, because the relay is in-process: killing
its data plane means killing waypointd, and then there is nothing to pass through.

There is no test hook to kill the data plane alone, so this was measured with
`SIGKILL` to waypointd, which is the realistic version of the same event:

```
21:03:09.553  SIGKILL to waypointd (pid 1318)
              relay sockets bound: 2 -> 0
              waypoint-mmdvm: active   waypoint-dmrgateway: active
21:03:12      systemd restarted waypointd (RestartSec=3)
              dmr relay: relaying 127.0.0.1:62033 <-> 127.0.0.1:62034
21:03:13.027  BM_3103_United_States {"up":true,"state":"up","detail":"logged in"}
```

**DMR outage: ~3.4 s**, self-recovering with no intervention. Both g4klx daemons
stayed `active` throughout and never noticed beyond dropped datagrams.

This is the availability trade flagged when the relay's default was set to off,
now with a number on it: **with the relay on, a waypointd restart costs about
three and a half seconds of DMR.** That is the reason it is opt-in.

The fail-open property that IS implemented — an observer that panics, blocks or
falls behind never delays or prevents a forwarded frame — is covered by unit
tests (`TestAPanickingTapDoesNotDelayForwarding`,
`TestABlockingTapDoesNotDelayForwarding`) and has no live hook. Reported as
untested-on-hardware rather than claimed.

---

## 6. Escape hatch — PASS

`shim_enabled: false`, then Apply:

```
MMDVM-Host  [DMR Network]  LocalPort=62032  GatewayPort=62031
DMRGateway  [General]      RptPort=62032    LocalPort=62031

127.0.0.1:62031  DMRGateway
127.0.0.1:62032  MMDVM-Host      (62033/62034 released)

dmr relay: switched off; the DMR loopback is direct again
```

Both rendered files came back **byte-identical to the pre-run snapshot**. BM
re-logged in on the direct wiring and the loopback resumed carrying traffic. The
relay's link was retracted from the status plane rather than left showing a stale
verdict.

---

## Defects found and fixed during the run

Three, all in the messages page, all found by looking at the rendered page on the
bench rather than by reading the code. Fixed and re-verified against the same
node.

1. **The page reported the relay as switched off while it was transmitting**, under
   a red banner telling the operator to go and enable it. It read
   `dmrnet.shim_enabled` from `/api/config`, and the config view does not expose
   the `dmrnet` section at all. Now reads the relay's link from `/api/status`,
   which answers the better question anyway: whether it is *running*.
2. **The node id rendered as `—`.** It read `general.id`; the view field is
   `general.dmr_id`.
3. Both above were invisible in the local demo node, which has no relay and no
   DMR id, and would have shipped.

## Known gap, not fixed

**The relay cannot be switched on from the UI or the config API.** `dmrnet` is a
store section that the config view does not expose, so there is no control and no
API field. It was set directly in the store for this run. Exposing it — a view
field and a settings control — is its own change.
