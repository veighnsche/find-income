# Remaining work — handoff to the next agent

Updated 24 September 2026 from Git status, saved implementation reports and the stopped workers' final handoffs. **Implementation is paused at the owner's request to preserve weekly tokens for finding a job.** This document is a handoff, not permission to restart workers, tests, provider calls or deployment. Resume only when the owner instructs it.

## Start here

The product is a private personal recruitment agency: the owner commissions a useful, finite round; Codex performs the work and owns record extraction/correction; Jev makes evidence-supported semantic choices; results and suggested next actions are saved; another owner action starts the next round. No endless scraping, chat interface or manual row-entry workflow.

Retain the existing Go backend, Vite+/Turborepo monorepo, Typst artifacts, Codex subscription sign-in and Jev SystemOne integration. Do not replace the language or harness as an incidental implementation choice.

The first useful unfinished milestone is **real discovery → worthwhile selected opening → truthful application pack** (I16). Finish this before optional source expansion or lifecycle polish. The full remaining lifecycle is still listed below; reaching I16 does not complete the whole project.

Read [AGENTS.md](/Users/vince/Projects/find-income/AGENTS.md). Preserve its five one-line rules, especially direct prototype changes with **no backwards compatibility**. For genuinely difficult new design decisions, follow its three independently reworded Jev consultations with supplied evidence and saved requests/responses. Routine corrections do not require new consultations.

Owner context: Amsterdam; 32 hours/week; at least €4,500 gross/month for the actual hours; income needed urgently. Experienced SWE targeting backend or platform work without frontend duties. Do not reintroduce frontend/PHP positioning, fabricated freelance employment, unsupported experience or hiring probabilities. Verify claims against the approved career assets rather than hard-coding this person's details into generic classification logic.

## Exact repository and paused work

- Workspace: `/Users/vince/Projects/find-income` — **not a Git repository**.
- Git repository: `/Users/vince/Projects/find-income/dashboard`, branch `main`.
- Last implementation checkpoint on main: `605e172` (recovery controls); documentation checkpoint before this handoff: `2685b1a`. Stage A integrated 24 September 2026: `0602dcf` + `94c1cd0` (I10 backend) and `bf2f684` (I10/I11 interface).
- Root implementation goal is paused. Both implementation workers reported stopped. Do not assume a task's old title/status means work should resume.
- No deployment, live dashboard Codex login, real inbox connection or employer send has been accepted.

### Backend ready for integration review

**Integrated 24 September 2026 as `0602dcf` + `94c1cd0`; the notes below are the preserved pre-integration record.** The clean isolated checkout `/private/tmp/find-income-i10-advice` ends at **`8cee343c047251f0e44d937d62215509139140ff`**. Branch **`handoff/i10-cross-outcome`** preserves that commit in the repository, independently of the temporary checkout.

Apply only these two commits, in order, after checking the correction against the known findings:

```sh
# Reference commands for the next authorised implementation session; not executed by this handoff.
git -C /Users/vince/Projects/find-income/dashboard cherry-pick 149c616f4c7a7ba3d261a72051fb4f01e0081a95
git -C /Users/vince/Projects/find-income/dashboard cherry-pick 8cee343c047251f0e44d937d62215509139140ff
```

Do **not** cherry-pick their ancestor `09bfc01`: it duplicates interview backend already integrated on main as `c85a617`. Do not merge all isolated ancestry. Preserve the separate uncommitted web changes during integration.

`149c616` adds captured, charged next-action advice for process input, preparation, offers, delivery and interviews; latest-result reads support `outcome=all` and recorded failed delivery. `8cee343` corrects three reproduced defects: actual tools rejecting the new exact two-resource scopes, nil Jev provider panicking during result finalization, and complete captured advice failing actual Resume or consuming a futile reconciliation charge. It removes the obsolete one-resource accommodations. Action/target contract shapes did not change in the correction.

**Existing evidence:** the backend worker reports full `go test ./... -count=1` and the independent `TestReviewI10` overlay passing after `8cee343`; staged whitespace check passed; no live provider calls. Root read the main recovery corrections before the commit, but final integration review is unfinished. Do not claim the original authority-review report accepted `8cee343`: it records the failures of `149c616`. Reuse the passing reproductions and inspect the correction; a new broad audit is unnecessary.

### Uncommitted interface work to preserve and finish

**Integrated 24 September 2026 as `bf2f684`; the notes below are the preserved pre-integration record.** These eight files were modified on main, unstaged and uncommitted:

