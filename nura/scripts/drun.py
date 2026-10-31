#!/usr/bin/env python3
"""Run a root shell command on the dongle via `run0`, answering the polkit password prompt.

usage: DONGLE_PW=... drun.py 'shell command'
The password comes only from the environment; it is never written to disk.
"""
import os, pty, shlex, sys, time, select

cmd = sys.argv[1]
pw = os.environ["DONGLE_PW"].encode() + b"\n"
pid, fd = pty.fork()
if pid == 0:
    os.execvp("ssh", ["ssh", "-tt", "-o", "BatchMode=yes", os.environ.get("DONGLE_HOST", "dongle.local"), "run0 sh -c " + shlex.quote(cmd)])

sent, buf = False, b""
while True:
    r, _, _ = select.select([fd], [], [], 120)
    if not r:
        break
    try:
        data = os.read(fd, 4096)
    except OSError:
        break
    if not data:
        break
    buf += data
    if not sent and b"assword" in buf:
        time.sleep(0.2)
        os.write(fd, pw)
        sent, buf = True, b""
        continue
    if sent:
        sys.stdout.write(data.decode(errors="replace"))
        sys.stdout.flush()
_, status = os.waitpid(pid, 0)
sys.exit(os.waitstatus_to_exitcode(status))
