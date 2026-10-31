> **Author's runbook, kept as a worked example.** Hostnames, the Telegram bot and the Wi-Fi name are the author's; adapt them. The backup (`backup-2026-10-30/`) and `tools/` it mentions are **not** in this repo: they hold the device's IMEI and RF calibration, so make your own EDL backup of your own stick before flashing anything.

# HiMiLink 4G UFI → standalone SMS/OTP receiver

A HiMiLink "4G UFI" LTE USB stick (Qualcomm MSM8916) with a roaming SIM on Rogers LTE.
It now runs **Nura** (mainline Linux, formerly postmarketOS) instead of its stock Android firmware. It sits on a phone
charger, joins home Wi-Fi, and receives SMS. Every SMS is **forwarded to Telegram**. A password-protected web UI
at **http://dongle.local:8080** lets you read and send SMS, run USSD codes and watch the signal.

Everything was done on 2026-10-30. This file is the runbook. Details live in:

| File | For |
|---|---|
| `nura/README.md` | The OS port: layout, scripts, boot chain, on-stick files, quirks |
| `CLAUDE.md` | Condensed technical reference (also read by Claude Code) |
| `NOTES.md` | **Historical** notes from the stock-firmware phase (Proxmox, Android, adb). Outdated |

---

## 1. Everyday use

