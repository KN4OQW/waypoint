#!/bin/sh
# Take one DMR loopback capture on the bench node and convert it here.
#
# The node captures and does nothing else; every decision happens on the
# workstation where the result can be read before it is committed. So this
# drives tcpdump over ssh, pulls the pcap back, and runs tools/dmrdcapture over
# it to print what was caught.
#
# Usage:
#   BENCH_PASS=... tools/dmrdcapture/bench-capture.sh <label> [seconds]
#
#   label    names the output files: capture/<label>.pcap and <label>.txt
#   seconds  how long to capture (default 40 — long enough to key a message
#            after reading the prompt, short enough not to fill the disk)
#
# The password is never stored here. Export BENCH_PASS, or let it prompt.
#
# Why a whole script for two commands: the capture has to start BEFORE the radio
# transmits and the operator has to know when that is. Getting the ordering
# wrong wastes a trip to the bench, and the ordering is the only hard part.

set -eu

BENCH_HOST="${BENCH_HOST:-rescue@172.16.50.13}"
OUTDIR="${OUTDIR:-capture}"
LABEL="${1:-}"
SECONDS_TO_RUN="${2:-40}"

if [ -z "$LABEL" ]; then
	echo "usage: $0 <label> [seconds]" >&2
	exit 2
fi

if [ -z "${BENCH_PASS:-}" ]; then
	printf 'bench password for %s: ' "$BENCH_HOST" >&2
	stty -echo 2>/dev/null || true
	read -r BENCH_PASS
	stty echo 2>/dev/null || true
	echo >&2
fi

command -v sshpass >/dev/null 2>&1 || {
	echo "bench-capture: sshpass is not installed" >&2
	exit 1
}

# -q suppresses the image's login banner, which otherwise buries the capture
# output in "SSH may not work until a valid user has been set up".
SSH_OPTS="-q -o StrictHostKeyChecking=no -o PreferredAuthentications=password -o PubkeyAuthentication=no"
REMOTE="/tmp/f1-${LABEL}.pcap"

mkdir -p "$OUTDIR"

# portrange covers the stock pair (62031/62032) and the relay's own two ports
# (62033/62034), so the same filter works whether or not the shim is in the
# path. With the shim running every burst is seen twice — once per leg — which
# is what the -from filter below is for.
echo "== capturing ${SECONDS_TO_RUN}s on ${BENCH_HOST} -> ${REMOTE}"
echo "== KEY THE RADIO NOW (capture is already running)"

# shellcheck disable=SC2086
sshpass -p "$BENCH_PASS" ssh $SSH_OPTS "$BENCH_HOST" \
	"echo \"$BENCH_PASS\" | sudo -S -p '' sh -c \
	'timeout ${SECONDS_TO_RUN} tcpdump -i lo -s 0 -w ${REMOTE} \
	 \"udp and portrange 62031-62034\" 2>/dev/null; chmod 644 ${REMOTE}'" \
	|| true

# shellcheck disable=SC2086
sshpass -p "$BENCH_PASS" scp $SSH_OPTS "${BENCH_HOST}:${REMOTE}" "${OUTDIR}/${LABEL}.pcap" >/dev/null

echo "== ${OUTDIR}/${LABEL}.pcap"
echo
echo "== every frame, both legs:"
go run ./tools/dmrdcapture -in "${OUTDIR}/${LABEL}.pcap" -summary

echo
echo "== radio -> network leg only, as a fixture:"
go run ./tools/dmrdcapture -in "${OUTDIR}/${LABEL}.pcap" -from 62032 \
	-out "${OUTDIR}/${LABEL}.txt" \
	-header "REAL: radio -> network. FILL IN: radio, SMS format, confirmed-data setting, what was typed."
echo "   wrote ${OUTDIR}/${LABEL}.txt — edit its header before committing"
