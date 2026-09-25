# P0 retain/cut/shared-dependency ledger

26 Sep 2026 · Map only. No app code changed to produce this ledger. Implementation follows S1–S5 then S6/Q-S.

## 0. Pre-existing uncommitted changes (recorded, not discarded)

Recorded 26 Sep 2026 from `git status --porcelain=v1` on `dashboard/main` at `f75f15a`.

Modified (10):

- `apps/api/internal/musewire/service.go` + test: Contributor transport gates Contributor readiness only, Standard unaffected. Related to seven-step direct-CLI readiness.
- `apps/api/internal/store/delivery_review.go`, `migrations/001_initial.sql`, `store/prepsend_verify_test.go`: owner-decision revision revokes approved contact on role removal. Out-of-scope contact hardening; S2 deletion supersedes it.
- `apps/vite-app/src/features/discovery/discovery-section.tsx`: copy clarifies agent-chosen sources, no employer contact, finite allowance. Related.
- `apps/vite-app/src/features/discovery/grouped-jobs.tsx` + test: saved-selection display, remove-from-chosen control, sticky count. Related to selection persistence.
- `apps/vite-app/src/pages/TodayPage.tsx` + `shell.test.tsx`: saved-research card linking to verified run. Related to return-to-work.

Untracked:

- `apps/vite-app/src/pages/ApplicationsPage.test.tsx` (70 lines, committed-baseline gap; partly asserts review/send routes that S1 removes).
- `docs/product-vision.md`, `docs/connected-prototype-ux-parity-tasks.md`, `docs/connected-prototype-ux-acceptance.md`, `docs/prototype-ui-ux-differences.md`, `docs/assets/connected-prototype-first-use-2026-09-25.png` (task scope inputs; left untracked by this work).
- `e11run` (20M Mach-O arm64 build artifact; never commit).

Rule: P0/S commits add only ledger + subtraction edits. Where a retained file already carries a related pre-existing hunk, the commit message names it explicitly. Contact-file pre-existing hunks are superseded by S2 deletion and recorded here.

## 1. Measured baseline (26 Sep 2026)

- Go production: 76,029 lines; Go tests: 59,071 lines; total Go: 135,100 lines (matches task-list baseline).
- Vite `src`: 29,459 lines total.
- Contact frontend candidate: `features/review` (6 files, 1,740 lines) + `features/attempts` (8 files, 956 lines) = 14 files, 2,696 lines.
- Contact backend candidate: `delivery/*` + `deliveryservice/*` + `httpapi/delivery.go` + `delivery_test.go` + `prepsend_verify_test.go` = 3,198 lines; `store/delivery_*.go` (5 files) = 965 lines.
- Downstream candidate: `interviewprep` + `replydraft` + `offercomparison` = 1,462 lines; `agency/*` = 5,366 lines; `httpapi/interviews*|replies*|offer_*` = 994 lines; `store/interviews*|replies*|offer_*|correspondence*` = 2,389 lines.
- Ceremony/duplicate candidate: `codexservice/*` + `researchwire/*` + `rounds/*` + `musecode/{session,supervisor,run,probe}.go` = 20,282 lines; `codex/*` + `codexrunner/*` + `researchservice/*` + `researchexecute/*` + `researchmemory/*` + `researchrestore/*` + `runtimeaccept/*` + `recordsave/*` = 22,785 lines. P0 does not authorize deleting all of this by name; S4 deletes only what import/call traces prove unnecessary.
- Retained seven-step core (samples): `store/{findings,jobcheck,answermatch,answervalues,materialprep,materialedit,reasoncatalog,role_workflow}*` = 8,584 lines; `musewire/*` + `musecode/{bounds,readiness,dto,terminal}.go` = 4,504 lines; `publicresearch/*` + `jev/*` + `jevassess/*` + `jevservice/*` = 10,746 lines; Vite retained pages + discovery/check/answers/prepare = 13,956 lines.

No percentage target. S6 reports actual removed lines by subsystem.

## 2. Retain for seven steps

Frontend (refactor, do not delete):