```text
apps/web/src/agency-home.tsx
apps/web/src/api.ts
apps/web/src/home-recommendation.ts
apps/web/src/interview-panel.tsx
apps/web/src/offer-comparison-panel.tsx
apps/web/src/opportunities.tsx
scripts/ui-smoke/fixture.mjs
scripts/ui-smoke/recommendation-smoke.mjs
```

They add all-outcome saved-result recovery and five review actions: `review_result`, `review_comparison`, `review_delivery`, `review_interview`, `review_debrief`, alongside the existing four actions. They navigate to exact saved records and show stale/unavailable advice. They must not start work or send from a read/navigation action.

**Verification boundary:** focused recommendation browser checks passed before the final small assertion/effect changes; lint/TypeScript and whitespace checks passed after those changes. The full browser suite was deliberately not repeated. The worker stopped before backend integration/root review. Its report's general passing statement must be read with this final-handoff qualification. Finish the relevant integration checks once, then commit these explicit paths; do not discard or rewrite the slice from scratch.

## Ordered implementation checklist

### A. Finish the paused integration — first

- [x] **A1 — Integrate I10 backend. Sol / High.** Done 24 September 2026 as `0602dcf` (149c616) + `94c1cd0` (8cee343); ancestor `09bfc01` not cherry-picked. Verified: full `go test ./... -count=1` green; all four `TestReviewI10` reproductions (campaign-scope tools, nil provider, both captured-advice Resume paths) pass on main via remapped overlay; focused Outcome/Recommendation suites pass; `git diff --check`, Go `check-generated.sh` and contracts `check:generated` pass. No live provider calls.
- [x] **A2 — Finish I10/I11 interface integration. Sol / Medium.** Done 24 September 2026 as `bf2f684` (the eight preserved paths, no rewrite). Verified: client target shapes match the integrated backend contract (`review_result` omitted zero revision, comparison revision 1 + input SHA, delivery recorded count, interview/debrief `updatedAt`); `pnpm --filter @jobseek/web lint` (vp check + tsc) passes; focused Chromium recommendation smoke passes (outcome=all recovery, nine actions, exact destinations, failed delivery, currentness refusal, unresolved advice, read-only reload with zero POSTs). Full browser suite deliberately not repeated. Offer arithmetic, pending-request identity and 605e172 rejection/recovery behavior preserved.
- [x] **A3 — Reconcile tracking. Coordinator or Luna / Low.** Done 24 September 2026: commits and checks recorded here and in `implementation-tasks.md`, `task-graph.json`, `task-list-check.json`. I10/I11 stay partial until real usefulness/authenticated operation is demonstrated.

### B. Reach the first useful job-search result — critical path

- [ ] **B1 — Resolve concrete deployment inputs (I12). Sol / High.** Obtain the app host, different dedicated runner VM, private address/DNS/TLS/access path and permission for those concrete configuration changes. Existing `infra`/`linux` inventory is only a candidate inventory. Prefer prepared deployment scripts; do not invent infrastructure, purchase services or copy existing personal credentials.
- [ ] **B2 — Deploy and authenticate the prepared runtime (I06/I12). Sol / High.** Install pinned artifacts, Typst and approved private career assets; configure private app↔runner transport and MCP; establish the separate dashboard login and supported owner ChatGPT device login. Keep Jev credentials server-side. Verify the actual account's available model/effort and research tools; no paid API fallback. Follow the existing I12 runbook, not a new hosting design.
- [ ] **B3 — Verify actual boundaries (I05/I06/I12/I13). Sol / High; Astra / Medium only for a concrete unresolved authority issue.** Run the selected-host positive/negative tool and containment checks, exact native-profile/canary/rollout checks, SSH/cgroup cleanup on normal close/client death/supervisor death, idle startup/restart and authenticated capability checks. Login/reload/timers must create no recruitment work. Wrong scope/stale capabilities fail after Stop; uncertain remote work is not silently retried. Keep the isolation assertion false until the documented gates pass. Earlier offline reviews are already complete; review only new changes or failed host evidence.
- [ ] **B4 — Run bounded real discovery (I07/I08/I10/I14). Sol / Medium.** Use an owner-commissioned bounded round, then a continuation round only if useful/authorised. Verify a real official non-seed opening, persisted source text/provenance, unchanged-source reuse, changed-source refresh without duplicate roles, cursor/allowance continuation and honest inaccessible/unknown states. Jev must actually influence supported choices with saved evidence. Reuse the existing reference set; do not repeat the entire classifier study. Log actual usage, worthwhile results and missed coverage. Zero results do not establish usefulness.
- [ ] **B5 — Prepare and review a real pack (I11/I15/I16). Sol / Medium.** Select a worthwhile real lead; inspect its route/required questions; generate a cited Typst PDF and answers from approved facts; exercise whole-context correction if needed. Check backend/platform positioning, truthful employment and unknown personal details. Record elapsed time, usage and owner interventions. The owner must find the lead and pack worth pursuing. This milestone does not authorise sending.

