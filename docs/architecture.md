# Waypoint Architecture

*Status: accepted for Phase 0/1. Architecture-level changes go through the RFC process.*

## Layers

```
┌─────────────────────────────────────────────────────┐
│ Radio: MMDVM_HS (STM32F103+ADF7021) / MMDVM (F4/F7) │
└──────────────────────┬──────────────────────────────┘
                       │ serial (GPIO UART / USB CDC)
┌──────────────────────┴──────────────────────────────┐
│ g4klx host stack — UNMODIFIED upstream daemons      │
│ MMDVM-Host · DMRGateway · YSFGateway/DGIdGateway ·  │
│ P25/NXDN gateways · DAPNETGateway · APRSGateway ·   │
│ DStarGateway                                        │
└──────────────────────┬──────────────────────────────┘
                       │ MQTT (mosquitto, JSON events + control)
┌──────────────────────┴──────────────────────────────┐
│ waypointd (Go, single static binary)                │
│  · config store   · supervisor    · hardware ops    │
│  · REST + WebSocket API           · embedded web UI │
└──────────────────────┬──────────────────────────────┘
                       │ HTTPS
                 Browser / apps / integrations
```

Rationale for the split: the g4klx daemons are actively maintained upstream and moving to MQTT as their native data plane (May 2026 MMDVM-Host rename + libmosquitto requirement). Waypoint adds the layer upstream deliberately doesn't provide — configuration, supervision, hardware lifecycle, and UX — without forking the protocol implementations. We pin exact stack versions in [waypoint-stack](https://github.com/KN4OQW/waypoint-stack) and carry patches only while they're in flight upstream.

## waypointd components

### Config store
- Single schema-versioned document in SQLite; explicit migrations.
- Gateway INI files are **compiled outputs** of the store — regenerated deterministically, diffable, never parsed back.
- The store keeps settings for disabled modes (the incumbent "Apply Changes ate my DMR password" family is structurally impossible).
- **Override layer** ([RFC-0005](rfcs/0005-override-layer.md)): `overrides.d/<daemon>.d/*.conf` drop-ins merge last into generated configs (section/key merge, `!unset`, lexical precedence); hostfile `prepend.d`/`append.d` hooks. Overrides are surfaced read-only in the UI, not fought by the updater.
- **Profiles**: named snapshots of the network/mode subset of the store; one-click switch; import/export as signed JSON files.

### Supervisor
- Owns systemd units for stack daemons; derives desired-state from the config store.
- Subscribes to the MQTT bus for liveness/status; enforces reconnect policies for DMR masters, APRS-IS, DAPNET (papering over upstream gaps: MMDVM-Host #682, APRSGateway #1, DAPNETGateway #10).
- Publishes normalized status to `waypoint/status/#` topics — Home Assistant-friendly.

### Hardware ops
- Board detection: USB VID/PID table + GPIO serial probe (`MMDVM_HS_*`, full MMDVM, DVMega).
- Firmware flashing as an API operation with progress streaming: `stm32flash` over GPIO (BOOT0/RESET toggling, sysfs base-512 aware) and USB bootloader paths; `avrdude` for DVMega.
- Calibration wizard: drives MMDVMCal over the modem port; guided RX/TX offset sweep with live BER readout for HS boards, full level/invert workflow for repeater boards.

### API
- REST for config/actions (OpenAPI-documented), WebSocket for event streams.
- The bundled dashboard is a client of the public API with no private endpoints — third-party displays and apps are first-class by construction.
- AuthN: first-boot device claim sets the admin credential; session cookies + token auth for API clients; HTTPS default (self-signed at claim time, ACME optional).

## Web UI
- Svelte SPA, static assets embedded in the daemon binary (single-artifact deploy).
- Dark-mode default, responsive to 360 px, WCAG AA as a merge gate.
- Dual persona: *simple* (wizard, profiles, live activity) and *expert* (full config tree, generated-INI preview, live log/MQTT tail). The expert view is a commitment, not a leftover — Pi-Star's expert editors are one of its most loved features.

## Distribution
- **Phase 1:** `.deb` + install script on stock Raspberry Pi OS Lite (armv6/armhf/arm64); systemd-managed; works alongside an existing modem hat immediately.
- **Phase 3:** purpose-built image: read-only root, A/B slots with automatic rollback, separate config partition. (Same pattern `MW0MWZ/Pi-Star_OS` is validating with Alpine — deliberately arriving second on plumbing, first on payload.)
- Update artifacts are signed; the updater verifies before switching slots; failed boots roll back automatically.

## Non-goals (Phase 0–2)
- Forking any g4klx protocol daemon.
- Transcoding/cross-mode (upstream MMDVM-CrossMode/Transcoder exist; revisit Phase 4).
- Supporting non-Linux hosts.

## Board support tiers

| Tier | Family | Notes |
|---|---|---|
| Launch | MMDVM_HS (Hat/Dual Hat 12.288+14.7456 MHz, JumboSpot, ZUMspot line, Nano hotSPOT, NanoDV, D2RG, LoneStar, SkyBridge, EuroNode) | GPIO + USB; flash + offset-cal wizards |
| Fast-follow | Full MMDVM (MMDVM-Pi, STM32_DVM, ZUM F4M/F7M, RPT hats, Nucleo) | Repeater-class; full analog calibration |
| Legacy | DVMega (ATmega) | D-Star/DMR/YSF only; avrdude flashing |
