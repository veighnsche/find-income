# RW-E1 · Bounded first live exercise (staged canary) — reviewable plan

25 September 2026 · Lane E with D · Depends on RW-G0 (satisfied) · Worker: worker-rw-e1 (Sol-Medium)
Planning only. No paid call has occurred; this document authorizes none. Live execution needs RW-D1 readiness + RW-P1 authorization.

Sources: [RW plan](recruitment-workflow-implementation-plan.md) (RW-E1 box, RW-G1/G2 scenarios), `/tmp/rw-d0-handoff.md` (runtime constraints), `/tmp/rw-e0-handoff.md` (evidence format + gate scenarios), `/tmp/rw-a0-handoff.md` (supported paths), `/tmp/rw-b0-handoff.md` (owner-context boundary). No secret values appear here; env names only.

## 1. Scope and non-goals

Staged path, in journey order: contextual correction → known-current-vacancy discovery → saved classification/reasons → selected-role checks + answer matching. Materials/preparation, rendering, delivery, and any send are out of scope (RW-P2/RW-G3/G4). No code changes are part of this exercise.

This plan does NOT hard-code a production source universe. It names one owner-nominated verification anchor (a known-current vacancy URL, §3) so the canary has a falsifiable target, and bounds (max pages, max roles, stop conditions, §6). Within those bounds Codex still chooses live sources, queries, public APIs, and company career pages freely. The anchor is a check, not a constant: Codex may reach it by any route it chooses, and any additional listings it collects through its own discovery are legitimate canary evidence.

## 2. Valid owner facts (only these may be used)

Per RW-B0: the only versioned, readable, correctable owner-context store is `Preferences` (seed: Amsterdam, 32h, EUR 4500 floor, 3 criteria; exactly one profile, id `current`).

- Allowed: the current saved `Preferences` row + version; the owner's verbatim correction text filed during the exercise; `SearchBriefView.facts` labeled as preference-derived search facts (location/remote/hours/pay/timezone), never as CV/experience facts.
- Allowed for answer matching only: existing owner-approved `SavedAnswer` versions, if any exist at exercise time; otherwise the honest outcome is `none_fits` (no invented answers).
- Career-root documents (`JOBSEEK_APPROVED_CAREER_ROOT`): opaque to the UI today (RW-B0 Q1 open). The exercise may record presence/absence only ("career folder connected, N documents" or "not connected"); it must not quote CV facts from disk into briefs, reasons, or answers.
- Forbidden: invented employer questions, invented per-job reasons, invented profile facts, hand-seeded search terms presented as Codex discovery.

## 3. Staged sequence with concrete sources and evidence refs

Prerequisite: RW-D1 proves readiness (§8) and RW-P1 records owner authorization (§6) before stage 1 starts.

- Stage 1 — contextual correction. Starting state: saved brief wrong or empty (owner-confirmed). Owner files one natural-language correction via `Change my search` → `POST /api/v1/rounds/process-input` (`targetKind:"profile"`, `targetId:"current"`, `expectedRevision:<vN>`). Expected: round completes, `GET /preferences` reads back version vN+1, status strip shows pending→saved. Evidence refs: owner-instruction id, round id, profile vN→vN+1.
- Stage 2 — known-current-vacancy discovery. Owner nominates at RW-P1 time exactly one currently-open vacancy URL (company career page preferred; public board posting acceptable) plus a one-line description of why it should match or not match the corrected brief. Operator records the nomination verbatim; Codex is NOT told the URL as a fetch target — it receives only the saved brief and discovers freely within the bounds of §6. Success: the anchor vacancy is genuinely collected from its real source during the bounded run and persisted as a finding. Evidence refs: run id, brief `rubricVersion`/`rubricSource`, reason-catalog version (authored once for this brief version), finding id(s) with source URL + route + artifact receipt.
- Stage 3 — saved classification/reasons. Jev classifies collected listings against the once-per-version catalog and selects supported saved reason text; explanation opens later with zero new calls. Evidence refs: finding→group assignments, selected reason ids + verbatim text, catalog version pin, reload + zero-call explanation-open observation.
- Stage 4 — selected-role checks + answer matching. Owner selects 1–2 roles (at least one must be a role with real employer questions if the canary is to prove the positive path; a no-question vacancy proves only the no-question case per RW-G2). Explicit `Check chosen jobs` action only; then Answer-stage Jev matching against approved answers + `none_fits`, with editable boxes left for the owner. Evidence refs: decision record + revisions, check records + question provenance (source URL/route per question), match-run id, answer revisions (or honest blanks/`none_fits`).

