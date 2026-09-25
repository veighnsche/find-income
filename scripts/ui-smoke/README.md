# UI smoke suites (Vite frontend gates)

Run from the dashboard root. Each command builds the Vite app once, starts a
synthetic HTTP API (or a self-contained in-file double) on an ephemeral
localhost port, drives the built bundle in silent headless Chromium, and
closes everything on exit. Chrome or a Playwright Chromium installation must
be available; `PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` can select a local
browser binary.

- `bun run e2e:slice0` — session contract: restore, login accept/reject,
  logout, expired-session 401.
- `bun run e2e:slice1` — read-only shell: sign-in, Today / My search / Jobs /
  Applications, deep links, reload, back, keyboard, unavailable states, no
  non-auth POST.
- `bun run e2e:slice2` — discovery/selection: run start/stop/resume/revisit,
  find-more new-source discovery, steer, four-group + Unknown coverage,
  verbatim saved reasons, zero-request explanations, guarded persistent
  selection, zero check traffic.
- `bun run e2e:slice3` — checked details/answers: select → explicit check →
  actual questions → saved answers; five check states, unselected/blocked
  honesty, Jev match prefill, no-match, exact edits/blanks, zero Codex/LLM
  answer calls.
- `bun run e2e:slice4` — preparation: prepare flow, optional blanks, held
  missing-required, exact edit + 409 conflict + reload, explicit rewrite,
  provenance/readiness across versions, approve-then-stale refused, zero
  automatic employer contact.
- `bun run e2e:slice5` — test-service delivery: exact version/recipient
  binding, revoked/stale refused, single-POST repeated clicks, lost-response
  recovery, partial/failure/uncertain honesty, read-only reconcile, snapshot
  agreement, zero external requests.
- `bun run e2e:slice5b` — connected review→send→outcome→history journeys for
  uncertain and failed attempts, single send each.

Slices 0–1 share the synthetic API in `fixture.mjs`; slices 2–5b carry
self-contained doubles importing only `browser.mjs`. All doubles use
`.invalid` test domains; no suite contacts a live API, model runner,
provider, account, or employer.

## Silent browsers

All suites launch through `browser.mjs`: a fresh temporary profile with
`--password-store=basic --use-mock-keychain`, preferring the isolated
headless shell (`PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH` overrides). System
Chrome is never used unless `JOBSEEK_ALLOW_SYSTEM_CHROME=1`.
