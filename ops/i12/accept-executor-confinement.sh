#!/bin/sh
# Proof that the Linux executor confinement backend mediates every child
# byte: run on the APP HOST as jobseek-api (the executor's uid) with the
# installed wrapper. Mirrors the T16 seatbelt proofs: the op's own proxy
# port succeeds fully observed while direct external egress, DNS, other
# loopback ports and host secrets stay unreachable.
#
# usage: accept-executor-confinement.sh SANDBOX_BINARY [CHROME_BINARY]
#
# A harness failure (fixture servers unreachable, wrapper missing, namespace
# init failure) is a probe failure, never a successful denial. Stage 5
# (browser starts confined) runs only when a chrome binary is given.
set -eu
[ "$(id -un)" = jobseek-api ] || { echo 'run as jobseek-api on the selected app host' >&2; exit 2; }
[ "$(uname -s)" = Linux ] || { echo 'Linux-only probe' >&2; exit 2; }
sandbox=${1:?usage: accept-executor-confinement.sh SANDBOX_BINARY [CHROME_BINARY]}
chrome=${2-}
case "$sandbox" in /*) ;; *) echo 'sandbox path must be absolute' >&2; exit 2;; esac
[ -x "$sandbox" ] || { echo 'sandbox wrapper missing or not executable' >&2; exit 2; }
command -v bwrap >/dev/null 2>&1 || { echo 'bwrap missing' >&2; exit 2; }
command -v socat >/dev/null 2>&1 || { echo 'socat missing' >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo 'python3 missing' >&2; exit 2; }
command -v timeout >/dev/null 2>&1 || { echo 'timeout missing' >&2; exit 2; }

work=$(mktemp -d /tmp/exec-confine.XXXXXXXX)
echo_pid=""
other_pid=""
trap 'rm -rf -- "$work"; [ -n "$echo_pid$other_pid" ] && kill $echo_pid $other_pid 2>/dev/null; true' EXIT HUP INT TERM

cat > "$work/echo_server.py" <<'PY'
import socket, sys
srv = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
srv.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
srv.bind(("127.0.0.1", 0))
srv.listen(16)
print(srv.getsockname()[1], flush=True)
while True:
    conn, _ = srv.accept()
    with conn:
        while True:
            data = conn.recv(65536)
            if not data:
                break
            conn.sendall(data)
PY
cat > "$work/nsprobe.py" <<'PY'
import socket, sys

def echo(port, token):
    s = socket.create_connection(("127.0.0.1", int(port)), timeout=10)
    with s:
        s.sendall(token.encode())
        got = b""
        while len(got) < len(token):
            chunk = s.recv(65536)
            if not chunk:
                break
            got += chunk
    sys.exit(0 if got.decode() == token else 1)

def refuse(port):
    try:
        socket.create_connection(("127.0.0.1", int(port)), timeout=10).close()
    except OSError:
        sys.exit(0)
    sys.exit(1)

def external():
    ok = False
    try:
        socket.create_connection(("192.0.2.1", 80), timeout=5).close()
    except OSError:
        ok = True
    else:
        sys.exit(1)
    try:
        socket.getaddrinfo("probe.invalid", 80)
    except OSError:
        pass
    else:
        sys.exit(1)
    sys.exit(0 if ok else 1)

def files():
    import os
    if os.path.exists("/etc/jobseek/api.env") or os.path.exists("/etc/ssl/private"):
        sys.exit(1)
    if not os.path.isdir("/etc/ssl/certs"):
        sys.exit(1)
    sys.exit(0)

{"echo": lambda: echo(sys.argv[2], sys.argv[3]), "refuse": lambda: refuse(sys.argv[2]),
 "external": external, "files": files}[sys.argv[1]]()
PY

python3 "$work/echo_server.py" > "$work/echo.port" 2>"$work/echo.err" &
echo_pid=$!
python3 "$work/echo_server.py" > "$work/other.port" 2>"$work/other.err" &
other_pid=$!
for _ in $(seq 1 50); do
  [ -s "$work/echo.port" ] && [ -s "$work/other.port" ] && break
  sleep 0.05
done
echo_port=$(cat "$work/echo.port" 2>/dev/null || true)
other_port=$(cat "$work/other.port" 2>/dev/null || true)
case "$echo_port$other_port" in ''|*[!0-9]*) echo 'fixture servers did not start' >&2; exit 2;; esac

# Positive control OUTSIDE the wrapper: the fixture must answer directly,
# or a later denial proves nothing about the sandbox.
python3 "$work/nsprobe.py" echo "$echo_port" confined-ok || { echo 'echo fixture unreachable outside the sandbox' >&2; exit 2; }

profile='(version 1)(allow default)(deny network-outbound)(allow network-outbound (remote ip "localhost:EPORT"))'
profile=$(printf '%s' "$profile" | sed "s/EPORT/$echo_port/")
stage=0
confined() {
  stage=$((stage + 1))
  op=$work/op$stage
  mkdir -m 0700 "$op"
  cp "$work/nsprobe.py" "$op/nsprobe.py"
  (cd "$op" && timeout 60 "$sandbox" -p "$profile" /usr/bin/python3 "$op/nsprobe.py" "$@")
}

confined echo "$echo_port" confined-ok || { echo 'STAGE 1 FAILED: proxy-port echo through the wrapper' >&2; exit 1; }
confined refuse "$other_port" || { echo 'STAGE 2 FAILED: other loopback port reachable from inside' >&2; exit 1; }
confined external || { echo 'STAGE 3 FAILED: external connect or DNS succeeded from inside' >&2; exit 1; }
confined files || { echo 'STAGE 4 FAILED: host secrets visible or CA bundle missing inside' >&2; exit 1; }
echo 'Stages 1-4 passed: proxy-only egress, no DNS, no host secrets.'

if [ -n "$chrome" ]; then
  [ -x "$chrome" ] || { echo 'chrome binary not executable' >&2; exit 2; }
  stage=$((stage + 1))
  op=$work/op$stage
  mkdir -m 0700 "$op"
  (cd "$op" && timeout 120 "$sandbox" -p "$profile" "$chrome" \
    --no-sandbox --disable-gpu --no-first-run --no-default-browser-check \
    --disable-sync --disable-extensions --disable-background-networking \
    --disable-component-update --disable-default-apps --disable-quic \
    --password-store=basic --use-mock-keychain --virtual-time-budget=3000 \
    --user-data-dir="$op/prof" --timeout=60000 \
    --dump-dom about:blank > "$op/dom.html") || { echo 'STAGE 5 FAILED: confined browser did not exit 0' >&2; exit 1; }
  grep -q '<html' "$op/dom.html" || { echo 'STAGE 5 FAILED: no DOM from confined browser' >&2; exit 1; }
  [ ! -e "$op/prof" ] || rm -rf -- "$op/prof"
  echo 'Stage 5 passed: confined browser renders.'
else
  echo 'Stage 5 skipped: no chrome binary given.'
fi
echo 'Executor confinement proof passed.'
