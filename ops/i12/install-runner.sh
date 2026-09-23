#!/bin/sh
set -eu
[ "$(id -u)" -eq 0 ] || { echo 'run as root on the selected dedicated runner VM' >&2; exit 2; }
artifacts=${1:?usage: install-runner.sh ABSOLUTE_ARTIFACT_DIRECTORY CODEX_SHA256 RUNNER_CONFIG AUTHORIZED_KEYS APP_PRIVATE_IP}
codex_sha=${2:?missing Codex hash}
config=${3:?missing private Codex config}
keys=${4:?missing restricted authorized_keys}
app_ip=${5:?missing app private IP}
for path in "$artifacts" "$config" "$keys"; do
  case "$path" in /*) ;; *) echo 'all paths must be absolute' >&2; exit 2;; esac
done
case "$codex_sha" in *[!0123456789abcdef]*) exit 2;; esac
[ "${#codex_sha}" -eq 64 ] || exit 2
[ -x "$artifacts/bin/codex-runner" ] && [ -x "$artifacts/bin/codex" ] && [ -f "$config" ] && [ -f "$keys" ] || exit 2
[ -f "$artifacts/SHA256SUMS" ] && (cd "$artifacts" && sha256sum -c --status SHA256SUMS) || { echo 'code artifact manifest mismatch' >&2; exit 2; }
[ -x /usr/bin/systemd-run ] && [ -x /usr/bin/ssh ] || { echo 'systemd-run and SSH are required' >&2; exit 2; }
if grep -Eq 'PRIVATE_APP_FQDN|REPLACE_FROM_SECRET_STORE|APP_PRIVATE_IP|REPLACE_WITH_APP_PUBLIC_KEY' "$config" "$keys"; then
  echo 'runner configuration still contains placeholders' >&2
  exit 2
fi
python3 "$(dirname "$0")/check-runtime-tools.py" "$config"
printf '%s  %s\n' "$codex_sha" "$artifacts/bin/codex" | sha256sum -c --status -
[ "$("$artifacts/bin/codex" --version)" = 'codex-cli 0.153.4' ] || exit 2
python3 - "$keys" "$app_ip" <<'PY'
import ipaddress, pathlib, sys
ip = ipaddress.ip_address(sys.argv[2])
allowed = ("10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "100.64.0.0/10", "fc00::/7")
if not any(ip in ipaddress.ip_network(net) for net in allowed):
    raise SystemExit("app source IP must be a private non-loopback address")
lines = [line.strip() for line in pathlib.Path(sys.argv[1]).read_text().splitlines() if line.strip() and not line.lstrip().startswith("#")]
prefix = f'from="{ip}",command="/usr/local/libexec/jobseek-runner-launch",restrict ssh-ed25519 '
if len(lines) != 1 or not lines[0].startswith(prefix) or len(lines[0].split()) < 3:
    raise SystemExit("authorized_keys must contain one source-bound forced key")
PY
if ! id jobseek-runner >/dev/null 2>&1; then
  useradd --system --home-dir /var/lib/jobseek-runner --shell /bin/sh jobseek-runner
fi
install -d -o root -g root -m 0755 /opt/jobseek /opt/jobseek/bin /usr/local/libexec /etc/jobseek /var/lib/jobseek-runner
install -d -o jobseek-runner -g jobseek-runner -m 0700 /var/lib/jobseek-runner/state /var/lib/jobseek-runner/work
install -d -o jobseek-runner -g jobseek-runner -m 0700 /var/lib/jobseek-runner/accept-outside
install -d -o root -g root -m 0755 /var/lib/jobseek-runner/.ssh
install -o root -g root -m 0755 "$artifacts/bin/codex-runner" /opt/jobseek/bin/codex-runner
install -o root -g root -m 0755 "$artifacts/bin/codex" /opt/jobseek/bin/codex
install -o root -g jobseek-runner -m 0640 "$config" /etc/jobseek/runner-config.toml
install -o root -g root -m 0644 "$keys" /var/lib/jobseek-runner/.ssh/authorized_keys
runuser -u jobseek-runner -- test -r /var/lib/jobseek-runner/.ssh/authorized_keys || { echo 'runner account cannot read authorized_keys' >&2; exit 2; }
install -o root -g root -m 0755 "$(dirname "$0")/runner-launcher.sh" /usr/local/libexec/jobseek-runner-launch
install -o root -g root -m 0755 "$(dirname "$0")/accept-native-profile.sh" /usr/local/libexec/jobseek-accept-native-profile
install -o root -g root -m 0755 "$(dirname "$0")/native-permission-probe.py" /usr/local/libexec/jobseek-native-permission-probe
install -o root -g root -m 0755 "$(dirname "$0")/accept-native-rollout.py" /usr/local/libexec/jobseek-accept-native-rollout
install -o root -g root -m 0755 "$(dirname "$0")/accept-canary.sh" /usr/local/libexec/jobseek-accept-canary
printf '%s\n' "$codex_sha" > /etc/jobseek/codex.sha256
chown root:root /etc/jobseek/codex.sha256
chmod 0644 /etc/jobseek/codex.sha256
touch /var/lib/jobseek-runner/state/config.toml
chown jobseek-runner:jobseek-runner /var/lib/jobseek-runner/state/config.toml
chmod 0600 /var/lib/jobseek-runner/state/config.toml
loginctl enable-linger jobseek-runner
echo 'Installed runner files; launcher, cgroup and namespace acceptance remain required.'
