# Bot data plane — investigation findings

Ground truth for the bot framework series. Every claim about daemon behaviour
below cites a file and line in the pinned upstream sources; anything that is
inference from reading rather than a statement the source makes is labelled
INFERENCE, and anything that needs the bench to settle is labelled OPEN.

Pinned sources, as built by `waypoint-stack` (`pins.env`):

| Tree | SHA | Note |
|---|---|---|
| MMDVM-Host | `71e598c7c1618aa4ee6f36d955624d6740d79fbc` | Waypoint fork of g4klx `43edd65` |
| DMRGateway | `79edbc43962f25b33d383d59f8bf635d24da74b8` | g4klx |

Citations are `File.cpp:NNN` against those SHAs. The `MMDVMHost/` checkout at the
root of the waypoint working tree is g4klx master (`dea6e9b`) and is *not* what
ships — it is gitignored scratch. Nothing here was read from it.

---

## 0. What already exists (read this before F2 and F3)

The runbook was written as though the data plane were greenfield. It is not.
Most of D-F8 and most of F3's codec work are merged on `main` already, and two
of the runbook's design decisions are contradicted by what is there. This
section is the finding; the rest of the document is the ground truth behind it.

| Runbook item | State on `main` | Where |
|---|---|---|
| D-F8 transparent relay | **Exists**, in-process | `internal/dmrshim` |
| D-F8 OBSERVE hook | **Exists**, as taps | `internal/dmrshim/shim.go:96,240` |
| D-F8 INJECT | **Exists**, both directions | `internal/dmrshim/shim.go:383,389` |
| D-F8 INTERCEPT | **Absent** — see below | — |
| D-F8 render seam, off by default | **Exists** | `internal/config/dmrshim.go`, `render.go:686,1426` |
| F2 relay lifecycle + health | **Exists**, tri-state | `cmd/waypointd/dmrshim.go` |
| F3 burst codec, both directions | **Exists** | `internal/dmrdata` |
| F3 SMS reassembly + CRC | **Exists** | `internal/dmrdata/sms.go`, `crc.go` |
| F3 RX wiring into waypointd | **Exists** | `internal/messages/inbound.go` |
| D10 RF-idle gate | **Exists**, per-slot | `internal/messages/messages.go:404,433` |
| Position decode | **Absent** — and see §D.1, the taps cannot see the packet | — |
| ACK synthesis | **Absent** | — |
| Bot ID range / egress guard | **Absent** | — |

`go test ./internal/dmrdata/... ./internal/dmrshim/... ./internal/messages/...
./internal/config/...` passes at `origin/main` (bccb02f).

### 0.1 D-F8 is wrong about the process model — STOP CONDITION

D-F8 specifies "a small Go daemon (`waypoint-datashim`)" with a rendered config
file and its own systemd unit. The relay that exists is a **package inside
waypointd** (`internal/dmrshim`), reconciled on a 15 s tick from the store
(`cmd/waypointd/dmrshim.go:160`). Its ports are resolved in exactly one place
(`internal/config/dmrshim.go:85`) and consumed by both the INI renderers and the
daemon, specifically so the four ports cannot disagree across an Apply
(`internal/config/dmrshim.go:10-17`).

Building `waypoint-datashim` as a separate daemon would mean a second copy of
that port resolution, a third participant in the Apply ordering, and a new
failure mode where the relay is up but waypointd is not. **Recommendation: drop
the separate-daemon half of D-F8 and keep the in-process relay.** Everything
D-F8 actually wants — observe, inject, a render seam that is off by default — is
already there. What is missing is intercept, and that is a change to the relay's
forwarding path, not a new process.

The MQTT hop D-F8 describes (`<prefix>/datashim/observed`, `/intercepted`,
`/inject`) also disappears with the separate daemon. In-process, a tap is a Go
function call (`shim.go:96`). Putting the local broker in the path of an
intercepted SMS would add latency to the one path that is latency-sensitive —
see §C on the ACK deadline.

### 0.2 The relay cannot intercept today, by explicit design