### C. Finish the remaining agency lifecycle after the first useful pack

- [ ] **C1 — Validate contacts/referrals (I18). Sol / Medium.** Exercise existing Codex-owned relationship creation/correction in the private runtime. Verify a recruiter introduction without a vacancy remains an unqualified lead, multiple routes do not duplicate one role, and no referral advantage/relationship is invented. Existing record/UI implementation is present; avoid building a general CRM.
- [ ] **C2 — Implement one read-only inbox connection (I19). Sol / Medium.** Obtain a real connectable owner account and read scope. Implement its bounded thread reads, pagination, provenance, deduplication, auth-loss behavior and commissioned processing inputs. Existing conversations must work without a prior app send. Notifications/due times/reconnect remain inert. Do not implement every provider or call a fixture a real connection.
- [ ] **C3 — Verify the implemented delivery route (I20/I21/I22). Sol / High.** Configure one usable sender/route, and obtain exact owner approval for a concrete reviewed destination/material set before a live send. Existing SMTP/MIME, review, intent, replay and Stop controls are implemented and reviewed. Exercise only the outstanding real-account/controlled-send acceptance; maintain exact approval binding and uncertain outcome handling. SMTP acceptance is not employer receipt. Receipt lookup is currently unsupported; do not fabricate it or resend to discover the outcome. If the chosen real role needs an unsupported portal, explicitly implement only that justified route or present the limitation.
- [ ] **C4 — Implement reply processing and drafts (I23). Sol / Medium.** Add a bounded commission using complete relevant correspondence, Jev intent/next-action classification, Codex-owned updates and follow-up drafts. Evaluate this new Jev class with supplied evidence; include important replies in saved next-action advice. Attribute conflicting claims and unknown dates. Drafting works independently of outbound delivery; sending reuses exact reviewed approval. No automatic processing on a notification/due date.
- [ ] **C5 — Validate interview usefulness (I24). Sol / Medium.** Use actual supplied interview context to produce a useful cited brief, questions and truthful examples; then exercise whole-note owner-reported debrief and exact record recovery. Implementation already exists. No invented experience, employer decision, automatic booking or messages. Inbox/calendar integration is not required for supplied-context preparation.
- [ ] **C6 — Validate offer usefulness (I25). Sol / Medium.** Use real supplied terms when available; verify extraction, source citations, exact Go pay/hour calculations, explicitly incomparable or missing terms and Jev qualitative tradeoffs. Include actual-hours economics rather than silently substituting full-time salary. Implementation and exact numeric display already exist. No inferred hiring probability, acceptance or negotiation. Record lack of real offer material as an acceptance prerequisite rather than inventing it.
- [ ] **C7 — Verify private operations (I26). Sol / Medium.** On the selected host, exercise current-format backup/restore, asset availability, fresh authentication/reconnect and idle restart. Interrupted delivery remains uncertain; no restored work auto-runs. Preserve secrets exclusion. Seven current-schema synthetic tests already pass; rerun them only if the schema/recovery code changes or integration raises a concrete issue.
- [ ] **C8 — Complete owner walkthrough and operating docs (I27). Sol / Medium; factual docs Luna / Low.** Exercise real search→pack→specifically approved delivery→reply→interview/offer paths where actual inputs exist, plus external-agent access and narrow-screen recovery. Review each outstanding result against the acceptance matrix below. Run appropriate integrated checks once after the final relevant changes. Document actual deployment/operation and remaining limitations. No fictional completion when live inputs or receipt evidence are missing.
- [ ] **C9 — Decide extra source coverage (I17, conditional). Sol / Medium only if needed.** Use observed I14 misses to justify one specific extra connector. Otherwise explicitly defer it with the reason. It never gates the first useful pack and does not require building all ATS adapters.

## Full I01–I27 disposition

This accounts for every original task. Detailed scope remains in [implementation-tasks.md](implementation-tasks.md); this handoff supersedes its historical dispatch directions and stale remaining-work wording.

