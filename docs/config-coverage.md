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
| DMR | `[DMR]` + `[DMR Network]` | DMRGateway | networks ✅ · static/dynamic TG (Options), TG hold, per-mode hangs ⬜ | 🟡 |
| YSF (Fusion) | `[System Fusion]` | YSFGateway / DGIdGateway | YSF + FCS rooms, DG-ID map, Wires-X passthrough | ⬜ (P0) |
| D-Star | `[D-Star]` | DStarGateway (ircDDB) | repeater/module, ircDDB, DExtra/DPlus/DCS, routing | ⬜ |
| P25 | `[P25]` | P25Gateway | P25 reflector network, TG list, NAC | ⬜ |
| NXDN | `[NXDN]` | NXDNGateway | NXDN reflectors, TG list, RAN | ⬜ |
| M17 | `[M17]` | M17Gateway | M17 reflectors/modules | ⬜ |
| POCSAG | `[POCSAG]` | DAPNETGateway | DAPNET auth (callsign, key), paging freq | ⬜ |
| FM | `[FM]` | analog (+ optional FM network) | CTCSS, timeout, kerchunk, audio levels, access mode | ⬜ |

Each entry becomes: a store section (`mode.<name>` + `gateway.<name>`), a
renderer for that gateway's INI, a systemd unit, and a dashboard tab. The
per-mode *enable* already flips the MMDVM-Host section; what's missing is the
gateway daemon config + reflector selection for every mode except DMR.

## 3. Cross-mode gateways  → [#21]

Transcoding bridges, each a unit + config + card: **YSF2DMR, DMR2YSF, YSF2NXDN,
DMR2NXDN, NXDN2DMR**. WPSD ships these as `dmr2ysf`/`ysf2dmr`/`dmr2nxdn` units.
Status: ⬜.

## 4. Host / network configuration  → [#32]

Not yet modeled at all, and the largest missing domain. NetworkManager is the
substrate, so the renderer target is NM connection keyfiles + `timesyncd.conf` +
`hostnamectl` — **not** INI files.

| Area | Surface | Renderer target |
|---|---|---|
| Ethernet / Wi-Fi | connection profiles, SSID/PSK, regulatory country | NM keyfile |
| IPv4 method | **DHCP vs static** (address, prefix, gateway) | NM keyfile `ipv4.method` |
| **VLAN** | tagged interfaces (parent + VLAN id) | NM `type=vlan` connection |
| DNS | servers, search domains, static vs auto | NM `ipv4.dns` / resolv.conf |
| **NTP** | time servers, enable | `systemd-timesyncd` `NTP=` |
| Hostname / timezone | node hostname, TZ | `hostnamectl` |

Risk to respect: a bad network apply can strand the node. The apply for this
domain needs a **confirm-or-revert** guard (stage → apply → if the admin
session doesn't reconfirm within N seconds, roll back), unlike the radio apply.

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

1. YSF end-to-end (mode section + YSFGateway config + tab) — completes MVP modes ([#17]).
2. Host network config (NetworkManager renderer + confirm-or-revert apply) — [#32].
3. Remaining modes (D-Star, P25, NXDN, M17, POCSAG, FM) via the [#21] plugin pattern.
4. Cross-mode gateways.
5. Full modem-calibration coverage + calibration wizard ([#20]).

[#1]: https://github.com/KN4OQW/waypoint/issues/1
[#17]: https://github.com/KN4OQW/waypoint/issues/17
[#20]: https://github.com/KN4OQW/waypoint/issues/20
[#21]: https://github.com/KN4OQW/waypoint/issues/21
[#22]: https://github.com/KN4OQW/waypoint/issues/22
[#24]: https://github.com/KN4OQW/waypoint/issues/24
[#32]: https://github.com/KN4OQW/waypoint/issues/32
[#33]: https://github.com/KN4OQW/waypoint/issues/33
