> **RETIRED 26 Sep 2026 — superseded by [product-vision.md](product-vision.md), [connected-prototype-ux-parity-tasks.md](connected-prototype-ux-parity-tasks.md) and [p0-retain-cut-ledger.md](p0-retain-cut-ledger.md). The seven-step manual-handoff scope removed application approval/sending, delivery, employer-site autofill, and downstream replies/interviews/offers. Kept for history only; do not implement from this document.**

# Autonomous recruitment implementation plan

Date: 24 September 2026. Status: ready for implementation planning handoff; no replacement functionality has been implemented by this document. Baseline: `df808dd`, after removal of the rejected discovery pipeline.

Historical execution backlog: [ordered tasks and concurrent implementation lanes](/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-tasks.md). Its Codex runner steps are superseded by the current [recruitment workflow plan](recruitment-workflow-implementation-plan.md); preserve this document as product intent and historical architecture evidence.

**Current runtime direction supersedes the provider references below (25 September 2026):** The owner selected the locally installed Muse Code CLI on their MacBook as the first generative runtime. Muse Contributor handles the entire generalized public vacancy-discovery loop: choosing sources, using general role/region/skill queries, retrieving public APIs/company pages/web results, and saving public vacancy evidence. Keep the owner's name, CV, private profile facts, answers and application details out of Contributor prompts and MCP tool returns; Meta terms §6.2 prohibit personal/sensitive/confidential input to Contributor. Jev performs personalized per-job classification and selects saved reasons. Muse Standard via the same local CLI is reserved only for grounded required-answer, tailored CV/message drafting in Prepare applications and an explicit owner-requested rewrite. It has zero calls during goals/brief correction, catalog authoring, discovery, classification, selected-job checks and Answer. Contributor may author general reason choices once per brief from non-identifying criteria; bind actual private requirements locally/Jev and reuse saved assessments. The owner reports about 50 practical hours/week on Contributor versus about one on Standard in their account and says this is subscription CLI use, not API-key pay-as-you-go. Verify the Contributor subscription lane before live public discovery and Standard entitlement before later Prepare work, both without a model call. Codex/OpenRouter are deferred alternatives, not a first-path selector. No live call or spend is authorized. In the older specification below, “Codex” denotes the intended recruitment-agent capability/role where possible; it does not override the selected Muse executable or the two-tier data boundary.

The result must be a personal recruitment agency that independently finds and investigates opportunities. Muse Contributor chooses public sources, searches, API requests and the order of public work. Jev judges public listing evidence against the owner’s private criteria server-side. The application remembers research, preserves evidence, maintains records and enforces the owner's run limits.

**Discovery design guard:** The Contributor vacancy-search loop must freely choose and adapt public sources, company career pages, public APIs and queries, including abandoning unproductive paths. Do not hard-code a source universe, fixed source menu or job results to reduce model calls. Reduce calls after collection with Jev classification, saved reason reuse, captured evidence and deterministic state handling.

The [owner report](/Users/vince/Projects/find-income/dashboard/docs/discovery-rapport-2026-09-24.md) explains the failure this plan must prevent. Later instructions in this conversation supersede its historical suggestion to register new sources as fenced connectors: visiting an unfamiliar source must not require registration, an adapter, a code change or a new approval step.

This is a sequence for building the software. It is not a sequence the recruiter must follow during research.

## 1. Product contract and scope

The owner supplies recruitment intent, preferences, materials and corrections conversationally. The app uses verified saved facts and choices with deterministic versioning; Jev handles supported choices. Contributor receives only generalized non-identifying search criteria. If new private free-text/CV meaning cannot be represented, record a scoped intake gap for owner review; do not silently call Standard outside Prepare or block public discovery. Login, explicit decisions and list filters remain ordinary controls.

An owner can start a bounded research run, watch real activity, steer it, stop it, and resume with prior knowledge intact. The agent may discover a new source, follow a useful link, inspect a public API, change a query, compare evidence, abandon an unproductive direction and save multiple opportunities within the allowance. No requested source, search phrase or search parameter is preselected by the engine.

The first release includes free public-source research; persistent cross-run research memory; evidence before opportunity creation; dynamic Jev assessment; employer and vacancy identity; batched record ownership; reliable run controls; and integration with the existing recruitment dashboard. It also includes operating and recovery checks necessary to trust those capabilities.

Existing packs, interviews, relationships, replies, offers and delivery remain connected to the resulting opportunity records. They are not grounds for rebuilding the entire product. Research does not grant authority to contact employers, submit applications, book meetings or purchase services. Those actions retain their existing explicit owner decisions.