## 4. Economical models and effort per operation (proposal)

`JOBSEEK_CODEX_MODEL`/`JOBSEEK_CODEX_EFFORT` are owner-selected at RW-D1/RW-P1 from actually-available values; this plan proposes starting at the lowest effort that completes each operation, escalating at most once per operation before stopping (see §6):

| Operation | Engine | Effort guidance | Bounded usage estimate |
|---|---|---|---|
| Correction round (`preferences.correct`) | Codex, 1 turn (server-enforced limits: ≤7 requests, ≤3 items, ≤8 tools, 1 turn) | Lowest available effort | 1 round, ≤1 turn |
| Reason-catalog authoring (once per brief version) | Codex/LLM, 1 authoring call | Lowest effort that yields a usable rubric | 1 call |
| Discovery collection | Codex retrieval (browser/exec within sandbox) | Lowest effort; bounded pages below | ≤6 page fetches, ≤2 distinct sources, ≤10 persisted findings |
| Per-listing classification + reason selection | Jev Choice (batched where supported) | n/a (classifier) | ≤10 listings in ≤3 batched calls |
| Selected-role checks (1–2 roles) | Codex retrieval, reuse of run evidence | Lowest effort; no re-collection of already-saved pages | ≤2 roles, ≤3 fetches each |
| Answer matching | Jev Choice among saved answers + `no-fitting-answer` | n/a (classifier) | 1 call per question batch, ≤3 calls |
| Explanation open, navigation, reload, readback | Neither (deterministic reads) | — | 0 model calls (asserted, §5) |

Totals for the ceiling math: ≤2 Codex authoring/correction turns + ≤12 Codex retrieval fetches + ≤6 Jev Choice calls + 0 calls for reads.

## 5. Per-action call-tracing plan (Codex / Jev / neither)

Observed from server logs/traces/doubles and persisted records, never from UI text alone (RW-E0 §2 format):

| Owner-visible action | Expected engine | Trace source |
|---|---|---|
| File correction | Codex (1 `process_input` round) | Round record (id, state, `operations: preferences.correct`), `codex/status` usage delta |
| Read back saved brief / reload | Neither | `GET /preferences` version, `GET /research/brief` — assert zero non-auth POST |
| Press Find jobs | Codex (commission 1 run) | Run id, journal/activity entries, artifact receipts per fetch |
| Catalog authoring | Codex/LLM once | Catalog version + `rubricSource` pin to brief version |
| Classification + reasons | Jev only | Match/classification records, finding→reason pins |
| Open explanation | Neither | Assert zero fetch/model call on open (repeat per RW-E0) |
| Select roles | Neither (guarded decision POST only) | Decision record; assert zero check/match traffic |
| Check chosen jobs | Codex retrieval (selected roles only) | Check records per role; assert unselected roles untouched |
| Answer matching / prefill | Jev only | Match-run record; assert zero Codex/LLM calls |
| Edit answer (exact save / blank) | Neither | Byte-exact answer revision + version guard |

Any deviation (e.g. a Codex call during Answer, a model call on explanation open, check traffic after selection alone) is recorded as a failure-criterion hit and keeps RW-G1/RW-G2 open.

## 6. Proposed spend ceiling, estimate math, and stop conditions

No spend limit is invented here: the ceiling below is a PROPOSAL for the owner to approve, amend, or reject at RW-P1. Unit prices are unverified (no paid call has ever run); the binding ceiling is therefore dual — operation counts AND currency, whichever is hit first.

