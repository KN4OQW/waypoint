# Bench run log — Raspberry Pi 3 Model B, BCM43430/1

Hardware validation of the first-boot setup flow, 2026-07-26.

Everything before this ran against a fake system or a Debian container. Neither
has a radio, and the setup access point is the one part of the flow whose
behaviour is decided by firmware. This is the first time any of it met one.

## The bench

| | |
|---|---|
| Board | Raspberry Pi 3 Model B Rev 1.2 |
| Radio | BCM43430/1 (the Pi 3B's 43438), firmware 7.45.98 (TOB) FWID 01-8e14b897, 2021-07-19 |
| Driver | `brcmfmac` + `brcmfmac_wcc`, kernel 6.12.34+rpt-rpi-v8 |
| OS | Raspbian GNU/Linux 13 (trixie), armhf userland on an arm64 kernel |
| NetworkManager | 1.52.1 |
| Serial | `000000008cb79e10` → SSID **`Waypoint-Setup-9E10`** |
| Reached over | `eth0` (172.16.50.13), so `wlan0` was free to reconfigure without cutting the session |

**This is not a stock Waypoint image.** The board runs WPSD *and* Waypoint side by
side — a development arrangement, not a supported one. Shipped nodes run a fresh
Debian with Waypoint as the only hotspot stack.

That distinction matters for exactly one result. Finding 1 below is caused by
WPSD's dnsmasq and would not occur on a stock image; the fix was kept as a
defensive diagnostic rather than as support for coexistence. Everything else —
the radio, the driver, the firmware, NetworkManager — is identical to what a
shipped node has, which is what this bench was for.

The harness is `internal/sysprov/benchhw_test.go`, build tag `benchhw`,
cross-compiled to armhf and run as root on the board. It drives the real
`sysprov.System` — the same code the helper runs — not a re-implementation.

## Results

| # | Step | Result | Notes |
|---|---|---|---|
| 1 | SSID from board serial | **PASS** | `Waypoint-Setup-9E10`, no fallback |
| 2 | `APUp` on brcmfmac | **PASS** (after fix) | 1.48 s; failed first — see finding 1 |
| 3 | AP mode / channel / power | **PASS** | `type AP`, ch 11 (2462 MHz), 20 MHz, 31 dBm |
| 4 | AP address | **PASS** | `wlan0 10.42.0.1/24` |
| 5 | dnsmasq drop-in written | **PASS** | `port=0`, `dhcp-option=114` present |
| 6 | `port=0` frees :53 | **PASS** | nothing on `:53` from NM's dnsmasq |
| 7 | Captive DNS binds AP address | **PASS** | responder bound `10.42.0.1:53` |
| 8 | DNS hijack answers | **PASS** | `dig @10.42.0.1 captive.apple.com` → `10.42.0.1` |
| 9 | OS probe → portal | **PASS** | `GET /generate_204` → 302 → `http://10.42.0.1/` |
| 10 | No routing off AP segment | **PASS** | `net.ipv4.conf.wlan0.forwarding = 0` |
| 11 | `APDown` | **PASS** | back to `type managed`, address released |
| 12 | Checkpoint → AP down → join | **PASS** | radio handed over cleanly |
| 13 | Wrong passphrase rejected | **PASS** | 4 s to a definite answer |
| 14 | Rollback + AP re-raised | **PASS** | AP back in **1.61 s**, total outage **6 s** |
| 15 | Error is actionable | **PASS** (after fix) | see finding 2 |
| — | Phone captive sheet | **NOT RUN** | needs a handset; no phone on the bench |
| — | Correct-passphrase join | **NOT RUN** | needs the network's real passphrase |
| — | Hostname / user / key / root lock | **NOT RUN here** | covered by the container suite against real `useradd`/`passwd`/`chsh`/`sshd`; not re-run on the board because it is a live dual-stack node |
| — | Claim over HTTPS, reboot persistence, reset depths | **NOT RUN** | need a stock image flashed to a card — see "What is still owed" |

## Finding 1 — an incumbent dnsmasq blocks the access point

**Symptom.** `APUp` failed in 2.2 s:

```
Error: Connection activation failed: IP configuration could not be reserved
(no available address, timeout, etc.)
```

**Cause.** NetworkManager's `ipv4.method=shared` starts its own dnsmasq for DHCP.
WPSD already runs one, bound to the wildcard `0.0.0.0:67`. NM's instance cannot
bind and dies:

```
dnsmasq: failed to bind DHCP server socket: Address already in use
dnsmasq-manager: dnsmasq exited with error: Network access problem (2)
device (wlan0): state change: ip-check -> failed (reason 'ip-config-unavailable')
```

Confirmed by stopping the incumbent: the AP then came up first time.

**Why the existing `port=0` did not cover it.** That drop-in disables dnsmasq's
*DNS* server so our captive responder can hold `:53`. The collision here is on
`:67`, which `port=0` does not touch.

**`bind-dynamic` does not fix it.** Tried on the bench: the incumbent holds the
wildcard, so a specific bind still loses. Nothing writable in
`dnsmasq-shared.d` can fix a conflict caused by *the other process*.

**What changed.** `APUp` now pre-flights `:67` and refuses with a message naming
the holder and the two ways out, in 66 ms instead of 2.2 s of NM timeout:

```
cannot raise the setup access point: dnsmasq already has the DHCP port (67), so
NetworkManager's shared mode cannot start its own. Stop it (for example
`systemctl stop dnsmasq`) or configure it with bind-interfaces so it does not
hold the wildcard address
```

**Scope — corrected after the run.** Waypoint is the only hotspot stack on a
node: a fresh Debian with Waypoint and nothing else, no Pi-Star and no WPSD. A
stock image installs no system dnsmasq, so on the hardware this is actually
shipped to, this collision does not arise.

It arose here because the bench board runs Waypoint *and* WPSD side by side,
which is a development arrangement rather than a supported one. So the check
earned its place as a **defensive diagnostic, not a compatibility feature**: if
something on a node does hold `:67`, the operator gets one sentence naming it
instead of an NM message about address pools. That is worth 66 ms on a path that
runs once per boot.

It is explicitly *not* an argument for making Waypoint coexist with another
hotspot stack. Nothing here should be read as supporting that configuration.

## Finding 2 — a rejected passphrase did not say so

**Symptom.** A deliberately wrong passphrase produced:

```
Warning: password for '802-11-wireless-security.psk' not given in 'passwd-file'
Error: Connection activation failed: Secrets were required, but not provided
```

which reads as "you forgot the password" when the operator supplied one.

**I misdiagnosed this first.** I assumed a race between `nmcli connection reload`
and the activation, and that NM had not loaded the secret. NM's own log says
otherwise:

```
15:18:08.660  connection 'waypoint-DeathStar' has security, and secrets exist.
              No new secrets needed.
15:18:12.042  no secrets: No agents were available for this request.
15:18:12.042  state change: need-auth -> failed (reason 'no-secrets')
```

NM had the key, tried it, was refused by the access point, and asked for a *new*
one. nmcli has no agent, so it reported the absence of a secret rather than the
rejection of one. The behaviour was correct all along; only the message was wrong.

A separate probe confirmed the keyfile itself is fine — NM parsed it, stored the
PSK (`psk-flags: 0`), and returned it on request.

**What changed.** `joinFailure` maps `no-secrets` / `Secrets were required` /
`passwd-file` onto:

```
"DeathStar" refused the passphrase; check it and try again
```

and a timeout onto a range/broadcast message. Verified on the bench.

**Kept from the misdiagnosis, on different evidence.** Two changes made while I
had the wrong theory earned their place independently, and their comments now say
what was actually observed:

- **`autoconnect=false` until the join is verified.** The bench log shows NM
  auto-activating a newly written profile (`policy: auto-activating connection
  'waypoint-benchprobe'`) before any explicit `connection up`. Two activations of
  one profile racing is a real hazard, and a failed profile would otherwise retry
  by itself forever. It is switched on after the join is verified, so boot-time
  rejoin still works. **Risk worth noting:** if that `nmcli connection modify`
  fails, the node will not rejoin on boot. It is logged, not fatal.
- **`waitForProfile`.** `reload` is asynchronous; waiting for NM to report the
  profile is cheap and removes a timing dependency. It did *not* fix finding 2.

## brcmfmac notes

- **Channel is chosen for you.** No channel was requested and the firmware settled
  on **11 (2462 MHz)** — matching the locally congested channel, with regulatory
  domain reported as `global` (no country was set, so `iw reg set` never ran).
  `band=bg` in the profile was honoured; nothing forced 5 GHz, which this chip
  does not have.
- **AP/STA sequencing was clean.** AP → down → station → AP again worked every
  time with no driver reload, no `rfkill` intervention, and no wedged interface.
  The AP came back **1.61 s** after the rollback. No brcmfmac firmware workaround
  was needed on 7.45.98.
- **MAC address changes with role.** AP mode used the real address
  `b8:27:eb:e2:cb:45`; after `APDown`, NM set a random scanning MAC
  (`da:68:77:c4:53:8c`, then `56:56:e8:f8:87:2e`). Harmless here, but anything
  that pins a client by MAC across the AP/station transition would be surprised.
- **`iw` lives in `/usr/sbin`.** Present on this image but not on a non-root
  `PATH`. The helper runs under systemd, whose root `PATH` includes `/usr/sbin`,
  so the bare-name exec is fine — verified, not assumed.
- **`ipv4.method=shared` really does mean dnsmasq.** There is no way to get NM's
  shared addressing without it, which is what makes finding 1 structural rather
  than incidental.

## What is still owed

- **A phone.** The captive sheet auto-opening is the one thing no amount of
  `curl` proves. The probe endpoints answer correctly (302 to the portal, none of
  the vendor success strings), but whether iOS and Android pop their sheet needs a
  handset.
- **A card.** Claim over HTTPS, reboot persistence, and both reset depths need
  the image below flashed to an SD card and booted. I cannot flash a card from
  here; the board's running system is a live dual-stack node, not a clean image.

## The image

Built from this tree and verified. `image/tests/build-local.sh` reproduces it.

    waypoint-firstboot-bench-armhf.img.xz   598 MiB
    sha256  dd1cd9734facf744899c5b22645bf993e6eb8b55f9541966c83cdcb91e8dfe0a

Checked by loop-mounting the finished rootfs:

| Check | Result |
|---|---|
| `/usr/bin/waypoint-provision-helper` installed | present, 0755 |
| `waypoint-provision-helper.socket` enabled | symlinked into `sockets.target.wants` |
| `waypoint-firstboot.service` enabled | symlinked into `multi-user.target.wants` |
| `waypoint-provision-helper.service` shipped | present |
| `sysusers.d` / `tmpfiles.d` fragments | both present |
| `waypoint` group created at build time | `waypoint:x:992:` |
| Provisioned marker absent | yes — first boot runs the wizard |
| Release public key baked in | the dev key the binaries were signed with |

That closes the one acceptance item from prompt 9.5 I had reported as unverified:
**a from-scratch image build does contain all units enabled.**

**This image trusts a throwaway signing key.** The module verifies every binary it
bakes in with minisign and that check is not skipped for a local build — skipping
it would mean not testing the trust path the real build uses. So the binaries are
signed with a generated key and that key becomes the image's release key. The
verification is real; the key is obviously not, which is the honest way to say the
image must never accept a production release. It is for this bench and nothing
else.

Five container packages had to be found by failing without them: `python3`,
`python3-yaml`, `python3-git`, `lsof`, and the partition tools. Two of them
(`python3-yaml`, `python3-git`) fail *late* — after the 500 MiB base image has
been downloaded, extracted and mounted — so a first attempt costs ten minutes
before it tells you. They are pinned in the build script with that noted.
- **A correct passphrase.** The success half of the join. The failure half — the
  one that strands operators — is verified.

## Reproducing

```
GOOS=linux GOARCH=arm GOARM=7 go test -c -tags benchhw -o sysprov.benchhw ./internal/sysprov
scp sysprov.benchhw pi@<board>:/tmp/
ssh pi@<board> 'sudo systemctl stop dnsmasq'          # only on a WPSD/Pi-Star box
ssh pi@<board> 'sudo WAYPOINT_BENCH_HW=1 /tmp/sysprov.benchhw -test.v -test.run TestBenchAccessPoint'
ssh pi@<board> 'sudo WAYPOINT_BENCH_HW=1 WAYPOINT_BENCH_SSID=<in-range-ssid> \
    /tmp/sysprov.benchhw -test.v -test.run TestBenchJoinRevert'
```

The board was returned to its prior state afterwards: `waypoint-*` profiles and
the `dnsmasq-shared.d` drop-in removed, `nmcli connection reload`, `dnsmasq`
restarted. `eth0`, `waypointd`, and `dnsmasq` all confirmed active; nothing of the
bench left behind.
