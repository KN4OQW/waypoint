#!/usr/bin/env bash
# Assert a built Waypoint image matches the module's contract. Reusable by CI.
#
#   image/tests/verify-image.sh --rootfs <dir>     # an already-mounted root fs
#   image/tests/verify-image.sh --image <file.img> # loopback-mount, then verify
#
# --image needs root (losetup/mount) — run it in a privileged context. --rootfs
# needs only read access to the mounted tree.
set -uo pipefail

KEYRING_SHA256="aa4641f449f5ca7364079e41b66ecd74175855d21c1fd7e414451b87a4f67ec2"

ROOTFS=""
IMAGE=""
while [ $# -gt 0 ]; do
  case "$1" in
    --rootfs) ROOTFS="$2"; shift 2;;
    --image)  IMAGE="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done

pass=0; fail=0
ok()   { echo "  PASS: $*"; pass=$((pass+1)); }
bad()  { echo "  FAIL: $*"; fail=$((fail+1)); }
have() { [ -e "$ROOTFS$1" ]; }

# --- if given an .img, loopback-mount its root partition and re-enter ---
if [ -n "$IMAGE" ]; then
  command -v losetup >/dev/null || { echo "losetup required for --image" >&2; exit 2; }
  loopdev="$(losetup --show -fP "$IMAGE")" || { echo "losetup failed" >&2; exit 2; }
  mnt="$(mktemp -d)"
  # Root is partition 2 on a Raspberry Pi OS image (1 = boot FAT, 2 = ext4 root).
  cleanup() { umount "$mnt/boot/firmware" 2>/dev/null; umount "$mnt" 2>/dev/null; losetup -d "$loopdev" 2>/dev/null; rmdir "$mnt" 2>/dev/null; }
  trap cleanup EXIT
  mount "${loopdev}p2" "$mnt" || { echo "mount root failed" >&2; exit 2; }
  mount "${loopdev}p1" "$mnt/boot/firmware" 2>/dev/null || true
  ROOTFS="$mnt"
fi

[ -n "$ROOTFS" ] || { echo "give --rootfs or --image" >&2; exit 2; }
[ -d "$ROOTFS" ] || { echo "rootfs '$ROOTFS' not a directory" >&2; exit 2; }
echo "=== verifying Waypoint image at $ROOTFS"

DEB_ARCH="$(cat "$ROOTFS/var/lib/dpkg/arch" 2>/dev/null | head -1)"
[ -n "$DEB_ARCH" ] && ok "target arch: $DEB_ARCH" || DEB_ARCH="unknown"

# 1. apt source
if have /etc/apt/sources.list.d/waypoint.sources && \
   grep -q 'URIs: https://kn4oqw.github.io/waypoint-stack' "$ROOTFS/etc/apt/sources.list.d/waypoint.sources" && \
   grep -q 'Signed-By: /usr/share/keyrings/waypoint-archive-keyring.gpg' "$ROOTFS/etc/apt/sources.list.d/waypoint.sources"; then
  ok "waypoint.sources present, public URL + Signed-By keyring"
else bad "waypoint.sources missing or wrong"; fi
# No build-time temp source left behind.
have /etc/apt/sources.list.d/waypoint-build.sources && bad "temp build source left in image" || ok "no temp build source"

# 2. keyring sha256
if have "/usr/share/keyrings/waypoint-archive-keyring.gpg"; then
  got="$(sha256sum "$ROOTFS/usr/share/keyrings/waypoint-archive-keyring.gpg" | awk '{print $1}')"
  [ "$got" = "$KEYRING_SHA256" ] && ok "keyring sha256 matches pin" || bad "keyring sha256 mismatch ($got)"
else bad "keyring missing"; fi

# 3. waypoint-stack + waypointd installed
status="$ROOTFS/var/lib/dpkg/status"
grep -q '^Package: waypoint-stack$' "$status" 2>/dev/null && ok "waypoint-stack installed" || bad "waypoint-stack not installed"
grep -q '^Package: waypoint-mmdvmhost$' "$status" 2>/dev/null && ok "waypoint-mmdvmhost installed" || bad "waypoint-mmdvmhost not installed"
have /usr/bin/MMDVM-Host && ok "/usr/bin/MMDVM-Host present" || bad "/usr/bin/MMDVM-Host missing"
have /home/pi-star/waypoint/bin/waypointd && ok "waypointd installed at RFC-0014 path" || bad "waypointd missing"
grep -q '^Package: mosquitto$' "$status" 2>/dev/null && ok "mosquitto broker installed" || bad "mosquitto not installed"

# 4. units enabled (symlinks in multi-user.target.wants), boot-check hook present
wants="$ROOTFS/etc/systemd/system/multi-user.target.wants"
[ -L "$wants/waypointd.service" ] && ok "waypointd.service enabled" || bad "waypointd.service not enabled"
[ -L "$wants/mosquitto.service" ] && ok "mosquitto.service enabled" || bad "mosquitto.service not enabled"
grep -q 'update-boot-check' "$ROOTFS/etc/systemd/system/waypointd.service" 2>/dev/null \
  && ok "waypointd.service has the -update-boot-check ExecStartPre hook" || bad "boot-check hook missing"