The forwarding path forwards *first*, then offers a copy to the observers
through a buffered channel that drops when full (`shim.go:304-311, 318-326`).
The package documentation states the property as a guarantee: "a tap cannot stop
a frame" (`internal/messages/inbound.go:17`), and "There is no error the
observation side can raise that stops a frame reaching the other daemon"
(`shim.go:36-38`).

INTERCEPT (D-F3, D-F8) requires the opposite: a frame addressed to a bot ID must
be terminated and *not* forwarded to DMRGateway. That is a deliberate reversal
of a documented invariant, not an addition to it. F2 must implement it as a
classify-before-forward step on the forwarding path itself — synchronous, no
channel — and must keep the existing guarantee for everything it does not
intercept. The distinction matters because the current design's whole argument is
that voice can never be affected by a parsing bug; a synchronous classifier
reintroduces exactly that risk and needs to be written to be incapable of
panicking or blocking (fixed-offset byte reads, no allocation, no map lookup on
the hot path).

The egress guard (D-F3) has the same shape and can share the classifier.

---

## A. The shim seam

**Answered. The seam exists, is rendered, and is off by default.**

### A.1 Wiring

Without the relay, MMDVM-Host and DMRGateway talk directly: MMDVM-Host binds
`[DMR Network] LocalPort` (62032) and sends to `GatewayPort` (62031); DMRGateway
binds `[General] LocalPort` (62031) and sends back to `[General] RptPort`
(62032).

With the relay in the path, each daemon keeps the port it *binds* and changes
only the port it *sends to* (`internal/config/dmrshim.go:19-33`):

```
                 binds 62032                          binds 62031
MMDVM-Host ─── sends to 62033 ──► relay ─── sends to 62031 ──► DMRGateway
           ◄── 62033 sends to ─── 62033/62034 ◄── sends to 62034 ───
```

| Value | Default | Rendered into |
|---|---|---|
| MMDVM-Host bind | 62032 | `MMDVM.ini [DMR Network] LocalPort` |
| relay host-facing bind | 62033 | `MMDVM.ini [DMR Network] GatewayPort` (`render.go:686-689`) |
| relay gateway-facing bind | 62034 | `DMRGateway.ini [General] RptPort` (`render.go:1426-1430`) |
| DMRGateway bind | 62031 | `DMRGateway.ini [General] LocalPort` |

The bus's reserved DMR range (62100–62199) dials DMRGateway directly and is
untouched; the relay's two ports are validated as distinct, in range, and clear
of that range (`internal/config/dmrshim.go:117-141`).

### A.2 Byte-transparency is available, but the source port is not free

A relay can sit here with no protocol awareness at all — the forwarding path
does not parse anything (`shim.go:284-312`). What it may **not** do is reply
from an arbitrary socket.

MMDVM-Host validates the source of every datagram on its DMR network socket
against the configured `GatewayAddress:GatewayPort`:

```cpp
// DMRNetwork.cpp:313
if (!CUDPSocket::match(m_addr, address)) {
    LogMessage("DMR, packet received from an invalid source");
    return;
}
```

`m_addr` is resolved from `GatewayAddress:GatewayPort` (`DMRNetwork.cpp:65`) and
is also the destination it transmits to (`DMRNetwork.cpp:440`). `CUDPSocket::match`
defaults to `IPMATCHTYPE::ADDRESS_AND_PORT` (`UDPSocket.h:64`), which compares
`sin_addr` **and** `sin_port` (`UDPSocket.cpp:116-122`).

DMRGateway does the same thing on its repeater-facing socket, against
`RptAddress:RptPort`:

```cpp
// MMDVMNetwork.cpp:265
if (!CUDPSocket::match(m_rptAddr, address)) {
```

**Consequence, and the answer to "does DMRGateway care about source port
stability?": yes, both ends do.** The relay must receive from and send to each
daemon on that daemon's own socket, so the source address each daemon observes
is the one it was configured to expect. The implementation does this
(`shim.go:268-269` — `pump` sends via the *other* leg's connection to that leg's
peer) and it also drops datagrams from an unexpected source itself, mirroring
the daemons (`shim.go:300-303`). Getting this wrong does not degrade gracefully:
every frame is dropped and the log line is "packet received from an invalid
source", which reads exactly like a dead link.

