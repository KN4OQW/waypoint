# First-boot setup: build log

A running record of the branches that build Waypoint's first-boot setup flow, one
entry per prompt, appended as each lands. Each entry says what shipped, the
judgment calls worth a second opinion, what was verified, and what was not.

The branches are **stacked**: each is cut from the one above it, so a single PR at
the end can carry the lot in order.

| # | Branch | Subject |
|---|---|---|
| 1 | `feat/provision-state` | Provisioned-state contract |
| 2 | `feat/provision-helper-proto` | Privileged-helper protocol |
| 3 | `feat/provision-helper` | Root helper daemon |
| 4 | `feat/wizard-gating` | waypointd client + wizard gating |
| 5 | `feat/setup-ap` | Setup AP + captive portal |
| 6 | `feat/net-handoff` | Network join with confirm-or-revert |

**Why this workstream exists.** The Waypoint image was designed around Raspberry
Pi Imager's advanced options: the operator would pre-seed a user and Wi-Fi at
flash time. Imager 2.x removed customisation for locally flashed images — it
refuses to offer the options without `init_format` metadata from an OS manifest —
so a node downloaded as a `.img.xz` arrives with no hostname the operator chose,
no account, and an unlocked root. The fix is to stop depending on Imager: a node
flashed with nothing but `dd` configures itself on first boot.

---

## 1 — `feat/provision-state`: the provisioned-state contract

**Shipped.** `internal/provision`. A JSON marker at `/var/lib/waypoint/provisioned`
(`State`: schema version, timestamps, hostname-set, recovery-user, root-locked),
`IsProvisioned`/`Load`/`Save`, and a `Mirror` that keeps a boolean in `config.db`.

**Calls worth a second opinion.**

- `IsProvisioned` takes a path rather than being no-arg — same `bool` return, but
  testable without touching `/var/lib`, matching how `tlscert.LoadOrCreate` takes
  a `dir`.
- **A newer schema is not "needs setup".** With A/B slots (RFC-0017), "older
  waypointd reads a newer marker" is a normal rollback event. Treating it as
  corrupt would drop a healthy node into the wizard — a self-inflicted outage and
  a takeover window. `Load` returns the state alongside `ErrSchemaNewer`; `Save`
  refuses to write it back down.
- **I/O errors surface as themselves**, not as `ErrCorrupt`. `IsProvisioned` still
  returns false, but `Load` hands back the real cause so an EACCES is not logged
  as corruption.
- The mirror lives in `meta` beside auth's `claimed_at`, not the settings tree, so
  it is never compiled into an INI or carried in an exported profile.

**Verified.** `gofmt`, `go vet ./...`, full suite, `-race -count=3`, armv6
cross-build. golangci-lint 0 issues on the package. 80.7% coverage. Ten
properties, including a failed `Save` leaving the previous marker byte-intact and
a concurrent reader never seeing a torn file across 200 varying-length writes.

**Not verified.** Nothing outstanding.

---

## 2 — `feat/provision-helper-proto`: the privileged-helper protocol

**Shipped.** `internal/privhelper` (the `Provisioner` interface, eleven
request/response pairs, the length-prefixed JSON codec, the validators) and
`internal/privhelper/privtest` (the in-memory fake and the conformance suite).

**Calls worth a second opinion.**

- `NetCheckpoint*` came out Create/Destroy/Rollback rather than
  Create/Confirm/Rollback, matching the existing `netconfig.Checkpoint` so a thin
  adapter can back the confirm-or-revert `Guard`.
- **Two orderings are contracts, not conventions**, pinned by the conformance
  suite: `CreateRecoveryUser` before `LockRoot`, and a recovery user with neither
  a password nor a key is refused at validation.
- `EnableSSH.PasswordAuth` is a `*bool`. "False" and "don't touch it" are
  different requests; conflating them means every call that only meant to restart
  sshd silently disables password logins.

**Three security checks, not format checks.**

- Password: rejects newlines and colons. `chpasswd` reads `user:password` lines,
  so a newline sets *a second account's* password.
- SSH key: must be a single line. `authorized_keys` is line-delimited, so a key
  containing `\n` is two entries and the second carries whatever `command=` the
  submitter chose.
- SSH key body: decoded and walked as SSH wire fields, requiring ≥2 fields
  consumed exactly. My first cut only compared the embedded algorithm name to the
  declared one, and `ssh-ed25519 AAAAC3NzaC1lZDI1NTE5` — an algorithm name with no
  key material — sailed through. Caught by the test.

