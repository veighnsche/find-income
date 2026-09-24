# Autonomous recruitment progress

Supervisor-owned working record. Only the supervisor edits this file and the task checklist. Workers save evidence under `/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/Txx-<name>/` and report paths.

Goal: `goal-4846ab3d-471a-41da-8e9a-5901f63ca736` (active). Spec: `docs/autonomous-recruitment-implementation-plan.md`. Schedule: `docs/autonomous-recruitment-tasks.md` (T01–T32, T06 contract checkpoint authoritative for interfaces).

## T01 baseline (done 2026-09-24)

- Repo: `/Users/vince/Projects/find-income/dashboard` (git root is `dashboard/`, not the parent). HEAD `df808dd` "refactor: remove discovery pipeline and related components" — matches documented baseline. Removal and planning work preserved.
- Uncommitted user changes preserved, not reverted: `README.md` (1 line: plan/checklist links), `docs/remaining-work.md` (+2 lines: implementation entrypoint), untracked `docs/autonomous-recruitment-implementation-plan.md`, `docs/autonomous-recruitment-supervisor-prompt.md`, `docs/autonomous-recruitment-tasks.md`. Full diff captured in supervisor session at T01 time.
- Evidence root created: `/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/`. No prior progress existed; no reset of any checklist.
- T01 prep workflow ran 3 discovery + 1 synthesis agents (all `result_ready`); findings reconciled into this record. No full suites rerun for T01.

## File ownership (from tasks doc; sole writer per surface)

- A (supervisor): OpenAPI + both generated clients, `internal/researchcontract` (T06 contract package), server composition, HTTP API, run UI, web api client, browser-smoke shared entrypoint, lockfile/root config/shared docs. Consolidates dependency changes.
- B (runtime): `internal/codex`, `codexrunner`, `rounds/service.go`, `agency/engine.go`, run lifecycle/attempt/capability/dispatch store files, `store/round_cost.go` (from T06), MCP registry/readiness/round_execution, `ops/i12` + selected live runner (exclusive operator).
- C (research/storage): `migrations/001_initial.sql` (sole writer), research store/artifact/executor modules, `source_links_fetch.go`, new context/memory/evidence and general-research handlers, `ops/i26` recovery.
- D (judgment/records): `jev`, `jevservice`, new identity/assessment/save store+tool files, `store/{companies,opportunities,evidence,ingestion_match,round_mutations}.go`, opportunity/evidence web panels for T19.
- Shared-file rules: no competing generators, no hand-edited generated code, one writer per file; `RoundOperationCost` signature agreed at T06 and moved into a B-owned cost module before T12/T18 overlap. New lane-local files inherit the lane owner.

## Test isolation and scarce resources

- Every focused test uses `t.TempDir` (or fresh private dir) for DBs, artifact roots, ports. No shared fixture mutation, no owner-data overwrite. T25 uses a fresh private test dir.
- One operator for the selected live host (B), one owner for the full suite/build (A at T28), one writer per shared file, one T30 commissioning operator (A).

## Product invariants (plan, condensed; checklist must not violate these)

Codex chooses sources/queries/params/order and owns rows; Jev classifies supplied evidence only. Six capabilities (`context_read`, `research_memory`, `evidence_capture`, `jev_assess`, `opportunity_match`, `records_save`) + general research; automatic exact-request claim/capture; immutable server-side captures; no trusted model-authored receipts; transactional batch saves with replay/conflict rules; Stop fences then cancels; resume keeps remaining allowance; unknown spend always explicit; reads/startup inert; no fixed sources/adapters/registration, seeded queries, phases, funnels, manual row forms, or compat paths.

## Acceptance index (plan §11; evidence per row before T32)

Unfamiliar source, autonomous pivot, multi-result, fresh exact repeat, semantic repeat, refresh/failure, cross-post vs distinct opening, save race/replay, authentic evidence, Jev outage, Stop/uncertain dispatch, resume/steering, budget/concurrency, public freedom with isolation, product flow, role suitability, recovery. Fixture vs integrated-runtime vs live-host evidence kept distinct.

