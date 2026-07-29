# Bench run: guided calibration (#20 / RFC-0021)

**2026-07-28.** MMDVM_HS_Dual_Hat on a Raspberry Pi 3 (`KN4OQW`,
172.16.50.13), Raspbian 12 bookworm, kernel 6.12.25+rpt-rpi-v7, armhf —
the same bench the #19 flashing run used.

**The receive-only half of this run is done and passed.** The keyed half — the
acceptance criterion itself — still needs a person with a radio and is written
up as a procedure at the end.

## What is verified without hardware

`internal/cal` is tested against a **scripted modem** that models a real
oscillator error: it decides what to send from how far the session has tuned it
away from the frequency the "radio" is transmitting on, and it builds genuine
DMR voice frames with a genuine number of bit errors flipped into them. The
sweep is not told the answer.

```
TestSweepFindsAnInjectedOffset          400 Hz injected → found, sub-1% BER
TestSweepPausesAndResumes               PTT released mid-candidate, resumed
TestSweepDistinguishesSilenceFromClean  0.00% ≠ "nothing heard"
TestSweepReportsNothingHeard            radio never keyed → named outcome
TestSweepRetunesTheSynthesiser          every SET_FREQ followed by SET_CONFIG
TestTransmitUnkeysWhenTheCallerGoesAway cancelled request ends the carrier
TestEncode23127MatchesPublished         derived Golay == MMDVMCal's tables
TestAmbePRNGMatchesPublished            derived AMBE PRNG == BERCal's table
```

Two defects were found there that a bench run would have found the hard way:

- **`SET_FREQ` alone does nothing on a hotspot.** `CIO::setFreq` stores the
  value and returns; the synthesiser is not reprogrammed until a `SET_CONFIG`
  runs the interface configuration again. A sweep without that second frame
  steps through frequencies the radio never moves to and draws a flat curve —
  which reads as *a board with no oscillator error at all*.
- **Dwell was measured from a candidate's start rather than its last frame**,
  which abandons any frequency that takes longer than the dwell to reach the
  frame minimum. At DMR's real rate of one frame per 60 ms, that is all of them.

## Run 1 — receive-only checks. **PASS**

Nothing transmitted; the node was never keyed.

**Correction, made after the fact:** this run was made with **MMDVM-Host still
running**. The command used to stop it named `waypoint-mmdvmhost`, which is not
the unit — it is `waypoint-mmdvm.service` — and systemd's "Unit not loaded" was
read as "not installed" rather than "you have the name wrong". Two processes were
therefore on the UART for every check below. The results still stand (the modem
answered, and answered correctly), but the silence in `TestBenchListenForFrames`
had a second cause nobody had accounted for, and the port-ownership path was
never exercised.

### The firmware these frames were validated against

```
MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #da73668
UDID 00350016390000074E503543 · protocol 1 · modes known=false (protocol 1 assumes)
```

**Not the build the #19 run recorded.** That log has GitID `899fc2a`; this board
now runs `da73668`, which is not on either fork's `master` — it is a branch
build, committed the same day as this run. That changes nothing about the
evidence (the `SET_CONFIG` parser is the same v1.6.1-family code), but the
identity is recorded here rather than assumed, because "we validated the frames
against the firmware" is only worth writing down if it says *which* firmware.

`modes known=false` is correct and not a fault: protocol-1 firmware reports no
capability bits, so mode support is MMDVM-Host's documented assumption rather
than something the modem said.

### What each check proved

```
TestBenchIdentity                        protocol 1, DMR carried
TestBenchSetConfigAccepted               the 27-byte SET_CONFIG was ACKed
TestBenchTuneAcceptsAnOffsetRange        -5000 … +5000 Hz all accepted at 438.8 MHz
TestBenchListenForFrames                 5 candidates walked, all unheard, ErrNothingHeard in 10.8 s
TestBenchCalDataIsRefusedFromAReceiveState  the modem refused to key
```

**The frame layout is right on real firmware.** This was the open risk: the
calibration frames were transcribed from two firmware parsers, and a wrong byte
in the middle of a 27-byte frame does not fail loudly — it is either NAKed with
a bare code or, worse, accepted as something other than what was meant. The real
Dual Hat ACKed the real sweep configuration.

**±5 kHz of tuning is accepted at 438.8 MHz**, so the default coarse span does
not run into the board's own frequency limits on 70 cm. (It would on a
frequency near a band edge or a banned satellite segment; the board enforces
both itself, with the same bare reason code.)

**The sweep terminates correctly with nobody transmitting.** Five candidates,
each given its dwell, each marked `heard=false scored=false`, and the run ended
in `ErrNothingHeard` rather than hanging or reporting a clean 0.00% — which is
the failure this design worried about most, since silence and perfection are the
same number.

