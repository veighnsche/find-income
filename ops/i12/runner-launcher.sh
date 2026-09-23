#!/bin/sh
set -eu

# SSH authorized_keys must force this file. Refuse any other requested command.
[ "${SSH_ORIGINAL_COMMAND-}" = /usr/local/libexec/jobseek-runner-launch ] || exit 126
[ -z "${SSH_TTY-}" ] || exit 126
[ -x /opt/jobseek/bin/codex-runner ] || exit 126
[ -x /opt/jobseek/bin/codex ] || exit 126
[ -f /etc/jobseek/runner-config.toml ] || exit 126
[ -f /var/lib/jobseek-runner/state/config.toml ] || exit 126

uid=$(id -u)
runtime_dir=/run/user/$uid
[ -S "$runtime_dir/bus" ] || exit 126

# The fixed transient unit is the single-run lock. Its main-process exit tears
# down the entire cgroup, including helpers left behind by a crashed runner.
# A user service needs PrivateUsers for the read-only config bind namespace.
exec /usr/bin/env -i PATH=/usr/bin:/bin LANG=C LC_ALL=C XDG_RUNTIME_DIR="$runtime_dir" DBUS_SESSION_BUS_ADDRESS="unix:path=$runtime_dir/bus" \
  /usr/bin/systemd-run --user --quiet --pipe --wait --collect --unit=jobseek-codex \
  -p ExitType=main -p KillMode=control-group -p TimeoutStopSec=5s -p RuntimeMaxSec=45min \
  -p NoNewPrivileges=yes -p PrivateUsers=yes -p ProtectSystem=strict \
  -p ReadWritePaths=/var/lib/jobseek-runner/state -p ReadWritePaths=/var/lib/jobseek-runner/work \
  -p BindReadOnlyPaths=/etc/jobseek/runner-config.toml:/var/lib/jobseek-runner/state/config.toml \
  /usr/bin/env -i PATH=/usr/bin:/bin LANG=C LC_ALL=C \
  JOBSEEK_RUNNER_CODEX_BINARY=/opt/jobseek/bin/codex \
  JOBSEEK_RUNNER_CODEX_SHA256="$(cat /etc/jobseek/codex.sha256)" \
  JOBSEEK_RUNNER_STATE_DIR=/var/lib/jobseek-runner/state \
  JOBSEEK_RUNNER_WORK_DIR=/var/lib/jobseek-runner/work \
  /opt/jobseek/bin/codex-runner
