# On-Hardware Verification Report — WPSD-parity build on the bench Pi

**Date:** 2026-07-13 · **Operator build:** waypointd at repo tip `e148bd3`
(POCSAG/FM surface) **plus** the DAPNET crash-loop fix in this branch.

## Target

| | |
|---|---|
| Host | `pi-star@172.16.50.13` (hostname `wpsd`) — a live **WPSD** image |
| Board | Raspberry Pi 3, `aarch64` kernel + **armhf** (32-bit) userland |
| OS | Raspbian GNU/Linux 13 (trixie) |
| Modem | **MMDVM_HS_Dual_Hat v1.6.1** (dual ADF7021, CA6JAU FW) on `/dev/ttyAMA0` |
| Broker | mosquitto 2.0.21 (active+enabled) |

The node was **already running Waypoint** from a prior deploy; the stock WPSD
services (`mmdvmhost`, `dmrgateway`, `pistar-watchdog`, `pistar-ap/remote/upnp`)
were already **masked**. This run updated waypointd to the current tip and ran
the full per-mode matrix on hardware. Everything was driven through the
waypointd API (`PUT /api/config/<section>`, `PUT /api/config/modes`,
`POST /api/config/apply`) — **no INI was hand-edited on the Pi.**

## Build & deploy

- **waypointd**: cross-compiled `GOOS=linux GOARCH=arm64 CGO_ENABLED=0`
  (pure-Go sqlite) from the branch tip. Static ELF runs fine on the aarch64
  kernel over the armhf userland. sha256 verified byte-identical after `scp`
  before install; prior binary saved as `waypointd.bak.predapnetfix`.
- **Stack daemons** were already deployed (armhf, current) for
  MMDVM-Host / DMRGateway / YSFGateway / P25Gateway / NXDNGateway / M17Gateway /
  dstargateway. Missing from the deployed set and **not built by
  waypoint-stack** (no pin/source): **DGIdGateway** (added at stack tip
  `0f9f47d`), **DAPNETGateway**, and the **CM bridges**. For those, the box's
  native **WPSD armhf binaries** (`/usr/local/bin/{DGIdGateway,DAPNETGateway,
  YSF2DMR,DMR2YSF,YSF2NXDN,DMR2NXDN}`) were copied into `waypoint/bin/` and
  pointed at waypointd's rendered INIs — this exercises waypointd's
  render→unit→link control plane even where the Go stack ships no binary.
  **NXDN2DMR has no binary anywhere** (WPSD ships YSF2P25 instead; the stack
  builds neither) → marked ABSENT.

## Render verification method

A small tool built from the repo's own `internal/config` package
(`rendercheck`) opens a copy of the live `config.db` (**+ its `-wal`**, which
is essential — a WAL-mode SQLite copy without the `-wal` is a stale snapshot)
and renders every target. Every rendered INI was diffed against the Pi's live
file: **byte-identical**, confirming the render tool and the running waypointd
agree. "re-enable identical" was proven by capturing the target INI's sha256
after the first enable and again after a disable→re-enable round-trip.

## Systemd `Conflicts=` installed (mutual exclusion the code can't express)

Derived from the fixed loopback ports in `internal/config/render.go` — two units
conflict when they bind the same `127.0.0.1:port`:

| Pair | Shared bind | Verified |
|---|---|---|
| `waypoint-ysfgateway` ↔ `waypoint-dgidgateway` | YSF 3200/4200 loopback | ✅ swap test |
| `waypoint-dmrgateway` ↔ `waypoint-dmr2ysf` | DMR 62031 loopback | ✅ |
| `waypoint-dmrgateway` ↔ `waypoint-dmr2nxdn` | DMR 62031 loopback | ✅ |
| `waypoint-dmr2ysf` ↔ `waypoint-dmr2nxdn` | DMR 62031 loopback | ✅ |

The "fat" YSF-side bridges (`ysf2dmr`, `ysf2nxdn`) and `nxdn2dmr` **depend on**
their source gateway (they connect to YSFGateway :42000 / NXDNGateway :14050),
so they are *not* mutually exclusive with it — no `Conflicts=` there. Their
collisions with MMDVM-Host's 3200 / 14021 loopback are **mode-level** (turn that
mode off in MMDVM-Host), not unit-level, so they are not encoded as unit
`Conflicts=` (that would stop the always-on modem).

