# RFC-0021: Guided Calibration

- Status: **accepted** (2026-07-28)
- Author: KN4OQW
- Implements requirements: #20 (first-run wizard drives a guided RX/TX offset sweep with live BER for hotspot boards, a level/invert workflow for repeater boards, results in the config store with provenance)
- Depends on: [RFC-0020](0020-board-identity-and-detection.md) / `internal/modem` — the board table, the raw-termios serial layer, the MMDVM frame codec and the port-ownership rule, all reused verbatim; [RFC-0019](0019-firmware-flashing.md) (the one-hardware-operation-at-a-time token, and the two-stream progress split); [RFC-0006](0006-connection-profiles.md) (calibration is never captured in a profile, and this RFC is why that exclusion matters); [RFC-0001](0001-config-store.md) (machine-written state outside the section map)

## Summary

A new hotspot that decodes nothing is almost always a **reference oscillator that
is a few hundred Hz off**, and the state of the art for fixing it is a wiki page,
an SSH session, and a terminal program driven by single keystrokes. Waypoint makes
it an **operation the node performs on itself**: pick the mode, key your radio when
asked, and watch the bit error rate fall as the node sweeps its own frequency and
keeps the offset that won.

The engine is **native Go against the MMDVM serial protocol** — the same protocol
`internal/modem` already frames to ask a board what it is. Waypoint does not drive
`MMDVMCal`. That is a deliberate reversal of the wording in #20, the README and
`architecture.md`, and §1 is the argument for it.

The safety model is stated before the mechanism, because this is the first thing
Waypoint has ever built that can key a transmitter. Two properties carry it: the
receive sweep — the flow an operator actually uses — **cannot key the radio at
all**, as a property of the firmware rather than a promise of ours; and every path
that can key is bounded by a dead-man timer inside the engine, not by a browser
staying open.

## Motivation

A hotspot's reference oscillator is specified in parts per million, and the number
that matters is what that becomes on the air. A ±2 ppm TCXO at 438 MHz is ±876 Hz.
The ±10 ppm parts that ship on cheap boards are ±4.4 kHz — far outside what a DMR
demodulator will tolerate. The board transmits perfectly happily; nothing decodes
it, and nothing anywhere says why.

Every incumbent answer to this is a variation on "read the wiki, then run
MMDVMCal":

    ssh pi-star@pi-star.local
    sudo pistar-mmdvmcal
    (press E, type a frequency, press b, hold PTT, press F, press F, press f, ...)
    (write the number on paper, put it in Expert → MMDVMHost → Modem → RXOffset)

Every step of that is a place to lose the number. The operator is asked to
transcribe a measurement by hand into a config field, in a different program, after
a procedure whose stopping condition ("BER stops improving") they have to judge by
eye from a scrolling terminal. Nothing records that the node was ever calibrated,
what it measured, or against which board — so when the offset is wrong six months
later there is no way to tell a drifted TCXO from a value someone typed wrong.

Nothing automates this anywhere. It is the single most common "my new hotspot does
not work" report on every forum, and it is a measurement a computer attached to the
radio is strictly better at than a human reading numbers off a terminal.

## Design

### 1. Native Go, not MMDVMCal

#20 says "drives MMDVMCal", and the intent behind that — do not reinvent the
community's calibration procedure — is right and is kept. What changes is who
executes it, and the reason is that **MMDVMCal is not where the difficult part
lives**.

The difficult part is bit error rate, and BER is computed **entirely on the host**.
The modem sends received frames up the wire and nothing else; there is no BER
number anywhere in the firmware (`grep -i ber` over MMDVM_HS finds only I²C
bus-error macros). MMDVMCal's contribution is `BERCal.cpp`. Everything else it does
— `SET_CONFIG`, `SET_FREQ`, `CAL_DATA`, `GET_STATUS` — is the protocol
`internal/modem/protocol.go` already speaks, in a codec that is already pure and
already unit-tested.

So the trade is: reimplement one function, or take on a subprocess. Taking on the
subprocess is worse than it sounds:

- **It is a terminal program, not a tool.** `CConsole::open()` puts stdin in raw
  mode; its `getChar()` polls with a zero timeout. Driving it means keystrokes.