**Verified.** All the above plus golangci-lint 0 issues, 82.6% / 82.6% coverage.
Eleven conformance property groups.

**Not verified.** Nothing outstanding.

---

## 3 — `feat/provision-helper`: the root helper daemon

**Shipped.** `cmd/waypoint-provision-helper`, `internal/sysprov` (the real
`Provisioner` over `hostnamectl`/`useradd`/`passwd`/`chsh`/`nmcli`), the socket
server and client in `internal/privhelper`, and the systemd `.socket`/`.service`
plus `sysusers.d` and `tmpfiles.d`.

**Calls worth a second opinion.**

- **Root's shell is left alone.** `chsh root -s nologin` is a common hardening
  step and it breaks `sudo -i` — the exact path the recovery account uses. `chsh`
  is instead used to *repair* a recovery account that already exists with
  `/usr/sbin/nologin`. **Flagged for your sign-off.**
- **`LockRoot`'s precondition is "can log in", not "is in the sudo group."** A
  locked, keyless member of `sudo` is a name in a file.
- `useradd` refuses any name resolving to **uid 0**, not just the string `root`.

**Three things the container test found that unit tests structurally could not.**

1. `rename()` cannot replace a bind-mounted `/etc/hostname`. Falls back to a
   truncating in-place write on `EBUSY`/`EXDEV`.
2. `sshd -t` fails for reasons unrelated to the drop-in under test. My first cut
   deleted the drop-in on any failure — which would silently undo a
   `PermitRootLogin no` the operator just asked for. The check is now
   **differential**.
3. `/run/sshd` must exist before sshd parses anything.

**Verified.** Everything above, plus a container integration test driving the real
`useradd`/`passwd`/`chsh`/`hostnamectl` and verifying with `getent`, `id`, `stat`,
and `sshd -T`. The conformance suite now runs **over a real Unix socket** as well
as in-process. golangci-lint 0 issues on the new packages. Added as a CI job.

**Not verified.** The systemd hardening (`SystemCallFilter`,
`CapabilityBoundingSet`) is unverified on real hardware. The helper binary is not
yet installed by the image module.

---

## 4 — `feat/wizard-gating`: waypointd client + wizard gating

**Shipped.** `internal/wizard` (step state machine, HTTP surface, gate) and
`cmd/waypointd/setup.go` wiring waypointd as the unprivileged end of the socket.

**Calls worth a second opinion.**

- The gate chain is `setupGate(auth.Gate(mux))` — setup outside claim, so the two
  never both hold an opinion. An unprovisioned node closes `/api/claim` too: it
  still answers to `raspberrypi.local` with an unlocked root, and claiming it
  first would hand someone a box the wizard is about to change under them.
- **Progress is a separate file from the marker.** Folding them together would
  mean a half-finished wizard left a marker `IsProvisioned` had to disbelieve.
- **`/api/setup/state` stops disclosing detail once setup is done.** Caught while
  wiring the gate: the endpoint is unauthenticated for the daemon's life, and my
  first cut returned the hostname, recovery username, and key fingerprint in every
  state.

**Verified.** An e2e test walking hostname → user → key → lock → claim through the
real handler chain, and an interrupted-and-resumed variant that restarts waypointd
mid-wizard and asserts `set_hostname` and `create_recovery_user` were each called
exactly **once** across the restart. Default hostname rejected at 400 with a
message naming both the default and the `.local` collision. golangci-lint 0 issues
on `internal/wizard`; 79.5% coverage.

**Not verified.** `cmd/waypointd` carries a **pre-existing** golangci-lint baseline
of 17 `errcheck` findings in files this workstream did not touch. The repo has no
`.golangci.yml`, so defaults apply everywhere — worth a config file or a sweep.

---

## 5 — `feat/setup-ap`: setup access point + captive portal

**Shipped.** `internal/setupap` (SSID from the board serial, boot-partition PSK
file), `internal/captive` (DNS responder, OS-probe handlers, session lock, AP
lifecycle controller), the `nmcli`/dnsmasq changes in `internal/sysprov`, and the
waypointd wiring.

**Calls worth a second opinion.**

- **dnsmasq gets `port=0`.** NM's shared mode starts dnsmasq for DHCP *and* DNS,
  and two processes cannot both hold 53. Rather than putting the hijack in a
  config file written blind, dnsmasq does DHCP only and the Go responder owns DNS.
- **AAAA gets an empty NOERROR, not a forged address.** A client handed a nonsense
  AAAA tries it first and stalls until it times out.
- **Forwarding is disabled per-interface**, not globally. Shared mode turns it on
  because it assumes you want to share a connection.
