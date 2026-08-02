#!/bin/bash
# Targeted subset of waypoint-stack/build.sh: only the components Tier 2 needs.
# Same pinned SHAs as pins.env, so these are the binaries that ship.
#
# Every mode with a gateway daemon is built, because every mode now has a Tier 2
# test (modes_test.go). MMDVM-Host itself is still absent: nothing here drives a
# modem, and the loopback tests stand in for the host with repeater.go.
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

build_at() { # name srcdir binaries...
  local name="$1" src="$2"; shift 2
  echo "=== building $name"
  make -C "$src" -j"$(nproc)"
  for bin in "$@"; do
    cp "$src/$bin" "$OUT/"
  done
}

clone_at https://github.com/g4klx/DMRGateway.git    79edbc43962f25b33d383d59f8bf635d24da74b8 /src/DMRGateway
clone_at https://github.com/g4klx/YSFClients.git    2b480aaea7c58f0e29694cd984b8fac0ac7eae99 /src/YSFClients
clone_at https://github.com/g4klx/P25Clients.git    9751c6e91ff159a6eec3afb41c10c845414df08c /src/P25Clients
clone_at https://github.com/g4klx/NXDNClients.git   18b4e9af545ed327b0f0ae54bce57cce6f232393 /src/NXDNClients
clone_at https://github.com/g4klx/M17Gateway.git    c72b989248b14005215469aa24eccc120b46efaa /src/M17Gateway
clone_at https://github.com/g4klx/DAPNETGateway.git 552754628f29ebdba0ea9bfd4710c998a42cd17c /src/DAPNETGateway
clone_at https://github.com/g4klx/DStarGateway.git  612f388727a9bb47aaeaae3a89f5abff3152ed93 /src/DStarGateway

build_at DMRGateway /src/DMRGateway DMRGateway
build_at "YSFClients (DGIdGateway, YSFGateway, YSFParrot)" /src/YSFClients \
  DGIdGateway/DGIdGateway YSFGateway/YSFGateway YSFParrot/YSFParrot
build_at "P25Clients (P25Gateway, P25Parrot)"   /src/P25Clients  P25Gateway/P25Gateway P25Parrot/P25Parrot
build_at "NXDNClients (NXDNGateway, NXDNParrot)" /src/NXDNClients NXDNGateway/NXDNGateway NXDNParrot/NXDNParrot
# M17Gateway's Makefile 'all' builds just the gateway (the echo service is built in).
build_at M17Gateway    /src/M17Gateway    M17Gateway
# DAPNETGateway's Makefile default target builds the one binary in the repo root.
build_at DAPNETGateway /src/DAPNETGateway DAPNETGateway

# DStarGateway's top Makefile 'all' also builds the DGW* helper tools, which need
# nothing this test uses and take longer than the gateway does. Name the gateway
# target explicitly instead. Note the lower-case binary name: the unit and the
# rendered config both call it dstargateway.
echo "=== building DStarGateway"
make -C /src/DStarGateway DStarGateway/dstargateway -j"$(nproc)"
cp /src/DStarGateway/DStarGateway/dstargateway "$OUT/"

echo "=== artifacts"
ls -la "$OUT"