- **Its output is block-buffered when it is not a terminal.** `fprintf(stdout, ...)`
  to a pipe flushes in 4 KB clumps, so the live BER readout — the entire point —
  arrives in bursts long after the frequency it describes. Fixing that means
  allocating a pty, which is real code that exists only to make a screen-scrape work.
- **The contract is human prose.** `"DMR audio seq. 3, FEC BER % (errs): 0.709%
  (1/141)"` is a sentence for a person, not an interface, and it is one upstream
  cosmetic commit away from changing.
- **It has to be in the image.** MMDVMCal is currently on waypoint-stack's
  still-to-pin list. Native means it never needs to be.

Against that, the port is bounded and testable — and it turns out to carry **no
tables at all**, only the procedure (`BERCal.cpp` and `Golay24128.cpp`,
GPLv2-or-later into this GPLv3 tree).

`regenerateDMR` is Golay(24,12) and Golay(23,12) with an AMBE whitening
sequence. Upstream ships all three as literal blobs, about a thousand lines of
generated data, and none of it is a fact about anyone's implementation. The
Golay code is **perfect** — 2048 syndromes and exactly 2048 error patterns of
weight ≤ 3, in one-to-one correspondence — so its decoder is enumerated at init
and is then correct by construction rather than correct if the paste was clean.
The 4096-entry AMBE table is a plain 16-bit linear congruential generator seeded
from the data word (`pr = 16·data`, then `pr = (173·pr + 13849) mod 65536`,
taking the top bit 24 times); every published value is reproduced exactly.

Spot values from both blobs are pinned in tests anyway. A derivation that drifts
produces a plausible bit error rate rather than an obviously broken one, which
is the failure mode worth a test. D-Star BER shares all of this and costs almost
nothing extra.

The decisive argument is the one this repo keeps making: BER decoding as a pure
function over a `[]byte` can be tested against recorded frames on a laptop, and a
pty full of someone else's printf output cannot.

**YSF, P25 and NXDN BER are deliberately not implemented** (§8). They need Viterbi,
Hamming and LICH ports and nobody calibrates an oscillator on them.

### 2. The safety model: what can key a transmitter

Until now Waypoint asked modems exactly one question and never spoke the traffic
protocol, and `internal/modem`'s package comment says so. Calibration changes that,
and it is the first code in this repo that can put a carrier on the air. The
model is three properties, in order of how much they are worth.

**The receive sweep cannot key the radio, structurally.** A BER sweep puts the
modem in `STATE_DMR` with DMR enabled and simplex set — an ordinary receive state.
In that state the firmware's `m_calState` is `STATE_IDLE`, and its `CAL_DATA`
handler (`SerialPort.cpp`, the transmit toggle) only acts when `m_calState` is one
of the transmit calibration states; anything else falls through to a NAK. So during
the flow an operator actually runs, the command that keys the radio is **refused by
the board**. This is not a rule Waypoint enforces on itself, and that is exactly
why it is the property worth having.

Confirmed on hardware: the bench Dual Hat refuses the transmit toggle from the
sweep's state with reason 2 — which is the firmware's *initial* error value,
left untouched because no state branch matched, rather than the "command not
implemented" that code usually means (docs/bench-calibration-run-log.md).

**Anything that does key is bounded inside the engine.** The transmit tests — DMR
deviation tone, the 1031 Hz test pattern, carrier — exist for the repeater
workflow and for confirming a TX offset. Each one:

- requires an explicit operator action per burst; there is no "leave it keyed" mode;
- is bounded by a **dead-man timer owned by the engine**, so a closed browser tab, a
  dropped Wi-Fi link or a panicking goroutine ends the transmission rather than
  extending it. The `defer` that clears TX does not inherit the request context, for
  the same reason RFC-0020's host restart does not;
- transmits **only on the frequency already in the config**. The wizard has no
  frequency entry field. MMDVMCal's `E` command exists because it has no config to
  read; Waypoint has one, and a text box that keys a transmitter on whatever is
  typed into it is not a feature;
