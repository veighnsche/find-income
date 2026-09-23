# I12 private app and dedicated runner package

This is the deployable Linux amd64 package for the existing Go API, web assets and restricted SSH/App Server runtime. It is **not** a deployment record. Choose one private app host and a **different dedicated runner VM**. The runner VM must contain no dashboard SQLite, backups, Jev/admin/API credentials, personal home mount or container-engine socket. The app host holds SQLite and `TYPESAFE_API_KEY`; only it serves the browser and MCP route. Both observed `infra` and `linux` hosts are candidates, but their placement is undecided. Do not copy the owner's current Codex home or credentials. App Server remains pinned to `codex-cli 0.153.4` and signs into the owner's ChatGPT account through the supported device-code flow.

The package deliberately uses one transport: the API's existing SSH client sends JSON lines through a restricted key to `/usr/local/libexec/jobseek-runner-launch`. A fixed, single transient `jobseek-codex` **user systemd service** owns the remote process cgroup. `codex-runner` pins and starts App Server on stdio with `CODEX_HOME=/var/lib/jobseek-runner/state` and work dir `/var/lib/jobseek-runner/work`. App Server reaches the application's private HTTPS `https://PRIVATE_APP_FQDN/api/v1/codex/mcp`; the production bridge requires its separate bearer token and each tool requires round capability and allowance. The browser reaches the same origin through a private Caddy listener. Caddy and firewall/TLS are deployment prerequisites, not automatically installed or enabled by these scripts. `JOBSEEK_CODEX_ISOLATION_VERIFIED` stays `false` until the target host checks below pass.

## Build and pin artifacts

Run on a trusted build machine with the repository's pinned Go/Node/pnpm dependencies. The output directory must be outside the checkout and must not contain secrets:

```sh
dashboard/ops/i12/build.sh /private/tmp/jobseek-i12-artifacts
```

Supply **approved Linux amd64** Codex and Typst binaries into `bin/codex` and `bin/typst` in that directory. Verify their provenance outside this package; do not use a macOS checksum on Linux. Record their SHA-256 hashes in a private deployment record and run:

```sh
dashboard/ops/i12/verify-artifacts.sh /private/tmp/jobseek-i12-artifacts CODEX_LINUX_SHA256 TYPST_LINUX_SHA256
```

The build writes a `SHA256SUMS` manifest for the Go binaries and web assets; the installers verify it on Linux. Record the manifest hash in the private deployment record as well as the separately approved runtime pins, so the copied artifact set can be compared against the reviewed build. The pin check requires exact `codex-cli 0.153.4` and Typst `0.15.1`. Typst is installed on the **app host** at `/opt/jobseek/bin/typst` for the pack service; it was absent from both observed hosts. Codex is installed on the **runner VM** at `/opt/jobseek/bin/codex`. Do not place binaries, tokens, `auth.json`, TLS keys or API environment files in Git, web assets or build caches.

## Prepare private configuration

Use the templates in this directory to create these files **outside the checkout** with owner-only permissions. Replace every placeholder; supply secrets through the operator's private secret channel, never as command arguments or in shell history:

| Host | File | Owner/mode | Content |
| --- | --- | --- | --- |
| App | `api.env` | root `0600` | Exact private origin, runner SSH path/user/key/known-hosts, chosen available model/effort, bridge token, Jev key, `JOBSEEK_CODEX_ISOLATION_VERIFIED=false` initially |
| App | `Caddyfile` | root `0644` | Private DNS/IP and TLS certificate paths; firewall accepts only owner private access and runner VM |
| App | `runner_key` and `known_hosts` | API user `0600`; root `0644` | Newly dedicated restricted SSH identity and **out-of-band verified** runner host key |
| Runner | `runner-config.toml` | root:jobseek-runner `0640` | ChatGPT-only sign-in and required `jobseek` HTTP MCP with the **same** bridge token |
| Runner | `authorized_keys` | root `0644` | Only the app host's private source IP, fixed launcher command, and OpenSSH `restrict` key options; root ownership prevents runner edits and the target UID can read it |

Generate the bridge token with a cryptographic secret generator and keep the same value in the two private files. The value is a transport credential, not a round capability. Never print either file in acceptance output. The private DNS name must resolve to the app's private IP **from the runner**, present a certificate trusted by that VM, and be reachable only through the selected private network. Give `jobseek-proxy` read access to `/etc/jobseek/tls/fullchain.pem` and `/etc/jobseek/tls/key.pem` without granting the API or runner access to the private key. The Caddy `/api/*` proxy preserves the MCP URL exactly. App `JOBSEEK_PUBLIC_ORIGIN` must equal that HTTPS origin. The app API listens only on `127.0.0.1:8080`; the dedicated `jobseek-caddy.service` serves same-origin web assets. The private app address, TLS key, DNS, firewall, runner host key, credential source and model/effort are explicit owner/operator inputs; no selected values are implied by the repository.