## First-boot / seed

New waypointd opened the existing `config.db` and **backfilled defaults** for
the sections the previous binary lacked (`display`, all five bridges, `pocsag`,
`fm`) — existing config preserved, new sections defaulted. `/api/health` → ok.

**Secret leak test:** none of the 3 plaintext secrets rendered into the INIs
(BM password etc.) appear anywhere in `GET /api/config`; the view exposes only
`has_password`/`has_ircddb_password`/`has_auth_key` flags (5×true, 2×false).
**PASS.**

**Modem handshake (confirmed on the hat):**
```
MMDVM protocol version: 1, description: MMDVM_HS_Dual_Hat-v1.6.1 20230526
14.7456MHz dual ADF7021 FW by CA6JAU GitID #899fc2a
```
Port `/dev/ttyAMA0` is correct (no change needed); RSSI map loaded (14 points).

## Per-mode matrix

Legend: ✅ pass · SKIP (with reason) · ABSENT (no binary) · n/a (not applicable).
"link" = the daemon actually reaching its network/reflector (or, for the
render-only modes, that the mode section renders live).

| Mode | render | unit active | network-link | disable-survives | re-enable-identical | secret-on-disk |
|---|---|---|---|---|---|---|
| **DMR** | ✅ | ✅ | ✅ opens BM_3102 master | ✅ (id + all 5 networks intact) | ✅ | ✅ blank kept all 5 pwds |
| **D-Star** | ✅ | ✅ | ✅ loaded 94 D-Plus / 508 Dextra / 458 DCS | ✅ | ✅ | ✅ ircDDB pwd preserved |
| **YSF** | ✅ | ✅ | ✅ loaded 1708 reflectors + FCS open | ✅ | ✅ | n/a |
| **YSF + DG-ID** | ✅ | ✅ swap | ✅ dgid starts, ysfgw stops (Conflicts) | ✅ reverse swap | ✅ | n/a |
| **P25** | ✅ | ✅ | ✅ opens P25/Rpt network | ✅ | ✅ | n/a |
| **NXDN** | ✅ | ✅ | ✅ loaded 346 reflectors + net open | ✅ | ✅ | n/a |
| **M17** | ✅ | ✅ | ✅ opens M17/Rpt network (journal) | ✅ | ✅ | n/a |
| **Display** | ✅ (`Display=HD44780` + `[HD44780]` block) | n/a | **SKIP** — no `i2cdetect`, no LCD attached | ✅ | ✅ | n/a |
| **POCSAG** | ✅ (`[POCSAG] Enable=1` + DAPNETGateway.ini) | ⚠️→**FIXED** | **SKIP** — no seeded DAPNET AuthKey (not invented) | ✅ | ✅ | ✅ AuthKey preserved |
| **FM** | ✅ (`[FM] Enable=1`, CTCSS 127.3) | n/a (no daemon) | n/a | ✅ | n/a | n/a |
| **YSF2DMR** | ✅ | ✅ (binary runs) | ✅ starts; master-login **SKIP** (no seeded creds) | ⚠️ daemon stays up (see F2) | ✅ | ✅ pwd preserved |
| **DMR2YSF** | ✅ | ✅ | ✅ starts | ✅ **stopped** via Conflicts=dmrgateway | ✅ | n/a |
| **YSF2NXDN** | ✅ | ✅ | ✅ starts | ⚠️ daemon stays up (see F2) | ✅ | n/a |
| **DMR2NXDN** | ✅ | ✅ | ✅ starts | ✅ **stopped** via Conflicts=dmrgateway | ✅ | n/a |
| **NXDN2DMR** | ✅ (INI + unit provisioned) | **ABSENT** | ABSENT — no binary in stack or WPSD | n/a | n/a | n/a |

Every "re-enable-identical" is a byte-for-byte sha256 match of the rendered
target across a disable→re-enable cycle (losslessness property 3, on hardware).

## Findings