### The safety property, checked against the board — and a defect it found

`TestBenchCalDataIsRefusedFromAReceiveState` asks the firmware to key from the
sweep's own state and requires a refusal. It got one:

```
refused as expected: cal: the modem refused command 0x08:
                     the command is not known to this firmware
```

RFC-0021 §2's claim — that the sweep cannot transmit as a property of the board
rather than a promise of ours — holds on this firmware.

The reason code is the interesting part, and it exposed a **defect in Waypoint's
own error text**. Reason 2 is conventionally "command not implemented", and that
is what Waypoint translated it to. It is wrong here: MMDVM_HS initialises its
per-frame error to `2U` (`SerialPort.cpp`) and its `CAL_DATA` handler only
clears it inside a branch matching the modem's *current calibration state*, so a
command the firmware implements perfectly well comes back as reason 2 with
nothing missing. The sentence above is what the run printed, and an operator
told "this firmware does not implement it" would go looking for a firmware
update they do not need. The message now offers both readings, and a test pins
it.

### Rebuilding and rerunning

```sh
GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c -o cal.test ./internal/cal
scp cal.test rescue@172.16.50.13:/tmp/
ssh rescue@172.16.50.13 'cd /tmp && sudo WAYPOINT_BENCH_FREQ=438800000 ./cal.test -test.v'
```

MMDVM-Host must be stopped first if it is running; the harness will not do it
for you, because a test binary that quietly takes a node off the air is worse
than one that says it could not open the port.

<details>
<summary>The original procedure, for reference</summary>

#### Receive-only checks (no transmitter, nothing on the air)

```sh
# from the repo, if the binary needs rebuilding:
GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c -o cal.test ./internal/cal
scp cal.test rescue@172.16.50.13:/tmp/

ssh rescue@172.16.50.13
sudo systemctl stop waypoint-mmdvm
cd /tmp && sudo WAYPOINT_BENCH_FREQ=438800000 ./cal.test -test.v
sudo systemctl start waypoint-mmdvm
```

What each test is for, and what would make it interesting:

| Test | Proves | A failure would mean |
|---|---|---|
| `TestBenchIdentity` | the session opens and the board answers | the port, the speed, or the settle timing is wrong |
| `TestBenchSetConfigAccepted` | the 27-byte protocol-1 `SET_CONFIG` is **ACKed by real firmware** | a byte in the frame layout is transcribed wrong |
| `TestBenchTuneAcceptsAnOffsetRange` | ±5 kHz of tuning is accepted at 438.8 MHz | the sweep's span crosses a limit the board enforces |
| `TestBenchListenForFrames` | the sweep loop runs and terminates | it hangs, or claims a measurement from silence |
| `TestBenchCalDataIsRefusedFromAReceiveState` | the firmware **refuses to key** from the sweep's state | RFC-0021 §2's safety argument is wrong on this firmware |

That last one is the one to read carefully. The claim that the sweep is
structurally unable to transmit is a claim about the *board*, not about
Waypoint, and this is where it is checked against the board.

</details>

## Run 2 — first live measurements, 2026-07-29. **The engine works; four defects found**

A DMR handheld on a simplex channel, 433.900 MHz, colour code 2, into the Dual
Hat's receiver. Every number below is a real bit error rate measured off the air.

### The measurement is real

The decisive run, five offsets at eight seconds each:

```
-200 Hz  110 frames   1.238%   (192/15510 bits)
-100 Hz  131 frames   1.305%   (241/18471 bits)
  +0 Hz  131 frames   0.807%   (149/18471 bits)   ← minimum
+100 Hz  131 frames   1.299%   (240/18471 bits)
+200 Hz  131 frames   1.575%   (291/18471 bits)
```

A smooth curve with its minimum at the configured frequency, at **0.807% BER** —
so this node needs no offset, and the sweep would correctly tell its operator so.
An earlier fixed-frequency run scored 245 frames at 4.52% with per-frame errors
ranging 0 to 21; **a frame with zero errors is the proof the scorer is right**,
because misaligned bits cannot produce one.

### Four defects, none of which any amount of emulation would have found

**1. Two processes on one UART, silently.** Every early run heard nothing, and
the modem answered with a GET_VERSION reply every 1.8 seconds — MMDVM-Host,
running under `waypoint-mmdvm.service`, polling a modem it could not keep. The
stop command used `waypoint-mmdvmhost`, which is the DEBIAN PACKAGE name, not the
unit; systemd's "Unit not loaded" was read as "not installed". Root bypasses the
advisory exclusive lock, so nothing failed loudly — the two processes simply
interleaved, and the handheld could not key either. The bench harness now refuses
to run while MMDVM-Host is active and says which name to stop.