Do not build provider-specific boards, connector registration, seeded search terms, fixed source/role menus, mandatory classify/search/verify phases, one-page or one-role funnels, a second opportunity inbox that must be manually processed, a Pi fork, an arbitrary SQL interface, or compatibility paths for the removed prototype. Scheduling recurring searches is a later feature unless the owner commissions it; loading a page or reading status never starts work.

## 2. Architecture and responsibilities

Use the existing Go/SQLite API and React dashboard, through the installed local Muse Code CLI for the selected first runtime. Use Contributor for the entire generalized public vacancy-discovery loop and public selected-job/question evidence; Standard is application-preparation-only; Jev stays separate. The app-owned controller and retrieval tools preserve authority, evidence and run limits. Codex/OpenRouter remain deferred alternatives, not a prerequisite selector. Keep the existing single active owner run initially; permit bounded concurrent research operations inside it. Storage must nevertheless remain correct under two concurrent callers and successive runs. A multi-agent scheduler is unnecessary for the first release.

| Component | Responsibility |
| --- | --- |
| Muse recruitment agent | Contributor selects sources and general public search queries and operates browser/API/retrieval tools. Standard is reserved for grounded application preparation and explicit rewrite. It frames work for separate Jev classification; deterministic application code owns records. |
| Jev | Classify supplied alternatives or score supplied evidence, including source usefulness, query overlap, role relevance and uncertain identity. It neither researches nor generates missing factual explanations. |
| Research execution | Execute general search, fetch, browser and networked-code operations; reserve allowance and shared work; capture authentic results; apply deadlines and cancellation. |
| Application store | Preserve intent, observations, evidence, identities, judgments, records, audit history and resumable state across conversations and runs. |
| Run supervisor | Bind current authority, start/continue/interrupt Codex, record events, enforce limits, reconcile uncertain work, and publish truthful progress. |
| Dashboard | Present the brief, real work, outcomes, evidence, uncertainty and run controls; accept owner messages and decisions. |

The authority boundary is the owner's account/campaign, the kind of action and the current run allowance. It must allow discovery of new resources. It must not whitelist particular companies, URLs, search terms, page numbers or API argument values as a prerequisite to research.

Reuse revision checks, idempotent attempts, captured Jev exchanges, immutable records and generation fencing where they satisfy that contract. Change their current interfaces directly when they encode the old restrictions. A transport credential alone must not grant record authority. The CLI/tool session must enforce a separate public-only MCP DTO and tool allowlist, not rely on prompt intent. Existing `context_read` and research tools can expose owner facts/preferences, so do not expose them to Contributor. Keep owner profile/CV/answers, private Jev inputs/results and raw private session history server-side; project only generalized criteria and public source evidence. Separate Contributor and Standard sessions, histories, checkpoints and workspaces; never downgrade a private Standard transcript to Contributor. Audit inherited host MCP/plugins/native tools/hooks/user rules/memory/background observers because session `config.mcpServers` is additive and MCP tools run outside the shell sandbox. Keep raw source receipts locally while Contributor freely chooses public sources. Never extract credentials or bypass the installed harness.

## 3. Resolve the runtime uncertainty first

Implement a bounded capability proof before committing to the research execution binding. Use the installed local Muse Code 1.3.0 CLI, separate public/private sessions, a disposable test store and instrumented fixture sources. A small live public-source check belongs to a later explicitly authorized exercise. Minimal tools used by the proof should exercise the intended interfaces and be retained where useful, not become another throwaway product architecture.

Record the binary version/digest, effective configuration, tool availability, protocol events, persisted artifacts, calls made, interruptions and resumed state. Test these cases:

1. Codex selects a query and follows an unseeded public result. It can inspect a page rendered in a browser and call a discovered public API with parameters it selected. Prove a general research path for each; do not assume a desktop browser feature exists on a headless runner merely because a flag exists.
2. Interrupt after an observed result and after a saved record. Resume the saved conversation and recover its evidence and record IDs. Separately disconnect around a request and demonstrate an honest uncertain state without blindly replaying it.
3. Stop during an in-flight operation. Confirm that further dispatch and business writes are blocked, owned processes settle or are contained, and late observations cannot revive the run.
4. Keep two browser/API operations in flight together. Demonstrate separate reservations, correlated results/captures, shared allowance enforcement and cancellation of both on Stop before selecting the research backend.

Produce a capability table for search, HTTP/API, browser and executable research: pre-dispatch interception, actual-result capture, cancellation, usage reporting, freshness reuse, and recovery. A listed tool or a completed-turn notification does not satisfy this proof.

The current wrapper creates a new thread each time and drops most item activity. The pinned `codex-cli 0.153.4` also rejected `thread/items/list` in an earlier live probe. Extend the wrapper to use supported resume/history/event mechanisms. If a newer supported binary is needed, change the prototype's pin, generated protocol types and acceptance checks directly; keep one baseline rather than a compatibility matrix.

