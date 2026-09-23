# Jobseek dashboard

Private job-seeking dashboard under construction. The current slice provides local administrator sign-in, Today preferences, and owner-managed agent credentials. T07 authentication was accepted by the coordinator at commit `5dfc5e3` after clean-clone install/check/test/build checks and closure of both independent security findings. This remains local development guidance, not a full release or deployment guide. The foundation graph and cache evidence remain in [docs/foundation.md](docs/foundation.md).

## Local setup

Use the pinned toolchain: Node `24.21.0`, pnpm `12.3.4`, and Go `1.27.1`. From the repository root, install from the lockfile and build the web app and Go service:

```sh
pnpm install --frozen-lockfile
pnpm build
```

The server stores SQLite data under `JOBSEEK_DATA_DIR`. Choose one private directory and use it for both administrator setup and every `pnpm dev` session. For a local macOS account, for example:

```sh
export JOBSEEK_DATA_DIR="$HOME/Library/Application Support/jobseek-dashboard/data"
mkdir -p "$JOBSEEK_DATA_DIR"
chmod 700 "$JOBSEEK_DATA_DIR"
```

The API also enforces private directory and database file permissions. Keep this path out of source control, build caches, shared folders, and cloud-synced directories.

Configure the local administrator interactively. The terminal hides both password entries; the password is read from stdin and is never a command-line argument. Do not pipe, echo, or put a password into an example command:

```sh
./apps/api/bin/jobseek setup-admin
```

Start the dashboard in the same terminal so it uses the same data directory:

```sh
pnpm dev
```

Open <http://127.0.0.1:5173/> and sign in with the password just configured. The Go API listens on `127.0.0.1:8080`; Vite proxies `/api` to it. Both development servers bind to loopback. Check the local connection with:

```sh
curl --fail http://127.0.0.1:5173/api/v1/health
```

Stop both services with Ctrl+C. To use a different terminal later, repeat the `JOBSEEK_DATA_DIR` export there before running setup or `pnpm dev`. The default data path is the OS user config directory, but setting an explicit private path avoids accidentally using different databases between commands.

## Sign-in and agent credentials

The initial setup creates the single local administrator. Sign-in uses a private session cookie; sign out from the dashboard to end the session. In **Settings → Agent access**, create a distinct named token for each agent, select only the scopes it needs, and choose a 7-, 30-, or 90-day expiry. The raw token appears once: copy it directly into the agent's approved secret store. It cannot be shown again. Use **Revoke** in Settings to invalidate it immediately; create a replacement deliberately if needed. Agent credentials cannot administer the dashboard.

The token controls are implemented, but the corresponding opening, evidence, action, draft, and assessment APIs are not yet available. Creating a token does not mean an agent integration is ready. There is no outbound messaging capability.

## Current scope and checks

The current browser workflow is limited to sign-in, Today preferences, service status, and agent credential controls. Opportunity CRUD is being built and is not yet available in the dashboard; People workflows, Jev assessments, embedded Codex, and Typst/PDF generation are not integrated. Do not enter real opportunity data or personal application assets until those workflows and their acceptance review are complete.

Useful repository checks after changes:

```sh
pnpm check
pnpm test
pnpm build
```

These commands passed in the coordinator's clean clone at commit `5dfc5e3`; this documentation update did not rerun them. `pnpm e2e` still fails explicitly until T24 adds browser journeys. The web test task has no committed test cases yet. This README does not mark the full dashboard release or T25 complete.
