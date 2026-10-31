> **Historical (stock-firmware phase, 2026-10-30 morning).** The stick now runs Nura standalone; see `README.md`.

# 4G UFI Dongle — Read SMS: Progress Notes

_Last updated: 2026-10-30_

## Goal
Read SMS messages from a 4G LTE USB dongle.

## Where the dongle lives
- Plugged into the **Proxmox server**: `root@<proxmox-host>` (reached via SSH).
- NOT on the laptop. All dongle work happens on the Proxmox host.
- Secrets (NOT stored here): Proxmox root pw, laptop sudo pw, SIM PIN. Keep them in your
  password manager. The SIM PIN is required to unlock the modem each power cycle.

## What the device is
- **HiMiLink "4G UFI"** MiFi — Qualcomm, Android **4.4.4**, `ro.secure=1`, `ro.debuggable=1`.
- USB id in tether mode: `05c6:9024`; in modem mode re-enumerates as `05c6:9091`.
- ADB serial: `<serial>`. IMEI `<IMEI>`. SIM ICCID `<ICCID>`.
- Web UI on device port 8000: login `admin`/`admin`. **Has no SMS feature at all**
  (verified: whole web-server binary has zero "sms" strings; API cmdids are wifi/apn/
  network/reboot/usbtether only).

## USB composition
- Default/factory config: `diag,serial_smd,rmnet_bam,adb` (property `persist.sys.usb.config`).
- Vendor forces `rndis,none,adb` at runtime when `sys.usb.tethering=true` (the MiFi router mode).
- `setprop sys.usb.config ...` does NOT stick (watchdog reverts it).
- **Correct lever:** the web API `sethimiusbtether` with `params:"0"` → switches to modem
  mode (exposes QMI `cdc-wdm0`). `params:"1"` → back to RNDIS tether. See `toggle-usb-mode.sh`.

## Web API auth (reverse-engineered)
- `POST http://127.0.0.1:8000/himiapi/json` with body `{"cmdid":"login","username":"admin","password":"admin"}`
  → returns `{"session":"<uuid>","reply":"ok"}`.
- Authenticated calls need BOTH: header `Authorization: <session>` AND body field
  `"sessionId":"<session>"`. (Missing the body field → `{"reply":"SessionOut"}`.)

## Reading SMS — what we found (DEFINITIVE)
- **SIM storage: EMPTY.** EF_SMS (`3F00/7F10/6F3C`) = 40 linear-fixed records × 176 bytes;
  all 40 have status byte `00` (free). Raw dump saved in `efsms.txt`.
- **Modem (ME) storage: EMPTY** — ModemManager `--messaging-list-sms` returns none.
- **Modem not registered** when last checked: signal 0%, registration "searching",
  detached. So nothing can be received until it gets signal (antenna/coverage/SIM service).
- Therefore any *previously received* SMS are only in Android's telephony DB
  `/data/data/com.android.providers.telephony/databases/mmssms.db`.

## Why the Android DB is blocked (needs ROOT)
- `adb` connects as unprivileged `shell` (uid 2000). No `READ_SMS`; Android 4.4 can't
  grant it at runtime (`pm grant` fails — permission not requested by `com.android.shell`).
- `run-as` blocked (provider "is not an application").
- `adb backup` dead: backup service not running + telephony app has backup disabled
  (flags = SYSTEM HAS_CODE, no ALLOW_BACKUP).
- No `su` binary. `adb root` exists but is a **custom vendor build that demands a password**
  (returns "password error"). We did NOT crack it. If you know/obtain that adb-root
  password, that's the clean path: `adb root` then pull `mmssms.db`.

## Tooling installed
- **Laptop (Omarchy/Arch):** `modemmanager usb_modeswitch libqmi libmbim sshpass` + ModemManager
  enabled. (Partial pacman install was done — run `sudo pacman -Syu` to be safe.)
- **Proxmox (Debian):** `adb` (`adb start-server` running), `libqmi-utils` (qmicli 1.36 —
  NOTE: this build has NO `--wms-list-messages`, so SMS listing must go through MM, not qmicli),
  `modemmanager`.

## Key gotchas learned
- The device shell has **no `head`** — pipe to the Proxmox side for `head`/`grep`.
- qmicli read-record path separator must be **dashes**, not commas, and must include the MF:
  `file=0x3F00-0x7F10-0x6F3C` (commas break the key=value parser).
- After PIN unlock the modem re-enumerates and MM drops it; re-tag udev
  (`udevadm trigger --subsystem-match=net --subsystem-match=usbmisc`) then restart MM.
- MM needs BOTH the `cdc-wdm0` (usbmisc) and the `wwpXXX` (net) ports tagged
  `ID_MM_CANDIDATE=1` or it errors "Failed to find a net port in the QMI modem".