Current official Codex hooks exclude hosted WebSearch. Tool events also do not expose every HTTP request made inside a shell program. Use the following decision rule:

- Where a native path demonstrably provides the needed control and capture, reuse it with instrumentation.
- Otherwise provide general research tools/backends through MCP that own request dispatch and capture. A search backend or browser runtime is infrastructure; it must not define a list of job sources. Choose an available backend during this proof and record its account, cost and capture behavior. Do not assume an unconfigured paid search service is available.
- Networked code runs in an instrumented execution environment with captured input/output and mediated outbound access. If per-request observation is required, verify it at that boundary. Logging the shell command is insufficient. The agent must still be able to construct arbitrary public API requests and parsers.
- Do not enable a parallel unobserved route that bypasses controls advertised as enforced. Do not restore source-specific adapters to fill a coverage gap. If a necessary capability remains impossible with supported Codex integration, document the failing requirement and compare Pi against that concrete case before switching.

This proof selects the mechanism. The requirements for open research, evidence, memory and bounded execution remain unchanged. A failed probe produces a specific corrective task; it does not justify an open-ended research project.

General API access accepts Codex-selected public URLs, parameters, body and supported content types, including read-only queries implemented with POST. Do not mistake a fixed GET-only helper for general API access. Validate action effects and execution bounds without embedding a website-specific method menu. The proof establishes protocol and backend behavior; production memory, identity and UI guarantees are completed and tested in the later packages.

## 4. Application tool contracts

Expose the public projection below to Contributor; the prior private `context_read` remains outside its tool inventory. Any domain capability exposed to Contributor must apply the same public DTO/allowlist boundary; private Jev work remains server-side. General research tools are execution infrastructure alongside them. Contributor can call only the public capabilities in any useful order, revisit them, and process batches; Jev assessment remains server-side. No engine dictates the next research step.

All calls have validated schemas, bounded payloads and stable IDs. Mutation and external-call boundaries validate current run authority, generation, deadline, allowance and retry identity. Authenticate from the active execution context; never let model-supplied account IDs select another account. Return actionable typed outcomes rather than a generic authority error. Page large results and provide artifact references for long content.

| Capability | Inputs and operations | Returned information |
| --- | --- | --- |
| `public_context_read` (new Contributor projection) | General non-identifying role/region/skill criteria, public research subject, pagination and public checkpoint only. | Public-only source/query context, run limits and public evidence references. Existing `context_read` returns owner facts/preferences and must remain inaccessible to Contributor. Private Jev inputs/results and private session history never appear in this DTO. |
| `public_research_memory` | Public-source investigation, claims/checkpoints and source/query/filter context only. | Public reusable results, claims, evidence refs and next steps; never private owner data or Standard history. |
| `public_evidence_capture` | Trusted public-source receipt/artifact reference and requested public excerpt/link. | Public projection of capture ID, URL/time/status and excerpt. Keep raw receipts locally; owner-supplied material and private artifacts never enter Contributor tools. |
| `jev_assess` (server only) | App supplies private owner brief, public listing evidence and saved reason choices to Jev. | Save classification, chosen reason IDs, uncertainty and usage server-side. Do not return private Jev inputs/results to Contributor. |
| `opportunity_match` | Captured role/employer attributes and known identifiers; optional additional candidates or evidence. | Exact established matches, broader possible matches and distinguishing evidence, or an unresolved/new result. Identity remains checked again when saving. |
| `public_records_save` | Bounded public vacancy/company evidence batches and stable request key; private assessment binding happens server-side. | Public record/evidence IDs and safe conflict states only; no private Jev results or owner facts. Multiple batches may run within the allowance. |

General research execution automatically performs the exact-request memory claim/reservation and capture. Codex does not have to remember a ceremonial sequence of memory calls before each click. Explicit memory operations are for investigative intent, semantic overlap, conclusions, priorities and resumption. Automatically captured facts and model-authored notes remain distinguishable.

An exact successful replay returns the previous result. Reusing an idempotency key with a different logical payload conflicts. Run credentials rotate, but their secret value must not become part of the business-operation digest and make a legitimate resumed replay look like a new request. An uncertain dispatched operation is reconciled before deciding whether any new attempt is justified.

Suggested shared outcomes include `reused`, `claimed_elsewhere`, `stale`, `revision_conflict`, `identity_ambiguous`, `capture_incomplete`, `budget_exhausted`, `stopped`, `rate_limited` and `outcome_uncertain`, with the relevant saved references and permitted next action. They describe facts, not prescribed source choices.

## 5. Research memory and provenance

Use SQLite and private content-addressed artifact storage. Do not introduce a vector service, search cluster, provider registry or workflow framework before evidence shows the need. Add the following logical entities, reusing existing run/attempt/Jev tables rather than duplicating them:

