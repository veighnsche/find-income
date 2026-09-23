#!/bin/sh
set -eu
[ "$(id -un)" = jobseek-runner ] || { echo 'run as jobseek-runner on runner VM' >&2; exit 2; }
[ -x /opt/jobseek/bin/codex ] && [ -x /usr/bin/python3 ] || { echo 'pinned Codex and Python are required' >&2; exit 2; }
[ -f /var/lib/jobseek-runner/work/i12-accept/native-permission-probe.py ] || { echo 'prepare synthetic canary first' >&2; exit 2; }
[ -f /etc/jobseek/runner-config.toml ] && [ -f /var/lib/jobseek-runner/state/config.toml ] || { echo 'runner config is missing' >&2; exit 2; }
uid=$(id -u)
runtime_dir=/run/user/$uid
[ -S "$runtime_dir/bus" ] || { echo 'runner user manager is unavailable' >&2; exit 2; }
if XDG_RUNTIME_DIR="$runtime_dir" systemctl --user is-active --quiet jobseek-codex.service; then
  echo 'stop the active runner before native profile acceptance' >&2
  exit 2
fi
# Use the same trusted outer unit settings and bind mount as the SSH launcher.
# The command inside it uses the exact named profile selected by config.toml.
exec /usr/bin/env -i PATH=/usr/bin:/bin LANG=C LC_ALL=C XDG_RUNTIME_DIR="$runtime_dir" DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
  /usr/bin/systemd-run --user --quiet --pipe --wait --collect --unit=jobseek-native-accept \
  -p ExitType=main -p KillMode=control-group -p TimeoutStopSec=5s -p RuntimeMaxSec=3min \
  -p NoNewPrivileges=yes -p PrivateUsers=yes -p ProtectSystem=strict \
  -p ReadWritePaths=/var/lib/jobseek-runner/state -p ReadWritePaths=/var/lib/jobseek-runner/work \
  -p BindReadOnlyPaths=/etc/jobseek/runner-config.toml:/var/lib/jobseek-runner/state/config.toml \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C LC_ALL=C \
  HOME=/var/lib/jobseek-runner/state CODEX_HOME=/var/lib/jobseek-runner/state TMPDIR=/var/lib/jobseek-runner/state \
  /opt/jobseek/bin/codex sandbox -P jobseek-native -C /var/lib/jobseek-runner/work -- \
  /usr/bin/python3 /var/lib/jobseek-runner/work/i12-accept/native-permission-probe.py