- `apps/vite-app/src/pages/TodayPage.tsx`, `SearchPage.tsx`, `JobsPage.tsx`, `JobDetailPage.tsx`, `ApplicationsPage.tsx`, `application-continue.tsx`, `role-stages.tsx`, `useRead.tsx`, `fixtures.tsx`, `format.tsx`, `navigation.test.tsx`, `role-stages.test.tsx`, `shell.test.tsx`.
- `apps/vite-app/src/features/discovery/*` except contact links; `features/check/*`; `features/answers/*`; `features/prepare/*`; `features/owner-context/*`; `components/shared/{stage-progress,stage-explainer,activity-disclosure}*`; `components/ui/*` shared primitives; `api/client.ts`, `api/session.tsx`; `routes/useRoute.tsx` minus review/attempt variants; `App.tsx` shell minus review/attempt routes.
- Retain pre-existing selection-removal and saved-research hunks; S1 repoints review/send links to Handoff placeholder.

API registrations to retain (`apps/api/internal/httpapi/handler.go`):

- Health/auth: `GET /health`, `POST /auth/login|logout`, `GET /auth/session`.
- Preferences read now, write added in A1: `GET /preferences`; replace `PUT /preferences → unsupported` with retained write contract.
- Discovery/check/answers/materials: `/research/runs*` (5), `/research/captures/{id}`, `/research/identity`, `/research/brief`, `/research/briefs/{version}/catalog`, `/research/runs/{id}/findings`, `/opportunities/{id}/finding`, `/opportunities/{id}/checks*` (4), `/answers*` (4), `/opportunities/{id}/answers/match*` (2), `/opportunities/{id}/answers/current`, `/opportunities/{id}/questions/{questionId}/answer`, `/opportunities/{id}/materials/*` (4).
- Opportunities/evidence/packs: `/companies` GET, `/opportunities` GET, `/opportunities/{id}` GET, `/opportunities/{id}/decision` GET+POST, `/opportunities/{id}/organisation` (verify; keep read only if Check/Handoff cites it), `/opportunities/{id}/screening`, `/opportunities/{id}/application-packs`, `/application-packs/{id}` + `/pdf` + `/source.zip`, `/opportunities/{id}/evidence-sources*` + `/evidence*`, `/opportunities/{id}/qualification*` reads, `/changes*`, `/workflow/roles`, `/opportunities/{id}/workflow`, `/runtime-status`, `/owner-instructions*`.
- Muse/CLI truth: keep only what direct Contributor/Standard readiness needs; S4 trims the rest (see §4).

Store/tables to retain:

- `preferences_*`, `companies`, `opportunities`, `opportunity_routes`, `owner_opportunity_decisions`, `evidence`, `evidence_sources`, `qualification_*` reads, `audit_changes`, `auth_*`, `agent_credentials` (minimal auth), `jobs` + `job_attempts` only if direct-CLI persistence uses them (verify; do not conflate with send attempts), `source_captures`, `source_openings`, `source_sightings`, `research_requests|observations|notes`, `identity_decisions`, `entity_identity_keys`, `jev_*` + `round_jev_*` only where Jev evidence requires (verify per table), `application_packs`, `owner_input_sources`, `record_changes|sightings`, `run_events`, `run_checkpoints`, `role_workflow`, `reason_catalogs`, `job_checks|job_check_activity|job_check_questions`, `saved_answers*`, `findings`, `answer_match_runs|answer_matches`, `answer_values`, `opportunity_material_versions`.
- Keep `applicationpacks_*`, `findings`, `jobcheck`, `answermatch`, `answervalues`, `materialprep`, `materialedit`, `reasoncatalog`, `role_workflow`, `research_captures|identity|events`, `jev_assessments*`, `muse_cursors|r Muse runs` store files where direct CLI/Jev reads require them.

Tests to retain/update: discovery/grouped-jobs/check/answers/prepare/page/shell/workflow/brief-catalog/checkchain suites; Jev, publicresearch save/search, materialprep/edit, findings/jobcheck/answermatch/answervalues/reasoncatalog/role_workflow store tests.

## 3. Cut: contact workflow (S1+S2+S5)

Frontend (S1 owner: shell/routes): delete `features/review/*` (6 files), `features/attempts/*` (8 files); remove `review` + `attempt` variants from `useRoute.tsx` (+ `useRoute.test.tsx` cases), `App.tsx` imports/`RoutePage` branches/nav-active logic; remove Prepare/Applications/continuation review/send/attempt links; remove `api/client.ts` delivery functions + `Delivery*` schema types (`getDeliveryCapability`, `prepareDeliveryReview`, `getDeliveryReview`, `approveDeliveryReview`, `sendDeliveryReview`, `reconcileDeliveryReview`, `closeDeliveryReview`); delete `ReviewPage/SendReview/ReviewAuthorization/Attempt*` tests; update `ApplicationsPage.test.tsx` review/send route expectations to Handoff.

