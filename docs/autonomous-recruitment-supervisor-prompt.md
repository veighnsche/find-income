# Autonomous recruitment implementation supervisor prompt

Paste the prompt below into the implementation task. It instructs that supervisor to create the goal and begin implementation; saving this document does not start either.

## Prompt

You are the implementation supervisor for the autonomous recruitment replacement in `/Users/vince/Projects/find-income/dashboard`. Implement the approved plan through completion. Own lane A yourself and coordinate lanes B, C and D with three implementation subagents. You are one of the four active slots, not a fifth observer. Work on lane A while the other lanes make progress.

### Establish the persistent goal

Use the actual goal tools exposed by this session. Call `get_goal` first. If there is no unfinished goal, call `create_goal` with the following objective and omit `token_budget` because I have not specified one:

> Implement and verify the complete autonomous recruitment replacement in /Users/vince/Projects/find-income/dashboard according to docs/autonomous-recruitment-implementation-plan.md and every task T01–T32 in docs/autonomous-recruitment-tasks.md. Coordinate four implementation lanes including the supervisor, complete the integrated product, recovery, selected-host and bounded live acceptance checks, fix material failures, and deliver the documented working result with traceable completion evidence.

If the same implementation goal is already unfinished, continue it instead of creating a duplicate. If an unrelated unfinished goal prevents creation, do not overwrite it or mark it complete; report the conflict and resolve it with the user. Follow the available tools' status and continuation rules. If goal tools are unavailable, report that limitation explicitly instead of claiming a persistent goal exists.

The supervisor alone owns the goal lifecycle. Workers do not create separate goals. Keep this goal active through ordinary progress, context compaction and successive implementation turns. Do not stop after planning, scaffolding, one lane, a milestone, or a passing fixture. Do not ask whether to continue already-authorized implementation. Continue until the completion criteria below are met, subject to actual platform limits and any later user instruction.

Only mark the goal `complete` when the implementation and required verification are actually complete. Use `paused` only at the user's explicit request. Use `blocked` only under the goal tool's conditions: the same external blocking condition has recurred for at least three consecutive goal turns and no meaningful independent work remains. Track those occurrences honestly; do not manufacture them by polling. A user resumption starts a fresh blocked audit. Before any unavoidable interruption, leave a precise checkpoint. Never claim success to close an incomplete goal or to accommodate a resource limit. Do not create an automation or separate user-owned tasks as a substitute for the goal.

### Read the governing material and current state

Read these before implementation, following their links when relevant:

- `/Users/vince/Projects/find-income/AGENTS.md` and any applicable nested instructions.
- `/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-implementation-plan.md` — product, architecture and acceptance requirements.
- `/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-tasks.md` — authoritative task dependencies, concurrent scheduling and file ownership.
- `/Users/vince/Projects/find-income/dashboard/docs/discovery-rapport-2026-09-24.md` — why the rejected system failed; later plan decisions supersede its historical connector-registration suggestion.
- Current README, remaining-work, repository status, relevant implementation and existing evidence. If progress already exists, resume from verified state rather than resetting the checklist.

The documented baseline is `df808dd`, after removal of the rejected discovery pipeline; inspect current HEAD rather than assuming it is unchanged. Preserve existing user changes, the removal, and the planning documents. Reconcile actual files with task status. A checked box or worker assertion alone is not proof.

Use the plan as the product specification and the task list as the implementation schedule. Do not silently reduce acceptance requirements to make completion easier. Resolve routine implementation details yourself. For a genuinely new difficult design decision, follow AGENTS.md: supply all evidence to Jev SystemOne, make three semantically equivalent consultations with all explanatory prose freshly rewritten, save requests/responses, investigate disagreements, and treat agreement as advice. Reuse the existing consultations linked from the plan. Scheduling and routine coding do not require another design ritual, and these consultations do not impose three Jev calls on every product operation.

### Preserve the intended product

Build a personal recruitment agency in which Codex independently chooses sources, queries, API parameters and research order, and owns the records. Jev classifies the supplied evidence and alternatives; it cannot research or invent missing facts.

Keep the existing Go/SQLite API, React dashboard and Codex App Server integration. Prove the actual runtime path first, then implement the selected native or instrumented general research binding and small application supervisor. Do not build a Pi fork unless a demonstrated unmet requirement triggers the comparison specified in the plan. These four implementation lanes are a development arrangement, not a new multi-agent research scheduler inside the product.

Provide the six app capabilities `context_read`, `research_memory`, `evidence_capture`, `jev_assess`, `opportunity_match` and `records_save`, alongside general search/browser/API/executable research. Preserve automatic exact-request claims, authentic immutable captures, persistent research memory, evidence-based identity, transactional batch saves, allowance enforcement, Stop fencing and remaining-budget resume.

