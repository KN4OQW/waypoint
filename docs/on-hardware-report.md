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
| **Display** | ✅ (`Display=HD44780` + `[HD44780]` block) | n/a | n/a — inert parity INI (no `[Display]` parser in the fork). Physical panel now driven by the **native** LCD driver: see the LCD section below | ✅ | ✅ | n/a |
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

## LCD (native HD44780 driver) — on-glass validation (2026-07-14)

**Build:** waypointd `9768d40-lcd2` — the `feature/lcd-templates` user-defined
template system **plus** the apply-reloads-LCD fix (F6 below). Cross-compiled
`GOOS=linux GOARCH=arm64 CGO_ENABLED=0`, sha256 `b82d1e44…185f61fc`, verified
byte-identical after `scp` before install; prior binary saved as
`waypointd.bak.prelcd`. `restore.sh` + `waypoint-backup-2026-07-13.tgz` from the
previous run confirmed present and parse-checked before starting.

**Panel:** a physical **20×2 HD44780** on a **PCF8574 I2C backpack**, now attached
to the bench Pi — the hardware absent from the 2026-07-13 run. This closes manual
follow-up #3 and the Display SKIP above.

**Recon:** `i2cdetect -y 1` → backpack ACKs at **0x27** (sole device on the bus).
`i2c-dev` already enabled (`dtparam=i2c_arm=on` in `config.txt`; `/dev/i2c-1`
present; `i2c_dev`+`i2c_bcm2835` loaded) — **no reboot needed**.

**Configured entirely via the API** — `PUT /api/config/lcd`
(`enabled=true`, `i2c_address=0x27`, `rows=2 cols=20`, the default page set) then
`POST /api/config/apply`. The LCD section drives no INI, so nothing was
hand-edited. The renderer bound the **real** device — the journal shows
`lcd: renderer started on /dev/i2c-1@0x27 (2x20, 3 pages)` with **no**
`unavailable, disabled` line, i.e. `hd44780.Open`'s probe ACKed at 0x27 and the
driver did **not** fall back to the headless noop.

**Init timing that worked (clean, no garbage):** the datasheet 4-bit handshake in
`internal/lcd/hd44780/hd44780.go` — 50 ms power-on settle; three `0x30` nibbles at
5 ms / 1 ms / 1 ms; `0x20` (enter 4-bit) at 1 ms; function-set/display-off/clear
(2 ms)/entry-mode/display-on. On glass: display cleared, backlight on, both rows
clean — no block/garbage row (the classic 4-bit-init failure mode was **not**
present), so these values are confirmed good on this panel.

