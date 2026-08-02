# Waypoint

**An open, community-governed host system and UI for MMDVM digital voice hotspots.**

Waypoint is a ground-up hotspot host system for amateur radio digital voice — DMR, YSF, D-Star, P25, NXDN, POCSAG and beyond — built on the public [g4klx](https://github.com/g4klx) GPL stack and the new MQTT data plane of [MMDVM-Host](https://github.com/g4klx/MMDVM-Host).

It exists because the amateur community deserves a hotspot platform that is:

- **Lossless** — configuration is a schema-versioned store; applying a change never destroys another setting. Gateway INI files are generated artifacts, not the source of truth.
- **Honest about status** — the dashboard consumes structured MQTT/JSON events from the host stack. No log scraping, no "shows Not Linked while linked."
- **Secure by default** — no default credentials (first boot claims the device), HTTPS out of the box, a real security-reporting channel.
- **Safe to update** — tagged releases with changelogs; updates are atomic or they don't happen. Your local customizations live in a documented override layer that survives every update.
- **Usable from your phone** — responsive to 360 px with a dark-default and light theme, and a first-run claim/login you can complete on a phone; screen-reader accessible.
- **Governed to outlive any one person** — public repos, public CI, a review SLA, a written no-telemetry policy, and an RFC process that activates at v1. See [GOVERNANCE.md](GOVERNANCE.md).

## Quick start

The easiest way to run Waypoint is the ready-to-flash SD-card image (Raspberry Pi
OS Lite Bookworm + the full stack):

1. Download `waypoint-<version>-bookworm-<arch>.img.xz` and `SHA256SUMS` from the
   [latest release](https://github.com/KN4OQW/waypoint/releases/latest), and
   verify them: `sha256sum -c SHA256SUMS` and `minisign -Vm SHA256SUMS -P <release key>`.
2. Flash with **Raspberry Pi Imager** (Use custom → the `.img.xz`). In Imager's
   advanced options set your **Wi-Fi and a username/password** — the image ships
   no default login.
3. Boot the Pi, browse to **`https://<address>/`**, and complete the one-time
   claim to set your Waypoint admin account.

Pick **arm64** on a Pi 3/4, **armhf** for a Pi Zero 2 W / 2 / 3 (32-bit). Pi Zero
W and Pi 1 are not supported. Full flashing, verification, and first-boot walk-through:
**[docs/image.md](docs/image.md)**.

**Locked out?** A Waypoint password cannot be recovered — it is stored only as a
hash — but you can take a node back with a shell on it (`sudo waypointd
reset-claim`) or with its SD card in a reader. Both routes, and what each one
costs you, are in **[docs/recovery.md](docs/recovery.md)**. The same instructions
are on the node's own login screen, which is where you will be when you need
them.

## Status

**Active development — the config core, the full mode stack, cross-mode buses, LAN peering, host networking, and modem detection and firmware flashing are in place, with per-mode, two-node and on-hardware flashing runs validated on the bench.** The first flashable SD-card image ships as **`v1-initialimg`** — an initial image built end-to-end by public CI (base Raspberry Pi OS Lite + the signed stack from the [waypoint-stack](https://github.com/KN4OQW/waypoint-stack) apt repo + the signed `waypointd`) for both arm64 and armhf. The [requirements register](https://github.com/KN4OQW/waypoint/issues?q=is%3Aissue+label%3Atype%3Arequirement) is imported (every item carries provenance back to the community complaint or upstream issue that motivated it), the architecture is documented in [docs/architecture.md](docs/architecture.md), and both the stack and the image build in public CI. Everything is public from the first commit.

What's built today:

- **Config store + generators** for all eight modes (DMR, YSF, D-Star, P25, NXDN, M17, POCSAG, FM) and the operator-defined cross-mode **buses** that replaced the old per-pair bridge grid. A schema-versioned store compiles `MMDVM-Host.ini` plus each gateway's INI as deterministic outputs; see [docs/config-coverage.md](docs/config-coverage.md).
- **Settings UI** with per-mode tabs (DMR mirrors the Pi-Star/WPSD field set), plus config import.
- **Cross-mode buses** ([RFC-0003](https://github.com/KN4OQW/waypoint/discussions/158)) — a *bus* is a named object the operator creates and attaches modes to (DMR → Bus A, YSF → Bus A); voice entering from any attached mode is transcoded and emitted to every other, with IDs, callsigns and talkgroups translated per destination. One bus replaces the old grid of pairwise `YSF2DMR`/`DMR2YSF`/… daemons — the operator names the endpoints and `waypoint-bus@<id>` is generated. **Addendum A** ([loopback hand-off](https://github.com/KN4OQW/waypoint/discussions/157)) lets a bus attach to a live mode *without displacing it*: DMR multiplexes through DMRGateway (RF DMR and every upstream network stay up), while YSF/NXDN hand their gateway to the bus for the attachment's duration. Frame layer, hub daemon, UI, and per-mode bench runs are done ([docs/on-hardware-report.md](docs/on-hardware-report.md)).
- **LAN bus peering** ([RFC-0016](https://github.com/KN4OQW/waypoint/discussions/171)) — a bus can span more than one Waypoint node on the same LAN. One node owns it; others join over a dedicated, mutually-authenticated (mTLS) point-to-point link and contribute their local modes as remote attachments. A keyed-up transmission on the garage YSF node is reframed locally, streamed to the shack node, and emitted on DMR there — never touching a reflector or the WAN. Pairing is mDNS discovery plus a short-code handshake with revocation; media rides dedicated TCP, not the broker (a latency call measured on the bench, not estimated — §Design 1). LAN-only by design, and validated on a two-node bench pair — resolving the long-open [#65](https://github.com/KN4OQW/waypoint/issues/65) "can buses span nodes?" question.
- **Persistent event history** ([RFC-0004](https://github.com/KN4OQW/waypoint/discussions/159)) — every event the daemon learns is persisted to a separate `events.db`, and `GET /api/history` serves it, so last-heard, networks, and the event log render the same for any browser regardless of when it connected or whether the daemon has restarted. Retention is an operator setting (default 7 days), pruned nightly, in a new Station Settings tab ([#68](https://github.com/KN4OQW/waypoint/issues/68)).
- **Override layer** ([RFC-0005](https://github.com/KN4OQW/waypoint/discussions/160)) — hand-edited `overrides.d/<daemon>.d/*.conf` drop-ins merge last into every generated INI (section/key merge, `!unset`, lexical precedence), and `prepend.d`/`append.d` hooks preserve local host entries across refreshes. Overrides survive every update and are surfaced read-only in the Expert tab. Closes the Pi-Star `P25HostsLocal` grievance ([#2](https://github.com/KN4OQW/waypoint/issues/2)).
- **Connection profiles** ([RFC-0006](https://github.com/KN4OQW/waypoint/discussions/161)) — save the current mode/network setup as a named profile and switch to another in one click; export/import as a file (secrets scrubbed, hardware fingerprint attached). Callsign, frequencies and calibration are never in a profile, so a switch can't change identity or detune the radio. The openSpot feature both incumbents lack ([#3](https://github.com/KN4OQW/waypoint/issues/3)).
- **Migration from Pi-Star / WPSD** ([RFC-0007](https://github.com/KN4OQW/waypoint/discussions/162)) — point Waypoint at a mounted incumbent card (or upload its config files), scan for a preview and a report of anything that won't carry over, then import the whole setup into the store. Reuses the losslessness-tested INI mapper; verified against stock Pi-Star 4.2.1 and current WPSD configs ([#4](https://github.com/KN4OQW/waypoint/issues/4)).
- **MQTT-native status pipeline** ([RFC-0008](https://github.com/KN4OQW/waypoint/discussions/163)) — live status is folded, server-side, from the structured event stream into one authoritative value served at `GET /api/status`, streamed over a WebSocket (`/api/ws`), and republished onto retained `waypoint/status/#` topics for Home Assistant. **Self-healing**: a stranded transmission expires on a watchdog and a killed gateway shows down within a ~1 s supervisor probe, so the dashboard reflects truth instead of latching a stale state — no log scraping, ever ([#5](https://github.com/KN4OQW/waypoint/issues/5)).
- **Phone-usable UI** ([RFC-0009](https://github.com/KN4OQW/waypoint/discussions/164)) — the dashboard and settings are responsive to 360 px, with a genuine **light theme** alongside the dark default (composing with the accent themes) and **real first-run claim/login screens** you can complete on a phone. Verified at 360 / 768 / 1280 px in both themes ([#6](https://github.com/KN4OQW/waypoint/issues/6)).
- **Inline talkgroup/reflector names** ([RFC-0010](https://github.com/KN4OQW/waypoint/discussions/165)) — a fetched **DMR talkgroup-name database** resolves TG numbers to names on the dashboard ("TG 3112 · Texas Statewide" in on-air, last-heard, and the event log), and the DMR routing picker is typeahead-searchable by name or number — the Pi-Star #9 ask, open since 2018 ([#8](https://github.com/KN4OQW/waypoint/issues/8)).
- **Home Assistant, zero YAML** ([RFC-0011](https://github.com/KN4OQW/waypoint/discussions/166)) — MQTT Discovery publishes retained config that points HA at the `waypoint/status/#` state topics, so a node's mode, active transmission, feed health, and per-gateway/network liveness appear as entities under one device automatically, with device availability via an MQTT Last-Will. The topic scheme is documented ([docs/mqtt-topics.md](docs/mqtt-topics.md)) ([#9](https://github.com/KN4OQW/waypoint/issues/9)).
- **HTTPS out of the box** ([RFC-0012](https://github.com/KN4OQW/waypoint/discussions/167)) — a per-device self-signed certificate is minted on first start (SANs cover the hostname and every local IP), TLS is served by default, the session cookie's `Secure` flag turns on automatically, and bare `http://` redirects to HTTPS — so the claim/login password never crosses the network in cleartext. One trust prompt, [documented](docs/tls.md); optional Let's Encrypt for public hostnames ([#11](https://github.com/KN4OQW/waypoint/issues/11)).
- **Signed releases + verified downloads** ([RFC-0013](https://github.com/KN4OQW/waypoint/discussions/168)) — a minisign (Ed25519) verifier gates both software and reference data: release artifacts are signed in CI and verified before applying (`waypointd -verify …`), and host/talkgroup-list refreshes verify a signature/checksum before replacing the cache. A tampered artifact or database file is rejected with a clear error. [Signing docs](docs/signing.md) ([#12](https://github.com/KN4OQW/waypoint/issues/12)).
- **Atomic updates with rollback** ([RFC-0014](https://github.com/KN4OQW/waypoint/discussions/169)) — an update *completes or the prior version boots*, never a half-installed brick. A signed manifest is verified, the release binary is staged and **atomically swapped** in with the old one kept as a rollback, then **health-gated**: if the new version does not come up healthy it is reverted automatically. A boot-time check reverts an update that was swapped but never confirmed (power pulled mid-update), closing the power-loss window. `waypointd -update-check` / `-update`, plus `/api/update/check|apply`. [Update docs](docs/updates.md) ([#13](https://github.com/KN4OQW/waypoint/issues/13)).
- **Tagged releases + visible versioning** ([RFC-0015](https://github.com/KN4OQW/waypoint/discussions/170)) — a release is a git semver tag, and `waypointd -version`, `/api/health`, the dashboard footer, and the GitHub release page all report it by construction. Pushing a `v*` tag builds and signs the per-arch binaries, generates a changelog from the merged PRs, and publishes a signed `update.json` for the updater to fetch. [Release docs](docs/releases.md) ([#14](https://github.com/KN4OQW/waypoint/issues/14)).
- **Host / OS networking** ([#32](https://github.com/KN4OQW/waypoint/issues/32)) — Wi-Fi and Ethernet/IPv4, plus hostname, timezone, NTP, and guarded VLAN, all configured from the UI. A network change is **confirm-or-revert**: a NetworkManager-native checkpoint restores the prior state if the new config would strand the box, so a bad setting can't lock you out. Hardware-validated.
- **Modem detection** ([#18](https://github.com/KN4OQW/waypoint/issues/18)) — Waypoint asks the modem what it is rather than inferring it from the port it turned up on. One three-byte version request, and the identity string that comes back carries board family, firmware version, reference oscillator and radio count. Ports the operator has claimed for something else are left alone — a Nextion display is a serial device on exactly the kind of port a sweep would otherwise walk into — and MMDVM-Host is never taken off the air without permission. Where the wire is genuinely ambiguous (several products ship the same firmware and answer with the same string) it says so and offers the choice instead of picking one. It also diagnoses the failure a stock Raspberry Pi OS creates, where a correctly fitted hat is electrically fine and completely mute because Bluetooth owns the UART and a login console is sitting on it.
- **Firmware flashing, for the GPIO hats** ([RFC-0019](https://github.com/KN4OQW/waypoint/discussions/174)) — one button, no SSH, progress on screen. The STM32's ROM bootloader over the GPIO UART with BOOT0/nRST driven from the Pi's own lines, implemented in Go: no `stm32flash`, no `dfu-util`, nothing added to the image, and no second project's command-line shape acting as Waypoint's API. Images are built, signed and published by CI in [KN4OQW/MMDVM_HS](https://github.com/KN4OQW/MMDVM_HS) — a pinned, public fork, byte-for-byte reproducible — and matched to your board from what detection already read off the wire, so nobody is asked to name their board's reference oscillator. (Pi-Star asks; almost nobody was ever told; and getting it wrong doesn't fail loudly, it transmits off frequency.) **An interrupted flash is recoverable by retry** — the bootloader lives inside the STM32 and cannot be erased, so a half-written board is reachable exactly as it was. Validated on hardware including a flash deliberately killed mid-write: [docs/bench-flash-run-log.md](docs/bench-flash-run-log.md).
- **Guided calibration** ([RFC-0021](https://github.com/KN4OQW/waypoint/discussions/177)) — a hotspot that decodes nothing is almost always a reference oscillator a few hundred Hz off, and the state of the art for fixing it is a wiki page, an SSH session and a terminal program driven by single keystrokes. Waypoint makes it something the node does to itself: press start, key your radio when it asks, and watch the bit error rate fall as the node sweeps its own frequency and keeps the offset that won. The measurement is **native Go against the MMDVM protocol** — the modem computes no BER, so driving MMDVMCal would buy one function and charge a pty, a screen-scrape of human prose, and another binary in the image for it; the FEC arithmetic is ported and **none of its tables are** (the Golay code is perfect, so its decoder is enumerated at init, and the 4096-entry AMBE whitening table turns out to be a 16-bit LCG). The sweep advances on **frames, not seconds**, so you key in comfortable bursts and it pauses and resumes rather than scoring a frequency you were not transmitting through as a clean 0.00%. The curve is shown before anything is written, and applying is a separate act. Nothing automates this anywhere else ([#20](https://github.com/KN4OQW/waypoint/issues/20)).
- **Gateway daemons** pinned and reproducibly built for four architectures (amd64, arm64, armhf, armv6hf) in [waypoint-stack](https://github.com/KN4OQW/waypoint-stack): MMDVM-Host (**forked to restore M17**, which upstream removed), DMRGateway, YSFGateway/DGIdGateway, P25Gateway, NXDNGateway, M17Gateway, and DStarGateway.

Still ahead: the DAPNETGateway (POCSAG) build; the cross-**codec** bus path (only the AMBE+2 reframe envelope ships today, so DMR/YSF-DN/NXDN interoperate but a vocoder-crossing attachment does not yet); the full-size MMDVM ([#25](https://github.com/KN4OQW/waypoint/issues/25)) and DVMega ([#26](https://github.com/KN4OQW/waypoint/issues/26)) board tiers; and hardening the OS image — a read-only root with **A/B slots and automatic rollback** ([RFC-0017](https://github.com/KN4OQW/waypoint/discussions/172), design).

**Flashing USB stick boards** (ZUMspot USB, Nano hotSPOT, NanoDV USB, LoneStar USB) is designed but deliberately unbuilt ([RFC-0020](https://github.com/KN4OQW/waypoint/discussions/176)). They are a different population from the hats — no BOOT0 exposed to the host, reachable only through a DFU bootloader in their own flash — which makes them the only boards a flash can actually brick. Validating that needs one on a bench, and shipping an unvalidated flash path for the one class of hardware it can destroy is the wrong way round. CI already builds and signs their images, so it is host-side work whenever a board turns up.

Two standing caveats: peering stays **LAN-only by design** — no WAN/Internet mesh, no owner failover — and `v1-initialimg` is an **initial** image, flashable and complete but early. Treat it as a beta while it takes on hardware miles.

Reference bench hardware: MMDVM_HS_Dual_Hat (STM32F103, dual ADF7021) on a Raspberry Pi 3, running Waypoint's own [MMDVM_HS](https://github.com/KN4OQW/MMDVM_HS) firmware build (which adds M17 packet data), plus full-size MMDVM (STM32F4/F7) targets.

## Architecture (short version)

```
Radio (MMDVM_HS / MMDVM firmware)
  ↕ serial
g4klx host stack (MMDVM-Host + mode gateways, unmodified)
  ↕ MQTT (mosquitto, JSON events)
waypointd — Go core daemon
  · schema-versioned config store (SQLite); INIs are compiled outputs
  · service supervisor (mode gateways + per-bus hub daemons) with reconnect policies
  · hardware ops: board detect, firmware flash (GPIO hats), guided calibration
  · REST + WebSocket API (the dashboard is just the first client)
  ↕ HTTPS
Web UI — responsive SPA, embedded in the daemon binary
```

Full detail: [docs/architecture.md](docs/architecture.md).

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md). The short version: every PR gets a human response within 14 days — even if it's "no, and here's why." Requirement issues labeled `good-first-issue` are curated for newcomers. Feature-scale changes start as an issue — the [RFC process](GOVERNANCE.md#rfcs) is dormant until v1, because a comment period needs a community to comment.

This project also runs AI-assisted triage (Claude): new issues and PRs get an initial technical read within minutes, and you can mention `@claude` in any thread for interactive help. AI never merges; maintainers do.

## License

GPL-3.0. The bundled g4klx components are GPL-2.0-or-later. Documentation is CC-BY-SA-4.0.

---

*Waypoint is an independent community project. It reuses no code from Pi-Star or WPSD and is not affiliated with either; we're grateful to both for years of service to the hobby, and to Jonathan Naylor G4KLX, whose stack makes all of this possible.*
