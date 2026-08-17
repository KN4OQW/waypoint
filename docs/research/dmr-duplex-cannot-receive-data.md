# A duplex MMDVM node cannot receive short data while idle

Established on hardware 2026-08-17, against the pinned MMDVM-Host
(`71e598c`) and the KN4OQW MMDVM_HS fork (`bf66faf`). This is an
architectural property, not a bug, and it decides how inbound bot traffic has
to be designed.

## The claim

On a node running `Duplex=1`, a short DMR data transmission — a text message, a
position report, a message to a bot — is received **only if the modem happens to
be transmitting when it arrives**. On an idle node it is discarded, silently,
and no amount of host-side configuration changes that.

## The evidence chain, in the order it closes

### 1. The modem picks its receiver by whether it is transmitting

`IO.cpp:206-213`:

```cpp
case STATE_DMR:
#if defined(DUPLEX)
    if (m_duplex) {
      if (m_tx)  dmrRX.databit(bit, control);   // full slot receiver
      else       dmrIdleRX.databit(bit);        // idle receiver
    } else
      dmrDMORX.databit(bit);                    // simplex: everything
```

### 2. The idle receiver forwards only CSBKs

`DMRIdleRX.cpp`:

```cpp
if (colorCode == m_colorCode && dataType == DT_CSBK) {
    frame[0U] = CONTROL_IDLE | CONTROL_DATA | DT_CSBK;
    serial.writeDMRData(false, frame, DMR_FRAME_LENGTH_BYTES + 1U);
}
```

Data headers and rate-1/2 payload never leave the modem. Observed directly: with
the node's colour code corrected to match the transmitting radio, the preambles
arrived and the payload did not.

```
Preamble CSBK  x5
Debug: DMR RX discarded (colour code, where<<8|type): 1 262   // idle RX, DT_DATA_HEADER
Debug: DMR RX discarded (colour code, where<<8|type): 1 263   // idle RX, DT_RATE_12_DATA
```

### 3. And the host only accepts ONE kind of CSBK from that path

This is the fact that closes the door, and it was nearly missed. The idle
receiver's output has exactly one consumer, `CDMRControl::processWakeup`
(`DMRControl.cpp:48-77`):

```cpp
// Wakeups always come in on slot 1
if (data[0U] != TAG_DATA || data[1U] != (DMR_IDLE_RX | DMR_SYNC_DATA | DT_CSBK))
    return false;
...
CSBKO csbko = csbk.getCSBKO();
if (csbko != CSBKO::BSDWNACT)
    return false;
```

So the path accepts only a **BS Downlink Activate**. A data preamble is CSBK
opcode `0x3D` (`csbkoPreamble`), not `BSDWNACT`, so even the preambles that do
reach the host are rejected there. **They wake nothing.**

The idle receiver is a repeater wake-up detector. It is not a degraded data
path, and it was never meant to be one.

### 4. Why the obvious firmware patch does not work

Relaxing `DMRIdleRX` to forward every data type moves the drop from the modem to
`processWakeup`, which rejects anything that is not a `BSDWNACT` CSBK. The frames
would die one layer higher with the same result. A working version would have to
change MMDVM_HS **and** MMDVM-Host together, and would still have to solve two
further problems:

- `DMRIdleRX` has no slot context at all. Its buffer is not split per timeslot
  and it always calls `writeDMRData(false, ...)`, so every burst it forwarded
  would be attributed to one slot regardless of where it actually arrived.
- `DMRSlotRX` aligns to slot boundaries using a delay derived from the
  transmission (`m_delay`, `setDelay`), so it cannot simply be fed while idle.

This was checked before building, and the check is why nothing was built.

## The fix: run the node simplex

`CDMRDMORX` handles `DT_DATA_HEADER`, `DT_RATE_12_DATA`, `DT_RATE_34_DATA`,
`DT_RATE_1_DATA`, the voice types and CSBKs alike (`DMRDMORX.cpp:113-152`),
gated on nothing but the colour code. `IO.cpp` selects it whenever `m_duplex` is
false, and the shipped firmware already contains both paths — no reflash.

A hotspot is normally simplex. This node was configured duplex with a 5 MHz
split, which is a repeater's arrangement, and it is what put the idle receiver in
the path.

## What this means for the bot framework

**Inbound bot traffic has exactly this problem.** A message to a bot is a short
data transmission, and on a duplex node an idle modem discards it. Three things
follow:

1. **The framework should require, or at least strongly prefer, a simplex node
   for inbound features.** That is a documented requirement, not an assumption to
   leave for an operator to discover.
2. **Readiness should say so.** A node with `Duplex=1` and any inbound data
   feature enabled — bots, inbound messaging, positions — is a configuration that
   cannot work, and the panel can say that at save time with the citations above
   rather than letting an operator debug silence.
3. **Outbound is unaffected.** Transmission does not go through any of this, and
   the outbound SMS path is already proven on this node. A broadcast-only feature
   sidesteps the entire issue.

## The colour code, separately

The same investigation found the operator's radio transmitting position reports
on colour code 1 while the node was configured for 2, discarded at
`DMRSlotRX.cpp:177` / `DMRIdleRX.cpp` with no counter and no log until this
series added one. On an Anytone or BTECH radio the digital APRS system takes its
frequency, colour code AND timeslot from a designated APRS channel rather than
from the channel in use, which is how the two came to disagree without the
operator changing anything.

Production implication: **an operator cannot see a colour code mismatch from any
surface the node owns.** The diagnostic added in the MMDVM_HS fork
(`writeDMRDiscarded`) is what makes it visible, and something equivalent needs to
reach the panel before this is shipped to people who will not have a software
defined radio to hand.
