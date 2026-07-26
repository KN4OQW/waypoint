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

---

## 8 — `feat/tls-after-hostname`: TLS ordering

**Shipped.** `tlscert.Holder` — the device certificate held for the daemon's life
and replaceable without a restart — plus the wizard hook that remints it, and the
LAN redirect moved from 301 to 308.

**The ordering bug.** The certificate's SANs are minted from the hostname, and on
first boot the hostname is still `raspberrypi`. A certificate minted at daemon
start therefore names a host that is about to stop existing. The operator finishes
setup, is told to browse to `https://hs-shack.local/`, and gets a name-mismatch
warning on the address the node itself just gave them — the worst possible moment
to train someone to click through a certificate warning.

The certificate is now (re)generated *after* the hostname is chosen, and served
through `GetCertificate` so the running listener picks it up on the next
handshake rather than at the next restart. The operator meets the self-signed
trust prompt once, on the name they will keep.

**Calls worth a second opinion.**

- **`Ensure` regenerates only when the current certificate does not cover the
  name.** This is the "no cert regen after first LAN boot" criterion, and it
  matters beyond tidiness: a new certificate every boot means a new trust prompt
  every boot, which trains an operator to dismiss certificate warnings — the
  opposite of what a pinned self-signed certificate is for.
- **Coverage requires both forms.** `hs-shack` and `hs-shack.local`. A node is
  reached by both — the bare name where DNS search works, the mDNS name
  everywhere else, and the dashboard tells the operator to use the second.
- **A dotted name is normalised to its first label.** An operator who types
  `hs-shack.local` means the host `hs-shack`; minting for the dotted form would
  produce SANs for `hs-shack.local.local`.
- **301 → 308 on the LAN redirect.** 301 and 302 permit a client to rewrite the
  request as a GET. A `POST /api/claim` arriving on the HTTP listener would be
  replayed as a GET — the operator's claim quietly turning into a request for the
  claim page, with the password dropped. 308 preserves the method and body. This
  changes an existing behaviour and an existing test, so flag it if you disagree.
- **`/api/health` is not special-cased out of the redirect.** It is reachable over
  HTTP as a 308, not as a hole: a health endpoint served in the clear on the LAN
  would be the one unencrypted content surface on the box, and anything checking
  it can follow a redirect.
- **The captive portal stays plain HTTP**, unchanged. Every connectivity probe is
  an `http://` URL, and redirecting one to a self-signed certificate produces a
  warning interstitial inside the captive sheet, where there is frequently no way
  to click through. TLS is enforced on the LAN side, where the operator has a real
  browser.

**Verified.** Nine holder properties, including ECDSA P-256 pinned by a test (the
certificate is the operator's trust anchor for the node's life; quietly acquiring
a different key type is the kind of change that would otherwise pass review), no
regeneration across a simulated restart, exactly one remint per rename, and a
`TLSConfig` that really serves the new certificate after a mid-run remint.

The acceptance test runs a **real TLS server** over the daemon's own handler chain
and claims through a **real client** that trusts only the device certificate and
verifies `hs-shack.local`. It then checks the bare name works and that the boot
name `raspberrypi` no longer validates — so the remint replaced rather than merely
added.

`go vet ./...`, full `go test -race ./...`, three-arch cross-builds clean.
golangci-lint 0 issues on `internal/tlscert` and `internal/wizard`; I also cleared
three pre-existing `errcheck` findings in `tlscert.go` while I was in the file.

**Not verified.** Nothing new on the bench list. Note the remint currently happens
only via the wizard's hostname step — a hostname changed later through
`/api/network/host/apply` does not yet trigger one. That is a real gap, but it
belongs to the network-config surface rather than first-boot setup, and I have not
touched it.

---

## 9 — `feat/custom-toml-fastpath`: the power-user fast path

**Shipped.** `internal/seed` — a TOML on the boot partition that provisions a node
without anyone touching the wizard — plus `PasswordHash` through the protocol,
the daemon wiring, and the documented format in `docs/provisioning.md`.