## Dispatch table

| Task | Lane | Agent | Status | Dependency checkpoint | Evidence |
| --- | --- | --- | --- | --- | --- |
| T01 | A | supervisor | done | — | this file; git HEAD df808dd + recorded diff |
| T02 | B | lane-b-t02-runtime-proof/5 → lane-b-t02-resume/8 (replacement; prior worker terminal after interrupt-redirect, reuse rejected) | done | T01 done | impl-notes/autonomous-recruitment/T02-proof/ (isolated headless-shell selected; 16/16 fixtures both backends + live PASS; supervisor re-ran isolated green) |
| T03 | C | lane-c-t03-storage-contracts/6 | done | T01 done | impl-notes/autonomous-recruitment/T03-storage-contracts/ (verified; prov. P1–P8 + J1–J4 for T06) |
| T04 | D | lane-d-t04-acceptance-corpus/7 | done | T01 done | impl-notes/autonomous-recruitment/T04-corpus/ (verified: 20 items, hashes match, no spend) |
| T06-input | D | lane-d-t04-acceptance-corpus/7 | done | T03+T04 done | impl-notes/autonomous-recruitment/T06-D-input/schema-input-note.md (verified: C1–C8 corrections, assessment binding spec, DDL list, corpus trace, save-validation; no tracked files touched) |
| T05 | A | supervisor | done | T01 done | impl-notes/autonomous-recruitment/T05-contracts/ (2 drafts; prov. items for T06) |
| T06 | A | supervisor | done | T02+T03+T05 done | impl-notes/autonomous-recruitment/T06-contract/contract.md + `internal/researchcontract/` (tests green) + OpenAPI research endpoints with both clients regenerated once (checks pass) + `store/round_cost.go` move (store green) |
| T07 | C | lane-c-t07-schema/9 | done | T06 done | impl-notes/autonomous-recruitment/T07-schema/ (schema frozen +358 additive; 12/12 green, verified) |
| T11 | C | lane-c-t11-memory/12 | done | T07 done | impl-notes/autonomous-recruitment/T11-memory/ (ClaimStore+CaptureReader+handlers; 27/27 incl. race, verified) |
| T16 | C | lane-c-t16-execution/17 | done | T02+T11 done | impl-notes/autonomous-recruitment/T16-execution/ (Executor; 43/43+race verified; egress gaps closed; keychain silence proven) |
| T12 | B | lane-b-t12-authority/13 | done | T07 done | impl-notes/autonomous-recruitment/T12-authority/ (real Authority + in-txn helpers; 11/11 incl. race, verified) |
| T13 | B | lane-b-t13-supervisor/15 | done | T08+T12 done | impl-notes/autonomous-recruitment/T13-supervisor/ (supervisor+journal; 20/20+3/3 incl. race, verified) |
| T17 | B | lane-b-t17-mcp/18 | done | T08+T13 done | impl-notes/autonomous-recruitment/T17-mcp/ (7-tool registry + TurnRunner + HTTP adapter; suites green, verified) |
| T22 | B | lane-b-t22-packaging/22 | done | T16+T17 done | impl-notes/autonomous-recruitment/T22-packaging/ (pins+probes+install+confinement; 24/24 verified; 3 T23 follow-ups landed) |
| T23 | A | supervisor | running | T10+T16+T17+T18 done | integrated controlled-source run; one checkpoint; faults routed to owners |
| T14 | D | lane-d-t14-identity/14 | done | T04+T07+T09 done | impl-notes/autonomous-recruitment/T14-identity/ (entry fix verified, Matcher+sink 16/16 green) |
| T18 | D | lane-d-t18-saves/16 | done | T12+T14 done | impl-notes/autonomous-recruitment/T18-saves/ (atomic RecordSaver; 19/19 incl. race, verified) |
| T21 | D | lane-d-t21-eval/19 | done | T04+T09+T14+T18 done | impl-notes/autonomous-recruitment/T21-eval/ (16/16+6/6 eval matrix, verified; zero prod edits) |
| T19 | D | lane-d-t19-handoff/21 | done | T10+T18 done | impl-notes/autonomous-recruitment/T19-handoff/ (identity panel + fit/unknowns; web 27/27/lint/build verified) |
| T20 | C | lane-c-t20-recovery/20 | done | T11 done | impl-notes/autonomous-recruitment/T20-recovery/ (backup-v2 + scrubber; 15/15 verified) |
| T08 | B | lane-b-t08-events/10 | done | T06 done | impl-notes/autonomous-recruitment/T08-events/ (sink+correlator+resume/interrupt/steer; 21/21 incl. race; suites green, verified; durable binding at T13) |
| T09 | D | lane-d-t09-jev/11 | done | T06 done | impl-notes/autonomous-recruitment/T09-jev/ (Handler + 16/16 tests green, verified; sink binding at T14) |
| T10 | A | supervisor | done | T06 done | impl-notes/autonomous-recruitment/T10-http/note.md (7 routes + contract tests green, full httpapi green) |
| T15 | A | supervisor | done | T05+T10 done | impl-notes/autonomous-recruitment/T15-run-ui/note.md (panel + client + state tests 14/14, web suite/build/lint green) |

