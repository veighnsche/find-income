#!/usr/bin/env python3
"""Read-only protocol/forced-command probe; run from app host with dedicated key."""
import json
import subprocess
import sys
import time

if len(sys.argv) not in (5, 6):
    raise SystemExit("usage: accept-runner-transport.py USER@HOST IDENTITY KNOWN_HOSTS LAUNCHER [HOLD_SECONDS]")
target, identity, known_hosts, launcher = sys.argv[1:5]
hold = int(sys.argv[5]) if len(sys.argv) == 6 else 0
if hold < 0 or hold > 60:
    raise SystemExit("HOLD_SECONDS must be 0..60")
base = ["/usr/bin/ssh", "-F", "/dev/null", "-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "IdentitiesOnly=yes", "-o", "ClearAllForwardings=yes", "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ConnectTimeout=10", "-o", "UserKnownHostsFile=" + known_hosts, "-i", identity, "--", target]

# A different command must never yield a shell or a successful exit.
denied = subprocess.run(base + ["/bin/sh"], input=b"", stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=20)
if denied.returncode == 0 or denied.stdout:
    raise SystemExit("restricted SSH key accepted an arbitrary command")

proc = subprocess.Popen(base + [launcher], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
try:
    request = {"method": "initialize", "id": 1, "params": {"clientInfo": {"name": "jobseek-i12-probe", "version": "1"}}}
    proc.stdin.write(json.dumps(request, separators=(",", ":")).encode() + b"\n")
    proc.stdin.flush()
    import selectors
    ready = selectors.DefaultSelector()
    ready.register(proc.stdout, selectors.EVENT_READ)
    if not ready.select(timeout=20):
        raise SystemExit("App Server initialize did not answer")
    line = proc.stdout.readline(1 << 20)
    reply = json.loads(line)
    if reply.get("id") != 1 or "result" not in reply:
        raise SystemExit("App Server initialize failed")
    print("Restricted SSH launcher returned an App Server initialize response.")
    if hold:
        time.sleep(hold)
finally:
    proc.stdin.close()
    try:
        proc.wait(timeout=10)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait(timeout=10)
        raise SystemExit("runner did not stop after SSH stdin closed")
    time.sleep(1)
print("Transport closed. Check the user systemd cgroup is empty on the runner VM.")
