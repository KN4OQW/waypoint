# RFC-0020: Flashing USB Stick Boards (Maple DFU)

- Status: **proposed** (design only; adoption is gated on the trigger below)
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: the USB half of #19, split out of
  [RFC-0019](0019-firmware-flashing.md) once it became clear it is a coverage
  decision about different hardware rather than a missing piece of that one
- Depends on: RFC-0019 (the catalog, the engine, the API and the UI are all
  shared; this adds one transport behind them)

## Summary

Waypoint flashes GPIO hats through the STM32's ROM bootloader
([RFC-0019](0019-firmware-flashing.md)). The USB stick boards — ZUMspot USB,
Nano hotSPOT, NanoDV USB, LoneStar USB, the Libre Kit in its USB form — cannot
be reached that way, and need the **Maple/STM32duino DFU bootloader** that lives
in the first 8 KB of their own flash.

This RFC records that design, fully researched, and **does not propose building
it yet**. The firmware CI already produces and signs the images
(`*-usb` variants, linked at `0x08002000`), so adoption is host-side work alone.

## Why this is not part of RFC-0019

**They are disjoint populations, not two ways of doing one thing.** A GPIO hat
has no USB connection to the Pi at all; a USB stick does not expose BOOT0 to the
host. Neither path is a fallback for the other, and the hats — Waypoint's launch
tier and current target — are completely served without this.

**It is the only brickable path.** A hat's ROM bootloader is mask-programmed and
cannot be erased, so an interrupted flash is recoverable by retry as a property
of the silicon. A stick's reachability lives in 8 KB of its own flash. That
asymmetry is the whole reason RFC-0019's safety model has two halves.

**Validating it needs hardware nobody here owns.** A ZUMspot USB or Nano hotSPOT
is around $150. Shipping an unvalidated flash path for the one class of board
that can be destroyed by it is precisely the wrong way round, and the project
has no way to run its own acceptance test.

## Adoption trigger

Adopt when **a USB stick board is on the bench**, and not before. At that point
the work is: the reset trigger, the usbfs DFU transport, and settling the
verification question below. Everything else — catalog, matching, arbitration,
progress, API, UI — already exists and is exercised by the GPIO path.

## Design


Three steps, none of them shelled out:

1. **Enter the bootloader — with the 1200-baud touch, not the `1EAF` magic.**
   A running board is at `1eaf:0004` (its CDC serial interface). The firmware
   implements *two* ways in, and they are not equally good (`STM32F10X_Lib/usb/
   usb_serial.cpp` in the firmware fork):

   - **Baud 1200 plus a DTR negative edge.** The CDC control handler watches for
     a high→low DTR transition while the line coding is 1200 baud, and on seeing
     one arms the independent watchdog with a reload of 10 and spins — a hardware
     reset into the bootloader. This is the Arduino "1200bps touch", and the host
     side is pure termios: set 1200 baud with DTR asserted, then drop DTR. No USB
     stack is involved in the trigger at all, and Waypoint already has every
     primitive it needs.
   - **The four-byte `1EAF` sequence**, which upstream's `upload-reset` uses, and
     which the firmware's own authors annotate: *"FIXME this is mad buggy; we
     need a new reset sequence. E.g. NAK after each RX means you can't reset if
     any bytes are waiting."*

   Waypoint uses the first. An earlier draft of this RFC specified the second,
   from reading upstream's tooling rather than the firmware it drives.

   Then wait for the device to re-enumerate as `1eaf:0003` — an ID
   [`modem/ports.go`](../../internal/modem/ports.go) already recognises and
   already reports as "in its bootloader, one flash from working".
2. **Write.** DFU class requests over `/dev/bus/usb/BBB/DDD` usbfs ioctls
   (`USBDEVFS_CLAIMINTERFACE`, `USBDEVFS_SETINTERFACE`, `USBDEVFS_CONTROL`) — no
   libusb, no `dfu-util`, no cgo, so the static cross-compiled build survives.
   DFU is control transfers only (`DFU_DNLOAD`, `DFU_GETSTATUS`, `DFU_CLRSTATUS`,
   `DFU_ABORT`), which is the smallest useful subset of USB there is.

   **Alt setting 2**, which the bootloader maps to `0x08002000` — matching
   `bootloader.ld` in the firmware, so the two agree by evidence rather than by
   assumption. Alt 1 is the legacy `0x08005000` layout and alt 0 is a RAM upload
   the bootloader deliberately refuses; Waypoint offers neither.

   **Verification is an open question on this path.** The bootloader does
   implement `DFU_UPLOAD`, but `dfuCopyUPLOAD` returns from `userAppAddr +
   userFirmwareLen + offset` — read-back semantics tied to what was just
   downloaded rather than free addressing. Whether it can verify an image the way
   the ROM bootloader's `READ_MEMORY` does has to be established on a board. If it
   cannot, the fallback is to reset and re-probe, comparing the identity string —
   weaker than a byte-for-byte readback, and the UI must say which of the two it
   got rather than reporting both as "verified".
3. **Leave.** DFU detach and re-enumeration back to `1eaf:0004`, then re-probe.

**The 3B+ quirk, explained rather than worked around.** An STM32F103 hard-pulls
USB D+ high, so a soft reset does not disconnect it and the host never
re-enumerates. The STM32duino bootloader forces one by reconfiguring D+ (PA12) as
GPIO and driving it low briefly. `generic_boot20_pc13_long_rst.bin` holds it low
*longer* than `generic_boot20_pc13.bin`, and a 3B+'s USB hub does not notice the
short pulse.

That is a device-side timing property, fixable only by writing a different
bootloader — which needs SWD and is the one write §1 forbids. `make stlink-bl`
does exactly that, via st-flash, and it is a bench operation with a wire, not
something a web page can offer. So the host side gets a longer enumeration
window, and a board that still does not come back is reported as what it is —
"this board carries the short-reset bootloader; on a 3B+ it needs the long-reset
one, which requires SWD" — rather than retried until the operator gives up.


## Open questions

1. **Can a flash be verified on this path at all?** The bootloader implements
   `DFU_UPLOAD`, but `dfuCopyUPLOAD` returns from `userAppAddr + userFirmwareLen
   + offset` — read-back tied to what was just downloaded rather than free
   addressing. If it cannot verify an image the way `READ_MEMORY` does, the
   fallback is reset-and-re-probe against the identity string, and the UI must
   distinguish the two rather than calling both "verified". RFC-0019 treats a
   readback as non-optional; this path may not be able to honour that, and
   saying so is better than quietly weakening the word.
2. **Bootloader recovery.** A board whose bootloader is damaged, or which
   carries the short-reset build and lives on a 3B+, needs SWD. Do we publish
   the procedure (an ST-Link clone is a few pounds) as the documented escape
   hatch, or treat it as a return-to-vendor?
3. **Which USB boards are actually in the wild.** The catalog names five, from
   upstream's config set. Whether operators own them in numbers that justify the
   work is worth knowing before doing it — this RFC is cheap to leave sitting.
