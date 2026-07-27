#!/bin/bash
# Targeted subset of waypoint-stack/build.sh: only the components Tier 2 needs.
# Same pinned SHAs as pins.env, so these are the binaries that ship.
set -euo pipefail

OUT=/out
mkdir -p "$OUT" /src

export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -qq -y --no-install-recommends \
  g++ make git ca-certificates libmosquitto-dev nlohmann-json3-dev libboost-dev >/dev/null

clone_at() { # repo sha dir
  git clone -q "$1" "$3"
  git -C "$3" checkout -q "$2"
}

clone_at https://github.com/g4klx/DMRGateway.git 79edbc43962f25b33d383d59f8bf635d24da74b8 /src/DMRGateway
clone_at https://github.com/g4klx/YSFClients.git 2b480aaea7c58f0e29694cd984b8fac0ac7eae99 /src/YSFClients

echo "=== building DMRGateway"
make -C /src/DMRGateway -j"$(nproc)"
cp /src/DMRGateway/DMRGateway "$OUT/"

echo "=== building YSFClients (DGIdGateway, YSFGateway, YSFParrot)"
make -C /src/YSFClients -j"$(nproc)"
cp /src/YSFClients/DGIdGateway/DGIdGateway "$OUT/"
cp /src/YSFClients/YSFGateway/YSFGateway   "$OUT/"
cp /src/YSFClients/YSFParrot/YSFParrot     "$OUT/"

echo "=== artifacts"
ls -la "$OUT"
