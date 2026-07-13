# Security Policy

Hotspots sit on home networks, carry amateur-license identities, and are too often exposed to the internet. Waypoint treats security reports as first-class work.

## Reporting a vulnerability

**Please do not open a public issue for a suspected vulnerability.**

Use GitHub's private vulnerability reporting: **Security → Report a vulnerability** on this repository. You'll get an acknowledgment within **72 hours** and a status update at least weekly until resolution.

We practice coordinated disclosure: we'll work with you on a fix and timeline (default 90 days), credit you in the advisory (or keep you anonymous, your choice), and publish a GitHub Security Advisory with a CVE where warranted.

## Scope

- `waypointd` (core daemon, API, web UI)
- Waypoint-built distribution artifacts (packages, images)
- Build/release pipeline integrity

Vulnerabilities in the upstream g4klx components should go to their repositories, but if you're unsure — report here and we'll route it with you, privately.

## Design commitments

- No default credentials; first-boot device claim
- HTTPS by default
- Signed releases; checksummed database downloads
- No telemetry (see GOVERNANCE.md)
