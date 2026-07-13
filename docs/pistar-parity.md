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

**Baseline as of this spec:** `general` (core identity/RF), `modem` (core),
`dmr`, `dmrnet`, `modes`, and `networks` are **done**. Every other mode's own
parameters and its gateway configuration are **pending** acceptance — the
store/renderer/UI scaffolding present on the `dmr-pistar-ui` branch is exactly
what this checklist gates. Host/OS, cross-mode, POCSAG, and FM config are
not modeled at all. Display is modeled (store section `display`, Setup tab).

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
| YSF2DMR / YSF2NXDN / YSF2P25 / DMR2YSF / DMR2NXDN Mode | per cross-mode daemon | — | pending | cross-mode bridges; "Gateways" tab is a stub. |
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

## System Fusion (YSF) panel  ·  status: **pending**

WPSD YSF panel. MMDVM-Host `[System Fusion]` / `[System Fusion Network]`, gateway
`YSFGateway.ini`. **D2:** WPSD's DG-ID / YCS path is modeled — `ysfgw.enable_dgid`
renders `DGIdGateway.ini` *instead of* `YSFGateway.ini` (both from the pinned
YSFClients tree; they share MMDVM-Host's fixed 3200/4200 loopback, so they are
mutually exclusive). The deploy's two units — `waypoint-ysfgateway.service` and
`waypoint-dgidgateway.service` — must carry systemd `Conflicts=` on each other so
restarting the enabled one stops the other; waypointd's apply restarts whichever
target `ysfgw.enable_dgid` selects. The YSF2DMR/NXDN/P25 cross-mode bridges remain
out of scope (Waypoint runs no DMR/NXDN/P25 cross-gateways).

### WPSD YSF fields

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| YSF Host / YSF Startup Host | `YSFGateway.ini [Network] Startup` | `ysfgw.startup` | pending | reflector or FCS room, e.g. FCS00290. |
| UPPERCASE Hostfiles | hostfile casing | `ysfgw.upper_hostfiles` | done | fetch-time transform in the `ysfhosts` cacher (uppercases reflector names, preserving all other fields); NOT an INI key — neither pinned binary parses `WiresXMakeUpper`. |
| WiresX Auto Passthrough | `[General] WiresXCommandPassthrough` | `ysfgw.wiresx_passthrough` | done | |
| Enable DGIdGateway *(WPSD new — D2)* | `DGIdGateway.ini` | `ysfgw.enable_dgid` | done | swaps the YSF render target/unit to DGIdGateway (mutually exclusive with YSFGateway). |
| YCS Network *(WPSD new — D2)* | `DGIdGateway.ini` `[DGId=5]` | `ysfgw.ycs_network` | done | links the startup reflector/room as a static DG-ID network (Type from the id: FCS room → `FCS`, else `YSF`). |
| YSF2DMR: CCS7/DMR ID · DMR Master · DMR Options · DMR Master Password · YSF2DMR TG | `YSF2DMR.ini` | — | pending | cross-mode bridge (DMR Options is a WPSD addition). |
| YSF2NXDN: NXDN ID · NXDN Host | `YSF2NXDN.ini` | — | pending | cross-mode bridge. |
| YSF2P25: DMR ID · P25 Host | `YSF2P25.ini` | — | pending | cross-mode bridge. |

### Waypoint YSF fields (modeled beyond the WPSD panel labels)

| Waypoint field | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Suffix (RPT/ND) | `YSFGateway.ini [General] Suffix` | `ysfgw.suffix` | pending | RPT duplex / ND simplex. |
| Revert to startup on inactivity | `[Network] Revert` | `ysfgw.revert` | pending | |
| Inactivity revert (min) | `[Network] InactivityTimeout` | `ysfgw.inactivity_timeout` | pending | default 30. |
| YSF reflector network | `[YSF Network] Enable` | `ysfgw.ysf_network` | pending | |
| FCS room network | `[FCS Network] Enable` | `ysfgw.fcs_network` | pending | |
| APRS position beacon | `[APRS] Enable` | `ysfgw.aprs` | pending | |
| Low Deviation | `[System Fusion] LowDeviation` | `ysf.low_deviation` | pending | |
| Self Only | `[System Fusion] SelfOnly` | `ysf.self_only` | pending | |
| TX Hang / Mode Hang | `[System Fusion] TXHang` / `ModeHang` | `ysf.tx_hang` / `ysf.mode_hang` | pending | defaults 4 / 20. |
| Remote Gateway | `[System Fusion] RemoteGateway` | `ysf.remote_gateway` | pending | |