| ID | Current status | Remaining work / completion evidence |
| --- | --- | --- |
| I01 | Accepted bounded slice | Preserve reviewed idle startup and factual qualification; do not reimplement. |
| I02 | Accepted inventory | Inventory is complete; live setup is I12, not proof from tool presence. |
| I03 | Accepted reference inventory | Reuse dated evidence; refresh only sources needed for actual current discovery. |
| I04 | Accepted bounded core | Preserve durable round/atomic allowance behavior; final runtime integration is below. |
| I05 | Partial | B3: live API/MCP/worker authority, Stop fencing and no idle work. |
| I06 | Partial | B2–B3: real ChatGPT login, research/tool access, cancellation and isolation. |
| I07 | Partial | B4: real incremental collection, identity/reuse/refresh/continuation. |
| I08 | Partial | B4: real official opening beyond seed employers. |
| I09 | Accepted helper slice | Preserve factual/output checks; new semantic classes get their own evidence. |
| I10 | Partial | A1–A2 integrated (`0602dcf`, `94c1cd0`, `bf2f684`); B4, C4: verify real decisions/usefulness. |
| I11 | Partial | A2 integrated (`bf2f684`); B5: authenticated minimal-input journey. |
| I12 | Partial | B1–B3: actual selected-host deployment, private transport and login. |
| I13 | Partial | B3: actual host containment/authority evidence; offline discovery/native findings already rechecked. |
| I14 | Partial | B4: real bounded discovery and worthwhile results, beyond isolated Jev reference calls. |
| I15 | Partial | B5: real cited pack and contextual correction; correction implementation already exists. |
| I16 | Not started | B5: owner accepts one real lead plus useful truthful pack. |
| I17 | Conditional | C9: justified coverage improvement or documented deferral. |
| I18 | Partial | C1: actual Codex-owned contacts/routes without duplicates or invented advantage. |
| I19 | Not started | C2: one real read-only correspondence adapter and authorised connection. |
| I20 | Partial | C3: real usable sender/route; maintain unsupported receipt/portal limits. |
| I21 | Partial | C3: exact approved controlled live delivery using existing controls. |
| I22 | Partial | C3: actual authorised outcome evidence; existing code review is complete. |
| I23 | Not started | C4: reply classification, record updates, saved advice and drafts. |
| I24 | Partial | C5: actual interview brief/debrief usefulness and authenticated operation. |
| I25 | Partial | C6: actual offer extraction/comparison usefulness and authenticated operation. |
| I26 | Partial | C7: real host restore/reconnect/idle checks; local current-schema checks passed. |
| I27 | Partial | C8: full supported live journey and accurate final operating docs. |

## Missing inputs and authorization boundaries

Collect these when they become necessary; do not repeatedly ask questions already answered in the owner's session.

1. Concrete private app/runner placement, networking/DNS/TLS/access and deployment configuration authorization. Earlier placement questions were unanswered; silence is not a selection.
2. Owner availability for supported dedicated-runner ChatGPT login and a bounded live test using the remaining account allowance. Runtime model availability is unverified.
3. One inbox account/provider and authorised read scope for I19; one usable sender/route for live I20 acceptance.
4. Exact approved destination, body, attachments and material revision for a controlled send. Broad implementation authorization does not authorise employer outreach.
5. Actual interview/offer material and owner usefulness feedback for the corresponding live acceptances.

**Jev disclosure restriction:** the owner explicitly authorised **bounded result facts only** for offer/delivery cross-outcome advice, excluding recipients, message bodies and raw offer text. Interview content and content-derived hashes also stay local in that advice path. Separate detailed per-outcome assessments have their own scope. Do not expand these payloads to satisfy a generic classifier-context preference. Bounded metadata does not establish that Jev has enough evidence for strong semantic advice; keep that limitation visible. `TYPESAFE_API_KEY` is server-side; never print its value or copy env files into a handoff.

## Economical concurrency and verification

Use at most **two implementation workers plus a coordinator** for the next stage, with explicit disjoint paths. This is a token-saving operating choice, not a requirement to keep two tasks busy. No delegation is started by this document.

