# Real loopback captures

Unlike `testdata/synthetic/` (frames this layer *emits*, pinned as golden), these
are **real frames captured off a live gateway loopback** — the wire format a
production MMDVM-Host actually speaks. `TestRealCaptureDMRParrot` parses them and
reframes the extracted AMBE across all three modes to prove the layer is faithful
to real traffic, not just to its own constructors (RFC-0003 §6, Prompt 3 Task 4).

## `dmr_parrot_9990.bin`

A keyed-up DMR transmission, captured on the bench Pi (WPSD stack) with:

```
sudo tcpdump -i lo -s 0 'udp and (port 62031 or port 62032)' -w dmr_key.pcap
```

while a radio was keyed to **BrandMeister Parrot / Echo (TG 9990)**. The frames
here are the `MMDVM-Host -> DMRGateway` direction only (UDP `127.0.0.1:62032 ->
:62031`) — i.e. the modem's own live RF decode, the exact 55-byte "DMRD" frames
`ParseDMR` is written against. Loopback path per the running config:

```
[DMR Network]  LocalPort=62032  GatewayPort=62031   (MMDVM-Host.ini)
[General]      RptPort=62032    LocalPort=62031     (DMRGateway.ini)
```

Layout: `[0]` voice LC header, `[1..20]` twenty voice frames, `[21]` terminator —
22 × 55 = 1210 bytes. The 20 voice frames yield 60 AMBE codewords (LCM of the
3/4/5 codewords-per-frame of DMR/NXDN/YSF), so the cross-mode reframe test needs
no padding. Trimmed from a longer (~340-frame) transmission to keep the fixture
small; the header, a contiguous voice run, and the real closing terminator are
preserved verbatim.

**It is a PRIVATE call, not a group call.** Byte `[15]` is `0xe1` — bit 6 set —
because BM Parrot is normally dialled as a private call to 9990. This matters to
anyone reusing the fixture for routing tests: a DMRGateway `TGRewrite` is a
*group*-call concept and correctly will not match it (`PassAllPC` catches it
instead). Retarget the fixture onto a talkgroup and you must clear bit 6 as well,
or the transmission routes nowhere near the rule you meant to exercise. See
`test/tier2` for a harness that does this.

### Sanitization

- **Src ID `3180202` / dst `9990` are left as captured.** The source is KN4OQW's
  own public RadioID (already the id hard-coded in `fixtures_test.go`), and 9990
  is the public Parrot service — neither is third-party PII. Addressing rides the
  DMRD header (bytes 5–10), never the codec, so it is independent of the AMBE.
- The AMBE payload is the operator's own brief Parrot test transmission. It is
  the low-rate codec bitstream (the reframe unit), committed intentionally as the
  real-audio ground truth; no third-party traffic is included.

Regenerate/extend by re-running the capture on the bench Pi and slicing the pcap;
keep the header/voice/terminator shape so `TestRealCaptureDMRParrot` still holds.

## `ysf_bench_from_dmr.bin`

Produced BY the `waypoint-bus` daemon on the bench Pi during Phase-1 hardware
validation (`docs/on-hardware-report.md`, 2026-07-20). The daemon was fed the real
DMR Parrot transmission above on the DMR loopback and reframed it to YSF; this is
what emerged on the YSF peer port (`127.0.0.1:4200 -> :3200`), captured with
tcpdump.

Unlike the synthetic YSF golden fixture, these are YSFD bytes a real daemon
emitted, carrying source callsign **KN4OQW** resolved from DMR id 3180202 via the
shared `DMRIds.dat` — proving on-hardware ID->callsign resolution and the
DMR->YSF reframe. It is a prefix of a longer transmission (voice header + 9 voice
frames; tcpdump bounded the capture), so it has no terminator. `TestRealCaptureYSFFromDMRBench`
parses it. The AMBE is the operator's own Parrot test audio (see the sanitization
note above); addressing is KN4OQW's public RadioID.

YSF/NXDN *modem-side* real captures (a keyed-up C4FM/NXDN transmission decoded by
MMDVM-Host) remain unavailable: those modes were disabled on the bench modem, so
their synthetic golden fixtures stand until a modem-side capture is feasible.

## `ysf_peer_from_dmr.bin`

Produced during RFC-0016 **Phase-2** two-node hardware validation
(`docs/on-hardware-report.md`, 2026-07-20). The bench Pi (owner, node "shack")
owned Bus A with a local DMR attachment; a second `waypointd`/`waypoint-bus` on an
x86 host (member, node "garage") contributed YSF over a pinned-mTLS LAN peer link.
A DMR Parrot transmission was replayed into the owner's DMR loopback; the owner
reframed it DMR->YSF and streamed it over the peer link, and these are the YSFD
bytes the **member** emitted at its local YSF loopback (`127.0.0.1:3200`), captured
there. Unlike `ysf_bench_from_dmr.bin` (a single-node reframe), these frames
crossed a real LAN peer link end to end. `TestRealCapturePeerYSFFromDMR` parses
them. Same operator Parrot audio + KN4OQW public RadioID as the other captures
(see the sanitization note above).

## `dmr_peer_from_ysf.bin`

The mirror of the file above, and the reason it exists: `ysf_peer_from_dmr.bin`
only proves **owner -> member**. This one is **member -> owner**, closing issue
#65 acceptance 3's "voice works both ways" on hardware
(`docs/on-hardware-report.md`, 2026-07-29).

Same two-node topology (owner = bench Pi `172.16.50.13` with a local DMR
attachment on Bus A; member = an x86 host contributing YSF over a pinned-mTLS LAN
peer link), driven the other way: the committed `ysf_peer_from_dmr.bin` was
replayed into the **member's** YSF loopback (`127.0.0.1:4200`) at the real 100 ms
YSF cadence; the member requested and was granted the cluster-wide token, streamed
over the peer link, and the owner reframed YSF->DMR. These are the `DMRD` bytes the
**owner** emitted at its own DMR loopback (`127.0.0.1:62032 -> :62031`), recorded
there. `TestRealCapturePeerDMRFromYSF` parses them.

Layout: `[0]` voice header, `[1..20]` twenty voice frames, `[21]` terminator —
22 × 55 = 1210 bytes, one whole transmission, the same shape as
`dmr_parrot_9990.bin` (and the same 60-codeword LCM property, so it reframes
across all three modes without padding).

What only this capture can show:

- **Callsign -> id resolution.** YSF is callsign-addressed, DMR is id-addressed, so
  the owner had to resolve **KN4OQW -> 3180202** through the shared `DMRIds.dat`
  to build these frames at all. The other captures exercise id -> callsign.
- **The codec bits survive the whole round trip.** These 60 codewords are
  byte-identical to `dmr_parrot_9990.bin`'s, which is where the audio started:
  real DMR capture -> reframed to YSF -> replayed at the member -> across the peer
  link -> reframed back to DMR here. The test asserts it.

Two things to know before reusing it:

- **`dst` is `9`, a group call** (the DMR attachment's `default_tg`), unlike
  `dmr_parrot_9990.bin`'s private call to 9990 — so a `TGRewrite` *does* apply to
  this one.
- **The stream id is `0`**, legitimately: `ParseYSF` synthesizes no stream id, so a
  YSF-origin transmission carries 0 through the reframe. Do not "fix" a test that
  sees zero here.

The header is present because this was the **second** transmission of the run,
inside the owner's 3 s voice hang. A *cold* member key-up loses its header: the
member drops the very frame that triggers its token request, because the grant has
not arrived yet (finding F-65-1 in the hardware report). Same operator Parrot audio
and KN4OQW public RadioID as every other capture here (sanitization note above).