Before using the runner config, check the pinned binary's [official MCP configuration](https://learn.chatgpt.com/docs/extend/mcp) and [App Server authentication modes](https://learn.chatgpt.com/docs/app-server#authentication-modes). The runner's `config.toml` is bind-mounted read-only into its transient systemd unit. Its persistent `auth.json` stays inside the dedicated runner state and is neither copied from a desktop account nor exported with dashboard backups.

## Install and operate

Transfer the code artifacts and the separately pinned runtime binaries to their selected hosts using an authenticated private channel. As root, call each installer with **absolute paths** to already prepared private files. The installers copy files and reload the app unit; they do not start a service or perform login.

```sh
# App host
/path/to/ops/i12/install-app.sh /path/to/artifacts /private/config/api.env /private/config/Caddyfile /private/config/runner_key /private/config/known_hosts /private/approved-career

# Dedicated runner VM
/path/to/ops/i12/install-runner.sh /path/to/artifacts CODEX_LINUX_SHA256 /private/config/runner-config.toml /private/config/authorized_keys APP_PRIVATE_IP
```

The app installer takes a final private approved-career directory containing exactly the four source files used by the current pack loader: `cv-vince-liem.typ`, `cv-vince-liem.md`, `github-evidence-review.md`, and `portfolio-case-studies.md`. It copies only those regular files to root-owned `/var/lib/jobseek/assets`, readable by the API group and outside the web root. The pack loader verifies their pinned digests before use; a mismatch leaves preparation unavailable. The environment template sets `JOBSEEK_APPROVED_CAREER_ROOT` and `JOBSEEK_TYPST_PATH` explicitly. Do not put the source directory into the public build artifact.

The runner account has no ordinary writable home: only its separate 0700 `state` and `work` dirs are writable. `authorized_keys` forces the launcher and refuses forwarding/PTYS. The launcher checks `SSH_ORIGINAL_COMMAND`, strips SSH environment, uses the fixed App Server binary/hash/paths and starts a `systemd-run --user --pipe` transient service with a fixed unit name (one concurrent connection), `KillMode=control-group`, `ExitType=main`, read-only system/config mount and a 45-minute ceiling. The owner/operator must confirm **on the selected runner VM** that user-systemd's `PrivateUsers`/bind mount properties and cgroup teardown actually work; the host inventory did not prove this. `loginctl enable-linger` enables the runner's user manager. A failed launcher, namespace or cgroup test leaves the isolation flag false. Do not weaken properties to make a failed test pass.

Validate Caddy's rendered config and private TLS/firewall before serving, then start or stop explicitly. Keep `JOBSEEK_CODEX_ISOLATION_VERIFIED=false` at this stage; the API still serves the bearer-gated MCP route but refuses owner Codex control connections until acceptance:

```sh
caddy validate --config /etc/jobseek/Caddyfile
systemctl enable --now jobseek-api.service jobseek-caddy.service
systemctl stop jobseek-caddy.service jobseek-api.service
# On the runner VM, stop any active transient run and inspect its cgroup:
sudo -u jobseek-runner XDG_RUNTIME_DIR=/run/user/$(id -u jobseek-runner) systemctl --user stop jobseek-codex.service
```

The app's `setup-admin` subcommand is separate from Codex sign-in; it reads the new password from terminal/stdin and must run with the app's private data directory before owner browser login. A first-run command is `runuser -u jobseek-api -- env JOBSEEK_DATA_DIR=/var/lib/jobseek/data /opt/jobseek/bin/jobseek-api setup-admin` in a private terminal. No paid API-key login or imported desktop state is a substitute. Sign-in, status, page load and restart must not commission recruitment; only the owner's explicit round Start may do so.

## Acceptance on selected hosts

Run these repeatable checks in order. The pre-flag operator probe connects to the **same pinned App Server through the same restricted SSH launcher** directly from the app host. This is the bootstrap path while app Codex control remains intentionally disabled. It does not create a product round or grant the model a dashboard capability.

