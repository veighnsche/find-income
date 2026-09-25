# Recruitment workflow implementation progress

Goal: “Implement and verify the recruitment workflow backlog through RW-G5: saved owner requirements drive real persisted job results, selected roles yield sourced employer questions and editable answers, and grounded materials reach an explicitly reviewed safe test send, with accurate evidence and frequent coherent Git commits.” (active, no token budget)

Plan: `docs/recruitment-workflow-implementation-plan.md`. Only the coordinator updates this ledger and the plan checkboxes. Model selection per dispatch recorded in Worker column (skill: user/model-selection).

| Task | Lane | Status | Worker | Owned files | Deps | Evidence / commits | Blockers | Next |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| RW-A0 | A | complete | coordinator | (mapping only) | — | /tmp/rw-a0-handoff.md: 5 paths mapped, safeguards confirmed, gaps classified (no PUT assumption, no second frontend) | — | — |
| RW-B0 | B | complete | worker-rw-b0 (Sol-Medium) | (read-only) | — | /tmp/rw-b0-handoff.md: Preferences-only versioned context; useOwnerContext interface proposal; 6 Lane-A questions open | — | — |
| RW-D0 | D | complete | worker-rw-d0 (Sol-Medium) | (read-only) | — | /tmp/rw-d0-handoff.md: 10-row table; host unselected (owner blocker); sandbox-binary wiring gap; no paid calls/secrets | — | — |
| RW-E0 | E | complete | worker-rw-e0 (Sol-Medium) | (read-only) | — | /tmp/rw-e0-handoff.md: stage→test map, 4-class evidence format, per-gate scenarios incl. owner-met regression | — | — |
| RW-G0 | A+D/E | complete | coordinator | this ledger | RW-A0, RW-B0, RW-D0, RW-E0 | e5cc228 Wave-0 checkpoint. Diagnosis accepted: behavior gaps → RW-A1/B1/C1; config gaps block live gates only; live services NOT marked ready | Host selection (owner) blocks RW-D1/G1+ | RW-A1, RW-A3, RW-D1–D3, RW-E1 |
| RW-A1 | A | complete | coordinator | process_input_test.go, docs/rw-a1-correction-contract.md | RW-A0, RW-B0 | 4d91983. No production fix needed; new HTTP readback test; contract answers all 6 B0 questions | — | RW-B1, RW-C1 |
| RW-A2 | A | complete | worker-rw-a2 (Sol-High) + coordinator fix | 2 new verify test files, researchservice.go 1-line pin, discovery.go gofmt | RW-A1 | f24501d. All criteria PASS; race fix applied+built; suites green | — | RW-C2 |
| RW-B1 | B | complete | worker-rw-b1 (Sol-High) | useOwnerContext.* + SearchPage + shell.test.tsx | RW-A1 | 35a7e60. Saved requirements + contextual corrections live; coordinator-verified in 222/222 full suite | — | RW-B2, RW-C2 |
| RW-C1 | C | complete | worker-rw-c1 (Sol-High) | saved-brief.*, discovery-section.* | RW-A1 | be388a0. Saved brief foregrounded; coordinator-verified in 222/222 | Server version pinning deferred to RW-A2/C2 | RW-C2 |
| RW-C2 | C(+B) | complete | worker-rw-c2 (Sol-High) | discovery saved-brief/section/grouped-jobs.* | RW-A2, RW-B1, RW-C1 | b0044fc. Single context per surface, focus wiring, authorship, stale retention; 236/236 | — | RW-E2 |
| RW-D1 | D | complete | worker-rw-d123 (Sol-Medium) + coordinator fix | docs/rw-runtime-readiness.md, wire.go, main.go, sandbox_config_test.go | RW-A0, RW-D0 | 5bfe275. BLOCKED (valid): fail-closed paths verified, sandbox-binary env plumbed+tested; live proof impossible — host unselected (owner) | Host selection (owner); install/keys | — |
| RW-D2 | D | complete | worker-rw-d123 (Sol-Medium) | docs/rw-runtime-readiness.md | RW-A0, RW-D0 | BLOCKED (valid): materials gating/one-shot/render verified from code; live proof impossible — runner+model/effort, career root, Typst uninstalled | Host selection (owner); install/keys | — |
| RW-D3 | D | complete | worker-rw-d123 (Sol-Medium) | docs/rw-runtime-readiness.md | RW-A0, RW-D0 | BLOCKED (valid): 6-var SMTP gating + digest binding verified; isolation is operational (sink+route+review), no code-level sink restriction; sink unprovisioned | Sink provisioning; 6 vars; RW-G4 procedure | — |
| RW-E1 | E(+D) | complete | worker-rw-e1 (Sol-Medium) | docs/rw-e1-live-exercise.md (new) | RW-G0 | ca9ebce. Staged-canary plan: correction→anchor discovery→classification→checks; op ceiling 2+12+6, owner-set $C (no invented limit), tracing table, versions, no-result scenario; no paid calls/secrets | — | RW-P1 |
| RW-P1 | E/coord | complete | worker-rw-p1 (Sol-Medium) | docs/rw-p1-authorization.md (new) | RW-E1 | Authorization MISSING (no reuse; evidence refs); owner request recorded: plan approval, $C, anchor, model/effort, host, evidence inventory | f55f958. 6 owner decisions pending | RW-G1 |
| RW-E2 | E | in_progress | worker-rw-e2 (Sol-High) | scripts/ui-smoke/rw-discovery-smoke.mjs (new) | RW-C2, RW-E0 | — | — | — |
| RW-G1 | E+A/C/D | pending | — | — | RW-E2, RW-D1, RW-P1 | — | — | — |
| RW-A3 | A | complete | worker-rw-a3 (Sol-High) | 3 new chain test files | RW-A0 | a079d1c. All criteria PASS, no production fix; coordinator-verified new tests green; zero-LLM Answer path proved | — | RW-B2 |
| RW-B2 | B | complete | worker-rw-b2 (Sol-High) | CheckPage.*, AnswersPage.* | RW-A3, RW-B1 | 94b812d. Answer entry checked-only, source linkage, zero-LLM boundary test; 31/31 | — | RW-E3 |
| RW-E3 | E | complete | worker-rw-e3 (Sol-High) | rw-checks-smoke.mjs + e2e:rw-checks | RW-B2, RW-C2, RW-E0 | Fixture-verified: selection-quiet, unselected guard, 9/9 questions span-traced, gating/honesty, prefill/no-fit, round-trips, zero-LLM; coordinator ran wired smoke green | — | RW-G2 |
| RW-G2 | E+A/B/D | pending | — | — | RW-G1, RW-E3 | — | — | — |
| RW-A4 | A | complete | worker-rw-a4 (Sol-High) | 2 new prepsend test files | RW-A3 | 2a4e222. All safeguards PASS, no production fix; new tests green | — | RW-C3, RW-E4 |
| RW-C3 | C | in_progress | worker-rw-c3 (Sol-High) | prepare/review/attempts features + ApplicationsPage | RW-A4, RW-C2 | — | — | — |
| RW-E4 | E | pending | — | — | RW-A4, RW-C3, RW-E0 | — | — | — |
| RW-P2 | E/coord+D | pending | — | — | RW-G2, RW-D2, RW-E4 | — | — | — |
| RW-G3 | E+A/C/D | pending | — | — | RW-P2 | — | — | — |
| RW-G4 | E+A/C/D | pending | — | — | RW-G3, RW-D3, RW-E4 | — | — | — |
| RW-G5 | E/coord | pending | — | — | RW-G4 | — | — | — |
| RW-L1 | later | deferred | — | — | RW-G5 | planning-only; no deferred UX invented in this run | — | — |
| RW-L2 | later | deferred | — | — | RW-L1 | separately authorized; not in this run | — | — |

## Ready queue

- Wave 0 done (RW-G0 accepted). Ready: RW-A1 (coordinator), RW-A3, RW-D1, RW-D2, RW-D3, RW-E1.
- Blocked on owner host selection: live portions of RW-D1/D2/D3; RW-D1–D3 proceed to record no-spend verification + named blockers.
- B/C UI (RW-B1/C1) wait on RW-A1 handoff.
