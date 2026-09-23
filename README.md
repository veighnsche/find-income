# Jobseek recruitment agency

An undeployed personal recruitment agency prototype using Go/SQLite, Vite+ and React, native Go workspaces in Turborepo, Typst and Codex App Server. Codex gathers evidence and operates records; Jev classifies supplied evidence and choices. The owner commissions bounded work through Start/Stop/Resume.

See the [full implementation task list and concurrent execution guide](docs/implementation-tasks.md) for current acceptance, remaining work, model/effort assignments and live-task handoffs. The [task graph](docs/task-graph.json) records dependencies and status. Five tasks and several independent slices are reviewed; the complete agency journey is not implemented.

## Current behavior

The web app has Agency and Account views, a read-only campaign brief, opportunity inspection, saved source intake, round controls and automatic progress reads. Manual preference/board/category forms have been removed. Contextual correction drafts and selection are currently browser-local; server-backed correction/history/selection remain unfinished.

The API persists bounded rounds, charged attempts, scoped company/opportunity mutations, exact remote dispatch evidence and incremental collector batches. The runtime has fake-tested lifecycle and explicit turn execution, and Jev has factual/candidate-choice helpers. These components are not yet wired into a real discovery journey: production Start/Resume report unavailable until the commissioned executor is bound. Login, startup and progress reads launch no recruitment work.

Private host/account setup, bounded non-seed research, durable Jev orchestration, application packs and delivery remain open. No live runtime, real discovery quality or deployment readiness follows from the fixture checks. The unsupported direct recruitment write routes return unavailable.

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

Configure the local administrator interactively; the terminal hides both password entries. Protected programmatic stdin is also supported. Never put a literal password in command arguments or shell history:

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

## Verification

Run these from this repository:

```sh
pnpm check
pnpm test
pnpm build
```

`pnpm e2e` currently fails intentionally as a placeholder; task I14 replaces it with actual journey coverage. Browser fixture checks are recorded in the task handoffs and do not establish live provider behavior. [Foundation notes](docs/foundation.md) describe the workspace/toolchain.

## Development handoffs

One writer owns each file during concurrent work. The coordinator commits coherent reviewed checkpoints after their relevant checks, stages explicit paths, and verifies the remaining status before dispatching more work. Shared schema, contracts, startup and web integration have named owners. The unused prototype changes directly; there are no backward-compatibility or upgrade paths.
