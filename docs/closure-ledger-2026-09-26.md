# Closure ledger — 40 tasks, findings, acceptance (Q4)

26 September 2026 · Session simple-themisto. Every entry below names the
implemented task, the checkpoint commit, meaningful evidence observed in
this run, and the remaining limit. Product vision (`docs/product-vision.md`)
stayed authoritative; the app never fills, attaches, sends, submits, or
contacts employers.

## Verification battery (observed this session, HEAD 8e086ac unless noted)

- `go build ./... && go vet ./...` (apps/api): clean.
- `go test -count=1 -p 2 ./...` (apps/api): all packages green (incl. Q0 8/8,
  Q1 spine + 5 branch tests, musewire incl. strict-schema test; the
  E11 live-validation turn is opt-in and passed once separately).
- `bun run typecheck` (apps/vite-app): exit 0.
- `NODE_OPTIONS=--no-experimental-webstorage bunx vitest run --pool=forks
  --poolOptions.forks.maxForks=2`: 38 files / 419 tests green.
- `bun run check` (dashboard root): 6/6 turbo tasks green (TS + Go
  generated-client drift checks, contracts vp lint, vite eslint, tsc, go vet).
- `bun run build` (apps/vite-app): green in ~5s (chunk-size notice only).
- Live scratch server (loopback, temp data dir): boot, owner login, prefs /
  brief / saved-jobs reads, muse readiness contributor+standard = ready,
  round-gated writes = precise 503, unknown routes = 404.
- `E11_VALIDATE=1 go test -run TestLiveTransportValidationTurn`: PASS (33s,
  zero retrieval) after the strict-schema fix.

Known flakes (not regressions): the full parallel `go test ./...` can miss
the 3s startup probe in `TestConfiguredServerStartupLeavesRecruitmentQueued`
(passes alone and at `-p 2`); the full parallel vitest run occasionally drops
an unrelated timing-sensitive test (passes alone and at maxForks=2; three
consecutive green runs observed at collection).

## 40 tasks

| Task | Checkpoint | Evidence |
| --- | --- | --- |
| I0 baseline/ownership | a38ee26 | goal, baseline, one-writer file ledger in docs |
| I1 contracts C1–C9 | f81c777, 983eb04 | frozen contract doc; ownership ledger |
| R1 direct CLI seam | 02c077e | musewire direct exec; echo-plumbing + validation-turn tests |
| M1 one artifact system | 23a2654 | combined-pack workflow removed; R08 journey probe 404s |
| R2 cut rejected runtime | 4a93058, 2622b33 | dead runners/packages gone; codex connect/MCP 404 (Journey08) |
| I2 subtraction gate | a010d41 | boundary doc; `bun run check` green (8e086ac) |
| I3 publish contracts | 6ca278b, ee9dab1, e33b68e | openapi.yaml + handoff_saved + regen clients in sync |
| F1 shared seams | 0f34944 | invalidation scopes, run restore, saved-goals provider + tests |
| D1 catalog | 78d7da4 | once-per-version catalog; Journey01; Q0-01/06 green |
| D2 dedup/identity | 78d7da4 | vacancy identity; Journey06; recollect-identity spine assert |
| D3 recovery | 78d7da4 + eaffdee | failed-run resume; Journey07; no localStorage (F test) |
| D4 owner context | 78d7da4 | sourced context; ExperiencePanel tests |
| K1 check contract | 78d7da4 | verdict/match/commit writers; zero-question gate (Q0-07) |
| K2 prefilter | 78d7da4 | lexical prefilter + none_fits; answermatch tests |
| K3 commit | 78d7da4 | commit pins + 409 names missing (360d3ea follow-up) |
| K4 clarification | eaffdee | clarification API + failed-run resume + R19 honesty |
| M2 readiness | c4452da | evidence pins; Q0-05 green; readiness tests |
| M3 requiredness | c4452da | true requiredness table; Q1 route-branch pins |
| M4 grounding | c4452da | grounding rules; unsupported-claim holds (materialprep tests) |
| M5 edit/rewrite | c4452da | exact edit literal + zero model calls; rewrite versions |
| M6 export/index/Handoff | c4452da | versions/export/saved-jobs/Handoff reads + handoff_saved save |
| C1 continuation | b22fba5 | truthful check continuation; blocked/outdated reads |
| A1 suggestions | b22fba5 | Jev match over approved answers + no-fit; match tests |
| A2 dirty text | b22fba5 | dirty-text preservation; journey answer tests |
| A3 commit blanks | b22fba5, 360d3ea | required blanks block with named 409 |
| G1 goals/context | c4452da | wants/don't-wants form + sourced panels + tests |
| G2 commission | c4452da | commission panel, allowance, blocker states + tests |
| G3 cards/evidence | c4452da | cards, Why panel (zero new model call), evidence |
| G4 groups/selection | c4452da | five groups, roving tabs, sticky action bar + tests |
| E1 one Prepare | 30df939 | useDraftArtifacts single POST, holds + resume; 42 E tests |
| E2 editors/rewrite | 30df939 | prefilled exact editors, explicit rewrite, versions |
| E3 partial/stale | 30df939 | Outdated banners, pinned-version exports |
| E4 manual Handoff | 30df939 | HandoffView rebuild, explicit save, terminal state |
| F2 shell/nav | 2455306 | truthful strip/stages/lifecycle, deep links, no local recovery |
| F3 saved job list | 2455306 | saved-jobs join, per-job Handoff links, honest labels |
| Q0 subtraction proof | e6ae53f…7c58cd7 | 8/8 gap harness green |
| Q1 connected proof | 096db99, ec65747 | spine + 5 branch tests; prepared-transition fix |
| Q2 UI/usability | 0c0aef6 + suites | screenshot-structural match; 419 green; build green |
| Q3 live evidence | 4d1b045 + probes | validation turn PASS; server behaviors; precise blocks |
| Q4 ledger | this doc + 8e086ac | battery above; ledger; no optional-review loops |