- is refused outright when callsign or TX frequency is unset, and when the
  frequency is not in an amateur allocation. That check is new here
  (`internal/cal`), and it is a **union of the ITU regions** deliberately: Waypoint
  does not know the operator's region, and a check that guesses one would refuse
  legal operation somewhere. It catches the failure it is built for — a
  transposed digit putting a carrier outside the amateur service entirely — and
  claims nothing more.

**One hardware operation at a time, and the node always comes back.** Calibration
takes the `hwOps` token (RFC-0019 §7) so it cannot overlap a flash, a detection or
a stack update — the last of which health-gates on MMDVM-Host being up and would
read a calibration as a bad update. MMDVM-Host is stopped through the same
`modem.Holder` arbitration detection uses, and restarted unconditionally on the way
out.

### 3. Why the sweep is DMR, and why it needs the operator's radio

**DMR, because D-Star would lie.** The ADF7021's automatic frequency control is a
compile-time option that is **on by default for D-Star and off for the 4FSK modes**
(`ADF7021.h`: "AFC is already enabled by default in D-Star";
`ADF7021_ENABLE_4FSK_AFC` is commented out). A D-Star BER sweep would show a
flat, healthy curve while the oscillator error it is supposed to find is silently
corrected in hardware — the demodulator hiding the very fault being measured. DMR
sees the error honestly.

**The operator's radio, because there is no internal reference.** The 1031 Hz test
pattern BER mode (`DMR1K`) compares against a known pattern, but only another MMDVM
can generate that pattern, so it measures a link between two nodes rather than one
node's oscillator. A duplex board cannot usefully hear itself. So the stimulus is a
handheld keyed on the node's receive frequency, and the wizard's job is to make
that a guided step rather than a footnote.

### 4. The sweep advances on frames, not on seconds

The naive sweep is a `for` loop with a `time.Sleep` per candidate, and it is wrong
for this hardware. A ±10 ppm board can be 4 kHz out, so the search has to span
±5 kHz; the demodulator's capture window is a few hundred Hz, so the coarse step
has to be smaller than that. That is tens of dwells, and a wall-clock sweep would
demand a single PTT hold longer than anyone can manage, then silently record "0
frames, BER undefined" for every candidate the operator was not transmitting
through.

So the engine measures in **frames, not time**. A candidate offset is complete when
it has counted a minimum number of voice frames (60 ms each); until then it stays
current. When frames stop arriving the sweep **pauses and says so** — "key up
again" — and resumes exactly where it was when they return. The operator can key in
comfortable bursts, and no candidate is ever scored on a sample too small to mean
anything.

Two passes: coarse across the full span, then fine around the winner. The result is
a curve, not a number, and the curve is shown — a clean V is a measurement, a flat
line with one lucky dip is not, and an operator who can see which one they got can
tell whether to believe it.

### 5. One oscillator, two offsets

`RXOffset` and `TXOffset` are separate keys in MMDVM-Host, and on a hotspot they
are separate keys describing **one physical error**: a single TCXO clocks both
paths, so a reference that is 600 Hz low is 600 Hz low transmitting and receiving.
The sweep measures it on receive because that is the direction that can be measured
without test gear, and writes both.

The wizard says that in as many words rather than silently writing a field the
operator did not watch being measured, and the TX side can then be confirmed —
never measured — with the test pattern and the operator's own radio.

### 6. Repeater boards: what is decidable and what is not

The full-size MMDVM path is the same engine and a different workflow, because the
board is analog on both sides: `RXLevel`, `TXLevel`, DC offsets, and the three
invert flags. Its feedback is the `CAL_DATA` reply frame, which reports `max`,
`min`, `diff`, `centre` and — usefully — whether the received signal was
**inverted**.

That last one is the honest dividing line, and the workflow is built on it:

- **RX invert is decidable.** The modem says so directly. Waypoint reads it,
  reports it, and offers to set it.
- **Levels are not.** Setting `TXLevel` correctly means 2.75 kHz deviation measured
  by something that can measure deviation. Waypoint drives the tone, shows the
  live numbers, and guides — it does not pretend a number it cannot measure.

