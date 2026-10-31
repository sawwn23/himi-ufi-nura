#!/bin/sh
# Build the webapp for arm64 and install it plus everything in nura/overlay/ on the stick.
#
# usage: DONGLE_PW=... nura/scripts/deploy.sh
# Target is DONGLE_HOST (default dongle.local). lte-dongle is only (re)started once its
# password exists; the first time, set one as printed at the end.
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
host=${DONGLE_HOST:-dongle.local}
: "${DONGLE_PW:?set DONGLE_PW to the stick user password}"

(cd "$here/../webapp" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	mise exec -- go build -trimpath -ldflags='-s -w' -o lte-dongle.arm64 .)
mkdir -p "$here/overlay/usr/bin"
(cd "$here/tools/diag_nv" && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
	mise exec -- go build -trimpath -ldflags='-s -w' -o "$here/overlay/usr/bin/diag_nv" .)

ssh "$host" 'rm -rf ~/.lte-stage && mkdir -p ~/.lte-stage/usr/bin'
scp -q "$here/../webapp/lte-dongle.arm64" "$host:.lte-stage/usr/bin/lte-dongle"
(cd "$here/overlay" && tar cf - .) | ssh "$host" 'tar xf - -C ~/.lte-stage'

DONGLE_HOST=$host "$here/scripts/drun.py" '
set -e
s=$(getent passwd '"$(ssh "$host" whoami)"' | cut -d: -f6)/.lte-stage
cd $s
find . -type f | while read -r f; do
	case "$f" in */bin/*|*/sbin/*) m=755 ;; *) m=644 ;; esac
	install -D -m $m "$f" "/${f#./}"
done
cd /; rm -rf $s
# binaries used to live in /usr/local (Alpine packages may not use it)
rm -f /usr/local/bin/lte-dongle /usr/local/bin/diag_nv /usr/local/sbin/diag-bind /usr/local/sbin/himi-ok-guard /usr/local/sbin/modem-watchdog /usr/local/sbin/sim-autounlock
systemd-sysusers
systemd-tmpfiles --create /etc/tmpfiles.d/lte-dongle.conf
systemctl daemon-reexec
udevadm control --reload
udevadm trigger --subsystem-match=leds --action=add
systemctl try-restart avahi-daemon
systemctl restart nftables
systemctl enable sim-autounlock modem-watchdog >/dev/null 2>&1
systemctl restart modem-watchdog
if [ -f /var/lib/lte-dongle/passwd ]; then
	systemctl enable lte-dongle >/dev/null 2>&1
	systemctl restart lte-dongle
	sleep 2; echo "lte-dongle: $(systemctl is-active lte-dongle)"
else
	echo "NEEDS_PASSWORD"
fi
echo "modem-watchdog: $(systemctl is-active modem-watchdog)"
echo "$(systemctl show -p RuntimeWatchdogUSec)"
'
