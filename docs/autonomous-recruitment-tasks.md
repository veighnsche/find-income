> **RETIRED 26 Sep 2026 — superseded by [product-vision.md](product-vision.md), [connected-prototype-ux-parity-tasks.md](connected-prototype-ux-parity-tasks.md) and [p0-retain-cut-ledger.md](p0-retain-cut-ledger.md). The seven-step manual-handoff scope removed application approval/sending, delivery, employer-site autofill, and downstream replies/interviews/offers. Kept for history only; do not implement from this document.**

# Autonomous recruitment: ordered implementation tasks

Source: [implementation plan](/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-implementation-plan.md). Status: backlog only; every implementation task below starts unchecked. This schedule implements the plan without changing its product decisions.

Execution entrypoint: [supervisor prompt](/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-supervisor-prompt.md), which instructs the implementing supervisor to create a persistent goal, own lane A and coordinate lanes B–D through verified completion.

Use **four active implementation slots, including the coordinator**. Run ready tasks concurrently, prioritize work that unlocks integration, and keep one writer per shared file. The lanes below organize software development; they impose no source choices or sequence on Codex's eventual research.

## Execution lanes

| Lane | Assigned work | Default task priority |
| --- | --- | --- |
| **A — coordinator, contracts and product** | Shared interfaces, generated contracts, HTTP API, run UI, application composition and integrated checks. The coordinator implements work as well as coordinating. | T01 → T05 → T06 → T10; prioritize T23 when ready, otherwise advance T15; then T27 → T28 → T30 → T32 |
| **B — runtime and supervision** | Actual-runtime proof, App Server events/continuation, authority/allowance, run supervisor, MCP registration and runner operations. | T02 → T08 → T12 → T13 → T17 → T22 → T24 → T29 |
| **C — research and persistence** | Schema, immutable captures, research memory, general research execution, artifact retention and backup/restore. | T03 → T07 → T11 → T16 → T20 → T25 |
| **D — judgment, identity and records** | Evaluation corpus, dynamic Jev, matching, transactional record saves, opportunity/evidence handoff and result verification. | T04 → T09 → T14 → T18 → T21 → T19 → T26 → T31 |

These are default ownership queues, not additional dependency edges. A lane takes the next ready task; it does not wait for unrelated tasks with smaller numbers. An idle lane can take a clearly separated ready task after the coordinator records the file handoff. Never add an assumed fifth QA/reviewer slot. Apply the model-selection skill when dispatching each worker; use the cheapest suitable model/effort for that bounded task.

## Start order and concurrent dispatch

| When ready | Lane A | Lane B | Lane C | Lane D |
| --- | --- | --- | --- | --- |
| First | **T01:** baseline and ownership, a short setup task. | — | — | — |
| Immediately after T01 | **T05:** draft contracts and view states. | **T02:** selected-runtime capability proof. | **T03:** draft storage invariants/interfaces. | **T04:** acceptance corpus and scenarios. |
| As soon as T02/T03/T05 finish | **T06:** settle the shared contracts. | Publish proof artifacts; resolve only recorded proof failures. | Supply final schema/interface input. | Continue T04 if needed; it is not a global contract gate. |
| After T06 | **T10:** API and projections. | **T08:** App Server lifecycle/events. | **T07:** initial schema and shared store shapes. | **T09:** dynamic Jev. |
| As individual dependencies clear | **T15:** run UI. | **T12:** publish authority checks; **T13:** supervisor; **T17:** MCP and commissioning. | **T11:** captures/memory, then **T16:** general execution. | **T14:** identity, then **T18:** atomic saves. |
| Before final integration is ready | Prepare T23 composition using settled constructors. | **T22:** runner packaging/probes after T16/T17. | **T20:** recovery implementation after T11; prioritize T16 first. | **T21:** semantic cases; **T19:** opportunity/evidence handoff. |
| First working assembly | **T23:** connect the real backend/API components; do not wait for T15 UI. | Resolve runtime-owned integration faults. | Resolve research-owned integration faults. | Resolve judgment/write-owned integration faults. |
| Independently after T23 | **T27:** browser/product acceptance. | **T24:** concurrent Stop/resume/failure acceptance. | **T25:** current-format restore proof. | **T26:** integrated identity/evidence acceptance. |
| Release candidate | **T28:** one repository-wide verification pass and candidate artifact. | Prepare selected-host execution from T22. | Preserve verified capture/backup evidence. | Prepare the live-result audit rubric from T21/T26. |
| Actual runner | Track readiness results and prepare the canary brief. | **T29:** selected-host install/readiness/isolation checks. | No new deployment activity in parallel on that host. | No parallel live recruiting run. |
| Live acceptance | **T30:** one bounded real research canary. | Support the same run's runtime checks as needed. | Inspect persistence read-only as needed. | **T31:** audit saved outcomes when T30 completes. |
| Finish | **T32:** final handoff and truthful completion record. | — | — | — |

Rows illustrate overlap, not synchronized waves. The dependencies listed on each task below are authoritative. Do not hold a ready task for the slowest lane in a row.

## Ordered task checklist

Dependencies must be **complete with their stated evidence** before starting the task, except preparation explicitly listed in T03/T04/T05. Interface-tested components may use test doubles at settled boundaries; that never counts as integrated or live acceptance. Production registration/readiness cannot expose placeholder implementations as working features.

- [x] **T01 — Record baseline and allocate files**

  Lane **A** · Depends on: **—**. Record current HEAD and existing changes; preserve the removal and planning work. Assign the file owners below, evidence locations, isolated test directories and scarce-resource owners. Extract the plan's invariants and acceptance cases into the working checklist. Do not rerun unchanged full suites just to establish activity.

  Evidence: HEAD `df808dd` verified; user diff (README + remaining-work + 3 untracked planning docs) preserved; working record `docs/autonomous-recruitment-progress.md`; evidence root `implementation-notes/autonomous-recruitment/` created. No suites rerun.

