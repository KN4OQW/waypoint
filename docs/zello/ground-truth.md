# Zello bridge: ground truth

Prompt 1 of the runbook in [docs/design/zello-bridge.md](../design/zello-bridge.md).
No product code. This file records what was checked, against what, and what the
check said — including the three places the design record was wrong.

Verified 2026-08-23 against:

| Source | Commit | Dated |
|---|---|---|
| zelloptt/zello-channel-api | `e11b2d5b0853617fc82d6ae968be50538277f8ad` | 2026-05-27 |
| DVSwitch/md380tools | `3c6acfeac72d61eda04db8ad5ee6164318c478b7` | 2018-05-04 |
| travisgoodspeed/md380tools | `d7f4206c146a` | 2026-02-22 |

## Summary

Confirmed: the one-channel-per-connection limit, the `codec_header` layout, the
`packet_duration` range, the 30-second ping contract, `-e` encode, and `-S`.

Corrected: the `-S` wire protocol is nothing like the design record said. There is
no packet header at all. And the server's AMBE form is already Waypoint's canonical
7-byte codeword, so a piece of Prompt 2 disappears.

Not run: the live byte capture against a running server, because it needs an ARM
target this desktop does not have. See [what was not done](#what-was-not-done).

## Zello Channels API

### Confirmed

**One channel per connection on consumer Zello.** API.md:63, verbatim:

> Connecting to multiple channels (up to 100) is currently supported for Zello Work only.

D4 stands as written; the channel manager needs one connection per channel.

**Logon requires a token on Friends and Family.** API.md:71-72 — `auth_token` is
"(Zello Friends and Family only) API authentication token. If omitted
`refresh_token` is required." `username` is optional and, if absent, "the client
will connect anonymously"; `password` is required if `username` is given. A
gateway that talks therefore needs both a token and a named account.

**A successful logon returns a refresh token.** API.md:145 — it "can be used to
quickly reconnect if the WebSocket connection is broken due to brief network
interruption."

**The 30-second ping contract.** API.md, verbatim:

> The API monitors connectivity by sending a WebSocket Ping frame to the client
> every 30 seconds. The WebSocket client must respond to the Ping frame with a
> Pong frame. If a client takes longer than 30 seconds to respond with a Pong
> frame, the API terminates the connection.

**`codec_header`.** API.md:181-191 — base64 of a 4-byte array,
`{sample_rate_hz(16LE), frames_per_packet(8), frame_size_ms(8)}`. The documented
example `gD4BPA==` decodes to `{0x80, 0x3e, 0x01, 0x3c}` = 16000 Hz, 1 frame per
packet, 60 ms. That example is the builder's unit test.

**`packet_duration`.** API.md:176 — "Values between 2.5 ms and 60 ms are
supported," on both `start_stream` and `on_stream_start`.

**Sample development tokens expire in 30 days.** AUTH.md — obtained from
developers.zello.com under Keys, Add Key, and "for development only and must never
be used in production app." Production tokens are signed on your own server from
the Issuer and Private Key.

### Corrections to the design record

**`frames_per_packet` is 1 or 2, not free.** API.md:188 constrains byte 2 to
"(1 or 2)". The design record left it open. It has to be one of those two.

**The listen-only error code is `listen only connection`, unhyphenated.** The
hyphenated form the design record quoted is the *description*: "The client tried
to send a message over listen-only connection." Matching on the description
instead of the code would never fire.

**`no phone` is not a documented error code.** The design record called it "a
common real-world transmit failure." It appears nowhere in this repository — not
in API.md, not in the error table at API.md:613-631, not anywhere in the tree. The
nearest documented codes are `channel is not ready`, `listen only connection` and
`failed to start stream`. It may be folklore from a community bridge, an older API
revision, or a Zello Work behaviour. Do not write a client that keys off it, and
do not treat its absence in a live test as a contradiction.

The full documented error table is at API.md:613-631. The three the design record
named that do exist: `not authorized` ("Username, password or token are not
valid."), `channels limit exceeded`, and `internal server error`.

## md380-emu

### `-e` and `-S` both exist, but not in the same place

Current upstream `travisgoodspeed/md380tools` (HEAD 2026-02-22, actively
maintained) parses `getopt(argc,argv,"edVvo:i:")` — **`-e` encode is present and
maintained upstream.** There is no `-S` and no `ambeServer` anywhere in it.

`DVSwitch/md380tools` parses `getopt(argc,argv,"S:edVvo:i:")` and carries
`ambeServer()` in `emulator/md380-emu.c:100-146`. **`-S` exists only in the fork.**

This settles the encode ambiguity the design record flagged. The wiki text saying
"encoding from WAV to AMBE will come soon" is stale documentation, not a statement
about the code: `-e` is in maintained upstream today. The fork's own
`emulator/README.md` still says "For now, it only decodes AMBE to raw audio,"
which is the same stale text, in a tree whose code plainly encodes.

**The fork is a 2018 snapshot.** Its entire history is one squashed commit,
"Initial commit," dated 2018-05-04. It has had no upstream merges in eight years.
Depending on it means depending on an unmaintained tree for the whole vocoder,
including the md380tools issue #925 SIGSEGV, which will never be fixed there.

`ambeServer()` is 46 lines and depends on nothing else in the fork. Carrying it as
a patch against maintained upstream is very likely the better arrangement — same
protocol, current firmware handling, a fix path for #925. Recommended for D2; see
[what this changes](#what-this-changes-in-the-runbook).

### The `-S` wire protocol

The design record described this as "the DV3000/AMBEServer packet protocol (start
byte `0x61`, big-endian length, packet type)". **It is not.** From
`emulator/md380-emu.c:100-146`, in full:

- `socket(AF_INET, SOCK_DGRAM, 0)` — UDP, as suspected.
- `bind()` to `htonl(INADDR_ANY)` on the `-S` port. **Not loopback-only.**
- A `select()` loop, then `recvfrom()` into a 1024-byte buffer.
- **Dispatch is on datagram length alone. There is no header, no magic byte, no
  length field, no packet type, and no sequence number.**

		if (n == 320) { encode_amb_buffer(ambe49, buffer); sendto(... ambe49, 7 ...); }
		if (n == 7)   { decode_amb_buffer(buffer, pcm);    sendto(... pcm, 320 ...); }

So the protocol in its entirety is: send exactly 320 bytes (160 signed 16-bit
samples, 20 ms at 8 kHz) and receive exactly 7 bytes back; send exactly 7 bytes
and receive exactly 320 back. A datagram of any other length is silently
discarded — no error, no reply.

Four consequences the client has to be written around:

1. **No request/response correlation.** Nothing identifies which reply belongs to
   which request. The client must keep exactly one request outstanding at a time
   and treat a missing reply as a timeout, not wait for a match. A pipelined
   client would silently mis-pair frames under loss.
2. **`INADDR_ANY`, not `127.0.0.1`.** As shipped, the vocoder answers anyone on
   the LAN who sends it 320 bytes. On a Waypoint node that is an unauthenticated
   network service. Bind it to loopback in the patch, or firewall the port; do not
   ship it as-is.
3. **`MSG_DONTWAIT` on both replies.** Under buffer pressure a reply is dropped
   silently rather than blocking, which reads to the client as a timeout.
4. **Single consumer.** The reply goes to whatever address `recvfrom` last wrote
   into `sa_read`, so two clients on one server interleave each other's audio.

### The server speaks Waypoint's canonical codeword already

This is the useful discovery. `encode_amb_buffer` (`emulator/ambe.c:252-282`)
packs its 7-byte output as bits 0-47 MSB-first into bytes 0-5, and bit 48 into
byte 6 as `0x80`:

	ambe49[0] = ambe[7] | ambe[6]<<1 | ... | ambe[0]<<7;   // bits 0-7, MSB-first
	...
	ambe49[6] = ambe[48] ? 0x80 : 0;                       // bit 48 -> byte 6 MSB

Waypoint's canonical form is the same layout. [internal/bus/frames/bits.go](../../internal/bus/frames/bits.go)
addresses bits MSB-first with `bitMask{0x80..0x01}`, byte `i>>3`, mask `[i&7]`, so
bit 48 lands in byte 6 under mask `bitMask[0]` — which is `0x80`. `AMBEBytes` is
7. The two agree bit for bit, and the golden fixture in
[ambe_vocoder_test.go](../../internal/bus/frames/ambe_vocoder_test.go),
`f8fe14398c5080`, ends in the `0x80` that layout predicts.

**So there is no mapping to write.** The `-S` server's 7 bytes go straight onto
the bus and straight into the existing `dmrAMBEFromCanonical`. Prompt 2 scope (b)
is unnecessary.

### The `.amb` file format is a different shape — and irrelevant here

The 8-byte `.amb` frame the design record described is real, but it is the file
format used by `-d`/`-e`, not the server. Confirmed structurally against the
fork's `sample.amb`: 1428 bytes = a 4-byte `2e 61 6d 62` (".amb") magic plus
exactly 178 eight-byte frames, which at 20 ms a frame is 3560 ms of audio.

Per `emulator/ambe.c:38-68`, a file frame is `packed[0]` status (warned about if
nonzero), `packed[1..6]` bits 0-47 MSB-first, and `packed[7] & 1` bit 48 **as the
LSB**. The server puts bit 48 in byte 6 as the **MSB**. The two forms differ both
in the byte offset and in where the 49th bit sits inside its byte, so code that
confuses them produces noise rather than an obvious failure. Waypoint touches only
the server form.

### A defect in the fork's file-decode path

Noted because it is evidence about which path is exercised, not because Waypoint
uses it. `emulator/ambe.c:41-50` reads the 4-byte magic, then:

	packed[5]=0;
	if(!strcmp(packed,".amb")){
	  fprintf(stderr,"Incorrect magic of %s.\n",infilename);
	  exit(1);
	}

The terminator is written to index 5, leaving `packed[4]` uninitialised, and the
test is inverted — `strcmp` returns 0 on a match, so `!strcmp` is true exactly
when the magic *is* `.amb`. Read literally, `-d` rejects valid files and accepts
invalid ones. That this survives in the fork says the file path is not what
DVSwitch runs; `-S` is. Another reason to prefer the server entry point.

## What was not done

**The live loopback byte capture.** Prompt 1 asks for it: send 320 bytes at a
running server, log what comes back. It was not run, and no claim here rests on
having run it.

The reason is the build. `emulator/Makefile` sets
`CC=arm-linux-gnueabihf-gcc -static` and links the binary against the MD380
firmware image at fixed addresses (`--section-start=.firmware=0x0800C000`,
`.sram=0x20000000`), with symbols from `../applet/src/symbols_d02.032`. md380-emu
is not a portable C program that emulates ARM — it *is* ARM code carrying the
licensed firmware, run natively on ARM or under qemu-user elsewhere. The
emulator README says so: "allowing it to run under Linux through qemu-user or
natively in an AARCH32 environment."

This desktop is x86_64 with no `arm-linux-gnueabihf-gcc`, no `qemu-arm`, and no
passwordless sudo, so building it means either installing a cross toolchain and
qemu here, or building on the armhf bench node — which also means fetching the
licensed firmware blob onto whichever machine does it. Both are decisions for the
maintainer rather than side effects of a verification pass.

Nothing above depends on it. The protocol is 46 lines of unambiguous C read in
full, and the bit layout is checked against a fixture that a real vocoder
produced. The capture would confirm the reading; it would not change it. What it
*would* independently establish is the per-frame timing, and that belongs to
Prompt 8 on real hardware anyway.

## What this changes in the runbook

**Prompt 2 loses scope (b).** There is no 7-byte-to-8-byte `.amb` mapping to
write; the server already speaks the canonical form. What replaces it is the
strict one-request-at-a-time discipline the headerless protocol forces, and a
timeout path, since a dropped datagram is indistinguishable from a slow one.

**D2 should target upstream plus a patch, not the fork.** `-e` is in maintained
upstream; only the 46-line `ambeServer()` is not. Depending on a 2018 snapshot for
the entire vocoder to get those 46 lines is the wrong trade, and it forecloses any
fix for issue #925.

**The vocoder port must be bound to loopback.** As shipped it binds `INADDR_ANY`.
Whether by patch or by firewall, that is a privacy-gate question and not an
implementation detail — an unauthenticated network service on a Waypoint node is
exactly what the project's posture forbids.

**D4 is unchanged**, and Prompt 3 can be written against the documented model as
long as it keys off `listen only connection` rather than the hyphenated
description, and does not expect `no phone`.

## Stop and report

Prompt 1 says to stop if the frame sizes differ from what the design record
documented. They do — not in size but in framing: there is no packet header where
one was specified. The reading is unambiguous and it makes the client simpler
rather than harder, so this is reported rather than treated as a blocker, and no
product code was written either way.

## Addendum: what the hardware said

Added after the desktop gained an ARM cross toolchain and the bench Pi 3 was
available. This section supersedes [what was not done](#what-was-not-done) above:
the build was done, and it changed the answer.

### The `-S` server does not work on the target

md380-emu was built as recommended — maintained upstream
(`d7f4206`) plus a backported `ambeServer()` bound to `INADDR_LOOPBACK` rather
than `INADDR_ANY` — cross-compiled armhf, and run on the bench Pi 3
(armv7l, kernel 6.12.25+rpt-rpi-v7).

It binds. It accepts a datagram. It then segfaults on the first request, in both
directions, with no output. The same happens under `qemu-arm-static` on the
desktop, and — decisively — **the DVSwitch fork's own unmodified binary,
built identically, segfaults in exactly the same place.** So this is not the
backport, and it is not the loopback change: the server path is broken on a
current Raspberry Pi OS kernel, which is what md380tools issue #925 describes.

The vocoder itself is fine on the same binary and the same machine. The file
path decodes `sample.amb` to 56960 bytes of PCM — 178 frames, exit 0 — natively
on the Pi. Only the server entry point dies.

### The 46-line claim was wrong

Recorded because this file made it. `ambeServer()` does not stand alone: it calls
`encode_amb_buffer` and `decode_amb_buffer`, which are fork-only too and are not
in upstream at all — upstream has only the file modes. The real backport is those
two functions plus the server plus two header declarations, and it only compiles
because `ambe_encode_thing2` and the firmware buffer symbols happen to exist
upstream already. Still small, but not self-contained, and the claim was made
from reading one function instead of trying to build it.

### There is a working vocoder on the bench already

The bench node carries `/usr/local/bin/wpambe` and `/tmp/md380_vocoder/`, from
earlier Waypoint work: AD8DP's `md380_vocoder` library (Doug McLain, GPL-2.0,
derived from md380tools) wrapped by a small `wpambe.c` that adapts it to
Waypoint's existing external-encoder contract. Its header declares exactly the
API this feature needs, with the layout already correct:

	int  md380_init(void);
	void md380_decode(uint8_t *ambe49, int16_t *pcm);  // 7 bytes MSB order -> 160 samples
	void md380_encode(uint8_t *ambe49, int16_t *pcm);  // 160 samples -> 7 bytes MSB order
	void md380_decode_fec(const uint8_t *ambe, int16_t *pcm);
	void md380_encode_fec(uint8_t *ambe, const int16_t *pcm);

"Packed into 7 uint8_t elements in MSB order" is the canonical form, so the
conclusion above — that no repacking is needed — survives the change of vocoder.
It also carries FEC variants, which Waypoint does not need because
`internal/bus/frames` already does that and is golden-tested.

`wpambe` runs on the Pi. The earlier `wpambe: vocoder init failed` in
`/tmp/wpambe.err` was a working-directory problem, not a real failure:
`md380_init()` loads its firmware images from the current directory, and from
`/tmp/md380_vocoder` it initialises cleanly.

### The real-time question, answered

One second of 8 kHz audio — 50 frames — encoded through the library on the
bench Pi 3:

	real 0m0.051s   user 0m0.020s   sys 0m0.007s
	16000 bytes of PCM in, 350 bytes (50 x 7) of AMBE out

**About 1 ms per 20 ms frame, roughly 20x faster than real time**, single stream,
on the Pi 3 that is the floor of the supported hardware. The design record noted
that no published per-frame benchmark existed and that Prompt 8 would have to
measure it. It is measured, and it is not close.

The codewords are real, not zeros: the first frame of a 1 kHz tone encodes to
`f8f011044ca880`, settling to `ff92020202 0800` in steady state. Byte 6 carries
`0x80` on the first frame, which is the canonical bit-48 placement this file
predicted.

### What this means for D1 and D2

D2's premise is gone. The `-S` UDP server is not a viable transport on the
hardware Waypoint targets, and its whole protocol — the headerless dispatch, the
one-request-at-a-time discipline, the straggler problem — exists only to talk to
it. Linking `md380_vocoder` removes all of that: an in-process function call has
no framing, no correlation problem and no timeout path.

That also undermines D1's reasoning. The external vocoder service was justified
as containing the licensed blob and the #925 crash risk in something restartable.
A linked library contains neither, so the argument for keeping the vocoder out of
process weakens considerably — while the argument for cgo gets cheaper, because
D3 already accepts cgo for libopus in the same binary.

There is a third option the plan never considered, and Waypoint already ships it:
`internal/wxvoice` has an `ExternalVocoder` backend that shells out to an
operator-supplied encoder command, which is exactly what `wpambe` was written
for. It is encode-only today, and it is a pipe rather than a call, but it is an
existing, shipped contract for "the operator supplies a vocoder we may not
redistribute" — the same licensing problem this feature has.

This is a decision for the maintainer, not a detail to settle in code. Prompts 3
through 7 were not started, because Prompt 4's endpoint shape depends on which
way it goes.

## Addendum: the first live contact

A logon was sent to `wss://zello.io/ws` with a real consumer account's username
and password and no `auth_token`. The server answered:

	{"error":"not enough params","seq":1}

That is a sharper answer than AUTH.md gives, and it is worth having. The refusal
is `not enough params`, not `not authorized` — the request is rejected as
incomplete and the credentials are never evaluated at all. So the token is not an
alternative to an account password, and a working Zello password proves nothing
about whether a bridge will connect.

It also confirms the auth model this package was built against rather than
contradicting it, which is the outcome Prompt 3 could not reach without an
account: `auth_token` really is required on consumer Zello, and the client's
refusal to dial without one matches what the service does.

`describeError` now names this case, because it is the failure an operator who
has entered everything they think they need will actually hit. The save-time
validator refuses an enabled account with no token before it gets that far, but
the message has to be right for the path that reaches the wire.

Still unverified: everything after logon. No token was available, so no stream has
been started, no audio has crossed, and `on_channel_status` has never been seen
from the real service.
