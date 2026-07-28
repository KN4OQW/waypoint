# Bench run: guided calibration (#20 / RFC-0021)

**2026-07-28.** MMDVM_HS_Dual_Hat on a Raspberry Pi 3 (`KN4OQW`,
172.16.50.13), Raspbian 12 bookworm, kernel 6.12.25+rpt-rpi-v7, armhf —
the same bench the #19 flashing run used.

> **Status: the hardware run has NOT been made.** Everything below the line
> marked *Pending* is a procedure, not a result. The harness is built,
> cross-compiled and deployed to the node at `/tmp/cal.test`, and it stops at
> the one thing that cannot be automated from here: `/dev/ttyAMA0` is
> `root:dialout` and the `rescue` account is in neither, so opening the port
> needs a `sudo` password nobody but the operator has. The keyed part of the
> acceptance criterion needs a person with a radio regardless.
>
> This file is written now, with the commands and the expectations, so the run
> is a matter of pasting three lines and filling in what came back — and so the
> gap does not get quietly forgotten.

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

## Pending — the run to make

MMDVM-Host must be stopped first; the harness will not do it for you, because a
test binary that quietly takes a node off the air is worse than one that says it
could not open the port.

### 1. Receive-only checks (no transmitter, nothing on the air)

```sh
# from the repo, if the binary needs rebuilding:
GOOS=linux GOARCH=arm GOARM=6 go test -tags bench -c -o cal.test ./internal/cal
scp cal.test rescue@172.16.50.13:/tmp/

ssh rescue@172.16.50.13
sudo systemctl stop waypoint-mmdvmhost
cd /tmp && sudo WAYPOINT_BENCH_FREQ=438800000 ./cal.test -test.v
sudo systemctl start waypoint-mmdvmhost
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

### 2. The acceptance criterion (a radio is keyed — this transmits)

> "A misconfigured bench unit (+400 Hz offset injected) is brought to <1% BER by
> wizard alone."

Injecting the error is what makes it a real test: set the node's RX/TX offset to
**+400 Hz** on the settings page first, so the sweep has to find its way back to
0 rather than confirming a node that was already right.

1. Settings → General → Calibration: set RX Offset and TX Offset to `400`, Apply.
2. Hardware tab → Calibration → **START SWEEP**.
3. Set the HT to DMR on the node's RX frequency, colour code 1, and hold PTT
   when the panel asks. Talk, or just hold it — DMR sends voice frames either
   way. Let go whenever; the sweep pauses and resumes.
4. Expect the curve's minimum at **−400 Hz** relative to the injected value —
   that is, back at the node's true zero — at well under 1% BER.
5. Press **USE −400 Hz**, then Apply the configuration.

Record here afterwards: the curve, the best point's BER and frame count, how
many PTT presses it took, and anything the panel said that was wrong or
unhelpful. The last one matters most — the measurement is the easy part.

## Not covered by this run, and known to be

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
