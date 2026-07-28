# RFC-0019: Built-in Firmware Flashing

- Status: **accepted** (2026-07-27)
- Author: KN4OQW
- Implements requirements: #19 (firmware flashing as a core API operation with progress streaming — GPIO ROM bootloader and USB bootloader paths, firmware from pinned releases built in CI)
- Depends on: #18 / `internal/modem` — the board table, the raw-termios serial layer, and the port-ownership rule this reuses verbatim; [RFC-0013](0013-signed-releases-verified-downloads.md) (the verifier every firmware artifact passes through); [RFC-0004](0004-event-history-store.md) (why per-chunk progress does **not** go through the event hub)

## Summary

Flashing a modem becomes an **operation Waypoint performs**, not a script an
operator is told to run over SSH. One button, one code path, two hardware
transports: the STM32 **ROM bootloader** over the GPIO UART with BOOT0/nRST driven
from the Pi's own lines, and the **Maple DFU bootloader** over USB for the stick-form
boards. The firmware itself comes from a **pinned, CI-built, minisign-signed
catalog** Waypoint publishes — not from a third party's directory layout — and the
board and oscillator are **matched from what detection already read off the wire**
(#18) rather than encoded in the name of the script the operator picked.

Everything is implemented in Go against the interfaces this repo already has:
AN3155 over the existing termios layer, GPIO lines over `/dev/gpiochip*`, DFU over
usbfs. No `stm32flash`, no `dfu-util`, no new packages in the image, and no
argv-and-stdout contract with someone else's CLI.

The safety argument is the load-bearing part and it is stated first, because
"flashing" is the operation operators are most afraid of: on the GPIO path an
interrupted flash is **recoverable by retry as a property of the silicon**, and on
the USB path Waypoint **refuses the one write that could brick a board** rather
than offering it behind a warning.

## Motivation

Pi-Star's flashing surface is a set of scripts whose names encode the answer the
operator is supposed to already know:

    pistar-mmdvmhshatflash hs_hat | hs_dual_hat | hs_hat-12mhz | hs_dual_hat-12mhz
    pistar-zumspotflash    rpi | rpi_duplex | usb | libre

That is sbin #14/#18: a per-board, per-TCXO script matrix bolted on over years,
which asks an operator to name their board's **reference oscillator** — a fact
almost nobody buying a hotspot has ever been told, and which picking wrong does not
fail loudly. It detunes the radio and the node transmits anyway.

And sbin #55 is the other half: those scripts fetch firmware from a third party's
release directory and break when that directory is restructured. The upstream
project owes Waypoint nothing and reorganising its own repo is its right; a host
system that treats another project's file layout as an API has taken a dependency
it cannot maintain, and the operator experiences the breakage as *Waypoint is
broken*.

Waypoint is positioned to fix both, because it already knows the answers. Detection
(#18) reads board family, firmware version, reference oscillator and radio count
out of the modem's own identity string before anything is configured. The operator
should never be asked to type what the modem already said.

## Design

### 1. The safety model: what can be bricked, and what cannot

The two transports have different failure floors, and the design follows from that
rather than from convenience.

**GPIO / ROM bootloader — cannot be bricked.** The STM32F103's bootloader is
mask-programmed into system memory at the factory. Holding BOOT0 high across a
reset enters it *regardless of what is in flash*, including nothing at all. An
interrupted write therefore leaves a board that is corrupt as a modem and reachable
exactly as it was a moment earlier — so retry works, always. This is issue #19's
second acceptance criterion, and it costs nothing to guarantee: it is a property of
the part, not of this code. What the code must do is not squander it, which means
driving BOOT0 and nRST itself rather than asking a human to hold a jumper.

**USB / Maple DFU — brickable, in exactly one way.** These boards do not expose
BOOT0; their reachability depends on a DFU bootloader living in the first 8 KB of
flash. An interrupted **application** write is still recoverable, because the
bootloader is untouched and still enumerates. An interrupted **bootloader** write
is recoverable only over SWD with hardware the operator does not own.

The rule that falls out, and the one place this RFC is deliberately less capable
than the incumbent:

> **Waypoint never writes below the application load address on a DFU board.**

Upstream's fix for the RPi 3B+ USB problem is precisely that write — replacing the
bootloader with the long-reset-pulse variant (`make stlink-bl`). Waypoint detects
the condition and **says so, with the SWD procedure**, instead of performing a
write whose interruption produces a coaster. A refusal that names the remedy is a
better outcome than a progress bar with a 1-in-N chance of ending the board.

### 2. Firmware provenance: a catalog Waypoint builds

A new repository, `KN4OQW/MMDVM_HS`, shaped like `waypoint-stack`:

- A pinned upstream SHA in `pins.env`, bumped only by a PR whose description is a
  changelog reading of the upstream diff. Nothing floats.
- CI builds **every board × oscillator variant** with `arm-none-eabi-gcc`,
  minisign-signs each `.bin` with the RFC-0013 release key, and publishes them to a
  tagged release alongside a signed `firmware.json`:

```json
{
  "version": "v1.6.1-wp1",
  "variants": [
    {
      "id": "mmdvm_hs_dual_hat-14m7456",
      "board_ids": ["mmdvm_hs_dual_hat", "zumspot_duplex", "lonestar_dual"],
      "tcxo_hz": 14745600, "duplex": true, "transport": "gpio",
      "load_address": "0x08000000",
      "url": "…/mmdvm_hs_dual_hat_fw.bin", "sha256": "…", "sig_url": "…/.minisig"
    }
  ]
}
```

The catalog and every artifact are fetched through
[`internal/verifydl`](../../internal/verifydl/verifydl.go) with **signature
verification mandatory** — not opt-in as it is for reference data. The distinction
is not fussiness: a poisoned host list misroutes traffic, while a poisoned firmware
image is arbitrary code on a transmitter that a licence holder is legally
responsible for. Verified artifacts are cached on disk, so a re-flash and a flash
on a node that has since gone offline both work without a second fetch.

`board_ids` is the join back to `internal/modem`'s board table. When a board is
added there, the catalog entry names it — the mapping lives in data, in one place,
and no filename is ever parsed to recover a fact.

### 3. Choosing the variant: refuse rather than guess

Detection returns a `modem.Resolution` that already distinguishes three outcomes —
one candidate, several, or none — and flashing consumes that distinction directly:

- **Unambiguous board, oscillator reported by the firmware.** Match, offer the
  flash, say nothing.
- **Several candidates but one matching variant** (the common case: sibling boards
  that share an oscillator and a radio count take the same image). Flash it, and
  name what was flashed rather than what was guessed.
- **Ambiguous, or `TCXOAssumed`.** Refuse, and show the picker with the candidates.
  A wrong-oscillator image does not fail; it produces a node transmitting off
  frequency, which is worse than an unflashed node and much harder to diagnose.

Duplex is a hard filter, not a preference: a single-ADF7021 board flashed with a
dual image is a modem that reports capabilities it does not have.

### 4. The GPIO path: AN3155 in Go

`internal/flash/stm32.go` implements the ST USART bootloader protocol (AN3155)
directly, as pure framing over an `io.ReadWriter`, testable against a scripted fake
exactly the way [`modem/protocol.go`](../../internal/modem/protocol.go) is:

| Step | Wire |
| --- | --- |
| Sync | `0x7F`, autobaud, expect ACK `0x79` (NACK `0x1F`) |
| Capabilities | `GET` (`0x00`) — returns bootloader version **and the supported command list** |
| Identity | `GET_ID` (`0x02`) — the device ID, checked against the variant's expected part |
| Erase | `ERASE` (`0x43`) *or* `EXTENDED_ERASE` (`0x44`), **whichever `GET` advertised** |
| Write | `WRITE_MEMORY` (`0x31`), 256-byte word-aligned chunks, address + XOR, length + data + XOR |
| Verify | `READ_MEMORY` (`0x11`) readback compared against the image |

Three choices worth defending:

- **The erase command is selected from `GET`, never hardcoded.** F103
  medium-density bootloaders offer `0x43`; later parts offer only `0x44`. Hardcoding
  either is how this breaks silently on the first board of the fast-follow tier
  (#25), and asking the bootloader costs one round trip.
- **Verification is a readback, not an ACK.** The bootloader ACKs that it accepted
  a write, not that flash holds what was sent. Reading it back is what makes
  "flashed successfully" a claim rather than a hope, and it is also the cheap
  detector for a board whose flash is read-protected.
- **The exit is a reset, not `GO`.** Driving BOOT0 low and pulsing nRST brings the
  board up exactly as it comes up from cold power. `GO` (`0x21`) jumps to the
  application with the bootloader's peripheral state still configured, which is a
  different machine from the one the operator will have after their next reboot.

The bootloader speaks **8E1**; the modem protocol speaks 8N1. Both live in the same
package, so `openSerial` grows a parity parameter rather than acquiring a second
implementation. Sync is attempted at 115200 and falls back to 57600 (the speed
upstream's tooling effectively uses), because a failed autobaud and a dead board are
indistinguishable from one attempt.

### 5. GPIO line control: by label, not by number

`internal/flash/gpio.go` requests BOOT0 and nRST through `/dev/gpiochip*` with the
v2 line ioctls, selecting the chip **by its label** (`pinctrl-bcm2835`,
`pinctrl-bcm2711`, `pinctrl-rp1`). Line 20 and line 21 then mean the same physical
header pins on every Pi, and there is no base arithmetic anywhere in the path.

The sysfs fallback exists for anything the character device cannot open, and *that*
is where the issue's base-512 requirement is honoured: the export number is
`base + offset`, with `base` read from `/sys/class/gpio/gpiochipN/base` on the chip
whose label matched. Linux 6.6 moved dynamic GPIO base allocation up to 512 and
broke every script that had `echo 20 > /sys/class/gpio/export` written into it —
which is the whole reason the character device is the primary path and the fallback
computes rather than assumes.

The lines themselves belong to the **board table**, defaulting to BCM20 (BOOT0) and
BCM21 (nRST) — the MMDVM_HS hat family's wiring — with a per-board override, because
a pin number is a hardware fact about a board and the flash engine should not be
where hardware facts live. Lines are released before MMDVM-Host is restarted; a
held line is a modem the host cannot reset.

### 6. The USB path: Maple DFU over usbfs

Three steps, none of them shelled out:

1. **Enter the bootloader.** A running board is at `1eaf:0004` (its CDC serial
   interface). Upstream's `upload-reset` puts it into DFU by driving a line-state
   pattern and writing a four-byte `1EAF` magic on that port; Waypoint performs the
   same sequence and then waits for the device to re-enumerate as `1eaf:0003` — an
   ID [`modem/ports.go`](../../internal/modem/ports.go) already recognises and
   already reports as "in its bootloader, one flash from working".
2. **Write.** DFU class requests over `/dev/bus/usb/BBB/DDD` usbfs ioctls
   (`USBDEVFS_CLAIMINTERFACE`, `USBDEVFS_CONTROL`) — no libusb, no `dfu-util`. The
   application alt-setting only; the load address comes from the catalog entry and
   the engine refuses any address below it, per §1.
3. **Leave.** DFU detach and re-enumeration back to `1eaf:0004`, then re-probe.

**The 3B+ quirk** is handled honestly. The host side gets a longer reset hold and a
longer enumeration window, because a 3B+'s USB hub is slower to bring the device
back. But the actual defect is a *short-reset bootloader on the board*, and the only
real fix is a bootloader write this design forbids (§1). So a board that fails to
re-enumerate on a 3B+ is reported as exactly that — "this board carries the
short-reset bootloader; on a 3B+ it needs the long-reset bootloader, which requires
SWD" — rather than retried until the operator gives up.

### 7. Port ownership, and one exclusion that is not obvious

Arbitration is `modem.Holder`, reused unchanged: MMDVM-Host is stopped only with
explicit authorisation, and restarted afterwards no matter how the job ends —
failure, panic, or a browser tab closed mid-flash — on a context that does not
inherit the request's cancellation. A node that ends a flash off the air because
someone navigated away is a worse bug than no flashing at all.

The non-obvious one: **a flash and a stack update must exclude each other.**
`internal/stackupdate` health-gates an update on MMDVM-Host being up. A flash stops
MMDVM-Host for a minute. Run them concurrently and a perfectly good stack update
observes a dead host, concludes it broke the node, and rolls back — a spurious
revert with no visible cause. `detect.go` already makes this argument for probing;
flashing holds the port far longer, so the two operations take the same lock.

Also checked before the port is opened: a `getty` on the GPIO UART. It is a normal
state on a stock Pi and it produces "busy" rather than a useful message unless
someone looks for it.

### 8. Progress: two streams, deliberately

Per-chunk progress **must not go through the event hub.** Everything published
there is persisted to SQLite by [`events/writer.go`](../../internal/events/writer.go);
a 128 KB image in 256-byte chunks is ~500 progress ticks, and writing 500 rows to
the SD card to animate a progress bar is the kind of thing this project is
supposed to be better than.

So:

- **Byte-level progress** — `GET /api/flash/events` (SSE), fed from an in-memory
  per-job broadcaster. Ephemeral by construction; a client that reconnects gets the
  current state, not a replay.
- **Milestones** — `flash_started`, `flash_ok`, `flash_failed` on the hub, so they
  land in history, on the dashboard and on the LCD like every other node event, and
  so "when did this node's firmware change, and from what to what" is answerable
  months later.

After a successful flash the engine re-probes with the detector and stores the new
`modem.Identity`. The identity string coming back changed is the proof the flash
took — better evidence than the bootloader's own ACK, and it is what the operator
sees.

### 9. API, CLI, UI

    GET  /api/flash            current/last job, cached catalog, the detected board's matches
    GET  /api/flash/catalog    refresh the signed catalog now
    POST /api/flash            start a job → 202 {job_id}; one in flight, refused otherwise
    GET  /api/flash/events     SSE byte-level progress

    waypointd -flash-check     report what would be flashed, change nothing
    waypointd -flash           run it, for the bench and a headless caller

All behind the session wall, mirroring the update endpoints' shape. The UI is a
Hardware panel showing the detected board, the running firmware, what the catalog
offers, and a flash button that is **greyed with the server's reason** when the
match is ambiguous — the same rule [`buses.go`](../../cmd/waypointd/buses.go)
established: the validity verdict comes from the one validator, never re-derived in
JavaScript.

### 10. What is deliberately not in v1

- **Bootloader writes** (§1). SWD-only, documented as a procedure, never automated.
- **Full-size MMDVM F4/F7 (#25).** The AN3155 core is the same; only bootloader
  *entry* differs (jumper or DTR rather than a Pi GPIO line), so entry is an
  interface with one implementation today and the fast-follow tier adds a second.
- **DVMega (#26).** ATmega, `avrdude`'s protocol, a separate engine.
- **Operator-supplied local `.bin`.** The provenance argument in §2 does not survive
  it. Recorded as an open question, because the demand is real.

Downgrades **are** permitted: any catalog version may be flashed, older included.
Firmware carries no schema to migrate, and "the new release regressed on my board"
is a real thing an operator needs to escape from tonight.

## The contract (test harness)

Every side effect — serial, GPIO, usbfs, catalog fetch, the service holder, the
clock — is injected, so the engine is tested without a hat on the bench:

1. **AN3155 framing**, table-driven against a scripted fake: sync, a NACKed first
   sync retried, erase command chosen from what `GET` advertised, chunk alignment
   and size, XOR checksums, and a readback mismatch aborting the job.
2. **Interrupt and retry.** An injected failure mid-write leaves a failed job; a
   second run against the same fake (modelling half-written flash) syncs and
   completes. This is the acceptance criterion in unit form.
3. **Variant matching**, table-driven: unambiguous board ⇒ one artifact; several
   candidates ⇒ refusal naming them; `TCXOAssumed` ⇒ refusal; duplex mismatch ⇒
   refusal.
4. **Verification gate.** A bad signature or digest aborts with **no bytes written
   to the port** — asserted against the fake, not inferred.
5. **Arbitration.** Host running without authorisation ⇒ refused; with it ⇒ stopped
   and restarted, including when the job fails and when the caller's context is
   cancelled mid-write.
6. **Mutual exclusion** with stack update, asserted in both directions.
7. **Progress split.** A full flash publishes a bounded number of hub events
   (milestones only), while the job broadcaster sees every chunk.
8. **GPIO.** Chip selected by label against a fixture tree; the sysfs fallback
   computes its export number from the chip's `base`, asserted against a
   base-512 fixture and a base-0 one.

Manual, on the reference bench (MMDVM_HS_Dual_Hat on a Pi 3): flash from the UI
with no SSH; confirm the identity string changes; pull power mid-write and retry;
confirm a deliberately wrong-oscillator variant is refused before any write.

## Alternatives considered

- **Shell out to `stm32flash` and `dfu-util`** (the incumbent). Rejected. It adds
  two packages to the image, makes another project's argv and stderr into
  Waypoint's API — the exact class of breakage sbin #55 reports — and reduces
  progress streaming to scraping a percentage out of a text stream. The protocols
  are small and well specified; the C tools' value is portability Waypoint does not
  need, because Waypoint targets one board family on one OS.
- **Pin upstream's published `.bin` files by URL + SHA-256 instead of building
  them.** Rejected as the end state. It is strictly better than scraping, and the
  catalog format could express it, but it leaves an unsigned third-party artifact
  being fetched by a node that then runs it on a transmitter — the trust boundary
  RFC-0013 exists to draw. Building in CI also means the firmware and the host are
  versioned together, which is what makes "your board needs ≥ 1.4.8 for this" a
  thing Waypoint can enforce rather than mention.
- **A libusb binding (`gousb`) for the DFU path.** Rejected. usbfs is a handful of
  ioctls, `x/sys` is already a dependency, and cgo would end the static
  cross-compiled build the release pipeline depends on. `hd44780` and the modem
  serial layer set this precedent already.
- **Flashing from the operator's browser (WebSerial/WebUSB).** Rejected. The modem
  is attached to the node, not to the laptop; a browser path would work only for
  the USB boards and only on some browsers, and it would put the node's own
  arbitration rules outside the node.
- **GPIO path only; tell USB owners to use a PC.** Rejected. The USB stick boards
  are a large share of the installed base, and "use a Windows machine" is the
  answer this project exists to stop giving.

## Open questions

1. **DFU specifics need bench confirmation.** *(Partly closed, 2026-07-28.)* The
   application load address is no longer an inference: the firmware's own
   `bootloader.ld` places ROM at **`0x08002000`** with 120K, against `normal.ld`'s
   `0x08000000` with 128K — the 8K difference being the Maple bootloader, which is
   exactly the region §1 refuses to write. The alt-setting and the `1EAF` reset
   sequence are still transcribed from upstream's tooling rather than a datasheet,
   and the reference bench has no USB board, so the DFU path still does not ship
   until one is on it.

   Note also that the GPIO hats and the USB sticks are disjoint populations: a hat
   has no USB connection at all, and a stick does not expose BOOT0 to the host. The
   DFU path is not a fallback for the hats — it is the only path for a different
   set of boards.
2. **Bootloader recovery.** Do we publish an SWD procedure (an ST-Link clone is a
   few pounds) as the documented escape hatch for the 3B+ long-reset upgrade and
   for a board someone bricked elsewhere — or stay silent and let it be a return?
3. **Operator-supplied firmware.** Developers building their own MMDVM_HS want to
   flash it. An operator-registered signing key in the store is the honest shape; a
   plain "unverified flash" toggle is the easy one. Neither is in v1.
4. **Fork maintenance.** MMDVM_HS upstream is quiet. The fork inherits the rebase
   duty `pins.env` already documents for MMDVM-Host, and that duty needs a named
   owner before the first bump, not after.
5. **Who publishes the catalog.** If the firmware repo's release publishes
   `firmware.json`, a firmware release ships without a `waypointd` release and
   `waypointd` pins only the key and the URL. If Waypoint's release assembles it
   (the `update.json` precedent), the two are coupled. This RFC proposes the
   former; the coupling argument deserves a hearing.
