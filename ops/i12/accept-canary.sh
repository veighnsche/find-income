#!/bin/sh
set -eu
[ "$(id -un)" = jobseek-runner ] || { echo 'run as jobseek-runner on runner VM' >&2; exit 2; }
mode=${1:?usage: accept-canary.sh prepare|check|clean}
work=/var/lib/jobseek-runner/work/i12-accept
outside=/var/lib/jobseek-runner/accept-outside
state=/var/lib/jobseek-runner/state
probe=/usr/local/libexec/jobseek-native-permission-probe
case "$mode" in
  prepare)
    [ ! -e "$work" ] && [ -d "$outside" ] && [ -z "$(ls -A "$outside")" ] || { echo 'old canary state exists; inspect then clean explicitly' >&2; exit 2; }
    [ -f "$probe" ] || { echo 'root-owned native probe is missing' >&2; exit 2; }
    [ ! -e "$state/i12-native-sentinel.txt" ] || { echo 'old state sentinel exists' >&2; exit 2; }
    mkdir -m 0700 "$work"
    printf 'i12 synthetic readable context\n' > "$work/context.txt"
    printf 'i12 synthetic private state\n' > "$state/i12-native-sentinel.txt"
    printf 'i12 synthetic outside decoy\n' > "$outside/decoy.txt"
    ln -s "$outside/decoy.txt" "$work/escape"
    cp "$probe" "$work/native-permission-probe.py"
    chmod 0500 "$work/native-permission-probe.py"
    sha256sum "$work/native-permission-probe.py" | cut -d ' ' -f 1 > "$outside/probe.sha256"
    echo 'Synthetic canaries prepared for the named native profile test.'
    ;;
  check)
    [ -f "$work/native-permission-probe.py" ] && [ -f "$outside/probe.sha256" ] || exit 1
    [ "$(sha256sum "$work/native-permission-probe.py" | cut -d ' ' -f 1)" = "$(cat "$outside/probe.sha256")" ] || { echo 'native probe was changed' >&2; exit 1; }
    [ "$(cat "$work/context.txt")" = 'i12 synthetic readable context' ] || exit 1
    [ "$(cat "$state/i12-native-sentinel.txt")" = 'i12 synthetic private state' ] || exit 1
    [ "$(cat "$outside/decoy.txt")" = 'i12 synthetic outside decoy' ] || exit 1
    [ ! -e "$work/new.txt" ] || { echo 'work write succeeded' >&2; exit 1; }
    [ -L "$work/escape" ] || { echo 'work symlink was changed' >&2; exit 1; }
    echo 'Synthetic work, state, outside decoy and probe remain unchanged.'
    ;;
  clean)
    rm -rf -- "$work"
    rm -f -- "$outside/probe.sha256" "$outside/decoy.txt" "$state/i12-native-sentinel.txt"
    echo 'Synthetic canaries removed.'
    ;;
  *) exit 2 ;;
esac