- Proposed operation ceiling: 2 Codex authoring/correction turns + 12 Codex retrieval fetches + 6 Jev Choice calls, exactly the §4 totals. Any operation beyond these counts requires a fresh authorization; it is not covered.
- Proposed currency ceiling (to be confirmed): owner sets $C at RW-P1 after seeing current unit prices. Estimate math for the owner: `ceiling $C ≥ (2 × p_codex_turn) + (12 × p_retrieval_fetch) + (6 × p_jev_choice) + 20% headroom`, where each `p_` is the current list price the operator looks up at RW-P1 time. If the computed estimate exceeds the owner's $C, shrink the bounds (fewer fetches/roles) rather than exceeding $C.
- Suggested $C starting figure for discussion only (not authorized): $25. If the RW-P1 price lookup shows the §4 totals cost more than $25, the owner either raises $C or narrows the exercise; the plan makes no assumption.
- Stop conditions (any one halts the exercise immediately, with partial evidence retained): operation or currency ceiling reached; anchor vacancy unreachable or closed (record as blocked, do not substitute silently); `Unknown` rate indicating input/classification failure (per RW-G1 S3: mass-Unknown is investigated, never accepted); any invented question/reason/fact detected; any ceiling-exceeding retry requested; owner says stop.

## 7. Expected persisted versions

After a successful exercise, all of the following exist and survive reload, and the RW-G1 record names each id:

profile vN (pre-correction) → profile vN+1 (post-correction readback); brief `rubricVersion`/`rubricSource` pinned to vN+1; reason-catalog version authored once for that brief version; research run id (+ journal); finding ids (including the anchor) with source URLs, groups, and reason pins; decision record (selected roles + revisions); check records per selected role with question provenance; answer-match run id + answer revisions incl. honest blanks/`none_fits`.

## 8. Prerequisites mapped to RW-D1 / RW-P1

Nothing in §3 runs until every RW-D1 item is proved (no-spend) and RW-P1 is recorded:

| Need | Proved by | Blocker today (RW-D0) |
|---|---|---|
| Host selected (app host + separate runner VM) | Owner decision, recorded by coordinator | Owner placement pending (`infra` vs `linux`) |
| App installed (API+Caddy+TLS, `api.env`) | RW-D1 install + `GET /health` | Nothing installed on either host |
| Isolated runner + containment proof; `JOBSEEK_CODEX_ISOLATION_VERIFIED=true` only after accept steps | RW-D1 accept probes + owner ChatGPT sign-in | No containment proven anywhere |
| Jev key (`TYPESAFE_API_KEY`) present | RW-D1 log-line + no-spend answer-match probe | Unverifiable until host selected |
| Codex model/effort available values chosen | Owner selection at RW-D1/RW-P1 | Unverifiable; §4 guidance is a proposal |
| Retrieval backends (pinned shell, python, sandbox wrapper, artifact/scratch roots) + `wireResearch` sandbox-binary wiring fix | RW-D1 install + fix (D lane) | Wiring gap: `JOBSEEK_RESEARCH_SANDBOX_BINARY` unread; Linux wrapper unused |
| Same instance serves UI/API paths under test | RW-D1 wiring check | No live instance |
| Spend ceiling $C + anchor nomination + valid owner evidence recorded | RW-P1 authorization record | Awaits this plan's review |

Elapsed time and the RW-D0 audit are not authorization and not readiness.

## 9. Honest no-result scenario (separate)

Scenario N1 — deterministic empty run. Setup: a discovery run against fixtures/doubles that return zero usable listings (or a no-spend blocked-setup state: runner/Jev absent). Expected UI: plain "no results" or "unavailable / needs setup" state; never a fake search or silent success. Per-action trace: commission attempt → honest 503/blocked or empty finding set; zero persisted findings; no classification calls. Evidence class: FIXTURE-VERIFIED (deterministic doubles), recorded in its own RW-G1 §2 block — never under live-integrated. If the live §3 run itself returns zero usable listings, that is recorded separately as LIVE-INTEGRATED with its run id and investigated (input failure vs genuinely empty market), not merged with N1.

## 10. Done-when check (RW-E1)

- [x] Plan covers correction → anchor discovery → saved classification/reasons → checks/answer matching with concrete sources and evidence refs (§3), valid owner facts (§2), economical models/effort with usage estimates (§4), proposed ceiling + estimate math + stop conditions (§6), per-action call tracing (§5), expected persisted versions (§7), separate no-result scenario with evidence class stated (§9), and RW-D1/RW-P1 prerequisites (§8).
- [x] Plan explicitly does not hard-code a production source universe (§1); Codex still chooses live sources within the staged bounds.
- [x] Zero paid calls, zero sends, zero secret values, no code changes.