This is the reason a relay is the only seam available at all. To originate a
burst toward the radio, something must send from `GatewayAddress:GatewayPort` —
so the only way in is to *be* that address (`shim.go:4-13`).

---

## B. Inbound SMS capture — NOT DONE

**ANSWERED 2026-08-17.** A private M-SMS from the 6X2 Pro to 9000001 was
captured off the bench loopback and is committed as
`internal/dmrdata/testdata/capture-radio-tms.txt`, with
`TestReassembleRecordedCaptures` asserting it decodes to the text that was
typed.

The transfer is five preamble CSBKs, one data header and six rate-1/2 blocks,
and the existing codec reads it with every error counter at zero — no new
decoding work was needed for the M-SMS inbound path. Two incidental facts worth
keeping:

- **The radio sends five preambles**, where this package's `DefaultPreambles`
  is nine (`sms.go:15`). Nine is not wrong — it buys a receiver that is not
  already listening more time to open — but the assumption that a radio sends
  that many is now known to be false.
- **A message to an ID that exists nowhere crosses the loopback normally.**
  9000001 answers to nothing and the transfer was still complete and
  well-formed at the shim. That is the premise intercept rests on, now observed
  rather than assumed.

The tooling built for this is described below — see
[bench-f1-captures.md](bench-f1-captures.md) for the procedure, and
`tools/dmrdcapture` for the capture driver and the pcap→fixture converter.

Two corrections to what was believed about the bench, both verified 2026-08-17:
`tcpdump` **is** installed (an older note said apt was restricted and nothing on
the box could capture packets), and the node is **already running the relay** —
`GatewayPort=62033` / `RptPort=62034`, four sockets confirmed with `ss`, up
since Aug 12. So the loopback is in its shimmed wiring today and every burst
crosses the wire twice, once per leg. That is measured, not predicted: a probe
capture showed each frame as both `62032->62033` and `62034->62031`.

What can be said without it:

The tree already carries two real captures of the same layer, committed in the
documented format (`# <data-type from the DMRD flags byte> <33 bytes hex>`, one
burst per line):

- `internal/dmrdata/testdata/capture-radio-etsi.txt` — **radio → network**,
  three messages, ETSI DMR-Standard framing on port 5016, no TMS header. This is
  a 6X2 Pro with its channel SMS format set to *DMR Standard* rather than M-SMS.
- `internal/dmrdata/testdata/capture-brandmeister.txt` — BrandMeister → radio,
  Motorola TMS on port 4007.

So an inbound capture of the *ETSI* dialect from this radio already exists and
already decodes (`internal/dmrdata/dialect_test.go`). What does **not** exist is
a capture of the radio sending **M-SMS** (TMS, port 4007) inbound, which is the
dialect the runbook's test plan specifies and the one the bot path will most
often meet.

B therefore reduces to a narrower ask than the runbook assumed: one capture,
radio → 9000001, with the channel's SMS format set to **M-SMS**, to confirm the
inbound TMS parse and to answer §C. The existing README format and sanitisation
convention (own RadioID only) applies.

Note for whoever runs it: the addressed ID does not need to exist for the
capture. The frames cross the loopback regardless — that is the whole premise of
intercept.

---

## C. The confirmed-data / ACK question (D-F9) — ANSWERED: UNCONFIRMED

**Settled 2026-08-17 by the §B capture. The radio sends unconfirmed data, so
D-F9 is moot and INTERCEPT is unblocked.**

The evidence is the decode itself rather than a field read by eye.
`parseDataHeader` accepts only `DPFUnconfirmedData` and returns
`ErrUnsupportedPDU` for `DPFConfirmedData` (`header.go:78-80`), and the
reassembler counts that as `Unsupported` and drops the transfer. The captured
message decoded with `Unsupported: 0` and `Messages: 1`, which is only possible
for an unconfirmed transfer.

What follows:

- **F3 keeps only the `AckFor(tx) → nil` seam.** No ACK synthesis.
- **No confirmed-data reassembler is needed** — the per-block CRC-9 and ARQ work
  described in §C.1 below does not have to happen. F3 stays the size the runbook
  budgeted.
