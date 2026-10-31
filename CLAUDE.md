# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Tooling for a **HiMiLink "4G UFI" LTE USB dongle** (Qualcomm MSM8916, roaming SIM on Rogers LTE B5).
The goal is receiving SMS/OTPs. Two tracks:

1. **`webapp/`**: Go web UI that manages the dongle through ModemManager. Runs on the stick itself as
   `lte-dongle.service` at `http://dongle.local:8080` (password login), or on the laptop on localhost.
2. **`nura/`**: port of **Nura** (formerly postmarketOS, renamed 2026-09-27; sites moved to `nura.eco`) to
   the dongle, so it runs standalone on a phone charger and serves the webapp itself over home Wi-Fi.

`README.md` is the human runbook (everyday use, timeline, troubleshooting). Keep it updated alongside this file.
`NOTES.md` and the root `*.sh` scripts are earlier history from the stock Android firmware, when the dongle sat
on a Proxmox host and later the laptop. That's outdated: the stick now runs Nura standalone on Wi-Fi.

## webapp (Go 1.25 via mise, stdlib only)

```
cd webapp && mise exec -- go build -o lte-dongle . && ./lte-dongle          # http://127.0.0.1:8080
mise exec -- go vet ./...
```

- `main.go` shells out to `mmcli -J` for reads and to `busctl --json=short call` against
  `org.freedesktop.ModemManager1` for writes that take free text: `Messaging.Create` for SMS and
  `Modem3gpp.Ussd` for USSD. Going through D-Bus argv means SMS text needs no quoting. Inputs that do
  reach mmcli (PIN, APN, USSD code, phone number, SMS id) are validated with the regexes in `main.go`.
- The modem's D-Bus index **changes on every replug or reboot**, so `modemPath()` resolves it per request.
  Never cache the index.
- Received and sent SMS are cached in memory, since they're immutable. ModemManager itself only holds SMS
  in RAM: they **vanish when the modem restarts**. No persistent archive, by decision: Telegram forwarding keeps the history.
- `static/index.html` is the whole UI (vanilla JS, polls `/api/status` every 3s and `/api/sms` every 5s). It's
  embedded via `go:embed`, so rebuild after any UI edit. Read/unread state lives only in browser `localStorage`.
- `auth.go`: password login. `-passwd-file` holds a PBKDF2 hash (write it with `-set-password`). Sessions are
  HMAC-signed cookies (key in `session.key` next to the hash file), valid for 30 days, and changing the password
  revokes them all. Cross-origin non-GET requests are always rejected. Without `-passwd-file` it refuses to bind a
  non-loopback address. Plain HTTP only, so use it on the home LAN only.
- `monitor.go`: the one background poll (every 5 s) behind forwarding and the LED. It's a single `busctl` GetManagedObjects call
  (~25 ms CPU on the stick, vs 60–85 ms per `mmcli`). Don't add per-feature `mmcli` polling: it pushed the service to ~30% of a core.
  `modemPath()` and `listSMS()` use the same call, and SIM properties are cached per SIM path. An open dashboard tab costs
  ~9% of a core (the `mmcli -m` / `--signal-get` it still runs), and no tab costs ~1%.
- `led.go`: drives the blue LED via `-led <brightness file>` with software blinking (the kernel has no timer trigger).
  A udev rule grants write access, and the unit's `ExecStopPost` switches it off.
- **Deploy to the stick:** `DONGLE_PW=... nura/scripts/deploy.sh` (arm64 static build + the whole `nura/overlay/`).
  If mDNS doesn't get through, use `DONGLE_HOST=dongle`: the router's DNS resolves it, and `~/.ssh/config` sets `HostKeyAlias`.
  It runs as system user `lte-dongle` and needs `overlay/etc/polkit-1/rules.d/50-lte-dongle.rules`, because the stock MM
  rule requires an active login session. Set or change the password on the stick with:
  `ssh -t dongle.local run0 -u lte-dongle /usr/bin/lte-dongle -passwd-file /var/lib/lte-dongle/passwd -set-password`,
  then `run0 systemctl enable --now lte-dongle` (or `restart`).
