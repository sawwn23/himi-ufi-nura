#!/usr/bin/env bash
# Bring the dongle up as a ModemManager modem (run ON the Proxmox host).
# Assumes the dongle is already in modem mode (./toggle-usb-mode.sh 0) and /dev/cdc-wdm0 exists.
# Pass the SIM PIN as $1 or env SIM_PIN. PIN is only needed while the SIM is locked.
#
# Usage:  SIM_PIN=1234 ./modem-up.sh        or     ./modem-up.sh 1234

set -uo pipefail
PIN="${1:-${SIM_PIN:-}}"

# qmi_wwan needs both the usbmisc (cdc-wdm) and net (wwpXXX) ports tagged for MM.
udevadm trigger --subsystem-match=net --subsystem-match=usbmisc --subsystem-match=tty 2>/dev/null
udevadm settle
systemctl restart ModemManager
# wait for the modem object to appear
for i in $(seq 1 12); do
  mmcli -L 2>/dev/null | grep -q 'Modem/' && break
  sleep 3
done
mmcli -L

# unlock if locked (ignores error if already unlocked)
if [[ -n "$PIN" ]]; then
  mmcli -i 0 --pin="$PIN" 2>&1 | tail -1
  sleep 4
fi
mmcli -m 0 --enable 2>&1 | tail -1
sleep 5

echo "=== state ==="
mmcli -m 0 2>/dev/null | grep -iE 'state:|signal|operator (name|id)|registration|access tech'
echo "=== sms ==="
mmcli -m 0 --messaging-list-sms 2>&1 | tail -5