- **INTERCEPT may be built.** The reason to hold it — that terminating a
  confirmed frame removes the only party that could answer it — does not apply.

Two caveats to carry forward rather than forget:

1. This is one radio in one codeplug state. **Confirmed Data is a per-channel
   setting on the 6X2 Pro**, so another operator, or this one on another
   channel, can still present a confirmed transfer. The gatekeeper should count
   `Unsupported` and surface it rather than dropping silently — that counter is
   the difference between "nobody messaged the bot" and "somebody did and we
   could not read it".
2. The radio's display was not observed under an intercepted (unanswered)
   transfer, because nothing intercepts yet. With unconfirmed data there should
   be nothing to observe, but the first INTERCEPT build should check it on the
   bench rather than infer it.

The rest of this section is the reasoning that was open before the capture, kept
because caveat 1 keeps it live.

What the source says: MMDVM-Host does not participate. Its network→RF data path
regenerates FEC and forwards (`DMRSlot.cpp:1357-1409`, `1776-1828`); there is no
ACK state machine, no response PDU construction, and no retry accounting
anywhere in `DMRSlot.cpp`. Whatever a confirmed-data transfer needs, MMDVM-Host
will not supply it — it relays. So if the radio wants a Tier II response, the
responder has to be us, at the shim.

What the existing code assumes: the TX path stamps a sequence number into the IP
identification field and the TMS message number and states plainly that
"nothing acknowledges it" (`internal/dmrdata/sms.go:29-31`). That is a statement
about the *outbound* path (Waypoint → radio) and is evidence that the radio
accepts an unacknowledged inbound message for display — messages built this way
demonstrably show on the 6X2 Pro. It is **not** evidence about what the radio
expects when *it* is the sender.

Why this is the load-bearing unknown: if the radio sends bot-addressed SMS as
confirmed data and we intercept it, we have removed the only party that could
ever have answered — DMRGateway and the network never see the frame. The radio
then retries and shows a failure, and the bot's reply (if any) arrives amid the
retries. That is a worse user experience than not intercepting at all, and it is
invisible in any test that does not involve the radio.

**Do not build INTERCEPT until §B is captured and §C is answered.** If the
answer is "confirmed", the ACK synthesis is F3 item 2 and the latency budget is
real (see §0.1 — this is why the intercept classifier should not take an MQTT
round trip). If the answer is "unconfirmed", D-F9 is moot and F3 keeps only the
`AckFor(tx) → nil` seam.

### C.1 If the answer is "confirmed", F3 is much larger than budgeted

Worth stating separately, because D-F9 frames this as an ACK question and it is
not only that. Confirmed data is a distinct data packet format, not a flag on
the one already supported:

```go
// internal/dmrdata/header.go:30-33
DPFUnconfirmedData DPF = 0x02 // the only format this package builds
DPFConfirmedData   DPF = 0x03 // per-block CRC-9 and an ARQ exchange; declined
```

`parseDataHeader` returns `ErrUnsupportedPDU` for it (`header.go:78-80`), and
the reassembler counts it as `Unsupported` and drops the transfer
(`sms.go:139`). So if the radio sends bot-addressed SMS as confirmed data, the
current codec **cannot read it at all** — the work is per-block CRC-9 and the
ARQ exchange, not a response PDU bolted onto the existing path.

`tools/dmrdcapture -decode` reports exactly this distinction off the §B capture,
so the answer costs one command once the capture exists. See
[bench-f1-captures.md](bench-f1-captures.md) §C.1.

The 6X2 Pro's per-channel *Confirmed Data* setting should be recorded in the
capture README either way, because it changes the answer and it is a codeplug
field, not a protocol constant.

---

## D. Positions — the transport, and what Waypoint cannot see

### D.0 There are TWO position transports, and the runbook names the wrong one

This is the most consequential finding for F5, and it was reached by reading the
pinned sources rather than by capture.

**Transport 1 — embedded in the voice superframe.** A radio puts its position in
the embedded LC of a *voice* transmission, alongside the talker alias, as
`FLCO::GPS_INFO`. MMDVM-Host reassembles it and hands it to the network:

```cpp
// DMRSlot.cpp:726-732 (RF side; the network side at 1556 only logs)
case FLCO::GPS_INFO:
    if (m_dumpTAData) { ...dump...; logGPSPosition(data); }
    if (m_network != nullptr)
        m_network->writeRadioPosition(m_rfLC->getSrcId(), data);
```

Note the forward is **outside** the `m_dumpTAData` guard, so it happens whatever
the node's logging configuration says. Waypoint renders `DumpTAData=0`, which
suppresses the log line and changes nothing else.

That emits a 14-byte packet that is **not** a DMRD frame:

```cpp
// DMRNetwork.cpp:255-268
::memcpy(buffer + 0U, "DMRG", 4U);
buffer[4U] = id >> 16; buffer[5U] = id >> 8; buffer[6U] = id >> 0;
::memcpy(buffer + 7U, data + 2U, 7U);
return write(buffer, 14U);
```

DMRGateway reads it (`MMDVMNetwork.cpp:279`), and `processRadioPosition`
forwards it to whichever network the active call is routed to
(`DMRGateway.cpp:1292-1311`) — the condition is an in-progress call, because
this position rode inside one. BrandMeister decodes it from there. **This is how
BrandMeister APRS works**, and it is entirely upstream: there is no Pi-Star or
WPSD code in the path, only configuration Waypoint already renders.

**Transport 2 — a standalone short-data call** to an APRS ingest id (the
310999/900999 question). This is an ordinary data PDU and would appear as DMRD
bursts like any message.

### D.1 STOP CONDITION for F5: the tap layer is blind to DMRG and DMRA

`dmrdBurst` requires the `"DMRD"` magic before it will look at a datagram
(`internal/messages/inbound.go:247-255`). `DMRG` (position) and `DMRA` (talker
alias) are relayed byte-for-byte by the shim and are **invisible to every tap**.

So D-F6 as written — observe private data calls to an ingest id — would capture
transport 2 and silently miss transport 1, which is the one a stock Anytone or
BTECH radio on BrandMeister actually uses. F5 must tap `DMRG` directly. That is
cheaper than the data-PDU path, not harder: no reassembly, no FEC, no CRC, just
a 14-byte packet with the source id and seven bytes of position.

### D.2 The decoder is in the pinned source

§D's stop condition asked for a citable open decoder before assigning any field
a meaning. There is one, and it is closer than the HBLink3 lineage the runbook
suggested — MMDVM-Host decodes this itself:

```cpp
// DMRSlot.cpp:1836-1879, operating on the 9-byte embedded block
errorI    = (data[2] & 0x0E) >> 1;                  // 3-bit position error class
longitudeI = ((data[2] & 0x01) << 31) | (data[3] << 23) | (data[4] << 15) | (data[5] << 7);
longitudeI >>= 7;                                    // sign-extended 25-bit
latitudeI  = (data[6] << 24) | (data[7] << 16) | (data[8] << 8);
latitudeI  >>= 8;                                    // sign-extended 24-bit
longitude = longitudeI * 360.0F / 33554432.0F;       // 360/2^25
latitude  = latitudeI  * 180.0F / 16777216.0F;       // 180/2^24
```

Error classes: 0 `<2m`, 1 `<20m`, 2 `<200m`, 3 `<2km`, 4 `<20km`, 5 `<200km`,
6 `>200km`, 7 unknown.

Mind the offsets. `writeRadioPosition` copies `data + 2U` for 7 bytes, so a
`DMRG` packet's payload begins at what this decoder calls `data[2]` — the seven
bytes are `data[2..8]`. A decoder written against the DMRG packet must shift the
indices, and getting that wrong yields a plausible-looking wrong position rather
than an obvious failure.

### D.3 Capture — NOT DONE, and not blocked on Waypoint

**Blocked for the same reason as §B** — it needs the radio beaconing and a hand
on the bench box.

One thing worth settling before that session, because it changes the design:
the runbook's D-F6 says positions are observed "passively at the shim (frames
continue upstream untouched so BM APRS keeps working)". That is consistent with
the relay as built — a tap sees a copy and cannot stop the frame
(`shim.go:304-311`) — so OBSERVE for 900999 needs no new mechanism at all, only
a tap that filters on destination. Good: the observe half of D-F8 is free.

