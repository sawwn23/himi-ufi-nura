#!/usr/bin/env bash
# Run commands in the Nura initramfs debug shell over USB telnet: dsh.sh 'cmd; cmd' [wait_seconds]
timeout $(( ${2:-6} + 6 )) telnet 172.16.42.1 23 < <(sleep 2; printf 'echo _BE""GIN_; %s; echo _E""ND_\n' "$1"; sleep "${2:-6}") 2>/dev/null \
  | tr -d '\r' | sed -n '/^_BEGIN_$/,/^_END_$/p' | sed '1d;$d'
