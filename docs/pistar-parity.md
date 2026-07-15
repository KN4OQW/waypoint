# Pi-Star / WPSD configuration parity — acceptance spec

This is the acceptance checklist for Waypoint's WPSD-parity work. Every WPSD
configuration field maps here to the MMDVM-Host or gateway INI key it drives, the
Waypoint store `section.key` that owns it, and a status against the code. It is
the checklist Prompt 10 verifies against.

**Source of truth:** *"Pi-Star vs WPSD Configuration Page — Authoritative Field
Map"*, built from the actual dashboard PHP (`configure.php`) of both projects:

- classic Pi-Star — `AndyTaylorTweet/Pi-Star_DV_Dash` `admin/configure.php`
- WPSD — `ytlzq0228/W0CHP-PiStar-Dash` (mirror of `repo.w0chp.net/WPSD-Dev`) `admin/configure.php`

The **WPSD field label** column is transcribed verbatim from those pages (the
`<b>Human Label</b>` a user sees) — no labels are invented. Where Waypoint models
a field WPSD has no label for, the row is marked *Waypoint-only*; where WPSD has a
field Waypoint does not model, `store section.key` is `—` and status is `pending`.

**Reference target:** WPSD, not classic Pi-Star. Waypoint already follows WPSD
conventions (DMRGateway multi-network routing, SystemX + TGIF as first-class
networks, XLX time-slot, DMR Roaming Beacon in place of the dropped JitterBuffer,
M17 as a real mode). Rows note where classic Pi-Star differs.

**Page shape:** WPSD renders one scrolling `configure.php` with mode-gated
panels; Waypoint keeps a persistent tabbed shell (one tab per mode). This spec
targets **field/feature parity inside the Waypoint shell**, not the single-page
layout.

**Status:** `done` — modeled in the store, rendered, and UI-wired, accepted at or
above WPSD parity · `partial` — present but diverging or incomplete/not surfaced ·
`pending` — not yet accepted for parity (branch scaffolding, if any, is noted).

**Baseline (verified 2026-07-13, branch `cross-mode-gateways`):** `general`,
`modem` (core offsets), `dmr`, `dmrnet`, `modes`, `networks`, `display`/Setup,
`dstar`, `ysf`, `p25`, `nxdn`, `m17` are all **done** — each done field verified to
have a store key, a renderer line, a view projection, and a UI control (see the
Prompt 10 parity report at the foot of this file). The five cross-mode bridges are
**superseded by the RFC-0003 bus architecture** — an intentional departure from
WPSD's per-bridge-daemon model; their store sections are retained (dormant) but no
longer rendered or surfaced (see the Cross-mode row). The remaining gaps are: (a) a small set of per-mode MMDVM-Host params that
are modeled + rendered but not surfaced in the view/UI (YSF Self-Only / Low
Deviation / TX Hang / Mode Hang / Remote Gateway; P25/NXDN/M17 TX Hang), and (b)
the modem-calibration, structured-location, POCSAG/DAPNET, FM, and host/OS
surfaces, which stay **pending**/not-modeled as noted per-row below.

---

## Divergences to hold the line on

Three WPSD-vs-classic-Pi-Star divergences Waypoint follows **WPSD** on, plus one
place Waypoint deliberately differs from both. Prompt 10 verifies these.

| # | Divergence | Direction | Where it shows up |
|---|---|---|---|
| D1 | **SystemX + TGIF are first-class DMR networks** (classic Pi-Star has neither as a built-in network; WPSD promoted both) | Follow **WPSD** | DMR panel — `systemx` / `tgif` network types; generated rewrite prefixes 4 / 5 |
| D2 | **YSF DG-ID / YCS** — WPSD adds *Enable DGIdGateway* + *YCS Network* (DGIdGateway) alongside plain YSFGateway | Follow **WPSD** | YSF panel — **done**; `ysfgw.enable_dgid` swaps the YSF render target to `DGIdGateway.ini` (from the same pinned YSFClients tree), `ysfgw.ycs_network` links the startup reflector on a DG-ID |
| D3 | **M17 Callsign Suffix** — WPSD adds the node-type letter (H hotspot / R repeater) appended to the callsign; classic Pi-Star has only host + CAN | Follow **WPSD** | M17 panel — `m17gw.suffix` → `M17Gateway.ini [General] Suffix` |
| Δ1 | **D-Star runs DStarGateway (MQTT era), not ircDDBGateway.** WPSD/Pi-Star expose ircDDBGateway fields (RPT1/RPT2 Callsign, Remote Password, Default Reflector, No DExtra, Callsign Routing…). Waypoint maps the same operator intent onto DStarGateway's `[Repeater 1]` / `[IRCDDB 1]` / protocol sections — a **functional** map, not a label clone | **Deliberate** difference | D-Star panel — see the two D-Star tables below |

Waypoint's own node runs **display-free** (`[General] Display=None`, dashboard
over MQTT; its forked MQTT-era MMDVM-Host has no `[Display]` parser and ignores
the keys). But as a WPSD clone Waypoint still **models** the full Display surface
(store section `display`, Setup tab) so a clone on stock/pre-MQTT MMDVM-Host, or
one driving a physical panel, gets working config — see the Setup / Display table.

