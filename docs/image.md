# Flashing the Waypoint image

Waypoint ships a ready-to-flash SD-card image built on Raspberry Pi OS Lite
(Bookworm). It contains the digital-voice stack, `waypointd` (dashboard +
supervisor + self-updater), and a hardened, appliance-grade OS posture. The image
carries **no default password and no pre-configured identity** — you set those up,
and the node is claimed over its web UI on first boot.

## Supported hardware

| Board | Tier | Image |
|---|---|---|
| Pi Zero 2 W | ARMv7 | armhf (32-bit) |
| Pi 2 | ARMv7 | armhf |
| Pi 3 / 3+ | ARMv7 or ARMv8 | armhf or arm64 |
| Pi 4 | ARMv7 or ARMv8 | armhf or arm64 |

**Not supported: Pi Zero W and Pi 1** (ARMv6). Debian/Raspberry Pi OS armhf
targets ARMv7 and will not boot on ARMv6 — use [Pi-Star](https://www.pistar.uk/)
on that hardware. Pick **arm64** on a Pi 3/4 for the best performance; **armhf**
runs on every supported board.

## Easiest: flash from Raspberry Pi Imager's OS list

[Raspberry Pi Imager](https://www.raspberrypi.com/software/) 2.0+ can pull the
Waypoint images straight into its **Choose OS** list from a custom repository — no
manual download, and Imager verifies the SHA-256 for you.

1. Open Imager → the gear / **⚙ App Options** (or Ctrl/⌘-Shift-X).
2. Set **Content Repository** to:
   ```
   https://kn4oqw.github.io/waypoint/os_list_waypoint.json
   ```
3. Back on the main screen, **Choose OS** now lists **Waypoint OS (64-bit)** and
   **Waypoint OS (32-bit)** (filtered to the board you pick under **Choose Device**).
4. **Choose Storage**, set your Wi-Fi + username/password under the advanced
   options (the image ships none — see step 2 below), and **Write**.

CLI equivalent:

```console
$ rpi-imager --repo https://kn4oqw.github.io/waypoint/os_list_waypoint.json
```

> **Which entry?** A Pi 3/4 sees both; choose **64-bit** for best performance. A
> Pi 2 or Zero 2 W sees only **32-bit**. Pi Zero W / Pi 1 are excluded (ARMv6).

> **Schema pin / maintenance note.** The custom-repository JSON follows Raspberry
> Pi Imager's *OS list* JSON Schema (Draft-07). That schema is published on the
> `rpi-imager` **`main`** branch and is **not** versioned, so it can change without
> notice. We vendor a pinned copy at [`scripts/os-list-schema.json`](../scripts/os-list-schema.json)
> and validate every generated `os_list_waypoint.json` against it in CI. If Imager
> changes the schema, regenerate against the new copy and bump the vendored file;
> the generator is [`scripts/gen-imager-json.py`](../scripts/gen-imager-json.py).

Prefer to download the `.img.xz` and flash it yourself? Continue below.

## 1. Verify the download

Every release ships `SHA256SUMS` (signed with the RFC-0013 release key) alongside
the images. Verify both the checksum and the signature before flashing.

```console
# Checksum:
$ sha256sum -c SHA256SUMS
waypoint-v1.0.0-bookworm-arm64.img.xz: OK

# Signature (the public key is docs/waypoint-release.pub in the repo):
$ minisign -Vm SHA256SUMS -P RWRecbiMg67TbiFHluBimEaWz3fXBqGcDo4WZyfN4LHazgHxu2n2sfKd
Signature and comment signature verified
Trusted comment: waypoint v1.0.0 image SHA256SUMS
```

A failed check means a corrupt or tampered download — do not flash it.

## 2. Flash with Raspberry Pi Imager

Use [Raspberry Pi Imager](https://www.raspberrypi.com/software/) with the
downloaded `.img.xz` as a **custom image**:

1. **Choose OS → Use custom** → select `waypoint-<version>-bookworm-<arch>.img.xz`.
2. **Choose Storage** → your SD card.
3. Click the **gear / "Edit settings"** (advanced options) and set:
   - **Hostname** (e.g. `waypoint`).
   - **Wi-Fi** — SSID, password, and your country (or leave off for Ethernet).
   - **Username and password** — this is *your* login for the Pi. The image ships
     none, so **you must set one here** (Bookworm requires a user). Waypoint's own
     web login is separate and set during claim (below).
   - Locale / timezone as you like.
4. **Write**.

> Why set the user here? The image contains no `pi:raspberry` default and does not
> disable Imager's first-boot user seeding — so the only credentials on the device
> are the ones you enter. If you skip this, the Pi has no OS login (the Waypoint
> web UI still comes up, but you cannot SSH in).

## 3. First boot + claim

Insert the card, connect the modem HAT, and power on.

- **First boot takes ~1–3 minutes.** Raspberry Pi OS runs its one-time firstrun
  (creates your user, expands the filesystem, applies Wi-Fi), then `waypointd`
  starts, **mints a self-signed TLS device certificate** (RFC-0012 — near
  instant), and comes up **unclaimed**.
- Find the node's address (your router's DHCP list, or `waypoint.local` via mDNS)
  and browse to **`https://<address>/`** (HTTP on port 80 redirects to HTTPS).
- Your browser will warn about the self-signed certificate — expected for a
  local appliance. Proceed; the cert's SANs cover the device hostname, `.local`
  name, and its IP.
- You land on the **claim screen**: set the Waypoint admin username + password.
  This is the account that manages the hotspot; it is **not** the OS user from
  step 2. Once claimed, the device is yours — the claim flow cannot be re-run
  without a reset.
- Configure your modem, DMR/YSF/P25/… networks, and Wi-Fi/identity from the
  dashboard. Enabling a mode renders its config and starts its gateway on Apply.

## 4. Updates (what happens after)

- **The OS** patches itself: `unattended-upgrades` applies **security** updates
  only, never the kernel/bootloader/firmware, and never reboots on its own.
- **Waypoint's software** updates on your terms: the dashboard's **Updates** tab
  shows available stack/`waypointd` versions; you apply them with a click (or
  opt into an automatic quiet-window). Every update is health-gated and rolls
  back automatically if the modem does not come back up (RFC-0014).

See [docs/updates.md](updates.md) for the full two-path update model.
