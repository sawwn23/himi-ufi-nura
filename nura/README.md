# Nura on the HiMiLink 4G UFI dongle

Mainline Linux (Nura, formerly postmarketOS) running on the dongle, working as of 2026-10-30:
it boots, joins Wi-Fi as `dongle.local`, unlocks the SIM and receives SMS without needing the laptop.

## Layout

| Path | What |
|---|---|
| `dts/msm8916-himi-ufi-esim.dts` | The board's device tree (SIM-routing GPIOs, verified LEDs, button). `.dtb` = current build |
| `dts/qcom-msm8916-himi-ufi-esim.dts` | armv7 wrapper. Unused: stock 32-bit firmware was the real blocker |
| `dts/debug/` | Diagnostic variants: LED heartbeat/panic triggers, modem/Wi-Fi disabled |
| `device-himi-ufi/` | Mirror of the local Nura device package (canonical copy lives in pmbootstrap's pmaports, `device/testing/`) |
| `overlay/` | Files installed on the stick by hand, at their target paths (see below) |
| `fw/` | Bootloader notes, the lk2nd patch and checksums of the flashed set (binaries not included, see `fw/README.md`) |
| `scripts/` | Helpers, listed below |

## Setup

`scripts/build-dtb.sh` needs a sparse checkout of `msm8916-mainline/linux` tag `v6.12.1-msm8916` at `nura/pmos-dts`
(only `arch/arm*/boot/dts/qcom`, `include`, `scripts/dtc/include-prefixes`):

    git clone --filter=blob:none --no-checkout --depth 1 --branch v6.12.1-msm8916 \
        https://github.com/msm8916-mainline/linux nura/pmos-dts
    git -C nura/pmos-dts sparse-checkout set arch/arm/boot/dts/qcom arch/arm64/boot/dts/qcom include scripts/dtc/include-prefixes
    git -C nura/pmos-dts checkout

Build the OS image with pmbootstrap. Put `device-himi-ufi/` into the pmaports tree as `device/testing/device-himi-ufi/`,
run `scripts/sync-package.sh` (regenerates the webapp tarball and checksums; the tarball is not committed),
then `pmbootstrap build device-himi-ufi` and `pmbootstrap install`.

## Scripts

- `scripts/build-dtb.sh [src.dts] [out.dtb]` builds against `pmos-dts`. With no arguments it rebuilds `dts/msm8916-himi-ufi-esim.dtb`.
- `scripts/mkboot.py <dtb> <out.img> [--drop w..] [--add w..]` repacks pmbootstrap's `boot.img` with another DTB or cmdline,
  for RAM tests (`fastboot boot`). Set `BOOT_DIR` to pick the rootfs chroot.
- `DONGLE_PW=... scripts/drun.py '<cmd>'` runs a root command on the stick via `run0`. Target is `DONGLE_HOST`
  (default `dongle.local`). The password is only read from the environment.
- `DONGLE_PW=... scripts/deploy.sh` builds `../webapp` for arm64 and installs it plus all of `overlay/` on the stick, then (re)starts the services.
- `scripts/dsh.sh '<cmd>' [wait]` runs a command in the initramfs debug shell (telnet 172.16.42.1:23, no-rootfs boots only).

## Boot chain on the eMMC

New GPT → DragonBoard 410c `sbl1`/`rpm`/`tz` → `qhypstub` (hyp) → **lk1st** (`aboot`, detects
"HiMiLink 4G UFI (eSIM-switch rev)") → `/boot/extlinux/extlinux.conf` inside the Nura rootfs in `userdata`.
The `boot` partition holds an older boot.img that's only used if extlinux is missing. The stock 32-bit TZ has no PSCI,
so every mainline kernel (arm64 and armv7) died before driver probe until this firmware was replaced.

## On-device customizations (`overlay/`)

`overlay/` is the source of truth. `scripts/sync-package.sh` copies it (plus a tarball of `../webapp`) into `device-himi-ufi` (`pkgrel=4`), recomputes the APKBUILD checksums and mirrors the package into pmbootstrap's pmaports. Then the user runs `pmbootstrap build device-himi-ufi`. The package builds `diag_nv` and `lte-dongle` with `go`, installs every file below, and its post-install sets avahi to `allow-interfaces=wlan0` (it edits the avahi-owned config instead of shipping a conflicting copy). `lte-dongle.service` is installed but not enabled: set the password first, then `systemctl enable --now lte-dongle`. `deploy.sh` is still the fast way to push changes to a running stick.

| File | Purpose |
|---|---|
| `/usr/sbin/sim-autounlock` + `sim-autounlock.service` | Unlock the SIM from `/etc/sim-pin` (root 0600, **not** in this repo), then enable the modem. Only tries when all 3 PIN retries remain, so it can never cause a PUK lock |
| `lte-dongle.service`, `sysusers.d/`, `tmpfiles.d/lte-dongle.conf` | Web UI on :8080 as unprivileged user `lte-dongle`, state in `/var/lib/lte-dongle` (password hash + session key) |
| `polkit-1/rules.d/50-lte-dongle.rules` | Lets `lte-dongle` use MM Device.Control/Messaging/USSD without a login session |
| `nftables.d/60_lte_dongle.nft` | Opens tcp/8080 on `wlan*` and `usb*` only |
| `himi-ok-guard` + `.service`, `diag-bind`, `diag_nv` (source in `tools/diag_nv`) | Keep modem NV 2500 = `HiMI_OK` so the firmware doesn't crash ~15 min after every modem start |
| `modem-watchdog` + `.service`, `system.conf.d/watchdog.conf` | Soft-restart the modem on a firmware crash; reboot when stuck unregistered; feed the hardware watchdog |
| `msm-modem-uim-selection.service.d/retry.conf` | Upstream UIM selection fails intermittently and ModemManager `Requires=` it. Retry 3×, never fail |

Also done by hand on the stick:
- Wi-Fi profile for your network (autoconnect). Created by the user with `nmcli --ask`.
- `deviceinfo_generate_extlinux_config="true"` added to `/usr/share/deviceinfo/deviceinfo`, the new DTB copied to `/usr/share/dtb/`, then `mkinitfs`.
  Package `pkgrel=2` already contains both.

## Known quirks

- `modem-watchdog` stop/starts the modem's remoteproc on a firmware crash (soft restart, ~1 min; reboot only as fallback), re-runs `sim-autounlock` if the SIM is locked for 2 min,
  and reboots when the modem stays unregistered for 10 min (with back-off). The PM8916 PMIC watchdog
  (`RuntimeWatchdogSec=60s`) resets a hung system.
- The modem firmware used to crash ~15 min after every modem start (`lte_ml1_common_timer.c:390`). Cause: it compares NV item 2500 with `HiMI_OK` at ~300 s uptime and self-destructs ~10 min later; the modem clears the item every boot. `himi-ok-guard` now rewrites it (verified 2026-10-30: 0 crashes after a full reboot). If the modem still crashes, the kernel restart leaves it SIM-locked/"searching"; `modem-watchdog` soft-restarts it.
- ModemManager 1.25.95 crashes (SIGABRT) once right after every SIM unlock. systemd restarts it and it registers normally.
- After a software reboot over USB, the laptop doesn't re-enumerate the USB gadget (VBUS stays up). Over Wi-Fi a reboot is fine (~32 s).
- No RTC battery: the clock is wrong until NTP syncs over Wi-Fi.
- LEDs: gpio72 = red (power), gpio73 = green (Wi-Fi associated), gpio71 = blue (free). gpio36 has no visible LED.

## Recovery

EDL works whatever is flashed: hold the reset button while plugging in. To fully restore stock, use your own EDL backup
(stock GPTs `stock-gpt-primary-34sectors.bin` at sector 0 and `stock-gpt-backup-last33sectors.bin` at sector
7634911, then every partition by name).