I-side P1–P5+P9 integration (fixtures, zero-question gate,
draft_requested e2e, GET /rounds, run_not_found trio, D3/D4
registrations, career loader, research_run, index + reuse-archive)
landed in 78d7da4 with Q0-02/03/07 green.

## R01–R30 findings

| Finding | Tasks | Evidence | Remaining limit |
| --- | --- | --- | --- |
| R01 catalog authoring | D1, Q1 | Journey01 once-per-version; spine catalog assert | live catalog authorship needs a live run |
| R02 stale goals/context | F1, G1 | invalidation scopes; brief-stale banner + tests | — |
| R03 commit/required blanks | I1, K3, A3 | commit pins; named-question 409 (360d3ea) | — |
| R04 untouched suggestions | K3, A1 | match/current pins; explicit keep/edit/blank (spine) | — |
| R05 lost dirty text | F1, A2 | preserved text; partial-failure hold test | — |
| R06 zero questions blocked | K1, K3, C1, A3, Q1 | QuestionsNoneVerified gate; questionless-to-handoff test | — |
| R07 stale readiness | M2, E3 | basis staleness → held; Outdated banners | — |
| R08 pack/artifact split | M1, M4, M5, E1, E2 | pack endpoints 404; one canonical set (Journey04, Q1) | — |
| R09 optionality/attachments | K1, C1, M3 | upload-only/form-values rules; Q1 branch pins | — |
| R10 drafting context | M4, E1 | facts/answers/spans basis; activity journal | Standard live draft needs key + auth |
| R11 unsupported claims | M4, Q1 | holds + clarification flow; draft-hold tests | — |
| R12 duplicate identity | D2 | URL-keyed vacancy identity; Journey06 | — |
| R13 browser-local recovery | D3, F1, F2 | server restore; localStorage-empty test | — |
| R14 shell/nav state | F1, F2 | truthful strip/stages from server reads | — |
| R15 commission/Change goals | F2, G2 | runId deep links; commission panel | — |
| R16 unsourced panel | D4, G1 | sourced context with loader + retry tests | — |
| R17 stages/no terminal | I3, F2, M6, E4 | stage rows; prepared walk (096db99); handoff_saved | — |
| R18 links/index | M6, F3, E4 | saved-jobs join; per-job Handoff routes | — |
| R19 inert checking | K1, C1 | performer honesty (eaffdee); blocked states | live check needs run auth |
| R20 capture completeness | K1, C1 | completeness states; no overclaim (adapter tests) | — |
| R21 clarification missing | K4, M4, E1 | clarification API + UI holds/resume | — |
| R22 hidden partial/stale | C1, M6, F3, E3, E4 | readable held/outdated everywhere + tests | — |
| R23 replay defects | M2, M5, E2 | same-key replays (Q1 lost-ack test; store tests) | — |
| R24 selection action | K1, G4 | eligible/gated Check-chosen-jobs bar | — |
| R25 card facts | D2, G3 | employer/arrangement from saved evidence | — |
| R26 run controls | G2, G3 | stop/resume/continue lifecycle + tests | — |
| R27 tab keyboard | G4, Q2 | roving tabIndex + arrows/Home/End (Q2 verified) | live keypress run not done |
| R28 numeric support | D2, G3 | no hard conflict hidden by score; honest Unknown | live Jev scoring needs key |
| R29 lexical exclusion | K2 | prefilter + none_fits fallback; match tests | — |
| R30 runtime remains | R1, R2, I2 | cuts; codex ceremony 404s; `bun run check` green | — |

