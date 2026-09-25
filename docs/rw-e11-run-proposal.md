> **RETIRED 26 Sep 2026 — superseded by [product-vision.md](product-vision.md), [connected-prototype-ux-parity-tasks.md](connected-prototype-ux-parity-tasks.md) and [p0-retain-cut-ledger.md](p0-retain-cut-ledger.md). The seven-step manual-handoff scope removed application approval/sending, delivery, employer-site autofill, and downstream replies/interviews/offers. Kept for history only; do not implement from this document.**

# RW-E11 · Concrete first-run proposal for owner review

25 September 2026 · Lane M · Depends on RW-E1 plan, RW-P1 record, E10 readiness.

**This proposal authorizes nothing.** It is the concrete operation the
owner can approve, adjust, or reject. No live call, retrieval run,
spend, or employer contact occurs until the owner explicitly approves
the exact scope below and every prerequisite is verified.

## 0. Decisions required from the owner

| # | Decision | Recommendation |
| --- | --- | --- |
| D1 | CLI/contract version path: installed CLI is `1.4.0`, contract pins `1.3.0` (E10-B1) | Re-pin the contract to `1.4.0` with schema/tool-inventory re-verification (reviewed M commit), rather than downgrading the owner's CLI |
| D2 | Approve this exact run scope and bounds (§2–§4), or adjust named values | Approve as written |
| D3 | Approve the Jev call budget (§3) and acknowledge Jev is separately billed with unverified plan/limits | Approve ≤8 batched calls with per-call usage logging; unknown plan stays unknown |
| D4 | Retrieval prerequisites: install and verify browser/API retrieval paths on this MacBook before the run (§1) | Owner confirms which browser/toolchain paths the operator may configure |
| D5 | Optional recall anchor: one known-current public vacancy URL as a findability benchmark, never as a required fetch | Run with an anchor if the owner names one, else without; absence only limits recall claims |

D2 approval covers this E11 discovery run only. E12 checks, E13
materials/send, and any Standard call need separate later proposals.

## 1. Prerequisites (all verified before the run)

- P1 · Version path resolved per D1: either the contract re-pin to
  `1.4.0` is committed with re-verified schema/inventory, or CLI
  `1.3.0` is installed; readiness must report `muse_ready` for the
  Contributor tier with no fallback.
- P2 · Effective Contributor subscription lane confirmed without a
  model call; no API-key override present at run time.
- P3 · Per-session MCP scope verified: only the five frozen public
  tools served (`public_search`, `public_fetch`,
  `public_save_vacancy`, `public_save_question`, `public_list_saved`);
  inherited host tools/plugins/skills audited against the E10
  inventory (34 skills, 0 installed plugins); `sessionMcp`
  negotiated on the verified session route.
- P4 · Retrieval containment verified on this MacBook: configured
  browser/API paths with pinned digests, sandbox enforcement,
  time/byte bounds, scratch/artifact permissions; loopback stays
  refused.
- P5 · Owner-confirmed search brief saved and read back through the
  versioned correction path; only reviewed/supplied facts are used.
- P6 · Stop/cancel demonstrated against the live session route
  without starting the run (admission refusal or dry cancel), or
  honestly recorded as unproven with reliance on the frozen
  Supervisor.Stop contract plus the hard wall-clock stop.

## 2. Exact operation

One adaptive public vacancy-discovery run through Contributor on the
owner's MacBook, started only by an explicit owner **Find jobs**
action after P1–P6. Contributor freely chooses public sources,
queries, public APIs and company career pages; the app executes
retrieval through the verified boundary and persists genuine public
receipts/results with checkpoints. Jev classifies persisted findings
against the saved brief with saved reasons. No Standard, Codex, or
OpenRouter call occurs. No employer contact occurs. No automatic
repeat or extension under any outcome.

## 3. Enforceable bounds (one run)

| Dimension | Bound | Source |
| --- | --- | --- |
| Wall clock | Hard stop at 45 minutes | Frozen ceiling |
| Model steps/turns | ≤100 | Below the 120 ceiling |
| Tool calls | ≤400 | Frozen ceiling |
| Bytes | ≤2 MiB per operation, ≤200 MiB total | Frozen ceiling |
| Background work (observers, subagents) | Off | Frozen |
| Retrieval fetch attempts | ≤12 with a receipt per attempt | RW-E1 provisional bound |
| Jev Choice/Score calls | ≤8, batched where supported, per-call usage logged | RW-E1 provisional bound, raised from 6 to cover screening plus reason reuse explicitly |
| Standard / Codex / OpenRouter calls | 0 | Hard |
| Runs | 1; no auto-repeat | Hard |

Subscription CLI use has no meaningful per-run dollar meter here, so
no dollar hard stop is asserted for it. Jev limits are unverified
(E10-B3): the run stays within the 8-call bound and logs observed
usage; unavailable usage is recorded as unknown, never estimated.

## 4. Stop conditions

Stop immediately, save partial evidence with checkpoints, and report
the blocker when any bound above cannot be maintained or is reached;
when Jev or retrieval fails; when the anchor (if any) is
closed/unusable; or when an invented fact, question, or reason
appears. Never increase limits, switch provider or tier, retry an
uncertain request, or extend the timebox. Unknown usage remains
unknown.

## 5. Call and data trace (recorded during the run)

- Full Contributor session log: every model input, tool call with
  arguments, tool result, checkpoint, and Stop/cancel event.
- Post-run audit asserting every session input and MCP result carried
  only generalized role/region/skill criteria and public vacancy
  evidence; owner identity, CV, private profile facts, saved answers,
  private Jev inputs/results, and private session history must be
  absent. Any violation fails the run.
- Retrieval receipts per attempt; Jev request/response usage per
  call; persisted finding versions; reload and zero-call
  explanation-open checks after the run.

## 6. What this run can and cannot prove

- Can prove: saved brief → real persisted results; genuine retrieval
  with receipts; Jev classification with saved reasons; public-only
  Contributor boundary; honest partial/no-result reporting.
- Cannot prove: recall of any particular vacancy without a D5
  anchor; subscription entitlement beyond this run's observed
  behavior; Jev plan/limit adequacy; anything about E12–E14.
- No suitable vacancy after the timebox leaves RW-G1 open. A failure
  before retrieval is "unavailable," not "no matching jobs."
