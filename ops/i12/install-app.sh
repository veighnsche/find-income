#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'run as root on the selected app host' >&2; exit 2; }
artifacts=${1:?usage: install-app.sh ABSOLUTE_ARTIFACT_DIRECTORY API_ENV_FILE CADDYFILE SSH_PRIVATE_KEY KNOWN_HOSTS APPROVED_CAREER_DIRECTORY HEADLESS_SHELL_ZIP}
api_env=${2:?missing API environment file}
caddyfile=${3:?missing rendered Caddyfile}
ssh_key=${4:?missing dedicated SSH private key}
known_hosts=${5:?missing pinned runner known_hosts}
career_sources=${6:?missing approved career directory}
shell_zip=${7:?missing approved headless-shell zip}
case "$artifacts$api_env$caddyfile$ssh_key$known_hosts$career_sources$shell_zip" in *'REPLACE'* ) exit 2;; esac
for path in "$artifacts" "$api_env" "$caddyfile" "$ssh_key" "$known_hosts" "$career_sources" "$shell_zip"; do
  case "$path" in /*) ;; *) echo 'all paths must be absolute' >&2; exit 2;; esac
done
[ -x "$artifacts/bin/jobseek-api" ] && [ -x "$artifacts/bin/typst" ] && [ -f "$artifacts/web/index.html" ] || exit 2
[ -f "$artifacts/SHA256SUMS" ] && (cd "$artifacts" && sha256sum -c --status SHA256SUMS) || { echo 'code artifact manifest mismatch' >&2; exit 2; }
[ -x /usr/bin/caddy ] || { echo 'Caddy is required at /usr/bin/caddy' >&2; exit 2; }
[ -f "$api_env" ] && [ -f "$caddyfile" ] && [ -f "$ssh_key" ] && [ -f "$known_hosts" ] || exit 2
if grep -Eq 'PRIVATE_APP_FQDN|RUNNER_PRIVATE_DNS|OWNER_SELECTED_|REPLACE_FROM_SECRET_STORE|PRIVATE_APP_IP' "$api_env" "$caddyfile"; then
  echo 'app configuration still contains placeholders' >&2
  exit 2
fi
for name in cv-vince-liem.typ cv-vince-liem.md github-evidence-review.md portfolio-case-studies.md; do
  [ -f "$career_sources/$name" ] && [ ! -L "$career_sources/$name" ] || { echo 'missing regular approved career asset' >&2; exit 2; }
done
[ -f "$shell_zip" ] && [ ! -L "$shell_zip" ] || { echo 'missing regular headless-shell zip' >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo 'python3 is required' >&2; exit 2; }
command -v bwrap >/dev/null 2>&1 || { echo 'bwrap is required for executor confinement' >&2; exit 2; }
command -v socat >/dev/null 2>&1 || { echo 'socat is required for executor confinement' >&2; exit 2; }
pin() { grep "^headless_shell " "$(dirname "$0")/runtime-pins.txt" | tr ' ' '\n' | grep "^$1=" | cut -d= -f2; }
zip_sha=$(pin linux64_zip_sha256)
bin_sha=$(pin linux64_binary_sha256)
shell_version=$(pin browser_version)
case "$zip_sha$bin_sha" in *[!0123456789abcdef]*) exit 2;; esac
[ "${#zip_sha}" -eq 64 ] && [ "${#bin_sha}" -eq 64 ] && [ -n "$shell_version" ] || exit 2
printf '%s  %s\n' "$zip_sha" "$shell_zip" | sha256sum -c --status - || { echo 'headless-shell zip digest mismatch' >&2; exit 2; }
for var in JOBSEEK_ARTIFACT_ROOT JOBSEEK_RESEARCH_SCRATCH_ROOT JOBSEEK_RESEARCH_CHROME_PATH JOBSEEK_RESEARCH_CHROME_SHA256 JOBSEEK_RESEARCH_PYTHON_PATH JOBSEEK_RESEARCH_SANDBOX_BINARY; do
  grep -q "^$var=" "$api_env" || { echo "api env misses $var" >&2; exit 2; }
done
env_sha=$(grep '^JOBSEEK_RESEARCH_CHROME_SHA256=' "$api_env" | cut -d= -f2)
[ "$env_sha" = "$bin_sha" ] || { echo 'api env shell pin differs from runtime pins' >&2; exit 2; }
if ! id jobseek-api >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/jobseek --shell /usr/sbin/nologin jobseek-api
fi
if ! id jobseek-proxy >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/jobseek-proxy --shell /usr/sbin/nologin jobseek-proxy
fi
install -d -o root -g root -m 0755 /opt/jobseek /opt/jobseek/bin /opt/jobseek/web /etc/jobseek /etc/jobseek/ssh
install -d -o jobseek-api -g jobseek-api -m 0700 /var/lib/jobseek /var/lib/jobseek/data
install -d -o jobseek-api -g jobseek-api -m 0700 /var/lib/jobseek/artifacts
install -d -o jobseek-api -g jobseek-api -m 0700 /var/lib/jobseek/scratch
install -d -o jobseek-proxy -g jobseek-proxy -m 0700 /var/lib/jobseek-proxy
install -d -o root -g jobseek-api -m 0750 /var/lib/jobseek/assets
for name in cv-vince-liem.typ cv-vince-liem.md github-evidence-review.md portfolio-case-studies.md; do
  install -o root -g jobseek-api -m 0640 "$career_sources/$name" "/var/lib/jobseek/assets/$name"
done
install -o root -g root -m 0755 "$artifacts/bin/jobseek-api" /opt/jobseek/bin/jobseek-api
install -o root -g root -m 0755 "$artifacts/bin/typst" /opt/jobseek/bin/typst
if [ -e /opt/jobseek/headless-shell ]; then
  echo 'existing headless-shell install; inspect then remove explicitly' >&2
  exit 2
fi
stage=$(mktemp -d /tmp/jobseek-shell.XXXXXXXX)
trap 'rm -rf -- "$stage"' EXIT HUP INT TERM
python3 - "$shell_zip" "$stage" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as zf:
    for name in zf.namelist():
        if name.startswith("/") or ".." in name.split("/"):
            raise SystemExit("unsafe zip entry: " + name)
    zf.extractall(sys.argv[2])
PY
top=$(printf '%s\n' "$stage"/*/ | head -n 1)
# zipfile extraction drops unix exec bits; restore the known entry point
# before any check or move (digest covers content, not mode).
[ -f "$top/chrome-headless-shell" ] || { echo 'shell binary missing from zip' >&2; exit 2; }
chmod 0755 "$top/chrome-headless-shell"
printf '%s  %s\n' "$bin_sha" "$top/chrome-headless-shell" | sha256sum -c --status - || { echo 'shell binary digest mismatch' >&2; exit 2; }
"$top/chrome-headless-shell" --version | grep -q "$shell_version" || { echo 'shell version mismatch' >&2; exit 2; }
mv "$top" /opt/jobseek/headless-shell
chown -R root:root /opt/jobseek/headless-shell
chmod 0755 /opt/jobseek/headless-shell /opt/jobseek/headless-shell/chrome-headless-shell
trap - EXIT HUP INT TERM
rm -rf -- "$stage"
install -o root -g root -m 0755 "$(dirname "$0")/executor-sandbox.sh" /usr/local/libexec/jobseek-executor-sandbox
python3 "$(dirname "$0")/verify-runtime-pins.py" headless-shell /opt/jobseek/headless-shell/chrome-headless-shell linux64
python3 "$(dirname "$0")/verify-runtime-pins.py" python "$(grep '^JOBSEEK_RESEARCH_PYTHON_PATH=' "$api_env" | cut -d= -f2)"
cp -R "$artifacts/web/." /opt/jobseek/web/
chown -R root:root /opt/jobseek/web
find /opt/jobseek/web -type d -exec chmod 0755 {} +
find /opt/jobseek/web -type f -exec chmod 0644 {} +
install -o root -g root -m 0600 "$api_env" /etc/jobseek/api.env
install -o root -g root -m 0644 "$caddyfile" /etc/jobseek/Caddyfile
/usr/bin/caddy validate --config /etc/jobseek/Caddyfile --adapter caddyfile >/dev/null
install -o jobseek-api -g jobseek-api -m 0600 "$ssh_key" /etc/jobseek/ssh/runner_key
install -o root -g root -m 0644 "$known_hosts" /etc/jobseek/ssh/known_hosts
install -o root -g root -m 0644 "$(dirname "$0")/app.service" /etc/systemd/system/jobseek-api.service
install -o root -g root -m 0644 "$(dirname "$0")/caddy.service" /etc/systemd/system/jobseek-caddy.service
systemctl daemon-reload
echo 'Installed app files and unit; validate Caddy/TLS/firewall and start explicitly.'
