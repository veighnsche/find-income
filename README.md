# Jobseek dashboard

Private job-seeking dashboard under construction. This repository currently contains the M0 foundation only: a Go health API, a Vite+ React shell, a shared OpenAPI contract and the mixed-language Turbo task graph. No opportunities, account data or application assets are stored yet.

## Run locally

Use Node 24.21.0, pnpm 12.3.4 and Go 1.27.1. Install and start both services:

```sh
pnpm install --frozen-lockfile
pnpm dev
```

Open <http://127.0.0.1:5173/>. Vite+ proxies `/api` to the Go service on `127.0.0.1:8080`. Stop with Ctrl+C. Both processes bind to loopback during this foundation stage.

```sh
pnpm check       # generated-code drift, Vite+ checks, TypeScript, Go vet
pnpm test        # Go health test; web test runner has no cases yet
pnpm build       # web dist and apps/api/bin/jobseek
pnpm generate    # regenerate TS and Go transport types from OpenAPI
pnpm format      # source-mutating, uncached
pnpm e2e         # fails explicitly until T24 adds browser journeys
```

See [foundation task graph](docs/foundation.md) for generator ownership, native Go filters, cache evidence and current boundaries.
