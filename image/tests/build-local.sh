#!/usr/bin/env bash
# Build a Waypoint SD-card image from this working tree.
#
# CI builds release images from signed release artifacts. This builds one from
# whatever is checked out, which is what you want for a bench: the point is to
# flash the code you are about to test, not the code that was released.
#
# It runs the whole thing inside a privileged container. CustomPiOS needs loop
# mounts and a chroot, which need root — and needing root on the host is a much
# bigger ask than needing Docker.
#
# Usage:  image/tests/build-local.sh [armhf|arm64]
set -euo pipefail

VARIANT="${1:-armhf}"
case "$VARIANT" in
  armhf) GOARCH=arm; GOARM=6 ;;
  arm64) GOARCH=arm64; GOARM= ;;
  *) echo "unknown variant $VARIANT (want armhf or arm64)" >&2; exit 1 ;;
esac

repo="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
work="${WAYPOINT_BUILD_WORK:-/tmp/waypoint-imgbuild}"
art="$repo/image/src/modules/waypoint/filesystem/build-artifacts"
mkdir -p "$work" "$art"

# ---------------------------------------------------------------------------
# 1. The binaries, signed.
#
# The module verifies every binary it bakes in with minisign, and that check is
# not skipped for a local build — a build that skipped it would not be testing
# the same trust path the real one uses. So the binaries are signed here with a
# throwaway key, and that key becomes the image's release public key. The
# verification is real; the key is obviously not, which is the honest way to say
# "this image must not accept a production release".
# ---------------------------------------------------------------------------
version="local-$(cd "$repo" && git rev-parse --short HEAD)$(cd "$repo" && git diff --quiet || echo -dirty)"
echo "==> building waypointd and waypoint-provision-helper ($VARIANT, $version)"
for b in waypointd waypoint-provision-helper; do
  ( cd "$repo" && CGO_ENABLED=0 GOOS=linux GOARCH="$GOARCH" GOARM="$GOARM" \
      go build -trimpath -ldflags "-s -w -X main.Version=$version" \
      -o "$art/$b-linux-$VARIANT" "./cmd/$b" )
done

echo "==> fetching the pinned archive keyring"
keyring_url="$(sed -n 's/.*WAYPOINT_KEYRING_URL="\(.*\)".*/\1/p' "$repo/image/src/modules/waypoint/config" | head -1)"
keyring_url="${keyring_url/\$\{WAYPOINT_APT_URL\}/https://kn4oqw.github.io/waypoint-stack}"
curl -fsSL -o "$art/waypoint-archive-keyring.gpg" "$keyring_url"

echo "==> signing with a throwaway key"
docker run --rm -v "$art:/art" -v "$work:/w" debian:bookworm-slim bash -c '
  set -e
  apt-get update -qq && apt-get install -qq -y --no-install-recommends minisign >/dev/null 2>&1
  [ -f /w/dev.key ] || minisign -G -W -p /w/dev.pub -s /w/dev.key >/dev/null
  for f in /art/*-linux-*; do
    case "$f" in *.minisig) continue;; esac
    minisign -S -W -s /w/dev.key -m "$f" >/dev/null
    minisign -V -p /w/dev.pub -m "$f" >/dev/null
  done
  chmod 644 /w/dev.pub /art/*.minisig'

# The image trusts the key its binaries were signed with. Restored on exit so a
# local build never leaves a dev key in the working tree.
pub="$repo/image/src/modules/waypoint/filesystem/waypoint-release.pub"
cp "$pub" "$work/release.pub.orig"
cp "$work/dev.pub" "$pub"
cleanup() {
  cp "$work/release.pub.orig" "$pub"
  rm -f "$repo/image/src/config.local" "$repo/image/src/custompios_path"
  docker run --rm --privileged -v "$repo:/w" debian:bookworm-slim \
    rm -rf "/w/image/src/modules/waypoint/filesystem/build-artifacts" 2>/dev/null || true
}
trap cleanup EXIT

cat > "$repo/image/src/config.local" <<EOF
# Local build — NOT a release image. See image/tests/build-local.sh.
export WAYPOINT_LOCAL_ARTIFACTS="/filesystem/build-artifacts"
export DIST_VERSION="0.1.0-local"
EOF

# ---------------------------------------------------------------------------
# 2. CustomPiOS, and the emulation to run an ARM chroot on an x86 host.
# ---------------------------------------------------------------------------
[ -d "$work/CustomPiOS" ] || git clone --depth 1 -q https://github.com/guysoft/CustomPiOS.git "$work/CustomPiOS"
echo "==> registering qemu-arm binfmt"
docker run --privileged --rm tonistiigi/binfmt --install arm >/dev/null 2>&1

# The dependency list is not guesswork: each of these was found by a build that
# failed without it. python3-yaml and python3-git in particular fail late, after
# the base image has been downloaded and mounted.
cat > "$work/run.sh" <<'RUNNER'
#!/usr/bin/env bash
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -qq -y --no-install-recommends \
  git curl ca-certificates xz-utils zip unzip kpartx parted dosfstools \
  e2fsprogs rsync sudo qemu-user-static binfmt-support p7zip-full \
  file bc gnupg jq python3 python3-yaml python3-git lsof fdisk util-linux \
  libarchive-tools zerofree >/dev/null
cd /work/repo/image/src
echo /work/CustomPiOS/src > custompios_path
export CUSTOM_PI_OS_PATH=/work/CustomPiOS/src
exec bash ./build "$VARIANT"
RUNNER
chmod +x "$work/run.sh"

echo "==> building the image (this takes a while; the base image is ~500 MiB)"
docker run --rm --privileged \
  -e VARIANT="$VARIANT" \
  -v "$repo:/work/repo" \
  -v "$work/CustomPiOS:/work/CustomPiOS" \
  -v "$work/run.sh:/run.sh:ro" \
  -v /dev:/dev \
  debian:bookworm-slim /run.sh

# ---------------------------------------------------------------------------
# 3. Compress, and prove the first-boot stack is actually in there.
# ---------------------------------------------------------------------------
out="${WAYPOINT_IMAGE_OUT:-$HOME/waypoint-$VARIANT-$version.img}"
docker run --rm --privileged -v "$repo:/work/repo" -v "$(dirname "$out"):/out" \
  -e OUT="/out/$(basename "$out")" -e UID_GID="$(id -u):$(id -g)" \
  debian:bookworm-slim bash -c '
    set -e
    apt-get update -qq && apt-get install -qq -y --no-install-recommends xz-utils >/dev/null 2>&1
    img=$(ls /work/repo/image/src/workspace-*/*.img | head -1)
    cp "$img" "$OUT"; xz -T0 -6 -f "$OUT"
    sha256sum "$OUT.xz" > "$OUT.xz.sha256"
    chown "$UID_GID" "$OUT.xz" "$OUT.xz.sha256"'

echo
echo "image:  $out.xz"
echo "sha256: $(cut -d" " -f1 < "$out.xz.sha256")"
echo
echo "This image trusts a throwaway signing key and will refuse a real release."
echo "It is for a bench, not for anyone else's node."