---

## Setup panels

### Control Software panel

WPSD `<h2>` "Control Software".

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Radio Control Software (DStarRepeater / MMDVMHost) | — | — | done (fixed) | Waypoint is MMDVMHost-only by design; no selector — Setup tab shows a read-only `MMDVMHost`. |
| TRX Mode (Simplex Node / Duplex Repeater) | `[General] Duplex` | `general.duplex` | done | Setup tab — labelled Simplex/Duplex selector (also the DUPLEX/SIMPLEX toggle on General). |

### MMDVMHost Configuration panel — mode toggles + Display

WPSD `<h2>` "MMDVMHost Configuration" (mode enables, then Display Type).

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| DMR Mode | `[DMR] Enable` | `modes.dmr` | done | |
| D-Star Mode | `[D-Star] Enable` | `modes.dstar` | done | |
| YSF Mode | `[System Fusion] Enable` | `modes.ysf` | done | |
| P25 Mode | `[P25] Enable` | `modes.p25` | done | |
| NXDN Mode | `[NXDN] Enable` | `modes.nxdn` | done | |
| M17 Mode | `[M17] Enable` | `modes.m17` | done | requires the MMDVM-Host fork (upstream removed M17). |
| POCSAG Mode | `[POCSAG] Enable` | `modes.pocsag` | done | enable only; DAPNET config pending. |
| FM Mode | `[FM] Enable` | `modes.fm` | done | *Waypoint-only toggle* — Pi-Star/WPSD edit FM via the expert INI editor. |
| YSF2DMR / DMR2YSF / YSF2NXDN / DMR2NXDN / NXDN2DMR Mode | per cross-mode daemon (MMDVM_CM) | `ysf2dmr.enable` / `dmr2ysf.enable` / `ysf2nxdn.enable` / `dmr2nxdn.enable` / `nxdn2dmr.enable` | superseded | **Superseded by the RFC-0003 bus architecture** — an intentional departure from WPSD's bridge model. The per-bridge-daemon surface is retired: no bridge INI is rendered, no `waypoint-<bridge>.service` is started, and the Gateways tab shows a placeholder. The bridge store sections are kept **dormant** — `SetCrossBridge`/`SetSection` still accept them and they round-trip through Save/Load — so disabling loses nothing (RFC-0001) and RFC-0003's migration can seed bus definitions from the saved masters/passwords/TGs. Apply stops any bridge daemon still running (`RetiredBridgeUnits`), which also closes the stale-daemon-on-disable defect. YSF2P25 / P252DMR were never modeled. |
| Display Type (None / OLED3 / OLED6 / Nextion / HD44780 / TFT Serial / LCDproc) | `[General] Display` (+ `[OLED] Type`) | `display.type` / `display.oled_type` | done | Setup tab. Node stays `DisplayLevel=0` (status over MQTT); the driver subsections render for clone parity. |
| Display Port | `[Nextion]` / `[TFT Serial]` `Port` | `display.port` | done | Setup tab — None / modem / ttyACM* / ttyUSB* / ttyS2 / ttyNextionDriver. |
| Nextion Layout (G4KLX / ON7LDS L2 / L3 / L3 HS) | `[Nextion] ScreenLayout` | `display.nextion_layout` | done | Setup tab — shown only when type = Nextion. |
| HD44780 geometry + I2C wiring | `[HD44780] Rows` / `Columns` / `I2CAddress` | `display.hd44780_rows` / `_cols` / `_i2c_addr` | done | Setup tab — shown only when type = HD44780. I2C is the PCF8574 `I2CAddress` (hex); MMDVM-Host has no separate I2C-bus key, GPIO wiring is the alternative `Pins` list (rendered as a constant). |

### General Configuration panel

