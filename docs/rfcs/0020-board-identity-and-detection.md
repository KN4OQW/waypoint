# RFC-0020: Board Identity and Detection

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #18 (MMDVM_HS board family support with auto-detection — GPIO UART and USB CDC, board/TCXO auto-detect where possible, explicit picker where not)
- Depends on: [RFC-0001](0001-config-store.md) (store conventions, and the hardware-fingerprint block this fills in), [RFC-0002](0002-security-posture.md) (every route here is behind the claim gate), [RFC-0008](0008-status-pipeline.md) (the no-log-scraping property this deliberately does not break)
- Related: [RFC-0019](0019-firmware-flashing.md) (#19 — reuses the board table, the serial layer and the port-ownership rule defined here), [RFC-0006](0006-connection-profiles.md) (the "captured on a differently-tuned board" warning this makes possible), #20 (guided calibration), #136 (the detected-but-never-configured bug this is written to avoid repeating)

## Summary

Waypoint learns what modem is attached by **asking it**, and keeps what the
modem said strictly separate from what the operator has configured — with one
named, explicit operation that crosses between them.

Four pieces:

1. **`internal/modem`** — the MMDVM identity handshake, a parser for the identity
   string the modem answers with, and a table of the boards that string can mean.
2. **A confined, arbitrated probe** — a bounded sweep of ports that could
   plausibly be a modem, which refuses to take the port from a running
   MMDVM-Host unless explicitly authorised, and always gives it back.
3. **A detected/configured split** — `hardware_state` is machine-written and
   never edited; `modem.board` / `modem.tcxo_hz` are operator-owned and never
   overwritten; `AdoptDetection` is the crossing.
4. **`internal/bootcfg`** — a diagnosis of why a perfectly good GPIO hat is
   inaudible on stock Raspberry Pi OS, and a privileged repair for it.

Board identity reaches **no rendered INI**. It earns its place entirely in what
it lets Waypoint refuse.

## Motivation

Today the modem surface is a free-text box. `docs/pistar-parity.md` scores the
board dropdown as *partial: raw UART port only*, and the field's own help text
tells the operator "if the node stops seeing the modem after a reboot, the port
has usually been renumbered" — the UI documents the bug instead of fixing it.

Two failure modes follow, and both produce a node that starts, reports itself
healthy, and does not work on the air:

- **The port is wrong.** MMDVM-Host exits when its port has no modem. Worse,
  `internal/stackupdate` gates an update's health on the host staying up, so a
  wrong port after an update reads as a bad update and triggers a rollback.
- **The configuration is right for a different board.** Duplex on a single
  ADF7021 never keys. A mode the firmware does not carry is silently absent, and
  the operator debugs their reflector.

Neither has a symptom that names its cause. Both are answerable from one line
the modem volunteers before anything has been configured.

## The identity string is the whole of auto-detection

A CA6JAU-lineage MMDVM_HS firmware answers `GET_VERSION` with:

```
MMDVM_HS_Dual_Hat-v1.6.1 20230526 14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a
```

That is board family, firmware version, build date, **reference oscillator**,
**radio count** (hence duplex capability) and provenance, in one string, before
any configuration exists. Bench evidence: `docs/on-hardware-report.md`.

The wire protocol is transcribed from MMDVM-Host's `Modem.cpp`. Waypoint
implements the identity half **only** — there is no code path in this repo that
can key a transmitter, and the one frame it ever writes is three bytes.

The grammar has drifted: the MHz field only appears from the 1.5.x era, and
full-size MMDVM uses a different shape entirely. So the parser recognises fields
wherever they appear rather than by position, and **never errors** — an
unparseable string still describes a modem that is really attached, and the
operator is better served by a partial answer plus a picker than by a refusal.

### Where auto-detection stops, and why

The identity string names the **firmware**, not the product. A JumboSpot is a
JumboSpot on the invoice and an `MMDVM_HS_Hat` on the wire, because that is what
its maker flashed. Three products answer to the Dual Hat's string.

So the split issue #18 asks for falls out of the hardware itself:

| Comes off the wire (detected) | Comes from the operator (picked) |
| --- | --- |
| Board family, reference oscillator, radio count, firmware, capability bits | Which of the candidate products it is |

Transport is the one extra discriminator Waypoint supplies: it knows whether it
found the modem on the GPIO UART or on a USB device, and that separates most of
the otherwise-identical sibling pairs (NanoDV NPi vs NanoDV USB, ZUMspot RPi vs
ZUMspot USB).

**Only the bench Dual Hat is marked `Verified`.** Every other row is seeded from
the firmware's `HW_TYPE` defines and vendor documentation, and its oscillator is
a hint consulted only when the firmware reports none — never an assertion.

## Port ownership is the load-bearing rule

MMDVM-Host holds the UART exclusively for as long as it runs. The rule is
enforced in `internal/modem`, not left to each caller to remember:

- **Host not running** → probe freely. This is first boot, post-flash, and any
  node whose modem has never been configured — the moments detection is worth
  most.
- **Host running** → refuse (`409`, with `stoppable: true`), unless the caller
  explicitly authorises stopping it.
- **Having stopped it** → restart it *whatever happens next*: probe failure,
  panic, a cancelled request, a closed browser tab. The restart runs on a
  context deliberately detached from the caller's. A node that ends a detection
  off the air is a worse bug than no detection at all.

Detection is also serialised against itself. Two interleaved stop/start cycles
could order a restart before the other's stop, leaving MMDVM-Host down with
nothing left to bring it back.

### Alternative considered and rejected: read it off MQTT

MMDVM-Host publishes its startup log, version line and all, to MQTT. Reading it
there would need no arbitration whatsoever.

It is not done. RFC-0008's no-log-scraping property is structurally guarded
(`internal/status/noscrape_test.go` fails the build if the status fold imports a
file reader), and hardware identity is not where the first crack in that goes. It
would also be strictly worse: it can only report a modem MMDVM-Host has already
successfully opened, which excludes every node that needs detection.

## The sweep is confined, and the argument is hd44780's

`internal/lcd/hd44780` argues that probing must not poke chips it knows nothing
about. The same applies here with more force: a hotspot's USB may also carry a
GPS, a Nextion display, or a radio's CAT interface.

Three constraints:

- Only tty devices that could plausibly be a modem are enumerated at all —
  `ttyAMA*`, `ttyS0` (only `ttyS0`; on x86 all 32 exist and all open), `ttyACM*`,
  `ttyUSB*`. Symlinked duplicates (`/dev/serial0`) are collapsed.
- The only frame written is `GET_VERSION`: three bytes, no side effect on
  anything that understands them and no meaning to anything that does not.
- **Ports the operator has already claimed are excluded by the caller**, not
  discovered and hoped about. The display port above all — a Nextion is a serial
  device sitting on exactly the kind of port this sweep would otherwise walk into.

Ordering is evidence-ranked: a recognised USB vendor/product pair is positive
evidence that something is a modem; a `/dev/ttyAMA0` that exists is not, because
it exists on every Pi whether or not a hat is fitted.

Every port tried is reported with what it did, not just the winner. "Nothing
found" is the outcome an operator most needs explained, and a board sitting in
its DFU bootloader is called out by name — it cannot answer, but it is one
firmware flash ([RFC-0019](0019-firmware-flashing.md)) from working.

### Serial layer

Raw termios on a raw file descriptor, not an `os.File`. Read timeouts come from
`VTIME` — "answer within half a second or you are not a modem" — and handing the
descriptor to Go's poller would quietly take that away. `hd44780` talks to its
bus the same way for the same reason. No new dependency: `golang.org/x/sys` was
already in `go.mod`.

`HUPCL` is cleared, and that clearing is load-bearing: with it set, closing the
port drops DTR, and dropping DTR resets an STM32 CDC board. Detection that
reboots the modem every time it looks at one is not detection.

## Detected is not configured

Issue #136 — *a detected HD44780 panel is never reflected in the LCD config* — is
what happens when a project forgets this. The failure there is not that detection
and configuration were separated; they should be. It is that nothing ever crossed
the gap.

So the split is kept and the bridge is built and named:

| | Written by | Edited by | Where |
| --- | --- | --- | --- |
| What the modem said | detection | nobody | `hardware_state` (outside `Model.sections()`, like `update_state`) |
| What the node believes | adopt, or the operator | the operator | `modem.board`, `modem.tcxo_hz`, `modem.port`, `modem.uart_speed` |

`AdoptDetection` returns what it changed, field by field, rather than changing it
quietly — a silent adopt is how an operator stops knowing which of two boards
their node is configured for.

It refuses two things:

- **A board the modem's identity rules out.** The picker resolves an ambiguity;
  it does not overrule the hardware.
- **An oscillator that was inferred rather than reported.** A reference frequency
  guessed wrong detunes the radio, and a value in the config reads as a fact.

## What board identity actually buys

Nothing in a rendered INI. MMDVM-Host is told a port and a speed and nothing else
about the hardware. The return is entirely in refusals and warnings:

| Check | Severity | What it prevents |
| --- | --- | --- |
| Duplex on a single ADF7021 | error | A host that starts and never keys |
| A mode the firmware does not carry | error | A silently absent mode, debugged as a reflector problem |
| A port the modem is not on | error | MMDVM-Host exiting; a stack update rolling itself back |
| Oscillator mismatch | warning | A detuned transmitter |
| A configured board the modem cannot be | warning | Every check above being made against the wrong hardware |

Capability checks fire **only on protocol-2 firmware**, where the bits are the
modem's answer. On protocol 1 they are MMDVM-Host's assumption, mirrored so the
two agree but flagged as an assumption throughout — including in the UI, which
says so in words. Refusing a mode on the strength of a guess is exactly what that
flag exists to prevent.

Nothing here blocks Apply. The modem may be the thing that is wrong.

The profile fingerprint fields RFC-0001 and RFC-0006 reserved for this are now
filled, so a profile carries the board and oscillator it was captured on.

## Why a hat can be perfectly good and completely mute

Waypoint's image already frees the PL011 for the modem. Phase-1 distribution is a
`.deb` on stock Raspberry Pi OS Lite, and there the defaults are exactly wrong:
on a Pi 3 and later Bluetooth owns the UART and a login console sits on it.

That failure has no symptom an operator can act on. Detection finds nothing,
MMDVM-Host would exit, and nothing anywhere says the word "Bluetooth". **Naming
it is most of the value** — the repair is three lines in two files, but knowing
which three lines is what costs an evening.

`internal/bootcfg` reads a commented-out `enable_uart` as off (the state a stock
`config.txt` ships in), accepts `miniuart-bt` as well as `disable-bt` (telling an
operator who wanted to keep Bluetooth that their correct fix is wrong would be
worse than not asking), edits `cmdline.txt` on one line (the firmware truncates
the kernel command line at a newline), and leaves the operator's own lines alone.

The privileged half is a new `privhelper` call taking **no arguments** — the
interface's whole argument is that it names operations rather than offering
"write this file". It has **no reverse**: Waypoint knows exactly which lines free
the UART, but it cannot restore a serial console it never saw at a baud rate it
was never told, and a half-working reverse on a headless node is worse than none.
The getty is **masked, not disabled** — a disabled `serial-getty` is still
startable, and it is exactly the unit a generator recreates.

## API

All four routes sit behind the RFC-0002 claim gate and default to denied; they
are in the route matrix that asserts it.

| Route | Does |
| --- | --- |
| `GET /api/hardware` | Last detection, board table, configured hardware, GPIO UART diagnosis, and every disagreement |
| `POST /api/hardware/detect` | Probe now. `{"stop_host": true}` authorises taking the port from a running host; without it a running host is a `409` |
| `POST /api/hardware/adopt` | Write the last detection into the config; `{"board_id": …}` answers an ambiguity, `409` with candidates when one is needed |
| `POST /api/hardware/uart` | Free the GPIO serial port |

Detect and adopt are separate because probing reads the world and adopting
changes the node. Fusing them would mean a button labelled "have a look" silently
reconfigures a working node, and would leave the ambiguous case unanswerable —
there would be nowhere to put the answer.

## Out of scope

- **Firmware flashing** — [RFC-0019](0019-firmware-flashing.md) / #19. This RFC
  reports a board in DFU; it does not write to one.
- **Calibration** — #20. The offsets stay where they are.
- **Full-size MMDVM (#25) and DVMega (#26)** — separate tiers. A board this table
  cannot name is offered as unrecognised, not guessed at.
- **Frequency-range validation from the oscillator.** A 12.288 MHz board and a
  14.7456 MHz board do not cover the same ranges, and bounding the RX/TX inputs
  accordingly is obviously desirable. It is deliberately not done here: the
  author does not have limits verified well enough to enforce, and asserting
  wrong ones would refuse configurations that work. Tracked as a follow-up.

## Open questions

1. **Board table accuracy.** Only the Dual Hat is bench-verified. The oscillator
   and transport for the ZUMspot line, LoneStar, SkyBridge, EuroNode and D2RG
   come from vendor documentation. Owners of those boards: a `Detect` result
   pasted into the issue is the single most useful contribution to this work.
2. **Should adopt be offered during first-boot setup?** Everything is in place
   for the wizard to detect and adopt with no operator input in the unambiguous
   case. It is left out of this RFC because setup is the one flow where an
   unexpected question is most expensive, and the ambiguous case is common.
3. **Stable port naming.** A `/dev/serial/by-id` symlink would survive USB
   renumbering, which is the failure the current help text describes. Detection
   makes it recoverable rather than fatal, so this is a smaller win than it was —
   but is it worth adopting the by-id path instead of the `ttyACM*` one?