| What | How |
|---|---|
| Read SMS / OTPs | Telegram chat with **your Telegram bot**, arriving within ~5 s |
| Web UI | `http://dongle.local:8080` , web password |
| Shell | `ssh dongle.local` (key `~/.ssh/id_ed25519_dongle`); over a USB cable `ssh dongle-usb` |
| Root | `run0 <cmd>` on the stick (asks for the stick user's password; there is no sudo) |
| Quick status | `ssh dongle.local 'mmcli -m any -K \| grep -E "generic.state \|operator-name "'` |
| Signal | `ssh dongle.local 'mmcli -m any --signal-setup=5; sleep 6; mmcli -m any --signal-get'` |

**Power-on sequence**, all automatic: Wi-Fi in ~30 s, then the SIM is unlocked with the stored PIN, then it
registers on Rogers LTE ~1 min after power-up. The web UI and Telegram forwarding start by themselves. Already-forwarded
SMS are never re-sent.

**LEDs:** red = power. Green = Wi-Fi associated. **Blue** (driven by the web app): solid = registered on LTE,
slow blink = not registered (searching / locked / recovering), fast blink for 1 min = new SMS, off = no modem
or web app not running.

**Signal targets:** RSRP better than −110 dBm, SNR above 0 dB. Current spot: RSRP −106…−108, SNR −2…+2 (OK for SMS).
The first spot was −117 dBm. Window, height and distance from metal matter more than anything else.
Use a decent charger and cable. Brownouts make the stick boot-loop (LED flashing on and off).

---

## 2. Timeline: how we got here

1. **Stock firmware on Proxmox** (see `NOTES.md`). The stick ran Android 4.4. No signal at the server's location, and
   past SMS were stuck in the Android telephony DB behind a root password. Toggled the stick into USB modem mode
   (`toggle-usb-mode.sh`).
2. **Moved to the laptop.** One USB port browned it out, a USB 3.1 port powered it fine. With better signal it
   registered on Rogers (roaming always worked: it was a placement/power problem). The laptop's ModemManager
   handled it (`modem-up.sh`, `read-sim-sms.sh`, `signal-watch.sh`).
3. **Go web UI** (`webapp/`) over ModemManager: status, signal chart, SMS threads, send SMS, USSD, PIN, data.
4. **Full eMMC backup** over EDL to `backup-2026-10-30/` (all partitions + stock GPTs + `SHA256SUMS`). The Android SMS
   DB (`mmssms.db`) came along, and its 3 old messages were extracted to `recovered-sms.json`.
5. **Nura port.**
   - Fingerprinted the board from the stock DTB and live sysfs, and wrote `nura/dts/msm8916-himi-ufi-esim.dts`:
     the eSIM-select GPIOs (119 high; 12/14/114 low), LEDs and the reset button.
   - Every mainline kernel died before driver probe: the stock 32-bit TrustZone has no PSCI.
   - **Replaced the boot firmware** with DragonBoard 410c `sbl1/rpm/tz` + `qhypstub` + **lk1st** (lk2nd with a
     new `himi,ufi-esim` board entry), on a new GPT. Calibration/IMEI partitions were restored at identical sizes.
   - Built a device package `device-himi-ufi`, installed with pmbootstrap, and got Wi-Fi, the modem and SMS working.
6. **On-stick fixes:** SIM auto-unlock at boot, and a retry for the flaky UIM-selection service.
7. **Web UI moved onto the stick** with a password login, running as an unprivileged service.
8. **Telegram forwarding** of every received SMS.
9. **Placement:** moved to a better room (+10 dB RSRP).
10. **Watchdogs:** modem watchdog (reboot when stuck unregistered) + PM8916 hardware watchdog.

---

## 3. Architecture

```
Rogers LTE ── modem DSP (stock Qualcomm firmware, read from the stick's own modem/persist partitions)
                 │ QMI
           ModemManager ◄── sim-autounlock.service (PIN from /etc/sim-pin)
                 │ D-Bus (polkit rule for user lte-dongle)
        lte-dongle.service  (Go, user lte-dongle, /var/lib/lte-dongle)
           ├── :8080 web UI + JSON API (login required) ──► home Wi-Fi only (nftables)
           └── forward loop, every 5 s ──► api.telegram.org ──► your Telegram
```

Boot chain on the eMMC: `sbl1` (db410c) → `tz`/`rpm` (db410c) → `qhypstub` → **lk1st** (`aboot`) →
`extlinux.conf` in the Nura rootfs (in `userdata`) → kernel `v6.12.1-msm8916` + our DTB.

---

## 4. Web app (`webapp/`)

Go 1.25 (via `mise`), standard library only. The UI is a single embedded `static/index.html`, plus `static/login.html`.

| File | Does |
|---|---|
| `main.go` | API over `mmcli -J` (reads) and `busctl` D-Bus calls (send SMS, USSD). Validates every input. Resolves the modem index per request, since it changes on every restart |
| `auth.go` | Login. PBKDF2-SHA256 password hash, HMAC-signed 30-day session cookie, ~1 guess/s throttle, cross-origin writes rejected. Refuses to listen on a non-loopback address without a password |
| `monitor.go` | Shared 5 s background poll (one D-Bus call) feeding forwarding and the LED |
| `led.go` | Blue status LED (`-led`): solid / slow blink / fast blink on new SMS / off |
| `forward.go` | Telegram forwarding. Dedup by content hash in `/var/lib/lte-dongle/forwarded`, retry on failure, token never logged |

```sh
cd webapp && mise exec -- go build -o lte-dongle . && ./lte-dongle     # laptop, http://127.0.0.1:8080, no login
mise exec -- go vet ./...
```

**On the stick**, all state is in `/var/lib/lte-dongle/` (mode 0700, user `lte-dongle`):

| File | Content |
|---|---|
| `passwd` | Web UI password hash |
| `session.key` | Cookie signing key (delete to log everyone out) |
| `telegram` | `token=` and `chat_id=` (chat <chat-id>, bot your Telegram bot) |
| `forwarded` | Hashes of SMS already sent to Telegram |

### Deploying a new version

Run in **your own terminal**, not via Claude's `!` prompt, which can't read typed input:

```sh
cd ~/3d-modem
read -rs DONGLE_PW && export DONGLE_PW && nura/scripts/deploy.sh          # prefix DONGLE_HOST=dongle if dongle.local fails
```

It cross-compiles for arm64, installs the binary and **everything in `nura/overlay/`** as root, then enables and
restarts the services.

### Changing secrets

```sh
# web UI password (then restart)
ssh -t dongle.local run0 -u lte-dongle /usr/bin/lte-dongle -passwd-file /var/lib/lte-dongle/passwd -set-password
# new Telegram bot/token (after /revoke in @BotFather); finds your chat ID when you message the bot
ssh -t dongle.local run0 -u lte-dongle /usr/bin/lte-dongle -telegram-file /var/lib/lte-dongle/telegram -telegram-setup
ssh -t dongle.local run0 systemctl restart lte-dongle
```

SIM PIN: `/etc/sim-pin` on the stick (root, 0600). The stick's login password: `passwd` on the stick.
None of these secrets are stored in this folder.

---

## 5. Nura on the stick

Build pieces (DTS, device package, firmware sources, scripts) are described in `nura/README.md`. Files we added by hand
on the stick, mirrored in `nura/overlay/`. The modem-stability pieces (the `himi-ok-guard`/`diag-bind`/`diag_nv`/`modem-watchdog`/`sim-autounlock` rows and `watchdog.conf`) are all packaged in `device-himi-ufi` `pkgrel=4` (see `nura/scripts/sync-package.sh`):

| On the stick | Purpose |
|---|---|
| `/usr/sbin/sim-autounlock` + `sim-autounlock.service` | Unlock SIM at boot. Only tries with all 3 PIN retries left, so it can never cause a PUK lock |
| `msm-modem-uim-selection.service.d/retry.conf` | Upstream UIM selection fails intermittently. Retry 3× and never block ModemManager |
| `/usr/bin/lte-dongle`, `lte-dongle.service` | The web app (installed by `deploy.sh`) |
| `sysusers.d/`, `tmpfiles.d/lte-dongle.conf` | Service user and its state dir |
| `polkit-1/rules.d/50-lte-dongle.rules` | Lets that user control ModemManager without a login session |
| `nftables.d/60_lte_dongle.nft` | Opens tcp/8080 on Wi-Fi/USB only, never on LTE |
| `/usr/sbin/himi-ok-guard` + `.service`, `diag-bind`, `/usr/bin/diag_nv` | **Fixes the ~15 min modem crash.** Keeps modem NV item 2500 = `HiMI_OK` over the DIAG channel (`/dev/rpmsg0`); only writes when the item is all-zero. Log: `journalctl -u himi-ok-guard` |
| `/usr/sbin/modem-watchdog` + `.service` | Modem firmware crash: stop/start the modem's remoteproc (found by name `4080000.remoteproc`, its `remoteprocN` number changes between boots) + `sim-autounlock`, ~1 min; reboot only if not registered 3 min later or it crashes again within 5 min. SIM locked for 2 min: run `sim-autounlock`. Unregistered for 10 min: reboot, with back-off 10→20→40 min … 6 h that resets on registration. Log: `journalctl -u modem-watchdog` |
| `udev/rules.d/90-lte-dongle-led.rules` | Lets `lte-dongle` drive the blue LED |
| `avahi/avahi-daemon.conf` | Stock config + `allow-interfaces=wlan0`, so `dongle.local` only resolves to the Wi-Fi address |
| `systemd/system.conf.d/watchdog.conf` | systemd feeds the PM8916 hardware watchdog: a frozen system hard-resets after 60 s |

Also set by hand: Wi-Fi profile for your network (`nmcli`), hostname `dongle` (mDNS via avahi → `dongle.local`).

---

## 6. Troubleshooting

| Symptom | Cause / fix |
|---|---|
| Web UI / Telegram show "searching" for minutes, no signal values | Modem firmware crash (`journalctl -k \| grep "fatal error"`, seen as `lte_ml1_common_timer.c`). **Cause found 2026-10-30:** the HiMiLink firmware compares NV item 2500 with `HiMI_OK` at ~300 s of modem uptime and self-destructs ~10 min later; the modem clears the item every boot. `himi-ok-guard` rewrites it, so this should no longer happen. If it does, check `journalctl -u himi-ok-guard` (and that `/dev/rpmsg0` exists after `diag-bind`). `modem-watchdog` soft-restarts the modem as a fallback. Found via github.com/akbar-npj/msm8916-openwrt (Docs/Modem Stability) |
| "locked" right after boot | Normal for ~30 s until `sim-autounlock` runs. If it stays locked: `journalctl -u sim-autounlock` (it refuses if retries < 3; unlock in the web UI) |
| ModemManager vanishes briefly after unlock | Known: MM 1.25.95 SIGABRTs once after each unlock, and systemd restarts it |
| `dongle.local` doesn't resolve | mDNS multicast doesn't get through from some rooms (a repeater/mesh node). Use `ssh dongle` / the stick's IP or the stick's IP. avahi also announces the USB address `172.16.42.1`. Fix: `allow-interfaces=wlan0` |
| No SMS in Telegram | `ssh dongle.local journalctl -u lte-dongle -n 30`: look for `forward SMS … error`. Wrong clock (no RTC, before NTP) or no Wi-Fi resolves itself |
| Stick not on Wi-Fi at all | Plug it into the laptop: `ssh dongle-usb` (172.16.42.1). After a software reboot over USB, replug |
| Stick boot-loops (LED flashing) | Not enough USB power. Better charger/cable, or powered hub |
| Clock wrong | No RTC battery. Fixed once NTP syncs over Wi-Fi |

---

## 7. Recovery

- **EDL** always works, whatever is flashed: hold the reset button while plugging in (`05c6:9008`). Tool:
  `tools/venv/bin/edl`, run from `tools/edl`. Read back every write.
- **lk1st fastboot:** replug with no valid boot image, or `fastboot boot <img>` to RAM-boot a test image
  (`nura/scripts/mkboot.py`). Never run `fastboot oem help`: it wedges.
- **Back to stock Android:** write both stock GPTs from `backup-2026-10-30/` with `edl ws`
  (`stock-gpt-primary-34sectors.bin` at sector 0, `stock-gpt-backup-last33sectors.bin` at 7634911), then every
  partition with `edl w <name> <file>`.
- `fsc/fsg/modemst1/modemst2/persist/modem` hold the **IMEI and RF calibration**. They are irreplaceable, so keep
  `backup-2026-10-30/` safe, ideally with a second copy off this laptop.

---

## 8. Possible next steps

1. ~~Modem watchdog~~ done. Optional: Telegram alert when it reboots.
2. ~~SMS archive~~ dropped: Telegram keeps the history.
3. ~~Fold `nura/overlay/` into the `device-himi-ufi` package~~ done in `pkgrel=4` (run `nura/scripts/sync-package.sh`, then `pmbootstrap build`; untested on a fresh install).
4. ~~Blue LED status~~ done. ~~avahi Wi-Fi only~~ done.
5. ~~Find the cause of the ~14-min modem firmware crash~~ done: NV 2500 / `HiMI_OK`, see Troubleshooting.