---

## P25 panel  ·  status: **pending**

WPSD P25 panel (host + NAC). MMDVM-Host `[P25]` / `[P25 Network]`, gateway
`P25Gateway.ini`.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| P25 Host / P25 Startup Host | `P25Gateway.ini [Network] Static` | `p25gw.static` | pending | comma-separated startup TGs. |
| P25 NAC | `[P25] NAC` | `p25.nac` | pending | hex; default 293. |
| *(Waypoint)* Self Only | `[P25] SelfOnly` | `p25.self_only` | pending | |
| *(Waypoint)* Override UID Check | `[P25] OverrideUIDCheck` | `p25.override_uid_check` | pending | |
| *(Waypoint)* Remote Gateway | `[P25] RemoteGateway` | `p25.remote_gateway` | pending | |
| *(Waypoint)* TX Hang | `[P25] TXHang` | `p25.tx_hang` | pending | default 5. |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `p25gw.voice` | pending | |
| *(Waypoint)* RF / Network hang | `[Network] RFHangTime` / `NetHangTime` | `p25gw.rf_hang_time` / `net_hang_time` | pending | defaults 120 / 60. |

---

## NXDN panel  ·  status: **pending**

WPSD NXDN panel (host + RAN). MMDVM-Host `[NXDN]` / `[NXDN Network]`, gateway
`NXDNGateway.ini`.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| NXDN Host / NXDN Startup Host | `NXDNGateway.ini [Network] Static` | `nxdngw.static` | pending | comma-separated startup TGs. |
| NXDN RAN | `[NXDN] RAN` | `nxdn.ran` | pending | decimal Radio Access Number; default 1. |
| *(Waypoint)* Self Only | `[NXDN] SelfOnly` | `nxdn.self_only` | pending | |
| *(Waypoint)* Remote Gateway | `[NXDN] RemoteGateway` | `nxdn.remote_gateway` | pending | |
| *(Waypoint)* TX Hang | `[NXDN] TXHang` | `nxdn.tx_hang` | pending | default 5. |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `nxdngw.voice` | pending | |
| *(Waypoint)* RF / Network hang | `[Network] RFHangTime` / `NetHangTime` | `nxdngw.rf_hang_time` / `net_hang_time` | pending | defaults 120 / 60. |

---

## M17 panel  ·  status: **pending**

WPSD M17 panel. MMDVM-Host `[M17]` (via Waypoint's MMDVM-Host fork — upstream
removed M17) / `[M17 Network]`, gateway `M17Gateway.ini` (pre-MQTT). **D3:** WPSD
adds the Callsign Suffix; M17 uses a **CAN** (not RAN/NAC) and has no RemoteGateway.

| WPSD field label | INI key | store `section.key` | status | notes |
|---|---|---|---|---|
| Startup Reflector *(WPSD; Pi-Star "M17 Host")* | `M17Gateway.ini [Network] Startup` | `m17gw.startup` | pending | e.g. "M17-M17 C"; empty = none. |
| M17 CAN | `[M17] CAN` | `m17.can` | pending | Channel Access Number, decimal 0..15; default 0. |
| Callsign Suffix *(WPSD new — D3)* | `M17Gateway.ini [General] Suffix` | `m17gw.suffix` | pending | H hotspot / R repeater appended to callsign. |
| *(Waypoint)* Self Only | `[M17] SelfOnly` | `m17.self_only` | pending | |
| *(Waypoint)* Allow encrypted M17 frames | `[M17] AllowEncryption` | `m17.allow_encryption` | pending | off by default. |
| *(Waypoint)* TX Hang | `[M17] TXHang` | `m17.tx_hang` | pending | default 5. |
| *(Waypoint)* Revert to startup after inactivity | `[Network] Revert` | `m17gw.revert` | pending | |
| *(Waypoint)* Network hang | `[Network] HangTime` | `m17gw.hang_time` | pending | default 240. |
| *(Waypoint)* Voice announcements | `[Voice] Enabled` | `m17gw.voice` | pending | |

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
