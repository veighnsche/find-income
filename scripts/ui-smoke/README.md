# UI fixture smoke

Run `pnpm e2e` from the dashboard root. The command builds the web app once, starts a synthetic HTTP API on an ephemeral localhost port, drives the built bundle in headless Chromium, and closes both browser and server on exit. Chrome or a Playwright Chromium installation must be available; `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` can select a local browser binary.

This smoke checks browser interactions and DTO wiring only. It does not call a live API, Codex runner, provider, account, or employer, and it does not establish full I14 discovery or pack acceptance.