**The central design call: it drives the wizard, it does not bypass it.** `Apply`
calls `SetHostname`, `CreateUser`, `InstallKey`, `JoinNetwork`, `Lock` — the same
steps, in the same order, as the interactive flow. A seed path that wrote the
marker itself would be a provisioning route with none of the validators, none of
the layered root-lock verification, and none of the idempotency — reachable by
anyone who can write to a FAT partition. What it skips is the access point and the
typing, not the rules.

**Schema is Raspberry Pi's `custom.toml`, key for key.** An operator who already
has one — from Imager, a provisioning script, a colleague — should be able to drop
it on the card and have it mean what it says. Unknown keys are ignored rather than
refused; a file that also configures things Waypoint has no opinion about is a
normal file.

**One protocol addition.** `CreateRecoveryUserRequest.PasswordHash`, applied with
`chpasswd --encrypted`, with `ValidatePasswordHash` guarding the same
colon/newline injection as the plaintext path. This is the *recommended* form for
a seed file: the file sits on a FAT partition anybody who takes the card can read,
so it should hold a hash rather than the operator's actual password. The validator
deliberately does not enumerate hash identifiers — crypt(3) grows new ones
(yescrypt replaced SHA-512 as Debian's default within this project's lifetime) and
refusing a hash the node's own libcrypt understands would reject a working
credential.

**Failure directions, each chosen deliberately.**

- **A bad TOML falls back to the wizard**, it does not stop the daemon. The
  operator who wrote it is not watching this boot; a node that refuses to come up
  because of a typo is a node they cannot reach to fix the typo.
- **Validation is whole-file, before anything is applied.** A partial application —
  hostname set, no account, root unlocked — leaves a node in a state nobody chose
  and nobody is present to notice.
- **A failed Wi-Fi join does not fail provisioning.** Refusing to finish would
  leave the node unprovisioned, which on a node with no other network means it
  raises the AP and waits for somebody who is not there. It finishes, logs loudly,
  and netwatch (prompt 6) is the safety net.
- **The file is deleted on success, left alone on refusal.** Deleted because it
  carries credentials on a readable partition; left alone when refused so the
  operator can see what they wrote. **Flagged for your sign-off** — deleting an
  operator's file is a real choice.
- **A stale file is ignored on an already-provisioned node**, so a card moved
  between boxes cannot rename a live node out from under its operator.

**Verified.** Ten seed properties and five daemon-level ones. The acceptance test
goes through the daemon's own startup path rather than calling the package
directly, because "no AP raised" is a property of the ordering in
`initSetup`/`initSetupAP`, not of the seed: it asserts `APUp` was called **zero**
times, that the claim is still required, and that the claim then succeeds. The
absent-file case asserts the wizard is untouched and still at `hostname`.

Eleven invalid-file cases each assert the error names the file *and* the specific
problem, and that nothing was half-applied. The fixture is the **documented**
TOML, so documentation drifting from the parser fails the build.

`go vet ./...`, `go test -race ./...`, three-arch cross-builds clean.
golangci-lint 0 issues across the five touched packages. `internal/seed` 84.3%
coverage.

**A latent test bug this prompt surfaced.** `TestControllerClientCancelsTheWindow`
(prompt 5) started `ctrl.Run` in a goroutine and never waited for it, so on an
unlucky schedule the cancelled context made `Run` log through `t.Logf` after the
test had returned — a data race. It had been passing since prompt 5 and only fired
once `-race` scheduling shifted. Now the test waits for the goroutine, and uses a
second tick instead of a sleep.

**New dependency.** `github.com/BurntSushi/toml` v1.4.0, promoted to direct. A
hand-rolled TOML subset parser would have been the alternative; for a file format
an operator hand-edits, a real parser is worth the dependency.

---

## 9.5 — `feat/firstboot-image-integration`: image, CI, and stale accounts

Two commits on one branch.

### Part A — image and CI