- `forward.go`: forwards every received SMS to Telegram when `-telegram-file` (`token=`/`chat_id=` lines) exists.
  Link a bot with `ssh -t dongle.local run0 -u lte-dongle /usr/bin/lte-dongle -telegram-file /var/lib/lte-dongle/telegram -telegram-setup`
  (it finds the chat ID once the user messages the bot), then restart the service. Forwarded SMS are recorded by content
  hash in `forwarded` (MM renumbers SMS on restart), so restarts never re-send. A failed send is retried every 5s.

## nura/ port: how the pieces fit

**Boot chain (current state of the dongle's eMMC):** stock firmware could not run mainline Linux, because its
32-bit TZ lacks PSCI. Both arm64 and armv7 kernels died before driver probe. It was replaced by:
DragonBoard 410c `sbl1/rpm/tz` (Linaro release 176) + `qhypstub` (hyp) + **lk1st** (lk2nd built as
the primary bootloader, `aboot`). This needed a **new GPT**, because the db410c `tz` doesn't fit the stock partition. The
calibration and radio partitions were restored from backup at identical sizes. All of it lives in `nura/fw/`
(`out/` = the exact flashed set + `SHA256SUMS`; `src/` = lk2nd 23.1 / qhypstub / qtestsign sources).

**Board identity:** the vendor DTBs are generic Qualcomm reference trees, so the board was fingerprinted from
the stock DTB and live Android sysfs. Key facts encoded in `nura/msm8916-himi-ufi-esim.dts`:
- The SIM is routed by eSIM-select GPIOs: **119 high, 12/14/114 low**. These are applied via `&mpss` pinctrl.
  Getting them wrong = no SIM.
- The reset/EDL button is gpio37 active-high. LEDs, verified by eye: gpio72 red (power), gpio73 green (`phy0assoc`),
  gpio71 blue (free). gpio36 has no visible LED.
- The stock bootloader passes the panel string `st7735s_128160`. lk2nd had no entry for this board, so one
  (`himi,ufi-esim`) was added in `fw/src/lk2nd/lk2nd/device/dts/msm8916/msm8916-512mb-mtp.dts`.

**DTBs must be built against Nura's kernel tree, not Linus's.** `nura/pmos-dts` is a sparse checkout of
`msm8916-mainline/linux` tag `v6.12.1-msm8916`, which has extra nodes. Build with
`nura/scripts/build-dtb.sh` (no arguments rebuilds `nura/dts/msm8916-himi-ufi-esim.dtb`, byte-reproducibly). 11 dtc
warnings is the baseline: upstream ufi001c produces the same. `dts/debug/` holds diagnostic variants
(heartbeat/panic LED triggers, for when there's no serial console). `nura/README.md` describes the layout.

**Nura packaging:** a local device package `device-himi-ufi` (aarch64, ships our DTB to `/usr/share/dtb/`).
The canonical copy is inside pmbootstrap's tree at
`~/.local/var/pmbootstrap/cache_git/pmaports/device/testing/device-himi-ufi/`, mirrored in
`nura/device-himi-ufi/`. After editing it, recompute `sha512sums` in the APKBUILD and bump `pkgrel`. It's based on
upstream `device-zhihe-generic`, which uses `append_dtb` + `msm-firmware-loader` (modem/Wi-Fi firmware is read
from the stick's own `modem`/`persist` partitions at boot).

**`pmbootstrap` and anything with `sudo` must be run by the user.** pmbootstrap uses sudo internally. Tell them
the command (e.g. `pmbootstrap install`). `pmbootstrap config <key> <val>` is safe to run yourself.

## Device workflows

- **EDL:** hold the reset button while plugging in (USB `05c6:9008`). `adb reboot edl` does not work.
  The tool is `~/3d-modem/tools/venv/bin/edl` (run it from `tools/edl`). Verify every write by reading it back.
- **Fastboot (lk1st):** a plain replug lands in lk1st fastboot when `boot` holds no valid image.
  `fastboot boot <img>` RAM-boots. lk1st log: `fastboot oem log && fastboot get_staged out.txt`.
  Do **not** run `fastboot oem help`: it wedged fastboot and needed a replug.
- **Test images:** `nura/scripts/mkboot.py <dtb> <out.img> [--drop quiet splash]` repacks pmbootstrap's `boot.img`
  (v0 header) with a different appended DTB. Set `BOOT_DIR` to choose the source rootfs chroot.
- **Booted Nura without a rootfs:** USB network gives dongle `172.16.42.1` / laptop `172.16.42.2`. The initramfs
  debug shell opens on telnet :23 about 40s after boot. `nura/scripts/dsh.sh '<cmds>' [wait]` runs commands there.
- **Installed Nura:** the stick runs standalone on Wi-Fi as **`dongle.local`**. `ssh dongle.local`
  works, as does `ssh dongle-usb` (172.16.42.1 over USB). Both use key `~/.ssh/id_ed25519_dongle`. There's no sudo/doas:
  root goes through `run0` (polkit, wheel), so use `DONGLE_PW=... nura/scripts/drun.py '<cmd>'`. The user supplies the
  password; never write it to disk. A software reboot works over Wi-Fi (~32s), but over USB the laptop won't see the
  gadget again until a replug.
- **On-device customizations** live in `nura/overlay/`, mirroring their target paths: SIM auto-unlock service (PIN in
  `/etc/sim-pin` on the stick only) and the msm-modem-uim-selection retry drop-in. Everything in it is also packaged in
  `device-himi-ufi` `pkgrel=4`. `overlay/` stays the source of truth: after editing it or `webapp/`, run
  `nura/scripts/sync-package.sh` (copies into the package, recomputes `sha512sums`, mirrors to pmaports), then the user
  runs `pmbootstrap build device-himi-ufi`. `lte-dongle.service` is shipped but not enabled (needs the password file).
  ModemManager 1.25.95 SIGABRTs once after each SIM unlock and is restarted by systemd. Code talking to MM must
  tolerate it briefly vanishing from D-Bus.
- **Modem ~15 min firmware crash (SOLVED 2026-10-30):** the HiMiLink baseband compares NV item 2500 with `HiMI_OK` at ~300 s of
  modem uptime and self-destructs ~600 s later (`lte_ml1_common_timer.c:390`). The modem clears the item at every boot, so
  `himi-ok-guard.service` (overlay, uses `diag-bind` + `diag_nv`, source in `nura/tools/diag_nv`) rewrites it, only when it is
  all-zero. Found via github.com/akbar-npj/msm8916-openwrt (Docs/Modem Stability). `modem-watchdog` is now a fallback: on a crash
  it stop/starts `remoteproc1` + `sim-autounlock` (~1 min) and only reboots if that fails. DIAG is `/dev/rpmsg0` after `diag-bind`
  (lost on every modem restart). Don't disassemble the modem blob: not needed, and it trips the safety classifier.
- `mmcli -m any --signal-setup=5` arms RSRP/SNR readings. Parse `mmcli -K` key/value output in scripts: the human format
  shows `Status | lock: sim-pin`, and after unlock `sim-pin2`.

## Backup and restore (irreplaceable data)

`backup-2026-10-30/` is a read-only full EDL dump of the stock eMMC: all partitions, `rawprogram0.xml`,
`SHA256SUMS`. It also holds the **full stock GPTs**: `stock-gpt-primary-34sectors.bin` at sector 0 and
`stock-gpt-backup-last33sectors.bin` at sector 7634911. `fsc/fsg/modemst1/modemst2/persist/modem` hold the
IMEI and RF calibration. Never overwrite them without reading the target first. Full restore to stock =
write both stock GPTs with `edl ws`, then every partition with `edl w <name> <file>`.
