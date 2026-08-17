# Why a transmission can vanish with no trace on the node

Opened while chasing a position beacon that the node never saw. The beacon is
still unexplained; this document records what was *established* along the way,
because two of the findings are load-bearing for the bot framework regardless of
how the beacon turns out.

Status of the beacon itself: **UNRESOLVED.** See "What is not established".

## The two silent gates in the modem firmware

Both live in `MMDVM_HS` (KN4OQW fork, `bf66faf`), in the duplex receive path
`CDMRSlotRX`. Both discard a burst *before* anything host-side exists, so no
amount of instrumentation in waypointd, MMDVM-Host, the loopback or MQTT can
observe them.

### Gate 1 — sync correlation

`correlateSync()` tests exactly two patterns (`DMRSlotRX.cpp:374,398`):

```cpp
if (countBits64((m_patternBuffer & DMR_SYNC_BITS_MASK) ^ DMR_MS_DATA_SYNC_BITS) <= MAX_SYNC_BYTES_ERRS) {
...
} else if (countBits64((m_patternBuffer & DMR_SYNC_BITS_MASK) ^ DMR_MS_VOICE_SYNC_BITS) <= MAX_SYNC_BYTES_ERRS) {
```

`DMRDefines.h:60-67` defines eight patterns — MS, BS, S1 and S2, each in data and
voice flavours. The duplex path correlates only the two MS ones. A burst carrying
direct-mode (S1/S2) or base-station sync never correlates and is never seen.

### Gate 2 — colour code

Inside the data branch (`DMRSlotRX.cpp:177`, and `:279` for slot 2):

```cpp
slotType.decode(frame1 + 1U, colorCode, dataType);
if (colorCode == m_colorCode) {
   ...
}
// no else
```

There is no else branch, no counter and no debug output. A mismatched colour code
is discarded in silence.

Note the asymmetry with voice: the `CONTROL_VOICE` branch immediately below
applies **no colour code check at all**. So a colour-code mismatch suppresses
data — headers, rate-1/2 payload, CSBK preambles — while voice-sync bursts still
reach the host.

## Why this matters to the bot framework

A user whose radio disagrees with the node on colour code, or which transmits in
direct mode, gets **no diagnostic anywhere**. Their message to a bot does not
appear in the loopback capture, the events database, the MQTT stream, or any log.
It is indistinguishable from never having sent one.

That is worth designing for. "Nobody messaged the bot" and "somebody did and the
modem discarded it" look identical from every surface Waypoint owns, and the
second is a support case that currently has no evidence trail at all.

## Instrument note: everything on the node is downstream of these gates

During this investigation four independent-looking instruments agreed that the
radio was not transmitting: the loopback capture, the events database, a
`mosquitto_sub` watch on `mmdvm/#`, and an idle-window capture. All four were
wrong, and they were wrong together, because every one of them sits above the
firmware. An SDR showed the transmissions plainly.

The lesson generalises: independent instruments are not independent if they share
an upstream dependency. When the question is "did the radio transmit", only an
instrument off the air can answer it.

## Method: reading a burst off the air

`~/dmr-sdr-work/dmrdemod.py` (not committed; scratch) demodulates an `rtl_sdr`
capture and correlates against all eight sync patterns, then hands the 264-bit
burst to this tree's own `dmrdata.ParseBurst` for the colour code and data type.

Two things cost real time and are worth writing down:

- **Channel-filter before the discriminator.** Demodulating at the full capture
  bandwidth throws away about 19 dB (960 kHz vs a 12.5 kHz channel). The tell was
  deviation estimates of 12-52 kHz for a signal whose peak deviation is 1944 Hz.
- **Calibrate the slicer on the sync.** Scaling levels from the whole burst is
  skewed by the payload and misclassifies the inner +/-1 symbols, which yields
  the confusing signature of a sync matching at 0 bit errors over a payload the
  BPTC decoder calls unfixable. Fitting gain and offset to the 24 known sync
  symbols roughly halved the FEC correction count and fixed one burst's colour
  code from 14 to 2.

`ParseBurst` reporting `unfixable` is the honesty check on the whole chain: it
refuses to certify bits that do not survive their own FEC.

## What IS established

- The radio transmits on 438.800 MHz — the node's `RXFrequency` — at intervals of
  30-31 s, measured across five separate captures at roughly 14 sigma.
- The node records nothing for those transmissions, in any surface it owns.
- At TURBO power with a better antenna, two bursts correlated MS_DATA at **0 bit
  errors**, and both decoded a slot type of **colour code 2** — matching the node.

## What is NOT established

Stated plainly so nobody builds on it:

- **Which gate closes on the beacon, or whether either does.** The bursts at the
  30-31 s cadence have never correlated against any sync pattern; that may mean
  they use an untested sync, or merely that they were too weak to demodulate.
  Those are very different conclusions and the data does not separate them.
- **The beacon's colour code.** The two bursts that decoded to CC 2 were not
  positively identified as beacons — that capture had no control transmission,
  because a storm took the node's power down mid-run.
- **Any payload.** No burst has yet produced a BPTC payload its own FEC accepts,
  so no addressing has been read off the air.

The gate for the next session: **no conclusion about the beacon until a control
transmission — one the node logs as `rf_voice` — decodes cleanly in the same
capture.** That rule has already caught two wrong conclusions here.
