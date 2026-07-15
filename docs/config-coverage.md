# Configuration coverage & remaining work

An assessment of the whole configuration surface Waypoint must own, what the
store models today, and what remains. Everything here follows one architecture
(RFC-0001): a typed store is authoritative, and every config file is a
deterministic **compiled output** of it.

```
store  →  typed model  →  renderers  →  compiled outputs  →  apply (regenerate + restart)
```

There are two renderer *families*, not one:

- **Radio** → `MMDVM-Host.ini` + per-gateway INIs (DMRGateway, YSFGateway, …).
- **Host / OS** → NetworkManager keyfiles, `timesyncd.conf`, `hostnamectl` — the
  bench Pi runs **NetworkManager** (not dhcpcd), **systemd-timesyncd**, and
  NetworkManager-managed `resolv.conf`. Same store→compile→apply flow, different
  output targets.

Status: ✅ done · 🟡 partial · ⬜ pending

---

## 1. Radio core

| Area | Store section | Status | Notes |
|---|---|---|---|
| Station identity | `general` | ✅ | callsign, DMR ID, duplex, location, URL, timeout, mode-hangs |
| Frequencies + modem port | `modem` | ✅ | RX/TX Hz, UART port/speed |
| Modem calibration | `modem` | 🟡 | offsets, invert flags, RX/TX level modeled; **per-mode TX levels, DC offsets, RSSI mapping, DMR delay not yet** → [#20] |
| DMR params + slots | `dmr`, `dmrnet` | ✅ | color code, ID, slots, embedded-LC |
| Mode enables | `modes` | ✅ | all 8 modes toggle |
| DMR networks | `networks` | ✅ | add/remove, address/port/enable, password (write-only), rewrites |

## 2. Per-mode config systems  → [#33], [#21], [#17]

**"Each mode needs its own config system."** A mode is not just an enable flag —
it is an MMDVM-Host section **plus a gateway daemon plus that gateway's own
reflector/routing config plus a dashboard card**. This is the concrete work
behind the gateway-plugin model ([#21]); DMR + YSF are the MVP ([#17]).

| Mode | MMDVM-Host section | Gateway daemon | Gateway config surface | Status |
|---|---|---|---|---|
| DMR | `[DMR]` + `[DMR Network]` | DMRGateway | networks ✅ · static/dynamic TG (Options) ✅ · TG hold, fine-grained per-mode hangs ⬜ | 🟡 |
| YSF (Fusion) | `[System Fusion]` | YSFGateway / DGIdGateway | YSF + FCS rooms ✅ · DG-ID map ✅ · Wires-X passthrough ✅ · daemon built | ✅ |
| D-Star | `[D-Star]` + `[D-Star Network]` | DStarGateway (ircDDB, MQTT era) | module ✅ · ircDDB login ✅ · startup reflector ✅ · DExtra/DPlus/DCS/XLX ✅ · daemon built | ✅ |
| P25 | `[P25]` | P25Gateway | reflector network ✅ · startup TG list ✅ · NAC ✅ · daemon built | ✅ |
| NXDN | `[NXDN]` | NXDNGateway | reflectors ✅ · startup TG list ✅ · RAN ✅ · daemon built | ✅ |
| M17 | `[M17]` *(via fork)* | M17Gateway | CAN ✅ · startup reflector+module ✅ · suffix/voice/hang ✅ · daemon built | ✅ |
| POCSAG | `[POCSAG]` | DAPNETGateway | store+renderer+tab ✅ (DAPNET server/callsign/AuthKey, paging freq) · daemon: pin `DAPNETGateway` in waypoint-stack `build.sh` | ✅ (config) |
| FM | `[FM]` | analog (no gateway) | CTCSS ✅ · timeout ✅ · kerchunk ✅ · audio levels ✅ · access mode ✅ (host-only, no daemon) | ✅ |

Each entry becomes: a store section (`mode.<name>` + `gateway.<name>`), a
renderer for that gateway's INI, a systemd unit, and a settings tab. **Every
mode's config layer is complete** in this repo: store section, renderer
(`render.go`), enable-gated render target, and settings tab
(`ui/static/settings.js` `TABS`). What remains is per-domain, not per-mode:
DMR's fine-grained TG-hold / per-section RF-Net hang overrides (global mode-hang
*is* modeled in `general`), and — in the separate **waypoint-stack** deploy repo
— pinning the `DAPNETGateway` binary in `build.sh` so POCSAG's rendered INI has a
daemon to consume it (the other seven mode daemons are already built there).

### M17 required a host fork (upstream removed it)

M17 could not be built on the *pinned g4klx* MMDVM-Host: upstream **removed M17
(and AX.25) entirely** in commit `1e2e0c74` ("M17 and AX.25 removal cleanups.",
2025-08-27) after deleting the M17 source files in `9720c7a` ("Make space for
dPMR."). Our pinned `43edd65` (2026-05-29) is post-removal — no `M17*.cpp/.h`,
no `[M17]`/`[M17 Network]` sections, no `MODE_M17`. With no host support the
radio never keys M17 and `M17Gateway` has nothing to link to.

Rather than drop the mode, Waypoint carries a **fork of MMDVM-Host** that
restores M17 on top of the pinned SHA: revert both removal commits, then
reconcile against ~9 months of drift (the `MMDVMHost*` → `MMDVM-Host*` rename,
the deleted display subsystem, the MQTT era). M17 is restored **display-free and
JSON-less** (M17 predates MMDVM-Host's JSON reporting; a follow-up can add
`writeJSON` to `CM17Control` for dashboard RF-activity parity with the other
modes). AX.25 rode along in the same removal commits but its source files are
gone from the tree, so it stays disabled (`USE_AX25` undefined). The fork is
pinned in waypoint-stack; the gateway is the still-current g4klx/M17Gateway,
which is **pre-MQTT** (file/console logging, so its own status is not on the
dashboard data plane — RF activity still surfaces via MMDVM-Host).

Bench-validated: the forked MMDVM-Host loads `M17: enabled`, its modem-capability
line reads `Modes: D-Star DMR YSF P25 NXDN M17 POCSAG`, and it opens the `[M17
Network]` loopback (17011). M17Gateway loads 224 reflectors from the space/tab
`M17Hosts.txt`, opens its Rpt port to MMDVM-Host (17010), and links to a live
reflector (`Linked to M17-M17 C`, ACKN received). Both units stable, NRestarts=0.

## 3. Cross-mode gateways  → [#21]

Transcoding bridges, each a unit + config + card: **YSF2DMR, DMR2YSF, YSF2NXDN,
DMR2NXDN, NXDN2DMR** (the WPSD `dmr2ysf`/`ysf2dmr`/`dmr2nxdn` family). Config
layer ✅: each has a store section (`model.go`), an INI renderer (`RenderYSF2DMR`
… `RenderNXDN2DMR`), an **enable-gated** render target (an off bridge contributes
no target — `render.go`), and the Gateways settings tab. Remaining: pin/build the
`MMDVM_CM` (juribeparada) bridge binaries in waypoint-stack `build.sh` — the
config renders but the daemons are not yet compiled there. Status: ✅ (config) ·
🟡 (daemon builds pending in waypoint-stack).

## 4. Host / network configuration  → [#32]  → 🟡 (foundation shipped)

The second renderer family — **now complete**. NetworkManager is the substrate,
so the renderer target is NM connection keyfiles + a `systemd-timesyncd` drop-in +
`hostnamectl`/`timedatectl` — **not** INI files. `internal/netconfig` covers
read-only status, the keyfile renderer, the confirm-or-revert apply engine, and
the full edit surface (Ethernet/IPv4, Wi-Fi, VLAN, NTP, hostname/timezone).

| Area | Surface | Renderer target | Status |
|---|---|---|---|
| **Live status** | interfaces, link, IPv4, DNS, Wi-Fi SSID+signal, NTP sync, timezone | read-only (`nmcli`/`timedatectl` → `GET /api/network/status`) | ✅ |
| Ethernet / Wi-Fi | connection profiles, SSID/PSK, hidden, regulatory country, autoconnect priority | NM keyfile | ✅ (renderer + UI; live Wi-Fi *association* needs the AP credential) |
| IPv4 method | **DHCP vs static** (address, prefix, gateway) | NM keyfile `ipv4.method` | ✅ (model + renderer + UI + validation) |
| **VLAN** | tagged interfaces (parent + VLAN id + own IPv4) | NM `type=vlan` connection (`waypoint-vlan<id>`) | ✅ (renderer + UI + validation; hardware-validated) |
| DNS | servers, search domains, static vs auto, DHCP override | NM `ipv4.dns` / `dns-search` / `ignore-auto-dns` | ✅ |
| **NTP** | time servers, enable | `systemd-timesyncd` drop-in `NTP=` + `set-ntp` | ✅ (direct apply; hardware-validated) |
| Hostname / timezone | node hostname, TZ | `hostnamectl` / `timedatectl` (idempotent exec) | ✅ (direct apply; **closes the Phase-1 stubs**, below) |

Wi-Fi scan for the join picker is served at `GET /api/network/wifi/scan`
(`nmcli device wifi list`, cached ~10 s); the timezone picker at
`GET /api/network/timezones` (`timedatectl list-timezones`). IPv4 validation
refuses an empty static config and a gateway outside the subnet (a
lock-yourself-out mistake); VLAN validation enforces the 802.1Q id range (1–4094)
and unique ids.

**Three apply mechanisms, by safety class.** VLAN and connection changes go
through the confirm-or-revert **guard** (a bad one can cut the uplink). NTP and
hostname/timezone **apply directly** — they cannot strand the node, so they skip
the guard: NTP renders a timesyncd drop-in + `timedatectl set-ntp` + a
change-gated `systemctl restart`, and hostname/timezone are idempotent
`hostnamectl`/`timedatectl` exec calls (the third apply mechanism — no rendered
file). All are idempotent: re-applying the values already in effect is a no-op.

**Phase-1 stub closure.** The original parity plan (§5, dashboard/system) listed
*station-ID/legal helpers* and left **hostname/timezone** as unfinished Phase-1
stubs. Those are now closed here: hostname and timezone are modeled
(`netconfig.Host`), edited in the Network tab, and applied via
`hostnamectl`/`timedatectl`.

Risk respected: a bad network apply can strand the node, so this domain's apply
is a **confirm-or-revert** guard (`internal/netconfig` `Guard`), unlike the radio
apply. `POST /api/network/apply` checkpoints, renders the keyfiles, **activates**
them, and returns a confirm token + deadline; `POST /api/network/confirm` makes it
permanent; **no confirm by the deadline rolls back automatically on a server-side
timer** — the revert never depends on the admin's HTTP session surviving (which
the apply itself may sever). The default `composite` backend is NetworkManager's
native D-Bus checkpoint (`NMCheckpoint`, via `busctl`) restoring **live device
state**, composed with the keyfile snapshot (`KeyfileCheckpoint`) for on-disk
consistency; `-network-backend keyfile` is the fallback where NM checkpoints are
unavailable. **Hardware-validated on the bench Pi (NM 1.52.1, 2026-07-14):**
static→DHCP without confirm auto-reverts and stays reachable; with confirm it
sticks; a Wi-Fi PSK never appears in any API response or log; a VLAN create rolls
back / sticks the same way; NTP syncs against pool servers; hostname changes are
applied and idempotent (see `docs/on-hardware-report.md`).

Ownership rule: Waypoint writes and prunes only `waypoint-*.nmconnection`
profiles (0600 root) — a hand-made NM profile on the same box is never touched.
Render is pure (deterministic per-profile UUID), so an unchanged store re-applies
to no diff. Wi-Fi PSKs use the write-only/preserved-on-blank/redacted secret
pattern (`View`/`Set`), wired now ahead of the Wi-Fi surface.

## 5. Dashboard / system  → 🟡

Log levels and MQTT partly wired; **updates lifecycle, auth/TLS (RFC-0002),
service supervision ([#22]), station-ID/legal helpers ([#24])** pending.

## 6. Auxiliary services  → ⬜

APRS (APRSGateway), GPSD, transparent data, DAPNET beyond POCSAG.

---

## Architecture implications

1. **The store generalizes cleanly.** Every domain above is "a set of store
   sections + a renderer + an apply target + a dashboard tab." The [#21] pattern
   ("a mode is a unit + config schema + dashboard card") is the same shape host
   networking needs.
2. **A second renderer family is required** for host config (NetworkManager /
   timesyncd), parallel to the INI renderers — same purity + atomic-swap
   discipline.
3. **Apply safety is per-domain.** Radio apply restarts daemons (brief RF drop,
   fine). Network apply can cut the admin's own connection → needs
   confirm-or-revert.
4. **Secrets scale.** DMR network passwords are handled (write-only, preserved
   on blank); Wi-Fi PSK, DAPNET keys, and D-Star/ircDDB passwords need the same
   `sensitive`-key treatment (RFC-0001 §Profiles, RFC-0002 at rest).

## Sequenced next steps

The per-mode **config** layer is complete — all eight modes and the five
cross-mode bridges have store sections, renderers, render targets, and settings
tabs. What's left is deploy-side (waypoint-stack) and adjacent domains:

1. Pin/build the two remaining daemons in waypoint-stack `build.sh`:
   **DAPNETGateway** (POCSAG) and the **MMDVM_CM** cross-mode bridge binaries —
   the only mode/bridge daemons not yet compiled there.
2. Host network config — [#32]. **Complete** (`internal/netconfig`): status,
   keyfile renderer, confirm-or-revert apply, and the full edit surface
   (Ethernet/IPv4, Wi-Fi, VLAN, NTP, hostname/timezone), all hardware-validated on
   the bench Pi. Closes the Phase-1 hostname/TZ stubs.
3. DMR fine-grained coverage: TG hold + per-section RF/Net hang overrides (global
   mode-hang already modeled in `general`).
4. Full modem-calibration coverage + calibration wizard ([#20]).

[#1]: https://github.com/KN4OQW/waypoint/issues/1
[#17]: https://github.com/KN4OQW/waypoint/issues/17
[#20]: https://github.com/KN4OQW/waypoint/issues/20
[#21]: https://github.com/KN4OQW/waypoint/issues/21
[#22]: https://github.com/KN4OQW/waypoint/issues/22
[#24]: https://github.com/KN4OQW/waypoint/issues/24
[#32]: https://github.com/KN4OQW/waypoint/issues/32
[#33]: https://github.com/KN4OQW/waypoint/issues/33
