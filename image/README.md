# Waypoint OS image

A [CustomPiOS](https://github.com/guysoft/CustomPiOS) module that turns official
**Raspberry Pi OS Lite (Bookworm)** into the Waypoint hotspot image: the
waypoint-stack digital-voice daemons, `waypointd` (dashboard + supervisor +
RFC-0014 self-updater), and a security posture built for an appliance that lives
on an SD card and holds a modem open 24/7.

> This PR adds the **module** only. The CI workflow that builds and publishes the
> `.img` is a separate PR. The module builds locally so it can be developed and
> reviewed on its own.

## What the image contains

- **waypoint-stack** (`waypoint-stack` metapackage + daemons) from the signed apt
  repo at `https://kn4oqw.github.io/waypoint-stack`. The archive keyring is pinned
  by sha256; the deb822 source trusts only that keyring (`Signed-By`).
- **waypointd** at `/home/pi-star/waypoint/bin/waypointd` (the RFC-0014 updater's
  path), verified at build time against the RFC-0013 release key with minisign.
- **systemd units**: `waypointd.service` (enabled) with the
  `ExecStartPre=-…-update-boot-check` power-loss-revert hook (RFC-0014), plus the
  gateway units (present but not enabled — waypointd enables each on Apply).
- **mosquitto** — the MQTT data plane the stack publishes on (RFC-0008).

### No secrets (D5)

The image ships **no credentials, no TLS material, no claim state**:

- `BASE_ADD_USER=no` — the base image's default `pi:raspberry` user is **not**
  created (no `userconf.txt`). The operator pre-seeds their own user + Wi-Fi with
  **Raspberry Pi Imager's** advanced options; the image does not disable that
  first-boot mechanism.
- No TLS cert/key — waypointd mints a self-signed device cert on first start
  (RFC-0012).
- No config store — waypointd creates it on first boot and enters the claim flow
  (RFC-0002). The device is unclaimed until an operator claims it over the web UI.

`image/tests/verify-image.sh` asserts all of this on a built image.

## Two paths, two clocks — the security-update policy

A Waypoint node updates two different things, on two schedules, and they never
cross:

| | **The OS** | **Waypoint's software** |
|---|---|---|
| What | kernel/libc/openssl/… (Raspberry Pi OS + Debian) | waypoint-stack daemons + `waypointd` |
| Who | `unattended-upgrades` (the distro's clock) | `waypointd` (health-gated, RFC-0014) |
| When | the distro's security schedule | operator click, or an opt-in quiet window |

**OS path (`unattended-upgrades`).** Configured for **security origins only**,
determined *empirically* from each repo's real `Release` metadata (not copied from
blog posts):

- **armhf** (Raspbian base): `origin=Raspbian,label=Raspbian` +
  `origin=Raspberry Pi Foundation,label=Raspberry Pi Foundation`.
- **arm64** (Debian base): `origin=Debian,label=Debian-Security,codename=bookworm-security`
  + `origin=Raspberry Pi Foundation,label=Raspberry Pi Foundation`.

  (Raspbian and the Raspberry Pi archive have no separate `*-security` suite, so
  their whole `bookworm` archive is the security channel; the blacklist fences off
  the dangerous bits.)

- **The Waypoint origin (`Waypoint`) is excluded** from `Origins-Pattern` entirely,
  so `unattended-upgrades` never touches the stack. Belt-and-braces: every
  `waypoint-*` package is also `apt-mark hold`.
- **Package-Blacklist**: `raspberrypi-kernel`, `raspberrypi-bootloader`,
  `linux-image-.*`, `raspi-firmware` — a bad kernel/firmware bump is the exact
  "an update bricked my Pi" failure Waypoint exists to avoid.
- **`Automatic-Reboot "false"`** — a hotspot must not vanish mid-QSO to reboot.
- **needrestart** is set to *list only* (`$nrconf{restart}='l'`) with kernel hints
  off, so a library upgrade never auto-bounces MMDVMHost off `/dev/ttyAMA0`.

**Waypoint path.** `waypointd`'s RFC-0014 updater owns the stack's versions. It
installs *exact* versions with `apt-get install <pkg>=<version>`, which **proceeds
even on a held package** (an explicit versioned install overrides the hold's
"don't auto-change" intent — verified on the bench), so the holds need no
unhold/re-hold dance; a bare `apt upgrade`/`unattended-upgrades`, by contrast,
respects the hold and leaves the stack alone.

## SD-card longevity

`journald` is `Storage=volatile` with a bounded `RuntimeMaxUse` — the journal
lives in a RAM ring buffer, never wearing the SD card. No Waypoint component reads
`/var/log` back: event history is SQLite (RFC-0004), live status is MQTT/SSE
(RFC-0008), update state is the store + marker.

## Serial (the modem)

waypointd renders `UARTPort=/dev/ttyAMA0` by default, so the image frees the PL011
UART for the modem HAT: `enable_uart=1` + `dtoverlay=disable-bt` in `config.txt`,
the serial login console stripped from `cmdline.txt`, and `serial-getty` /
`hciuart` disabled.

## Module layout

```
image/
  src/
    config                         # dist: DIST_NAME, MODULES="base(waypoint)", no-secrets toggles
    build                          # wrapper around CustomPiOS build_custom_os
    fetch-base-image.sh            # download + sha256-verify the pinned base image
    variants/{armhf,arm64}/config  # BASE_ARCH + pinned base image URL/sha256
    modules/waypoint/
      config                       # WAYPOINT_* settings (URLs, pins, paths)
      start_chroot_script          # the install logic (runs in the chroot)
      filesystem/
        waypoint-release.pub       # RFC-0013 release key (bundled into the image)
        root/…                     # apt source, u-u periodic, needrestart, journald, units
  tests/verify-image.sh            # loopback assertions (reused by the CI PR)
  README.md
```

## Building locally

You need Docker and a [CustomPiOS](https://github.com/guysoft/CustomPiOS) checkout.

```sh
git clone https://github.com/guysoft/CustomPiOS
echo "$PWD/CustomPiOS/src" > image/src/custompios_path

# Fetch + verify the pinned base image, then build the armhf variant.
image/src/fetch-base-image.sh armhf
image/src/build armhf
# -> image/src/workspace-armhf/*.img
```

The canonical build runs inside the CustomPiOS Docker container (QEMU + loopback
handled for you); see CustomPiOS's README for the container invocation. The CI PR
wires that up.

### Offline / pre-release builds

Until the public apt repo (GitHub Pages) and the first `waypointd` release are
live, point the build at local copies — the verification steps (keyring sha256,
waypointd minisig) still run, so a local build proves the same trust path:

```sh
export WAYPOINT_APT_URL="http://<host>/waypoint-stack"     # a local mirror of the signed repo
export WAYPOINT_LOCAL_ARTIFACTS="/path/with/keyring+waypointd+minisig"
```

The finished image still ships the **public** `waypoint.sources`; the override
only changes where the *build* fetches from.

## Verifying a built image

```sh
sudo image/tests/verify-image.sh --image image/src/workspace-armhf/*.img
# or against an already-mounted rootfs:
image/tests/verify-image.sh --rootfs /mnt/waypoint-root
```

## Trust roots

- **apt archive key** (GPG) — signs the waypoint-stack repo `Release`; public half
  is `waypoint-archive-keyring.gpg` (pinned by sha256 here).
- **release key** (minisign, RFC-0013) — signs `waypointd` releases; public half is
  [`docs/waypoint-release.pub`](../docs/waypoint-release.pub), bundled into the
  image so `waypointd`'s Go verifier (no runtime minisign dependency) can check its
  own future updates.