| Entity | Required facts and invariants |
| --- | --- |
| `research_requests` | Account-scoped exact request fingerprint, request descriptor, most recent attempt/capture, freshness and negative-result expiry, lease owner/generation/expiry. At most one live claim for the same request. |
| `research_observations` | Immutable request attempt, actual query/URL/parameters, timing, outcome, provenance receipt and capture reference. Preserve empty, blocked, failed, rate-limited and uncertain outcomes as well as useful results. |
| `research_notes` | Investigation intent, source usefulness, coverage, semantic overlaps, conclusions, evidence refs and outstanding work. Associate with a run and brief version without hiding it from later runs. |
| `source_captures` | Immutable bounded body/DOM/text artifact reference, digest, original and final URL, redirects where visible, retrieval time, media type/status, completeness and executor identity. A role ID is optional. |
| `identity_decisions` | Employer/role observation, candidates and their revisions, evidence, same/new/unresolved decision, applicable Jev assessment, and decision basis. |
| `entity_identity_keys` | Established employer or vacancy identity keys and source aliases linked to canonical records, with evidence. Unique only for strong identities within the appropriate namespace. |

The exact request fingerprint includes operation, actual endpoint/search backend, method, body, parameters/filters, relevant locale/session context and pagination. Normalize only equivalences the system can justify. Preserve meaningful parameter values, repeated parameters, paths and ordering where significant. A tracking-parameter assumption must not silently equate different requests. Do not include secrets in human-readable keys or logs.

Reuse captured read results without pretending to replay a browser's cookies or navigation state. Stateful browser actions require a matching execution context or a deliberate new operation; Codex can consume earlier evidence directly when navigation is unnecessary. Cache scope and freshness must make that distinction visible.

Separate source freshness from assessment freshness. An unchanged captured job page may support reassessment against a changed brief without being fetched again. Query results, empty results and temporary failures have explicit expiry. Refresh records why it was needed; no-result or exhausted work must not permanently blacklist a source. Pagination state and coverage are observations, never authority fences.

Claim, reuse and lease takeover are transactional. Expired leases permit recovery after reconciliation; stale workers cannot update the active request state. Content received late can be retained as an immutable late observation, while remaining unable to commit opportunity changes or release someone else's lease. Do not automatically retry a timed-out external operation whose outcome is unknown.

Capture exact source material before extracting claims. Record whether evidence is a fetched response, rendered DOM, search result, owner statement or model note. Search snippets are discovery evidence and cannot silently become complete vacancy descriptions. Preserve source conflicts, missing facts and truncation. A hash establishes integrity of captured bytes, not truth of an employer claim.

Store original captures outside the model-writable workspace and the public web root. The execution service issues receipts; `evidence_capture` resolves them server-side. Excerpts bind to a specific immutable version and offsets or exact quoted spans. Redaction/display projections do not alter that original binding. Resource bounds produce explicit incomplete captures or artifact pagination rather than hidden truncation.

Semantic research reuse uses indexed prior requests/notes and Jev when overlap needs judgment. Exact request deduplication is enforced; semantic no-repeat behavior is evaluated on representative cases and carries uncertainty. Neither this plan nor the UI may claim mathematically perfect equivalence across every differently phrased search.

## 6. Employer and vacancy identity; atomic writes

Replace the current advisory URL/company-title lookup with indexed candidate retrieval and transactional identity resolution. Start with normalized known URLs, established employer domains/aliases, requisition identifiers in issuer namespaces, locations, teams, dates and lexical/content signals. Full-text retrieval can broaden candidates; similar text or the same title is never a unique identity key.

Codex supplies evidence distinguishing a cross-post from another vacancy. Jev assesses semantic cases from those alternatives and captures. Keep source claims, Codex's explanation and Jev's returned decision separate. Do not interpret a fixed confidence threshold as proof that two jobs are identical. If the source reuses a URL or requisition number for a materially different opening, retain the historical sighting and resolve the new identity explicitly.

Unresolved discoveries stay in research memory with their evidence and next question. Codex can investigate them later. They do not become a duplicate qualified opportunity, an automatic merge, or a mandatory owner cleanup queue. A factual opportunity may be saved before fit is known; label it unassessed rather than requiring a fixed qualification stage or inventing suitability.

`records_save` commits a bounded batch all-or-nothing. Inside one transaction: validate current authority and revisions; resolve stable replay; validate capture/assessment bindings; recheck identities and concurrent match candidates; reserve record allowance; write companies, opportunities, evidence links, aliases, assessments and audit events; persist the result. A conflict returns specific item details so Codex can reassess or retry an independent subset. Transport batch limits must not become a per-run result quota.