| Check | Method | Result |
|---|---|---|
| **a. init** | glass: clear + backlight + no garbage | ✅ **PASS** (operator-confirmed) |
| **b. idle rotation** | glass: pages cycle at their durations | ✅ **PASS** — Idle ↔ Network alternate ~6 s |
| **c. live tokens** | glass: `{ip}`/`{hostname}`/`{time}` | ✅ **PASS** — `172.16.50.13` / `wpsd` / clock matches |
| **d. activity interrupt** | demo feed → voice traffic on the real panel | ✅ **PASS (demo-sourced)** — the `interrupt=true` Activity page takes over on each call (`DMR <call>` / `TG <tg>`) and releases to rotation after linger. **RF-from-a-real-radio: MANUAL** (a radio can't be keyed from here) |
| **e. reconfigure round-trip** | API edit a line + reorder pages + apply | ✅ **PASS** — renderer hot-reloads (F6); daemon **PID unchanged** (no restart); `GET` reflects the new order + edit |
| **f. failure honesty** | point at wrong `0x3f` + apply | ✅ **PASS** — `lcd: I2C /dev/i2c-1@0x3f unavailable, disabled: probe: input/output error`, falls back to noop, **daemon stays up**, apply **8.1 s (no stall)**; restoring `0x27` rebinds the real device |
| **Character set** | glass: `°`, `—`, descenders, `\`, `~` | ✅ **PASS** — see below |

### Character-set handling (HD44780 ROM gotcha)

The driver's `sanitizeASCII` (`internal/lcd/tokens.go`) is the defined fallback:
any rune outside printable ASCII (`0x20`–`0x7E`) is replaced with `?` **before**
it reaches the panel — the HD44780 CGROM is not UTF-8. Verified on glass with the
template `T=45°C hi—there`: both `°` (U+00B0) and `—` (U+2014) rendered as `?`
(`T=45?C hi?there`). Lowercase descenders `g j p q y` render cleanly (they are
plain ASCII, present in every HD44780 ROM).

**ROM-A00 caveat (documented, not a driver bug):** this panel is **ROM A00
(Japanese)**, where two *ASCII* code points differ from glyph: `\` (0x5C) renders
as **¥** and `~` (0x7E) as **→**. The driver passes these through (they are inside
printable ASCII), so the substitution is a property of the panel's ROM, not the
software — an A02 (European) panel shows `\`/`~` normally. Recommendation: avoid
`\` and `~` in templates for portable output. `{` and `}` are reserved for tokens,
so they rarely appear literally. No mapping change was made: normalizing `\`/`~`
would be wrong on A02 panels, and the ROM can't be probed.

### Finding F6 — LCD renderer didn't reconfigure on apply (FIXED in this branch)

The renderer started **once at daemon boot** and captured its config; `configApply`
renders INIs and restarts gateway units but the LCD section drives **no INI and no
unit**, so it was never in the restart set. Consequence: enabling the driver, or
editing pages/geometry, through the UI + Apply reached the store but **never the
glass** — the panel only updated on a full `waypointd` restart. For a feature whose
entire UX is "edit pages, Apply, watch the panel," that made it effectively
unusable without an SSH restart (same shape as the DAPNET F1 lesson: a store change
that silently doesn't take).

**Fix** (`cmd/waypointd/main.go`): `reloadLCD(m)` on apply — it diffs the applied
`LCD` config against the running renderer's and, only when it changed, cancels the
renderer (which releases the I2C device), waits for it to stop, and starts a fresh
one from the new config. An unrelated apply (e.g. a DMR change) leaves the panel
untouched, so it never blinks needlessly. Guarded by a mutex (apply runs on an HTTP
goroutine, the renderer on its own). Tests: `TestReloadLCD` (start-on-enable,
no-op-when-unchanged, restart-on-edit, stop-on-disable), race-clean. **Verified on
hardware:** edit + reorder + apply updated the glass with the daemon **PID
unchanged** (no restart), and enabling the driver via API + apply lit the panel
with no restart.

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
- **LCD run (2026-07-14):** the panel is physical hardware, so the LCD driver is
  left **enabled** on 0x27 (20×2) with the default Idle/Activity/Network page set —
  the validated working state. waypointd is `9768d40-lcd2`, prior binary at
  `waypointd.bak.prelcd`. Mode selection unchanged (DMR only). `restore.sh` still
  rolls the config back (it predates the LCD section, which then backfills to
  disabled on restore — the driver degrades to noop if the panel is later removed).

## Host network — confirm-or-revert guard (2026-07-14)

The host/OS networking domain's **confirm-or-revert** apply was validated on the
bench Pi. This is the safety property the whole domain hinges on: a bad network
apply can strand the node, so an apply checkpoints, activates the change, and
**automatically rolls back on a server-side timer** unless the admin confirms in
time — the revert must not depend on the admin's HTTP session surviving (the
apply may sever it).

- **Build/run:** the branch tip cross-compiled `GOOS=linux GOARCH=arm64`, run as
  a *separate* test daemon (`/tmp/waypointd-hwtest`, port 8074, throwaway store —
  the production waypointd and its config.db were untouched) with
  `-nm-keyfile-dir /etc/NetworkManager/system-connections -network-backend
  composite -network-confirm-timeout 40s`. NM 1.52.1. Everything driven through
  the API (`PUT /api/network/config`, `POST /api/network/apply|confirm`).
- **Backend proven:** the `composite` backend = NetworkManager's native D-Bus
  checkpoint (`CheckpointCreate/Rollback/Destroy` via `busctl`, verified working
  as root on NM 1.52.1) restoring **live device state**, composed with the
  keyfile snapshot for on-disk consistency. This is the H1 "preferred once
  validated on the bench NM version" path — now validated.
- **Ownership honored:** the stock `Wired connection 1` profile was never edited
  or deleted; only `waypoint-eth0`/`waypoint-wifi` were written, and all
  `waypoint-*` profiles were removed at the end (node left on stock DHCP,
  reachable).

Baseline: `eth0` at `172.16.50.13/24` gw `172.16.50.1` (my SSH path — so the
test ran **detached** on the Pi to survive the link flipping).

| Test | Scenario | Expected | Result |
|---|---|---|---|
| **A** | Managed static → DHCP, **do NOT confirm**; wait past the 40 s window | server-side timer rolls back to the pre-apply static; node reachable | **PASS** — after the window `eth0=172.16.50.13`, active `waypoint-eth0`, gateway reachable |
| **B** | Managed static → DHCP, **confirm within the window** | change sticks (DHCP); no rollback after the window | **PASS** — `waypoint-eth0.method=auto` stuck, still `auto` + reachable 45 s past the old deadline |
| **C** | Configure Wi-Fi with a sentinel PSK; exercise the full PUT→GET→apply path | PSK never in any API response or log | **PASS** — 0 occurrences across `GET /api/network/config`, `/status`, `/wifi/scan`, **and** the daemon log; view shows `has_psk:true`; the secret lives only in the `0600` keyfile |

Notes:
- Test A specifically exercised **our** 40 s guard timer (NM's own rollback
  backstop is armed at 40 s + 30 s grace, so it had not yet fired when the node
  was already back — the server-side revert stands on its own).
- Test C proves the **secret-handling** path (redaction in the view + never
  logged), which is the security-relevant guarantee. Live Wi-Fi *association*
  was not performed — the test harness has no real AP credential (none was
  invented). The scan endpoint against the live radio returned nearby networks
  correctly (dedup, signal, security, in-use).
- The auto-rollback now emits a journal line (`network apply auto-rolled back:
  no confirm before the deadline…`) so the operator sees a server-side revert.

### VLAN, NTP, hostname (2026-07-14, part 2)

The rest of the host-network surface — VLANs (through the confirm-or-revert guard)
and the DIRECT-apply host settings (NTP, hostname) — was validated on the bench Pi
(NM 1.52.1, composite backend, throwaway daemon on port 8075, production config.db
untouched). The script saved the as-found hostname/NTP state and **restored it at
the end** (host settings apply directly and mutate the real system).

| Test | Scenario | Expected | Result |
|---|---|---|---|
| **V-A** | Create VLAN 50 on eth0 (static 10.50.0.2/24), **do NOT confirm** | server-side timer rolls back — the `eth0.50` interface and `waypoint-vlan50` keyfile removed | **PASS** — `eth0.50` came up at 10.50.0.2 on apply; after the 40 s window both the interface and keyfile were gone |
| **V-B** | Same VLAN 50, **confirm within the window** | it sticks | **PASS** — `eth0.50 = 10.50.0.2` still present 45 s past the old deadline |
| **NTP** | Set `pool.ntp.org` + `time.cloudflare.com`, enable, direct apply | drop-in written, `timedatectl NTP=yes`, clock synchronizes | **PASS** — `/etc/systemd/timesyncd.conf.d/waypoint.conf` rendered `NTP=pool.ntp.org time.cloudflare.com`; `NTP=yes`, `NTPSynchronized=yes`, server `pool.ntp.org` |
| **Hostname** | Change hostname, re-apply the same value | hostname changes; a repeat apply is a no-op | **PASS** — `hostnamectl --static` → `waypoint-bench`; second apply returned `changed=false` (idempotent) |

Notes:
- The VLAN is a *tagged child* of `eth0` (`eth0.50`), not `eth0` itself, so this
  path never risked the SSH uplink — but it still exercised the full guard
  (checkpoint → activate `nmcli connection up waypoint-vlan50` → rollback/confirm).
- mDNS reflection could not be checked — `avahi-resolve` is not installed on this
  image; the static hostname change via `hostnamectl` was confirmed directly.
- Cleanup verified: hostname back to `wpsd`, the waypoint timesyncd drop-in removed,
  `set-ntp` restored, no `waypoint-*` profiles left, `eth0` still `172.16.50.13`.

## Manual follow-ups
1. Over-the-air QSO per mode (requires keying a radio).
2. Host network: a live **Wi-Fi association** on the bench Pi needs the AP's real
   PSK (not seeded); the credential-handling path is proven, association is not.
3. POCSAG/DG-ID/bridge **network logins** need real credentials — none were
   seeded, so none were invented or registered.
4. LCD/HD44780 hardware drive — **DONE (2026-07-14)**: a 20×2 HD44780 on a PCF8574
   backpack (0x27) was attached and the native driver validated on glass — see the
   *LCD (native HD44780 driver)* section above. The only remaining LCD MANUAL is the
   RF-from-a-real-radio activity interrupt (validated via the demo feed instead).
