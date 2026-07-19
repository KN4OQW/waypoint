# RFC-0002: Security Posture — First-Boot Claim, Sessions, Reset

- Status: **proposed**
- Author: KN4OQW
- Comment window: 14 days from PR open
- Implements requirements: #10 (first-boot device claim — no default credentials)

## Summary

A Waypoint device ships with **no admin credential**. On first boot it serves exactly one interactive surface — a claim page — and refuses every other route until someone sets an admin username and password. After claim, all configuration and activity surfaces require an authenticated session. Recovery from a lost credential requires **OS-level or physical authority** and always returns the device to the unclaimed state; it never grants access by itself.

This RFC fixes the claim state machine and its route allowlists, credential storage, the session model, brute-force damping, the reset procedure, the TLS story, and the release-blocking test contract. It also closes RFC-0001's open question 2 (secrets at rest). It contains **no implementation** — the daemon and UI changes are follow-up PRs.

## Motivation

Hotspots are the amateur-radio device most likely to be exposed to the internet, and the incumbent platforms shipped with well-known default credentials for years. The results are on the public record:

- **BRARA, 2019** — an internet-exposed Pi-Star unit running default credentials was demonstrated compromised **in under two minutes**. Default `pi-star`/`raspberry` (and the underlying `pi`/`raspberry` OS account) are not secrets; they are published in every setup guide.
- **RadioReference thread 496832** — a WPSD user found the configuration dashboard **wide open on the LAN**, no authentication in front of it at all. Anyone on the network could read and rewrite the config.

The failure is structural: the config UI is reachable *before* anyone has established who owns the device. A default credential is a shared secret with the entire internet, and "no auth on the LAN" assumes a trust boundary that home networks do not provide (a compromised IoT device, a guest, a port-forward someone forgot).

SECURITY.md already states the design commitment this RFC implements:

> - No default credentials; first-boot device claim
> - HTTPS by default

There is no `pi-star`/`raspberry` equivalent at any point in a Waypoint device's life, and no config surface is served to an unauthenticated caller once the device is claimed. This document specifies exactly how that commitment is enforced.

## Design

### Claim state machine