Do not call Jev or fetch a page while holding the write transaction. If the candidate set changed while an assessment was in flight, return a conflict and use the new evidence. A match tool result is advice until commit. A historical capture remains usable after its research lease ends; saving it requires current write authority and intact provenance, not an artificially live fetch lease.

For changed postings, append a sighting and versioned claims instead of overwriting the only source body. Preserve authoritative owner corrections and mark conflicting new source information for reassessment. The existing opportunity ID must work directly with screening, packs, relationships, interview, reply and offer features.

## 7. Jev during runs

Extend the existing validated SystemOne client and exchange recorder. Do not route research decisions through fixed `next_outcome` menus or hardcoded source/query labels. Contributor may propose public-source and vacancy-identity alternatives from public captures. The application frames any private owner-brief comparison and Jev request server-side; private Jev inputs and results never return to Contributor. Suitable public questions include source coverage, query overlap and whether two public listings describe one opening.

Frame hypotheses as hypotheses. An invented query can be a proposed option; an invented employer statement cannot be evidence. Include an appropriate unresolved/none alternative when the evidence does not force a choice. Save the full question, alternatives, context/brief version, exact request and response, returned model, usage and source references. Validate references, answer IDs, distributions and response completeness before applying results.

Reuse an assessment only for the same relevant evidence, question/alternatives, brief/rubric and model inputs. Changing a profile or source invalidates only dependent judgments, not the captured source history. Failures remain explicit; Codex can continue independent research or report a partial result rather than inventing a classification. Any retry is a separately reserved attempt with a reason, never an unbounded loop.

The AGENTS.md requirement for three fully rewritten consultations applies to difficult design decisions. It does not impose three provider calls on every runtime judgment. Use the saved design consultations already completed; make a new triple consultation only when a material unresolved design choice requires it.

## 8. Runs, limits, steering and recovery

Reuse the persisted round lifecycle internally; use the word run in the product. Keep queued, running, awaiting input, stopping, paused, completed and failed as execution states. They do not describe investigation phases. Successful completion means saved outcomes and an evidence-backed report; a completed Muse turn alone does not establish success.

The supervisor persists dispatch intent, local Muse session/turn IDs, a current control generation, ordered activity references and a resumable checkpoint. Continue the existing conversation when supported. When its context is unavailable, start a new conversation using a persisted checkpoint and retrieved shared memory, and show that recovery happened. Do not depend exclusively on model prose, an in-memory transcript or an unverified history API.

The checkpoint records brief version, current investigations/claims, evidence and saved record IDs, unresolved attempts, remaining allowance and next useful work. Codex may propose next steps; the supervisor owns durable state. Persist tool outcomes as they occur so a crash before a model-authored summary does not erase completed work. A research run can span multiple bounded Muse turns under one allowance; remove the single-turn ten-minute ceiling as the definition of the whole job, while retaining bounded per-operation deadlines.

Owner steering is a durable message with a revision and acknowledgement state. Clarifications can enter the active conversation through a verified steering path. A correction that changes applicable facts or permissions rotates authority and makes affected pending saves reassess current versions. If live steering is unsupported, stop/reconcile the turn and continue with the saved message; do not silently ignore it. Budget increases and changed contact authority cannot be inferred from a research page or model suggestion.

Stop first fences app tools and revokes dispatch/write authority, unqueues pending work, stops background activity, interrupts the Muse turn and waits for owned execution cleanup. No new request or business commit may begin after that boundary. Report stopping until execution is confirmed settled; a completed external request cannot be undone. Keep late evidence and uncertainty without treating either as permission to continue. Resume reconciles in-flight work, rotates credentials/generation, restores the checkpoint and uses the remaining allowance. It never silently grants another full budget.

Use pre-dispatch reservations for every controlled external call and parallel operation. Count actual redirects/subrequests and bytes separately where relevant; distinguish a user-visible research action from the browser's many network requests. Per-origin throttling and bounded retries apply generically and must permit redirects and unfamiliar public sites. Public-network address checks cover redirects and DNS changes; scripts/browser traffic cannot reach app/admin/private services through an alternate path.

Budget presentation distinguishes enforced limits, reserved in-flight usage, observed usage and unknown usage. For the first single adaptive Contributor discovery exercise, target about 30–45 minutes with a proposed 45-minute hard stop, finite model-step/turn and retrieval/Jev limits, progress checkpoints, and no automatic repeat or extension. The exact step/request ceilings need no-model-call readiness evidence before authorization. These are initial operational settings to validate and adjust, not source/phase/result quotas or authorization to spend during planning. Configure per-request and per-run byte/network-request limits from the runtime proof, including browser subresources, before a live run.

