# Implementation progress — 26 September 2026

Goal: Complete all 40 tasks in dashboard/docs/current-code-implementation-lanes-2026-09-26.md, close the mapped current-code findings, and deliver the owner's seven-step UI and usability through a verified connected journey from saved goals to manual per-job Handoff. Commit coherent progress early and often, and preserve durable task, dependency and acceptance evidence until the goal is complete.

Baseline: HEAD ef463c48035f392848c676eea06d6f4e30751307 matches reviewed commit. Worktree retains: M docs/connected-prototype-ux-parity-tasks.md (superseded notice, valid); untracked authorities current-code-implementation-lanes, current-code-wishes-review, muse-spark-implementation-prompt. No unrelated user edits discarded. Contact/lifecycle removal recorded as retained; remaining subtraction is R30 runtime ceremony + R08 duplicate preparation workflow only.

File ownership (one writer per file; transfers recorded before editing):
- I (coordinator): packages/contracts/openapi.yaml, generated Go/TS contracts, httpapi/handler.go, cmd/server/main.go, all SQL migrations, store/role_workflow.go, shared DTOs, root verification/toolchain config.
- R: Contributor/Standard invocation adapters, musewire/live.go, standard.go, runner/factory/session/probe consumers, agreed obsolete package cuts. Hands check/classification/drafting consumers to D/K/M by exact file after R2.
- D: discovery/classification services, store/reasoncatalog.go, opportunities.go, agreed discovery persistence/query, discovery HTTP handlers/tests.
- K: check adapter/service, store/jobcheck.go, answer values/matches, clarifications, jev/answer_match.go, check/answer HTTP handlers/tests.
- M: store/artifacts.go, artifact_readiness.go, material persistence, materialprep/*, artifact/material/Handoff services, renderer/export adapters.
- F: App.tsx, api/client.ts, pages/useRead.tsx, routes/useRoute.tsx, shared nav/stage components, role-stages.tsx, application-continue.tsx, Search/Today/Jobs/JobDetail/Applications pages + integration tests.
- G: features/goals/*, features/owner-context/*, features/discovery/* + focused tests.
- C: features/check/* + focused tests.
- A: features/answers/* + focused tests.
- E: features/prepare/*, features/handoff/* + focused tests (after M1 hands over obsolete Prepare controls).
- Q: agreed connected-test/harness/evidence files, updated smoke gates.
- api/client.ts always F-owned; generated types always I-owned; handler.go/main.go/workflow/migrations always I-owned.

Task ledger (checkbox only when acceptance evidence exists):
- [x] I0 baseline/ownership ledger — done: HEAD ef463c4 == reviewed commit; docs-only delta; ledger committed a38ee26; baseline workflow synthesis confirmed.
- [x] I1 minimum cross-layer semantics — done: contracts C1–C9 frozen in implementation-contracts-2026-09-26.md (f81c777) with shapes, ownership, 8 failing Q0 cases. No Jev consultation: semantics derive directly from product vision + reviewed findings.
- [ ] R1 direct CLI seam — active (worker lane/r1). Temp R ownership of LiveTransport construction blocks in cmd/server/main.go + researchwire/wire.go (call-site compat only).
- [ ] M1 remove second preparation workflow — active (worker lane/m1). Temp M ownership of obsolete pack controls in features/prepare/PreparePage.tsx + ApplicationsPage pack list for deletion only; then handover to E. client.ts/openapi/handler/main/migrations stay I/F-owned: worker proposes, coordinator applies.
- [ ] R2 cut rejected runtime consumers — blocked on R1+M1
- [ ] I2 subtraction gate — blocked on M1+R2
- [ ] I3 publish contracts — blocked on I2
- [ ] D1 catalog lifecycle — blocked on I3+R1
- [ ] D2 vacancy identity/card facts — blocked on I3+R1
- [ ] D3 server run recovery — blocked on I3+D1+D2
- [ ] D4 sourced owner context — blocked on I3+R2
- [ ] K1 truthful route-complete checks — blocked on I3+R1
- [ ] K2 Jev approved candidates — blocked on I3+K1
- [ ] K3 answer commit — blocked on I3+K1+K2
- [ ] K4 owner clarification — blocked on I3+K1+K3
- [ ] M2 evidence pins/replay — blocked on M1+I3+K1+K3
- [ ] M3 route-needed targets — blocked on M2+K1
- [ ] M4 grounded prepare — blocked on M2+M3+K3+K4+R1
- [ ] M5 edit/rewrite same artifacts — blocked on M2+M4
- [ ] M6 export/canonical index — blocked on M3+M4+M5+I3
- [ ] F1 shared seams — blocked on I3
- [ ] F2 opening/status/stages — blocked on F1+D3+D4+K1
- [ ] F3 saved-artifact job list — blocked on F1+F2+M6
- [ ] G1 goals/source context — blocked on F1+D4
- [ ] G2 commission/run controls — blocked on F1+F2+D1+D3
- [ ] G3 jobs/choice — blocked on F1+D2+K1
- [ ] G4 selection/tabs — blocked on G3+F1+K1
- [ ] C1 truthful check UI — blocked on F1+K1+K3
- [ ] A1 suggestions vs saved — blocked on F1+K2+K3
- [ ] A2 draft preservation — blocked on F1+A1+K3
- [ ] A3 save/commit/continue — blocked on A1+A2+K1+K3
- [ ] E1 one Prepare action — blocked on F1+A3+M4
- [ ] E2 editors/rewrite — blocked on E1+M5+F1
- [ ] E3 partial/stale readable — blocked on E1+E2+M2+M6
- [ ] E4 manual Handoff — blocked on E2+E3+F3+M6
- [ ] Q0 connected harness — active (worker lane/q0, new harness files only; contract cases update after I3). Offending-test corrections go through lane owners later.
- [ ] Q1 seven-step identities — blocked on all feature lanes
- [ ] Q2 UI/usability — blocked on Q1
- [ ] Q3 live provider evidence — blocked on Q1 (+Q2 corrections for walkthrough)
- [ ] Q4 closure ledger — blocked on I2+Q1+Q2+Q3

Integrated dependencies landed: I0 (a38ee26), I1/C1–C9 (f81c777). R1 design settled on evidence: `muse exec` has native web tools + --output-schema, so direct invocation needs no MCP loopback; app verifies cited URLs itself.

Commits: a38ee26 docs(I0) ledger/baseline/ownership checkpoint.

Verification: meaningful focused checks per checkpoint before commit; Q gates require production HTTP + real temp store, controlled provider adapters only, no seeded catalog/answered state/ready artifacts.

Remaining limits: full 40-task scope open; live Contributor/Jev/Standard gates require configured services and implementation-run authorization — fixture success cannot close them; no employer contact authorized.

Next ready actions: integrate Wave 1 worker outputs (R1, M1, Q0) per file set with focused checks, then R2/I2/I3.
R2 candidate surface (read-only map, cut only after R1/M1 consumer trace): codex/*, codexrunner/*, codexservice/*, rounds/*, researchwire, cmd/codex-runner, httpapi/codex.go + rounds.go (+research/discovery dead paths), store/round_supervision.go, researchexecute, agency engine/input (pack parts via M1), runtimeaccept/doc.go. External importers outside those dirs: cmd/server/main.go, materialprep/standard.go (R1 seam), researchservice.go, reasoncatalog.go. Frontend: run/check activity feeds, actor-label, client.ts codex/round methods, fixtures. Keep: needed run persistence for D3, public evidence, Jev, bounded direct invocation, auth.