The decode is the open part. `internal/dmrdata` decodes the *transport* (BPTC,
slot type, CRC, reassembly) for any data burst, so a 900999 transfer will
reassemble into bytes today; what is missing is the payload format above it.
The runbook's instruction stands and is the right one: match it against a
citable open decoder (the HBLink3 / KF7EEL `gps_data` lineage) before assigning
any field a meaning, and ship a partial decode with unknown bytes mapped rather
than a guess.

---

## E. Net → RF group data

**Answered, and the answer is more permissive than the runbook feared.**

### E.1 Group data passes network→RF exactly like private data

`CDMRSlot::writeNetwork` (`DMRSlot.cpp:1127`) handles the network→RF direction.
Its data-header branch (`DMRSlot.cpp:1357-1409`) reads the group/individual flag
out of the **embedded data header** and uses it for exactly two things — the
short LC and the log line:

```cpp
// DMRSlot.cpp:1368
bool gi = dataHeader.getGI();
...
// DMRSlot.cpp:1397
setShortLC(m_slotNo, dstId, gi ? FLCO::GROUP : FLCO::USER_USER, ACTIVITY_TYPE::DATA);
```

There is no branch on `gi` that drops, rewrites, or restricts anything. The
block branch (`DMRSlot.cpp:1776-1828`) handles `DT_RATE_12_DATA`,
`DT_RATE_34_DATA` and `DT_RATE_1_DATA` identically to each other and does not
look at addressing at all.

### E.2 There is no access control in the network→RF direction

Every `CDMRAccessControl` call site in `DMRSlot.cpp` — lines 251, 259, 425, 432,
500, 507, 886, 894 — lies inside `CDMRSlot::writeModem` (`DMRSlot.cpp:144`),
which is RF→network. `CDMRSlot::writeNetwork` (`DMRSlot.cpp:1127-1835`) contains
none. Network→RF traffic is not ACL'd by MMDVM-Host at all.

**So D9's group-broadcast mode is viable as far as MMDVM-Host is concerned**, and
W5 item 6's fallback ("if F1 found MMDVM-Host drops net→RF group data: lastheard
only") is not needed. Whether a *radio* opens for it is a separate question that
only the bench answers — that is HV step 9, and the RX-group-list caveat in the
runbook is the right worry.

### E.3 What must agree between DMRD byte[15] and the embedded header

The DMRD wrapper is parsed in `CDMRNetwork::read` (`DMRNetwork.cpp:141-181`):

| Field | Bits | Source |
|---|---|---|
| srcId | bytes 5–7 | `DMRNetwork.cpp:141` |
| dstId | bytes 8–10 | `DMRNetwork.cpp:143` |
| slot | byte 15 bit 7 (`0x80`) → slot 2 | `DMRNetwork.cpp:145` |
| FLCO | byte 15 bit 6 (`0x40`) **set = USER_USER (private)** | `DMRNetwork.cpp:157` |
| data sync | byte 15 bit 5 (`0x20`) | `DMRNetwork.cpp:165` |
| voice sync | byte 15 bit 4 (`0x10`) | `DMRNetwork.cpp:166` |
| data type | byte 15 low nibble, when data sync | `DMRNetwork.cpp:169-171` |

Note the polarity: bit 6 **set** means *private*, clear means *group*. It is the
inverse of the name "group flag" and is an easy bug to write.

The answer to the runbook's question is: **for the data-header burst,
they do not have to agree, because MMDVM-Host does not compare them.** The
network→RF data path uses only the embedded header's `getGI()`
(`DMRSlot.cpp:1368`); `dmrData.getFLCO()` is never consulted in that branch. Contrast
the voice path, which *does* compare and logs a warning on mismatch
(`DMRSlot.cpp:1157-1159`).

What byte 15 does control in the data direction is **branch selection**: the low
nibble picks the data type, and getting the sync bit or nibble wrong sends the
burst down the voice path. It also controls slot selection, which brings a real
hazard — see §G.2. The existing TX path sets these correctly
(`internal/messages/messages.go:560-566`).