Token/currency limits are hard only where the provider/execution mechanism can enforce a reserved upper bound. Otherwise show observed/unknown usage and enforce the available time, request and turn bounds. Do not relabel a post-call token target or subscription quota as a hard euro cap. The Muse CLI subscription does not expose a meaningful per-run dollar meter here; do not claim a dollar hard stop for it. Any separately billed Jev, retrieval or other service needs its own verified cost/limit and explicit owner authorization. All retries and Jev calls draw from the same owner-visible run ledger.

Persist an activity journal with deduplicated event IDs/cursors and projections for the UI. Reconnect serves stored events and live updates without double-counting. Event overload or a disconnected recorder becomes an explicit coverage/recovery failure. Browser reloads, status reads, login and reconnect may recover observations but never commission new recruitment work. Resuming after owner Stop remains an owner action; crash recovery may continue only within the still-active original commission and confirmed authority.

## 9. Product and API changes

Extend the home view with conversational recruitment intent and a readable proposed brief. Populate it from existing preferences and owner messages. Show finite allowance, material unknowns and what Start commissions. Do not ask the owner to fill source, query, company or vacancy fields. Existing authorization to perform a bounded run should not trigger repeated permission prompts for ordinary public reads or record saves.

During work, show actual queries, visited sources, investigations, saved/reused opportunities, failed paths, meaningful Jev judgments, evidence freshness, remaining allowance and stopping/blocking reasons. Derive counts from persisted records. Distinguish new roles, updates, cross-posts, observations and unresolved findings. Never display a fixed percentage or fabricated phase progress. Show concise explanations, with raw evidence/activity available on demand; keep credentials and raw protocol payloads out of browser DTOs.

Use one active-run surface for Start, conversational steering, Stop and Resume. Preserve owner messages and acknowledgment across reloads. Replace required record-entry chores with saved verified facts/choices and deterministic updates where possible. New private free-text/CV extraction that cannot be represented remains a scoped intake gap for owner review; Standard is not invoked outside Prepare. Existing conversational intake/correction textareas already fit and should be reused, not deleted merely because they are inputs. Authentication and search/filter controls remain.

Add typed API operations for research commissioning, steering, activity pagination/streaming, observation/capture reads, identity explanations and run reports. Reuse appropriate round stop/resume/status routes; do not expose the low-level protocol or unrestricted database access. Document mutations and errors in OpenAPI and regenerate the Go and TypeScript clients. Reads remain side-effect-free.

Saved roles appear immediately in the existing opportunity views with cited facts, fit/unknowns and source history. Existing pack/interview/reply/offer actions consume those records without copying them into another funnel. The final run report explains useful outcomes, what was searched, what was reused, remaining uncertainty, budget/stop reasons and suggested next work. “No suitable opportunities found within this run” is a valid honest result; activity volume alone is not success.

## 10. Implementation work packages

Each package produces a reviewable working change with its acceptance evidence. Shared schema, contracts and integration files have one writer at a time. Parallelize only after those contracts are settled. Do not generate another machine-readable phase graph for the recruiter's behavior.

| ID | Deliverable and principal touchpoints | Dependencies | Completion evidence |
| --- | --- | --- | --- |
| P0 | Runtime proof and execution binding. `apps/api/internal/codex/{client.go,turn.go,turn_methods.go,methods.go}`, `codexservice/round_execution.go`, `codexrunner`, `ops/i12` runtime/config/probes. | None | Verified search/browser/API access; capture coverage table; saved-thread continuation; Stop and disconnect evidence; concrete research backend and selected binary. |
| P1 | Shared data and tool/API contracts. Add research/capture/identity entities in `store/migrations/001_initial.sql`; define tool DTOs, errors and OpenAPI projections. | P0 interface findings | Reviewed schema/invariants; generated contracts agree; fixture sources cover missing/conflicting/duplicate data. |
| P2 | Persistent memory, immutable capture storage and generic research execution. New focused `internal/research` package plus store modules; adapt public-address checks from `codexservice/source_links_fetch.go`, replacing the saved-company-only interface. | P1 | Automatic exact-request reuse, capture-before-role, lease races/expiry, stale refresh, real provenance and source-independent operation. |
| P3 | Dynamic Jev and identity matching. Extend `jev`, `jevservice`, `store/jev_attempts.go`; add broad identity retrieval/decision storage; replace advisory-only matching in `store/{companies.go,opportunities.go,ingestion_match.go}` where applicable. | P1; P2 capture refs | Dynamic alternatives, full exchange provenance, abstention, cross-post/separate-role distinction and current-version checks. |
| P4 | Six application tools and atomic batch writes. `codexservice/tools.go`, new handlers, `store/{round_mutations.go,round_attempts.go,round_tool_capabilities.go,evidence.go}`, existing CRUD/audit helpers. | P2 and P3 | Tool-call integration from real Codex; multiple saved roles; race-safe identity; exact replay; conflicting batch rollback; historical captures remain usable. |
| P5 | Autonomous research commissioning and supervision. `agency/engine.go` plus a focused research runner; `rounds/service.go`, run lifecycle/remote dispatch/reconciliation/capability store, `codexservice/{readiness.go,round_execution.go}`. Replace narrow research instructions and saved-resource scope restrictions. | P1; integrate P2–P4 | One full run with model-selected sources/queries, continued turns, checkpoint/recovery, steering, remaining-budget resume and no late writes after Stop. |
| P6 | Dashboard and downstream handoff. `httpapi/{handler.go,rounds.go}` plus research endpoints; OpenAPI/generated clients; `apps/web/src/{agency-home.tsx,briefing.tsx,owner-instruction.tsx,api.ts,opportunities.tsx,evidence-panel.tsx}` and relevant existing panels. | P1; complete against P4/P5 | Owner starts/steers/stops/resumes conversationally; true event/cost views; source-backed roles reach existing workflows; no required row-entry forms. |
| P7 | End-to-end acceptance and quality evaluation. New research integration fixtures and `scripts/ui-smoke/research-smoke.mjs`; extend existing suites only where behavior changed. | P2–P6 | Acceptance matrix below passes; one bounded real-source canary supports usefulness claims; limitations recorded separately from fixture results. |
| P8 | Current-format operations and handoff. Update `ops/i12`, `ops/i26/{recovery.py,test_recovery.py}`, runtime docs, README and remaining-work. | P7 | Private fresh installation, capture-aware backup/restore, target-host cancellation/isolation, correct readiness and idle behavior, operational evidence and known limitations. |

