# Implementation progress — 26 September 2026

Goal: Complete all 40 tasks in dashboard/docs/current-code-implementation-lanes-2026-09-26.md, close the mapped current-code findings, and deliver the owner's seven-step UI and usability through a verified connected journey from saved goals to manual per-job Handoff. Commit coherent progress early and often, and preserve durable task, dependency and acceptance evidence until the goal is complete.

Baseline: HEAD ef463c48035f392848c676eea06d6f4e30751307 matches reviewed commit. Worktree retains: M docs/connected-prototype-ux-parity-tasks.md (superseded notice, valid); untracked authorities current-code-implementation-lanes, current-code-wishes-review, muse-spark-implementation-prompt. No unrelated user edits discarded. Contact/lifecycle removal recorded as retained; remaining subtraction is R30 runtime ceremony + R08 duplicate preparation workflow only.

File ownership (one writer per file; transfers recorded before editing):
- I (coordinator): packages/contracts/openapi.yaml, generated Go/TS contracts, httpapi/handler.go, cmd/server/main.go, all SQL migrations, store/role_workflow.go, shared DTOs, root verification/toolchain config.
- R: Contributor/Standard invocation adapters, musewire/live.go, standard.go, runner/factory/session/probe consumers, agreed obsolete package cuts. Hands check/classification/drafting consumers to D/K/M by exact file after R2.
- D: discovery/classification services, store/reasoncatalog.go, opportunities.go, agreed discovery persistence/query, discovery HTTP handlers/tests.
- K: check adapter/service, store/jobcheck.go, answer values/matches, clarifications, jev/answer_match.go, check/answer HTTP handlers/tests.
- M: store/artifacts.go, artifact_readiness.go, material persistence, materialprep/*, artifact/material/Handoff services, renderer/export adapters.
- F: App.tsx, api/client.ts, pages/useRead.tsx, routes/useRoute.tsx, shared nav/stage components, role-stages.tsx, application-continue.tsx, Search/Today/Jobs/JobDetail/Applications pages + integration tests.
- G: features/goals/*, features/owner-context/*, features/discovery/* + focused tests.
- C: features/check/* + focused tests.
- A: features/answers/* + focused tests.
- E: features/prepare/*, features/handoff/* + focused tests (after M1 hands over obsolete Prepare controls).
- Q: agreed connected-test/harness/evidence files, updated smoke gates.
- api/client.ts always F-owned; generated types always I-owned; handler.go/main.go/workflow/migrations always I-owned.

Task ledger (checkbox only when acceptance evidence exists):
- [x] I0 baseline/ownership ledger — done: HEAD ef463c4 == reviewed commit; docs-only delta; ledger committed a38ee26; baseline workflow synthesis confirmed.
- [x] I1 minimum cross-layer semantics — done: contracts C1–C9 frozen in implementation-contracts-2026-09-26.md (f81c777) with shapes, ownership, 8 failing Q0 cases. No Jev consultation: semantics derive directly from product vision + reviewed findings.
- [x] R1 direct CLI seam — done 02c077e: direct muse exec + output-schema + native web tools; app-side URL verification; loopback MCP/isolated-config deleted; echo zero-spend proofs pass; evidence/save boundary documented.
- [x] M1 remove second preparation workflow — done 23a2654: 30 pack files cut; route artifacts canonical; pack UI controls deleted; E files handed over. No pack PDF survives (not re-derivable).
- [x] R2 cut rejected runtime consumers — done 4a93058 + 2622b33: pack round-op/outcome/agency branches, codex session endpoints + CodexControl, pack tool/config/relevance, owner-authority case, required-tools list, migration 017, recovery script. R30 HTTP assertions pass.
- [x] I2 subtraction gate — done: no pack/codex/MCP-server production refs (greps); suite fails only on Q0 01/02/03/05/06/07 (future-lane gaps); build/vet/typecheck green.
- [x] I3 publish contracts — done 6ca278b + ee9dab1: 12 paths + 14 schemas cut; handoff_saved terminal; both regens pass check:generated; client/fixtures/stage UI aligned (temp I transfer returned to F/E/G). Gates OPEN for D/K/F. Wave-2 transfers: D owns researchwire/* for commission/recovery reads; K owns musewire/check.go + checkadapter.go (R hands over).
- [x] D1 catalog lifecycle — done (wave 2): deterministic brief→catalog derivation, get-or-author, concurrent convergence; Q0-01 PASS.
- [x] D2 vacancy identity/card facts — done (wave 2): URL reconcile in CreateOpportunity + P4 active-URL unique index; explicit-reuse retires the superseded row (R02/R03/T26 green); Q0-06 PASS. Card work-pattern capture + classify lookup-first deferred to M wave (P7/P8).
- [x] D3 server run recovery — done (wave 2) + I P2/P3/P9: recovery reads, GET /rounds list, run_not_found, D3/D4 route registrations; Q0-07 PASS.
- [x] D4 sourced owner context — done (wave 2) + I P9: owner identity + approved career sources, loader wired in main; zero answers, zero model calls.
- [x] K1 truthful route-complete checks — done (wave 2) + I zero-question gate (QuestionsNoneVerified): verdict/completeness/gaps/eligibility; Q0-03 PASS.
- [x] K2 Jev approved candidates — done (wave 2): Jev-only matching, no lexical gate, bounded recall + runoff, explicit no-fit. Handler wiring to MatchQuestions deferred (current Prefilter path keeps Q0-02 green).
- [x] K3 answer commit — done (wave 2) + I draft_requested (migration 018, openapi, store/commit/handler): keep/edit/clear/blank + idempotent commit + C4 required-blank satisfaction; Q0-02 PASS. ContinueAnswers batch refactor deferred (no Q0 caller).
- [ ] K4 owner clarification — contract done (wave 2: service + resolve-once semantics + tests); store/migration/API pending (I, with M/C/A/G wave).
- [ ] M2 evidence pins/replay — blocked on M1+I3+K1+K3
- [ ] M3 route-needed targets — blocked on M2+K1
- [ ] M4 grounded prepare — blocked on M2+M3+K3+K4+R1
- [ ] M5 edit/rewrite same artifacts — blocked on M2+M4
- [ ] M6 export/canonical index — blocked on M3+M4+M5+I3
- [x] F1 shared seams — done 0f34944 (wave 2): invalidation bus, SavedGoalsProvider, run restore, goal-editor registry, answer drafts, run links, commitRoleAnswers; 307/307 vite tests pass.
- [ ] F2 opening/status/stages — blocked on F1+D3+D4+K1
- [ ] F3 saved-artifact job list — blocked on F1+F2+M6
- [ ] G1 goals/source context — blocked on F1+D4
- [ ] G2 commission/run controls — blocked on F1+F2+D1+D3
- [ ] G3 jobs/choice — blocked on F1+D2+K1
- [ ] G4 selection/tabs — blocked on G3+F1+K1
- [ ] C1 truthful check UI — blocked on F1+K1+K3
- [ ] A1 suggestions vs saved — blocked on F1+K2+K3
- [ ] A2 draft preservation — blocked on F1+A1+K3
- [ ] A3 save/commit/continue — blocked on A1+A2+K1+K3
- [ ] E1 one Prepare action — blocked on F1+A3+M4
- [ ] E2 editors/rewrite — blocked on E1+M5+F1
- [ ] E3 partial/stale readable — blocked on E1+E2+M2+M6
- [ ] E4 manual Handoff — blocked on E2+E3+F3+M6
- [ ] Q0 connected harness — harness landed e6ae53f (8 substantive failing cases, only failures in httpapi suite); contract-specific stub updates after I3. Offending tests (correct through lane owners later): seeded-catalog fixtures in httpapi/discovery_test.go, briefcatalog_verify_test.go, musewire/service_test.go:235, store/findings_test.go, researchwire/control_http_test.go:231; R19-pending in httpapi/checkperform_test.go; R03/R04 GET-only in AnswersPage.test.tsx:844.
- [ ] Q1 seven-step identities — blocked on all feature lanes
- [ ] Q2 UI/usability — blocked on Q1
- [ ] Q3 live provider evidence — blocked on Q1 (+Q2 corrections for walkthrough)
- [ ] Q4 closure ledger — blocked on I2+Q1+Q2+Q3

Integrated dependencies landed: I0 (a38ee26), I1/C1–C9 (f81c777). R1 design settled on evidence: `muse exec` has native web tools + --output-schema, so direct invocation needs no MCP loopback; app verifies cited URLs itself.

Commits: a38ee26 docs(I0); f81c777 docs(I1/C1–C9); 983eb04 docs(I1 dispatch); e10557a docs(R2 map); e6ae53f test(Q0 harness); a7736fc docs(Q0); b4dcb67 docs(R1/M1 dispatch); 02c077e refactor(R1); 23a2654 refactor(M1); 4a93058 refactor(R2); 7c58cd7 test(Q0 rewrite); 2622b33 fix(R2 residuals).
Accepted boundaries: (1) publicresearch MCP registry type + musecode public_* allowlist + researchwire tool-protocol tests are inert (zero production session constructors) — D1/D3 rewrite them against the direct journey. (2) codexservice internals (dispatch/MCP bridge methods) + cmd/codex-runner retained for rounds/researchwire dispatch; HTTP session surface gone. (3) test_recovery.py fixture broken pre-existing (verified at HEAD b4dcb67: missing delivery tables); recovery.py pack checks removed; Go researchrestore green. (4) openapi pack/codex paths + generated code cut at I3 (contract publish).
Q0 state: cases 01/02/03/04/06/07/08 PASS; only 05 (R07/R23 staleness) fails — M/C/A/G wave scope.

Verification: meaningful focused checks per checkpoint before commit; Q gates require production HTTP + real temp store, controlled provider adapters only, no seeded catalog/answered state/ready artifacts.

Remaining limits: full 40-task scope open; live Contributor/Jev/Standard gates require configured services and implementation-run authorization — fixture success cannot close them; no employer contact authorized.

Next ready actions: dispatch M/C/A/G wave (M2–M6 + Q0-05 R07/R23 + deferred P6/P7/P8 + K proposals 3/4/6 + C/A/E/G UI lanes), then E/F finish and Q1–Q4 acceptance.
Wave-3 transfers: M owns materialprep/*, store/artifacts.go, store/artifact_readiness.go, store/prepare_activity.go, httpapi/materials.go, httpapi/artifacts.go, httpapi/evidence.go (I hands over; handler.go/openapi/migrations stay I-owned). C+A owns features/check/* + features/answers/*. G owns features/goals/*, features/owner-context/*, features/discovery/*. No wave-3 worker touches components/shared/*, api/client.ts (F-owned; propose missing seams to I), handler.go, openapi, migrations, or another lane's files. I keeps K4 persistence + P6/P7/P8 + K-proposal-6 inline.
Wave-2 + I-integration evidence: D (11 files: catalog/commission/recovery/owner-context) + K (11 files: verdict/match/commit/clarification + musewire check per transfer) + I (P1 fixtures incl. 2 httpapi helpers D missed, zero-question gate, draft_requested e2e, GET /rounds, run_not_found trio, D3/D4 registrations, career loader, P5 research_run, P4 index + reuse-archive composition). Deferred with rationale: K proposal 4/ContinueAnswers (no Q0 caller), P10 (optional), P6/P7/P8 + K3/K5/K6 + Q0-05 (M/C/A/G wave).
R2 candidate surface (read-only map, cut only after R1/M1 consumer trace): codex/*, codexrunner/*, codexservice/*, rounds/*, researchwire, cmd/codex-runner, httpapi/codex.go + rounds.go (+research/discovery dead paths), store/round_supervision.go, researchexecute, agency engine/input (pack parts via M1), runtimeaccept/doc.go. External importers outside those dirs: cmd/server/main.go, materialprep/standard.go (R1 seam), researchservice.go, reasoncatalog.go. Frontend: run/check activity feeds, actor-label, client.ts codex/round methods, fixtures. Keep: needed run persistence for D3, public evidence, Jev, bounded direct invocation, auth.
