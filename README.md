# Jobseek recruitment agency

An undeployed personal recruitment agency prototype using Go/SQLite, Vite+ and React, Turborepo, Typst and Codex App Server.

## Current behavior

The fixed-board discovery pipeline has been removed at the owner's instruction. There is no job-search implementation, discovery commission, board adapter, collector, candidate-staging funnel or discovery UI. The [owner report](docs/discovery-rapport-2026-09-24.md) records why it was rejected.

The app follows the seven-step manual-handoff scope in [product-vision.md](docs/product-vision.md): deterministic goals forms, free Contributor discovery with Jev classification, persistent selection, deep selected-job checks, Jev-suggested editable answers, Standard-drafted materials, and a saved Handoff page with manual apply instructions. It never emails, submits, attaches, autofills, or otherwise contacts an employer. Application approval/sending, delivery, employer-site autofill, and downstream replies/interviews/offers were removed (see [p0-retain-cut-ledger.md](docs/p0-retain-cut-ledger.md)); older plans under `docs/` are kept for history only. Startup and progress reads launch no recruitment work.

No live recruitment run has happened. Earlier T-series packaging and host-verification notes are retired with the plans above; live Contributor/Standard behavior is verified separately from fixture behavior per [connected-prototype-ux-acceptance.md](docs/connected-prototype-ux-acceptance.md).

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

`bun run e2e:slice0` through `bun run e2e:slice4` plus `e2e:rw-checks` and `e2e:rw-discovery` run the browser fixture gates; they do not establish live provider behavior. [Foundation notes](docs/foundation.md) describe the workspace/toolchain.

## Development handoffs

One writer owns each file during concurrent work. The coordinator commits coherent reviewed checkpoints after their relevant checks, stages explicit paths, and verifies the remaining status before dispatching more work. Shared schema, contracts, startup and web integration have named owners. The unused prototype changes directly; there are no backward-compatibility or upgrade paths.
