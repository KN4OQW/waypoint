# Voice encoders for spoken alerts

Waypoint synthesises speech and hands 8 kHz audio to an encoder. It does not
ship, build, bundle or endorse an encoder, and it has no dependency on one:
the `external` backend is a command the operator configures.

This page documents the contract and the backends known to satisfy it, so the
feature is discoverable without the project redistributing anything.

## The contract

An external encoder is any command that:

- reads **8 kHz signed 16-bit little-endian mono PCM** on stdin, and
- writes **raw AMBE codewords** to stdout.

One codeword per 20 ms of audio, i.e. per 160 samples.

### The size, and the mistake to avoid

Codewords are **7 bytes — the raw 49-bit AMBE+2 voice bits, with no FEC.**

This is the easy thing to get backwards. A vocoder library typically offers both
an encode and an encode-with-error-correction entry point, and the second gives
the 72-bit, 9-byte form. Waypoint's frame layer applies DMR's Golay protection
itself while building the burst, so feeding it protected codewords produces
protection over protection. The result is noise on the air, and no layer between
the encoder and the radio reports anything wrong. Call the plain encoder.

`internal/bus/frames.AMBEBytes` is the authority for this number, and the
external backend defaults to it.

## Known-working backends

### AMBE-3000 hardware (DVstick 30, ThumbDV, etc.)

A licensed DVSI codec chip over USB. Clean provenance and the option to prefer
if you are choosing one; no build step and no firmware to source. Waypoint's
`dongle` backend is a placeholder and is not implemented — until it is, drive a
dongle through `external` with a command of your own.

### md380_vocoder (software, ARM)

<https://github.com/nostar/md380_vocoder> — GPL v2+, AMBE+2 at 2450x1150.

Things to know before choosing it:

- **It does not run on a Pi Zero or Pi 1.** Its README is explicit: Pi 2, 3 or 4
  only. Waypoint targets Pi Zeros, so this cannot be a universal backend.
- **Its build downloads MD380 radio firmware** and links it in at fixed
  addresses. That image is Tytera's copyrighted firmware containing DVSI's AMBE
  implementation; it is not ours to redistribute, which is why nothing here
  ships it and why the library fetches it rather than vendoring it.
- **Obtain the firmware from your own radio's official update package** if you
  own an MD380/MD390. That is better provenance than any mirror and does not
  depend on a third-party host staying up.
- **Verify whatever copy you obtain.** The image this was tested against is
  994,304 bytes, SHA-256
  `c0351f250a834660641bca3b06be931c80cc1ed0ef808356c550b99cd0f4c632`.

#### Building it (operator side, tested on a Pi 3)

The library links a firmware image at absolute addresses, so the final link has
to place two sections explicitly. Without that, `md380_init` fails with ENOMEM
or the process segfaults — a modern toolchain defaults to a position-independent
executable and relocates them.

```sh
make                                   # builds libmd380_vocoder.a

gcc -O2 -no-pie \
    -Wl,--section-start=.firmware=0x0800C000 \
    -Wl,--section-start=.sram=0x20000000 \
    -o wpambe wpambe.c -L. -lmd380_vocoder -lm
```

A minimal adapter satisfying the contract above is roughly:

```c
#define SAMPLES  160   /* 20 ms at 8 kHz */
#define CODEWORD 7     /* raw 49 bits -- NOT the FEC'd 9-byte form */

int md380_init(void);
void md380_encode(uint8_t *ambe49, const int16_t *pcm);

/* read 160 samples, call md380_encode, write 7 bytes, repeat;
   zero-pad a trailing partial block so the last syllable is not clipped */
```

Verify before wiring it up — one second of audio must produce 50 codewords:

```sh
./wpambe < one-second-8k.raw | wc -c     # expect 350
```

#### Configuring it

Weather panel → Spoken Alerts → Voice encoder → **External command**, then the
command, e.g. `/usr/local/bin/wpambe`.

## Licensing, plainly

The encoder you choose is yours to obtain and run. Waypoint links no vocoder,
distributes no firmware, and takes no dependency on either — the interface is a
command name in your configuration. AMBE+2 is patent-encumbered independently of
any implementation's copyright, which is why licensed hardware exists and why
this project ships a socket rather than a plug.
