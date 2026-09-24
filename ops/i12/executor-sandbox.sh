#!/bin/sh
# Linux confinement backend for the T16 sandbox.go seam.
#
# Installed root-owned 0755 at /usr/local/libexec/jobseek-executor-sandbox and
# wired as the executor's SandboxBinary. CLI-compatible with the seatbelt
# frontend it replaces: sandbox-exec -p '<profile>' <cmd> [args...], invoked
# with cwd=opHome and the executor's fixed environment.
#
# Mechanism: the child runs in a fresh network namespace (bwrap
# --unshare-net) whose loopback carries exactly one listener: an in-namespace
# socat forwarding 127.0.0.1:<proxyport> over a per-op Unix socket to an
# outside socat dialing the executor's real recording proxy on 127.0.0.1.
# No route leaves the namespace, DNS has no resolver, and no other loopback
# port listens, so direct external egress, bypass connects and other-port
# connects all fail while proxied requests succeed fully observed. This
# mirrors the seatbelt profile's guarantee (outbound IP only to the op's own
# proxy port); see accept-executor-confinement.sh for the proof procedure.
#
# Fail-closed: missing bwrap/socat/python3, an unparseable profile, a dead
# bridge or a failed namespace init exits 125/126 with nothing executed.
# Only /usr, /opt/jobseek (the install tree: binaries and web assets, no
# secrets), /etc/ssl/certs (public CA bundle: the browser/python verify
# origin TLS through the CONNECT tunnel themselves) and the op dir are
# visible inside. /etc as a whole is never bound.
set -eu

# parse_proxy_port extracts the numeric proxy port from the seatbelt profile
# text. Prints the port; returns 1 on any other shape.
parse_proxy_port() {
  port=$(expr " ${1-}" : '.*localhost:\([0-9][0-9]*\)' 2>/dev/null) || return 1
  case "$port" in ''|*[!0-9]*) return 1;; esac
  [ "$port" -ge 1 ] 2>/dev/null && [ "$port" -le 65535 ] 2>/dev/null || return 1
  printf '%s' "$port"
}

main() {
  [ "${1-}" = "-p" ] || { echo 'executor-sandbox: first argument must be -p' >&2; exit 126; }
  profile=${2-}
  case "$profile" in *network-outbound*) ;; *) echo 'executor-sandbox: not a sandbox profile' >&2; exit 126;; esac
  port=$(parse_proxy_port "$profile") || { echo 'executor-sandbox: no numeric proxy port in profile' >&2; exit 126; }
  shift 2
  target=${1-}
  case "$target" in /*) ;; *) echo 'executor-sandbox: target must be absolute' >&2; exit 126;; esac
  [ -f "$target" ] && [ -x "$target" ] || { echo 'executor-sandbox: target not executable' >&2; exit 126; }
  command -v bwrap >/dev/null 2>&1 || { echo 'executor-sandbox: bwrap missing' >&2; exit 126; }
  command -v socat >/dev/null 2>&1 || { echo 'executor-sandbox: socat missing' >&2; exit 126; }
  command -v python3 >/dev/null 2>&1 || { echo 'executor-sandbox: python3 missing' >&2; exit 126; }

  op_home=$(pwd -P)
  sock=$op_home/.jobseek-proxy.sock
  rm -f -- "$sock"
  socat_pid=""
  cleanup() {
    if [ -n "$socat_pid" ]; then kill "$socat_pid" 2>/dev/null || true; fi
    rm -f -- "$sock"
  }
  trap cleanup EXIT HUP INT TERM
  socat "UNIX-LISTEN:$sock,fork,mode=600" "TCP:127.0.0.1:$port" &
  socat_pid=$!
  for _ in $(seq 1 50); do
    [ -S "$sock" ] && break
    sleep 0.05
  done
  [ -S "$sock" ] || { echo 'executor-sandbox: proxy bridge did not start' >&2; exit 126; }

  set +e
  bwrap --unshare-user --unshare-pid --unshare-net --unshare-uts --unshare-ipc \
    --uid 0 --gid 0 --die-with-parent --new-session \
    --proc /proc --dev /dev --tmpfs /tmp \
    --symlink usr/bin /bin \
    --ro-bind /usr /usr \
    --ro-bind-try /opt/jobseek /opt/jobseek \
    --ro-bind /etc/ssl/certs /etc/ssl/certs \
    --bind "$op_home" "$op_home" \
    --chdir "$op_home" \
    -- /usr/bin/sh -eu -c '
      port=$1; sock=$2; shift 2
      python3 -c "import fcntl, socket, struct
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
req = struct.pack(\"16sh\", b\"lo\", 0)
flags = struct.unpack(\"16sh\", fcntl.ioctl(s.fileno(), 0x8913, req))[1]
fcntl.ioctl(s.fileno(), 0x8914, struct.pack(\"16sh\", b\"lo\", flags | 0x1 | 0x40))" || exit 125
      socat "TCP-LISTEN:$port,bind=127.0.0.1,reuseaddr,fork" "UNIX-CONNECT:$sock" &
      inner=$!
      python3 - "$port" -c "import socket, sys, time
for _ in range(50):
    try:
        socket.create_connection((\"127.0.0.1\", int(sys.argv[1])), timeout=0.5).close()
        break
    except OSError:
        time.sleep(0.05)
else:
    raise SystemExit(1)" || { kill "$inner" 2>/dev/null || true; exit 125; }
      exec "$@"
    ' jobseek-ns-init "$port" "$sock" "$@"
  rc=$?
  set -e
  exit "$rc"
}

# Sourcing this file defines helpers without running anything (the case
# below only fires when executed), so the profile parser is unit-testable.
case "${0##*/}" in
  executor-sandbox.sh|jobseek-executor-sandbox) main "$@" ;;
esac
