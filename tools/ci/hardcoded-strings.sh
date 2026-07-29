#!/usr/bin/env bash
# Warn about user-visible English being added to the frontend without going
# through a message catalog (issue #23).
#
# This is a HEURISTIC and a WARNING, never a gate. It looks only at the lines a
# branch ADDS to the scoped files, because the sweep that moved the existing
# strings is not finished — scanning the whole file would drown the signal in
# strings that are already known about and already counted.
#
# What it looks for, on added lines only:
#   - text between > and < inside a template literal or HTML, with no data-i18n
#     attribute on the same line and no msg()/t() call producing it
#   - placeholder / title / aria-label / alt attributes with literal English
#
# What it CANNOT see, and why a green run proves nothing:
#   - strings built by concatenation across several lines
#   - text assigned to .textContent from a variable
#   - anything in a file outside the scoped list
#   - the difference between prose and a protocol token that happens to be a word
#
# The real completeness check is the bracket walk described in
# docs/translations.md: point the UI at a catalog whose every value is its own
# key in brackets and read the pages. Anything still in English was missed.
#
# Usage: tools/ci/hardcoded-strings.sh [base-ref]   (default: origin/main)
set -uo pipefail

BASE="${1:-origin/main}"
FILES="ui/static/index.html ui/static/settings.html ui/static/app.js ui/static/settings.js"

# Words that are protocol/domain tokens rather than copy, so a line containing
# only these is not worth reporting.
TOKENS='DMR|D-?Star|YSF|P25|NXDN|M17|POCSAG|FM|MMDVM|RF|NET|TG|CC|CAN|RAN|IP|IPv4|IPv6|DNS|NTP|VLAN|SSID|UART|GPIO|TCXO|USB|MHz|kHz|Hz|dBm|BER|RSSI|LCD|HD44780|I2C|SPI'

added=$(git diff --unified=0 "$BASE"...HEAD -- $FILES 2>/dev/null \
        | grep -E '^\+[^+]' | sed 's/^+//')

if [ -z "$added" ]; then
  echo "hardcoded-strings: no changes to the scoped frontend files"
  exit 0
fi

hits=$(printf '%s\n' "$added" \
  | grep -vE 'data-i18n|msg\(|WPI18n\.|^\s*(//|\*)' \
  | grep -oE '>[^<>{}`$]*[A-Za-z]{3}[^<>{}`$]*<|(placeholder|title|aria-label|alt)="[^"]*[A-Za-z]{3}[^"]*"' \
  | grep -vE "^>($TOKENS)<$" \
  | sort -u)

count=$(printf '%s' "$hits" | grep -c . || true)
if [ "$count" -eq 0 ]; then
  echo "hardcoded-strings: nothing suspicious in the added lines"
  exit 0
fi

echo "hardcoded-strings: $count added line(s) look like user-visible English"
echo "Move them to ui/static/locales/en-US.json — see docs/translations.md."
printf '%s\n' "$hits" | while IFS= read -r line; do
  [ -n "$line" ] || continue
  echo "::warning title=Possible hardcoded UI string::${line}"
done
exit 0
