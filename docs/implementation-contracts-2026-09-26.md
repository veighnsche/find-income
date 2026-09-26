# Frozen cross-layer contracts (I1)

26 September 2026 · Status: frozen for implementation. Owner's [product vision](product-vision.md) governs conflicts. Code changes landing these contracts belong to I3; lanes implement against the shapes below. No vague boundary remains between Answers/Prepare or Prepare/Handoff.

## C1 · Run identity and recovery (owner: D; links: F)

- Research runs keep durable server IDs. New reads: relevant run lookup (active/paused/stopped/failed/completed/empty), latest terminal, history.
- Stable deep links `#/search?run=<id>`; server-backed restoration. localStorage is optional convenience only, never the recovery source.
- Stop/resume preserves saved cursor/evidence; retries reuse durable identity. `Find more` explicitly creates a new deduplicated pass; read/resume never starts one.
- Error shape: `run_not_found` for unknown IDs; terminal runs expose only valid actions.

## C2 · Brief/catalog lifecycle (owner: D; schema/registration: I)

- The saved explicit goal (preferences) version governs one authored reusable rubric/reason catalog.
- Authoring happens inside explicit search commission (`POST /api/v1/research/runs`), never on passive read or form save. `AuthorReasonCatalog` becomes idempotent per brief version: concurrent/retried commission converges on one accepted version.
- Failed authoring/classification persists captured jobs + failure; resume continues without discarding them.
- Reads (`GET briefs/{version}/catalog`, results, reasons, goals) are GET-only and make zero model calls.

## C3 · Checked application shape (owner: K)

- New terminal checked outcome `questions_none_verified`: verified route/documents/requirements saved with an empty question set. Distinct from `questions_unresolved` (blocked, held with retained findings).
- Completeness/gaps derive from actual capture metadata; the unconditional `complete` stamp is removed. Adapter verification gaps surface as real gaps, not `other`.
- Requiredness enum per document/question: `required | optional | not_required | unknown`. Polarity preserved (`not required` never demands an item).
- Field/upload mapping: attachment questions are upload instructions, never text form values. Route (email/portal), destination, field mapping saved verbatim.
- Selection: only selected, explicitly started, eligible checks admitted. Performer validated before leaving `checking`; unavailable/unsupported returns honest error, never inert `checking`. Recheck eligibility is a separate explicit rule.

## C4 · Answer continuation (owner: K; UI: A; shared seam: F)

- `PUT .../questions/{questionId}/answer`: exact `keep | edit | clear | blank` with provenance + question/check identity + expected saved version. Stale/cross-job writes conflict (409 + current versions).
- `POST .../answers/commit`: idempotent stage commit against current job/check/question set. Required blank with explicit `draft_requested` choice commits (Standard drafts from verified facts); this resolves the R03 policy conflict. Missing required values without any saved choice conflict with missing IDs.
- Zero-question route commits an empty answer set; zero Jev calls; same continuation semantics as the answered path.
- Frontend commits every visible choice (untouched suggestions included) before navigating; failed/partial saves stay on preserved answers.

## C5 · Owner clarification (owner: K; resume: M; UI: E)

- Distinct question origin `owner_clarification` (vs `employer`), tied to a sourced vacancy requirement + affected work IDs. Never presented as an employer question.
- Saved exact owner answer becomes verified job-scoped context. New text is never silently library-approved.
- One saved answer resolves the same clarification once; API exposes dependency identity so M resumes only affected items.

## C6 · One current artifact set (owner: M; cut: M1; UI: E)

- Route artifacts (`opportunity_artifacts`) are the single canonical user content. Combined-pack prepare/edit/rewrite/readiness endpoints, controls, stores and tests are removed (M1). A pack PDF may survive only as a derived export of the same current set — no independent drafting/readiness history.
- Prepare/edit/rewrite/export/Handoff all consume the same artifact identities + current versions.

## C7 · Evidence freshness and recovery (owner: M; readback: E/F)

- Each artifact persists its basis: consumed check/vacancy/source/profile/fact/answer identities + versions.
- Readiness compares basis against current versions before generation and before commit. Changed inputs invalidate affected items to held/outdated; full prior content retained and readable; unaffected work reused only when dependencies justify it.
- Request keys bind to payload/basis/pin digests: identical replay resolves accepted work; changed replay conflicts. Lost acknowledgments resolve via accepted-write lookup before any new submission.

## C8 · Manual Handoff (owner: I workflow; M projection; E/F UI)

- Transition table replaces `reviewing/sent` with terminal `handoff_saved` (durable saved Handoff; no contact semantics). I owns this change; no production path enters contact states.
- Per-item ready/held/outdated status; saved destination/field mapping; prior content readable under blocked/outdated checks.
- Opening a page is GET-only; link/copy/download never means applied. Partial sets stay listed. Saved work reopens for edits or dependent preparation.
- No fill/attach/send/submit control, API, or browser automation exists anywhere.

## C9 · Shared frontend seams (owner: F; consumers: G/C/A/E)

- One saved-goal context shared by Search + discovery; accepted goal writes update Find-jobs readiness immediately.
- Scoped invalidation after accepted goals/selection/check/answers/material writes; no blind global polling or recommissioning.
- Stable run/job deep links; goal-editor open/focus callback; answer-draft preservation across rematch/navigation (job/check/question-scoped).
- Handwritten client (`api/client.ts`) updated once per integrated contract; generated types stay I-owned.

## Ownership of later extensions

I owns coherent contract extensions (openapi, generated code, handler, main, migrations, role_workflow, shared DTOs). Lanes propose; I integrates. F owns the handwritten client + shared UI seams.

## Failing connected regression cases (Q0 boundary)

Production HTTP router + real temporary store; only provider/process boundaries stubbed; no seeded catalog, answered stage, or ready artifacts. Each case fails on current code:

1. Fresh DB, first explicit commission authors catalog + classifies; second run reuses; new goal version authors once. (fails: R01)
2. Answers UI continuation calls commit; untouched suggestion + edited + optional blank + required draft-request persist and advance. (fails: R03/R04)
3. Verified questionless email/upload route completes and prepares; unresolved questions stay held. (fails: R06)
4. Prepare/rewrite/edit/download/Handoff agree on identity/version/content; no second pack truth. (fails: R08)
5. Changed answer/recheck invalidates readiness; prior content readable; stale replay conflicts. (fails: R07/R23)
6. Same verified vacancy across passes keeps one identity + links; different vacancy stays distinct. (fails: R12)
7. Second context recovers server run; resume continues; Find more is explicit + deduplicated. (fails: R13)
8. Reads/Why/exact edits cause zero provider work; no employer-action endpoint reachable. (fails: R30 residue)

Difficult-choice consultation: no Jev SystemOne consultation was needed for I1 — every semantic above is a direct derivation from the owner's product definition and the reviewed findings, not an open design choice. D2 matching rules, M4 grounding design, and C4 blank semantics follow the plan's explicit text; implementation lanes consult only if genuinely ambiguous cases surface.
