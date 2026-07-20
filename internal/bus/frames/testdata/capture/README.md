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
