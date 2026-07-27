# Tier 2 — loopback integration tests

These drive the **real, pinned** gateway daemons against configs produced by
this project's renderers, with **no RF and no upstream credential**.

The ordinary unit tests (Tier 1) prove the renderers emit the bytes they
intended. They cannot tell you whether a daemon accepts those bytes, or where
traffic actually lands once it does. That is what these cover.

## How it works

The mode loopbacks are the seam. MMDVM-Host binds one port of each pair and the
gateway binds the other:

| Mode | MMDVM-Host binds | Gateway binds |
|---|---|---|
| DMR | `62032` | `62031` (DMRGateway) |
| YSF | `3200` | `4200` (YSFGateway / DGIdGateway) |

`repeater.go` stands in for MMDVM-Host: it binds `62032`, announces itself with
the `DMRC` config packet DMRGateway waits for, and injects `DMRD` frames exactly
where a keyed transmission enters the system. Nothing below that line — modem,
RF decode — is involved, and nothing above it is faked. Its YSF counterpart binds
`3200` and answers DGIdGateway's `YSFP` polls, because `CYSFNetwork::write` drops
everything until the link reaches LINKED and only a returned poll gets it there —
without that the echo path is silently dead.

`homebrew.go` is a stub upstream master implementing enough of the homebrew
protocol (`RPTL`/`RPTK`/`RPTC`/`RPTPING`) for a real DMRGateway to log in. It
never verifies the password hash, so **no real credential is involved** — which
master a frame reaches is decided purely by the generated rewrite rules, and
that is the property under test.

The injected audio is the real bench capture
(`internal/bus/frames/testdata/capture/dmr_parrot_9990.bin`), retargeted onto
different talkgroups. Addressing rides the `DMRD` header, never the codec, so
the AMBE payload is untouched.

## Running

Build the pinned daemons (same SHAs as `waypoint-stack/pins.env`):

```sh
docker run --rm -v "$PWD/out:/out" -v "$PWD/build.sh:/build.sh:ro" \
  debian:trixie-slim bash /build.sh
```

DMRGateway hard-fails without an MQTT broker (`m_mqtt->open()` is fatal), so
start one:

```sh
docker run -d --rm --name tier2-mosq -p 127.0.0.1:1883:1883 eclipse-mosquitto:2 \
  sh -c 'printf "listener 1883\nallow_anonymous true\n" > /m.conf && exec mosquitto -c /m.conf'
```

Then:

```sh
LD_LIBRARY_PATH="$PWD/out/lib" go test -tags tier2 -v ./test/tier2/
```

The daemons link `libmosquitto.so.1`; if your host lacks it, extract it from the
same container image into `out/lib` (see `build.sh`). Override the binary
directory with `WAYPOINT_GW_BIN`.

## What each test claims

- `TestTier2_TGIFRouting` — a keyed group call on TG 31665 lands on TGIF's
  socket and not the primary's. TGIF has no echo service, so this asserts on
  outbound traffic rather than a reply.
- `TestTier2_ParrotEcho` — a keyed group call on TG 9990 reaches the primary,
  the generated `TypeRewrite` converts it to the private call BM Parrot expects,
  and the echo returns down the loopback.
- `TestTier2_DGIdGatewayAcceptsRenderedConfig` — the pinned DGIdGateway parses
  our generated INI, stays up, binds `:4200`, and materializes the generated
  DG-ID table.
- `TestTier2_DGIdParrotEcho` — voice keyed on DG-ID 1 reaches the local Parrot
  the generated config declares and comes back: every returning frame carries
  DG-ID 1 (DGIdGateway stamps the slot on the way back) and the AMBE returns
  byte-exactly. Self-contained — no reflector, no internet, no credential. The
  injected transmission is the real bench Parrot audio, reframed DMR→YSF by
  `internal/bus/frames` and addressed with `Params.DGId`.
- `TestTier2_BackToBackCrossNetwork` / `...SameNetwork` — diagnostics, not
  acceptance. See below.

## Gotcha: the Parrot fixture is a private call

`dmr_parrot_9990.bin` has flags `0xe1` — bit 6 set, i.e. a **private** call,
because BM Parrot is normally dialled that way. A `TGRewrite` correctly does not
match it (`PassAllPC` catches it instead). Any test of talkgroup routing must
key a group call explicitly; `retarget(..., group: true)` does that.

## Open: cross-network back-to-back drop

`TestTier2_BackToBackCrossNetwork` reproduces a drop worth investigating. Within
one DMRGateway session, a transmission routed to TGIF followed by one routed to
the primary — same slot, distinct stream ids — never reaches the primary at all,
at gaps of 700 ms, 3 s and 8 s. Two back-to-back transmissions to the *same*
network both arrive, so the effect is specific to switching networks rather than
to sending a second transmission.

This is why the acceptance tests above run one gateway process per claim.

Caveat: this injector is not MMDVM-Host and does not reproduce its RF hang
timers or whatever else it emits between transmissions. Confirming with a keyed
radio, or with an injector that mirrors MMDVM-Host more closely, is the next
step before calling it an upstream bug.
