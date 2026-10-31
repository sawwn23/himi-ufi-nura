#!/usr/bin/env bash
# Toggle the 4G UFI dongle between RNDIS tether mode and modem (QMI) mode,
# using the vendor's own authenticated web API (admin/admin).
#
# Usage:  ./toggle-usb-mode.sh 0     # -> modem mode (exposes /dev/cdc-wdm0 for SMS/QMI)
#         ./toggle-usb-mode.sh 1     # -> RNDIS tether / MiFi router mode (original)
#
# Run this ON the Proxmox host (where the dongle + adb + web UI live), or wrap in ssh.
# The web UI is on the device's loopback; adb-forward it first if needed:
#   adb forward tcp:18000 tcp:8000   (then set BASE to 127.0.0.1:18000)

set -euo pipefail
WANT="${1:-}"
BASE="${BASE:-http://127.0.0.1:18000}"   # adb-forwarded device web UI
USER="${WEBUSER:-admin}"
PASS="${WEBPASS:-admin}"

if [[ "$WANT" != "0" && "$WANT" != "1" ]]; then
  echo "usage: $0 <0|1>   (0=modem/QMI mode, 1=RNDIS tether mode)" >&2
  exit 1
fi

# ensure the web UI is reachable
command -v adb >/dev/null && adb forward tcp:18000 tcp:8000 >/dev/null 2>&1 || true

S=$(curl -s -m 10 -X POST "$BASE/himiapi/json" \
      -H 'Content-Type: application/json' \
      -d "{\"cmdid\":\"login\",\"username\":\"$USER\",\"password\":\"$PASS\"}" \
    | sed -n 's/.*"session":"\([^"]*\)".*/\1/p')
[[ -n "$S" ]] || { echo "login failed" >&2; exit 1; }
echo "logged in (session=$S)"

echo "current:"; curl -s -m 10 -X POST "$BASE/himiapi/json" \
  -H 'Content-Type: application/json' -H "Authorization: $S" \
  -d "{\"cmdid\":\"gethimiusbtether\",\"sessionId\":\"$S\"}"; echo

echo "setting usbstatus=$WANT ..."
curl -s -m 10 -X POST "$BASE/himiapi/json" \
  -H 'Content-Type: application/json' -H "Authorization: $S" \
  -d "{\"cmdid\":\"sethimiusbtether\",\"params\":\"$WANT\",\"sessionId\":\"$S\"}"; echo
echo "done. wait ~12s for USB re-enumeration."