Backend (S2 owner: server/contracts): remove `handler.go:160–166` (`GET /delivery/capability`, `POST /delivery/reviews`, `GET|POST /delivery/reviews/{id}[...]` 7 registrations); delete `httpapi/delivery.go`, `httpapi/delivery_test.go`, `httpapi/prepsend_verify_test.go`; delete `delivery/*` (3 files) and `deliveryservice/*` (4 files); remove `Handler.Delivery` field/option plumbing; remove `cmd/server/main.go:160–171` SMTP sender wiring + `JOBSEEK_SMTP_*` env handling; delete `cmd/e13send/main.go` (send runner); delete `store/delivery_intent.go`, `store/delivery_review.go`, `store/delivery_review_test.go`, `store/delivery_route_assessment.go`, `store/delivery_route_recovery.go`, `store/prepsend_verify_test.go`; drop `delivery_reviews`, `delivery_items` (+ `owner_decision_revision` pre-existing column), `delivery_route_assessments` tables/FKs/triggers from `001_initial.sql`; remove delivery schemas from OpenAPI/generated `api.gen.go` + Vite `client.ts` types; fix `agency/recommendation_outcome.go` delivery-advice import (cut with S3 agency path).

Negative autofill check: `grep -rni autofill|auto-fill|fill.*employer apps docs` finds only planning-prohibition prose and unrelated CV text; no Vite/API employer-site autofill module exists. S6 keeps a negative reachability probe instead of inventing a file to delete.

Keep: pack reads/PDF/ZIP, verified opportunity routes/route evidence for manual Handoff instructions.

## 4. Cut: downstream lifecycle (S3+S5)

Routes (`handler.go:88–109,154–155,169–180` + offer/relationship/action groups): remove interviews (4), correspondence (5: import/threads/get/process + `GET /replies/{id}`), rounds generic lifecycle (`/rounds/active`, `/latest-completed`, `/prepare`, `/compare-offers`, `/process-input`, `/{id}`, `/{id}/results`, `/history`, `/{id}/stop|resume|mutations`), offer-comparisons (2), `offer-option-sets` POST, and after consumer trace: `organisation/*` mutation + summaries if seven steps do not read them, `relationships/*`, `actions/*` (generic), `ingestions` write/retry if discovery no longer uses them. Each retained generic table keeps a named seven-step consumer in the S3 commit message.

Packages/files: delete `interviewprep/*` (3), `replydraft/*` (2), `offercomparison/*` (3); delete `agency/{interviews,offers,replies,packs,recommendation_outcome,recommendation_recovery,input*}.go` + tests after verifying no retained caller; delete `httpapi/{interviews,replies,offer_comparisons,offer_options,rounds,process_input,recommendation,records,relationships,actions}.go` + paired tests where the route group is cut; delete `codexservice/{interviews,replies,offer_comparison,applicationpacks_delivery,lifecycle,round_*}*` + tests with the same groups; delete `store/{interviews,interview_recovery,correspondence,replies,offer_comparisons,offer_options,round_*,offer_*}*.go` + tests with the groups; drop `interviews`, `interview_debriefs`, `correspondence_*` (4), `reply_processings`, `reply_drafts`, `offer_comparison_intakes`, `offer_comparisons`, `offer_tradeoff_assessments`, `offer_option_sets`, `offer_options`, `rounds`, `round_*` (10 tables) unless P0-proven retained-consumer keeps a named subset; remove paired OpenAPI/generated/client/types/tests.

Explicit non-equivalence: `job_attempts`/`round_attempts` are research/ingestion attempts, not employer-send attempts. S3 verifies current research/ingestion consumers before trimming; send attempts live only in delivery tables cut by S2.

## 5. Challenge duplicate ceremony/data paths (S4+S5)

