# HiMiLink 4G UFI: mainline Linux + SMS-to-Telegram

Turn a cheap **HiMiLink "4G UFI" LTE USB stick** (Qualcomm MSM8916, 512 MB) into a standalone SMS receiver.
The stock Android firmware is replaced by **Nura** (mainline Linux, formerly postmarketOS). The stick sits on a
phone charger, joins your Wi-Fi, forwards every SMS to Telegram and serves a small password-protected web UI.

> **Status:** works on the author's stick (the *eSIM-switch* board revision, stock build `msm8916_32_512`).
> Other UFI sticks have different GPIO wiring. Flashing means replacing the boot firmware over EDL, which
> **can brick the stick or destroy its IMEI and RF calibration**. Read `nura/fw/README.md` and take a full
> backup of your own device first. Nothing here is affiliated with the vendor, and you flash at your own risk.

## What's in here

| Path | What |
|---|---|
| `webapp/` | Go web UI (stdlib only): SMS read/send, USSD, SIM PIN, signal, LED status, Telegram forwarding. Talks to ModemManager through `mmcli` and D-Bus |
| `nura/dts/` | Device tree for the board: SIM-routing GPIOs (119 high; 12/14/114 low), LEDs, reset/EDL button |
| `nura/device-himi-ufi/` | pmbootstrap device package: DTB, modem-stability fixes, the web app, service units |
| `nura/overlay/` | The same on-device files at their target paths. Source of truth for the package; `scripts/deploy.sh` pushes them to a running stick |
| `nura/fw/` | Notes, lk2nd patch and checksums for the boot firmware swap (no binaries) |
| `nura/scripts/` | DTB build, deploy, remote-run, package sync, boot image repack |
| `docs/RUNBOOK.md` | The author's full runbook: timeline, everyday use, troubleshooting, recovery |
| `docs/legacy-stock-firmware/` | Notes and scripts from the stock-Android phase (the vendor web API, QMI, EF_SMS dumps) |
| `CLAUDE.md` | Condensed technical reference, also used by Claude Code |

## Things worth knowing (the non-obvious findings)

- **Stock firmware can't run mainline.** Its 32-bit TrustZone lacks PSCI, so arm64 and armv7 kernels both die
  before driver probe. The fix is DragonBoard 410c `sbl1/rpm/tz` + `qhypstub` + **lk1st** on a new GPT.
- **The SIM is routed by GPIOs.** With the wrong levels there is no SIM at all (`nura/dts/`).
- **The modem firmware crashed every ~15 minutes.** It compares NV item 2500 with `HiMI_OK` at ~300 s of modem
  uptime and self-destructs ~10 minutes later; the modem clears the item at every boot. `himi-ok-guard` rewrites it
  over the DIAG channel (`nura/tools/diag_nv`). Found via
  [akbar-npj/msm8916-openwrt](https://github.com/akbar-npj/msm8916-openwrt) (Docs/Modem Stability).
- **ModemManager 1.25.95 aborts once after every SIM unlock** and is restarted by systemd. Clients must tolerate it
  vanishing from D-Bus briefly.
- The modem's D-Bus index changes on every restart, so the web app resolves it per request.

## Build and run

Web app only (any machine with ModemManager and a modem), Go 1.25:

```sh
cd webapp && go build -o lte-dongle . && ./lte-dongle        # http://127.0.0.1:8080, no login on loopback
```

On a network address it refuses to start without `-passwd-file`. Create the hash with
`./lte-dongle -passwd-file ./passwd -set-password`. Sessions are HMAC-signed cookies; the server speaks plain HTTP,
so keep it on a trusted LAN. Forward SMS by linking a Telegram bot: `./lte-dongle -telegram-file ./telegram -telegram-setup`.

Whole OS image: see `nura/README.md` (DTB build, device package, pmbootstrap).
Running stick: `DONGLE_PW=... nura/scripts/deploy.sh` (set `DONGLE_HOST`; the SIM PIN lives only in `/etc/sim-pin` on the stick).

## Secrets

Nothing sensitive is stored in this repo. The SIM PIN, web password hash, session key and Telegram token live
only on the stick (`/etc/sim-pin`, `/var/lib/lte-dongle/`). `.gitignore` also excludes full device backups,
because they contain the IMEI and RF calibration.

## Credits

[msm8916-mainline](https://github.com/msm8916-mainline) (lk2nd, qhypstub, qtestsign, the kernel tree),
[OpenStick](https://github.com/kinsamanka/OpenStick-Builder) (GPT layout and the firmware-swap approach),
[postmarketOS / Nura](https://nura.eco), and the modem stability notes in
[akbar-npj/msm8916-openwrt](https://github.com/akbar-npj/msm8916-openwrt).

## License

MIT for the original code (see `LICENSE`); `nura/dts/` is GPL-2.0-only, derived from Linux device trees.
