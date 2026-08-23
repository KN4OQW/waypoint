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

### D2 — Vocoder: md380-emu as an external service

The router is codec-free by contract: it regroups codewords for the destination's
cadence and copies them verbatim, and its package comment says "No vocoder, no
DSP." A Zello attachment is the first thing on the bus that must transcode, so the
vocoder sits at the endpoint edge, never inside the router, and the router's
guarantee is preserved.

md380-emu is the primary path: software, already the source of the fixtures the
frames tests are pinned against, run as a persistent `-S` UDP server on
127.0.0.1:2470 from the DVSwitch fork. It stays an external, restartable process
rather than being linked in — the AMBE+2 firmware blob is licensed and must never
be redistributed (fetch-at-enable, never committed), and md380tools issue #925
has it segfaulting on some recent Raspberry Pi OS kernels. An out-of-process
vocoder makes that a restart instead of a daemon crash.

An optional `vocoder = hardware` path drives a DV3000/ThumbDV for operators who
own one: better audio, less CPU, and the only fully licensed option, since the
DVSI chip carries its own licence.

codec2 does not apply — it is not AMBE-compatible and a codec2 frame will not
decode on a DMR radio. mbelib is decode-only.

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

### D6 — Identity: one column, not a new resolver

Add `zello_username` to the phonebook schema and to the resolver chain
(phonebook, then DMRIds.dat, then the raw ID). Everything else is wiring into
what exists.

Zello to RF: resolve the incoming `from` username to a DMR ID, or to a configured
gateway ID when unknown, and feed the existing `Emitter.Observe` path so RF radios
see who is talking. MMDVM-Host needs `EmbeddedLCOnly=off` for TA to pass.

RF to Zello: consumer Zello attributes every stream to the account that logged on,
so individual RF callsigns cannot appear as distinct Zello senders. The RF talker's
alias travels as stream metadata, which some Zello clients will not surface. This
is a limitation of the platform and should be documented as one rather than worked
around.

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

### Prompt 2 — md380-emu UDP client and .amb mapping

The 49-to-72-bit FEC transform is already implemented and golden-tested:
`internal/bus/frames/dmr.go` provides `dmrAMBEFromCanonical`,
`dmrExtractAMBE2`/`dmrInsertAMBE2` and `AMBEBytes`/`dmrAMBEBytes`, pinned byte for
byte in `ambe_vocoder_test.go` against fixtures produced by a real md380 vocoder
on a Pi. **Do not reimplement any FEC or canonical-to-on-air logic.**

Scope: (a) a thin Go client for the md380-emu `-S` UDP server, per Prompt 1's
captured framing, exposing `Encode(pcm []int16) []byte` and
`Decode(ambe7 []byte) []int16`; (b) the 7-byte canonical to 8-byte `.amb` frame
mapping, reusing the existing frames helpers for bit ordering; (c) service
lifecycle — health check, restart, and a guard for the md380tools issue #925
SIGSEGV.

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

**Stop and report** if a live logon returns `not authorized` or `no phone` in a way
that contradicts the documented auth model.

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

How to obtain a Zello developer token and that the sample one expires every 30
days; creating a dedicated bridge account; the ToS caveats; the libopus and
CustomPiOS build notes; the optional DV3000 path. New user-facing strings go in
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

Zello's API is beta and documented as subject to change. Free-tier bridging runs
on a dedicated account and a token refreshed every 30 days; that is ongoing
operational fragility, not a one-time setup, and Waypoint must never ship an
account or a token. Community bridges (asl-zello-bridge, zellostream) establish
the pattern, but the pattern depends on Zello's continued goodwill. If free-tier
gateway accounts get rate-limited or banned, the fallback is to document Zello
Work, which does support multi-channel logon.

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