- [x] **T02 — Prove the actual research/runtime path**

  Lane **B** · Depends on: **T01**. On the isolated selected runtime, exercise unseeded search, rendered browsing, discovered public API including read-only POST, executable research, authentic capture, saved-thread continuation, uncertain disconnect, Stop, and two simultaneous operations. Publish the selected binary/backend, receipt/event shapes, capture/control/cost coverage and exact failure evidence. Select the plan's native-or-instrumented binding; no source adapters. This is a protocol/backend proof using a disposable store, not the final product acceptance.

  Evidence: `implementation-notes/autonomous-recruitment/T02-proof/` — report.md (binding decision: isolated Playwright headless-shell `chromium_headless_shell-1243` selected, system Chrome rejected on profile coupling/hang/float), versions.txt, logs/ (16/16 fixtures green both backends + live real-binary proof PASS 1.6s), samples/ (5 receipts + 5 captures). Supervisor independently re-ran isolated fixtures green. Carried: stale `TestRetryRequiresCorrelatedTerminalHistory` count assertion → T08 entry (behavior stands, turn-list-authoritative).

- [x] **T03 — Prepare storage contracts and invariants**

  Lane **C** · Depends on: **T01**. Draft request/observation/note/capture/identity entities, keys, lease transitions, freshness, transaction boundaries, checkpoint/event fields and backup inventory. Coordinate required Jev/identity fields with D. Leave execution-receipt-dependent details provisional until T06. No schema freeze or production implementation yet.

  Evidence: `implementation-notes/autonomous-recruitment/T03-storage-contracts/storage-contracts-draft.md` — six entities, fingerprint, lease/freshness/txn/event/backup drafts, §5 self-review, 8 provisional items (P1–P8) for T06, Jev/identity proposals J1–J4 for D. Verified: no production file touched.

- [x] **T04 — Prepare the acceptance corpus**

  Lane **D** · Depends on: **T01**. Assemble a compact frozen set of cross-posts, distinct same-title vacancies, changed/reused postings, ambiguous and conflicting evidence, missing facts, query overlap and source failure cases. Include unfamiliar-source/browser/API scenarios and pre-recorded expected distinctions. Separate real captures from synthetic cases. Prepare the live-audit rubric; do not spend on a live recruitment run.

  Evidence: `implementation-notes/autonomous-recruitment/T04-corpus/` — 20-item frozen `corpus.json` (all 8 required kinds + brief_change), `expected-distinctions.md`, `provenance.md` (5 real-anchored w/ verified hashes, 15 synthetic), `synthetic-captures.json`, `live-audit-rubric.md`. Verified: real-source files/hashes match, no live spend.

- [x] **T05 — Draft tool/API contracts and product states**

  Lane **A** · Depends on: **T01**. Draft the six capability schemas, general research interfaces, errors, run/activity/budget projections, owner messages and versioned view-state fixtures. Map the conversational Start/Steer/Stop/Resume journey and retained workflow handoff. Draft against the plan while T02 runs; do not regenerate or freeze incompatible backend assumptions.

  Evidence: `implementation-notes/autonomous-recruitment/T05-contracts/capabilities.md` (six schemas, provisional research interfaces, shared outcomes/errors) + `product-states.md` (DTOs, owner messages, fixture shapes, journey, handoff). No OpenAPI regen; T02/T03-dependent items marked provisional for T06.

- [x] **T06 — Publish the shared implementation contract**

  Lane **A** · Depends on: **T02, T03, T05**. Reconcile actual-runtime findings with the schema/tool/API drafts. Fix receipt authenticity, capture lookup, claims, allowance/authority, event/checkpoint and identity/save interfaces. Update OpenAPI and generate both clients once. Publish constructor/store interfaces and representative fixtures so each lane can compile/test independently. Record one contract checkpoint and explicit file ownership; do not create a compatibility layer.

  Evidence: `implementation-notes/autonomous-recruitment/T06-contract/contract.md` (P1–P8/J1–J4 settled, C1–C8 accepted, TTLs, canonicalization, FTS5, ingestion/research identity split); `apps/api/internal/researchcontract/` (A-owned: outcomes, receipt/capture, fingerprint, authority/event/checkpoint, executor/memory, Jev/identity/save interfaces + 4 versioned fixtures, tests green); OpenAPI research endpoints + both clients regenerated once (check scripts pass); `RoundOperationCost` moved to B-owned `store/round_cost.go` (store green, `store.` qualifiers kept).

- [x] **T07 — Implement the initial schema and store foundation**

  Lane **C** · Depends on: **T06**. Add research/capture/identity and required run/event/usage fields directly to the initial schema; provide agreed row types, indexes, transaction helpers and fixtures. One schema writer handles requests from B/D. Verify fresh initialization, constraints and foreign keys. Existing private databases remain untouched.

  Evidence: `implementation-notes/autonomous-recruitment/T07-schema/` — 001_initial.sql +358 purely additive (9 research tables + run_events/run_checkpoints + 2 FTS5 + btree indexes, partial uniques, immutability triggers; supervisor confirmed zero removals); 9 new C-owned store files (ResearchDB, BEGIN IMMEDIATE helper, canonical hashing, fixtures); 12/12 schema tests PASS (supervisor re-ran). Schema FROZEN. Carried: `TestOpportunityConcurrentWritersGetOneConflict` SQLITE_BUSY_SNAPSHOT exposed by longer write window → T14 entry (D-side BEGIN IMMEDIATE fix per T03 §4).