Do not restore fixed sources, source adapters, source registration prerequisites, seeded query menus, mandatory research phases, one-role funnels, manual database-entry forms or compatibility paths. Unknown public sources must be researchable without changing the app. Keep uncertainty and incomplete evidence visible. Status reads and startup must remain inert. Retain the downstream recruitment workflows and distinguish research authority from permission to contact employers or submit applications.

### Dispatch the four lanes

Use the collaboration tools available in this session, not separate sidebar tasks. Inspect existing subagents before starting replacements; reuse suitable idle workers. Apply `/Users/vince/.codex/skills/model-selection/SKILL.md` for every dispatch and use supported model/effort choices appropriate to the bounded work. Keep at most four active implementation slots including yourself. Workers must not independently spawn extra agents or broaden their assignments.

| Lane | Owner and responsibility | Initial assignment after T01 |
| --- | --- | --- |
| A | You: contracts, HTTP API, run UI, composition, integrated verification and handoff. | T05 |
| B | Runtime worker: actual Codex proof, events, authority, supervision, MCP registration and runner operations. | T02 |
| C | Research/storage worker: initial schema, claims, captures, memory, execution and recovery. | T03 |
| D | Judgment/records worker: acceptance corpus, Jev, identity, atomic records and opportunity/evidence views. | T04 |

Complete T01 first, then dispatch T02, T03 and T04 while you implement T05. Follow the current checklist's explicit `Depends on` fields. Its rows and lane queues are not synchronized waves or additional dependencies. Start each ready task as soon as its actual prerequisites have the required evidence. Assign workers bounded ready tasks and then send follow-ups; do not tell a worker to implement an entire lane regardless of dependencies.

Prioritize the initial chain T02 → T06 → T07 → T11 → T16 → T23 and the T12 authority handoff. Let runtime, judgment and API work proceed against the concrete T06 interfaces with appropriate test doubles. T18 needs T12's real authority operations, not completion of all T13 supervisor work. T19 does not wait for T15. Start T23 backend composition when T10/T16/T17/T18 are ready even if T15 UI work remains; checkpoint or explicitly hand off the UI work. T27 later requires T15/T19/T23.

Start T20 recovery work after T11 and T22 packaging/probes after T16/T17. After T23, run T24–T27 concurrently when their individual prerequisites are satisfied. Follow with T28, T29, T30, T31 and T32. Adjust capacity toward the measured longest unfinished chain. Do not invent elapsed-time guarantees, serialize independent tasks by numeric order, or keep workers busy with unnecessary reviews. An idle worker may take disjoint ready work after you record an explicit ownership transfer.

Every worker assignment must specify:

- Task ID, concrete deliverable and verified dependency checkpoint.
- Relevant requirements and evidence, exact writable files, shared interfaces, and files owned by other lanes.
- Focused validation, isolated test resources and required handoff artifacts.
- What to do if an interface or prerequisite is missing: report the precise need promptly; do not fabricate a production fallback or edit another owner's files.
- A return report containing changed files, interface changes, commands/results, evidence paths, limitations and remaining blockers. Workers propose completion; you verify and update the shared checklist.

### Enforce ownership and publish early handoffs

Use the file-owner table in the task list. A alone owns shared OpenAPI/generated clients, HTTP composition, common run UI, shared browser-suite registration, root configuration and shared documentation. C alone owns the initial schema. B owns Codex lifecycle, supervisor, MCP registry and runner operations. D owns Jev, identity/save transactions and the assigned opportunity/evidence panels. New lane-local files inherit the same owner.

Resolve the `RoundOperationCost` definition/constants handoff into B's cost module before T12/T18 concurrent edits. Coordinate all other shared-file changes through their owner. Never run competing generators, edit generated code by hand, or allow workers to revert each other's changes. A consolidates dependency and lockfile changes.

Use the shared integration checkout with explicit file ownership unless isolation is useful. If worktrees are needed, synchronize the exact contract/schema checkpoints into dependent worktrees; do not implement against stale interfaces. Give concurrent tests separate temporary databases, artifact directories and ports. Keep one operator for the selected live host and one owner for the full suite/build.

Publish narrow, usable checkpoints promptly. T06 must settle actual receipt/capture, authority/allowance, event/checkpoint, identity/save and API interfaces with representative fixtures. Test doubles enable development; they never count as working production registration, integrated acceptance or live proof. When a contract changes, coordinate the owner and affected consumers as one coherent change.

### Maintain a durable execution record

Create or continue `/Users/vince/Projects/find-income/dashboard/docs/autonomous-recruitment-progress.md`. Keep the shared task checklist current without renumbering away failed or unfinished requirements. Only the supervisor edits shared progress/checklist documents; workers save evidence in task-specific directories under `/Users/vince/Projects/find-income/implementation-notes/autonomous-recruitment/` and report the paths.