WPSD splits classic Pi-Star's single General panel into "General Configuration" +
"Location and Hotspot Info Settings". Both are folded into Waypoint's General tab.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| System Hostname | `hostnamectl` (host) | — | pending | host/OS domain, not an INI — config-coverage §4. |
| Gateway Callsign | `[General] Callsign` | `general.callsign` | done | |
| DMR/CCS7 ID *(WPSD; Pi-Star "CCS7/DMR ID")* | `[General] Id`, `[DMR] Id` | `general.id` | done | `dmr.id` overrides for DMR; falls back to `general.id`. |
| NXDN ID | `[NXDN Network]` id | — | pending | separate NXDN radio ID not modeled. |
| Radio Frequency (RX) | `[Info] RXFrequency` | `modem.rx_freq_hz` | done | Hz in store, MHz in UI. |
| Radio Frequency (TX) | `[Info] TXFrequency` | `modem.tx_freq_hz` | done | |
| Radio/Modem *(board type dropdown)* | `[Modem]` board defaults | `modem.port` | partial | raw UART port only; no board-model dropdown. |
| Baudrate *(WPSD new)* | `[Modem] UARTSpeed` | `modem.uart_speed` | partial | modeled + rendered (default 115200); not in the UI. |
| Gateway Latitude | `[Info] Latitude` | — | pending | not modeled (DStarGateway renders `0.0`). |
| Gateway Longitude | `[Info] Longitude` | — | pending | not modeled. |
| Gateway Town / City-State | `[Info] Location` | `general.location` | partial | single free-text field; not structured town/country. |
| Gateway Country | `[Info]` (location) | — | pending | not modeled. |
| Gateway URL (Auto / Manual) | `[Info] URL` | `general.url` | partial | free text; no auto/manual mode. |
| RF Power | `[Info] Power` | `general.power` | done | |
| Node Lock (Public / Private) *(WPSD: in DMR panel)* | `[DMR] SelfOnly` | `dmr.self_only` | done | now a PRIVATE / PUBLIC control in the DMR panel — see the DMR panel table below. Other modes keep their own per-mode Self-Only. |
| DMR IDs (allow other IDs) *(WPSD: in DMR panel)* | `[DMR] SelfOnly` (inverse) | `dmr.self_only` | done | see the DMR panel table below (same bit as Node Lock). |
| APRS Host Enable / APRS Gateway | gateway `[APRS]` | `ysfgw.aprs` | partial | YSF APRS beacon only; no general APRS-IS host. |
| System TimeZone | `timedatectl` (host) | — | pending | host/OS domain. |
| Dashboard Language | dashboard | — | pending | dashboard domain. |
| Update Notifier *(WPSD new)* | updates lifecycle | — | pending | |
| GPS daemon support / GPSd *(WPSD Location panel)* | `gpsd` (host) | — | pending | replaces Pi-Star Mobile GPS. |