- [x] **T08 — Implement App Server event and conversation support**

  Lane **B** · Depends on: **T06**. Persist/correlate supported item/turn events through the agreed sink; continue stored conversations; support interrupt and verified steering or its stop/reconcile fallback. Handle disconnect/backpressure/unknown events explicitly. Focused protocol tests prove correlation, cleanup and recovery inputs without depending on the real research store yet.

  Evidence: `implementation-notes/autonomous-recruitment/T08-events/` — `run_events.go` (RunEventSink implementing EventSink, ItemCorrelator, ResumeStoredThread, InterruptAttempt, SteerAttempt) + additive wiring in round_execution.go/service.go; entry item fixed (turn-list-authoritative counts, gating identical); 21/21 focused tests incl. -race; codexservice/codex/codexrunner suites green (supervisor re-ran, browser tests skipped). Sink is in-process (poor fit to existing tables); durable table binding is a T13 follow-up.

- [x] **T09 — Implement dynamic Jev assessment**

  Lane **D** · Depends on: **T06**. Accept Codex-defined questions/alternatives and captured-evidence references; validate structure, uncertainty and input binding; retain exact exchanges/model/usage and assessment reuse keys. Build the `jev_assess` handler against agreed capture/authority interfaces. Test missing/conflicting evidence, malformed/outage results and explicit reserved retries. No fixed source/query menu or three-call runtime ritual.

  Evidence: `implementation-notes/autonomous-recruitment/T09-jev/` — new D-owned `internal/jevassess/` implementing `researchcontract.Assessor` (validate→check→bind→reserve→single provider attempt→finish→sink; T06 reuse-key formula; verbatim alternatives + reserved abstain); 16/16 tests PASS incl. missing/conflicting/incomplete evidence, malformed/outage, reserved retry (supervisor re-ran green). Doubles only, zero spend; new-table sink binds at T14, real reader at T23.

- [x] **T10 — Implement HTTP contracts and safe projections**

  Lane **A** · Depends on: **T06**. Add research commission, steer, activity/capture/identity/report reads and existing run-control bindings behind agreed service interfaces. Validate owner identity, versions, idempotency and redacted DTOs; preserve inert reads. Use contract tests with service doubles until T23 connects the real supervisor.

  Evidence: `implementation-notes/autonomous-recruitment/T10-http/note.md` — 7 routes behind `ResearchService` interface (`httpapi/research.go`), validation + redacted generated DTOs, contract error mapping, 3/3 contract tests PASS, full httpapi package green. Run control reuses rounds stop/resume; unwired service → honest 503.

- [x] **T11 — Implement research memory and immutable provenance**

  Lane **C** · Depends on: **T07**. Add exact fingerprints, transactional claims/lease recovery, cache/freshness/justified refresh, observations, searchable investigation notes, private immutable artifacts and trusted receipt resolution. Supply `context_read`, `research_memory` and `evidence_capture` handlers in lane-owned files. Test two-store claims, stale workers, pre-role capture, forged receipts, snippets versus full evidence, truncation and historical-capture reuse.

  Evidence: `implementation-notes/autonomous-recruitment/T11-memory/` — new `internal/researchmemory/` (ClaimStore + CaptureReader asserted, private 0700 artifacts, T-observe/T-note txns, 3 handlers with constructors for T17); 27/27 tests PASS incl. -race (supervisor re-ran); store suite green; schema untouched (frozen). Design notes: frozen-schema receipt cross-check, takeover reconciliation pre-validated outside write txn (pool deadlock avoided), handlers return outcomes in-band.

