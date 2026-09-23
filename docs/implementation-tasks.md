# Recruitment agency — full task list and concurrent execution guide

Updated 23 September 2026. The full I01–I27 list implements the [implementation plan](/Users/vince/Projects/find-income/dashboard-implementation-plan.md) under the active completion goal. API/contracts are committed in `2fdfe7d`, deployment configuration in `717ef93`, and web review in `8c8b66d`. Root check/test/build pass. Core corrections and immutable pack-correction preparation are committed; commissioned input integration and native runner configuration now run concurrently.

**Current position: 5 tasks accepted, 13 partial, 2 active tasks, 7 not started or conditional.** Accepted: I01–I04 and I09. Partial: I05–I08, I10–I12, I14, I15, I18, I20, I24, I26. Active: I13 and I25. I17 is conditional. Counts describe acceptance, not percentage of product completion.

The first useful milestone is **real automatic discovery → one selected lead → one truthful application pack** (I16). Complete-source coverage, a general asset library, all email/ATS integrations and the entire agency lifecycle are not prerequisites for that milestone. Live runtime access and the actual search capability remain unverified.

## 1. Models and reasoning levels

Assignments below are engineering judgments using the [model-selection skill](/Users/vince/.codex/skills/model-selection/SKILL.md). Official guidance recommends selecting model and effort to match task complexity and usage constraints; it does not prove the cheapest completed assignment for this repository. [OpenAI model-selection guidance](https://developers.openai.com/api/docs/guides/model-selection), checked 23 September 2026.

| Work | Model | Reasoning | Dispatch values |
| --- | --- | --- | --- |
| Bounded evidence gathering and established multi-step document updates | GPT-6 Luna | Medium | `gpt-6-luna`, `medium` |
| Final factual documentation from already verified results | GPT-6 Luna | Low (Light in the skill) | `gpt-6-luna`, `low` |
| Ordinary implementation, UI, connectors and defined acceptance work | GPT-6 Sol | Medium | `gpt-6-sol`, `medium` |
| Coupled persistence, execution, integration and external-action code | GPT-6 Sol | High | `gpt-6-sol`, `high` |
| Independent round/runner and delivery authority reviews | GPT-6 Astra | High | `gpt-6-astra`, `high` |

Use the assigned starting level; escalate a specific task when an actual ambiguity or repeated failed approach justifies it. Do not assign every worker Astra, re-run accepted slices for appearances, or claim measured token savings. Reuse relevant existing task context and give each worker only its scope, interfaces and required evidence. This table selects implementation workers; it does not select the dashboard's eventual runtime model or change the current coordinator model.

## 2. How to run concurrently

Use **one coordinator plus up to four independent live implementation tasks**. Four is a work-in-progress ceiling, not a requirement to keep idle workers generating output. If using the separate subagent tool with this session's four total slots, use at most three subagents plus the coordinator. The schedule below targets the user-visible live tasks already used on this project.

Dependencies in the task index gate **full integration and acceptance**. They do not prohibit an explicitly named independent slice from coding against an agreed narrow interface. A worker must have useful owned code to change, not an instruction to wait and repeatedly check another task. When a worker finishes, review its result and immediately dispatch the next ready slice; do not wait for an entire batch to finish.

### Current live assignments

| Task | Model / reasoning | Owned work |
| --- | --- | --- |
| I05/I11 contextual input | Sol High | Shared process_input, profile effective-version boundary, explicit paused replacement, pack transaction guard and API/contracts. |
| I13 native runtime correction | Sol Medium | Runtime-owned Codex files and ops/i12 restrictive profile; final wrapper/account/MCP acceptance remains open. |
| I11 contextual-input UI | Sol Medium | apps/web/src and browser fixture smoke against the published process-input contract. |
| I25 offer comparison domain | Sol Medium | New offercomparison and Jev offer files; no shared schema or UI edits. |

Core corrections committed7310026 after independent review, final retry/import fixes and root focused tests. Immutable pack-correction preparation committed1c8417f; shared storage/commission integration remains open. Native research and synthetic Linux evidence are complete; three fresh Jev consultations advised the profile direction with material confidence variation, recorded in [native-tool-boundary.md](native-tool-boundary.md). The runtime writer implements that direction while core connects contextual input. The web writer consumes the published contextual-input contract; interview helpers are committed7299c54 after root review/tests, and the worker now implements the independent I25 offer domain. Four live workers maximum.

Reviewed checkpoints: API `2fdfe7d`, deployment `717ef93`, UI `8c8b66d`. Root `pnpm check`, `pnpm test`, `pnpm build` pass after contract formatting. Web has no automated test files; controlled browser interactions and screenshots were reviewed. The e2e placeholder is replaced by browser fixture smoke in a2726ed; root pnpm e2e passed.

I13 found four concrete gaps despite passing tests: native tools outside round accounting, expired paused rounds retaining the active slot, missing production Lever continuation, and prepare context reads using a resource outside the prepare scope. The three core findings are corrected and reviewed in7310026; native runner and live acceptance remain open. Reliable commissioned input processing, corrected-pack persistence, explicit paused-round replacement and Jev choice among neutral discovered leads remain open. The commissioned input, profile-version and explicit paused-round replacement design is recorded in [contextual-input-decisions.md](contextual-input-decisions.md); shared implementation follows this review. See [input integration gap](/Users/vince/Projects/find-income/implementation-notes/implementation/I11-input-orchestration-gap.md).

I14 retained ten completed responsibility cases (seven real, three synthetic) and one HTTP529-incomplete case. The 22 calls reported 60,960 tokens, exceeding the 60,000 target by 960 on the final response; failed-call usage is unknown. No automatic retry or general accuracy claim. See [evaluation](jev-responsibility-evaluation.md). Recovery committed3bcbd42 with five passing synthetic tests. Host/account and inbox choices remain unanswered; no live deployment, mailbox access or employer send occurred.

### Initial wave ownership and handoff record

These slice labels divide existing tasks; they do not create additional product acceptance checkboxes.

| Slot | Slice / model / reasoning | Implement now | Required handoff / stopping point |
| --- | --- | --- | --- |
| A — core | I05-A · Sol High | Owner instruction scope, direct select/dismiss/ack, necessary profile/evidence mutation boundaries, typed result/history contracts. Own the shared schema, OpenAPI/generated types, HTTP and round bridge. | Publish usable types and store signatures early for B/C/D and web. Stop after focused authority tests and report; no live executor claim. |
| B — runtime | I06-A · Sol High | Finish the bounded research/tool adapter and commissioned execution integration in runtime-owned files. Reuse accepted lifecycle and turn code. Verify one actual supported research path; unresolved provider access remains explicit. | Consume existing allowance/dispatch interfaces; request shared tool registration/store changes from A. If a difficult provider/tool decision is unresolved, gather evidence and use the required Jev consultation before committing to it. |
| C — collection | I07-A · Sol High | Connect accepted Lever batches to existing source identity/snapshot/reuse/refresh code through the shared authority. Preserve final-page drain and cross-round continuation. | Own collector and explicitly handed-over ingestion/source identity files. A supplies any initial-schema/shared mutation edits. Stop after collector/store integration fixtures; no automatic scanner or live search. |
| D — Jev (handed off) | I10-A · Sol High | Add durable decision-attempt capture through a narrow store interface, including invalid responses/usage; connect existing factual and candidate-choice helpers to supplied acquisition evidence. | Own Jev/service logic and explicitly named Jev persistence files; A alone applies shared schema edits. Final source-choice integration waits for I08 candidates, but supplied-candidate execution/failure tests can run now. |

**Previous slot-D handoff, now reviewed and committed:** integrate server-backed contextual instructions, direct selection/dismissal/acknowledgement, typed cards and history in `apps/web/src/**`. Core owns DTOs and sends its stable interface directly. I10-A durable supplied-input Jev capture passed coordinator review and deadline correction; production decision application and real quality remain open.

A publishes **contracts, not speculative alternate implementations**. B/C/D can implement their owned logic and focused tests while A handles shared changes; they cannot independently invent incompatible DTOs or edit the same files. If a dependency blocks the entire assigned slice, stop it and reuse its slot for a ready slice below.

### Rolling schedule through first useful employment work

| Stage | Work that can overlap | Release condition |
| --- | --- | --- |
| A — finish current foundations | I05-A, I06-A, I07-A, I10-A as above | Review each bounded handoff independently; leave full task unchecked until integrated. |
| B — connect the product | I11 server-backed UI when I05 types land; I08 source discovery after the required I06 research/I07 acquisition interfaces land; I10 final selection/orchestration integration; I12 deployment/configuration and real setup as runtime prerequisites allow | Reuse released slots, at most four. I05 core owns final shared startup/orchestration binding and supplies schema/contract changes. If it is actively integrating, it consumes a slot; replace the least-ready B task rather than adding a fifth worker. |
| C — get to a reviewable whole | Finish I05/I06/I07/I08/I10/I11/I12; start I15 pack generation/review once its source, runtime and selected-role contracts are usable | I15 may use sourced fixtures before discovery is accepted. The web writer alone integrates pack UI. Missing live host/login does not block offline pack work. |
| D — verify the boundary | I13 Astra High review of the integrated discovery/runtime path; I15 independent pack implementation can continue in disjoint files | Freeze files under review. Implementer fixes findings, then reviewer checks affected boundaries. I13 acceptance requires actual I12 evidence; an offline review can be recorded as partial. |
| E — prove discovery | I14 real bounded discovery/quality acceptance; finish I15 pack path in parallel | I14 runs after I13 and its task dependencies. Record actual calls, partial results, non-seed sources and Jev decisions. A zero-result run is not useful discovery. |
| F — prove usefulness | I16 real lead-to-pack walkthrough | I14 and I15 accepted. Show the owner an actual worthwhile lead and truthful usable pack; do not send it as part of this milestone. |

**First-path wiring owner:** core (I05) owns the production Start/Resume → commissioned work loop and shared registration/startup. Runtime (I06) owns executable turns and bounded research; collection (I07/I08) owns acquisition/identity; Jev (I10) owns recorded semantic decisions; web (I11) owns the UI. This final binding is part of the existing tasks and must not remain an unnamed gap between them. One owner action must cause the supported work to finish or pause with a report, not merely queue inert rows.

### Rolling schedule after the first pack

The following work may start as soon as its dependencies permit, but shared-file integration yields to finishing I16.

| Concurrent group | Models / reasoning | Conditions and ownership |
| --- | --- | --- |
| I18 contacts, I20 one delivery adapter, I26 current-format operation/backup, conditional I17 extra source | Sol Medium; Sol High; Sol Medium; Sol Medium | I18 needs I15; I20/I26 need I12+I15; I17 needs observed I14 coverage misses. Separate domain/adapter/ops/collector ownership; one schema and one web integrator. |
| I19 inbound correspondence, I21 approval/delivery, I24 interview prep, I25 offer comparison | Sol Medium; Sol High; Sol Medium; Sol Medium | I19 needs I12+I18; I21 needs I15+I20; I24 needs I15+I18; I25 needs I01+I09+I18. Split inbound/outbound packages and named domain files; schema/web changes serialize through the integrator. |
| I22 delivery review, I23 reply drafts, remaining interview/offer/ops fixes | Astra High; Sol Medium; assigned task level | I22 follows I21; I23 follows I09+I19 and does not wait for outbound delivery. Freeze reviewed delivery files; drafts cannot send through an unaccepted route. |
| I27 complete journey, then verified operating docs | Sol Medium, then Luna Low | All I27 dependencies accepted. Technical acceptance precedes the short factual documentation pass. I17 remains optional. |

### File ownership: one writer per path

All paths below are relative to `/Users/vince/Projects/find-income/dashboard`. The project root is not a Git repository; `dashboard/` contains the Git repository. Reuse the saved local project directly and inspect its status before each run. If later work is uncommitted, preserve that work when isolating a task.

| Owner | Default owned paths | Shared-change rule |
| --- | --- | --- |
| Core/integrator | `apps/api/internal/rounds/**`, `apps/api/internal/store/round*.go`, `apps/api/internal/store/migrations/001_initial.sql`, `apps/api/internal/httpapi/**`, `apps/api/cmd/server/**`, `packages/contracts/**`, `apps/api/internal/codexservice/tools.go`, `round_bridge.go` | Own shared schema, generated output, service registration and startup. Hand over individual domain store files explicitly. No broad ownership of every store file while delegating them. |
| Runtime | `apps/api/internal/codex/**`, `apps/api/internal/codexrunner/**`, `apps/api/cmd/codex-runner/**`, runtime/lifecycle/execution files under `internal/codexservice` excluding core bridge files | Coordinate shared struct/signature changes. No schema/HTTP edits without handoff. |
| Collection | `apps/api/internal/collector/**`; explicitly assigned `internal/store/ingestion*.go` and matching identity tests | `ingestion_dispatch_resolution.go` and `ingestion_resume_test.go` stay with the core/runtime boundary unless specifically handed over. No generic jobs/round/store-wide edits. |
| Jev | `apps/api/internal/jev/**`; explicitly assigned organisation/assessment persistence/service files | Source identity, shared initial schema and round authorization remain with their named owners. No hidden fallback classifier. |
| Web | `apps/web/src/**` | Other workers propose DTOs/behavior, then the web writer integrates their UI. Do not let pack/contacts/delivery workers also edit `main.tsx`, `api.ts` or shared styles. |
| Later feature workers | Named application/inbound/outbound/interview/offer/ops files declared at dispatch | Proposed new module paths are chosen once and recorded before creation. Shared schema, contracts, startup and web remain single-writer. |
| Coordinator | Current task document, task graph, coordination ledger and acceptance decisions | Reviewers are read-only; implementation workers own only their named evidence report. |

A handoff states exact paths/signatures, current edits, expected behavior and the new owner. It does not require user approval for routine implementation already authorized. Necessary shared edits can be applied by the integrator while domain workers code in parallel. Do not run repository-wide formatting, generation or broad tests concurrently with another writer changing their inputs.

## 3. Full task index

**Accepted** means the stated bounded task scope passed review. **Partial** means useful slices passed review but the full task is unchecked. **Not started** may still have earlier drafts; it does not mean the directory is empty. **Conditional** is deliberately excluded from the first-use critical path.

| ID | Outcome | Status | Full acceptance depends on | Model / reasoning |
| --- | --- | --- | --- | --- |
| I01 | Make the current core safe while idle | Accepted | None | GPT-6 Sol / High |
| I02 | Establish host, sign-in and research prerequisites | Accepted | None | GPT-6 Sol / Medium |
| I03 | Collect the independent discovery reference set | Accepted | None | GPT-6 Luna / Medium |
| I04 | Implement durable rounds and atomic allowances | Accepted | I01 | GPT-6 Sol / High |
| I05 | Expose one round authority through API, MCP and workers | Partial | I04 | GPT-6 Sol / High |
| I06 | Finish Codex lifecycle and bounded research tools | Partial | I02, I05 | GPT-6 Sol / High |
| I07 | Finish incremental Lever identity and refresh | Partial | I05 | GPT-6 Sol / High |
| I08 | Discover additional official employer sources | Partial | I06, I07 | GPT-6 Sol / Medium |
| I09 | Reconcile the Jev factual contract and output checks | Accepted | None | GPT-6 Sol / Medium |
| I10 | Wire Jev source, research and shortlist decisions | Partial | I05, I07, I08, I09 | GPT-6 Sol / High |
| I11 | Deliver the brief, round home and contextual correction | Partial | I05, I06 | GPT-6 Sol / Medium |
| I12 | Establish the real private app and isolated runner | Partial | I02, I05, I06 | GPT-6 Sol / High |
| I13 | Independently review round and runner authority | In progress | I05, I06, I07, I10, I11, I12 | GPT-6 Astra / High |
| I14 | Prove real discovery and initial Jev usefulness | Partial | I03, I08, I10, I11, I12, I13 | GPT-6 Sol / Medium |
| I15 | Build one truthful Typst application pack | Partial | I05, I06, I09, I11 | GPT-6 Sol / Medium |
| I16 | Accept the first useful recruitment result | Not started | I14, I15 | GPT-6 Sol / Medium |
| I17 | Add source breadth when observed misses justify it | Conditional | I14 | GPT-6 Sol / Medium |
| I18 | Add lightweight recruiter, referral and contact records | Partial | I15 | GPT-6 Sol / Medium |
| I19 | Connect bounded read-only correspondence | Not started | I12, I18 | GPT-6 Sol / Medium |
| I20 | Implement one supported application delivery route | Partial | I12, I15 | GPT-6 Sol / High |
| I21 | Bind exact approval to bounded delivery | Not started | I15, I20 | GPT-6 Sol / High |
| I22 | Review delivery authority and verify a controlled send | Not started | I21 | GPT-6 Astra / High |
| I23 | Process replies and prepare follow-ups | Not started | I09, I19 | GPT-6 Sol / Medium |
| I24 | Prepare interviews from actual context | Partial | I15, I18 | GPT-6 Sol / Medium |
| I25 | Compare real offer terms without invented certainty | In progress | I01, I09, I18 | GPT-6 Sol / Medium |
| I26 | Verify current-format backup and private operation | Partial | I12, I15 | GPT-6 Sol / Medium |
| I27 | Review the complete journey and publish accurate operating docs | Not started | I16, I18, I19, I22, I23, I24, I25, I26 | GPT-6 Sol / Medium; docs Luna Low |

## 4. Task deliverables and acceptance

### I01 — Make the current core safe while idle

- [x] **Accepted** · GPT-6 Sol / High · Dependencies: none.

**Remaining work:** None in this task. Preserve the reviewed idle-startup and ambiguous qualification behavior during integration.

**Task scope:** Review paused core changes; fix ambiguous pay/hours/location qualification and remove unconditional recruitment dispatch from server startup. Retain current generic profile/storage foundations. Own cmd/server and qualification files/tests only initially; report cross-lane defects rather than taking their files.

**Acceptance:** Focused store/fit/server checks show ambiguous terms cannot become qualified and startup claims no collection, ingestion or Jev work, including when provider configuration is present in a fake setup. Preserve useful drafts; no schema compatibility or silent data reset. Save exact diff, commands and remaining defects in implementation-notes/implementation/I01-core.md.

**Why this model/effort:** Cross-file startup and qualification correction requires Sol High; expensive architecture expansion is not part of this slice.

**Reviewed evidence:** Coordinator reviewed final startup, capability and qualification diff plus focused passing worker checks. Live round authority remains I04/I05.

### I02 — Establish host, sign-in and research prerequisites

- [x] **Accepted** · GPT-6 Sol / Medium · Dependencies: none.

**Remaining work:** Inventory is complete; actual host, account and research verification belong to I06/I12.

**Task scope:** Use the existing runtime task for a bounded read-only inventory of configured app/runner transport, supported account flow and research/tool path. Read configuration presence without exposing values. Identify concrete host/access gaps and the smallest positive/negative checks for I12. Write only implementation-notes/implementation/I02-feasibility.md; do not provision, copy credentials or start model work.

**Acceptance:** Report what exists, what was observed, and what remains unavailable. Separate prerequisite identification from live sign-in/isolation success. State how searches and tool operations will be bounded; never assume desktop tools exist on the runner. A truthful inventory may finish while I12 remains blocked on an external prerequisite.

**Why this model/effort:** Existing context and a narrow inventory fit Sol Medium; escalate only if a concrete isolation/protocol conflict needs stronger judgment.

**Reviewed evidence:** Coordinator read complete inventory and checked config/tool declarations against current code; no live readiness accepted.

### I03 — Collect the independent discovery reference set

- [x] **Accepted** · GPT-6 Luna / Medium · Dependencies: none.

**Remaining work:** Inventory is complete. I14 must capture/refetch complete relevant text, compare stored hashes and review expected judgments before using it as a quality reference.

**Task scope:** Reuse source-research context to collect about 20 dated public employer openings across at least three source families, including sources beyond the Lever seeds. Save exact source text or a traceable retrieval record, URL, source family, date and stated/unknown hours/pay. Include a clearly separated few negative/ambiguous cases and identify 8–12 candidate initial Jev cases; do not claim expected labels are reviewed. Own implementation-notes/implementation/reference-set/ and I03-reference.md only.

**Acceptance:** Every record traces to an accessible employer source or a documented access failure; no fabricated contacts or qualification. Distinguish reference openings from recommended jobs and avoid padding to meet a quota. Coordinator verifies samples; I14 reviews expected judgments and uses the corpus rather than researching it again.

**Why this model/effort:** Bounded evidence capture is repeatable multi-step work for Luna Medium; semantic quality review belongs to Sol during acceptance.

**Reviewed evidence:** Reference inventory accepted after sample corrections. Adyen hash/date independently matched, public Palantir checked; reviewer Ashby retrieval failed. Full descriptions and expected judgments still required in I14.

### I04 — Implement durable rounds and atomic allowances

- [x] **Accepted** · GPT-6 Sol / High · Dependencies: I01.

**Remaining work:** None in the store/controller slice. Integrate through I05; preserve the Stop/Resume generation and idempotency regressions.

**Task scope:** Add the minimal round/attempt/report model, initial schema and controller around existing jobs. Implement Start/Stop/Resume states, input/profile scope, atomic reservations, one active round, stop reasons, partial results and restart reconciliation. Own round/store schema changes under one assigned writer; no full lifecycle tables yet.

**Acceptance:** Race/fake-worker tests prove double Start creates one round, competing claims cannot overspend, retry consumes allowance, Stop fences new work, and Resume reconciles uncertain attempts without replenishing limits. Interrupted restart stays idle. Review the minimal service contract before adapters implement it.

**Why this model/effort:** Atomic state, concurrency and cross-file persistence need Sol High; independent boundary review follows in I13.

**Reviewed evidence:** Generation corrections reviewed and independently race-tested; coordinator corrected/tested expired Start replay. Acceptance limited to round store/controller, I05 integration pending.

### I05 — Expose one round authority through API, MCP and workers

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I04.

**Remaining work:** Fix I13 expired-round slot, production Lever continuation and prepare context scope findings; then pack contextual correction and live integration acceptance. Then implement the missing production contextual-input processing boundary recorded in I11-input-orchestration-gap.md.

**Task scope:** Integrate round capabilities with existing authenticated domain operations, worker claims and MCP. Own OpenAPI/generated contracts and HTTP round controls for this handoff. Distinguish inert external intake, direct owner decisions and delegated mutations; permit only explicitly scoped preference corrections.

**Acceptance:** Cookie/agent/MCP/internal paths cannot bypass actor, resource, operation, revision or allowance checks. Stale tools cannot mutate after Stop. Page load/login/timers/reconnect claim no recruitment. Publish stable DTO/tool boundaries for runtime, collector and web before their integration.

**Why this model/effort:** Authority spans transports and workers, making Sol High appropriate. Do not fork separate business logic per adapter.

**Reviewed evidence:** Committed 2fdfe7d: commissioned discovery/prepare work loops and shared authority/contracts; root full API suite and generated check pass.

### I06 — Finish Codex lifecycle and bounded research tools

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I02, I05.

**Remaining work:** Complete actual host/account/control and cancellation acceptance in I12/I13; offline protocol evidence does not establish isolation.

**Task scope:** Reconcile existing lifecycle/readiness drafts. Implement generation-bound login/disconnect, uncertain dispatch reconciliation, scoped app tools and bounded research access. Keep server credentials outside the runner and no paid API fallback. Own internal/codex*, excluding shared startup and store/schema files unless handed over.

**Acceptance:** Focused protocol/service/runner tests cover late login, quota/readiness gaps, interruption, remote uncertainty and retries without clearing dispatch identity. Show how tool requests—not just turns—consume the round allowance. Fake checks do not count as real login or host containment.

**Why this model/effort:** Uncertain remote lifecycle and cross-file tool integration need Sol High; do not spend Astra on routine implementation.

**Reviewed evidence:** Committed runtime/tool integration is called by commissioned agency outcomes; offline tests pass.

### I07 — Finish incremental Lever identity and refresh

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I05.

**Remaining work:** Verify real commissioned continuation, source reuse and changed-source outcomes in I14 after authority review.

**Task scope:** Reuse source-identity drafts; adapt Lever to commissioned batches with page/intra-page continuation, stable opening/sighting identity, source snapshots, unchanged-input reuse and changed-role refresh. Own internal/collector and ingestion identity files only after explicit store/schema handoff.

**Acceptance:** Unchanged sources skip extraction; changed same-ID content updates one role with evidence history. Source failures do not close roles, uncertain matches are not silently merged, oversized pages respect item allowance, and due times never dispatch work. Run focused collector/store boundary checks.

**Why this model/effort:** Existing identity and runtime coupling justify Sol High; no general crawler or extra connector is required here.

**Reviewed evidence:** Committed agency loop uses bounded collector persistence and re-extracts current new/changed sources.

### I08 — Discover additional official employer sources

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I06, I07.

**Remaining work:** Have Jev choose among bounded neutral discovery leads, then verify a real non-seed official opening in I14. Existing source verification and registration are implemented.

**Task scope:** Implement the smallest verified bounded search/career-link path from I02 alongside Lever. Produce persisted source candidates and exact employer evidence, with operation limits, unsupported-source reporting and continuation. This is candidate acquisition; I10 wires Jev selection. Do not require Ashby/general custom-site coverage to finish it.

**Acceptance:** Deterministic source fixtures exercise discovered official links beyond the seeds, redirected/inaccessible pages and allowance exhaustion without manual board entry. A real non-seed result is verified later in I14. Search snippets alone are not vacancy evidence.

**Why this model/effort:** One verified acquisition path with established boundaries is Sol Medium; escalate if the feasibility record exposes a genuinely different integration problem.

**Reviewed evidence:** Committed 2fdfe7d: exact company-detail provenance, bounded official one-hop reads and atomic supported board registration; fixtures pass.

### I09 — Reconcile the Jev factual contract and output checks

- [x] **Accepted** · GPT-6 Sol / Medium · Dependencies: none.

**Remaining work:** None in the factual helper slice. I10 owns persistence/orchestration and I14 owns real usefulness checks.

**Task scope:** Reuse the screening task to reconcile its mode-bearing ScreeningRubricVersion = 1 with intended v2 scope observations. Batch independent facts, preserve source context and uncertainty, stage dependent support, bound candidate counts/usage and record numerical tolerance. Own internal/jev helper/tests and I09-jev.md only; no service/schema integration yet.

**Acceptance:** Focused fake-provider tests show factual interpretation input omits mode, generic scopes are preserved, missing support remains unresolved, invalid/overflow outputs fail explicitly and raw probabilities are not silently normalised. Existing 0.01 tolerance versus consultation 0.001 is documented, not quietly changed to pass two 0.99 cases. No new live quality claim.

**Why this model/effort:** This is a bounded helper correction from a defined contract: Sol Medium, with escalation if reconciling semantics reveals a new difficult decision.

**Reviewed evidence:** Coordinator inspected helper, staged support, bounds, mode-free request construction, tests and reported package test/vet checks. Real Jev quality and durable integration remain unaccepted.

### I10 — Wire Jev source, research and shortlist decisions

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I05, I07, I08, I09.

**Remaining work:** Complete Jev selection among neutral discovered leads and useful continuation within one finite commissioned outcome; the current engine stops after one extraction. Verify contextual-input organisation and current decision reuse, then real useful discovery. Retained-batch continuity alone is not substantial-round acceptance. See [remaining outcome gap](/Users/vince/Projects/find-income/implementation-notes/implementation/I10-round-outcome-gap.md).

**Task scope:** Integrate durable assessments and bounded candidate selection with the controller and acquisition services. Include organisation, factual scope, lead/research priority and next useful implemented outcome. Persist full request/response, versions, options, source support and usage; treat facts and require/avoid/prefer policy separately.

**Acceptance:** Integration fakes show Jev choices affect selected work while Go enforces scope/arithmetic. No keyword ranking or hidden Codex-only fallback; stale or invalid results stay unresolved. Candidate omission, long text and conflicts are observable. Display saved next-action advice while idle; no planning-only commission.

**Why this model/effort:** Semantic contracts, persistence and orchestration cross packages; use Sol High on this integration rather than on evidence copying.

**Reviewed evidence:** Committed 2fdfe7d: recorded choices, guarded current screening/organisation projection and pack relevance; responsibility evaluation has ten complete cases.

### I11 — Deliver the brief, round home and contextual correction

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I05, I06.

**Remaining work:** Implement reliable commissioned processing for contextual instructions and owner-pasted vacancies/links, including pack correction; distinguish ending paused work from Resume; verify actual runtime journey. Saved-input browser fixtures alone are insufficient.

**Task scope:** Reuse contextual-recovery work. Build the known-answer brief, named Start/Stop/Resume, partial sourced cards, preserved selection and one context-bound instruction/URL/full-text input against stable DTOs. Remove manual source/preference/record forms and inactive navigation. Use stored recommendations supplied by I10 when integrated.

**Acceptance:** Browser fixtures show no repeated known questions, row entry, chat or extra planning click; material unknowns remain visible. Correct/dismiss/select has the right local or campaign scope. Drafts and partial results survive refresh without new work. Check narrow-screen reading and interruption states.

**Why this model/effort:** The approved interaction pattern and stable API make this ordinary Sol Medium UI work. Do not assign Luna to uncertain browser state/debugging.

**Reviewed evidence:** Committed 8c8b66d: pack controls/versions/PDF/citations, relationships and screening; browser fixtures and root check/test/build pass.

### I12 — Establish the real private app and isolated runner

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I02, I05, I06.

**Remaining work:** Select and configure the concrete private app/runner environment, supported login and MCP transport; verify actual model/tool access, containment, shutdown/restart and operation. Prepare deployable configuration/runbook from current code while missing host/account details are resolved. No host, live login or production execution has been verified.

**Task scope:** Complete the concrete host/account prerequisites identified in I02, private transport/MCP reachability, persistent paths and actual supported sign-in. Validate positive work access and denied/absent app-secret/DB access. Coordinate unavoidable owner login input; do not purchase a host or infer an available account.

**Acceptance:** Record actual host/runtime identity, authenticated reachability, isolation checks and sign-in readiness. Readiness and restart trigger no recruitment. Missing host or owner login keeps this task incomplete; it must not prevent independent offline implementation from progressing.

**Why this model/effort:** Real authentication, isolation and recovery configuration are consequential cross-environment work: Sol High, with targeted Astra review in I13.

### I13 — Independently review round and runner authority

- [ ] **In progress** · GPT-6 Astra / High · Dependencies: I05, I06, I07, I10, I11, I12.

**Remaining work:** Review minimal corrections for all four findings, then actual host/account isolation evidence; passing package tests did not cover these production paths.

**Task scope:** Use a fresh bounded reviewer context: requirements, exact diff, interfaces and raw test evidence, without implementer conclusions. Review authority, limits, idle/restart behaviour, runner containment and uncertain dispatch. Read-only; report actionable failures with a reproduction or exact violated invariant.

**Acceptance:** Coordinator resolves findings and the reviewer verifies only changed/problematic boundaries. No live discovery acceptance until consequential boundary issues are resolved. Do not repeat an unlimited repository audit or broadly retest unchanged code.

**Why this model/effort:** This is the concentrated security-sensitive review; Astra High is justified here rather than for every implementation task.

**Reviewed evidence:** Independent review identified native-tool authority exposure, expired paused round deadlock, missing production Lever continuation and prepare round_context scope mismatch. Report finalization underway.

### I14 — Prove real discovery and initial Jev usefulness

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I03, I08, I10, I11, I12, I13.

**Remaining work:** Run actual bounded discovery after I12/I13, verify non-seed sources and useful owner results. Classifier reference calls alone do not accept the dashboard journey.

**Task scope:** Replace the e2e placeholder with actual first-journey coverage and run bounded real discovery. Use the existing reference set; review expected outcomes for 8–12 initial Dutch/English Jev cases, candidate/wording/long-text variants, and roughly two capped search rounds. Record sources reached/missed and owner usefulness without claiming statistical guarantees.

**Acceptance:** One owner action yields real sourced Jev-assessed roles beyond the seeds and an honest report, then remains idle. Verify meaningful partial outcomes and deterministic fault cases. Zero results may pass controls but fail usefulness. Record exact code state, actual usage, interventions and root check/test/build once for the integrated milestone.

**Why this model/effort:** Sol Medium can execute a defined evidence protocol and investigate results; escalate specific ambiguous failures rather than upgrading all review work.

**Reviewed evidence:** Committed c15d85c documents ten completed Jev cases (seven real, three synthetic) and one HTTP529-incomplete case; raw evidence retained.

### I15 — Build one truthful Typst application pack

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I05, I06, I09, I11.

**Remaining work:** Finish contextual correction for an existing pack, review integrated web flow and validate a real selected-role pack in I16.

**Task scope:** Import only the existing approved source assets needed for one pack. Inspect the selected role’s actual route and required questions, use separately reviewed Jev requirement/asset relevance, generate answers and compile/render a Typst PDF. Store immutable sources, answers, destination, attachments and material versions; show review and contextual correction.

**Acceptance:** With a sourced fixture, produce an inspectable PDF/answers with no false tenure or employment labels, no asset-entry form and no sent event. Source every claim; missing required personal facts are asked only after checking existing material. No general library, portfolio rebuild or multi-pack prerequisite. I16 supplies the real selected role.

**Why this model/effort:** Existing Typst/assets and established API make a one-pack vertical slice Sol Medium; implementation can overlap discovery validation instead of waiting for it.

**Reviewed evidence:** Committed 2fdfe7d: selected-role prepare outcome, guarded immutable pack replay, cited current profile/source context and owner-only review/PDF/source routes.

### I16 — Accept the first useful recruitment result

- [ ] **Not started** · GPT-6 Sol / Medium · Dependencies: I14, I15.

**Remaining work:** Validate one worthwhile real discovered lead through an inspectable truthful application pack with the owner.

**Task scope:** Use a real worthwhile discovery result to prepare one application pack. Inspect route requirements read-only, verify generated PDF/answers against approved facts, and walk the owner through selection and review. No employer send is part of this acceptance.

**Acceptance:** Record Start-to-card/report/selected-lead/pack timing, actual usage and owner interventions. The owner judges the lead/pack worth pursuing; material unknowns and unsupported routes remain explicit. Discovery alone or a synthetic PDF does not pass. Review the integrated changes and appropriate checks once.

**Why this model/effort:** A concrete end-to-end acceptance check needs Sol Medium judgment, not another full Astra review.

### I17 — Add source breadth when observed misses justify it

- [ ] **Conditional** · GPT-6 Sol / Medium · Dependencies: I14.

**Remaining work:** Only implement additional source coverage after an observed I14 miss justifies it; otherwise mark deferred with the reason.

**Task scope:** Implement Ashby or another specifically justified source gap using the existing research and same round/identity/cursor contract. Keep this task conditional on recorded coverage misses; it never gates I15/I16.

**Acceptance:** Observed missing source coverage improves under bounded fixtures/live comparison; complete-array endpoints respect item continuation. If current coverage is sufficient, leave this task deferred with evidence instead of building unused adapters.

**Why this model/effort:** A bounded connector over stable interfaces fits Sol Medium; no model can claim a measurable gain without the comparison.

### I18 — Add lightweight recruiter, referral and contact records

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I15.

**Remaining work:** Web relationships committed 8c8b66d; verify actual Codex-owned records and selected-role behavior in the private runtime journey.

**Task scope:** Implement modest origin/route/counterparty/event relationships, pre-vacancy leads and Codex-owned drafting/correction. Link multiple routes to one opportunity. Preserve assignment-specific qualification and current profile; no general CRM.

**Acceptance:** A recruiter introduction without an opening stays an unqualified lead; one direct application and one referral to the same role do not duplicate it. No contact-entry forms, invented relationships, sends or claimed referral advantage.

**Why this model/effort:** Ordinary domain/API/UI integration on existing records uses Sol Medium.

**Reviewed evidence:** Committed 2fdfe7d: sourced relationships and scope-guarded reassociation; no fabricated referral advantage.

### I19 — Connect bounded read-only correspondence

- [ ] **Not started** · GPT-6 Sol / Medium · Dependencies: I12, I18.

**Remaining work:** Implement one real connectable read-only correspondence adapter and commissioned processing inputs, without requiring outbound messages.

**Task scope:** Choose a real connectable owner account and implement authorised bounded inbox/thread reads, pagination, provenance and deduplication. No dependency on an app-originated send. Record inert incoming/due notifications without auto-processing.

**Acceptance:** Fixtures and an authorised read-only connection check handle existing conversations, auth loss and repeated retrieval. A notification or restored account starts no round. An unavailable real account remains an external blocker, not an invented integration success.

**Why this model/effort:** One supported read adapter with defined boundaries is Sol Medium; do not build every provider.

### I20 — Implement one supported application delivery route

- [ ] **Partial** · GPT-6 Sol / High · Dependencies: I12, I15.

**Remaining work:** Integrate exact approval and durable intent in I21; verify a real sender account and bounded receipt lookup; controlled live delivery requires I22 authorization and evidence. No product send endpoint exists yet.

**Task scope:** Implement a verified employer-accepted email or supported candidate-portal route with exact required fields/attachments, bounded tool actions and read-only receipt lookup. Portal delivery must not wait for an email adapter. Exercise fake destination paths before real external action.

**Acceptance:** Route requirements, login/challenge/attestation stops and supported/unsupported states are truthful. Public ATS GET access is not treated as submission permission. Unsupported routes may offer optional prepared handoff, never a claimed send.

**Why this model/effort:** Browser/provider side effects and uncertain outcomes require Sol High.

**Reviewed evidence:** Committed5c3306b: immutable UTF-8 MIME, verified implicit-TLS SMTP and explicit negative/accepted/uncertain outcomes. Root synthetic protocol tests pass, including unexpected positive DATA reply remaining uncertain.

### I21 — Bind exact approval to bounded delivery

- [ ] **Not started** · GPT-6 Sol / High · Dependencies: I15, I20.

**Remaining work:** Implement exact-material approval, bounded delivery intent, per-item outcomes and uncertainty reconciliation.

**Task scope:** Implement one-item and small exact-batch review, immutable approved material/destination, send intent, preflight substantive route checks, per-item outcomes and reconciliation under remaining allowance. New material invalidates affected approval; cosmetic changes alone do not.

**Acceptance:** Fake boundary tests cover stale approval, altered attachments, double clicks, timeout-after-send, restart and no blind retry. Preparation never sends; receipt/verified evidence alone establishes sent. Unknown outcome offers explicit delivery checking without replenishing budget.

**Why this model/effort:** Authority plus external side effects spans storage, UI and adapters; use Sol High and a bounded independent review.

### I22 — Review delivery authority and verify a controlled send

- [ ] **Not started** · GPT-6 Astra / High · Dependencies: I21.

**Remaining work:** Independently review delivery authority and validate a specifically authorized controlled send; code review alone does not establish live delivery.

**Task scope:** Review the exact send/approval/reconciliation diff independently, using requirements and raw evidence without earlier verdicts. Verify only a specifically approved controlled destination or actual reviewed batch; no employer outreach follows merely from this task’s existence.

**Acceptance:** No stale or unapproved material is sent and uncertain outcomes cannot duplicate submission on retry/restart. Coordinator verifies corrections and actual receipt evidence; if no destination is authorised, code review may finish but live delivery acceptance remains pending.

**Why this model/effort:** Astra High is reserved for this second concentrated external-action authority review, not routine message drafting.

### I23 — Process replies and prepare follow-ups

- [ ] **Not started** · GPT-6 Sol / Medium · Dependencies: I09, I19.

**Remaining work:** Implement Jev-supported reply interpretation, next-action advice and Codex follow-up drafts from complete relevant conversation context.

**Task scope:** Add a reply-processing commission using complete relevant thread context, Jev intent/next-action decisions and Codex record updates/drafts. Evaluate this Jev class separately; incorporate important replies into saved next-outcome advice.

**Acceptance:** Existing inbound conversations work without prior app sends. Conflicts remain attributed; due dates never trigger work. Drafting is accepted independently of I21/I22, while sending any follow-up uses their reviewed approval path.

**Why this model/effort:** Bounded interpretation/drafting integration is Sol Medium; do not duplicate the sending engine.

### I24 — Prepare interviews from actual context

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I15, I18.

**Remaining work:** Domain helpers committed7299c54 after root source review and passing interviewprep/jev tests. Integrate commissioned Codex execution, bounded Jev capture/charging, immutable persistence/UI and separately reviewed quality cases; no interview acceptance yet.

**Task scope:** Implement one-interview preparation using employer/context evidence and truthful examples, plus contextual debrief capture. Accept owner-supplied complete interview context without requiring inbox or calendar integration.

**Acceptance:** One commission returns a useful brief/questions with evidence and unknowns. New schedule messages/bookings remain proposals until the supported exact-approval path is available. No invented experience or automatic booking.

**Why this model/effort:** Existing evidence and known outcome boundaries fit Sol Medium.

### I25 — Compare real offer terms without invented certainty

- [ ] **In progress** · GPT-6 Sol / Medium · Dependencies: I01, I09, I18.

**Remaining work:** Implement sourced offer comparison, exact comparable arithmetic and Jev qualitative tradeoffs, preserving unknowns.

**Task scope:** Implement sourced offer intake/comparison, Go exact comparable amounts and Jev qualitative tradeoffs with separate real-case evaluation. Accept complete supplied offers as well as later inbox-derived evidence; no inbox dependency.

**Acceptance:** Unknown/incompatible pay, hours, benefits, arrangements and project economics remain explicit; no hiring probability or silent acceptance/rejection. The owner can understand consequential differences and authorise any later action separately.

**Why this model/effort:** Sol Medium can implement defined comparison rules; escalate a specific unresolved economic/semantic policy rather than inventing one.

### I26 — Verify current-format backup and private operation

- [ ] **Partial** · GPT-6 Sol / Medium · Dependencies: I12, I15.

**Remaining work:** Validate final current schema in I27 and actual private-host recovery/account reconnect/idle behavior after I12.

**Task scope:** Package persistent private app/runner operation and current-format data/asset/pack backup/restore with clear account reconnection boundaries. Extend the current snapshot as later domain tables land; no legacy migration support. Do not wait for interview/offer UI to protect useful stored work.

**Acceptance:** Restore a synthetic current-format backup with referenced assets, verify auth/restart/idle behaviour and secrets exclusion, and record real host operating steps. Final validation in I27 uses the complete current schema. No production credentials in backups/build artifacts.

**Why this model/effort:** Known persistence and operational checks fit Sol Medium; documentation handoff can use Luna after commands are verified.

**Reviewed evidence:** Committed3bcbd42: current-format backup/restore, credential removal/free-page purge, exact pack/assets integrity and preserved board eligibility. Five synthetic tests pass independently.

### I27 — Review the complete journey and publish accurate operating docs

- [ ] **Not started** · GPT-6 Sol / Medium · Dependencies: I16, I18, I19, I22, I23, I24, I25, I26.

**Remaining work:** Run the complete supported journey and final recovery checks, then update operating documentation from observed outcomes.

**Task scope:** Exercise the implemented search→pack→approved controlled delivery→reply→interview/offer journey, narrow-screen review, external-agent scope and final current-format recovery. Record optional I17 disposition separately. Have Luna Low update operating docs from verified commands/outcomes after the technical review, without rerunning tests or changing acceptance.

**Acceptance:** Coordinator reviews exact code state, root checks and real-versus-fixture boundaries. Every control produces a useful bounded outcome and stops. No manual row work, chat, hidden autonomous activity or fictional delivery remains. Docs describe actual capabilities and unresolved external prerequisites.

**Why this model/effort:** Sol Medium performs the defined integrated review; Luna Low handles the final factual documentation pass. No extra full audit of unchanged components.

## 5. Live tasks to reuse

These are existing task IDs, not automatic dispatch commands. All are local. Use IDs to avoid ambiguity; old task titles may describe an earlier slice. Reuse context when relevant and send a fresh bounded assignment. Do not automatically resume any old instruction.

| Lane | Existing live task ID | Next recommended assignment |
| --- | --- | --- |
| Core | `01a0cf13-2c03-73f2-b074-6c73d7120ebe` | I05-A, Sol High |
| Runtime | `01a0ce8e-4c6e-7c93-b63c-1084063ba7c0` | I06-A, Sol High; later I12, then I26 |
| Collection | `01a0ce8e-2459-78d0-9843-ac474acffe5f` | I07-A, Sol High; later I08/I17, Sol Medium |
| Jev | `01a0ce8e-5f14-76f0-b700-ab7a67a8f7a0` | I10-A, Sol High |
| Web | `01a0ce8f-6bf3-7320-8111-75ac34379aa8` | I11 integration, Sol Medium, when DTOs land |
| Reference evidence | `01a0ce8e-743e-7123-9a0c-bbd1a1a59d88` | Targeted I14 source recapture only if needed, Luna Medium |
| Applications/communications | Choose relevant idle task or create when ready | I15/I18/I19/I23/I24/I25 at Sol Medium; I20/I21 at Sol High |
| Independent reviews | Fresh reviewer context when ready | I13 and I22, Astra High; requirements and raw evidence without prior verdicts |

Coordinator task: `01a0ccf3-046c-7303-9dc8-5240dc6f6b52`. Existing runtime implementation authorization is resolved; do not ask the owner again for the same edits. Actual account sign-in is a separate prerequisite.

### Dispatch and review procedure

1. Confirm the task is ready for the named slice and no active task owns its files. Read only the relevant current reports and interfaces.
2. Send the assignment with explicit `model` and `thinking` values, scope, paths, exclusions, acceptance checks and report destination. For reused live tasks, use `send_message_to_thread`; create a new live task only when genuinely needed for the requested coordinated run.
3. Workers report a concise start, material finding/blocker and final handoff to the coordinator. They do not narrate every tool call or poll waiting dependencies.
4. The coordinator does useful independent review/integration work while workers execute. Use grouped `wait_threads` with returned cursors for progress, not repeated full history retrieval. Do not send acknowledgment-only messages that wake finished workers.
5. Review actual code and the relevant checks. Preserve accepted slices, return concrete defects to their owner, and rerun affected checks after correction. Independently review I13/I22 without supplying implementer verdicts.
6. Update this task's status and [task graph](/Users/vince/Projects/find-income/dashboard/docs/task-graph.json), then record actual ownership/dispatch in the [coordination ledger](/Users/vince/Projects/find-income/dashboard-coordination.md). Full acceptance needs all listed dependencies; completion of an isolated fixture is partial acceptance only.
7. Commit each coherent reviewed checkpoint before its next implementation dispatch: inspect the exact diff, stage explicit paths, run the relevant checks, and record the commit plus remaining working-tree status. Coupled core/runtime/contract changes may share a commit when splitting would break the build. Keep credentials, generated runtime data and unrelated changes out; do not use blanket staging. Workers hand off code to the coordinator instead of creating overlapping commits.
8. Dispatch the next ready slice into the free slot without waiting for unrelated tasks. If no useful assignment is ready, let that worker stop.

Use this short assignment template:

```text
Implement <Ixx / slice> using <model>, thinking=<level> in /Users/vince/Projects/find-income.
Read AGENTS.md, the relevant task entry, plan sections and <specific interface/report>.
Current accepted work: <reuse, do not rebuild>. Deliver <concrete remaining outcome>.
You own <exact paths>. Exclude <shared/other worker paths>. Coordinate <named handoff>.
Acceptance: <specific behavior, meaningful tests and fixture/live distinction>.
Write evidence to implementation-notes/implementation/<task report>. Do not edit task status.
Send material updates and final files/checks/limitations to coordinator
01a0ccf3-046c-7303-9dc8-5240dc6f6b52. Stop at this handoff; do not start another task.
```

## 6. Verification, prerequisites and product constraints

Run focused package/behavior checks during implementation. At a coherent integration checkpoint, the designated integrator runs `pnpm check`, `pnpm test`, and `pnpm build` from `/Users/vince/Projects/find-income/dashboard`; use relevant Go race tests for changed concurrent state. `pnpm e2e` builds the web bundle and runs synthetic HTTP/browser interaction smoke. It catches Prepare/Stop/Resume/version/retry/mobile regressions but does not count as live API, Codex, host or full I14 journey acceptance. Do not run all suites after each document or UI text edit, or have every worker repeat the same root checks.

Concrete unresolved inputs are the private app/runner host and access, supported owner sign-in, one actually usable bounded research capability, a correspondence account for I19, and a supported/authorized delivery destination for I20–I22. Investigate available configuration without exposing secrets; ask only for genuinely missing owner information. Host/login gaps do not block offline code or fixtures. Provisioning, live account actions and external delivery are recorded separately from synthetic tests.

The implementation remains a personal recruitment agency: substantial owner-started rounds with Stop/Resume, automatic evidence collection and Codex-owned records, Jev semantic organization/choices, exact Go arithmetic/authority, minimal contextual input and no chat. Reuse known personal facts and approved career assets; no frontend/PHP emphasis inferred from historical work, invented employment or fabricated specialist experience. Keep Vite+, native Go/Turborepo, SQLite, Typst and the supported Codex account path. Do not add Flue, an extra CLI, backward compatibility, legacy backfills or upgrade machinery.

New difficult design choices follow [AGENTS.md](/Users/vince/Projects/find-income/AGENTS.md): supply researched evidence to Jev SystemOne, make three fresh semantically equivalent consultations with all explanatory prose rewritten, save requests/responses, examine disagreements and treat agreement as advice. The existing consultation is not rerun for routine task allocation. No new product architecture decision is made by this scheduling document.