Record each task's status (`pending`, `running`, `blocked` or `done`), lane/agent, dependency checkpoint, exact file ownership, implementation checkpoint, validation commands/results, evidence paths, limitations and next action. Task-level `blocked` is a scheduling fact and does not automatically change the persistent goal status. Keep a compact current dispatch table, integration checkpoint, unresolved contract changes and selected-host/canary state. Capture artifact or revision identity for tests, including a reproducible description of uncommitted changes where applicable. Keep credentials and private source material out of public documentation.

On each continuation or context compaction, read the goal, progress/checklist, current repository state and agent status. Reconcile them before redispatching. Do not restart finished probes, recreate active workers, reset budgets or lose pending user questions. Update the record at meaningful handoffs, failures and accepted completions rather than logging every command. Give concise progress updates about completed work, the current bottleneck and the next resolving action.

### Verify the complete implementation

Accept task completion only after its deliverable, focused checks and handoff evidence exist. Inspect the relevant changes and results economically; a worker's “done” message is not sufficient. Fix material findings in the owning lane and rerun affected checks. Do not add an unchanged serial review loop after every task.

The following gates are mandatory:

1. **T02 runtime proof:** exercise the actual selected binary/backend, unseeded search, rendered browsing, discovered read-only POST APIs, executable research, authentic capture, two concurrent operations, Stop, continuation and uncertain disconnect. Record unsupported behavior and actual control/capture/cost coverage. A tool listing, shell-command log or completed turn alone is insufficient.
2. **T23 real composition:** connect real executor, capture/memory, Jev, identity, records, supervisor and HTTP; demonstrate multiple records, cross-post reuse, a meaningful research pivot, evidence and continuation. No placeholder may advertise production readiness.
3. **T24–T27 acceptance:** verify concurrency/failure/Stop/recovery, current-format restore using composed data, integrated identity/evidence and the complete relevant product/browser journey. Keep fixture, integrated-runtime and live-host evidence distinct.
4. **T28 release candidate:** run the required `pnpm check`, `pnpm test`, `pnpm build`, generated-output checks and remaining relevant race/recovery checks once under the designated owner. Browser evidence must apply to the candidate. Revalidate affected behavior after fixes; do not repeat broad checks without a reason.
5. **T29 selected host:** prove the exact candidate's isolation, transport, process cleanup, captures, readiness, limits and inert idle behavior on the actual host. A prepared installation script is not a passed host check.
6. **T30–T31 live acceptance:** commission one bounded real recruitment canary with the existing owner brief and an explicit finite allowance; audit its actual claims, identities, suitability/unknowns, memory/reuse and report usefulness. Record observed and unknown spend honestly. Other lanes observe the same run rather than commissioning duplicates. Do not invent a minimum vacancy yield or contact employers.
7. **T32 handoff:** update the task list, README, remaining-work and runtime/recovery documentation with the implemented result, precise evidence, candidate identity and known limits. Distinguish your usefulness assessment from actual owner feedback; record pending subjective feedback honestly without pretending it was received.

Use existing authorization and configured access. Discover relevant host/access constraints early, and complete the concrete candidate and all independent work before requesting any genuinely missing final decision. If an essential credential, host choice, approval or finite live allowance is missing, request exactly that information while continuing unaffected work. Never invent credentials, spending authorization or live evidence. A blocked required live gate remains unfinished; do not silently downgrade it to a fixture or describe the goal as complete.

### Finish

Before completion, reconcile all T01–T32 deliverables against their evidence and the plan's acceptance criteria. Resolve material failures and ensure required checks apply to the final candidate. Stop or release owned test processes and workers safely, preserving the intended running product and unrelated user processes.

Only then call `update_goal` with `status: "complete"`. Give a concise final handoff with what now works, verification and live-result evidence links, how to run/use/recover the app, and any remaining nonblocking limitations or owner feedback. If work is externally blocked or execution is interrupted, leave an honest incomplete checkpoint instead.

Begin now: inspect/create the goal, read the governing documents and current state, complete or reconcile T01, then dispatch the first ready work across all four lanes. This is an implementation instruction; carry it through to the verified result.

## Reference note

The goal lifecycle above follows the `get_goal`, `create_goal` and `update_goal` tool contracts available in the authoring session; the receiving supervisor must obey its actual available tool contracts. The use of bounded independent assignments and coordinated shared-file edits follows [OpenAI's multi-agent guidance](https://developers.openai.com/api/docs/guides/agents-api/multi-agent). This prompt uses the existing Codex session's tools and does not require introducing the Agents API or a new harness into the application.
