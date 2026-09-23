#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'run as root on the selected app host' >&2; exit 2; }
artifacts=${1:?usage: install-app.sh ABSOLUTE_ARTIFACT_DIRECTORY API_ENV_FILE CADDYFILE SSH_PRIVATE_KEY KNOWN_HOSTS APPROVED_CAREER_DIRECTORY}
api_env=${2:?missing API environment file}
caddyfile=${3:?missing rendered Caddyfile}
ssh_key=${4:?missing dedicated SSH private key}
known_hosts=${5:?missing pinned runner known_hosts}
career_sources=${6:?missing approved career directory}
case "$artifacts$api_env$caddyfile$ssh_key$known_hosts$career_sources" in *'REPLACE'* ) exit 2;; esac
for path in "$artifacts" "$api_env" "$caddyfile" "$ssh_key" "$known_hosts" "$career_sources"; do
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
if ! id jobseek-api >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/jobseek --shell /usr/sbin/nologin jobseek-api
fi
if ! id jobseek-proxy >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/jobseek-proxy --shell /usr/sbin/nologin jobseek-proxy
fi
install -d -o root -g root -m 0755 /opt/jobseek /opt/jobseek/bin /opt/jobseek/web /etc/jobseek /etc/jobseek/ssh
install -d -o jobseek-api -g jobseek-api -m 0700 /var/lib/jobseek /var/lib/jobseek/data
install -d -o jobseek-proxy -g jobseek-proxy -m 0700 /var/lib/jobseek-proxy
install -d -o root -g jobseek-api -m 0750 /var/lib/jobseek/assets
for name in cv-vince-liem.typ cv-vince-liem.md github-evidence-review.md portfolio-case-studies.md; do
  install -o root -g jobseek-api -m 0640 "$career_sources/$name" "/var/lib/jobseek/assets/$name"
done
install -o root -g root -m 0755 "$artifacts/bin/jobseek-api" /opt/jobseek/bin/jobseek-api
install -o root -g root -m 0755 "$artifacts/bin/typst" /opt/jobseek/bin/typst
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