**Modem / calibration** (WPSD Expert → MMDVMHost → Modem). Core offsets are on the
General tab; the rest is modeled but not surfaced.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| RX Offset | `[Modem] RXOffset` | `modem.rx_offset` | done | on the General tab. |
| TX Offset | `[Modem] TXOffset` | `modem.tx_offset` | done | |
| TX / RX / PTT Invert | `[Modem] TXInvert`/`RXInvert`/`PTTInvert` | `modem.tx_invert`/`rx_invert`/`ptt_invert` | partial | modeled + rendered; not in the UI. |
| RX Level / TX Level | `[Modem] RXLevel` / `TXLevel` | `modem.rx_level` / `tx_level` | partial | modeled + rendered (default 50); not in the UI. |
| Per-mode TX levels, DC offsets, RSSI mapping, DMR delay | `[Modem]` (various) | — | pending | full calibration — config-coverage [#20]. |

---

## DMR panel  ·  status: **done**

WPSD's expanded DMR panel. MMDVM-Host `[DMR]` / `[DMR Network]`, gateway
`DMRGateway.ini`. Routing is **generated from network type + primary** (WPSD
model); the operator never hand-writes rewrite lines. **D1:** SystemX and TGIF are
first-class networks.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| DMR Master (primary selector) | `[DMR Network N] Location=1` | `networks[].primary` | done | primary = no dial prefix; TG9990 Parrot catch-all. |
| BrandMeister Master | `[DMR Network N] Address` | `networks[].address` (bm) | done | from `/api/dmr/masters` (`DMR_Hosts.txt`). |
| BrandMeister Password *(BM Hotspot Security)* | `[DMR Network N] Password` | `networks[].password` (bm) | done | write-only; blank keeps stored. |
| BrandMeister Extended ID | `[DMR Network N] Id` (suffix) | `networks[].essid` (bm) | done | "01".."99" appended to DMR ID. |
| BrandMeister Network Enable | `[DMR Network N] Enabled` | `networks[].enabled` (bm) | done | |
| BrandMeister Dashboards *(link)* | — | — | done | external link to brandmeister.network; UI affordance only. |
| Brandmeister Manager *(WPSD new)* | — | — | done | external link to the BM hotspot/static-TG manager; UI affordance only (no BM-API daemon config). |
| DMR+ / FreeDMR / HBlink Master | `[DMR Network N] Address` | `networks[].address` (dmrplus) | done | unified DMR+/FreeDMR/HBlink type. |
| DMR+ / FreeDMR / HBlink Network *(options)* | `[DMR Network N] Options` | `networks[].options` (dmrplus) | done | StartRef=… etc., sent at login. |
| DMR+ / FreeDMR / HBlink Extended ID | `[DMR Network N] Id` (suffix) | `networks[].essid` (dmrplus) | done | |
| DMR+ / FreeDMR / HBlink Network Enable | `[DMR Network N] Enabled` | `networks[].enabled` (dmrplus) | done | |
| SystemX Master *(WPSD new — D1)* | `[DMR Network N] Address` | `networks[].address` (systemx) | done | rewrite prefix 4. |
| SystemX Network *(options)* | `[DMR Network N] Options` | `networks[].options` (systemx) | done | |
| SystemX Extended ID | `[DMR Network N] Id` (suffix) | `networks[].essid` (systemx) | done | |
| SystemX Network Enable | `[DMR Network N] Enabled` | `networks[].enabled` (systemx) | done | |
| SystemX Tools *(link)* | — | — | pending | external link; cosmetic. |
| TGIF Security Key *(D1)* | `[DMR Network N] Password` | `networks[].password` (tgif) | done | rewrite prefix 5; fixed master tgif.network. |
| TGIF Extended ID *(D1)* | `[DMR Network N] Id` (suffix) | `networks[].essid` (tgif) | done | |
| TGIF Network Enable *(D1)* | `[DMR Network N] Enabled` | `networks[].enabled` (tgif) | done | |
| TGIF Dashboards *(link)* | — | — | done | external link to tgif.network; UI affordance only. |
| XLX Master | `[XLX Network] File` / host | `networks[].address` (xlx) | done | XLX uses its own `[XLX Network]` section. |
| XLX Startup TG | `[XLX Network] Startup` | `networks[].xlx_startup` | done | |
| XLX Startup Module override | `[XLX Network] Module` | `networks[].xlx_module` | done | |
| Time Slot *(XLX — WPSD new)* | `[XLX Network] Slot` | `networks[].xlx_slot` | done | |
| XLX Master Enable | `[XLX Network] Enabled` | `networks[].enabled` (xlx) | done | |
| Enable DMR Roaming Beacon *(WPSD new; replaces JitterBuffer)* | `[DMR] Beacons` | `dmr.beacons` | done | WPSD dropped the JitterBuffer field — Waypoint matches. |
| DMR Color Code | `[DMR] ColorCode` | `dmr.color_code` | done | 0..15 select. |
| DMR EmbeddedLCOnly | `[DMR] EmbeddedLCOnly` | `dmr.embedded_lc_only` | done | |
| DMR DumpTAData | `[DMR] DumpTAData` | `dmr.dump_ta_data` | done | |
| Node Lock *(WPSD: moved into DMR panel)* | `[DMR] SelfOnly` | `dmr.self_only` | done | PRIVATE / PUBLIC toggle in the DMR panel (General DMR Settings + DMR Settings tab). PRIVATE = `SelfOnly=1`. |
| DMR IDs (allow other IDs) *(WPSD: moved here)* | `[DMR] SelfOnly` (inverse) | `dmr.self_only` | done | Same bit as Node Lock, inverse framing (PUBLIC = allow other IDs). MMDVM-Host has no multi-ID allowlist, so Waypoint models one field, not a dead per-ID key. |
| *(Waypoint-only)* Time Slot 1 / 2 Enabled | `[DMR Network] Slot1` / `Slot2` | `dmrnet.slot1` / `slot2` | done | implicit in WPSD; explicit toggles here. |
| *(Waypoint-only)* Talkgroup routing override | `[DMR Network N] TGRewrite` | `routes[].slot/tg/network` | done | "tie a dialed TG to a gateway"; wins over primary. |
| DMR JitterBuffer *(Pi-Star classic only)* | `[DMR Network] JitterBuffer` | — | N/A | removed in WPSD; Waypoint omits (matches WPSD). |

---

## D-Star panel  ·  status: **done** (deliberate functional map — Δ1)

WPSD/Pi-Star expose **ircDDBGateway** fields. Waypoint runs **DStarGateway**
(MQTT era): MMDVM-Host `[D-Star]` / `[D-Star Network]`, gateway
`dstargateway.cfg`. The tables below give (a) each WPSD ircDDBGateway field and how
Waypoint covers its intent, then (b) the DStarGateway fields with no WPSD label.

There is **no RPT1/RPT2 concept** here (Δ1): the repeater callsign comes from the
station identity (`general.callsign`) and the module is a single band letter
(`dstar.module`) that the renderer mirrors into the gateway `[Repeater 1] Band` —
so the two files can never disagree on the module. The ircDDB password is a
write-only secret handled exactly like the DMR-network passwords: redacted from
the API View (`has_ircddb_password` reports only whether one is set) and preserved
on a blank write, both UI-side (`cleanDstargw` omits it) and server-side
(`SetDStarGateway`).

### WPSD ircDDBGateway fields → Waypoint

| WPSD field label | Waypoint INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| RPT1 Callsign (callsign + module) | `[D-Star] Module` + `[General] Callsign` | `dstar.module` (+ `general.callsign`) | done | Δ1: callsign from station identity, module from `dstar.module`; renderer mirrors module into gateway `[Repeater 1] Band`. |
| RPT2 Callsign (callsign + "G") | gateway callsign (auto) | — | N/A | gateway callsign derived; not a separate field. |
| Remote Password (ircDDBGateway remote control) | `[Remote Commands] Enabled=0` | — | N/A | Waypoint disables remote commands; no equivalent field. |
| Default Reflector (reflector + module + startup + auto-connect) | `[Repeater 1] Reflector` + `ReflectorAtStartup` | `dstargw.reflector` | done | e.g. "REF001 C"; empty = none; auto-connect derived — `ReflectorAtStartup=1` iff a reflector is set. |
| ircDDBGateway Language | — | — | pending | no dashboard/gateway language. |
| Time Announce | — | — | pending | not modeled. |
| Callsign Routing | `[IRCDDB 1]` login | `dstargw.ircddb_*` | done | Δ1: Waypoint always runs ircDDB for routing (see next table). |
| No DExtra | `[Dextra] Enabled` (inverse) | `dstargw.dextra` | done | Waypoint models the enable directly (not the inverse "No" toggle). |

### Waypoint DStarGateway fields (no direct WPSD label — Δ1)

| Waypoint field | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Reflector reconnect | `[Repeater 1] ReflectorReconnect` | `dstargw.reflector_reconnect` | done | enum Never/Fixed/5..180; clamped so a bad value can't render an unstartable config. |
| ircDDB host | `[IRCDDB 1] Hostname` | `dstargw.ircddb_hostname` | done | default ircv4.openquad.net. |
| ircDDB username | `[IRCDDB 1] Username` | `dstargw.ircddb_username` | done | blank = station callsign. |
| ircDDB password | `[IRCDDB 1] Password` | `dstargw.ircddb_password` | done | secret; write-only; redacted in view; preserved on blank write; blank = anonymous. |
| D-Plus (REF) enable | `[D-Plus] Enabled` | `dstargw.dplus` | done | force-disabled upstream if Login empty. |
| D-Plus login | `[D-Plus] Login` | `dstargw.dplus_login` | done | blank = station callsign; needs US-Trust registration. |
| DCS enable | `[DCS] Enabled` | `dstargw.dcs` | done | |
| XLX enable | `[XLX] Enabled` | `dstargw.xlx` | done | |
| Self Only | `[D-Star] SelfOnly` | `dstar.self_only` | done | |
| Remote Gateway | `[D-Star] RemoteGateway` | `dstar.remote_gateway` | done | off for a local DStarGateway. |

---

## System Fusion (YSF) panel  ·  status: **done** (WPSD parity; see gaps)

WPSD YSF panel. MMDVM-Host `[System Fusion]` / `[System Fusion Network]`, gateway
`YSFGateway.ini`. **D2:** WPSD's DG-ID / YCS path is modeled — `ysfgw.enable_dgid`
renders `DGIdGateway.ini` *instead of* `YSFGateway.ini` (both from the pinned
YSFClients tree; they share MMDVM-Host's fixed 3200/4200 loopback, so they are
mutually exclusive). The deploy's two units — `waypoint-ysfgateway.service` and
`waypoint-dgidgateway.service` — must carry systemd `Conflicts=` on each other so
restarting the enabled one stops the other; waypointd's apply restarts whichever
target `ysfgw.enable_dgid` selects. The YSF2DMR/YSF2NXDN cross-mode bridges (and
their DMR/NXDN counterparts) are **superseded by the RFC-0003 bus architecture** —
the Gateways tab now shows a placeholder, not per-bridge cards; see the Cross-mode
row in the mode table.

### WPSD YSF fields

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| YSF Host / YSF Startup Host | `YSFGateway.ini [Network] Startup` | `ysfgw.startup` | done | reflector or FCS room, e.g. FCS00290; datalist picker in `panelYSF`. |
| UPPERCASE Hostfiles | hostfile casing | `ysfgw.upper_hostfiles` | done | fetch-time transform in the `ysfhosts` cacher (uppercases reflector names, preserving all other fields); NOT an INI key — neither pinned binary parses `WiresXMakeUpper`. |
| WiresX Auto Passthrough | `[General] WiresXCommandPassthrough` | `ysfgw.wiresx_passthrough` | done | |
| Enable DGIdGateway *(WPSD new — D2)* | `DGIdGateway.ini` | `ysfgw.enable_dgid` | done | swaps the YSF render target/unit to DGIdGateway (mutually exclusive with YSFGateway). |
| YCS Network *(WPSD new — D2)* | `DGIdGateway.ini` `[DGId=5]` | `ysfgw.ycs_network` | done | links the startup reflector/room as a static DG-ID network (Type from the id: FCS room → `FCS`, else `YSF`). |
| YSF2DMR: CCS7/DMR ID · DMR Master · DMR Options · DMR Master Password · YSF2DMR TG | *(no INI — retired)* | `ysf2dmr.dmr_id` / `.master` / `.options` / `.password` / `.tg` | superseded | **Superseded by the RFC-0003 bus architecture.** No YSF2DMR.ini is rendered; the store section is kept dormant (data preserved for RFC-0003 migration, password still redacted/preserved). See the Cross-mode row. |
| YSF2NXDN: NXDN ID · NXDN TG | *(no INI — retired)* | `ysf2nxdn.nxdn_id` / `.tg` | superseded | **Superseded by the RFC-0003 bus architecture** — dormant store section, no INI. |
| YSF2P25: DMR ID · P25 Host | `YSF2P25.ini` | — | not modeled | out of scope (no P25 cross-gateway). |

### Waypoint YSF fields (modeled beyond the WPSD panel labels)

| Waypoint field | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Suffix (RPT/ND) | `YSFGateway.ini [General] Suffix` | `ysfgw.suffix` | done | RPT duplex / ND simplex; `panelYSF` GATEWAY card. |
| Revert to startup on inactivity | `[Network] Revert` | `ysfgw.revert` | done | BEHAVIOUR card toggle. |
| Inactivity revert (min) | `[Network] InactivityTimeout` | `ysfgw.inactivity_timeout` | done | default 30. |
| YSF reflector network | `[YSF Network] Enable` | `ysfgw.ysf_network` | done | REFLECTOR NETWORKS card. |
| FCS room network | `[FCS Network] Enable` | `ysfgw.fcs_network` | done | REFLECTOR NETWORKS card. |
| APRS position beacon | `[APRS] Enable` | `ysfgw.aprs` | done | REFLECTOR NETWORKS card. |
| Low Deviation | `[System Fusion] LowDeviation` | `ysf.low_deviation` | done | store + rendered + `ViewYSF`/`panelYSF` BEHAVIOUR card (gap G1 closed). |
| Self Only | `[System Fusion] SelfOnly` | `ysf.self_only` | done | store + rendered + BEHAVIOUR card toggle (gap G1 closed); parity with P25/NXDN/M17/D-Star `self_only`. |
| TX Hang / Mode Hang | `[System Fusion] TXHang` / `ModeHang` | `ysf.tx_hang` / `ysf.mode_hang` | done | store + rendered (defaults 4 / 20) + `panelYSF` HANG TIMERS card (gap G1 closed). |
| Remote Gateway | `[System Fusion] RemoteGateway` | `ysf.remote_gateway` | done | store + rendered + BEHAVIOUR card toggle (gap G1 closed); parity with P25/NXDN/D-Star `remote_gateway`. |

---

## P25 panel  ·  status: **done**

WPSD P25 panel (host + NAC). MMDVM-Host `[P25]` / `[P25 Network]`, gateway
`P25Gateway.ini`.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| P25 Host / P25 Startup Host | `P25Gateway.ini [Network] Static` | `p25gw.static` | done | comma-separated startup TGs; datalist picker. |
| P25 NAC | `[P25] NAC` | `p25.nac` | done | hex; default 293. |
| *(Waypoint)* Self Only | `[P25] SelfOnly` | `p25.self_only` | done | BEHAVIOUR card. |
| *(Waypoint)* Override UID Check | `[P25] OverrideUIDCheck` | `p25.override_uid_check` | done | BEHAVIOUR card. |
| *(Waypoint)* Remote Gateway | `[P25] RemoteGateway` | `p25.remote_gateway` | done | BEHAVIOUR card. |
| *(Waypoint)* TX Hang | `[P25] TXHang` | `p25.tx_hang` | partial | store + rendered (default 5); **no view/UI** — gap G2 (not a WPSD panel field). |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `p25gw.voice` | done | GATEWAY card. |
| *(Waypoint)* RF / Network hang | `[Network] RFHangTime` / `NetHangTime` | `p25gw.rf_hang_time` / `net_hang_time` | done | defaults 120 / 60; HANG TIMERS card. |

---

## NXDN panel  ·  status: **done**

WPSD NXDN panel (host + RAN). MMDVM-Host `[NXDN]` / `[NXDN Network]`, gateway
`NXDNGateway.ini`.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| NXDN Host / NXDN Startup Host | `NXDNGateway.ini [Network] Static` | `nxdngw.static` | done | comma-separated startup TGs; datalist picker. |
| NXDN RAN | `[NXDN] RAN` | `nxdn.ran` | done | decimal Radio Access Number; default 1. |
| *(Waypoint)* Self Only | `[NXDN] SelfOnly` | `nxdn.self_only` | done | BEHAVIOUR card. |
| *(Waypoint)* Remote Gateway | `[NXDN] RemoteGateway` | `nxdn.remote_gateway` | done | BEHAVIOUR card. |
| *(Waypoint)* TX Hang | `[NXDN] TXHang` | `nxdn.tx_hang` | partial | store + rendered (default 5); **no view/UI** — gap G2. |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `nxdngw.voice` | done | GATEWAY card. |
| *(Waypoint)* RF / Network hang | `[Network] RFHangTime` / `NetHangTime` | `nxdngw.rf_hang_time` / `net_hang_time` | done | defaults 120 / 60; HANG TIMERS card. |

---

## M17 panel  ·  status: **done**

WPSD M17 panel. MMDVM-Host `[M17]` (via Waypoint's MMDVM-Host fork — upstream
removed M17) / `[M17 Network]`, gateway `M17Gateway.ini` (pre-MQTT). **D3:** WPSD
adds the Callsign Suffix; M17 uses a **CAN** (not RAN/NAC) and has no RemoteGateway.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Startup Reflector *(WPSD; Pi-Star "M17 Host")* | `M17Gateway.ini [Network] Startup` | `m17gw.startup` | done | e.g. "M17-M17 C"; datalist picker. |
| M17 CAN | `[M17] CAN` | `m17.can` | done | Channel Access Number, decimal 0..15; default 0. |
| Callsign Suffix *(WPSD new — D3)* | `M17Gateway.ini [General] Suffix` | `m17gw.suffix` | done | H hotspot / R repeater select in `panelM17`. |
| *(Waypoint)* Self Only | `[M17] SelfOnly` | `m17.self_only` | done | BEHAVIOUR card. |
| *(Waypoint)* Allow encrypted M17 frames | `[M17] AllowEncryption` | `m17.allow_encryption` | done | off by default; BEHAVIOUR card. |
| *(Waypoint)* TX Hang | `[M17] TXHang` | `m17.tx_hang` | partial | store + rendered (default 5); **no view/UI** — gap G2. |
| *(Waypoint)* Revert to startup after inactivity | `[Network] Revert` | `m17gw.revert` | done | BEHAVIOUR card. |
| *(Waypoint)* Network hang | `[Network] HangTime` | `m17gw.hang_time` | done | default 240; HANG TIMER card. |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `m17gw.voice` | done | GATEWAY card. |

---

## POCSAG panel  ·  status: **pending**

WPSD "POCSAG / Paging Configuration". MMDVM-Host `[POCSAG]`, gateway
`DAPNETGateway.ini`. Only the mode enable is modeled.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| POCSAG Server | `DAPNETGateway.ini` server | — | pending | DAPNET core server. |
| POCSAG Callsign | `DAPNETGateway.ini` callsign | — | pending | |
| POCSAG Frequency | `[POCSAG] Frequency` | — | pending | |
| DAPNET AuthKey | `DAPNETGateway.ini [DAPNET] AuthKey` | — | pending | secret. |
| POCSAG Whitelist | `DAPNETGateway.ini` | — | pending | |
| POCSAG Blacklist | `DAPNETGateway.ini` | — | pending | |
| *(Waypoint)* POCSAG enable | `[POCSAG] Enable` | `modes.pocsag` | done | renders an empty `[POCSAG]`. |

---

## FM panel  ·  status: **pending**

There is **no dedicated FM panel** in classic Pi-Star or WPSD — FM is edited
through the expert MMDVMHost INI editor (`[FM]` section). Waypoint adds an FM mode
toggle; the analog config surface is not modeled.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| *(expert INI editor)* FM Callsign / CW ID | `[FM] Callsign*` | — | pending | edited via expert in WPSD; not modeled. |
| *(expert INI editor)* Repeater Callsign / Frequency | `[FM] RptCallsign` / `RptFrequency` | — | pending | |
| *(expert INI editor)* CTCSS frequency / thresholds / level | `[FM] CTCSS*` | — | pending | |
| *(expert INI editor)* Timeout / Timeout level | `[FM] Timeout` / `TimeoutLevel` | — | pending | |
| *(expert INI editor)* Kerchunk / Hang time | `[FM] KerchunkTime` / `HangTime` | — | pending | |
| *(expert INI editor)* Access mode | `[FM] AccessMode` | — | pending | |
| *(expert INI editor)* Audio levels | `[FM]` (various levels) | — | pending | |
| *(Waypoint-only)* FM enable | `[FM] Enable` | `modes.fm` | done | renders an empty `[FM]`. |

---

## Out of scope for the mode panels (tracked elsewhere)

These WPSD panels are not mode config and are covered by config-coverage.md §4–6:
GPSd, Firewall (dashboard/SSH/uPnP/Auto-AP access), Wireless (wpa_supplicant),
IP config (DHCP/static/VLAN/DNS), Remote Access Password (dashboard auth,
RFC-0002), and the Expert raw-INI editors. Status: all **pending**.

---

## Prompt 10 parity report (verified 2026-07-13 · branch `cross-mode-gateways`)

Verification only — no feature changes in this slice. Layers checked per field:
**store** ([`model.go`](../internal/config/model.go)) · **render**
([`render.go`](../internal/config/render.go)) · **view**
([`view.go`](../internal/config/view.go)) · **UI**
([`settings.js`](../ui/static/settings.js)). Evidence: `go test ./internal/config/`
passes, including the new real-export round-trip guard.

### 1 · Checklist walk — every "done" field has all four layers

Panels walked field-by-field; each `done` row was confirmed to have a store key, a
renderer line, a view projection, **and** a UI control. Result:

| Panel | Store | Render | View | UI | Verdict |
|---|:-:|:-:|:-:|:-:|---|
| Setup / Display (Control Software, mode toggles, Display driver+subsections) | ✅ | ✅ | ✅ | ✅ | **done** |
| General (identity, RX/TX freq, power, offsets, node-lock) | ✅ | ✅ | ✅ | ✅ | **done** (per-row `partial` items are genuine — see below) |
| DMR (`dmr`/`dmrnet`/`networks`/`routes`, D1 SystemX+TGIF) | ✅ | ✅ | ✅ | ✅ | **done** |
| D-Star (Δ1 DStarGateway map, ircDDB secret) | ✅ | ✅ | ✅ | ✅ | **done** |
| YSF gateway surface (startup, suffix, revert, YSF/FCS/APRS, D2 DG-ID/YCS/UPPER) | ✅ | ✅ | ✅ | ✅ | **done** |
| P25 (NAC, static, self-only, override-UID, remote-gw, voice, hang) | ✅ | ✅ | ✅ | ✅ | **done** |
| NXDN (RAN, static, self-only, remote-gw, voice, hang) | ✅ | ✅ | ✅ | ✅ | **done** |
| M17 (CAN, startup, D3 suffix, self-only, allow-enc, revert, hang, voice) | ✅ | ✅ | ✅ | ✅ | **done** |
| Cross-mode bridges (YSF2DMR/DMR2YSF/YSF2NXDN/DMR2NXDN/NXDN2DMR) | ▪ | ▪ | ▪ | ▪ | **superseded (RFC-0003)** |

> **Cross-mode bridges — superseded.** Verified `done` on 2026-07-13, then retired:
> the per-bridge-daemon model is replaced by the RFC-0003 bus architecture (an
> intentional departure from WPSD's bridge model). No bridge INI is rendered, no
> `waypoint-<bridge>.service` is started, and the Gateways tab shows a placeholder.
> The bridge store sections are kept **dormant** (data preserved for RFC-0003's
> migration); apply stops any bridge daemon still running.

**Statuses flipped this pass:** YSF/P25/NXDN/M17 panel headers `pending → done`;
YSF `ysfgw.startup/suffix/revert/inactivity_timeout/ysf_network/fcs_network/aprs`,
all P25/NXDN mode+gateway rows, and all M17 rows `pending → done`. The baseline
note was rewritten to match the shipped code.

**Remaining gaps (no field claimed `done` has one):**

- **G1 — YSF `[System Fusion]` mode params not surfaced. (CLOSED)** `ysf.self_only`,
  `ysf.low_deviation`, `ysf.tx_hang`, `ysf.mode_hang`, `ysf.remote_gateway` are now
  in `ViewYSF` and `panelYSF` (BEHAVIOUR + HANG TIMERS cards), matching how
  P25/NXDN/M17/D-Star surface their Self-Only / Remote-Gateway equivalents. Closed
  alongside the cross-mode-bridge retirement.
- **G2 — per-mode `TXHang` not surfaced.** `p25.tx_hang`, `nxdn.tx_hang`,
  `m17.tx_hang` are stored + rendered (defaults 5) but absent from the view/UI.
  Not a WPSD panel field, so no WPSD-parity impact; marked `partial`.
- **Pre-existing pending (unchanged, accurate):** modem calibration (invert
  flags, RX/TX levels, DC offsets, per-mode levels, DMR delay, UART baud);
  structured location (lat/long/town/country); board-model dropdown; general
  APRS-IS host; POCSAG/DAPNET; FM analog surface; host/OS (hostname, TZ, GPSd,
  firewall, Wi-Fi/IP, dashboard auth) and the Expert raw-INI editors.

### 2 · Losslessness — real WPSD/Pi-Star export round-trip

A realistic current WPSD/Pi-Star export (MMDVMHost + DMRGateway.ini, BM-primary +
TGIF, real secrets) was driven through the **actual** `Import → Save (SQLite) →
Load → Render` pipeline and diffed section-by-section
(`TestParityRealRoundTrip`, the real-export companion to `TestLosslessRoundTrip`):

- **DMRGateway.ini — 100% lossless.** Both `[DMR Network 1/2]` round-trip
  byte-identical: names, addresses, ports, `Location=1` primary flag, extended
  `Id`, **passwords**, and every generated rewrite/`PassAll` line. Both networks
  re-classified to their clean WPSD type on import (no verbatim-preserve needed).
- **MMDVM-Host — every modeled key round-trips.** `[General]` (callsign, id,
  timeout, duplex, RF/Net mode-hang, display), `[Info]` (RX/TX freq, power,
  location, URL), `[Modem]` (UARTPort, UARTSpeed, invert flags, offsets, levels),
  `[D-Star]/[DMR]/[System Fusion]/[P25]/[NXDN]/[M17]` mode params, and `[DMR
  Network]` (local/gateway addr+port, jitter, slots) all match.
- **Fields the importer drops** (all previously flagged non-modeled — no
  surprises): `[Info] Latitude/Longitude/Height/Description`; `[Modem]
  TXDelay/DMRDelay/RFLevel/RXDCOffset/TXDCOffset/CWIdTXLevel/DMRTXLevel`. These are
  the pending calibration/location surfaces above.
- **One intentional value change** (not a loss): `[General] Daemon 1 → 0` —
  Waypoint always renders `Daemon=0` because systemd owns the process lifecycle.

Note: `Import()` reads only MMDVMHost + DMRGateway.ini (the always-present core);
the per-mode gateway INIs (YSFGateway/DStarGateway/…) are seeded to defaults on a
real import and are exercised by the in-memory `TestLosslessRoundTrip` harness
(render → parse → `fromINI`) instead.

### 3 · Mode-gated behavior in the persistent-nav shell

Verified by inspection of the shell wiring (`settings.js`):

- **Statusbar LEDs track enable.** `renderStatus()` rebuilds `#leds` from
  `config.modes`, adding `.on` per enabled mode; the LED reflects applied truth and
  refreshes after `apply() → load()`.
- **Each panel carries its own enable.** Every mode panel's first control is
  `toggle("modes", "<mode>", …)` (and the Modes tab mirrors all eight); flipping it
  marks `modes` dirty and re-renders, then persists on Apply.
- **Divergence held, as specified.** Pi-Star/WPSD show/hide whole panels by mode
  enable; Waypoint keeps a persistent tab per mode (all `TABS` always in `renderNav`)
  and moves the enable into the panel, with settings preserved across disable
  (RFC-0001). This matches the "field/feature parity inside the shell, not the
  single-page layout" target set at the top of this spec.