### F1 — DAPNETGateway crash-loop / 45-second Apply (FIXED in this branch)
`DAPNETGateway` was an **unconditional** render target ("always-on, like the mode
gateways"). But unlike the digital-mode gateways — which idle harmlessly when
their mode is off — it **exits immediately**:
```
E: AuthKey not set or invalid   (exit 1)
```
So on any node that has not configured POCSAG (the common case) the unit
crash-looped every 3 s **and**, because `apply` does a blocking
`systemctl restart` on it, **every Apply took ~44.9 s**.

**Fix** (`internal/config/render.go`): gate the DAPNETGateway target on
`m.Modes.POCSAG`, exactly like a bridge is gated on its `Enable`. POCSAG off ⇒
no target ⇒ apply neither writes `DAPNETGateway.ini` nor restarts the unit.
Test `TestDAPNETTargetGatedOnPOCSAG` added; `TestDAPNETTargetRegistered` keeps
the POCSAG-on case. **Re-verified on hardware:** apply dropped **44.9 s → 6.7 s**,
DAPNET no longer in the restart list, unit stays down. POCSAG-on still renders
DAPNETGateway.ini (unchanged).

### F2 — Disabling a fat/YSF-side bridge does not stop its daemon (behavior note)
For bridges with a `Conflicts=` against an always-on unit (`dmr2ysf`,
`dmr2nxdn` vs `dmrgateway`), disable+apply **does** stop the daemon — apply
always restarts `dmrgateway`, whose `Conflicts=` then stops the bridge. But
`ysf2dmr`/`ysf2nxdn` have no always-on conflict partner, so after disable+apply
the **daemon keeps running and the stale INI is left in place** (the render
layer intentionally leaves stopping to systemd, per the code comment). A pure UI
disable of these two therefore leaves the bridge transcoding. Not fixed here
(out of the render layer's remit); candidate follow-up: have apply `stop` units
for bridges that transitioned enabled→disabled.

### F3 — A disabled bridge is `null` in `GET /api/config` (behavior note)
The bridge model uses "presence is enable," so a disabled bridge serializes as
`null` in the view. Its stored settings are **not lost** — proven by the
byte-identical re-enable — but they are not surfaced while disabled, unlike the
always-on modes whose fields stay visible when the mode is off.

### F4 — CM bridge + DAPNET binaries are not built by waypoint-stack
`pins.env` builds MMDVM-Host, DMRGateway, YSF/P25/NXDN/M17/DStar gateways and
(at tip) DGIdGateway — but **no MMDVM_CM bridges and no DAPNETGateway**. This run
used the box's WPSD armhf binaries to exercise the control plane. NXDN2DMR has no
binary at all. Follow-up: pin+build the CM bridges + DAPNETGateway in the stack
(or document that Waypoint relies on the base image for them).

### F5 — MQTT data plane is idle-silent (expected)
Each daemon publishes to its own topic (`dmr-gateway/log`, `ysf-gateway/log`, …)
in bursts on (re)start and on RF/network activity; an idle modem with no RF
produces no periodic heartbeat, so the dashboard's live pane stays empty until
traffic. The modem RX path is proven live by the handshake above. An actual
over-the-air QSO is a **manual follow-up** (a radio cannot be keyed from here).

## Exit state & restore path

- Node left **running Waypoint**, restored to its original mode selection
  (**DMR only**); all gateway units healthy; `dgidgateway`/`dapnetgateway`
  stopped; all bridges disabled+stopped. Apply latency ~6.8 s.
- **Backup:** `/home/pi-star/waypoint-backup-2026-07-13.tgz`
  (sha256 `eaab01de…d1a9608`) — config.db, rendered `etc/`, waypoint unit files,
  recorded service state, and the stock WPSD `/etc` configs. A verified copy is
  on the workstation at `waypoint-pi-backups/`.
- **Restore:** `/home/pi-star/restore.sh` (parse-checked) rolls waypointd back to
  this pre-session state (config.db + etc + units, re-enable the recorded
  services, reboot). It also carries a commented **FULL WPSD RESTORE** block
  (unmask `mmdvmhost`/`dmrgateway`/`pistar-*`, restore `/etc` configs) for
  reverting the node to stock WPSD by hand.

## Manual follow-ups
1. Over-the-air QSO per mode (requires keying a radio).
2. POCSAG/DG-ID/bridge **network logins** need real credentials — none were
   seeded, so none were invented or registered.
3. LCD/HD44780 hardware drive — verify on a node with a PCF8574 backpack
   (`i2cdetect` was absent here and no display is attached).
