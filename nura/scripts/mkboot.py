#!/usr/bin/env python3
"""Repack Nura's boot.img (header v0) with a different appended DTB and optional cmdline edits.

usage: mkboot.py <dtb> <out.img> [--drop word ...] [--add word ...]
"""
import hashlib, struct, sys, os

R = os.path.expanduser(os.environ.get("BOOT_DIR", "~/.local/var/pmbootstrap/chroot_rootfs_himi-ufi/boot"))
dtb_path, out_path, *rest = sys.argv[1:]
drop, add, mode = [], [], None
for a in rest:
    if a in ("--drop", "--add"): mode = a
    else: (drop if mode == "--drop" else add).append(a)

b = open(f"{R}/boot.img", "rb").read()
ks, rs, ps = struct.unpack_from("<I", b, 8)[0], struct.unpack_from("<I", b, 16)[0], struct.unpack_from("<I", b, 36)[0]
pg = lambda n: (n + ps - 1) // ps * ps
rd = b[ps + pg(ks): ps + pg(ks) + rs]
kern = open(f"{R}/vmlinuz", "rb").read() + open(dtb_path, "rb").read()

hdr = bytearray(b[:ps])
struct.pack_into("<I", hdr, 8, len(kern))
cmd = (hdr[64:576].rstrip(b"\0") + hdr[608:1632].rstrip(b"\0")).decode().split()
cmd = [w for w in cmd if w not in drop] + add
c = " ".join(cmd).encode()
hdr[64:576] = c[:511].ljust(512, b"\0")
hdr[608:1632] = c[511:].ljust(1024, b"\0")
h = hashlib.sha1()
for blob in (kern, rd, b""):
    h.update(blob); h.update(struct.pack("<I", len(blob)))
hdr[576:608] = h.digest().ljust(32, b"\0")
pad = lambda x: x + b"\0" * (pg(len(x)) - len(x))
open(out_path, "wb").write(bytes(hdr) + pad(kern) + pad(rd))
print(out_path, "cmdline:", " ".join(cmd))