P2 and the Jev/matching portion of P3 can proceed in parallel after P1. Supervision work in P5 can develop against the settled tool contract while P4 is implemented. P6 can build against contract fixtures concurrently, but fixture success is not end-to-end acceptance. P4/P5 integration and P7 are the convergence points. Do not distribute shared schema or generated-contract edits to multiple writers.

At each checkpoint, review the diff against the product contract, run focused checks, resolve material failures and record evidence. Commit coherent explicit paths when implementation is commissioned. Broaden to a full suite at integration milestones; do not rerun unchanged reviews indefinitely. New Jev design consultation is for a real unresolved decision, not every implementation detail.

## 11. Acceptance matrix

| Scenario | Required result |
| --- | --- |
| Unfamiliar source | A source absent from code, fixtures and owner input is found and used through general capabilities without adding an adapter or source registration. Include a browser-rendered page and a discovered public API in the capability proof. |
| Autonomous pivot | An unsuccessful query/source causes a materially different query or source chosen from evidence by Codex/Jev, without an engine-selected keyword or page. |
| More than one result | One sufficiently funded run can save several independently evidenced roles and update another; no one-page/one-role limit or staged-candidate quota. |
| Fresh exact repeat | The same effective request across runs or concurrent callers reuses a result or shares a claim, without another external dispatch or charge for that dispatch. |
| Semantic repeat | Differently phrased overlapping investigations retrieve prior coverage; Jev can recommend reuse or explain the need for new coverage through supplied alternatives. Uncertainty is retained. |
| Refresh and failure | Stale, updated, empty, 404, blocked and rate-limited responses remain distinguishable; justified refresh works and failures do not brick a source permanently. |
| Cross-post and distinct opening | Different URLs/titles for one vacancy resolve to one role with multiple sightings. Two genuinely distinct same-title openings remain separate. Include reused URLs/requisition IDs and conflicting sources. |
| Save race and replay | Two store handles saving the same established identity yield one record; changed match candidates cause a conflict; identical replay returns the same IDs; changed payload conflicts; failed batch leaves no partial business writes. |
| Authentic evidence | Pre-role capture succeeds; forged receipts, altered artifacts, unsupported quotes and complete-vacancy claims based only on snippets fail; unknown and contradictory facts remain visible. |
| Jev outage/invalid result | Captures and independent work survive; no fabricated assessment; retries have explicit new reservations; prior responses cannot be rebound to different evidence or a different brief. |
| Stop and uncertain dispatch | Stop fences new work and saves; in-flight observations are reconciled; an unknown remote outcome stays unknown; no automatic replay or false success. Verify owned-process cleanup on the actual target host. |
| Resume and steering | Restart/reload recovers durable state without commissioning new work; an authorized continuation avoids repeated completed work and preserves remaining limits. Owner corrections fence affected pending writes and stale dependent assessments while preserving audited, versioned records already saved. Corrections become acknowledged input. |
| Budget and concurrency | Concurrent dispatch cannot exceed enforced reservations; retries/subrequests are accounted for; unknown usage is never zero; exhausted allowance prevents new work and produces a truthful report. |
| Public-source freedom with isolation | Arbitrary public destinations and redirects work; private-network/credential access and research-triggered employer contact remain outside authority, including from scripts and page instructions. |
| Product flow | No structured company/role/source form is required. Activity derives from real events. Saved opportunities feed retained recruitment features. Status reads and page loads dispatch no work. |
| Role suitability | Explicit requirements, conflicting evidence and missing information remain distinct. Pay/hours/location and responsibility judgments cite the relevant source with its units/context. Silence does not satisfy a hard preference, and unassessed roles are not presented as qualified. |
| Recovery | Restore current-format data and captures consistently, invalidate active credentials/leases, retain uncertain attempts, and launch no work automatically from the backup. |

