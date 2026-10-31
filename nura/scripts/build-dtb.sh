#!/usr/bin/env bash
# Build a DTS from nura/dts against Nura's kernel tree (pmos-dts = msm8916-mainline v6.12.1-msm8916).
#   scripts/build-dtb.sh                         -> dts/msm8916-himi-ufi-esim.dtb
#   scripts/build-dtb.sh dts/debug/himi-diag64.dts out.dtb
# 11 dtc warnings is the baseline (upstream ufi001c gives the same).
set -euo pipefail
N=$(cd "$(dirname "$0")/.." && pwd)
P=$N/pmos-dts
src=${1:-$N/dts/msm8916-himi-ufi-esim.dts}
out=${2:-${src%.dts}.dtb}
pp=$(mktemp)
trap 'rm -f "$pp"' EXIT
cpp -nostdinc -undef -D__DTS__ -x assembler-with-cpp \
	-I "$N/dts" -I "$P/arch/arm64/boot/dts/qcom" -I "$P/arch/arm/boot/dts/qcom" \
	-I "$P/include" -I "$P/scripts/dtc/include-prefixes" "$src" -o "$pp"
dtc -q -I dts -O dtb -o "$out" "$pp"
echo "$out"
