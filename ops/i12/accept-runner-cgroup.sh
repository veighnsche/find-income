#!/bin/sh
set -eu
[ "$(id -un)" = jobseek-runner ] || { echo 'run as jobseek-runner on runner VM' >&2; exit 2; }
mode=${1:?usage: accept-runner-cgroup.sh check|kill-main}
unit=jobseek-codex.service
case "$mode" in
  check) ;;
  kill-main)
    [ "$(systemctl --user show --value -p ActiveState "$unit")" = active ] || { echo 'test runner is not active' >&2; exit 1; }
    systemctl --user kill --kill-whom=main --signal=SIGKILL "$unit"
    ;;
  *) exit 2 ;;
esac
for _ in 1 2 3 4 5 6 7 8 9 10; do
  state=$(systemctl --user show --value -p ActiveState "$unit" 2>/dev/null || true)
  group=$(systemctl --user show --value -p ControlGroup "$unit" 2>/dev/null || true)
  if [ "$state" != active ] && [ "$state" != activating ]; then
    if [ -z "$group" ] || [ ! -e "/sys/fs/cgroup$group/cgroup.procs" ] || [ ! -s "/sys/fs/cgroup$group/cgroup.procs" ]; then
      echo 'Runner unit is inactive and its cgroup has no processes.'
      exit 0
    fi
  fi
  sleep 1
done
echo 'Runner unit or cgroup remained active after close/main death' >&2
exit 1