1. As `jobseek-runner` on the runner VM, run `accept-boundary.sh`. It launches a real bubblewrap user/mount namespace, proves allowed context read and workspace write, and denies a host decoy/symlink, inherited key and metadata address. A launch failure is a failure, never a successful denial. Also inspect the runner filesystem/mounts and firewall for absence of app DB, backups, admin/Jev/API secrets, user homes and host sockets. This synthetic check does **not** establish Codex's own tool containment.
2. From the app host, run `accept-runner-transport.py jobseek-runner@RUNNER_PRIVATE_DNS /etc/jobseek/ssh/runner_key /etc/jobseek/ssh/known_hosts /usr/local/libexec/jobseek-runner-launch`. It checks that a different SSH command is denied and that the pinned App Server answers `initialize`. On the runner VM, run `accept-runner-cgroup.sh check` after normal close. Repeat with the probe's optional `60`-second hold while deliberately killing the SSH client, then run `accept-runner-cgroup.sh check`. For the supervisor-death case, start another held probe, run `accept-runner-cgroup.sh kill-main` while the unit is active, and confirm it stops with no descendant processes. Repeat after restart. A response alone is protocol reachability, not sign-in or isolation.
3. As `jobseek-runner` on the VM, run `/usr/local/libexec/jobseek-accept-canary prepare`, then `/usr/local/libexec/jobseek-accept-native-profile`. The latter uses the pinned Codex binary inside a transient unit with the launcher's namespace and config bind to test the named profile directly. It must read the work context while denying private sentinel/config/decoy reads, work writes/deletion, and native sockets. A command launch failure is a failed gate, not evidence of denial. From the app host in a **private operator terminal**, run `accept-live-runner.py login jobseek-runner@RUNNER_PRIVATE_DNS /etc/jobseek/ssh/runner_key /etc/jobseek/ssh/known_hosts /usr/local/libexec/jobseek-runner-launch`. The owner completes only the official device URL/code printed there; the script re-reads `account/read` for `type=chatgpt`. Then run `accept-live-runner.py native-profile jobseek-runner@RUNNER_PRIVATE_DNS /etc/jobseek/ssh/runner_key /etc/jobseek/ssh/known_hosts /usr/local/libexec/jobseek-runner-launch OWNER_SELECTED_AVAILABLE_MODEL OWNER_SELECTED_AVAILABLE_EFFORT`. This synthetic turn checks the actual config, active profile, account, model/effort, limits, required MCP tools and exact saved turn IDs; it asks for separate native `apply_patch` calls on the synthetic work file and synthetic state sentinel. The script prints the resulting thread and turn IDs. On the VM, run `/usr/local/libexec/jobseek-accept-native-rollout THREAD_ID TURN_ID` with those exact IDs, then `/usr/local/libexec/jobseek-accept-canary check`; run `clean` after recording results. The trusted rollout checker must find both attempted calls and matching denial outputs in the exact saved turn without printing session content. Missing/changed output, a changed sentinel, or a failed direct profile probe leaves containment **inconclusive** and the isolation flag false. Native `apply_patch` output is not an App Server `fileChange` event on this pin, so a completed turn, model prose, unchanged canary or direct command probe alone cannot satisfy this gate. The live turn consumes the owner's Codex allowance and must be scheduled with the owner; **do not run it during another round**.
4. On the app host, run `accept-idle.sh /var/lib/jobseek/data/jobseek.sqlite https://PRIVATE_APP_FQDN /path/to/private-ca.pem`. It restarts the API, verifies same-origin reachability, confirms unauthenticated MCP denial and checks that idle start/restart created no round/job/attempt rows. Inspect logs/round audit for unpersisted work separately.

Only after steps 1–4 pass may the operator set `JOBSEEK_CODEX_ISOLATION_VERIFIED=true` in the private app env and restart the API. The owner opens the private origin, signs into the **separate dashboard account**, and checks Codex status `ready` with the already authenticated dedicated runner. If the owner later disconnects, **Connect Codex** starts a new official device-code flow in the owner-only UI. Repeat the idle check. With an **owner-approved bounded product test round**, make one harmless authenticated `round_context` MCP call and verify exact allowance charging, wrong-scope/revoked capability denial, Stop fencing, interruption, history reconciliation and no new work on browser reload or restart. A failure requires reverting the isolation assertion to false and preserving uncertain dispatch evidence. A real accepted I12 deployment requires selected host identity, private TLS/MCP reachability, supported owner sign-in, actual required-tool call, Codex tool containment and supervisor-death cleanup. None of those live gates is claimed by this package. Startup and commissioned work-loop binding are implemented. Round Start remains unavailable until the selected-host runtime, account and required tools actually pass readiness; the offline package and browser fixtures do not establish that readiness.

References: [OpenSSH authorized-key loading under the target UID](https://github.com/openssh/openssh-portable/blob/master/auth2-pubkey.c), [systemd-run transient service and `--pipe`](https://man7.org/linux/man-pages/man1/systemd-run.1.html), [systemd user-service namespace and `BindReadOnlyPaths` constraints](https://man7.org/linux/man-pages/man5/systemd.exec.5.html), [Caddy's mutually exclusive `handle` routes](https://caddyserver.com/docs/caddyfile/directives/handle), [Caddy TLS certificate configuration](https://caddyserver.com/docs/caddyfile/directives/tls), [OpenAI App Server](https://learn.chatgpt.com/docs/app-server), [OpenAI MCP setup](https://learn.chatgpt.com/docs/extend/mcp).
