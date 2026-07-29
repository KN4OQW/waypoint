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
- **Override layer** ([RFC-0005](https://github.com/KN4OQW/waypoint/discussions/160)): `overrides.d/<daemon>.d/*.conf` drop-ins merge last into generated configs (section/key merge, `!unset`, lexical precedence); hostfile `prepend.d`/`append.d` hooks. Overrides are surfaced read-only in the UI, not fought by the updater.
- **Profiles** ([RFC-0006](https://github.com/KN4OQW/waypoint/discussions/161)): named snapshots of the network/mode subset of the store; atomic one-click switch; import/export as JSON files (secrets scrubbed, hardware fingerprint attached; signing is a follow-up). Identity and calibration are never captured.
- **Migration** ([RFC-0007](https://github.com/KN4OQW/waypoint/discussions/162)): one-time import from a mounted Pi-Star/WPSD card or uploaded config files; reuses the seed-path INI mapper, previews a report of unmappable features, then bulk-writes the store transactionally.

### Supervisor
- Owns systemd units for stack daemons; derives desired-state from the config store.
- Subscribes to the MQTT bus for liveness/status, and **asks** when listening is not enough; enforces reconnect policies for DMR masters and DAPNET, papering over upstream gaps the daemons cannot recover from themselves — DMRGateway resolves its master's address once in the network's constructor and re-opens the socket without re-resolving (MMDVM-Host #682), and DAPNETGateway's connection dies on an IP change without its read ever returning an error (DAPNETGateway #10). Each supervised **attachment** is derived from the config store, judged on three independent signals (the systemd unit state, Waypoint's own probe of the endpoint the renderer gave that daemon, and the daemon's own account of the link — announced on its status plane, and queried directly via `[Remote Commands]`, because a daemon retrying a lost connection announces only that an attempt is in progress), and remediated by restarting its unit — gated on the node's own connectivity, deferred while a transmission is on the air, rate-limited per unit and globally, and reset only after a *sustained* recovery. Every action lands in the event log. APRS-IS (APRSGateway #1) joins when waypoint-stack packages that daemon (#5); there is no APRS-IS address in the store until then.
- Publishes normalized status to `waypoint/status/#` topics — Home Assistant-friendly. The status pipeline ([RFC-0008](https://github.com/KN4OQW/waypoint/discussions/163)) folds the structured event stream into one self-healing status value (a stranded TX expires on a watchdog; gateway liveness is probed, not inferred), served at `GET /api/status`, over a WebSocket, and republished retained — no log scraping. Home Assistant MQTT Discovery ([RFC-0011](https://github.com/KN4OQW/waypoint/discussions/166)) makes those topics zero-YAML entities under one device, with availability via an MQTT Last-Will. Topic scheme: [docs/mqtt-topics.md](mqtt-topics.md).

### Hardware ops
- **Board detection** ([RFC-0020](https://github.com/KN4OQW/waypoint/discussions/175)): Waypoint asks the modem what it is — one `GET_VERSION` frame — and parses the identity string it answers with (board family, firmware, reference oscillator, radio count, capability bits). The sweep is confined to ports that could plausibly be a modem and excludes ones the operator has claimed; it refuses to take the port from a running MMDVM-Host unless authorised, and always gives it back. What the modem said (`hardware_state`, machine-written) is kept separate from what the operator configured (`modem.board`/`tcxo_hz`), with one explicit adopt step between them. The GPIO UART's availability on stock Raspberry Pi OS is diagnosed and repairable, because a hat there is electrically fine and completely mute.
- Firmware flashing as an API operation with progress streaming ([RFC-0019](https://github.com/KN4OQW/waypoint/discussions/174)): the STM32 ROM bootloader (AN3155) over the GPIO UART with BOOT0/nRST driven from `/dev/gpiochip*` (sysfs fallback reads the chip's base rather than assuming it, so kernel ≥6.6's base-512 move is a non-event), and the Maple DFU bootloader over usbfs for the USB boards — both implemented in Go, no `stm32flash`/`dfu-util` in the image. Firmware comes from a pinned, CI-built, minisign-signed catalog matched against what detection read off the wire, never from a third party's directory layout. DVMega (`avrdude`) is a separate engine (#26).
- Calibration ([RFC-0021](https://github.com/KN4OQW/waypoint/discussions/177)): a guided RX/TX offset sweep with a live BER readout, measured by the node itself against the operator's radio. **Native Go, not MMDVMCal** — the modem computes no BER, so a subprocess would buy one function and charge a pty, a screen-scrape and an image dependency for it; the FEC arithmetic is ported and none of its tables are. Repeater level/invert workflow included, unverified on hardware.

### API
- REST for config/actions (OpenAPI-documented), WebSocket for event streams.
- The bundled dashboard is a client of the public API with no private endpoints — third-party displays and apps are first-class by construction.
- AuthN: first-boot device claim sets the admin credential; session cookies + token auth for API clients; **HTTPS by default** ([RFC-0012](https://github.com/KN4OQW/waypoint/discussions/167)) — a per-device self-signed cert minted on first start (or Let's Encrypt for a public hostname), the session cookie's `Secure` flag auto-on under TLS, and an HTTP→HTTPS redirect. See [docs/tls.md](tls.md).

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
