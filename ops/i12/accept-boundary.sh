#!/bin/sh
set -eu
[ "$(id -un)" = jobseek-runner ] || { echo 'run as jobseek-runner on the selected runner VM' >&2; exit 2; }
command -v bwrap >/dev/null
[ -d /var/lib/jobseek-runner/work ] && [ -d /var/lib/jobseek-runner/state ] || exit 2
[ ! -e /var/lib/jobseek/data ] && [ ! -e /etc/jobseek/api.env ] && [ ! -e /run/podman/podman.sock ] || { echo 'app data, env or host socket is present on runner' >&2; exit 1; }

work=$(mktemp -d /var/lib/jobseek-runner/work/.boundary.XXXXXXXX)
host_decoy=$(mktemp /var/tmp/jobseek-private-decoy.XXXXXXXX)
trap 'rm -rf -- "$work"; rm -f -- "$host_decoy"' EXIT HUP INT TERM
printf 'approved context\n' > "$work/context"
printf 'synthetic app secret\n' > "$host_decoy"
ln -s "$host_decoy" "$work/escape"

# This verifies that Linux user/mount namespaces actually start, can read the
# allowed context and write the workspace, yet cannot see a host decoy or a
# symlink to it. It does not substitute for a live Codex tool sandbox test.
TYPESAFE_API_KEY=synthetic-decoy bwrap --unshare-all --share-net --die-with-parent --new-session \
  --clearenv --setenv PATH /usr/bin:/bin --setenv HOME /work --setenv DECOY "$host_decoy" \
  --ro-bind /usr /usr --proc /proc --dev /dev --tmpfs /tmp \
  --dir /work --bind "$work" /work --chdir /work \
  /usr/bin/sh -eu -c '
    [ "$(cat context)" = "approved context" ]
    printf "workspace write\n" > proof
    [ ! -e escape ]
    [ ! -e "$DECOY" ]
    [ ! -e /etc/jobseek/api.env ]
    [ -z "${TYPESAFE_API_KEY-}" ]
  '
[ "$(cat "$work/proof")" = 'workspace write' ] || exit 1

# A runner with shared network must have host firewall rules blocking metadata.
python3 - <<'PY'
import socket
try:
    socket.create_connection(("169.254.169.254", 80), timeout=2).close()
except (OSError, TimeoutError):
    pass
else:
    raise SystemExit("metadata endpoint reachable from runner")
PY
echo 'Synthetic namespace positive/negative checks passed; live Codex tool containment is still required.'
