#!/bin/sh
# Refresh device-himi-ufi's source files from nura/overlay/ and ../webapp, recompute the
# APKBUILD checksums and mirror the package into pmbootstrap's pmaports.
# Run after editing anything in overlay/ or webapp/, then (as the user):
#   pmbootstrap build device-himi-ufi
set -eu
here=$(cd "$(dirname "$0")/.." && pwd)
pkg=$here/device-himi-ufi
ov=$here/overlay
pma=${PMAPORTS:-$HOME/.local/var/pmbootstrap/cache_git/pmaports}/device/testing/device-himi-ufi

# overlay file -> flat name in the package dir (the modem files are already kept there)
while read -r src dst; do
	cp "$ov/$src" "$pkg/$dst"
done <<'LIST'
etc/systemd/system/lte-dongle.service lte-dongle.service
etc/sysusers.d/lte-dongle.conf lte-dongle-sysusers.conf
etc/tmpfiles.d/lte-dongle.conf lte-dongle-tmpfiles.conf
etc/polkit-1/rules.d/50-lte-dongle.rules 50-lte-dongle.rules
etc/nftables.d/60_lte_dongle.nft 60_lte_dongle.nft
etc/udev/rules.d/90-lte-dongle-led.rules 90-lte-dongle-led.rules
etc/systemd/system/msm-modem-uim-selection.service.d/retry.conf uim-retry.conf
LIST

# modem files: overlay is the source of truth too
for f in diag-bind himi-ok-guard modem-watchdog sim-autounlock; do cp "$ov/usr/sbin/$f" "$pkg/$f"; done
for u in himi-ok-guard modem-watchdog sim-autounlock; do cp "$ov/etc/systemd/system/$u.service" "$pkg/$u.service"; done
cp "$ov/etc/systemd/system.conf.d/watchdog.conf" "$pkg/watchdog.conf"

# webapp sources, reproducible tarball (built with go in the package's build())
(cd "$here/../webapp" && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
	--exclude=lte-dongle --exclude=lte-dongle.arm64 -cf - go.mod *.go static) \
	| gzip -9n > "$pkg/lte-dongle-webapp.tar.gz"

# checksums
cd "$pkg"
srcs=$(sed -n '/^source="/,/"$/p' APKBUILD | sed 's/^source="//; s/"$//')
sums=$(for s in $srcs; do sha512sum "$s"; done)
awk -v sums="$sums" '/^sha512sums="/{print; print sums; skip=1; next} skip&&/^"/{skip=0} !skip' APKBUILD > APKBUILD.new
mv APKBUILD.new APKBUILD

mkdir -p "$pma"
cp -a "$pkg"/. "$pma"/
echo "synced $pkg -> $pma"
