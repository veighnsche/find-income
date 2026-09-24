#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'run as root on the selected app host' >&2; exit 2; }
db=${1:?usage: accept-idle.sh ABSOLUTE_SQLITE_FILE PRIVATE_HTTPS_ORIGIN CA_CERT [SCRATCH_DIR]}
origin=${2:?missing private origin}
ca=${3:?missing CA certificate}
scratch=${4-}
case "$db" in /*) ;; *) exit 2;; esac
case "$origin" in https://*) ;; *) exit 2;; esac
[ -f "$db" ] && [ -f "$ca" ] || exit 2
if [ -n "$scratch" ]; then case "$scratch" in /*) ;; *) exit 2;; esac; fi
snapshot() {
  python3 - "$db" <<'PY'
import sqlite3, sys
db = sqlite3.connect("file:" + sys.argv[1] + "?mode=ro", uri=True)
for table in ("rounds", "round_attempts", "jobs", "job_attempts", "run_events"):
    print(table, db.execute("SELECT count(*) FROM " + table).fetchone()[0])
print("claimed", db.execute("SELECT count(*) FROM research_requests WHERE state='claimed'").fetchone()[0])
print("active_claims", db.execute("SELECT count(*) FROM run_checkpoints WHERE active_claims_json<>'[]'").fetchone()[0])
print("sending", db.execute("SELECT count(*) FROM delivery_items WHERE state='sending'").fetchone()[0])
PY
}
before=$(snapshot)
code=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --cacert "$ca" "$origin/api/v1/auth/session")
case "$code" in 2*|4*) ;; *) echo 'private app pre-restart check failed' >&2; exit 1;; esac
systemctl restart jobseek-api.service
systemctl is-active --quiet jobseek-api.service
code=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --cacert "$ca" "$origin/api/v1/auth/session")
case "$code" in 2*|4*) ;; *) echo 'private app post-restart check failed' >&2; exit 1;; esac
unauth=$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' --cacert "$ca" -X POST -H 'Content-Type: application/json' --data '{}' "$origin/api/v1/codex/mcp")
[ "$unauth" = 401 ] || { echo 'MCP accepted unauthenticated request' >&2; exit 1; }
after=$(snapshot)
[ "$before" = "$after" ] || { echo 'restart created recruitment work' >&2; exit 1; }
claimed=$(printf '%s\n' "$after" | awk '$1=="claimed"{print $2}')
active_claims=$(printf '%s\n' "$after" | awk '$1=="active_claims"{print $2}')
sending=$(printf '%s\n' "$after" | awk '$1=="sending"{print $2}')
[ "$claimed" = 0 ] && [ "$active_claims" = 0 ] && [ "$sending" = 0 ] || { echo 'idle host holds live research claims or sending deliveries' >&2; exit 1; }
if [ -n "$scratch" ]; then
  [ -d "$scratch" ] || { echo 'executor scratch dir missing' >&2; exit 1; }
  [ -z "$(ls -A "$scratch")" ] || { echo 'executor scratch not empty while idle' >&2; exit 1; }
fi
echo 'Private app restarted; no new round/job/attempt/event rows, no live claims, no sending deliveries; unauthenticated MCP denied.'
