# Bridging a bus to a Zello channel

Status: design record, 2026-08-23. Nothing is built. This file exists so the
work can be handed to a coding agent without the agent re-deriving the
decisions, and so the decisions are reviewable before any of them are
load-bearing.

The RFC process is dormant pre-v1 ([GOVERNANCE.md](../../GOVERNANCE.md#rfcs)), so
this is the record — not a proposal, and not something to open an RFC for. The one
place a future RFC-0003 addendum would be warranted is named at the end.

## What is being bridged

A Zello channel is an audio room reached over a WebSocket, carrying Opus. A
Waypoint bus is a named cross-mode meeting point carrying 7-byte AMBE+2
codewords, one talker at a time, defined by RFC-0003. Bridging them means a
vocoder and a transcode, and everything hard about this design follows from that.

	DMR RF <-> MMDVM-Host <-> loopback <-> waypoint-bus <-> Zello transport <-> wss://zello.io/ws
	                                       (AMBE codewords)  (vocoder + Opus)

## Corrections against the tree

The design work behind this file was done against upstream sources and a reading
of Waypoint that turned out to be wrong in five places. They are recorded here
rather than quietly fixed, because four of them would each have sent an agent to
build something that already exists or to put code in the wrong binary.

**The bus is RFC-0003, not RFC-0004.** RFC-0004 is the persistent event store.
The bus, its four loop-prevention rules, its single-talker arbitration and its
"a mode attaches to at most one bus" constraint are all RFC-0003 §5; the
loopback-port coordination and the apply contract are RFC-0003 Addendum A. An
agent sent to RFC-0004 for bus semantics reads about event retention. RFC texts
live in the repository's GitHub Discussions, not in `docs/`.

**The bus is not in waypointd.** `cmd/waypointd` does not import
`internal/bus/...` at all. `waypoint-bus` is its own binary, one process per bus,
started from the templated unit `waypoint-bus@.service` against a rendered
`/var/lib/waypoint/etc/waypoint-bus-<name>.json`. waypointd's part is rendering
that config and reconciling the units. This invalidates the premise D1 was
decided on; see D1 below.

**The 49-to-72-bit FEC transform already exists and is golden-tested.**
[internal/bus/frames/dmr.go](../../internal/bus/frames/dmr.go) provides
`dmrAMBEFromCanonical`, `dmrExtractAMBE2`/`dmrInsertAMBE2` and
`AMBEBytes`/`dmrAMBEBytes`. [ambe_vocoder_test.go](../../internal/bus/frames/ambe_vocoder_test.go)
pins the canonical-to-on-air conversion byte for byte against two encodings of
the same 20 ms produced by a real md380 vocoder on a Pi, through its plain and
its FEC entry points. That test exists because getting it wrong puts noise on the
air while every layer in between reports a clean transmission. Nothing in this
feature reimplements it.

**Talker Alias emission already exists.** [internal/talkeralias/](../../internal/talkeralias/)
carries `Emitter.Observe` driven by a resolver, with golden tests. Three live
branches — `feat/phonebook-resolver`, `feat/phonebook-talker-alias`,
`feat/phonebook-callsign-search` — carry in-flight resolver work.

**The node store is `/var/lib/waypoint/config.db`**, per `waypointd.service` in
the image module. `/home/pi-star/waypoint/config.db` exists only on nodes flashed
before the path split, and that unit's own comment says so.

## Decisions

### D1 — Where the transport lives: an endpoint in waypoint-bus

*Revised.* The original decision was "bus-native inside waypointd, because the bus
is in-process and waypointd is the sole RF gatekeeper." The bus is not in-process
in waypointd and waypointd is not on the audio path, so that decision was made
between two options neither of which describes this tree.

The Zello transport is a new endpoint kind in `cmd/waypoint-bus`, alongside the
UDP loopbacks in [endpoints.go](../../cmd/waypoint-bus/endpoints.go), with its
protocol logic in an `internal/bus/zello` package it wraps. That is where every
other attachment already lives, it inherits arbitration and loop prevention for
free (D5), and it puts the audio path in one process with no broker hop.

It also lands the cgo dependency in the right binary. `waypoint-bus` is one
process per configured bus and is not on any node that has not configured one;
waypointd is on every node. Building libopus into waypoint-bus costs a node with
no bus nothing, and keeps waypointd pure Go and cross-compilable (D3).

The rejected alternative is a separate gateway daemon speaking to the bus over
MQTT, like the bot framework. Cleaner isolation, and it would contain cgo
completely, but it doubles the latency budget on a 20 ms audio path and puts PTT
arbitration across a broker, which is exactly the thing RFC-0003 §5 solves
in-process. Keep it in reserve for the case named in D3.

### D2 — Vocoder: md380_vocoder linked via cgo, runtime firmware load

*Revised after the Prompt 2 stop, superseding both the original decision and its
first revision.* The md380-emu `-S` server segfaults on kernel 6.12.25
(md380tools #925) in both our build and DVSwitch's own unmodified binary; only
the server entry point dies, the vocoder is healthy. AD8DP's `md380_vocoder`
library (GPL-2.0-or-later, compatible with this project's GPL-3.0) exposes
`md380_encode`/`md380_decode` over the canonical 7-byte MSB-packed 49-bit form
directly, and measured 592 us for encode and decode together per 20 ms frame on the bench
Pi 3 — thirty-three times real time, on the kernel where the server dies.

It links into `waypoint-bus` behind the `zello` build tag.

The isolation the external service bought is already provided elsewhere. The
firmware blob is loaded at runtime from a configured path, fetched at enable,
never linked: upstream's Makefile objcopy-embeds the `.img` files into the
artifact and **must not be built that way**, since that redistributes the blob in
every `.deb` and image. And a vocoder crash is contained by the one-process-per-bus
model under `waypoint-bus@.service` — it kills one bus daemon, not waypointd.

Two properties the binding must hold, both established on hardware rather than
assumed:

- **Zero the 7-byte output before every encode.** `md380_encode` ORs into bytes
  0-5 rather than assigning them. Encoding a frame into a `0xff`-filled buffer
  returns `ffffffffffff00` where a zeroed buffer returns `f8f011044ca880` — the
  same frame, unrecognisable.
- **Use only the plain entry points.** The 72-bit FEC form stays
  `internal/bus/frames`' business, which is golden-tested. The library's
  `md380_encode_fec`/`md380_decode_fec` are not used; applying protection twice
  is noise on the air that nothing in between reports.

ARM-only. CI runs against the golden fixtures; vocoder-touching tests gate on
hardware; Pi Zero 2 W joins the validation matrix unverified.

The DV3000/ThumbDV path stays as the optional hardware alternative and the fully
licensed option, since the licence rides on the DVSI chip. The `ExternalVocoder`
pipe in `internal/wxvoice` is the named fallback **only** if the runtime-load
variant cannot be kept clean — a separate helper can be built on-device with
linked firmware without tainting shipped binaries — and that contract is
otherwise untouched. codec2 and mbelib remain inapplicable: codec2 is not
AMBE-compatible and will not decode on a DMR radio, and mbelib is decode-only.

#### Runtime load, proved on the bench

The bytes and the symbols are separable, which is what makes the runtime load
possible. `md380_vocoder.o`'s references to firmware functions are resolved at
link time from md380tools' `symbols_d02.032`, which is addresses and no code; the
bytes those addresses point at are mapped at run time with `MAP_FIXED` from files
the operator supplies. `md380_init()` is then only
`mprotect((void*)0x800c000, 0xf2c00, PROT_EXEC)` over the region already mapped —
`0xf2c00` is exactly the length of the D002.032 image.

Linked without `firmware.o` and `ram.o` on the bench Pi 3, the test binary is
67 KB with 56 KB of text, against a 994 KB firmware image that is nowhere in it,
and it produces `f8f011044ca880` for a 1 kHz tone — byte-identical to the
linked-blob build's output for the same input.

#### Both directions are stateful

Encoding the same frame twice does not give the same codeword. Frame 0 of a
1 kHz tone encodes to `f8f011044ca880` as the first operation after Open and to
`ff920202020800` immediately after — and `ff920202020800` is exactly what a
continuous tone settles to from frame 2 onwards. The encoder has adapted.

Three consequences for the endpoint. A codeword cannot be cached and replayed.
Frames must be fed in order. And two streams cannot share a vocoder even
sequentially without the first corrupting the second's model — which, with one
vocoder per process, is another reason a bus transcodes one talker at a time.
RFC-0003 §5's arbitration is doing real work here rather than merely being
reused.

#### The first frame is near-silent

Decoding a 50-frame sequence, frame 0 comes back at rms 38 against an input of
8485, frame 1 at 7444, and everything after at ~9167 for a steady-state ratio of
1.08. That is one frame of model warm-up, not a fault, and it means the first
20 ms of every transmission is lost unless the decoder is primed. The endpoint
has to account for it; a bridge that does not will clip the first syllable of
every over.

### D3 — Opus: hraban/opus, behind a build tag

pion/opus is decode-only and SILK-only (its own docs: "It currently only supports
the SILK codec, not the CELT codec"), so it cannot do the RF-to-Zello direction.
hraban/opus is a cgo wrapper over libopus with both directions, and is the only
viable choice.

The cost is real: libopus-dev at build, libopus.so.0 at runtime, no pure-Go
cross-compile for the binary that links it. Gate it behind a build tag so a build
without Zello stays pure Go, and add libopus to the CustomPiOS image and the ARM
toolchain. If libopus cannot be cleanly added to the image, that is the trigger to
reconsider D1's rejected alternative and move the transport out of process.

#### The cross build, proved

The whole feature cross-compiles and links for armhf from the desktop, which was
D3's open risk. The recipe is worth writing down because three parts of it are
not guessable:

	CC=arm-linux-gnueabihf-gcc CGO_ENABLED=1 GOOS=linux GOARCH=arm GOARM=6 \
	CGO_CFLAGS="-I<opus>/include" \
	CGO_LDFLAGS="-L<vocoder> -L<opus>/lib -Xlinker --just-symbols=<syms>/symbols_d02.032" \
	go build -tags "zello nolibopusfile" -ldflags '-extldflags "-lm"' ./cmd/waypoint-bus

- `nolibopusfile` is hraban/opus's own tag. libopusfile is not used and its
  header is absent on plain build hosts, so without this the build fails on a
  dependency the feature does not have.
- `--just-symbols` supplies the firmware entry points as addresses. The archive
  it accompanies must have had `firmware.o` and `ram.o` removed, or the binary
  carries the licensed blob.
- `-lm` has to arrive through `-extldflags`, not `CGO_LDFLAGS`. cgo appends the
  package's own `-lopus` *after* everything in CGO_LDFLAGS, and static libopus
  needs libm resolved after it — put `-lm` in CGO_LDFLAGS and the link fails on
  `floorf`, `lrintf` and `sqrt` with no indication that ordering is the problem.

The result was checked rather than assumed: the linked `waypoint-bus` is 15.6 MB,
starts on the bench Pi 3, and a byte-probe from the middle of the firmware image
does not appear anywhere in it.

### D4 — One WebSocket per Zello channel

Forced by Zello, not chosen. API.md states verbatim: "Connecting to multiple
channels (up to 100) is currently supported for Zello Work only." On consumer
Zello, logon carries one channel, so a channel manager owns one connection per
channel — each with its own token and refresh, its own ping/pong handler (the API
pings every 30 s and drops a client that takes longer than 30 s to pong), and its
own reconnect backoff.

Prompt 1 verifies this against the current API before anything is built on it. If
consumer multi-channel logon has started working, this decision collapses and the
channel manager gets much smaller.

### D5 — Routing: reuse RFC-0003 §5, do not reinvent it

The original decision proposed N:M bus-to-channel rows, a seen-cache and
hang-time for arbitration, and gateway-origin tagging for loop prevention. All
three of those already exist.

Arbitration is RFC-0003 §5 rule 2, implemented in
[internal/bus/router/router.go](../../internal/bus/router/router.go): the token
holder wins, a holder silent past the hang time releases, and every losing stream
raises `EventBusBusy` carrying the winner, which the UI already renders as
"busy: via DMR". A Zello talker arriving while DMR holds the token is that case
exactly, and it should produce that event with no new mechanism.

Loop prevention is RFC-0003 §5's four rules in the router, plus RFC-0016 §5's
cross-peer rules and hop ceiling in [peer/loop.go](../../internal/bus/peer/loop.go).
Never emitting to the source is already rule 1. A Zello endpoint that is an
ordinary attachment inherits this; origin tagging is only needed if a single
attachment fans to several channels and one channel's traffic must not return to
it, which is a within-endpoint concern, not a bus one.

On cardinality: RFC-0003 §5 has a mode attaching to at most one bus. One bus
fanning out to several Zello channels is just several attachments and needs no new
concept. A Zello channel attached to more than one bus would break that symmetry
and is deliberately out of scope for the first version — if it is wanted later it
is a bus-semantics change, not a config change.

### D6 — Identity: one DMR ID out, the Zello name back

*Revised.* The original decision added `zello_username` to the phonebook and gave
each Zello talker their own DMR ID through the resolver chain. Neither half
survives contact with this codebase, for two separate reasons.

**One DMR ID, not a mapping.** A Zello user with no DMR registration has no ID to
borrow, and one who has an ID has not authorised this node to transmit as them.
Either way, per-user IDs mean originating traffic under an identity the node does
not hold — on a network like BrandMeister that is transmitting as somebody else.
So every inbound Zello transmission is sourced from the node's own ID, rendered
from `General.ID`, and who is actually talking travels as the Talker Alias, which
is the field that exists for exactly this.

**The resolver cannot reach the daemon, by design.**
[phonebook_isolation_test.go](../../internal/config/phonebook_isolation_test.go)
makes it a rule that no phonebook row is ever rendered into a config: a name in a
generated file is a behaviour change nobody asked for, and an address would be a
PII disclosure into a world-readable file. `waypoint-bus` reads only rendered
config. So a phonebook lookup is not available to it and a `zello_username` column
would have nothing to feed — the schema change was written and then reverted
rather than shipping a release-visible migration for a column nothing could use.

What the alias carries instead is the `from` on Zello's own `on_stream_start`,
travelling on the frame's `SrcCallsign`. That is available, it is true, and it is
what the far end actually calls itself.

The name is passed through untouched rather than through `Template.Render`, which
uppercases. That is right for a callsign, which has a canonical form, and wrong
for a Zello display name whose case is part of it: "Booting6228" would reach the
radio as "BOOTING6228". The template's three shapes combine a callsign with a full
name and there is no such pair here, so it decides only whether an alias is
emitted at all.

The outbound direction has no equivalent and cannot. One connection is one logon
is one Zello identity, so every RF operator reaches the channel as the gateway
account. That is Zello's model; document it rather than appear to work around it.

**Unverified:** the DMRA frames are emitted onto the DMR attachment's loopback,
which on this bus is a local DMRGateway rather than MMDVM-Host directly. Whether
DMRGateway forwards them intact has not been tested, and belongs to Prompt 8.

### D7 — UI: an endpoint table and a lane view

The stated problem is that buses are hard to understand. A node-graph patch panel
would solve it and is out of scope for vanilla JS on the store-model-renderer-
apply-view-panel spine.

Two layers instead. An endpoint table per bus, one row per attached endpoint, with
a type dropdown (DMR talkgroup, Zello channel, later the RFC-0018 mesh
transports), an identifier field and an enable toggle. And a read-only bus board
rendering each bus as a horizontal lane with its endpoint chips and live status
dots — idle, connected, or talking and who — fed by the `bus_voice_start`,
`bus_voice_end` and `bus_busy` events the router already publishes. Clicking a
chip edits its row. Preset templates seed the common case.

The mental model this is trying to install is "a bus is a lane, things attach to
it," which is what RFC-0003 actually describes.

### D8 — No RFC

Recorded here and in the code comments that implement it, per CLAUDE.md. The one
change that would warrant an RFC-0003 addendum is named at the end of this file.

## Audio pipeline

	bus codeword ---> md380-emu ---> 8 kHz PCM ---> resample ---> Opus ---> WebSocket
	  7 bytes         decode        160 samples     8->16 kHz    16 kHz     binary
	  49 bits         (UDP 2470)    320 bytes                    mono       frames

- Bus: 7-byte (49-bit) AMBE+2, 20 ms per frame, three per 60 ms superframe, 8 kHz.
- Vocoder seam: 7-byte canonical codeword to md380's 8-byte `.amb` frame. The
  72-bit on-air form is the frames package's business, not this feature's.
- Resample: 8 kHz to 16 kHz and back. Keep it cheap; a Pi 3 is doing this live.
- Opus: 16 kHz mono. 60 ms matches Zello's own default; 20 ms costs bandwidth and
  buys latency. Expose it as a config key rather than picking for the operator.
- `codec_header`: base64 of `{sample_rate_hz uint16 LE, frames_per_packet uint8,
  frame_size_ms uint8}`. Zello's documented example `gD4BPA==` decodes to
  `{0x80,0x3e,0x01,0x3c}` — 16000 Hz, one frame, 60 ms — which is the check that
  the builder is right.

## Prompts

These are written to be handed to a coding agent one at a time. Prompt 1 gates
everything; Prompt 8 is last.

### Conventions injected into every prompt

New git worktree, one branch and one PR per prompt. DCO sign-off on every commit
(`git commit -s`). Imperative summary line, prose body explaining why — read
`git log` first, the house style is not bullet points. No Co-Authored-By and no AI
attribution anywhere in the commit or the PR.

New config keys render blank-omit. Verify against upstream primary sources
(g4klx/MMDVMHost, g4klx/DMRGateway, zelloptt/zello-channel-api,
travisgoodspeed/md380tools and the DVSwitch fork) before writing code. **Stop and
report** if ground truth contradicts a premise here, rather than improvising
around it.

The node store is `/var/lib/waypoint/config.db` per `waypointd.service` in the
image module; `/home/pi-star/waypoint/config.db` exists only on nodes flashed
before the path split. Check `/var/lib/waypoint` first and fall back to the legacy
path only if absent. Always use `sqlite3 .backup`, never bare `cp` — the store is
in WAL mode.

The vocoder firmware blob is fetched at enable and never committed.

### Prompt 1 — Ground truth, no product code

**Done.** Findings in [docs/zello/ground-truth.md](../zello/ground-truth.md); D2
and Prompt 2 below are revised from them. The original brief follows.

Verify, and write the findings to `docs/zello/ground-truth.md`.

Read `API.md` and `AUTH.md` in zelloptt/zello-channel-api and confirm: consumer
logon requires a JWT `auth_token`; one channel per connection on consumer Zello;
the `codec_header` layout; the `packet_duration` range; the 30-second ping/pong
contract; and the exact error strings for `not authorized`, `no phone` and
`channels limit exceeded`.

Build the **DVSwitch fork** of md380-emu and confirm `-e` encode and the `-S` UDP
server on 2470 exist in that fork's actual source. The upstream travisgoodspeed
wiki still hedges on encode ("encoding will come soon") while the `-e` path exists
in `emulator/ambe.c` and DVSwitch ships encode in production; settle it against
the fork you build, not the wiki. Capture the request and response bytes with a
loopback test — send 320 bytes of PCM, log what comes back, confirm the `0x61`
start byte and the 49-bit `.amb` payload. Evidence suggests UDP, not TCP; confirm
which.

**Stop and report** if consumer multi-channel logon works, if `-S` or `-e` is
missing from the fork, or if the frame sizes differ from the above. Do not
proceed to code.

### Prompt 2 — md380-emu UDP client

The 49-to-72-bit FEC transform is already implemented and golden-tested:
`internal/bus/frames/dmr.go` provides `dmrAMBEFromCanonical`,
`dmrExtractAMBE2`/`dmrInsertAMBE2` and `AMBEBytes`/`dmrAMBEBytes`, pinned byte for
byte in `ambe_vocoder_test.go` against fixtures produced by a real md380 vocoder
on a Pi. **Do not reimplement any FEC or canonical-to-on-air logic.**

*Revised after Prompt 1.* The `.amb` mapping this originally scheduled is not
needed: the `-S` server's 7-byte output is already the canonical form, bit for
bit. The 8-byte `.amb` frame is the file format, and it puts bit 48 in a
different byte as the LSB — do not go near it.

Scope: (a) a thin Go client for the `-S` UDP server exposing
`Encode(pcm []int16) []byte` and `Decode(ambe7 []byte) []int16`; (b) service
lifecycle — health check, restart, and a guard for the md380tools issue #925
SIGSEGV.

The protocol is headerless and dispatches on datagram length alone: 320 bytes in
returns 7, 7 bytes in returns 320, any other length is silently discarded.
Nothing correlates a reply with its request, so the client keeps **exactly one
request outstanding at a time** and treats a missing reply as a timeout. A
pipelined client would mis-pair frames under loss and produce audio that is wrong
rather than absent.

Decisive test: round-trip a canonical codeword from the existing golden fixtures
through the live md380-emu server and back; encode-then-decode of known PCM must
clear an intelligibility threshold.

**Stop and report** if the UDP server's frame bytes disagree with the
`ambe_vocoder_test.go` fixtures' bit ordering, or if per-frame throughput on the
bench Pi 3 exceeds real time. Do not "fix" the golden fixtures to match the
server.

### Prompt 3 — Zello WebSocket client

An `internal/bus/zello` package: dial `wss://zello.io/ws`, load a JWT and handle
the 30-day refresh, the logon state machine, ping/pong, the
`start_stream` / binary / `stop_stream` send path, the `on_stream_start` / binary
/ `on_stream_stop` receive path, the `codec_header` builder, reconnect with
backoff, one connection per channel.

Unit tests run against the protocol transcripts Prompt 1 captured. Opus via
hraban/opus behind a build tag; add libopus to the build docs.

Match the error code `listen only connection` — unhyphenated; the hyphenated form
is the description. Do not expect `no phone`: Prompt 1 found it is not a
documented error code.

**Stop and report** if a live logon returns `not authorized` in a way that
contradicts the documented auth model.

### Prompt 4 — Bus endpoint, routing and config spine

Add Zello as an endpoint kind in `cmd/waypoint-bus` alongside the UDP loopbacks in
`endpoints.go`, with config keys rendering blank-omit through
store-model-renderer-apply. Read RFC-0003 §5 and `internal/bus/router/router.go`
first.

**Arbitration and loop prevention are not yours to build.** Single-talker
arbitration is §5 rule 2 in the router, and a losing stream must raise the
existing `EventBusBusy` so the UI shows "busy: via <winner>" for a Zello talker
exactly as it does for a mode. Loop prevention is §5's four rules. Reuse both; the
transcode is the only new thing, and it lives at the endpoint edge so the router's
"no vocoder, no DSP" guarantee holds.

One bus may fan out to several Zello channels. A Zello channel attached to more
than one bus is out of scope — §5 has a mode attaching to at most one bus and this
version does not break that symmetry.

Tests for fan-out and for contention against a mode attachment.

**Stop and report** if the endpoint cannot raise `EventBusBusy` on the losing
stream without changing the router.

### Prompt 5 — zello_username in the resolver chain

Talker Alias emission already exists: `internal/talkeralias/` provides
`Emitter.Observe` driven by a resolver, and live branches
`feat/phonebook-resolver`, `feat/phonebook-talker-alias` and
`feat/phonebook-callsign-search` carry in-flight resolver work. **Do not build TA
encoding or a new resolver.**

Scope: (a) add `zello_username` to the phonebook schema and the resolver chain
(phonebook, DMRIds.dat, raw ID), following the interface shape on
`feat/phonebook-resolver`; (b) Zello to RF — map the incoming `from` username to a
resolved DMR ID, or a configured gateway ID, and feed the existing
`Emitter.Observe` path; (c) RF to Zello — resolve the RF talker to an
alias/callsign for outbound stream metadata.

Branch from whichever phonebook branch is designated as the base at prompt time.
**Confirm the base branch with the maintainer before starting**; do not branch
from main and re-derive resolver interfaces.

**Stop and report** if the resolver interface differs across the three live
branches in a way that forces a design choice, or if `Emitter.Observe`'s contract
cannot accept a synthesized non-RF-origin identity without modification.

Sequencing is soft-gated on those branches. If they have not merged when this
prompt comes up, defer it rather than fork the resolver interface a fourth time.

### Prompt 6 — Bus UI

Vanilla JS on the existing spine. The endpoint-table editor — rows are attached
endpoints, with a type dropdown, an identifier field and an enable toggle — plus
the read-only bus board with live status dots fed by `bus_voice_start`,
`bus_voice_end` and `bus_busy`, plus preset templates. No framework. Lossless
blank-omit config. Tests for the render-apply round trip and for blank-omit.

Accessibility is a merge gate: status is never colour alone, so a lane's status
dot carries text or an accessible name, and the axe job must pass with the panel's
data seeded rather than scanning its empty state.

### Prompt 7 — Docs

How to obtain the Issuer and Private Key from developers.zello.com and why those
are what to enter rather than the Sample Development Token — a node holding the
key material mints its own token per connection and nothing expires, while a
pasted sample token stops working after 30 days and takes the bridge down
silently. That **a dedicated Zello account is required**, because Zello allows one
session per account and a shared one fights with the operator's phone. That even a
listen-only bridge needs that account, because anonymous logon is refused. The ToS
caveats; the libopus and CustomPiOS build notes; the optional DV3000 path. New user-facing strings go in
`ui/static/locales/en-US.json`, and `fr-FR`/`ja-JP` claim `_meta.reviewed`, so
they are translated in the same branch or the `locales` CI job fails.

No RFC drafted.

### Prompt 8 — Hardware validation, last

On the bench Pi 3 with an MMDVM_HS_Dual_Hat and a DMR-6X2 Pro on BrandMeister:
end-to-end keyed-RF in both directions. **No keyed-RF claim without on-air
evidence** — captured logs and audio. Measure md380-emu against real time on the
Pi 3, and check for the issue-#925 SIGSEGV under that box's kernel.

**Stop and report** if real time or on-air fails.

## What is not settled

Zello's API is beta and documented as subject to change, and three of its
documented behaviours have already been measured to be wrong — see
[ground-truth.md](../zello/ground-truth.md). Treat API.md as a starting point and
check anything load-bearing against the service.

The 30-day token refresh is no longer among the risks: a node with the Issuer and
Private Key mints its own, and that was proved against the live service rather
than inferred. What remains is that Waypoint must never ship an account or key
material, that a bridge needs a Zello account of its own because Zello permits one
session per account, and that free-tier bridging depends on Zello's continued
goodwill. Community bridges (asl-zello-bridge, zellostream) establish the pattern.
If free-tier gateway accounts get rate-limited or banned, the fallback is to
document Zello Work, which does support multi-channel logon.

The identity asymmetry is a limit, not a gap. One connection is one logon is one
Zello identity, so every RF operator's audio reaches the channel as the gateway
account whoever keyed up — individual callsigns cannot appear as distinct Zello
senders, and N connections for N operators would mean N sets of their personal
credentials and N gateway members in the channel, for a bus that can only carry
one talker at a time anyway. Inbound is genuinely per-user, because
`on_stream_start` carries `from`. Document the asymmetry rather than appearing to
support something the platform does not.

There is no published per-frame benchmark for md380-emu on a Pi 3. Real-time
capability is inferred from DVSwitch's "Pi2, Pi3, H3, H5 are good matches" and
from live deployments. Prompt 8 measures it, and if a 20 ms frame does not encode
in under 20 ms of wall clock under load, the DV3000 becomes the default path and
the dongle becomes a documented requirement.

AMBE+2 is patented and using it requires a licence from DVSI. The firmware blob is
fetched at enable and never redistributed. The DV3000 path is the only fully
licensed option, because the licence rides on the chip.

An **RFC-0003 addendum** is warranted the first time a bus carries something that
is not an AMBE codeword between two mode gateways — which is what a Zello
attachment is. §2 guarantees the frame layer is byte-exact and the router touches
no codec; that guarantee survives here only because the transcode is at the
endpoint edge, and the addendum is where that boundary should be written down.
Not now. When it ships.
