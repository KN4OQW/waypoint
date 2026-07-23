#!/usr/bin/env bash
# Download + verify the pinned Raspberry Pi OS Lite (Bookworm) base image for a
# variant into image-<arch>/, ready for the CustomPiOS build.
#
#   image/src/fetch-base-image.sh <armhf|arm64>
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ARCH="${1:?usage: fetch-base-image.sh <armhf|arm64>}"

case "$ARCH" in armhf|arm64) ;; *) echo "arch must be armhf or arm64" >&2; exit 2;; esac

# Pull the pinned URL + sha256 out of the variant config (single source of truth).
# shellcheck disable=SC1090
DIST_PATH="$DIR" source "$DIR/variants/$ARCH/config"
url="$WAYPOINT_BASE_IMAGE_URL"
sha="$WAYPOINT_BASE_IMAGE_SHA256"

dest_dir="$DIR/image-$ARCH"
mkdir -p "$dest_dir"
dest="$dest_dir/$(basename "$url")"

if [ -f "$dest" ] && echo "$sha  $dest" | sha256sum -c - >/dev/null 2>&1; then
  echo "base image already present and verified: $dest"
  exit 0
fi

echo "downloading $url"
curl -fsSL --retry 3 -o "$dest.part" "$url"
echo "$sha  $dest.part" | sha256sum -c -
mv "$dest.part" "$dest"
echo "verified base image: $dest"
