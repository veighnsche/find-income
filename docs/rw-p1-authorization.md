# RW-P1 · First live exercise authorization record

25 September 2026 · Lane E with coordinator · Depends on RW-E1 (satisfied) · Worker: worker-rw-p1 (Sol-Medium)

This document is the thing the owner reviews. It records what is and is not authorized for the bounded first live exercise in [RW-E1](rw-e1-live-exercise.md) (§3 stages: contextual correction → anchor discovery → saved classification/reasons → selected-role checks + answer matching).

## 1. Existing-authorization reuse verdict: NO REUSE — nothing covers the E1 operation set

Searched the repo and session handoffs for any spend/operation authorization record. Findings:

| Candidate | Why it does not cover E1 |
|---|---|
| [Vite frontend plan](vite-frontend-implementation-plan.md) ("owner-authorized early implementation plan") | Authorizes implementation work, not paid calls or live provider spend. |
| [Autonomous recruitment progress](autonomous-recruitment-progress.md) | States explicitly: "Live canary: no allowance authorized yet". |
| [Remaining work](remaining-work.md) | States explicitly: the research path "does not authorize employer contact, delivery or live provider spending". |
| [RW-E1 plan](rw-e1-live-exercise.md) | "Authorizes none"; §6 ceiling is a proposal only. |
| Session handoffs (`/tmp/rw-*.md`) | None records an owner spend/operation authorization. |

No invented spend limit: no $C exists. Per the RW-P1 hard constraint, elapsed time and the RW-D0 readiness audit are not authorization.

**Verdict: authorization is MISSING. Nothing in §3 of RW-E1 may run until the owner grants every decision in §2 below.**

## 2. Explicit owner request — decisions still needed

1. **Plan approval.** Approve, amend, or reject the [RW-E1 staged canary](rw-e1-live-exercise.md) (§3 sequence, §4 operation bounds, §5 call tracing, §6 stop conditions, §9 separate no-result scenario).
2. **Spend ceiling $C.** After the operator looks up current unit prices, set the binding currency ceiling $C using the RW-E1 §6 estimate math: `$C ≥ (2 × p_codex_turn) + (12 × p_retrieval_fetch) + (6 × p_jev_choice) + 20% headroom`. The binding ceiling is dual — operation counts AND currency, whichever is hit first. Operations beyond the §4 totals (2 Codex turns + 12 retrieval fetches + 6 Jev Choice calls) need a fresh authorization.
3. **Anchor vacancy nomination.** Nominate exactly one currently-open vacancy URL (company career page preferred) plus a one-line description of why it should match or not match the corrected brief. Recorded verbatim; Codex is not told the URL as a fetch target.
4. **Model/effort values.** Select actual `JOBSEEK_CODEX_MODEL` / `JOBSEEK_CODEX_EFFORT` values from what is available (RW-E1 §4 proposes lowest effort that completes each operation, at most one escalation per operation).
5. **Host selection.** Choose the app host and separate runner VM (`infra` vs `linux` still pending per RW-E1 §8); RW-D1 proves readiness on the selected host before any stage runs.
6. **Valid owner evidence inventory.** Confirm the only usable owner facts are: the current saved `Preferences` row + version (profile id `current`); the owner's verbatim correction text filed during the exercise; `SearchBriefView.facts` labeled as preference-derived search facts only; existing owner-approved `SavedAnswer` versions if any (else honest `none_fits`); career-root presence/absence only, never CV facts quoted from disk. Refs: [RW-E1 §2](rw-e1-live-exercise.md), `/tmp/rw-b0-handoff.md`.

## 3. What is NOT yet authorized

- No paid Codex/Jev call of any kind; no retrieval fetch outside the RW-E1 §4 bounds once authorized.
- No materials/preparation, rendering, delivery, or any send — those belong to RW-P2/RW-G3/G4 and need their own authorization.
- No employer contact under any path.

RW-G1 stays open until RW-D1 readiness is proved, this authorization is granted, and the bounded run produces its persisted evidence.
