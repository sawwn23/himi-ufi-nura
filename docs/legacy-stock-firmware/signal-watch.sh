#!/usr/bin/env bash
# Live LTE signal + registration monitor for the 4G UFI dongle (run ON the Proxmox host).
# Watch this while repositioning the dongle / antenna. Ctrl-C to stop.
#
# Targets for a reliable LTE attach:  RSRP better than ~-110 dBm  AND  SNR positive (> 0 dB).
# "camped" = hears the tower but not attached;  reg state "registered" = success.
#
# Requires the dongle in modem mode with /dev/cdc-wdm0, and the SIM PIN verified once:
#   qmicli -d /dev/cdc-wdm0 --device-open-qmi --uim-verify-pin=PIN1,<PIN>
# Stop ModemManager so qmicli owns the QMI channel:  systemctl stop ModemManager

set -uo pipefail
DEV="${DEV:-/dev/cdc-wdm0}"
Q="qmicli -d $DEV --device-open-qmi"

trap 'echo; echo "stopped."; exit 0' INT

printf '%-9s %-10s %-10s %-9s %-22s %s\n' TIME RSSI RSRP SNR REG OPERATOR
while true; do
  sig=$($Q --nas-get-signal-info 2>/dev/null)
  rssi=$(echo "$sig" | sed -n 's/.*RSSI: *.\(-\?[0-9]*\) dBm.*/\1/p' | head -1)
  rsrp=$(echo "$sig" | sed -n 's/.*RSRP: *.\(-\?[0-9]*\) dBm.*/\1/p' | head -1)
  snr=$(echo  "$sig" | sed -n "s/.*SNR: *.\(-\?[0-9.]*\) dB.*/\1/p" | head -1)

  srv=$($Q --nas-get-serving-system 2>/dev/null)
  reg=$(echo "$srv" | sed -n "s/.*Registration state: *'\([^']*\)'.*/\1/p" | head -1)
  op=$(echo  "$srv" | sed -n "s/.*Description: *'\([^']*\)'.*/\1/p" | head -1)
  [ -z "$op" ] && op=$($Q --nas-get-system-info 2>/dev/null | sed -n "s/.*Domain: *'\([^']*\)'.*/(\1)/p" | head -1)

  printf '%-9s %-10s %-10s %-9s %-22s %s\n' \
    "$(date +%H:%M:%S)" "${rssi:-?}dBm" "${rsrp:-?}dBm" "${snr:-?}dB" "${reg:-?}" "${op:-}"
  sleep 3
done
