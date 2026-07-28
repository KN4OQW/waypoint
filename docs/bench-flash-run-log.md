# Bench run: firmware flashing (#19 / RFC-0019)

**2026-07-28.** MMDVM_HS_Dual_Hat on a Raspberry Pi 3 (`KN4OQW`, 172.16.50.13),
Raspbian 12 bookworm, kernel 6.12.25+rpt-rpi-v7, armhf. Both of issue #19's
acceptance criteria were met, and three defects were found that no amount of
emulated testing would have surfaced.

## What was run

`waypointd` built from `feat/firmware-flashing` (`GOARCH=arm GOARM=6`), pointed
at a **locally signed** firmware catalog served from the desktop over HTTP: the
`bin/mmdvm_f1.bin` build of the KN4OQW MMDVM_HS fork at `899fc2a`, signed with a
throwaway minisign key via a systemd drop-in overriding `-release-pubkey` and
`-firmware-url`. The release key was never involved. Both the catalog and the
image were verified with `waypointd -verify` before anything touched hardware.

Reflashing `899fc2a` over itself is the safest possible first write — identical
bytes, so a failure cannot be confused with a firmware regression — and the
identity string is the proof it took.

## The environment is the hard case

```
/dev/gpiochip0   pinctrl-bcm2835        54 lines   ← the header
/dev/gpiochip1   brcmvirt-gpio           2 lines
/dev/gpiochip2   raspberrypi-exp-gpio    8 lines   ← lines 20/21 exist here too
/dev/gpiochip4   pinctrl-bcm2835        54 lines   ← same label as gpiochip0
sysfs:           gpiochip512 base=512 ngpio=54 pinctrl-bcm2835
```

Everything issue #19 warns about is present at once: the sysfs base is **512**,
so every script that hardcodes `echo 20 > export` addresses nothing; the
**expander** is present with lines at the same offsets, wired to power rails and
the Ethernet PHY; and **two chips share the `pinctrl-bcm2835` label**, which the
fixture tests could not have predicted. `gpiochip0` is the one wired to the
header — established by resetting the modem through it, not by assumption.

## Criterion 1 — reflash from the UI, no SSH

```
POST /api/hardware/detect  → MMDVM_HS_Dual_Hat-v1.6.1 … GitID #899fc2a
GET  /api/flash            → match: hs_dual_hat-14m7456, 14.7456 MHz, duplex
POST /api/flash            → 202, job 1-1785264410
     fetching → writing 57916 → verifying 57916 → done
     before: 1.6.1   after: 1.6.1   error: none
```

934 progress events over `/api/flash/events`; **none** on the event hub, which
is the whole point of the two-stream split — those would have been 934 rows
written to the SD card. Three candidate boards (`mmdvm_hs_dual_hat`,
`zumspot_duplex`, `lonestar_dual`) collapsed to one image, so the ambiguity
never reached the operator.

The ROM bootloader reports **version 1.0**, not the 2.2 assumed for this part,
and advertises `ERASE` (0x43). Reading the command list out of `GET` rather than
hardcoding it was load-bearing on the first real board it ever met.

## Criterion 2 — an interrupted flash is recoverable

`SIGKILL` to `waypointd` at **14,080 of 57,916 bytes**. The daemon's GPIO lines
were released, the board came out of reset into half-written flash, and:

```
POST /api/hardware/detect  → /dev/serial0 → silent    (the modem is mute)
```

Recovery, on the mute board, with the ROM bootloader answering regardless:

```
GET  /api/flash   → match offered, from_config: true
POST /api/flash   → writing 57916 → verifying 57916 → done, after: 1.6.1
POST /api/hardware/detect → MMDVM_HS_Dual_Hat-v1.6.1 … GitID #899fc2a
```

## What broke, and what it changed

**1. The identity string was being corrupted** (`internal/modem`, from #18).
MMDVM_HS answers with protocol 1, the description, a **NUL**, and then the
chip's 96-bit UDID as ASCII hex. `cleanDescription` stripped control bytes and
concatenated, so `GitID #899fc2a` was read as
`899fc2a00350016390000074E503543` — a 31-character commit hash matching nothing
in any repository. MMDVM-Host stops at the NUL (`%.*s`, `strstr`); now so does
Waypoint, and the tail is stored as the UDID it always was.

**2. M17 was being denied on a board built for it.** MMDVM-Host enables M17 for
protocol-1 firmware by sniffing the description for `"v1.6."`. Waypoint's
`Modes()` claimed to mirror its assumptions "byte for byte" and omitted exactly
that rule — so it reported *no M17* for a v1.6.1 board whose M17 packet-data
support is the reason this project forks MMDVM-Host at all. Now
`m17: true`, confirmed over the wire.

**3. Recovery was impossible via the obvious button.** A modem too broken to run
its firmware cannot answer detection. Pressing Detect — the natural response to
a silent modem — found nothing, cleared the stored identity, and the flash
endpoint then refused with "run detection first". The board was recoverable only
over SSH, which is precisely the outcome #19 exists to abolish. Flashing now
falls back to the adopted configuration, and the panel says so.

The first two were found by reading a real reply; the third only by staging the
failure the acceptance criterion describes and then behaving like an operator.

## Not covered

- **USB / Maple DFU** — no USB board on this bench (RFC-0019 open question 1).
- **CI-built firmware** — this ran against a locally signed bench build. The
  `KN4OQW/MMDVM_HS` repo, its variant matrix and the signed catalog it publishes
  are the remaining work before an operator can do any of the above.
- **A cold power cut** — the interruption was a `SIGKILL` to the daemon, which
  is the same exposure for the modem (lines dropped mid-write) but leaves the
  Pi's own filesystem untouched.