A device is in exactly one of two states, derived from the store (RFC-0001's `meta` table): **unclaimed** or **claimed**. The HTTP server consults this state on every request and serves a strict route allowlist per state; everything not on the allowlist returns **403 Forbidden**.

#### Unclaimed

The device is unclaimed when **no admin credential exists** and `meta.claimed_at` is null. In this state the HTTP server serves **only**:

- the claim page and its static assets;
- `POST /api/claim`;
- `GET /api/health`.

**Every other route returns 403** — including the config API, the log/MQTT tails, and, explicitly, the **`GET /api/events` SSE stream**. The event stream carries live callsign and talkgroup activity; serving it pre-claim would leak the operator's on-air identity and traffic to any unauthenticated caller, which is precisely the RadioReference 496832 exposure. It is behind the wall from the first boot.

`GET /api/health` is intentionally unauthenticated in both states: it returns liveness and the claim state only (a boolean and a schema/version stamp), never configuration or activity. It exists so provisioning tooling and the claim page itself can discover whether a device still needs claiming.

#### Claiming

`POST /api/claim` accepts `{username, password}`. **The first successful claim wins, atomically.** The handler performs the credential write and the `meta.claimed_at` stamp in a **single store transaction**; a concurrent second claim that loses the race observes the already-set state and returns **409 Conflict**. There is no window in which two callers can both believe they own the device. A successful claim issues a session (see below) so the claimer is logged in immediately, without a second round-trip through the login page.

Password strength policy (minimum length, rejection of the most-common passwords) is applied at claim time; the exact policy is a UI/validation detail, not an architectural one, and is out of scope here beyond "there is one."

#### Claimed

The device is claimed when an admin credential exists and `meta.claimed_at` is set. In this state **all configuration and activity surfaces require an authenticated session.** The pre-authentication allowlist shrinks to:

- the login page and its static assets;
- `POST /api/session` (log in — establish a session);
- `GET /api/health`.

Every other route — the entire `/api/config/*` surface from RFC-0001, `/api/events`, profile management, hardware operations, the log tails — requires a valid session cookie or it returns 403. The claim page and `POST /api/claim` are **not** served once claimed; claiming again requires first returning to the unclaimed state via the reset procedure.

#### Acceptance criteria (restated from issue #10)

- A fresh install exposes **zero authenticated surfaces before claim** — the only reachable routes are the claim page, `POST /api/claim`, and `GET /api/health`, none of which read or write configuration or activity.
- **Config requires auth by default afterward** — once claimed, no config surface is served to an unauthenticated caller.

### Credential storage

- **A single admin account.** Waypoint is a single-operator appliance; multi-user RBAC is a non-goal for Phase 1 and is not designed here. One credential, one owner.
- Passwords are hashed with **argon2id** (`golang.org/x/crypto/argon2`), parameters:
  - `time = 1` (iterations),
  - `memory = 64 MiB`,
  - `threads = 4`,
  - **16-byte** cryptographically-random salt,
  - **32-byte** derived key.
- **The parameters are stored alongside the hash** (as an encoded parameter block, salt, and digest). This is deliberate: it lets a future release raise the cost parameters — or migrate the KDF entirely — and rehash on next successful login, **without a breaking migration**. A hash written under old parameters remains verifiable; the record carries everything needed to check it.
- **Auth material never touches the config surface.** The admin credential and session records live in **dedicated store tables**, not in the `settings` key tree. They are never exposed through any `/api/config` section, never appear in the config view or the generated-INI preview, and are **never part of a profile**. RFC-0001 already excludes auth from profile namespaces — *"Keys outside the profile's namespaces (device identity, auth, hardware calibration) are never part of a profile"* — and this RFC depends on that exclusion: exporting or importing a profile can neither leak nor overwrite the admin credential.

### Sessions

- Sessions are **server-side**, stored in a `sessions` table in `config.db`:
  - `id` — a **256-bit random token**, stored **hashed at rest** (the raw token exists only in the client cookie; a store compromise does not hand over live sessions);
  - `created_at`, `expires_at`, `last_seen`.
- The session cookie is **`HttpOnly`**, **`SameSite=Lax`**, and **`Secure` once TLS lands** (see below). `HttpOnly` keeps the token out of reach of any injected script; `SameSite=Lax` blunts CSRF against state-changing routes.
- **Idle expiry** defaults to **7 days**: a session whose `last_seen` is older than the idle window is invalid and is swept. Activity refreshes `last_seen`.
- **Explicit logout** is `DELETE /api/session`, which deletes the server-side record — logging out actually revokes the session, it does not merely drop the cookie.
- **Sessions survive a daemon restart** (they are in the store, not in process memory) — restarting `waypointd` for an update does not log the operator out.
- **Reset-claim revokes all sessions** — see below. Returning the device to the unclaimed state invalidates every outstanding session unconditionally.

### Brute-force damping

Two mechanisms, both intentionally modest:

- a **fixed small delay** applied to every *failed* login, so an attacker cannot pipeline high-rate guesses against the argon2id verifier;
- a **per-source failure counter with backoff** — repeated failures from a source progressively increase the delay / temporarily refuse attempts.

**This is damping, not a WAF.** It raises the cost of online guessing against a single device; it is not rate-limiting infrastructure, it does not defend against a distributed attacker, and it is not a substitute for a strong admin password (which the claim-time policy exists to encourage). Stated plainly so no one mistakes it for more than it is. The primary defense against credential attacks is that there is no default credential to guess and no config surface exposed before one is set.

### Reset procedure

There are **exactly two** reset paths, and **both require OS-level or physical authority**. Neither grants access on its own; both return the device to the unclaimed (claim-mode) state, from which a new owner must claim it afresh.

**a. `waypointd reset-claim` subcommand.** Run from a local shell (SSH or serial console) by someone who already has OS-level access to the device. It wipes the admin credential, revokes all sessions, and clears `meta.claimed_at`. Having a shell on the box is itself the authorization; the subcommand simply performs the reset cleanly rather than leaving the operator to hand-edit the store.

**b. Boot-partition marker file.** At daemon startup, `waypointd` checks for a reset marker at **both** `/boot/waypoint-reset` **and** `/boot/firmware/waypoint-reset` — Raspberry Pi OS **Bookworm moved the boot mount** from `/boot` to `/boot/firmware`, and Waypoint targets both, so it checks both locations. If the marker is present, the daemon:

1. wipes the admin credential,
2. revokes all sessions,
3. clears `meta.claimed_at`,
4. **deletes the marker**, and
5. **logs the reset loudly** (a prominent, unmistakable log line — this is a security-relevant event and must be auditable).

Placing the marker requires **root on the running system or removing the SD card** and writing to the boot partition on another machine. The subsequent **power cycle** is what triggers the daemon to consume the marker and complete the reset — the file alone does nothing until the device restarts. This is the recovery path for the operator who has lost the credential but has physical possession of the device.

Both paths land the device back in claim mode. Recovery is *return the device to me to re-own*, never *let me in*.

**SSH posture.** Waypoint **does not create OS accounts and does not touch `sshd`.** It manages its own application credential and nothing else. Since Raspberry Pi OS **Bullseye** there is no default `pi`/`raspberry` account — the OS account is created at imaging time (Raspberry Pi Imager) by the person flashing the card. That imaging-time account is the operator's own OS credential and their recovery credential for path (a); Waypoint neither knows it nor depends on it, and does not weaken it. Hardening `sshd` (keys-only, etc.) is the operator's call and outside Waypoint's scope.

### TLS

- A **self-signed certificate** is generated **at claim time** and Waypoint serves **HTTPS by default**, per the SECURITY.md commitment.
- **ACME** (Let's Encrypt and friends) is **optional**, for operators who front the device with a resolvable name.
- The cookie **`Secure` flag flips on when TLS ships** — it is gated on TLS being present so that a pre-TLS build does not set a flag that would make the cookie unusable over plain HTTP during bring-up.
- The certificate lifecycle, the self-signed-cert trust UX (browsers will warn on a self-signed cert), and ACME provisioning are **design-stated here, implementation deferred to their own PR.** This RFC commits to *HTTPS by default, self-signed at claim time, ACME optional*; it does not specify the cert-rotation mechanics.

### Secrets at rest

*(This section closes RFC-0001 open question 2, which deferred secrets-at-rest to this document.)*

RFC-0001 handles secrets **in transit and in exports** — `sensitive: true` schema keys are excluded from profile exports and redacted in diffs and logs — and explicitly deferred secrets **at rest** to this RFC.

**Phase 1 position:** configuration secrets (DMR/BrandMeister passwords, APRS-IS passcodes, DAPNET credentials, etc.) **remain plaintext JSON in `config.db`**, protected by **file permissions**: the database is mode **`0600`** and owned by the dedicated **`waypointd`** service user. Reading a secret requires already being that user or root — i.e. already having OS-level control of the device, at which point the secrets are the least of the operator's problems.

**OS keyring / at-rest encryption is explicitly deferred**, and why: on a headless appliance with no operator present at boot, any key used to decrypt the store must itself be recoverable by the daemon unattended — which means the key lives on the same disk as the ciphertext, or in a TPM the launch-tier target hardware (Pi Zero/3/4) does not have. Encrypting `config.db` with a key stored beside it raises the bar against *casual* disk inspection but not against anyone with root, while adding real key-management and recovery complexity (a lost key bricks the config). The honest Phase 1 tradeoff is `0600` + a dedicated service user.

This is an **accepted, revisitable risk**, stated plainly. It is revisited when the Phase 3 image (read-only root, separate config partition) lands, where full-partition encryption keyed to hardware becomes a coherent option rather than a half-measure.

## The security contract (test harness)

CI enforces, as **release-blocking** tests, at minimum:

1. **Pre-claim route matrix.** For an unclaimed device, assert the *only* routes that do not return 403 are the claim page assets, `POST /api/claim`, and `GET /api/health` — and assert explicitly that `GET /api/events` and a representative `/api/config/*` route return 403. The matrix is exhaustive over the registered route table, so a newly added route defaults to *denied* and fails the test until it is deliberately placed on an allowlist.
2. **Concurrent-claim race.** Fire two `POST /api/claim` requests concurrently at a fresh device; assert **exactly one** succeeds and issues a session, the other receives **409 Conflict**, and the store ends with a single admin credential and one `claimed_at` value.
3. **Hash-only at rest.** Assert the admin password never appears in the store in any recoverable form — the `password` column holds an argon2id record (params + salt + digest), and the session `id` column holds a hash, not the raw token. A grep of the raw DB for a known test password / token finds nothing.
4. **Session restart survival.** Establish a session, restart `waypointd`, assert the same cookie still authenticates; then advance past idle expiry and assert it no longer does; then `DELETE /api/session` and assert the record is gone and the cookie is rejected.
5. **Reset-to-claim round trip.** From a claimed device with a live session: (a) run `reset-claim`, and separately (b) plant a `/boot/firmware/waypoint-reset` marker and restart. For each path assert the admin credential is wiped, **all sessions are revoked** (the previously-live cookie now 403s), `claimed_at` is null, the device serves the claim page again, the marker file is deleted (path b), and the reset was logged.

## Alternatives considered

- **A hardware reset button.** The launch-tier target hardware (MMDVM_HS hats, ZUMspot, JumboSpot, Nano boards) **has no such button**, and Waypoint is not a commercial product shipping in custom enclosures where one could be added. A GPIO-jumper convention was considered and rejected as fragile and board-specific. The boot-partition marker is the hardware-authority path that works on every target board.
- **An RF / DTMF / signaling-metadata "knock"** — resetting the device by transmitting a magic source ID, DTMF sequence, or crafted signaling pattern. Rejected on several independent grounds, any one of which is disqualifying:
  - **Source IDs are user-programmable** — a caller's DMR ID / callsign is set in the radio, trivially spoofable, so a "knock" keyed to one is an authentication factor anyone can forge.
  - **DTMF decode is unavailable** without a vocoder in most digital modes — the host stack does not have the audio to decode DTMF in DMR/YSF/P25/NXDN, so the mechanism wouldn't even function across the modes Waypoint targets.
  - **The pattern leaks upstream** — anything transmitted flows out through the linked reflector/gateway to every other connected node, broadcasting the reset secret network-wide the moment it is used.
  - **It only works while the stack is healthy** — a knock is handled by the very software you might be trying to recover; if the daemon or modem is wedged, the knock does nothing. It could **never replace** the two OS/physical paths, only sit alongside them.
  Adding it would mean a **standing, always-listening reset surface** reachable over the air by a spoofable identity, for no capability the two primary paths don't already cover better. Rejected as an unnecessary standing surface.

## Open questions

1. Cost-parameter escalation policy: argon2id parameters are stored per-hash so they can be raised without a breaking migration (by design, above) — the open question is *when* to bump them and whether to rehash opportunistically on login vs. on an explicit admin action. Leaning opportunistic-on-login.
2. Self-signed-certificate trust UX: how much to invest in easing the first-visit browser warning (a downloadable CA, a documented fingerprint to verify) before ACME is available. Deferred to the TLS implementation PR.
3. At-rest encryption revisited at Phase 3: whether the separate config partition on the purpose-built image should be encrypted, and if so keyed to what, given the launch tier lacks a TPM. Tracked here so the Phase 1 plaintext-with-`0600` decision is explicitly a Phase 1 decision, not a permanent one.
