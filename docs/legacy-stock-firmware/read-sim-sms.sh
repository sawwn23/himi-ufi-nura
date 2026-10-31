#!/usr/bin/env bash
# Dump all SIM-stored SMS records (EF_SMS 3F00/7F10/6F3C) over QMI (run ON Proxmox host).
# Stop ModemManager first so qmicli can grab a QMI client:  systemctl stop ModemManager
# PIN must be verified once per power cycle: pass $1 or env SIM_PIN.
#
# Record layout: byte0 = status (00 free, 01 read, 03 unread-recv, 05 sent, 07 unsent);
# bytes 1.. = SMS PDU (SMSC len prefix + TPDU). Non-00 records hold a message.

set -uo pipefail
DEV="${DEV:-/dev/cdc-wdm0}"
PIN="${1:-${SIM_PIN:-}}"
Q="qmicli -d $DEV --device-open-qmi"

[[ -n "$PIN" ]] && $Q --uim-verify-pin="PIN1,$PIN" 2>&1 | tail -1

# record count from file attributes
ATTR=$($Q --uim-get-file-attributes="0x3F00,0x7F10,0x6F3C" 2>/dev/null)
echo "$ATTR" | grep -iE 'record size|record count'
N=$(echo "$ATTR" | sed -n 's/.*Record count: *\([0-9]*\).*/\1/p'); N="${N:-40}"

found=0
for r in $(seq 1 "$N"); do
  hex=$($Q --uim-read-record="record-number=$r,record-length=176,file=0x3F00-0x7F10-0x6F3C" 2>/dev/null \
        | sed -n '/Read result/,$p' | grep -oE '[0-9A-F]{2}' | tr -d '\n')
  st="${hex:0:2}"
  if [[ -n "$st" && "$st" != "00" ]]; then
    echo "REC $r status=$st PDU=${hex:2}"
    found=$((found+1))
  fi
done
echo "--- $found message(s) found on SIM ---"
echo "(decode a PDU with an online SMS-PDU decoder, or: python3 -c 'import sys' + a pdu lib)"