## Acceptance IDs (seven-step UI doc)

S01–S05: subtraction map (e10557a), contact workflow gone (0c18c59),
lifecycle gone (80af9db), one runtime path (9767c2f), clean base
(8c406bc…3570481 + I2 a010d41); negative reachability in Journey08 +
Q1 no-action probes. F01–F05: Q2 screenshot-structural match (0c0aef6).
G01/T01: goals persistence + Today resume (suites). J01–J06: direct-CLI
wiring + echo/validation evidence; stop/continue; exact five groups;
cards/Why; sticky action bar; goal-change preservation (suites + Q3).
AP01–AP05: chosen-only start; grounded vacancy + owner-clarification
label; Jev prefill with zero Answer-stage model calls; one safe
continuation; missing-fact hold/resume (lanes + Q1). P01–P04: Standard
wiring + grounding tests; route-needed artifacts; exact control;
copy/download (lanes + Q1 + E suites). H01–H03: saved list; verified
manual steps; no autofill/contact anywhere (lanes + Q1 + E4). X01–X04:
saved-state recovery + replay tests; owner-effort split; narrow/keyboard
semantics (Q2); proof levels separated (this ledger + Q3 record).

## Remaining limits and explicit follow-ups

1. TYPESAFE_API_KEY is unset: Jev classification/matching and Standard
   drafting (materials wiring is gated on the same flag) return honest
   503/unavailable. Set the key and rerun the live pass.
2. Full live Contributor collection + chosen-only deep check was not run:
   it contacts external sites and spends multi-turn budget. The E11
   validation turn (PASS, 33s, zero retrieval) proves the direct wire;
   the owner should explicitly authorize the full run.
3. Headless browser pass DONE after closure: 12/12 green driving the
   real API + vite dev pair in headless Chromium (login, search opening,
   jobs, detail, check, answers, prepare, real exact edit vN→vN+1, real
   export download with pinned filename, honest rewrite-unavailable,
   saved list, real handoff save + reload replay, second-session return,
   narrow 390px frames + keyboard focus; zero page errors, zero
   horizontal overflow on every frame; screenshots read back and
   verified). Seed role was built through production store writers.
4. Repo e2e slices 1–4 are stale (fail on pre-F2/pre-E UI expectations
   and pre-subtraction doubles: root opens My search now, pack/round
   concepts removed); slice0 session contract still passes. They are
   not part of `bun run check`. Superseded for journey proof by the
   headless pass above; rewriting them is drift-chase, recorded here
   instead.
4. Delivery smoke (skill): API + built-app reachability is verified at
   handoff with exact start commands and a concrete local URL below.

## Handoff runtime

- API: `go run ./apps/api/cmd/server` (or the built binary) with
  `JOBSEEK_LISTEN_ADDR=127.0.0.1:<port>`,
  `JOBSEEK_DATA_DIR=<kept data dir>`,
  `JOBSEEK_PUBLIC_ORIGIN=http://127.0.0.1:<port>`; first run needs
  `jobseek setup-admin` (password from stdin).
- App: `bun run build && bun run preview` in `apps/vite-app`
  (or `bun run dev` for hot reload).
- Open the printed local URL in a browser. The owner signs in with the
  administrator password; every provider-backed action reports its
  exact readiness state before anything can start.
