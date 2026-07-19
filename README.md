# Waypoint

**An open, community-governed host system and UI for MMDVM digital voice hotspots.**

Waypoint is a ground-up hotspot host system for amateur radio digital voice — DMR, YSF, D-Star, P25, NXDN, POCSAG and beyond — built on the public [g4klx](https://github.com/g4klx) GPL stack and the new MQTT data plane of [MMDVM-Host](https://github.com/g4klx/MMDVM-Host).

It exists because the amateur community deserves a hotspot platform that is:

- **Lossless** — configuration is a schema-versioned store; applying a change never destroys another setting. Gateway INI files are generated artifacts, not the source of truth.
- **Honest about status** — the dashboard consumes structured MQTT/JSON events from the host stack. No log scraping, no "shows Not Linked while linked."
- **Secure by default** — no default credentials (first boot claims the device), HTTPS out of the box, a real security-reporting channel.
- **Safe to update** — tagged releases with changelogs; updates are atomic or they don't happen. Your local customizations live in a documented override layer that survives every update.
- **Usable from your phone** — responsive, dark-mode-default, screen-reader accessible.
- **Governed to outlive any one person** — public repos, public CI, a review SLA, an RFC process, and a written no-telemetry policy. See [GOVERNANCE.md](GOVERNANCE.md).

## Status

**Active development — the config core and most of the mode stack are in place.** The [requirements register](https://github.com/KN4OQW/waypoint/issues?q=is%3Aissue+label%3Atype%3Arequirement) is imported (every item carries provenance back to the community complaint or upstream issue that motivated it), the architecture is documented in [docs/architecture.md](docs/architecture.md), and the stack builds in public CI. Everything is public from the first commit.

What's built today:

- **Config store + generators** for all eight modes (DMR, YSF, D-Star, P25, NXDN, M17, POCSAG, FM) and the five cross-mode bridges (YSF2DMR, DMR2YSF, YSF2NXDN, DMR2NXDN, NXDN2DMR). A schema-versioned store compiles `MMDVM-Host.ini` plus each gateway's INI as deterministic outputs; see [docs/config-coverage.md](docs/config-coverage.md).
- **Settings UI** with per-mode tabs (DMR mirrors the Pi-Star/WPSD field set), plus config import.
- **Override layer** ([RFC-0005](docs/rfcs/0005-override-layer.md)) — hand-edited `overrides.d/<daemon>.d/*.conf` drop-ins merge last into every generated INI (section/key merge, `!unset`, lexical precedence), and `prepend.d`/`append.d` hooks preserve local host entries across refreshes. Overrides survive every update and are surfaced read-only in the Expert tab. Closes the Pi-Star `P25HostsLocal` grievance ([#2](https://github.com/KN4OQW/waypoint/issues/2)).
- **Connection profiles** ([RFC-0006](docs/rfcs/0006-connection-profiles.md)) — save the current mode/network setup as a named profile and switch to another in one click; export/import as a file (secrets scrubbed, hardware fingerprint attached). Callsign, frequencies and calibration are never in a profile, so a switch can't change identity or detune the radio. The openSpot feature both incumbents lack ([#3](https://github.com/KN4OQW/waypoint/issues/3)).
- **Migration from Pi-Star / WPSD** ([RFC-0007](docs/rfcs/0007-config-import.md)) — point Waypoint at a mounted incumbent card (or upload its config files), scan for a preview and a report of anything that won't carry over, then import the whole setup into the store. Reuses the losslessness-tested INI mapper; verified against stock Pi-Star 4.2.1 and current WPSD configs ([#4](https://github.com/KN4OQW/waypoint/issues/4)).
- **Gateway daemons** pinned and reproducibly built for four architectures (amd64, arm64, armhf, armv6hf) in [waypoint-stack](https://github.com/KN4OQW/waypoint-stack): MMDVM-Host (**forked to restore M17**, which upstream removed), DMRGateway, YSFGateway/DGIdGateway, P25Gateway, NXDNGateway, M17Gateway, and DStarGateway.

Still ahead: the DAPNETGateway (POCSAG) and MMDVM_CM (cross-mode) daemon builds, host/network configuration ([#32]), first-boot device claim + TLS, the updates lifecycle, and end-to-end bench validation. Not yet a turnkey release — but no longer a skeleton.

[#32]: https://github.com/KN4OQW/waypoint/issues/32

Reference bench hardware: MMDVM_HS_Dual_Hat (STM32F103, dual ADF7021) on a Raspberry Pi 3, plus full-size MMDVM (STM32F4/F7) targets.

## Architecture (short version)

```
Radio (MMDVM_HS / MMDVM firmware)
  ↕ serial
g4klx host stack (MMDVM-Host + mode gateways, unmodified)
  ↕ MQTT (mosquitto, JSON events)
waypointd — Go core daemon
  · schema-versioned config store (SQLite); INIs are compiled outputs
  · service supervisor with reconnect policies
  · hardware ops: board detect, firmware flash, guided calibration
  · REST + WebSocket API (the dashboard is just the first client)
  ↕ HTTPS
Web UI — responsive SPA, embedded in the daemon binary
```

Full detail: [docs/architecture.md](docs/architecture.md).

## Contributing

Start with [CONTRIBUTING.md](CONTRIBUTING.md). The short version: every PR gets a human response within 14 days — even if it's "no, and here's why." Requirement issues labeled `good-first-issue` are curated for newcomers. Feature-scale changes go through a lightweight [RFC process](GOVERNANCE.md#rfcs).

This project also runs AI-assisted triage (Claude): new issues and PRs get an initial technical read within minutes, and you can mention `@claude` in any thread for interactive help. AI never merges; maintainers do.

## License

GPL-3.0. The bundled g4klx components are GPL-2.0-or-later. Documentation is CC-BY-SA-4.0.

---

*Waypoint is an independent community project. It reuses no code from Pi-Star or WPSD and is not affiliated with either; we're grateful to both for years of service to the hobby, and to Jonathan Naylor G4KLX, whose stack makes all of this possible.*