- **The session lock matches on MAC *or* IP.** A DHCP renewal mid-setup must not
  cost the operator their progress.
- **The portal is plain HTTP.** Every probe URL is `http://`, and a self-signed
  cert on the redirect target produces a warning interstitial inside the captive
  sheet, where there is often no way to click through.

**Two bugs the tests caught.**

1. The associate window was anchored on when `Run` started rather than when the AP
   came up — a supervisor starting late would have granted a fresh thirty minutes
   the AP had already spent broadcasting.
2. `DNSResponder.Shutdown` raced `ListenAndServe`; found under `-race`.

**Verified.** PSK-file parser (nine accept, nine reject cases) and session lock
(seven properties) unit-tested as asked. Every probe path returns 302 and is
asserted *not* to contain `Success` / `Microsoft Connect Test` / `Microsoft NCSI`.
DNS hijack over a real socket. golangci-lint 0 issues on the new packages.

**Not verified — needs a bench Pi.** A phone associating and the captive sheet
auto-opening; the PSK-file variant coming up as WPA2; the AP being gone after a
real join; the second device getting the 403 over the air. The RF path, `nmcli` in
AP mode, and each vendor's sheet-opening heuristic are untested here.

---

## 6 — `feat/net-handoff`: network join with confirm-or-revert

**Shipped.**

- The full join sequence in `Wizard.JoinNetwork`: checkpoint → lower the AP →
  join → **verify** → commit, with rollback and AP re-raise on every failure path.
- `captive.Controller` gained a reversible handover: `DownForJoin`, `Reraise`,
  `Commit`, and a `spends()` rule separating "down for good" from "paused".
- `internal/netwatch` — the fallback re-entry. A node with no route out for longer
  than its grace period raises the setup AP again, touching no provisioning state.
- `apSession` in waypointd ties the portal and DNS listeners to the AP's lifetime,
  so a re-raise brings the wizard back rather than an AP serving nothing.

**Calls worth a second opinion.**

- **The AP is lowered before the join, not after.** A single-radio Pi cannot be an
  access point and a station at once. The lowering is reversible — it does not
  spend the AP — which is what makes the rollback path work at all.
- **Association is not success.** `nmcli` reports success once the profile is up,
  and a node associated to the right SSID with no DHCP lease satisfies that while
  being completely unreachable. `verifyJoin` requires an address. This is the
  failure an operator finds ten minutes later, hunting for a node that is on their
  network and answering nothing.
- **The checkpoint is created before the radio is touched**, so a failure to free
  the radio is itself covered by the rollback.
- **netwatch reads `/proc/net/route`, not a connectivity probe.** A probe means
  choosing a host to reach, and a node that raised its own access point because
  some third party's server was down would be worse than one that never raised it.
  The AP's own interface is excluded — counting its route would mean a node that
  raised the AP immediately decided it was back online.
- **netwatch runs inside waypointd, not as a separate `waypoint-netwatch.service`.**
  It needs the AP controller's state, and a separate process would need a protocol
  to coordinate. The tradeoff: if waypointd itself is wedged, nothing re-raises the
  AP — though there would be no wizard to serve either. **Flagged for your
  sign-off** if you want it split out.
- **A 10-minute grace period.** Chosen against the two things that actually
  happen: a router rebooting after a firmware update is back inside three or four
  minutes; a node that has genuinely lost its network never comes back.

**Verified.** Eight join properties: good PSK commits and the checkpoint is
destroyed; bad PSK rolls back and the AP comes back (asserted as an ordered
handover log `down → reraise`, not just an end state); association-without-lease
and not-connected both fail and revert; a radio that cannot be freed aborts before
the join and releases the checkpoint; the sequence is retryable and the second
attempt commits; a node with no AP joins without any handover; and no checkpoint is
ever left outstanding on any path. Eight netwatch properties including short
outages *not* raising, long ones raising exactly once, a second outage getting its
own grace period, and the AP's own route not counting as connectivity.

`go vet ./...`, full `go test -race ./...`, three-arch cross-builds clean.
Coverage: netwatch 90.7%, wizard 77.3%, captive 85.4%.

**Not verified — needs a bench Pi.** The three acceptance criteria are all on
hardware: a bad PSK auto-restoring the AP *within the timeout* (the logic and
ordering are tested; the wall-clock behaviour of `nmcli --wait` under a failed
association is not), a good PSK persisting across a reboot, and unplugging the
upstream for N minutes re-raising the AP. The marker-preservation half of that
last one *is* tested — netwatch has no route to provisioning state, and there is a
test asserting it has not grown one.