Use deterministic fixture tests for races, Stop ordering, transaction rollback, malformed evidence, failures, cache/freshness and state transitions. Use a small frozen labeled set for semantic cases: actual cross-posts, same-title distinct jobs, revised/reused postings, renamed employers, ambiguous evidence and brief changes. Include hard negatives and unseen sources; success on one board or memorized fixture set is insufficient. Review citations and identity decisions, not just returned counts. Classifier probability does not replace evaluation.

For the live canary, use the owner's current brief without silently editing preferences or seeding convenient terms. Start with the finite reviewed allowance. Report unique useful roles, correctly reused work, duplicate/false-merge findings, citation support, unsuccessful paths, usage and unknowns. Do not prescribe a minimum real vacancy yield: availability changes. The controlled multi-result case proves throughput; the live run establishes actual source access and evidence quality. Owner usefulness is a separate final assessment and cannot be inferred from green tests.

Run focused tests during implementation, then the repository's `pnpm check`, `pnpm test`, `pnpm build`, and one complete relevant `pnpm e2e` integration pass. Add race-enabled Go checks for new shared-store/claim/save/Stop concurrency, and run the changed current-format recovery tests. Regenerate contracts and verify generated output. Run selected-host boundary, transport, cleanup, research and idle probes once the implementation is ready. Re-run a passed check only after a relevant change, failure or new concern.

## 12. Operations, completion and decision record

Change the initial schema directly. Use a fresh private data directory for the changed prototype; preserve existing private data without attempting migrations, backfills or automatic resets. Keep runtime, application and research-capture storage permissions explicit. Pin one verified runtime and its required execution dependencies. Update tool readiness lists and native access checks together so readiness means the actual required research paths work.

Backups must include referenced immutable captures and a manifest consistent with the SQLite snapshot. Retained evidence must remain retrievable for saved roles; deleting transient browser files cannot invalidate it. Bound transient logs/cache/artifacts and surface storage exhaustion. Retain compact request/identity history needed to prevent repeat work. Review the current recovery scrubber for new lease/token/event fields, invalidate live authority on restore, and test uncertain in-flight work. Keep raw private research and career artifacts out of Git and public static assets.

The implementation is complete when P0–P8 evidence is recorded, the acceptance cases pass, a bounded real research run demonstrates independent source selection with authentic captures and saved outcomes, and the dashboard accurately exposes progress, limits and uncertainty. Documentation must distinguish implemented behavior, fixture verification, live verification and owner usefulness. No “done” claim may rely solely on removing the old code, compiling the replacement or producing a polished activity view.

This plan authorizes no live deployment or recruitment execution by itself. Prepare concrete artifacts and checks during implementation; honor existing authorization for later actions and request any genuinely missing live allowance or deployment decision only when that action is ready to review. Never make routine research-source choices into owner permission chores.

The design record contains three sets of fresh Jev consultations, with full-request prose variations, preflight semantic checks and raw responses:

- [Tool capabilities and capture options](/Users/vince/Projects/find-income/implementation-notes/jev/research-tools-2026-09-24/review.md): focused tools favored; capture strategy not unanimous.
- [Codex versus Pi/custom harness](/Users/vince/Projects/find-income/implementation-notes/jev/harness-choice-2026-09-24/review.md): extend Codex favored, with weaker confidence under one wording.
- [Execution and identity boundaries](/Users/vince/Projects/find-income/implementation-notes/jev/implementation-boundaries-2026-09-24/review.md): evidenced identity consistently favored; one execution answer deferred to a runtime proof. P0 resolves that factual uncertainty rather than treating a majority as evidence of working infrastructure.

Primary runtime references: [Codex App Server](https://learn.chatgpt.com/docs/app-server), [Codex hook coverage](https://learn.chatgpt.com/docs/hooks#tool-coverage), and the conditional alternative's [Pi SDK](https://pi.dev/docs/latest/sdk) and [extension API](https://pi.dev/docs/latest/extensions). These describe available interfaces; the selected executable and operating environment still need the P0 proof.
