# M0 foundation

## Pins and ownership

| Tool | Exact pin | Source |
| --- | --- | --- |
| Node | 24.21.0 | `.node-version` and root `engines` |
| pnpm | 12.3.4 | root `packageManager` and lockfile |
| Go | 1.27.1 | `go.work`, `apps/api/go.mod` and CI |
| Turborepo | 2.11.3 | root package and lockfile |
| Vite+ | 1.0.0-rc.0 | root package and lockfile |
| oapi-codegen | v2.8.0 | Go generator scripts |
| openapi-typescript | 7.13.0 | contracts package and lockfile |

The selected Go module path is `github.com/veighnsche/find-income-dashboard/api`, which Turbo discovers as `api`; no `apps/api/package.json` exists. This is a local module identity. No GitHub remote or deployment has been created by M0.

`packages/contracts/openapi.yaml` owns the OpenAPI 3.0.3 transport contract. `packages/contracts/src/generated/api.ts` is produced by openapi-typescript. `apps/api/internal/httpapi/generated/api.gen.go` is produced by oapi-codegen. Regenerate with `pnpm generate`, and run `pnpm check` before build: its first stage compares both regenerated outputs in temporary files and fails on drift. The second stage runs Vite+ checks, `tsc --noEmit` and native Go vet. The Go service currently uses generated transport types for its health response.

## Task graph

`go.work` lists one native module; `turbo ls` includes `api`, `@jobseek/contracts`, `@jobseek/web` and Turbo's synthetic `go-workspace`. The root `turbo.json` enables `experimentalGoWorkspaces` and `experimentalTaskCommand`. The API package overrides `build` to produce `apps/api/bin/jobseek`, and declares that file as a restorable output. Go's own build/module caches are outside Turbo outputs. `api#generate` depends on contract validation and has only the generator configuration/script as its package inputs. Its contract input is included through the dependency hash. Native Go build/test/lint run after generation; web build/check run after contracts generation. `dev`, `format`, generated-code drift checks and future e2e journeys are uncached.

Useful filters:

```sh
pnpm exec turbo run api#test
pnpm exec turbo run lint --filter=api
pnpm exec turbo run build --filter=@jobseek/web
```

Runtime SQLite files, attachments, personal PDFs, backups, sessions and secrets must live in an external private data directory when those features are implemented. They are not build inputs or cache outputs. M0 does not create a data directory or store private records.

## Verification record

The foundation lane verified locally on 23 September 2026:

- A clean temporary Git clone passed `pnpm install --frozen-lockfile`, `pnpm check`, `pnpm test` and `pnpm build` with the exact host versions above. `pnpm format` passed in the working checkout.
- `turbo ls` discovers the native Go module without a JS wrapper. `turbo run api#test`, `turbo run lint --filter=api` and `turbo run build --filter=@jobseek/web` succeed.
- `pnpm dev` starts both services; `GET http://127.0.0.1:5173/api/v1/health` returns the actual Go JSON response. In a browser the shell displayed the connected status. With the API stopped, the same shell displayed an HTTP 502 error and retry control. Ctrl+C closed both listening ports.
- A repeated root build returned 5/5 cache hits. After deleting `apps/web/dist` and `apps/api/bin/jobseek`, another cached build restored both outputs. Temporary Go source, `go.mod`, OpenAPI and web source edits caused the expected misses; `GOFLAGS=-trimpath` also changed native Go task hashes. The files were restored afterward.
- Deliberate mismatches in each generated file made `pnpm check` fail. Adding a temporary `contractProbe` property to the OpenAPI health schema made `pnpm generate` update both Go and TypeScript types. The probe and outputs were then restored.

CI is defined in `.github/workflows/foundation.yml` and runs frozen install, check, test and build. The workflow has not run on GitHub yet. Vite+ test currently reports no web test files; M0 has one Go health route test. Browser journeys and domain feature tests belong to later tasks.

### References

- [Turbo Go workspace guide at v2.11.3](https://github.com/vercel/turborepo/blob/v2.11.3/apps/docs/content/docs/guides/tools/go.mdx)
- [Vite+ monorepo guide](https://viteplus.dev/guide/monorepo)
- [oapi-codegen releases](https://github.com/oapi-codegen/oapi-codegen/releases)