- Stage A/B overlap: backend integration owner uses Sol High and owns shared Go/contracts; one Sol Medium worker owns all web/smoke changes. Concrete host input gathering can be handled by the coordinator without another worker.
- Once integrated: one owner handles deployment/runtime; one may finish a ready disjoint domain slice. Do not run live recruitment before actual runtime acceptance. Do not start blocked inbox work without a real account choice.
- Later: inbox/domain and delivery/ops can overlap only with declared disjoint files. Serialize initial schema, generated contracts, server startup and shared web changes through one integrator.
- Reuse an existing task only if its context helps. Give it a bounded current assignment, not the entire history. Do not reactivate old instructions automatically.
- Sol Medium is the default for ordinary implementation/checks; Sol High for coupled scope/runtime/side-effect fixes. Use Astra only for a specific unresolved high-impact issue. Luna Low can update factual docs from verified outputs. No routine extra review agent or repeated full suite.
- Reuse worker logs. After integration, check the affected boundaries and run a broad suite only when changes/conflicts or a concrete failure justify it. Fix a failing test's actual race/assertion; never weaken evidence to get green output.
- Stage explicit paths and commit coherent work. Never bulk-stage the unfinished web slice with documentation or unrelated edits. No resets, cleanup of another worker's files, compatibility scaffolding or speculative refactors.

Existing stopped task IDs, if the owner chooses to reuse them: backend `01a0cf13-2c03-73f2-b074-6c73d7120ebe`; web `01a0ce8f-6bf3-7320-8111-75ac34379aa8`; runtime/authority `01a0ce8e-4c6e-7c93-b63c-1084063ba7c0`. All are local. Prefer task IDs over historical titles.

### Relevant existing commands — run only when needed

From `dashboard/apps/api`, the stopped backend worker's passing environment was:

```sh
GOPATH=/private/tmp/agency-go GOCACHE=/private/tmp/agency-gocache go test ./... -count=1
```

Use named affected packages/tests for narrow integration checks. From `dashboard/`: `pnpm --filter @jobseek/web lint`, `pnpm --filter @jobseek/contracts check:generated`, `pnpm e2e`. Go generated check: `apps/api/scripts/check-generated.sh` from `apps/api`. The full browser command builds web and uses synthetic HTTP fixtures; it is not live runtime acceptance. Native Chromium may require the established tool approval. Current recovery check: `PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s ops/i26 -p 'test_*.py' -v`.

The isolated backend fixtures resolve approved source files through `/private/tmp`; the handoff report records the three existing temporary links. Main resolves them from the workspace normally. Preserve approved files. Disk space was recently around 4 GiB free: inspect before large builds and do not delete another active worker's caches or temporary checkouts.

## Evidence map — read selectively

- [Original implementation plan](/Users/vince/Projects/find-income/dashboard-implementation-plan.md) and [full task definitions](implementation-tasks.md).
- [I10 backend handoff](/Users/vince/Projects/find-income/implementation-notes/implementation/I10-cross-outcome-advice.md): `149c616` details; later `8cee343` worker handoff and Git commit contain its correction inventory.
- [I10 authority findings and reproductions](/Users/vince/Projects/find-income/implementation-notes/implementation/I10-cross-outcome-authority-review.md). Reports findings against the original commit, not a fresh verdict on the correction.
- [I10 UI handoff](/Users/vince/Projects/find-income/implementation-notes/implementation/I10-cross-outcome-ui.md), with final-test qualification above.
- [I27 UI corrections](/Users/vince/Projects/find-income/implementation-notes/implementation/I27-client-server-corrections.md): committed605e172, root lint/full synthetic browser pass.
- [Current-schema recovery](/Users/vince/Projects/find-income/implementation-notes/implementation/I26-final-schema-review.md): committedc1f3c83/ff8777c, seven root tests pass.
- [Host inventory](host-readiness.md), [deployment and exact acceptance runbook](../ops/i12/README.md), [native tool boundary](native-tool-boundary.md), [backup runbook](../ops/i26/README.md).
- [Discovery authority review/corrections](/Users/vince/Projects/find-income/implementation-notes/implementation/I13-substantial-discovery-review.md), [interview integration review](/Users/vince/Projects/find-income/implementation-notes/implementation/I24-root-review.md), [offer authority review](/Users/vince/Projects/find-income/implementation-notes/implementation/I25-root-authority-review.md), [delivery authority review](/Users/vince/Projects/find-income/implementation-notes/implementation/I22-delivery-authority-review.md).

## Completion rule

Keep partial tasks partial until their stated live or owner-usefulness evidence exists. The first real pack is a useful milestone, not full completion. The full goal requires the remaining matrix to be satisfied, with I17 explicitly implemented or deferred for an evidenced reason. Missing account, host, real material or exact external-action approval must be recorded plainly. A stopped worker, passing fixture, saved report or drafted plan alone is not completion.
