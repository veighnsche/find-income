#!/usr/bin/env python3
"""Run only inside the selected runner's named Codex command sandbox."""

import errno
from pathlib import Path
import socket

base = Path("/var/lib/jobseek-runner")
work = base / "work/i12-accept"
state = base / "state"


def denied_read(path):
    try:
        with path.open("rb"):
            pass
    except OSError as error:
        if error.errno in (errno.EACCES, errno.EPERM):
            return
    raise SystemExit("native read unexpectedly reached a private path")


def denied_open_for_write(path):
    try:
        with path.open("ab"):
            pass
    except OSError as error:
        if error.errno in (errno.EACCES, errno.EPERM, errno.EROFS):
            return
    raise SystemExit("native write unexpectedly opened a protected path")


if (work / "context.txt").read_text() != "i12 synthetic readable context\n":
    raise SystemExit("native work read failed")
for path in (
    state / "i12-native-sentinel.txt",
    state / "config.toml",
    Path("/etc/jobseek/runner-config.toml"),
    base / "accept-outside/decoy.txt",
    work / "escape",
):
    denied_read(path)
    denied_open_for_write(path)
denied_open_for_write(work / "context.txt")
denied_open_for_write(work / "new.txt")
try:
    (work / "context.txt").unlink()
except OSError as error:
    if error.errno not in (errno.EACCES, errno.EPERM, errno.EROFS):
        raise SystemExit("native delete failed for an unexpected reason") from None
else:
    raise SystemExit("native delete unexpectedly succeeded")
try:
    sock = socket.socket()
except OSError as error:
    if error.errno not in (errno.EACCES, errno.EPERM):
        raise SystemExit("native network failed for an unexpected reason") from None
else:
    sock.close()
    raise SystemExit("native network socket unexpectedly opened")
print("Named native command profile denied private reads, all writes and network.")
