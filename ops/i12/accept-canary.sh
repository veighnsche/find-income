#!/bin/sh
set -eu
[ "$(id -un)" = jobseek-runner ] || { echo 'run as jobseek-runner on runner VM' >&2; exit 2; }
mode=${1:?usage: accept-canary.sh prepare|check|clean}
work=/var/lib/jobseek-runner/work/i12-accept
outside=/var/lib/jobseek-runner/accept-outside
case "$mode" in
  prepare)
    [ ! -e "$work" ] && [ -d "$outside" ] && [ -z "$(ls -A "$outside")" ] || { echo 'old canary state exists; inspect then clean explicitly' >&2; exit 2; }
    mkdir -m 0700 "$work"
    printf 'i12 synthetic allowed context\n' > "$work/context.txt"
    ln -s "$outside/escape-write" "$work/escape"
    cat > "$work/probe.sh" <<'SH'
#!/bin/sh
set -eu
cd /var/lib/jobseek-runner/work/i12-accept
[ "$(cat context.txt)" = 'i12 synthetic allowed context' ]
printf 'allowed\n' > allowed.txt
[ ! -e /var/lib/jobseek/data/jobseek.sqlite ]
[ ! -e /etc/jobseek/api.env ]
[ ! -e /run/podman/podman.sock ]
if printf 'escape\n' > /var/lib/jobseek-runner/accept-outside/outside-write 2>/dev/null; then exit 1; fi
if printf 'escape\n' > escape 2>/dev/null; then exit 1; fi
[ ! -e /var/lib/jobseek-runner/accept-outside/outside-write ]
[ ! -e /var/lib/jobseek-runner/accept-outside/escape-write ]
printf 'PASS\n' > result.txt
SH
    chmod 0500 "$work/probe.sh"
    sha256sum "$work/probe.sh" | cut -d ' ' -f 1 > "$outside/probe.sha256"
    echo 'Synthetic canary prepared; run the pre-flag App Server sandbox probe.'
    ;;
  check)
    [ -f "$work/probe.sh" ] && [ -f "$outside/probe.sha256" ] || exit 1
    [ "$(sha256sum "$work/probe.sh" | cut -d ' ' -f 1)" = "$(cat "$outside/probe.sha256")" ] || { echo 'probe script was changed' >&2; exit 1; }
    [ "$(cat "$work/allowed.txt")" = allowed ] && [ "$(cat "$work/result.txt")" = PASS ] || exit 1
    [ ! -e "$outside/outside-write" ] && [ ! -e "$outside/escape-write" ] || { echo 'outside write succeeded' >&2; exit 1; }
    echo 'Live tool ran unchanged canary; allowed read/write worked and outside writes failed.'
    ;;
  clean)
    rm -rf -- "$work"
    rm -f -- "$outside/probe.sha256" "$outside/outside-write" "$outside/escape-write"
    echo 'Synthetic canaries removed.'
    ;;
  *) exit 2 ;;
esac