INFERENCE, flagged as such: because MMDVM-Host trusts the embedded header for
addressing but DMRGateway's rewrite rules match on the *wrapper*, a frame whose
two disagree would route one way and display another. Nothing in the bot path
sends through DMRGateway (the shim injects toward MMDVM-Host directly), so this
is a trap for future work rather than a live issue. Keep them consistent anyway.

---

## F. Last-heard and RF idle

### F.1 The idle gate exists and D10 should consume it, not reimplement it

`internal/messages` already gates transmission on a quiet timeslot
(`messages.go:404-420`), measuring idleness per slot from the relay's own tap
(`messages.go:433-444`). Two properties of it are worth preserving into the
shared package F4 extracts:

- Idle is counted from **whichever is later**, the last burst seen on the slot or
  the moment the tap started watching. An unobserved slot is not a quiet slot
  (`messages.go:425-429`).
- A service with no tap attached reports **zero** idle time, so it never claims
  the channel is free when it cannot see it (`messages.go:431-432`).

Both were learned the hard way and the comments say so. F4's extraction should
move this code, not rewrite it.

### F.2 MMDVM-Host itself drops network frames while RF is active

This is the ground truth *under* D10, and it is stronger than "be polite":

```cpp
// DMRSlot.cpp:1132
if ((m_rfState != RPT_RF_STATE::LISTENING) && (m_netState == RPT_NET_STATE::IDLE))
    return;
```

A network frame arriving while the slot is busy with RF and no network
transmission is already running is **discarded silently**. For a multi-burst SMS
this is fatal rather than degrading: the data header is dropped, and every
following block then hits `if ((m_netState != RPT_NET_STATE::DATA) || (m_netFrames
== 0U)) { writeEndNet(); return; }` (`DMRSlot.cpp:1777-1780`) and is dropped too.
The message vanishes with no error anywhere and the sender is told it was sent —
which is precisely the failure `messages.go:425-429` documents having hit.

D10's hold-off is therefore not a courtesy. It is the only thing standing
between a bot reply and silent loss.

### F.3 Last-heard does not carry DMR IDs — a real gap for D9

The last-heard list is derived from the events store, and the events table's
`source` column is TEXT (`internal/events/events.go:88`). It is populated
preferring the *resolved* name:

```go
// internal/mqtt/bridge.go:116
e.Source = firstNonEmpty(f.SrcInfo, f.SrcCall, idString(f.SrcID))
```

So the numeric DMR ID survives only when the lookup found nothing. The public
projection then filters to publishable callsigns
(`internal/publicview/service.go:219-224`).

**Consequence for D9's `lastheard` fan-out mode:** it needs DMR IDs to address
private SMS, and the store does not reliably have them. Whoever implements D9
must either add a numeric ID column to the events schema (a migration) or track
heard IDs separately off the relay tap — which is cheap, since the tap already
sees `srcId` in every DMRD frame at bytes 5–7. The second is probably right, and
it also serves D-F6/positions correlation, but it is a decision W5 must make
knowingly rather than discover.

---

## G. The injection point

**Answered: a frame injected at the shim is indistinguishable from a
DMRGateway-originated one, with two caveats that will bite.**

### G.1 Why it is identical

MMDVM-Host's only test of provenance is the source address:port check at
`DMRNetwork.cpp:313`. `Shim.InjectToHost` writes from `hostConn` — the socket
bound to `HostBind`, which the renderer has already written into MMDVM-Host's
`GatewayPort` (`render.go:686-689`) — to `hostPeer` (`shim.go:383-384`,
`393-408`). It therefore passes `CUDPSocket::match` for the same reason relayed
frames do. Past that check the datagram enters `CDMRNetwork::read` and
`CDMRSlot::writeNetwork` with no record of how it arrived; there is no session,
sequence or stream validation in that path that distinguishes an injected frame
from a forwarded one.

The `InjectToGateway` direction is symmetric via `MMDVMNetwork.cpp:265`.

### G.2 Caveat 1: slot disabling drops injected frames before anything sees them

`CDMRNetwork::read` returns false — silently — for a frame on a disabled slot:

```cpp
// DMRNetwork.cpp:147-154
// DMO mode slot disabling
if (slotNo == 1U && !m_duplex)
    return false;
// Individual slot disabling
if (slotNo == 1U && !m_slot1) return false;
if (slotNo == 2U && !m_slot2) return false;
```

Two ways to lose every bot message with no log line anywhere:

1. **Simplex/DMO nodes reject slot 1 outright** (`!m_duplex`). A hotspot must be
   injected on slot 2.
2. **`Slot1=0` in `MMDVM.ini` drops all TS1 network traffic.** This is already a
   known open parity gap for Waypoint's rendered config.

The gatekeeper (D-F5) should pick the injection slot from the rendered
`[DMR Network]` `Slot1`/`Slot2` and duplex settings rather than defaulting, and
should refuse to transmit — visibly — when neither slot is available. A drop
here produces no evidence at all.

### G.3 Caveat 2: the RF-busy drop applies to injected frames too

`DMRSlot.cpp:1132` (§F.2) does not care where the frame came from. Injection
without the idle gate loses the message silently. Restated because it is the
single most expensive thing to rediscover.

### G.4 The 264-bit rule still applies

Not a new finding, but it belongs with the injection contract: `CBPTC19696::encode`
writes only the 196 payload bits, so a burst built into a zeroed buffer has no
sync and a slot type of zero, and MMDVM-Host reads that back and retransmits it
as data type 0 — unreassemblable by any radio. Every burst-producing function in
`internal/dmrdata` writes all 264 bits and
`TestBurstWritesEvery264Bits` fails if one stops
(`internal/dmrdata/dmrdata.go:22-30`). Anything F3 adds on the inject path
inherits this requirement.

---

## Summary of stop conditions and recommendations

1. **D-F8's separate `waypoint-datashim` daemon should be dropped** in favour of
   the in-process relay that exists. §0.1.
2. **INTERCEPT reverses a documented invariant** of the relay and must be built
   as a synchronous classify-before-forward step that cannot panic or block.
   §0.2.
3. **INTERCEPT is unblocked.** §C is answered: the radio sends UNCONFIRMED data,
   so terminating the frame strands nobody and no ACK is owed. Count
   `Unsupported` anyway — confirmed data is a per-channel radio setting and
   another operator can still present one. §C.
4. **D9 group mode is not blocked by MMDVM-Host** — there is no network→RF access
   control. W5 item 6's lastheard-only fallback is unnecessary. §E.2.
5. **D9 lastheard mode is blocked by the events schema** — last-heard stores
   resolved callsigns, not DMR IDs. §F.3.
6. **The gatekeeper must choose the injection slot from the rendered config**, and
   refuse visibly when no slot is available. §G.2.
6a. **F5 must tap DMRG, not just data calls to an ingest id.** Positions from a
   stock radio on BrandMeister ride inside voice transmissions and leave
   MMDVM-Host as a 14-byte DMRG packet that no existing tap can see, because
   every tap filters on the DMRD magic first. §D.1.
7. **F4 should extract the existing idle gate, not rewrite it.** §F.1.
8. **F3 does not need a confirmed-data reassembler.** §C came back unconfirmed,
   so the per-block CRC-9 and ARQ work is not on the critical path. §C.1.

Bench status: **§B and §C are done**; the fixture is committed and the codec
reads it. **§D remains open and is blocked at the radio**, not in Waypoint —
three capture windows over six PTT releases produced no position report at all,
which puts it in the 6X2 Pro's per-channel APRS binding. It should not hold up
F2 or F3. The procedure, the radio settings and the exact commands are in
[bench-f1-captures.md](bench-f1-captures.md).

The runbook's **900999 is unsupported by any evidence** and has been dropped
from these documents: nothing in the tree asserts it, the operator's radio is
set to 310999, and no beacon has yet been observed to settle it. F5's observe
set must take the ID from a capture, not from the runbook.

One live hazard that procedure calls out and is worth repeating here: the bench
renders `Slot1=0`, so **everything must happen on timeslot 2** or MMDVM-Host
drops it before anything logs (§G.2).
