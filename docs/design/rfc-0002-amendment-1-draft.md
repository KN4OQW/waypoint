# RFC-0002 Amendment 1: Multi-Account Authentication

- Status: **draft — for comment**
- Amends: [RFC-0002: Security Posture — First-Boot Claim, Sessions, Reset](https://github.com/KN4OQW/waypoint/discussions/156)
- Author: KN4OQW
- Comment window: 14 days from posting

## Summary

RFC-0002 specified **one credential, one owner** and said so deliberately: *"Waypoint is a single-operator appliance; multi-user RBAC is a non-goal for Phase 1 and is not designed here."* This amendment designs it.

It replaces the fixed-id `admin` row with an `accounts` table keyed to `phonebook(id)`, defines exactly three roles, and gives sessions an owner. **Every contract RFC-0002 fixed is preserved unchanged** — hash-only at rest, per-source damping, session semantics, and both reset paths. Nothing here relaxes the claim state machine, and nothing here touches the operating system.

This amendment contains **no implementation**. It is the design record; the daemon and UI changes are follow-up PRs.

## What does not change

Stated first, because an amendment to a security RFC is judged on what it leaves alone.

| RFC-0002 contract | Status under this amendment |
|---|---|
| No default credentials; first boot serves only the claim page, `POST /api/claim`, `GET /api/health` | **Unchanged.** The claim flow is untouched; it now writes the first `accounts` row instead of the fixed `admin` row. |
| First claim wins atomically; the loser gets 409 | **Unchanged.** |
| argon2id, `time=1`, `memory=64 MiB`, `threads=4`, 16-byte salt, 32-byte key, **parameters stored alongside the hash** | **Unchanged**, per account. The per-hash parameter block is what lets accounts created years apart coexist. |
| Auth material never reaches the config surface — dedicated tables, never a `settings` key, never in a profile | **Unchanged**, and now covers `accounts`. |
| Sessions: 256-bit token **hashed at rest**, `HttpOnly`, `SameSite=Lax`, `Secure` under TLS, 7-day idle expiry, `last_seen` refresh, `DELETE /api/session` revokes server-side, survives daemon restart | **Unchanged.** One column is added; no semantic is altered. |
| Brute-force damping: fixed delay on failure + **per-source** counter with backoff. Damping, not a WAF. | **Unchanged — deliberately per-source, not per-account.** See below. |
| Two reset paths, both requiring OS-level or physical authority, both returning the device to claim mode | **Unchanged.** |
| **No OS accounts. `sshd` is not touched.** | **Unchanged.** See "Boundary" below. |
| Secrets at rest: config secrets plaintext in `config.db`, mode `0600`, `waypointd` service user | **Unchanged.** Out of scope here. |

### Why damping stays per-source

Preserving this verbatim is not just compliance. Per-**account** damping would let anyone who knows a callsign lock a legitimate operator out of their own node by guessing at their account — turning a defence into a denial-of-service primitive. Per-source damping has no such property. The contract was right; multi-account makes it more right, not less.

## Motivation

Two things changed since RFC-0002 was written.

**The single credential is now shared in practice.** Clubs and repeater groups run these nodes. When three people need to reach the dashboard, the single admin password gets passed around — and a shared password cannot be revoked for one person, cannot be audited, and survives every departure. That is the failure RFC-0002 set out to prevent, arriving by a different door.

**The identity anchor already exists.** The phonebook (merged) is a table of operators keyed by a surrogate `id`, and its design note says why: *"later units hang password hashes, roles, and notification preferences off `phonebook(id)`."* `admin.role` was likewise added ahead of need, with the comment *"no code should branch on it until multi-user is actually designed."* This is that design.

## Design

### The `accounts` table

```sql
CREATE TABLE IF NOT EXISTS accounts (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  phonebook_id  INTEGER REFERENCES phonebook(id) ON DELETE RESTRICT,
  username      TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,          -- argon2id salt + digest; never a plaintext
  params        TEXT NOT NULL,          -- the per-hash parameter block (RFC-0002)
  role          TEXT NOT NULL CHECK (role IN ('admin','operator','viewer')),
  must_rotate   INTEGER NOT NULL DEFAULT 0,
  created_at    TEXT NOT NULL,          -- RFC-3339 UTC
  updated_at    TEXT NOT NULL
);
```

Four choices worth stating:

**`phonebook_id` is nullable.** A node claimed before this amendment has an admin with no phonebook row, and the migration must not invent one — a fabricated callsign is junk identity data in the table the dashboard reads. The column is null until an admin links it. New accounts created through the UI link at creation.

**`ON DELETE RESTRICT`, not `CASCADE`.** Deleting a phonebook entry must not silently delete somebody's login. The phonebook `DELETE` handler will begin returning `409 Conflict` when an account depends on the row, naming the account. *This is a visible behaviour change to an existing endpoint and is called out in the test contract.*

**`username` stays its own column** rather than being derived from the linked callsign. `POST /api/claim` accepts `{username, password}` and RFC-0002 fixed that; deriving the login name from the phonebook would change the claim contract. The UI may *default* the field to the linked callsign; the stored value is independent.

**`AUTOINCREMENT`**, for the same reason the phonebook uses it: sessions reference `accounts(id)`, and a reused rowid would silently re-point a live session at a different person.

### Migrating the fixed-id `admin` row

One numbered step on the existing schema ladder (RFC-0001), in a single transaction:

1. Create `accounts`.
2. `INSERT INTO accounts (phonebook_id, username, password_hash, params, role, must_rotate, created_at, updated_at) SELECT NULL, username, password_hash, params, 'admin', 0, created_at, created_at FROM admin WHERE id = 1;`
3. Add `sessions.account_id`, backfilled to the migrated account's id.
4. `DROP TABLE admin`.

**The hash is copied byte-for-byte.** No rehash, no re-derivation, no moment at which a plaintext exists. The migrated admin's password keeps working and its stored parameter block keeps describing it — which is precisely the property RFC-0002 stored the parameters for.

**`must_rotate = 0` for the migrated admin.** They chose that password themselves at claim time; forcing them to change it would be a rotation with no event behind it.

**Existing sessions are attributed, not revoked.** RFC-0002 guarantees sessions survive a daemon restart, and a schema migration happens during exactly such a restart. Revoking on migration would break that contract to no benefit — the sessions belonged to the only account that existed.

**"Claimed" is redefined without changing its meaning.** RFC-0002: *an admin credential exists and `meta.claimed_at` is set*. It becomes: **at least one `role='admin'` account exists** and `meta.claimed_at` is set. Same guarantee, same two conditions, same refusal to read a half-written state as claimed.

### Roles

Exactly three. No custom roles, no per-route grants, no groups — an appliance whose permission model needs a policy engine has the wrong permission model.

| Role | Holds |
|---|---|
| **admin** | Everything. Account management, identity, trust, and anything that changes what the node *is*. |
| **operator** | Anything that changes what the radio *does*: config sections, apply, calibration, firmware, hardware. **No account management.** |
| **viewer** | Read-only live activity and the redacted config view. |

The dividing line, stated once so the mapping below is derivable rather than memorised:

- **admin** — changes who may reach the node, or what the node is to the outside world (accounts, phonebook/PII, peering trust, host networking, updates, what the public page publishes).
- **operator** — changes what goes over the air.
- **viewer** — changes nothing.

**The last admin cannot be deleted or demoted.** An attempt returns `409 Conflict`. Without this, a claimed device can be locked out of itself with no path back except a physical reset — turning an ordinary mistake into a card-out-of-the-Pi recovery. The check is on the write, not on the UI.

### Permission mapping

Exhaustive over the route table as it stands. **The default is deny**: a route absent from this table is admin-only until it is deliberately placed, exactly as RFC-0002's route matrix already treats a newly registered route as denied.

#### No session required (unchanged from RFC-0002)

| Route | Note |
|---|---|
| `GET /api/health` | Liveness and claim state only, both states. |
| `POST /api/claim` | Unclaimed only. |
| `POST /api/session` | Log in. |
| `DELETE /api/session` | Any authenticated account, own session only. |
| `/api/public/node`, `/status`, `/lastheard`, `/counters`, `/public/*` | Anonymous **by operator opt-in**, default off (D2). Gated on the public-view toggle, never on role. |

#### viewer (and above)

| Route | Note |
|---|---|
| `GET /api/events`, `GET /api/history`, `GET /api/status`, `GET /api/ws` | The live dashboard. |
| `GET /api/config` | The redacted view — carries `HasPassword`, never a secret. **See open question 1.** |
| `GET /api/map` | Read-only map. |

#### operator (and above)

| Route | Note |
|---|---|
| `PUT /api/config/{section}`, `POST /api/config/apply` | The config spine. |
| `/api/cal/*` (`sweep`, `cancel`, `events`, `apply`, `transmit`, `listen`) | **Transmits.** RF-affecting by definition. |
| `/api/flash`, `/api/flash/catalog`, `/api/flash/events` | Firmware. |
| `/api/hardware`, `/hardware/detect`, `/hardware/adopt`, `/hardware/uart` | Detect takes the modem off the air. |
| `/api/lcd/detect`, `/api/lcd/adopt` | |
| `/api/profiles`, `/api/profiles/import`, `/api/profiles/*` | Mode/network snapshots. Identity and calibration are never in a profile (RFC-0001), so this grants no identity change. |
| `/api/messages`, `/api/messages/{id}` | **See open question 2.** |
| `/api/overrides` | Read-only view of override drop-ins. |
| `/api/buses/validate` | Dry-run only. |
| `/api/import/scan` | Preview only; `import/apply` is admin. |
| `/api/hostlists`, `/api/hostlists/refresh`, `/api/dmr/talkgroups`, `/api/dmr/ids`, `/api/dmr/masters`, `/api/{ysf,p25,nxdn,dstar,m17}/reflectors` | Reference data for config. |
| `GET /api/network/status`, `/api/network/wifi/scan`, `/api/network/timezones` | Read and scan only. |

#### admin only

| Route | Why |
|---|---|
| `/api/accounts`, `/api/accounts/{id}` *(new)* | Account management. |
| `/api/phonebook`, `/api/phonebook/{id}` | Carries email (PII, D4) and is the identity accounts are keyed to. |
| `/api/peering/*` (`discover`, `initiate`, `confirm`, `cancel`, `pending`, `peers`, `revoke`) | Trust between nodes. |
| `/api/network/config`, `/api/network/apply`, `/api/network/confirm`, `/api/network/host/apply` | Can strand the node. **See open question 3.** |
| `/api/update/check`, `/api/update/apply`, `/api/update/stack*` | Changes the software. |
| `/api/public-view/*`, `/api/branding/*` | Decides what the world sees. |
| `POST /api/map/position` | Writes the map. |
| `/api/import/apply` | Bulk overwrite of the config from a card. |
| `/api/buses/migrate` | Structural. |

### Sessions

One column:

```sql
ALTER TABLE sessions ADD COLUMN account_id INTEGER REFERENCES accounts(id) ON DELETE CASCADE;
```

Everything RFC-0002 fixed about sessions is untouched: the token is still 256 random bits, still stored only as a hash, the cookie flags are the same, idle expiry is still 7 days refreshed by `last_seen`, `DELETE /api/session` still deletes the server-side record, and sessions still survive a restart.

`ON DELETE CASCADE` makes the obvious thing true by construction: **deleting an account revokes its sessions in the same transaction.** Demoting or changing the role of an account also takes effect on the next request, because the role is read from `accounts` at authentication time and never copied into the session record — a session carries an owner, not a capability.

Admins additionally get "revoke this account's sessions" (a delete by `account_id`). That is a new capability, not a changed one.

### Reset

**Reset removes trust, not identity.**

Both paths wipe `accounts` and `sessions` and clear `meta.claimed_at`, returning the device to claim mode. Neither touches `phonebook`. The people the node knows are not a credential, and forgetting them would make a password recovery into a data loss.

**Both paths behave identically with respect to accounts, sessions, and claim state** — they call the same primitive, and that is the property the test asserts. (Path (b) additionally clears provisioning state; see reconciliation note 2 below. That is pre-existing, out of scope here, and unchanged by this amendment.)

Everything else RFC-0002 fixed about reset stands: both paths require OS-level or physical authority, the marker is checked at **both** `/boot/waypoint-reset` and `/boot/firmware/waypoint-reset`, the marker is deleted after use, and the reset is **logged loudly** as a security-relevant, auditable event.

### Account creation and first login

An admin creates an account with a username, a role, an optional phonebook link, and an **initial password** — set by the admin, subject to the same claim-time strength policy.

`must_rotate` is set to 1. On first login the session is issued normally, and **every route except the password-change route is refused** until the password is rotated. The rotation clears the flag. An admin-chosen password is a password two people know; the flag is what makes that state brief and bounded rather than permanent.

**Email invitation is future work, and explicitly not a dependency.** An invite flow needs the node to send mail, which needs the notification system, which does not exist. The design above works with no email at all — the admin reads the initial password to the operator over the air, or hands it to them in the shack. If invitations ever land they replace the initial-password step; nothing here waits for them.

### Boundary (unchanged, restated because it is the point)

> Waypoint **does not create OS accounts and does not touch `sshd`.** It manages its own application credential and nothing else.

This amendment is **app-level authentication only**. An `operator` account is not a Unix user, grants no shell, and appears in no `passwd` file. The recovery credential for reset path (a) remains the operator's own imaging-time OS account, which Waypoint neither knows nor depends on nor weakens. Hardening `sshd` stays the operator's call and outside Waypoint's scope.

Nothing in three roles changes that boundary. It is the core of RFC-0002 and it is not being amended.

## The security contract (test harness)

RFC-0002's five release-blocking tests stand unchanged. Six are added.

6. **Role matrix.** Exhaustive over the registered route table, for each of the three roles plus unauthenticated: assert the exact allow/deny verdict from the mapping above. Like the existing pre-claim matrix, a newly registered route **defaults to denied** and fails the test until it is deliberately placed.
7. **Last-admin protection.** Deleting the only admin, and demoting the only admin, each return 409 and leave the account intact. With two admins, either may be removed.
8. **Migration fidelity.** From a claimed pre-amendment store: the admin lands in `accounts` as `role='admin'`, the `password_hash` (salt + digest) and `params` are **byte-identical** to before, the pre-migration password still authenticates, `must_rotate` is 0, and a session live before the migration still authenticates after it.
9. **Reset, both paths.** From a claimed device with several accounts, live sessions, and a populated phonebook: for path (a) and path (b) independently, assert `accounts` and `sessions` are empty, `claimed_at` is null, **the phonebook is untouched row-for-row**, the device serves the claim page, the marker is deleted (path b), and the reset was logged.
10. **Forced rotation.** An account with `must_rotate=1` authenticates, is refused every route but the password change, and is granted the mapping's routes once rotated.
11. **Referential integrity.** Deleting a phonebook row an account depends on returns 409 and deletes nothing; deleting an account revokes its sessions in the same transaction. **This test must run against an on-disk store**, not `:memory:` — `store.Open` applies `_pragma=foreign_keys(1)` only to a file path, so an in-memory store does not enforce foreign keys and would pass this test vacuously.

## Open questions

1. **Does `viewer` get `GET /api/config`?** The dashboard shell reads it for the callsign chip, so some access is needed. It is redacted of secrets but still names networks, addresses and ports. Options: grant it, grant a narrower projection, or move the chip to `/api/status`. Leaning grant, flagged because it is the one viewer route that discloses configuration.
2. **Are text messages visible to `operator`?** Messages are correspondence. Reading a club member's texts is arguably above "changes what the radio does". Options: operator (as mapped), admin-only, or per-account visibility. Leaning admin-only on reflection, but it is a real call.
3. **Is host networking `admin` or `operator`?** Mapped as admin because a bad change strands the node. But the confirm-or-revert guard already exists precisely for that, which weakens the argument.
4. **Should `username` default to the linked phonebook callsign** in the UI, and should two accounts be allowed to link to the same phonebook row (one person, two roles)? Leaning yes and no respectively.

## Reconciliation notes (drift found while drafting)

Neither is caused by this amendment. Both are places where RFC-0002's text and the shipped implementation have diverged, surfaced here because this amendment touches the same sections. The maintainer may want to fold them in or record them separately.

1. **Status code for an authenticated-but-absent session.** RFC-0002 says a claimed device returns **403** to an unauthenticated caller. The implementation returns **401** with a `mode: "login"` body (`internal/auth/handlers.go`), reserving 403 for the unclaimed state's `mode: "claim"`. The implemented split is better — it tells a client which surface to show — but the RFC text says 403 in both states.
2. **Depth of reset path (b).** RFC-0002 §Reset lists five actions for the marker path. The implementation performs a **full re-provision**: it also clears the provisioned marker, discards in-flight setup progress, and clears the `config.db` mirror, so the next boot serves the setup wizard. The code documents this as deliberate — *"the two reset paths are deliberately different depths, matched to what the person running them can already do"* — but RFC-0002 does not describe it.
