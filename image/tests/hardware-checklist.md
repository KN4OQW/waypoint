# Waypoint image — bench flash/boot acceptance checklist

The SD-card flash + boot on real hardware is the acceptance gate for the image
release pipeline. Run this on a **Pi 3** with the **MMDVM_HS_Dual_Hat on
/dev/ttyAMA0** using the **armhf** image. Tick every box; capture the noted output.

## 0. Flash

- [ ] `sha256sum -c SHA256SUMS` → the armhf `.img.xz` reports `OK`.
- [ ] `minisign -Vm SHA256SUMS -P RWRecbiMg67TbiFHluBimEaWz3fXBqGcDo4WZyfN4LHazgHxu2n2sfKd` → `Signature and comment signature verified`.
- [ ] Flash with Raspberry Pi Imager (Use custom → the `.img.xz`). In advanced
      options set **Wi-Fi** and a **username + password** (the image ships none).
- [ ] Insert the card, seat the modem HAT, power the Pi 3.

## 1. First boot reaches the claim screen over HTTPS

- [ ] Within ~1–3 min the node is reachable. Find its IP (router DHCP) or use
      `waypoint.local`.
- [ ] `https://<ip>/` loads the **claim screen** (set-admin form). HTTP `http://<ip>/`
      **redirects to https**.
- [ ] The browser shows a self-signed-cert warning (expected); proceeding works.

## 2. Certificate SANs are correct

Over SSH (or from a laptop), inspect the served cert:

```
openssl s_client -connect <ip>:443 -servername waypoint.local </dev/null 2>/dev/null \
  | openssl x509 -noout -text | grep -A1 'Subject Alternative Name'
```

- [ ] SANs include the device **hostname**, its **`.local`** mDNS name, and its **IP**.
- [ ] `openssl x509 -noout -subject -issuer` shows it is self-signed (subject == issuer).

## 3. Claim completes

- [ ] Set an admin username + password on the claim screen; submit.
- [ ] You are logged into the dashboard; reloading `https://<ip>/` now shows the
      **login** (not claim) — i.e. the device is claimed.
- [ ] `curl -sk https://<ip>/api/health` returns `{"status":"ok",...}` with the
      image's `waypointd` version.

## 4. MMDVMHost starts and opens the modem

Configure the modem (port `/dev/ttyAMA0`) + at least one mode in the dashboard and
Apply, then over SSH:

- [ ] `systemctl is-active waypoint-mmdvm.service` → `active`.
- [ ] `sudo journalctl -u waypoint-mmdvm.service -b | grep -E "Opening the MMDVM|MMDVM protocol version"`
      shows **`Opening the MMDVM`** and **`MMDVM protocol version: N, description: MMDVM_HS_Dual_Hat…`**
      (the modem firmware banner — proof the modem opened on ttyAMA0).
- [ ] `sudo systemctl status waypoint-mmdvm.service` shows it running steadily
      (not crash-looping / auto-restart).

## 5. unattended-upgrades selects only security origins and skips waypoint-\*

```
sudo unattended-upgrade --dry-run --debug 2>&1 | tee /tmp/uu.log
```

- [ ] `grep -E "Allowed origins are:" /tmp/uu.log` lists **only** the security
      origins — for armhf: `o=Raspbian` and `o=Raspberry Pi Foundation`
      (no Debian-Security on armhf); the **Waypoint** origin is **absent**.
- [ ] `grep -iE "waypoint-" /tmp/uu.log` shows the waypoint-\* packages are **not**
      selected for upgrade (they are held). If any appear, they are listed under
      "not upgraded" / held, never "installing".
- [ ] `grep -E "raspberrypi-kernel|linux-image|raspi-firmware|bootloader" /tmp/uu.log`
      shows any such candidates are blacklisted (not selected).
- [ ] `apt-mark showhold | grep -c waypoint-` → matches the number of installed
      waypoint-\* packages (all held).

## 6. SD longevity + no secrets (spot-check)

- [ ] `journalctl --disk-usage` reports the journal in **volatile** storage
      (`/run/log/journal`), not `/var/log/journal`.
- [ ] `ls /home/pi-star/waypoint/tls/` was empty at first boot; a cert now exists
      (minted on first start, not shipped).

---

When every box is ticked, paste the captured outputs (cert SANs, MMDVM banner,
the `Allowed origins are:` line, `apt-mark showhold`) into the PR and mark it
ready for review.