Model selection per dispatch (Codex model names unavailable in this session; children inherit supervisor route, scope bounded instead): T02 ≈ Sol-High (cross-file uncertain runtime proof, binding trade-off); T03 ≈ Sol-Medium (bounded multi-step drafting); T04 ≈ Sol-Medium (bounded corpus assembly).

## Integration checkpoints / unresolved contracts / host-canary state

- Contract checkpoint: T06 PUBLISHED 2026-09-24 (authoritative for interfaces; no compat layer). Receipt/capture shapes from T02 §4; fingerprint/canonicalization/TTLs/cache-scope per contract §§2–3; claim/lease + P2 reconciliation via `Authority.ReconciliationFor`; event envelope + checkpoint + budget ledger per §5; identity/assessment/save per D C1–C8 + §§2–4; cost module moved; OpenAPI research endpoints + both clients generated once. Standing constraint: proof/test browsers must never touch the login keychain or show UI (carries into T16/T22). Reusable tables confirmed present: rounds, round_attempts, round_tool_capabilities, round_reconciliation_checks, jev_attempts, evidence, audit_changes, record_changes. Authority mechanics confirmed reusable: rounds.Service Reserve→Dispatch→Complete→ApplyMutation with generation fencing, Begin/FinishRoundReconciliation, uncertain-attempt handling (service.go); T12 extends directly.
- Unresolved: T14 entry resolved (BEGIN IMMEDIATE fix verified, full store suite green). T14 known gap: board-record-id retrieval has no `MatchAttributes` field (D01 still separates via FTS + Jev-distinct) — T18/T23 may add the field via contract patch if needed. Browser subrequest + direct script egress gaps → T16/T22 must close or re-prove; saved-company interface removal coordinated with B registry change (T16/T17).
- C→supervisor handoff (T03 §8–§9): P1 receipt shape + P8 cache_scope need T02 facts; P2/P7 need B interfaces at T06/T12; P3/P4 have C defaults for T06 sign-off (D accepts P3 default in T06-D-input §3.2); J1–J4 answered in T06-D-input (C1–C8 corrections, jev_assessments_dynamic + link table spec, record_sightings proposal, DDL list, save-validation, open items incl. source_openings.identity_key overlap question for T06).
- Selected host: undecided; T22 prepares procedure, T29 proves. Live canary: no allowance authorized yet; T30 needs explicit finite allowance + unchanged owner brief.