# Gateway units present but NOT enabled (waypointd enables on Apply).
have /etc/systemd/system/waypoint-mmdvm.service && ok "gateway units present" || bad "gateway units missing"
[ -L "$wants/waypoint-mmdvm.service" ] && bad "gateway unit wrongly enabled on a fresh image" || ok "gateways not enabled on a fresh node"

# 5. unattended-upgrades config exactly as specified (per arch)
uu="$ROOTFS/etc/apt/apt.conf.d/51waypoint-unattended-upgrades"
if have "/etc/apt/apt.conf.d/51waypoint-unattended-upgrades"; then
  grep -q 'label=Raspberry Pi Foundation' "$uu" && ok "u-u: Raspberry Pi Foundation origin" || bad "u-u: RPi origin missing"
  if [ "$DEB_ARCH" = "arm64" ]; then
    grep -q 'label=Debian-Security' "$uu" && ok "u-u: Debian-Security origin (arm64)" || bad "u-u: Debian-Security origin missing (arm64)"
    grep -q 'label=Raspbian' "$uu" && bad "u-u: Raspbian origin should not be on arm64" || ok "u-u: no Raspbian origin on arm64"
  else
    grep -q 'label=Raspbian' "$uu" && ok "u-u: Raspbian origin (armhf)" || bad "u-u: Raspbian origin missing (armhf)"
    grep -q 'label=Debian-Security' "$uu" && bad "u-u: Debian-Security should not be on armhf" || ok "u-u: no Debian-Security origin on armhf"
  fi
  grep -q 'label=Waypoint' "$uu" && bad "u-u: Waypoint origin must be EXCLUDED" || ok "u-u: Waypoint origin excluded"
  for b in raspberrypi-kernel raspberrypi-bootloader 'linux-image-.\*' raspi-firmware; do
    grep -q "$b" "$uu" && ok "u-u blacklist: $b" || bad "u-u blacklist missing: $b"
  done
  grep -qi 'Automatic-Reboot "false"' "$uu" && ok "u-u: Automatic-Reboot false" || bad "u-u: Automatic-Reboot not false"
  grep -q 'Update-Package-Lists "1"' "$ROOTFS/etc/apt/apt.conf.d/20auto-upgrades" 2>/dev/null \
    && ok "u-u: periodic enabled" || bad "u-u: periodic not enabled"
else bad "51waypoint-unattended-upgrades missing"; fi

# needrestart drop-in
nr="$ROOTFS/etc/needrestart/conf.d/waypoint.conf"
grep -q "restart. = 'l'" "$nr" 2>/dev/null && grep -q 'kernelhints. = -1' "$nr" 2>/dev/null \
  && ok "needrestart: no auto-restart, no kernel hints" || bad "needrestart drop-in wrong/missing"

# 6. holds present (waypoint-* on hold)
selections="$ROOTFS/var/lib/dpkg/status"
held="$(awk '/^Package: waypoint-/{p=$2} /^Status:.*hold/{print p}' "$selections" 2>/dev/null | wc -l)"
[ "$held" -ge 1 ] && ok "waypoint-* packages on hold ($held)" || bad "no waypoint-* holds found"

# 7. no TLS material
shopt -s nullglob
certs=("$ROOTFS"/home/pi-star/waypoint/tls/*)
[ ${#certs[@]} -eq 0 ] && ok "no TLS certs shipped (RFC-0012 mints on first boot)" || bad "TLS material present: ${certs[*]}"

# 8. no claim state / no store
have /home/pi-star/waypoint/config.db && bad "config store present (would imply a configured/claimed node)" || ok "no config store (unclaimed)"

# D5: no default user credential
have /boot/firmware/userconf.txt || have /boot/userconf.txt
if have /boot/firmware/userconf.txt || have /boot/userconf.txt; then bad "userconf.txt present (default credential!)"; else ok "no default userconf.txt credential"; fi

# 9. journald volatile
jc="$ROOTFS/etc/systemd/journald.conf.d/waypoint-volatile.conf"
grep -q '^Storage=volatile' "$jc" 2>/dev/null && grep -q '^RuntimeMaxUse=' "$jc" 2>/dev/null \
  && ok "journald volatile with bounded RuntimeMaxUse" || bad "journald not volatile/bounded"

# serial: modem UART freed
bootdir="$ROOTFS/boot/firmware"; [ -d "$bootdir" ] || bootdir="$ROOTFS/boot"
if [ -f "$bootdir/config.txt" ]; then
  grep -q '^enable_uart=1' "$bootdir/config.txt" && grep -q '^dtoverlay=disable-bt' "$bootdir/config.txt" \
    && ok "serial: enable_uart + disable-bt (ttyAMA0 freed for the modem)" || bad "serial: config.txt not set for the modem"
  grep -qE 'console=(serial0|ttyAMA0|ttyS0)' "$bootdir/cmdline.txt" 2>/dev/null \
    && bad "serial console still on cmdline.txt" || ok "serial: no serial login console"
else echo "  NOTE: boot partition not mounted; skipping config.txt/cmdline.txt checks"; fi

# minisign is build-only — must NOT be on the finished image
grep -q '^Package: minisign$' "$status" 2>/dev/null && bad "minisign left on image (should be build-only)" || ok "minisign not on image (build-only)"

echo ""
echo "=== $pass passed, $fail failed"
[ "$fail" -eq 0 ]