- [x] **T12 — Publish transactional authority and allowance checks**

  Lane **B** · Depends on: **T07**. Implement the shared current-generation/deadline/permission and allowance-reservation operations needed inside both external dispatch and record-save transactions. Include credential rotation, stable replay identity, simultaneous reservations and the Stop fence. Deliver the real store helper and focused race tests as a separately usable checkpoint. Settle the cost-module ownership split before D edits mutation dispatch. This narrow handoff unlocks T18 without waiting for the rest of the supervisor.

  Evidence: `implementation-notes/autonomous-recruitment/T12-authority/` — real `researchcontract.Authority` in `rounds/authority.go` + in-txn helpers in `store/round_authority.go` (Reader-composable for D's write txn) + 5 research op charges in B-owned `round_cost.go`; 11/11 tests PASS incl. -race (supervisor re-ran); rounds+researchcontract suites green. Checkpoint documented for T18.

- [x] **T13 — Implement run supervision and durable continuation**

  Lane **B** · Depends on: **T08, T12**. Add commissioning, event journal, checkpoints, multi-turn continuation, steering acknowledgment and uncertain-attempt reconciliation around T12's authority/allowance operations. Stop fences first, then cancels execution; resume retains remaining allowance. Publish enforced/reserved/reported/unknown usage and bounded concurrency/deadlines. Test against the agreed research/tool interfaces; keep saved audited records when corrections fence pending writes.

  Evidence: `implementation-notes/autonomous-recruitment/T13-supervisor/` — `rounds/supervisor.go` (NewSupervisor + Commission/Dispatch/ContinueRun/Steer/Stop/Resume/Usage/Checkpoint/RunBoundsFor, fence-first via shared rounds.Service) + `store/round_supervision.go` (durable RunEventJournal over T07 tables, idempotent append, cursor paging, 32KB bound); 20/20 supervisor + 3/3 journal tests PASS incl. -race on concurrency-sensitive ones (supervisor re-ran); rounds+store suites green. Tested vs doubles; real executor binds at T23. Follow-ups: T17 TurnRunner adapter + ResearchService HTTP adapter + RubricVersion source; per-op bounds default is a T22-calibration placeholder.

- [x] **T14 — Implement employer/vacancy identity and semantic judgments**

  Lane **D** · Depends on: **T04, T07, T09**. Add broad candidate retrieval, strong identity aliases, source sightings and current candidate/revision decisions; call Jev for evidence-based semantic comparisons. Preserve unresolved observations and distinct same-title roles; handle reused identifiers and owner corrections. Test with immutable capture fixtures from T06/T04; connect the real capture reader at T23.

  Evidence: `implementation-notes/autonomous-recruitment/T14-identity/` — entry item fixed (Patch/ArchiveOpportunity via BEGIN IMMEDIATE `writeOpportunityImmediate`, conflict semantics kept; flaky test 10/10, full store suite green — supervisor re-ran); new `internal/identity/` Matcher (keys retrieve, Jev decides, no key-only exact) + AssessmentSink + `store/identity.go` support; 16/16 identity tests PASS, jevassess green. Known gap: board-record-id retrieval (no MatchAttributes field; D01 still separates).

- [x] **T15 — Implement the conversational run UI**

  Lane **A** · Depends on: **T05, T10**. Build the brief/allowance display, Start, activity, owner messages, Steer, Stop, Resume, truthful counts, stale/unknown/error states and reload recovery. Use generated clients and typed fixtures initially. No source toggles, manual row forms or invented phase progress. Include focused UI tests with no status-read auto-dispatch.

  Evidence: `implementation-notes/autonomous-recruitment/T15-run-ui/note.md` — api client + `ResearchRunPanel` mounted on agency home + pure state module; 14/14 new tests, full web suite 16/16, lint/tsc/build green. Reads never dispatch; reload recovery via persisted run id.

- [x] **T16 — Implement source-independent research execution**

  Lane **C** · Depends on: **T02, T11**. Productionize the selected search/fetch/browser/execution binding around automatic reservation/reuse and authentic capture. Support arbitrary public URLs/queries/parameters, redirects and read-only API POST; distinguish browser state from reusable artifacts. Apply address/byte/request/time/concurrency bounds and cancellation with no unobserved bypass for advertised controls. Test against the T06 authority interface; real supervisor integration is T23/T24.

  Evidence: `implementation-notes/autonomous-recruitment/T16-execution/` — new `internal/researchexecute/` implementing `researchcontract.Executor` (validate→Check→Reserve→Claim→dispatch→Observe→Release; isolated headless-shell + sandboxed python3 + recording proxy; per-hop redirect checks; read-only POST, verb rejection); 43/43 tests PASS +1 gated live skip, -race clean (supervisor re-ran); neighbors green. BOTH T02 egress gaps CLOSED via per-op seatbelt (subprocess IP confined to its recording proxy; bypasses die EPERM; missing sandbox/unpinned binary fail closed). Keychain silence proven (`TestBrowseKeychainSilence`). T22: Linux confinement backend; T23: real Authority + paths + Stop wiring; B side of source_links removal recorded for T17.

- [x] **T17 — Implement MCP registration and the commissioned agent loop**

  Lane **B** · Depends on: **T08, T13**. Register the six domain capabilities and general research operations through agreed handler interfaces; replace scoped-source/phase instructions and saved-resource prerequisites with account/action/run authority. Bind active credentials; aggregate actual outcomes into checkpoints/reports. Keep readiness unavailable until concrete handlers are wired. Protocol/tool tests may use doubles here; production integration is T23.

  Evidence: `implementation-notes/autonomous-recruitment/T17-mcp/` — 7-tool registry (`research_tools.go`: 6 capabilities + research_execute, one-turn capability + runId/generation/key checks, in-band outcomes; 17 wired / 10 unwired), briefs store, TurnRunner (resume-or-open, resume-failure-never-fresh, interrupt), `researchservice` package (T10 HTTP adapter + agent loop), rubric `criteria-v<N>-<digest>` with journaled source, 4-way readiness gate; focused tests green incl. -race (supervisor re-ran samples); codexservice/rounds/researchservice/httpapi suites green. T23 wiring documented (ResearchToolchain + RunBriefs + New into Options.Research).

- [x] **T18 — Implement atomic batch records and replay**

  Lane **D** · Depends on: **T12, T14**. Build `records_save` and `opportunity_match` handlers plus transactional company/role/evidence/identity/audit writes. Recheck authority, candidate revisions, captures and aliases in the transaction; no network calls under the write lock. Test concurrent same-identity saves, changed candidates, batch rollback, exact replay with rotated credentials, changed-payload conflict, multiple roles and unassessed roles. Preserve existing canonical opportunity IDs for downstream workflows.

  Evidence: `implementation-notes/autonomous-recruitment/T18-saves/` — `store/records_save.go` (one BEGIN IMMEDIATE txn, full D §4 order, real T12 in-txn helper, item-specific conflicts) + `recordsave.Handler` (RecordSaver asserted; lock-free pre-checks then one store call); 19/19 tests PASS incl. -race (supervisor re-ran); store+identity suites green. Semantics: same-identity races converge winner-ok + losers-identity_ambiguous; explicit reusedIdentifier supersedes; snippet blocks vacancyComplete; unassessed→`unassessed` stage; canonical IDs preserved.

- [x] **T19 — Connect opportunity/evidence views and retained workflows**

  Lane **D** · Depends on: **T10, T18**. Add real source history, identity/reuse explanation, fit/unknown status and contextual owner correction to existing opportunity/evidence panels. Verify record shape handoff to packs/interviews/replies/offers without another staging funnel. Replace only genuinely required structured record-entry tasks; retain conversational inputs, authentication and navigation filters. D owns these specific panels, not A's run UI files.

  Evidence: `implementation-notes/autonomous-recruitment/T19-handoff/` — `ResearchIdentityPanel` on opportunity detail (decision+basis, candidates, assessment ref, sightings, correction via OwnerInstructionPanel; 503/404 calm states) + unassessed badges + evidence fit/unknowns block; `record-judgment.ts` helpers + 11 tests; web suite 27/27, lint/tsc/build green (supervisor re-ran). Downstream handoff verified direct (packs/interviews/replies/offers, no second funnel). T26/T27 want: `GET /research/assessments/{id}` read for assessment deep-link.

- [x] **T20 — Implement capture-aware retention and recovery**

  Lane **C** · Depends on: **T11**. Update current-format backup/restore for consistent DB/artifact manifests, capture integrity, lease/token scrubbing, uncertain attempts and storage exhaustion. Keep referenced evidence retrievable and prevent restore from dispatching work. Develop with T07 schema and fixture snapshots while other lanes finish; real composed-app restore is T25.

  Evidence: `implementation-notes/autonomous-recruitment/T20-recovery/` — `ops/i26` format `jobseek-current-backup-v2` (artifact-root sidecar, per-row blob+receipt verification, manifest + executor identity + journal counts, scrubber: live claims→reconcilable uncertain, checkpoint claims emptied, generation left stale; bounds + exhaustion surfacing); 15/15 recovery tests PASS (supervisor re-ran) + CLI smoke. Restore procedure recorded for T25.

- [x] **T21 — Run focused judgment/identity/save evaluation**

  Lane **D** · Depends on: **T04, T09, T14, T18**. Exercise the frozen cases, hard negatives, abstention, brief changes, numeric/contextual suitability, outage handling, duplicate races and false-merge prevention. Review source bindings and distinguish deterministic fixture outcomes from any provider evidence already captured. Correct material faults in D-owned code; do not repeatedly resubmit unchanged cases or claim calibrated probabilities.

  Evidence: `implementation-notes/autonomous-recruitment/T21-eval/` (per-case matrix + logs) — one new D-owned `recordsave/corpus_eval_test.go` (16/16 + 6/6 subtests PASS, supervisor re-ran; neighbors green; race clean). Zero production edits: no material D fault found (two initial failures were test-authoring errors). T26 needs recorded: board-record-id match pin, conflicting-flag on claims, cross-post second-sighting label (schema-frozen enum).

- [x] **T22 — Prepare runner packaging and operational probes**

  Lane **B** · Depends on: **T16, T17**. Update the selected pin/config, general execution dependencies, required tool manifest, readiness, transport, isolation, cleanup and idle probes. Define concrete network/byte limits from T02. Prepare a fresh-install procedure and probe inputs before release acceptance; installing/proving the final selected-host artifact is T29.

  Evidence: `implementation-notes/autonomous-recruitment/T22-packaging/` (note + 9 logs) — pins file + verifier, Linux bwrap confinement backend + 5-stage proof, 17-tool manifest + 4-way drift gate, calibrated limits (1 MiB/response, 32 reqs/op, 60s/op, 5 redirects; per-run observed-only), 60s per-op default + Dispatch clamp in supervisor, install procedure (artifact/scratch dirs, /opt/jobseek shell, env template with executor vars), extended idle probes; probes 24/24 PASS (supervisor re-ran), Go suites green. Also delivered 3 T23 follow-ups (SetResearchWiring, jev_assess op + authority scope). T29 executes on host. main.go aligned to T22 env names at T23.

- [x] **T23 — Assemble the working backend and API**

  Lane **A** · Depends on: **T10, T16, T17, T18**. Wire the real executor, capture/memory store, Jev, identity, record handlers and supervisor into startup and the HTTP API. Execute an integrated controlled-source run with multiple records, cross-post reuse, a meaningful pivot, persisted evidence and continuation. Verify no production placeholder remains. Publish one integration checkpoint; route faults to the existing file owner rather than editing their files concurrently.

  Evidence: `implementation-notes/autonomous-recruitment/T23-integration/` (note + checkpoint) — `researchwire.Wire` binds real executor/memory/captures/authority/Jev/identity/supervisor into toolchain + MCP (17 tools) + HTTP API; integrated run commission→fetch×2→assess→match→save (1 record, 2 sightings)→API POST→404+reuse→steer→stop→resume→restart-continuation; 5/5 wire + 43/43 execute tests GREEN incl. real sandboxed browse render; placeholder sweep clean (prod Go + web src); T22 probes re-verified 24/24. Cross-lane faults routed to owners (T22 follow-ups, D jevfix/jevfinish, C FK amend A1); supervisor fixed C scratch-root creation with no C worker active. OPEN: D Supersedes resolver nil (fresh assessments fine; reassessment chains unlinked).

- [x] **T24 — Verify integrated runtime failure and concurrency behavior**

  Lane **B** · Depends on: **T22, T23**. Exercise two real research operations under one allowance, Stop during each boundary, late receipts/writes, changed authority, reconnect, context reconstruction, retries, unknown spend, browser/script egress and inert startup/status. Use isolated fixtures/processes and actual selected-runtime behavior where needed. Record remaining target-host assertions for T29, not false live claims.

  Evidence: `implementation-notes/autonomous-recruitment/T24-runtime/` — new test-only `internal/runtimeaccept/` (22 tests, all clauses incl. 5 Stop boundaries, reconnect, reconstruction, retries, unknown latch, real browse/exec egress + EPERM bypass denial, inert startup); 22/22 PASS (supervisor re-ran). Material findings FIXED per contract-owner rulings (`T24-fix/`): F1 prod-observer resolves research.* as observed_failure; F2/F3 release-on-pre-claim-refusal with full refund (3 repro tests rewritten to fixed semantics); runtimeaccept/rounds/codexservice/researchexecute/researchwire suites green (supervisor re-ran).

- [x] **T25 — Prove current-format restore on composed data**

  Lane **C** · Depends on: **T20, T23**. Back up and restore records plus immutable captures produced by T23; verify manifest/foreign keys/provenance and that active claims/capabilities are invalidated, uncertain work retained, and no execution starts. Use a fresh private test directory; never overwrite the owner's existing data.

  Evidence: `implementation-notes/autonomous-recruitment/T25-restore/` — new test-only package `internal/researchrestore/` (imports A-owned wire, zero production changes); `TestRestoreComposedStackRoundTrip` composes real T23 data (3 captures, 5 observations, 2 opportunities, 3 assessments, checkpoint) then runs the real `ops/i26/recovery.py` backup/verify/restore CLI; manifest v2, FK/integrity clean, captures byte-identical, live claims invalidated, uncertain retained, stale-gen/terminal rejections mutate nothing, no execution starts. PASS incl. -race (supervisor re-ran green).

- [x] **T26 — Verify integrated evidence and identity outcomes**

  Lane **D** · Depends on: **T21, T23**. Re-run only the integration-sensitive cases against the real capture, store, Jev binding and save path. Confirm cross-source identity, separate vacancies, source conflicts, legitimate refresh, historical captures, independent useful records in batches and source-backed suitability. Compare against T04's expected distinctions.

  Evidence: `implementation-notes/autonomous-recruitment/T26-identity/` — new `internal/recordsave/t26_identity_integration_test.go` (10 tests / 13 cases vs T04 matrix, verbatim corpus bytes over fixture HTTP, zero spend) + test-only repair of the T21 eval double for T23 jevfinish hooks; 10/10 PASS, full recordsave suite green (supervisor re-ran). Confirmed: cross-source merge, hard-negative separation, conflicts bound with both citations, refresh versioning, historical reuse chains, batch atomicity, source-backed suitability. Limitation: B01 reassessment chain link empty (nil Supersedes resolver, known T23 opening) — behavior otherwise correct.

- [x] **T27 — Run full product/browser acceptance**

  Lane **A** · Depends on: **T15, T19, T23**. Add the research smoke and use the existing complete relevant browser suite to verify Start/Steer/Stop/Resume, acknowledged corrections, reconnect, true counts/costs, evidence, zero-result reporting and retained workflow handoff. Keep the smoke's shared fixture entrypoint under A. This is the browser acceptance pass; repeat it only for relevant subsequent changes.

  Evidence: `implementation-notes/autonomous-recruitment/T27-browser/` — new `research-smoke.mjs` + test-only `researchsmoke` server (real wire, fixture board, zero-spend Jev) + shared silent-launcher `browser.mjs`; `pnpm e2e:research` PASS (supervisor re-ran), synthetic suite PASS, Go/web/probes green. Genuine bugs fixed minimally with regression tests: B1 browser Stop/Resume now route to the supervisor (A-owned httpapi files, B behavior); B2 `Supervisor.NoteSavedRecords` journals `run.saved` into the checkpoint (B-owned, flagged for lane B). Limits: scripted turns (no live model), completion needs T29/T30 runner, Supersedes gap untouched.

- [x] **T28 — Verify and package one release candidate**

  Lane **A** · Depends on: **T24, T25, T26, T27**. Run `pnpm check`, `pnpm test`, `pnpm build`, generated-output checks and any remaining required race/recovery checks not already satisfied on this revision. Confirm the T27 browser evidence is current, rerunning affected coverage if code changed. Produce one identified artifact/config/schema manifest, green evidence and honest limitations. Do not launch duplicate full suites from every lane.

  Evidence: `implementation-notes/autonomous-recruitment/T28-candidate/` — candidate `rc1-20260924-df808dd-a71533b0` (HEAD + worktree digest), artifact at `/tmp/t28-rc1` (reproducible via `ops/i12/build.sh`), manifest + SHA256SUMS + 13 logs. All gates green on this revision: pnpm check/test/build, go build + full go test (30 pkgs) + 9 race pkgs, web 27/27, probes 24/24, research smoke (twice post-fix) + synthetic e2e, recovery round-trip inside suite. Minimal fixes: OpenAPI `vp check --fix` + gofmt 3 A-owned files (whitespace only). Supervisor sampled `pnpm check` green + artifact present. Limits: uncommitted tree, no live run yet, nil Supersedes, cross-compiled binaries unexecuted here.

- [ ] **T29 — Verify the selected host with the candidate artifact**

  Lane **B** · Depends on: **T28**. Use the prepared private fresh-install path and actual account/tool access. Prove boundary, transport, process cleanup, capture path, readiness, configured limits and idle behavior on that host. Honor existing authorization; resolve genuinely missing host/access decisions only when the concrete candidate is ready. One operator owns host/config changes.

  DEFERRED 2026-09-24 (owner decision, recorded at T32): no host selected yet (`infra` vs `linux`, see [host readiness](/Users/vince/Projects/find-income/dashboard/docs/host-readiness.md)). Needs: selected host, private fresh-install path + actual account/tool access per [runner operations](/Users/vince/Projects/find-income/dashboard/ops/i12/README.md), one operator. Box stays unchecked; not done.

- [ ] **T30 — Run one bounded real recruitment canary**

  Lane **A** · Depends on: **T29**. Use the unchanged owner brief and the explicit finite allowance. Demonstrate unfamiliar sources, dynamically chosen queries, actual captures, saved/reused results, Stop and remaining-budget resume as applicable. Record every reservation, observed/unknown usage and failure. No fabricated minimum vacancy yield and no employer contact. One operator commissions the run; other lanes observe the same run, not duplicate it.

  DEFERRED 2026-09-24 (owner decision, recorded at T32): blocked on T29. Needs: T29 green, unchanged owner brief, explicit finite canary allowance, one commissioning operator. Box stays unchecked; not done.

- [ ] **T31 — Audit the live outcomes once**

  Lane **D** · Depends on: **T30**. Check the saved claims against actual captures, role identities and cross-posts, missed/false matches in the inspected set, suitability/unknowns, semantic reuse and the report's usefulness. Separate tool success, factual support and owner usefulness. Material failures return to the owning task; no unchanged serial review loop.

  DEFERRED 2026-09-24 (owner decision, recorded at T32): blocked on T30. Needs: completed T30 live outcomes; audit rubric already prepared at `implementation-notes/autonomous-recruitment/T04-corpus/live-audit-rubric.md`. One operator. Box stays unchecked; not done.

- [x] **T32 — Complete the handoff**

  Lane **A** · Depends on: **T28, T29, T30, T31**. Update README, remaining-work, runtime/recovery documentation and this checklist with evidence links and implemented/fixture/live distinctions. Record the candidate identity, known limits, current-format recovery steps and owner-usefulness outcome or pending feedback. Mark implementation complete only when required evidence is satisfied; do not relabel a blocked live check as done.

  Evidence: `implementation-notes/autonomous-recruitment/T32-handoff/note.md` — README + remaining-work rewritten with candidate/limits/fixture-vs-live distinctions; runtime/recovery status notes in `ops/i12/README.md`, `ops/i26/README.md`, `docs/host-readiness.md`; T29/T30/T31 left unchecked with dated deferral notes above; every evidence link verified to resolve. Candidate `rc1-20260924-df808dd-a71533b0` ([manifest](/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/T28-candidate/manifest.md)); owner usefulness pending live feedback. Implementation (T01–T28) complete; live acceptance (T29–T31) deferred, not done.

## Interfaces that enable parallel work

T06 must publish a small concrete handoff, not just agreement on names:

| Interface | Producer / owner | Consumers and independent test approach |
| --- | --- | --- |
| Trusted execution receipt and capture reader | B supplies proved runtime facts in T02; C owns production receipt verification, artifacts and capture lookup. | D uses immutable capture fixtures for Jev/matching/save tests. A uses redacted capture DTOs for UI. No model-authored receipt is trusted. |
| Research request/claim/freshness and executor | C. | B uses a fake executor for supervisor/Stop tests; later T23 binds C's executor and memory implementation. |
| Run authority, reservations, event sink and checkpoint | B; C adds agreed fields/indexes in T07. | C uses a deterministic authority/clock test double for executor tests. D's actual transaction tests use the B-owned T12 authority implementation. A tests HTTP projections with the same errors and states. |
| Jev/identity/record-service interfaces | D. | B registers handler interfaces; A consumes saved IDs/projections. Capture integrity and identity decisions remain checked by D's write transaction. |
| API and activity DTOs | A owns OpenAPI and both generated clients. | All lanes use the same schema/examples; UI can develop before the real services are connected. |

Shared interfaces are implemented contracts, not a compatibility abstraction for the removed system. Test doubles belong to tests or explicitly unavailable fixture environments. If a contract must change, its owner updates it and affected consumers in one coordinated patch; do not add old/new variants or allow schema drift.

## File ownership and scarce-resource locks

The following assignments apply to implementation. New lane-local files carry the same owner. Exact paths are locked, not whole directories where responsibilities are deliberately split.

| Surface | Sole writer / rule |
| --- | --- |
| [OpenAPI](/Users/vince/Projects/find-income/dashboard/packages/contracts/openapi.yaml), [TypeScript generated client](/Users/vince/Projects/find-income/dashboard/packages/contracts/src/generated/api.ts), [Go generated API](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/generated/api.gen.go) | **A.** Other lanes request schema changes; they never hand-edit generated output or run competing generators. |
| [Initial SQL schema](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/migrations/001_initial.sql) | **C.** B/D submit concrete field/index/constraint needs at T03/T06. Freeze the integrated shape at T07; later changes are coordinated directly, with no migration/backfill branch. |
| [Codex protocol client](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codex), [runner](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codexrunner), [round service](/Users/vince/Projects/find-income/dashboard/apps/api/internal/rounds/service.go), [agency engine](/Users/vince/Projects/find-income/dashboard/apps/api/internal/agency/engine.go) and run lifecycle/attempt/capability/dispatch store files | **B.** Owns event-loop changes, run authority and lifecycle tests. C owns new research lease files; D owns identity/save files. |
| [MCP registry](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codexservice/tools.go), [readiness](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codexservice/readiness.go), [round execution](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codexservice/round_execution.go) | **B.** C/D implement their own new handler files and provide constructors; only B changes registration or run instructions. |
| Research store/artifact/executor modules and [source fetch safeguards](/Users/vince/Projects/find-income/dashboard/apps/api/internal/codexservice/source_links_fetch.go) | **C.** New context/memory/evidence and general-research handler files are C-owned. Coordinate removal/replacement of the obsolete saved-company-only interface with B's registry change. |
| [Jev client](/Users/vince/Projects/find-income/dashboard/apps/api/internal/jev), [Jev service](/Users/vince/Projects/find-income/dashboard/apps/api/internal/jevservice), new identity/assessment/save store and tool files, [companies](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/companies.go), [opportunities](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/opportunities.go), [evidence](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/evidence.go), [ingestion matching](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/ingestion_match.go), [round mutations](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/round_mutations.go) | **D.** To avoid B/D fighting over `RoundOperationCost`, agree its signature during T06 and move its definition/constants into a B-owned cost module before T12/T18 overlap. D retains the mutation dispatch/transaction body. This is direct code organization, not another runtime abstraction. |
| [Server composition](/Users/vince/Projects/find-income/dashboard/apps/api/cmd/server/main.go), [HTTP API](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi), run UI and [web API client](/Users/vince/Projects/find-income/dashboard/apps/web/src/api.ts) | **A.** B owns the runner command separately. D owns only the opportunity/evidence and specifically assigned downstream panels for T19; A must not edit them concurrently. |
| [Opportunity view](/Users/vince/Projects/find-income/dashboard/apps/web/src/opportunities.tsx), [evidence panel](/Users/vince/Projects/find-income/dashboard/apps/web/src/evidence-panel.tsx) and their styles | **D** for T19. A owns home/briefing/owner-instruction/run UI and common activity contracts. Assign any additional downstream panel explicitly before editing it. |
| [Browser smoke suite](/Users/vince/Projects/find-income/dashboard/scripts/ui-smoke) | **A** owns the shared fixture entrypoint and suite registration. D supplies corpus/expected results in its own files. Each lane owns focused tests beside its implementation. |
| [Runner operations](/Users/vince/Projects/find-income/dashboard/ops/i12) and selected live runner | **B.** Exclusive configuration/process-control operator. A performs the single T30 commissioning operation after B hands off readiness; B may observe/support it without commissioning a second run. |
| [Current-format recovery](/Users/vince/Projects/find-income/dashboard/ops/i26) | **C.** Starts after capture/schema contracts, not after the release candidate. |
| Workspace lockfile, root build configuration and shared docs | **A.** Dependency additions from B/C/D are consolidated. Give fixture tests separate temp DBs, artifact roots and ports. |

Use one shared integration checkout with these file locks, or isolated worktrees when a task needs them. With worktrees, commit the contract/schema handoffs first and bring those exact commits into dependent checkouts; do not fork dependent work from stale interfaces. Worktrees do not remove merge or integration dependencies. Publish a coherent checkpoint when another task needs it rather than waiting for an entire lane to finish.

## Critical path and wall-time rules

The likely early critical chain is **T02 → T06 → T07 → T11 → T16 → T23**. The runtime chain **T08 → T13 → T17**, the authority handoff **T07 → T12**, the judgment/write chain **T09 → T14 → T18** (also needs T12), and API task **T10** join it at T23. The UI chain **T10 → T15** joins at T27, not at backend assembly. Once integrated, T24/T25/T26/T27 run concurrently before **T28 → T29 → T30 → T31 → T32**.

This is dependency-based prioritization, not a promised elapsed duration. Record actual effort/blockers as tasks execute and move spare capacity to the longest unfinished chain. Do not invent hour estimates before the runtime proof determines the real execution work.

1. Start T03/T04/T05 while the scarce runtime proof runs. They are low-rework preparation; do not prematurely implement a guessed research backend.
2. Release narrow interfaces at T06. Jev, identity, API and UI development need capture/authority contracts, not every production backend completed.
3. Prioritize C's T07/T11/T16 and B's T12 authority handoff before polishing nonblocking surfaces. A gives T23 backend composition priority as soon as it is ready, even if T15 UI work is still in progress; checkpoint or hand off those separate UI files. If C is overloaded, hand off a separate executor or recovery file set to a free lane; leave the schema and claim transaction under one owner.
4. Start T20 and T22 as soon as their real prerequisites exist. Packaging, capture retention and recovery design must not be discoveries after final tests.
5. Every implementation task includes its focused tests and meaningful evidence. Do not add a separate broad review task after every checkbox. Review shared contracts once at T06, composed behavior at T23–T27, and live results at T31.
6. Keep one repository-wide suite/build owner at T28. Focused checks can run concurrently in isolated test environments when CPU/RAM permit; avoid competing generators, shared fixture mutation and duplicated full suites. Test commands and build output must identify the revision they exercised.
7. Failures return to the responsible file owner with a reproducer. Fixes can run concurrently in disjoint files; rerun affected acceptance and any required integration checks. An external access blocker holds the affected live task only, not independent code/fixture work.
8. A task is checked off only when its deliverable, focused checks and handoff evidence exist. Suggested record beside its checkbox: commit/checkpoint, test/evidence path, limitations. Preparing a probe is not passing it; a fixture pass is not a live run.
9. Keep the original product constraints throughout: open source selection, automatic capture/memory, Codex-owned rows, evidence-based Jev, transactional identity, finite visible run limits and no legacy support. Reuse the completed design consultations; ordinary scheduling does not require another Jev review. A genuinely new difficult design choice still follows AGENTS.md.

## Coverage back to the implementation plan

| Plan package | Implemented by tasks |
| --- | --- |
| P0 — runtime proof | T02 |
| P1 — contracts/schema | T03, T05, T06, T07 |
| P2 — research memory/capture/execution | T11, T16 |
| P3 — Jev and identity | T04, T09, T14, T21 |
| P4 — tools and record writes | T11, T17, T18, T23 |
| P5 — research supervision | T08, T12, T13, T17, T24 |
| P6 — product/API/handoff | T10, T15, T19, T27 |
| P7 — integrated/live acceptance | T04, T21, T23, T24, T26, T27, T28, T30, T31 |
| P8 — operations/recovery/handoff | T20, T22, T25, T29, T32 |

The immediate dispatch when implementation begins is **T01**, then **T02 + T03 + T04 + T05 concurrently**. No app implementation or live run is started by writing this backlog.
