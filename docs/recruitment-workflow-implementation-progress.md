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
| RW-A2 | A | pending | — | — | RW-A1 | — | — | — |
| RW-B1 | B | in_progress | worker-rw-b1 (Sol-High) | SearchPage.tsx, features/owner-context/*, shell.test.tsx | RW-A1 | — | — | — |
| RW-C1 | C | pending | — | — | RW-A1 | — | — | — |
| RW-C2 | C(+B) | pending | — | — | RW-A2, RW-B1, RW-C1 | — | — | — |
| RW-D1 | D | complete | worker-rw-d123 (Sol-Medium) + coordinator fix | docs/rw-runtime-readiness.md, wire.go, main.go, sandbox_config_test.go | RW-A0, RW-D0 | BLOCKED (valid): fail-closed paths verified, sandbox-binary env plumbed+tested; live proof impossible — host unselected (owner), nothing installed | Host selection (owner); install/keys | — |
| RW-D2 | D | complete | worker-rw-d123 (Sol-Medium) | docs/rw-runtime-readiness.md | RW-A0, RW-D0 | BLOCKED (valid): materials gating/one-shot/render verified from code; live proof impossible — runner+model/effort, career root, Typst uninstalled | Host selection (owner); install/keys | — |
| RW-D3 | D | complete | worker-rw-d123 (Sol-Medium) | docs/rw-runtime-readiness.md | RW-A0, RW-D0 | BLOCKED (valid): 6-var SMTP gating + digest binding verified; isolation is operational (sink+route+review), no code-level sink restriction; sink unprovisioned | Sink provisioning; 6 vars; RW-G4 procedure | — |
| RW-E1 | E(+D) | complete | worker-rw-e1 (Sol-Medium) | docs/rw-e1-live-exercise.md (new) | RW-G0 | ca9ebce. Staged-canary plan: correction→anchor discovery→classification→checks; op ceiling 2+12+6, owner-set $C (no invented limit), tracing table, versions, no-result scenario; no paid calls/secrets | — | RW-P1 |
| RW-P1 | E/coord | pending | — | — | RW-E1 | — | — | — |
| RW-E2 | E | pending | — | — | RW-C2, RW-E0 | — | — | — |
| RW-G1 | E+A/C/D | pending | — | — | RW-E2, RW-D1, RW-P1 | — | — | — |
| RW-A3 | A | in_progress | worker-rw-a3 (Sol-High) | new *_verify_test.go only | RW-A0 | — | — | — |
| RW-B2 | B | pending | — | — | RW-A3, RW-B1 | — | — | — |
| RW-E3 | E | pending | — | — | RW-B2, RW-C2, RW-E0 | — | — | — |
| RW-G2 | E+A/B/D | pending | — | — | RW-G1, RW-E3 | — | — | — |
| RW-A4 | A | pending | — | — | RW-A3 | — | — | — |
| RW-C3 | C | pending | — | — | RW-A4, RW-C2 | — | — | — |
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