## Current device state (as left)
- Dongle is in **modem mode** (`sethimiusbtether` = 0), QMI at `/dev/cdc-wdm0`.
- SIM PIN was verified; modem enabled but searching/0% signal.
- **Not yet restored to RNDIS tether mode.** Run `toggle-usb-mode.sh 1` to restore if the
  dongle is needed as a MiFi router.

## Network registration diagnosis (SIM won't connect) — 2026-10-30
SIM is **ATOM Myanmar** (IMSI MCC 414 / MNC 06). Dongle is physically at the Proxmox server
(likely poor reception inside the chassis). Verified with a CLEAN reboot, Android in full control:
- SIM **READY**, data roaming **ON**, network mode LTE+WCDMA (`preferred_network_mode=12`), APN set.
- Modem **camps on Rogers LTE (302720)** — the only available partner; Telus (302220) & Bell
  (302610) are **forbidden** for this SIM. Cell: `Forbidden: no`, `Registration restriction: unrestricted`.
- Never completes attach: `Registration state: not-registered-searching`, Android `mServiceState=1` (out of service).
- Signal (the smoking gun): **RSSI -74 / RSRP -121 dBm / SNR -13.8 dB**. Negative SNR = signal
  below noise floor → can camp (decode downlink broadcast) but can't complete the 2-way attach.

**Conclusion / fixes, in order:**
1. **Signal too weak** (prime suspect, user-fixable): put dongle on a **USB extension cable** to a
   window / away from the metal server; connect **external LTE antennas**. Want RSRP better than
   ~-110 dBm and **positive SNR**. Watch with `./signal-watch.sh` while repositioning.
   Observed readings fluctuated RSRP -121 → **-140 dBm** (noise floor = basically no signal),
   SNR -13.8 → -14.8 dB. Reg state seen as both `not-registered-searching` and `registration-denied`.
   At this signal you CANNOT distinguish a real roaming denial from an attach that just can't
   complete — so get RSRP/SNR into a usable range FIRST, then re-read the reg state.
2. If signal good and still not registering → **roaming provisioning** issue: call ATOM, confirm
   international roaming is ACTIVE for **Canada**, has balance/pack, SIM provisioned for Rogers LTE.
   (Rogers shut down 3G — LTE only, no 3G fallback.)
3. Isolation test: try the SIM in a normal phone in the same spot (SIM/roaming vs dongle placement).

Rogers reg attempts that were tried and failed: `mmcli --3gpp-register-in-operator=302720` (timeout),
`--3gpp-register-home` (timeout), APN set on QMI profile 1 (`internet`), airplane-mode cycle, dms reset.
NOTE: manual `qmicli --dms-set-operating-mode=offline/online` got the modem STUCK (`InvalidTransition`);
an `adb reboot` is the clean recovery. Prefer letting Android RIL / MM manage radio power — don't hand-toggle op mode.

## RESOLVED: SIM now connects (2026-10-30)
Moved the dongle OFF the Proxmox server ONTO the **laptop** (Omarchy/Arch). Two things fixed it:
- **USB power:** one laptop port boot-looped the dongle (light flashing on/off = brownout, no USB
  enumeration at all). A different port (USB 3.1, controller 0000:3e:00.0) supplied enough current
  and it enumerated fine as `05c6:9091` in modem mode → `cdc-wdm0`, net `wwp62s0u1i2`.
- **Signal:** on the laptop RSRP -106 dBm / SNR **+11 dB** (vs -140 / -14.8 at the server).
Result: `state: registered`, `registration: roaming`, `access tech: lte`, `packet service: attached`,
operator **Rogers (302720)**. So it was NEVER a roaming-provisioning problem — it was signal/power/placement.
Laptop's ModemManager auto-detects it (no udev-trigger dance needed, unlike the minimal Proxmox install).

Bring-up on laptop: `mmcli -L` → `mmcli -i 0 --pin=<PIN>` → `mmcli -m 0 --enable` → `mmcli -m 0 --signal-setup=5`.
Watch signal: `watch -n 3 'mmcli -m 0 --signal-get'` (want RSRP > ~-110, SNR > 0).
If moving for signal: use a POWERED USB hub (passive extension re-triggers the brownout).

SMS: SIM + modem storage still empty. Incoming SMS now arrive here — poll
`watch -n 5 'mmcli -m 0 --messaging-list-sms'`, read one with `mmcli -m 0 --sms=<N>`.
Already-received (past) messages remain only in the device's Android DB → still need root to read.

## Next steps / decisions
1. If waiting to RECEIVE an SMS: needs signal first (check antenna/coverage). Then route
   incoming to ME and watch with MM (`mmcli -m 0 --messaging-list-sms`, or monitor).
2. If messages were already received: need the device's **adb-root password** to pull the
   Android SMS DB. (Will not brute-force it.)
3. Decide whether to leave in modem mode or restore RNDIS tether.