**This path has never been run against a full MMDVM.** No repeater board exists on
the bench. The framing is unit-tested against the firmware's own parser
(`g4klx/MMDVM` `SerialPort.cpp::setConfig`, the 37-byte protocol-2 form), and the
UI and the bench notes both say plainly that it is unverified. It ships that way
rather than being hidden, because an operator with a repeater is better served by a
tool that says "this has not been tested on your hardware" than by no tool.

### 7. Results are a measurement, with provenance

Two things are written, and keeping them apart is the same split RFC-0020 draws
between what the modem said and what the operator configured:

- `calibration_state` — machine-written, outside `Model.sections()` so no PUT can
  reach it: the sweep that ran, when, on which board and firmware (the detection
  fingerprint), the full curve, the offset chosen, and how many frames each point
  was scored on. This is what makes "was this node ever calibrated, against what,
  and how well" answerable months later.
- The **config keys themselves** — `rx_offset`, `tx_offset`, and for repeaters the
  levels, DC offsets and invert flags — written through the ordinary config path
  with `by: "calibration"`, so they appear in the change history like any other
  edit and can be edited afterwards by hand.

Applying is a separate, explicit step from measuring. A sweep that produces a curve
the operator does not believe changes nothing.

Calibration stays excluded from profiles (RFC-0006), and this RFC is the reason
that exclusion earns its keep: an offset measured on one board is a property of
that board's oscillator, and importing it onto another node's differently-tuned
hardware would detune a working radio.

### 8. What is deliberately not in v1

- **YSF, P25, NXDN and FM BER.** Viterbi/Hamming/LICH ports for modes nobody
  calibrates on. The engine's shape takes them later without changing.
- **Generating an RSSI mapping file.** The `RSSIMappingFile` key is now rendered and
  configurable; producing the mapping needs a calibrated signal generator.
- **Automatic TX offset measurement.** It cannot be done without a receiver, and
  §5 explains why it does not need to be.
- **Unattended or scheduled recalibration.** Everything here requires a human with
  a radio, by design.

## The contract (test harness)

- `internal/cal` frame builders produce byte-for-byte what the firmware parsers
  accept: the 27-byte protocol-1 form against MMDVM_HS `SerialPort.cpp::setConfig`,
  the 40-byte protocol-2 form against `g4klx/MMDVM`'s, and `SET_FREQ` against both.
- BER decoding is a pure function over recorded frames, with fixtures: a clean DMR
  voice superframe scores 0, single-bit corruptions score the errors they contain,
  and the Golay tables derived at init match the published encodings.
- The sweep is driven by an injected frame source in tests — no hardware — covering
  the pause/resume path, the minimum-frame rule, and a candidate that never sees a
  frame.
- A transmit test that outlives its dead-man timer is stopped by the engine, proven
  by a test that abandons the caller's context mid-burst.
- The port-ownership rule from RFC-0020 is exercised: a running MMDVM-Host is
  refused without explicit authorisation, and restarted after every exit path.

## Alternatives considered

- **Drive MMDVMCal under a pty** — §1. The subprocess buys one function and costs a
  pty, a screen-scrape, an image dependency and its testability.
- **Ask the firmware for the frequency error.** The ADF7021 can report AFC readback,
  which would make this a single measurement instead of a sweep. The firmware does
  not expose it over the wire, so it would mean a firmware change gating a host
  feature — worth revisiting now that Waypoint can flash firmware (#19), and noted
  as a follow-up rather than a v1 dependency.
- **Sweep in D-Star** because its BER decode is marginally simpler. Rejected in §3:
  AFC would flatten the curve.
- **Write the offset automatically at the end of the sweep.** Rejected in §7:
  measuring and applying are different acts, and the operator sees the curve first.

## Open questions

- Whether the coarse span should narrow when detection reports a TCXO whose
  tolerance is known — a ±0.5 ppm part cannot plausibly be 4 kHz out, and a shorter
  sweep is a better experience. Needs a tolerance column in the board table.
- Whether a second, verifying sweep after applying is worth the operator's time, or
  whether the curve is evidence enough.
- Whether repeater `TXLevel` can be bounded usefully from the modem's own reply
  frames, which would turn part of §6's "not decidable" into "decidable within a
  range". Cannot be answered without a repeater board.