**2. A duplex channel cannot calibrate.** The handheld sat "trying to connect"
and dropped after five bursts. A radio on a repeater channel will not transmit
until it hears a downlink, and the sweep — by design — never transmits. Both ends
were behaving exactly as specified. The panel now says "simplex, Direct Mode or
DMO" before the button, and names the trap.

**3. The sweep gave up before the operator could key.** With nothing ever heard,
every candidate dwelled out and a seven-point run ended in 23 seconds — less time
than it takes to pick up a radio. There is now a bounded first-signal wait
(60 s default) before any candidate is consumed, which still gives up and sweeps
anyway, because the configured frequency is exactly where a badly-tuned board
hears nothing.

**4. Scoring during re-acquisition, and samples far too small.** The first
complete sweep alternated real values (1.9%, 2.3%) with **exactly 13.0319%** at
scattered offsets — the value random bits give through a perfect Golay code,
whose covering radius is 3. Retuning restarts the receiver mid-transmission and
it re-acquires sync, sometimes onto a false position. A settle delay plus
discarding the first frames fixed most of it; the rest was sample size. Ten
frames was the original default and the same frequency read 0.44% on eight frames
and 0.807% on a hundred and thirty-one. **The default is now fifty.**

One thing tried and reverted: gating the score on the receiver's own sync burst
(control 0x20) instead of discarding frames. It was worse — every point then read
a uniform 11.5% — so scoring from a just-re-correlated burst evidently catches it
before it is trustworthy. Recorded because the idea is an obvious one to have
again.

### Still not done

The injected-error half of the acceptance criterion. This node is already
correctly tuned, so the sweep had nothing to find; proving it *recovers* from a
known error still needs the +400 Hz injection below, now with the fifty-frame
default in place.

## Run 3 — the acceptance criterion. **NOT YET RUN** (a radio is keyed — this transmits)

> "A misconfigured bench unit (+400 Hz offset injected) is brought to <1% BER by
> wizard alone."

Injecting the error is what makes it a real test: set the node's RX/TX offset to
**+400 Hz** on the settings page first, so the sweep has to find its way back to
0 rather than confirming a node that was already right.

1. Settings → General → Calibration: set RX Offset and TX Offset to `400`, Apply.
2. Hardware tab → Calibration → **START SWEEP**.
3. Set the HT to **DMR simplex** (Direct Mode / DMO) on the node's RX frequency —
   transmit and receive both there — at the node's colour code, and hold PTT when
   the panel asks. Talk, or just hold it; DMR sends voice frames either way. Let
   go whenever, the sweep pauses and resumes.

   **A duplex/repeater channel does not work**, and fails in a way that looks
   like a broken sweep: the radio waits for a downlink that a receive-only sweep
   never sends, reports "connecting" and gives up, and the sweep records every
   candidate as unheard. This was found here on 2026-07-29 — 17 candidates, all
   silent, with the HT refusing to key.
4. Expect the curve's minimum at **−400 Hz** relative to the injected value —
   that is, back at the node's true zero — at well under 1% BER.
5. Press **USE −400 Hz**, then Apply the configuration.

Record here afterwards: the curve, the best point's BER and frame count, how
many PTT presses it took, and anything the panel said that was wrong or
unhelpful. The last one matters most — the measurement is the easy part.

## Not covered, and known not to be

- **The keyed acceptance run**, above.
- **Port arbitration.** `waypoint-mmdvm.service` is not installed on this
  bench, so "stop MMDVM-Host, sweep, restart it no matter what" has unit-test
  coverage and no hardware coverage.
- **The repeater workflow.** No full-size MMDVM exists on this bench. The
  level/invert path (`/api/cal/listen`, the transmit tests) is written from the
  firmware's parser and is **unverified on hardware**; the API reports
  `repeater_workflow_verified: false` and the panel says so. See RFC-0021 §6.
- **Protocol-2 `SET_CONFIG`.** The Dual Hat reports protocol 1, so the 40-byte
  form is exercised only by unit tests against `g4klx/MMDVM`'s parser.
- **YSF, P25 and NXDN bit error rates.** Not implemented (RFC-0021 §8).
- **A board whose oscillator is genuinely wrong.** The bench Dual Hat is
  correctly tuned, so the acceptance run injects an error into the configuration
  rather than measuring a bad crystal. Those are the same measurement from the
  sweep's point of view, but they are not the same experience — a board that is
  4 kHz out is also harder to hear at every candidate, and that has not been
  seen here.