---

## 7 — `feat/root-lock-reset`: root lockout + reset paths

**Shipped.**

- A third verification layer in the wizard, run before `LockRoot`.
- `ensureRecoverySudoers` in `internal/sysprov` — passwordless sudo for key-only
  recovery accounts, validated with `visudo -c` before it is trusted.
- `provision.Clear`, `wizard.ClearProgress`, and `cmd/waypointd/reset.go` holding
  both reset depths.
- `reset-claim --full`; the boot-partition marker now performs a **full**
  re-provision.

**The bug this prompt surfaced.** The recovery account is created key-only by
default, which means `passwd -l`. On Debian, `sudo` authenticates the *invoking
user's own password* — and a locked account has none to give. So the recovery
account could SSH in and then do nothing: no sudo, no root, on a node whose root
we had just locked. That is precisely the dead end the account exists to prevent,
and nothing in the earlier prompts would have caught it, because every test until
now asserted group membership rather than that `sudo` actually works.

The fix is a `/etc/sudoers.d/010-waypoint-recovery` drop-in with `NOPASSWD`, and
**only** for accounts whose password is locked. An account with a password
authenticates normally, and any drop-in from an earlier key-only run is removed —
so adding a password later does not silently leave passwordless root behind. The
file is written 0440 and validated differentially with `visudo -c`, because a bad
file in `sudoers.d` breaks sudo for everyone, which on a root-locked node means
nobody can administer it at all.

**The three layers, and what each catches alone.**

1. **The wizard** (`verifyRecoveryUser`) — the account *this wizard created*
   exists, can become root, and has a credential. Catches "the wizard's own
   account is not what it thinks it is".
2. **The helper** (`hasUsableRecoveryAccount`) — some non-root member of the sudo
   group can actually log in, checked against the live system. Catches an account
   changed out from under the wizard.
3. **The sudoers drop-in** — a key-only account can use sudo at all. Catches the
   account that can log in and then do nothing.

`Progress` gained `UserSudo`, recorded from `CreateRecoveryUser`'s response, so
layer 1 is checking something real rather than an assumption.

**The two reset depths, and why they differ.** This is the call worth your
sign-off.

| Path | Needs | Clears |
|---|---|---|
| `waypointd reset-claim` | a shell on the box | admin credential, sessions, `claimed_at` |
| `waypointd reset-claim --full` | a shell on the box | the above **+** provisioned marker, setup progress, mirror |
| boot-partition marker | **the SD card in a reader** | same as `--full` |

The marker is now a *full* reset, where it used to be claim-only. The reasoning:
whoever has a shell can already read the config and restart the daemons, so
handing the dashboard to a new administrator is the whole job — the node's
identity was never in question. Whoever holds the SD card is almost always
someone who cannot get in **at all**: a forgotten password on a node with root
locked, or a second-hand board carrying somebody else's setup. Giving them a
dashboard on a node still called someone else's hostname, with someone else's
recovery account and SSH key, would not be a recovery.

**What the full reset deliberately does not do.** It does not revert the
hostname, delete the recovery account, or unlock root. Those need the privileged
helper, and a reset that ran halfway — hostname reverted, root still locked, no
account — would be worse than one that reverts nothing. Re-running setup is
idempotent: it renames the host to whatever the operator picks next and converges
the account rather than duplicating it. The `--full` output says this explicitly,
and a test asserts it does.

**Verified.** Six reset properties (claim-only leaves the marker, mirror, and
progress untouched; full clears all three and the wizard really does resume at
`hostname`; both idempotent; honest reporting on a fresh node; the boot marker
takes the full path and deletes itself). The two existing marker tests were
updated rather than replaced.

The container integration test now proves the acceptance criterion against a real
Debian filesystem: `passwd -S root` is `L`, `sshd -T` reports
`permitrootlogin no`, and **`sudo -n -u rescue sudo -n -i id -u` returns `0`** —
the recovery user really can become root. A second subtest creates a
password-holding account and asserts it did *not* get passwordless sudo.

`go vet ./...`, full `go test -race ./...`, three-arch cross-builds clean.
golangci-lint **0 issues** across all seven of this workstream's packages.

**Not verified.** Nothing new on the bench list. Note the pre-existing lint
baseline is wider than previously reported: `./internal/...` as a whole has 63
findings (50 errcheck, 10 staticcheck, 3 unused) in packages this workstream never
touched. All seven packages it *did* touch are clean. Still worth a
`.golangci.yml` or a sweep before the PR.
