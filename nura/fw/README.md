# Boot firmware for the HiMiLink 4G UFI

The stock firmware has a 32-bit TrustZone without PSCI, so no mainline kernel boots on it. The stick was
re-flashed over EDL with a DragonBoard 410c boot chain and **lk1st** (lk2nd built as the primary bootloader):

    sbl1 / rpm / tz   Linaro DragonBoard 410c release 176  (https://releases.linaro.org/96boards/dragonboard410c/)
    hyp               qhypstub   (https://github.com/msm8916-mainline/qhypstub)
    aboot             lk2nd 23.1, project lk1st-msm8916  (https://github.com/msm8916-mainline/lk2nd)
    signing           qtestsign  (https://github.com/msm8916-mainline/qtestsign)

The binaries are not in this repo (Qualcomm/Linaro blobs, and the GPT is tied to one stick's partition sizes).
`SHA256SUMS` lists the exact set that was flashed.

## What differs from upstream lk2nd

`patches/lk2nd-himi-ufi-esim.patch` (against lk2nd `e9c8b21`):

- a `himi,ufi-esim` board node in `msm8916-512mb-mtp.dts`, matched on the panel string
  `st7735s_128160` that the stock bootloader passes, plus the reset/EDL button (gpio37, active-high);
- `USE_TARGET_HS200_CAPS=1` in `project/lk1st-msm8916.mk`.

## Partition table

The new GPT was derived from the OpenStick layout (https://github.com/kinsamanka/OpenStick-Builder), because the db410c
`tz` does not fit the stock partition. `fsc`, `fsg`, `modemst1`, `modemst2`, `persist` and `modem` hold the IMEI
and RF calibration: they must be restored from **your own** backup at identical sizes. **Take a full EDL backup of
your own stick first; flashing a mis-sized GPT can brick it or destroy the calibration.**