**Shipped.** Image install steps for the helper and its units, the
`waypoint-firstboot.service` oneshot, `WatchdogSec` on waypointd with
`internal/sdnotify`, `systemd-analyze verify` in CI, a differential
`.golangci.yml`, and issue #119 for the certificate remint gap.

**The watchdog is gated on the hub, not the timer.** `Restart=on-failure` cannot
catch the failure that actually strands a hotspot: a daemon whose event loop has
deadlocked still holds its listener open and never exits, so systemd believes it
healthy. `hub.Alive()` takes the mutex every event passes through, on its own
goroutine with a deadline. A ping that only proved a goroutine was scheduled would
keep exactly the wedged node alive indefinitely.

**The syscall check found one relaxation, and it is documented rather than
widened.** `SystemCallFilter=@system-service` does not permit `sethostname`. The
helper only reaches it through the no-systemd fallback in `sysprov.setHostname`;
on a node `hasSystemd()` is true and the hostname is set through `hostnamectl`, a
D-Bus call, so this process never makes the syscall. The fallback exists for
containers — precisely where the filter is not enforced. Recorded in
`verify-syscalls.sh` with that reasoning, and the check **fails if the exception
ever stops being needed**, so it cannot rot into a lie.

Resolving `@system-service` correctly mattered more than expected: systemd lists a
set's members without expanding nested groups, and my first parse produced 62
names instead of 376 — making almost every syscall look forbidden. A wall of false
positives is a check people learn to ignore, so the script now walks the closure
and fails if the resolved set is implausibly small.

**The lint gate is `new-from-rev`.** The baseline (63 findings in untouched
packages) is reported and does not block; new code is held to zero. Verified both
ways: 0 issues across the whole workstream, and a deliberately introduced finding
fails the gate.

### Part B — stale sudo accounts

**Shipped.** `ListSudoUsers` and `RemoveUser` (protocol now 13 methods), the real
implementation, fake, conformance coverage, the wizard's enumeration step, and a
docs section.

**Every `RemoveUser` refusal is a way to destroy the node being set up**, and none
is recoverable from the wizard: uid 0 under any name; system accounts below uid
1000; the account this setup just created; and an account with running processes.
There is no `-f` anywhere — `userdel -f` would delete an account out from under a
live session and leave files owned by a uid that gets reallocated.

**The wizard's own account is filtered where the list is built**, not in the UI. A
screen with a checkbox next to the recovery account, on the step immediately
before root is locked behind it, would be a bug with a bricked node at the end.
Both the wizard and the helper enforce it, so neither depends on the other having
remembered.

**Removal failures are per-account and never block setup.** This step sits between
"I have a recovery account" and "root is locked" — the worst place to strand
someone.

**A fake-modelling bug this surfaced.** The fake's `LockRoot` gated on
`RecoveryUser`, the *first* account it had seen. Once accounts could be removed,
removing the first one made `LockRoot` refuse even though a usable admin existed.
The real implementation always checked the live sudo group; the fake now mirrors
that with `hasUsableAdminLocked`. A fake that is wrong in a way the real thing is
not is worse than no fake.

**The docs say plainly that second-hand hardware should be reflashed.** The
listing finds accounts in the sudo group. It does not find a uid-0 account outside
it, a key in root's own `authorized_keys`, a systemd unit, or a modified binary.
It is a way to clear the obvious, not an audit, and the documentation says so.

**Verified.** Conformance covers both methods including every refusal. Eight
wizard properties. Three new container subtests against a real Debian: enumeration
with credential summaries, removal asserted through `getent`, the home directory,
the pruned sudoers drop-in and `visudo -c`; uid-0-under-another-name refused; and a
genuinely running process (`setpriv` + `sleep`) blocking removal. A boot simulation
checks the oneshot's condition and ordering in the real unit file and runs the real
binary on both sides of the marker. `systemd-analyze verify` clean on all 13 units.

`go vet ./...`, `go test -race ./...`, three-arch cross-builds clean.