- `codexservice.NewLazy` + `researchwire.Wire` + `rounds.Supervisor` `/research/runs` path vs `musewire.Service` Contributor path: retain one direct Contributor discovery/selected-check path. Current HTTP discovery uses the older round path while `musewire` is harness-only with `Authorized:false` checker and `UnavailableTransport{}` normal wiring. S4 keeps the smallest bounded CLI invocation + public capture/Jev/save primitives and deletes the loser commission/steering/session branch after tracing `commissionResearchRun|steerResearchRun|getResearchRun|listResearchActivity|getResearchReport` consumers.
- `musecode/{session,supervisor,run}.go` session/lane ceremony, `probe.go` `UnavailableTransport`, `musewire` cursor/run layers: keep only `Bounds|Facts|Check|Status|Tier` + direct CLI exec + cursors actually used by A2/A3/A5. Delete unsupported profile paths and dead supervisor branches.
- `/codex/connect`, `/codex/connect/cancel`, `/codex/mcp*`, `GET /codex/status`, `GET /muse/{readiness,checkpoints,report,commissions}`: keep one truthful direct-CLI readiness/activity surface; delete connect/MCP/session ceremony and redundant Muse status endpoints after frontend-consumer trace (`muse-state.ts`, `muse-panels.tsx`, `muse-journey.test.tsx`, `research-controls.tsx`).
- `codex/*`, `codexrunner/*`, `researchservice/*`, `researchexecute/*`, `researchmemory/*`, `researchrestore/*`, `runtimeaccept/*`, `recordsave/*`, `researchexecute` MCP `publicresearch/server.go` per-run tool server: S4 traces retained callers and deletes whole unused packages; retains minimal `publicresearch/{access,checks,save,search}` capture + Jev evidence + auth/CSRF + save/recovery primitives.
- Frontend: collapse duplicate brief/preferences reads and technical session/profile/readiness/budget panels into one truthful readiness/activity disclosure owned by Lane B/C; S4 deletes only proven-duplicate displays.
- Runners: keep `cmd/server`; delete `cmd/{codex-runner,e11run,e12run,e13run,e13send,researchsmoke}` after verifying no retained script/CI calls them (root `e11run` binary stays untracked, never committed).

## 6. Clean active support surface (S5)

- Schema: single coordinated edit of `001_initial.sql` + `002–011` follow-ups; drop cut tables/FKs/triggers/indexes; keep retained reads compiling; no backfill/compat views.
- Contracts: regenerate or hand-trim `httpapi/generated/api.gen.go` (5,892 lines) and Vite `api/client.ts` + `client.test.ts` for cut schemas; delete cut OpenAPI fragments.
- Tests: delete or rewrite cut workflow tests; no legacy-compat tests. Retained suites must pass.
- Active docs: retire send/lifecycle/ceremony instructions in `autonomous-recruitment-*`, `recruitment-workflow-*`, `rw-*`, `muse-recruitment-*`; point to `product-vision.md` + this ledger + parity/acceptance docs. Keep labeled historical evidence (`design/frontend/15-connected-design`, `rw-e*-evidence`, `rw-g5-acceptance`) as history.

## 7. Shared-file ownership and edit order

Single owners, no concurrent edits to the same file:

1. `apps/vite-app/src/routes/useRoute.tsx` + `App.tsx` + `api/client.ts`: Lane S1 owner. First: remove review/attempt routes/links/types; leave Handoff placeholder hook for Lane H.
2. `apps/api/internal/httpapi/handler.go` + `apps/api/cmd/server/main.go`: Lane S2 owner. Order: S2 delivery routes/SMTP → S3 lifecycle routes → S4 codex/muse/research route trim; S3/S4 propose diffs to S2 owner for integration.
3. `apps/api/internal/store/migrations/001_initial.sql` + `002–011`: S5 schema owner. Order: after S2/S3/S4 package decisions; one coordinated schema edit.
4. `apps/api/internal/httpapi/generated/api.gen.go` + OpenAPI source + Vite client: S5 contract owner, after route decisions.
5. Active docs: S5 docs owner, after code cuts land.

S1–S4 may run in parallel on disjoint packages; S5 integrates. Four-worker cap. No A–H feature work before S6/Q-S.

## 8. S6/Q-S gate checklist

- Route inventory: no `review|attempt|delivery|send|approve|reconcile` screen, hash route, or callable API; negative `autofill|SMTP|employer-site fill` probe passes.
- API registrations: cut groups absent from `handler.go`; retained seven-step reads compile and serve fixtures.
- Schema: cut tables/FKs absent; retained tables migrate cleanly; `go test ./...` (retained) + `bunx vitest run` (retained) pass.
- Contracts/docs: no orphan generated type, stale send contract, legacy compat test, or active send instruction remains.
- Measured diff reported by subsystem (frontend contact, backend contact, lifecycle, ceremony, contracts/tests/docs) with no percentage target.
