# UI fixture smoke

Run `pnpm e2e` from the dashboard root. The command builds the web app once, starts a synthetic HTTP API on an ephemeral localhost port, drives the built bundle in headless Chromium, and closes both browser and server on exit. Chrome or a Playwright Chromium installation must be available; `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` can select a local browser binary.

This smoke checks browser interactions and DTO wiring: Prepare and Stop/Resume, immutable pack selection, failed detail read retry, contextual input for the current campaign/profile and the exact selected pack version, paused round replacement, and completed round reporting. Production-shaped input reports show a saved profile/vacancy change, an unsupported source URL, and a saved vacancy whose semantic organisation remains unresolved; the pack-correction report shows the saved version and material unknown. It simulates lost responses after the server accepts a profile correction or paused preparation replacement, then reloads and replays the identical saved requests. It also injects stale paused-revision 409 responses for preparation, verifies explicit review and a new request, and checks a 390px layout. It does not call a live API, Codex runner, provider, account, or employer, and it does not establish full I11/I14 acceptance.

## Research smoke (T27 product acceptance)

Run `pnpm e2e:research` from the dashboard root. It builds the web app once, then `research-smoke.mjs` builds the test-only `researchsmoke` Go helper, starts it on an ephemeral localhost port (real T23 wired backend: disposable store, controlled fixture sources, scripted zero-spend Jev), drives the built bundle in silent headless Chromium, and closes everything on exit. It verifies Start/Steer/Stop/Resume, acknowledged corrections, reconnect, true counts/costs, evidence, zero-result reporting and retained workflow handoff. Agent tool calls run through the real backends; no live model drives turns here. Non-research rounds commission against the real store/HTTP with a disclosed no-op execution worker (intake durability, not runner completion).

## Silent browsers

Both suites launch through `browser.mjs`: a fresh temporary profile with `--password-store=basic --use-mock-keychain`, preferring the isolated headless shell (`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` overrides). System Chrome is never used unless `JOBSEEK_ALLOW_SYSTEM_CHROME=1`. The research smoke snapshots `~/Library/Keychains` before launch and fails if any entry is added or touched.
