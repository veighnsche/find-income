# Jobseek recruitment agency

An undeployed personal recruitment agency prototype using Go/SQLite, Vite+ and React, Turborepo, Typst and Codex App Server.

## Current behavior

The fixed-board discovery pipeline has been removed at the owner's instruction. There is no job-search implementation, discovery commission, board adapter, collector, candidate-staging funnel or discovery UI. The [owner report](docs/discovery-rapport-2026-09-24.md) records why it was rejected.

The remaining app handles supplied sources and saved records: account and campaign information, companies/opportunities, contextual corrections, application packs, relationships, interview preparation, replies and offer comparisons. Codex operates records; Jev classifies supplied evidence. These capabilities retain their own execution and evidence controls. Startup and progress reads launch no recruitment work.

The autonomous-recruitment replacement is implemented through T28 and packaged as candidate `rc1-20260924-df808dd-a71533b0` ([manifest](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/manifest.md), [verification note](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/note.md)). Integrated fixture evidence covers the composed backend (T23), runtime failure/concurrency (T24), current-format restore (T25), identity/evidence outcomes (T26), and browser acceptance (T27); all gates re-verified green on the candidate revision (T28 logs). No live recruitment run has happened: host verification (T29), the live canary (T30), and the live audit (T31) are DEFERRED by owner decision on 2026-09-24 pending host selection. The [ordered task checklist](/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-tasks.md) records per-task evidence and the deferral notes; see [remaining work](docs/remaining-work.md) for the current boundary. The retired implementation schedule is no longer a plan for this app.

## Local setup

Use the pinned toolchain: Node `24.21.0`, bun `1.4.2`, and Go `1.27.1`. From the repository root, install from the lockfile and build the web app and Go service:

```sh
bun install --frozen-lockfile
bun run build
```

The initial schema changes directly with this prototype. Databases created from the removed pipeline are not migrated; use a fresh data directory for this checkout. Existing private data is not reset automatically.

The server stores SQLite data under `JOBSEEK_DATA_DIR`. Choose one private directory and use it for both administrator setup and every `bun run dev` session. For a local macOS account, for example:

```sh
export JOBSEEK_DATA_DIR="$HOME/Library/Application Support/jobseek-dashboard/data"
mkdir -p "$JOBSEEK_DATA_DIR"
chmod 700 "$JOBSEEK_DATA_DIR"
```

The API also enforces private directory and database file permissions. Keep this path out of source control, build caches, shared folders, and cloud-synced directories.

Configure the local administrator interactively; the terminal hides both password entries. Protected programmatic stdin is also supported. Never put a literal password in command arguments or shell history:

```sh
./apps/api/bin/jobseek setup-admin
```

Start the dashboard in the same terminal so it uses the same data directory:

```sh
bun run dev
```

Open <http://127.0.0.1:5173/> and sign in with the password just configured. The Go API listens on `127.0.0.1:8080`; Vite proxies `/api` to it. Both development servers bind to loopback. Check the local connection with:

```sh
curl --fail http://127.0.0.1:5173/api/v1/health
```

Stop both services with Ctrl+C. To use a different terminal later, repeat the `JOBSEEK_DATA_DIR` export there before running setup or `bun run dev`. The default data path is the OS user config directory, but setting an explicit private path avoids accidentally using different databases between commands.

## Verification

Run these from this repository:

```sh
bun run check
bun run test
bun run build
```

`bun run e2e` runs browser fixture checks; they do not establish live provider behavior. [Foundation notes](docs/foundation.md) describe the workspace/toolchain.

## Development handoffs

One writer owns each file during concurrent work. The coordinator commits coherent reviewed checkpoints after their relevant checks, stages explicit paths, and verifies the remaining status before dispatching more work. Shared schema, contracts, startup and web integration have named owners. The unused prototype changes directly; there are no backward-compatibility or upgrade paths.
