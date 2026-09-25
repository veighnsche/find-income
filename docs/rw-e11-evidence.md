# RW-E11 · First discovery evidence

25 September 2026 · Lane M · Owner-authorized (D1–D5 + per-rerun yes/no).

## Outcome

Three bounded attempts. The third persisted **3 classified findings**
from genuine public retrieval against brief v2. RW-G1 substance is met:
saved owner requirements drove real persisted job results with Jev
reasons; the 12-fetch bound stopped the run on the 13th retrieval call
exactly as specified.

## Attempts

| Attempt | Run ref | Outcome | Searches/fetches | Saves ok | Jev calls | Findings |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | e11-first-discovery | completed, no retrieval | 12 attempted, all fenced | 0 | 0 | 0 |
| 2 | e11-first-discovery-r2 | completed, finding bug | 12/12 genuine | 1 | 1 | 0 |
| 3 | e11-first-discovery-r3 | bound stop (failed detail) | 13 (stopped) | 3 of 8 | 3 | 3 |

- Attempt 1: every retrieval died on authority — the commissioned round
  granted no research operations. The model adapted across backends,
  verified zero saves, invented nothing, and reported gaps honestly.
  Fixed by scoping research.search/fetch/api on commissioned rounds
  (`9a826c4`; browse/exec stay out) plus URL-form prompt guidance and
  a mid-run authority regression test.
- Attempt 2: genuine discovery (DuckDuckGo, Greenhouse boards, niche
  boards), 1 verified save (Creative Fabrica via its own Greenhouse
  board), 1 Jev judgment. Finding persistence rejected the sha-form
  evidence capture production receipts bind. Fixed by resolving
  evidence captures by row id or content sha (`f42adee`).
- Attempt 3: 3 verified saves from remotive.com company-verified
  pages, 3 Jev judgments, 3 persisted findings. The 13th retrieval
  call triggered the fetch bound; the host was SIGTERM'd (exit 143)
  with zero foreign tool calls. The stop-cause masking this exposed
  is fixed and tested (`f4cee84`).

## Persisted results (verified in jobseek.sqlite)

Brief v2 + catalog-v2-bf90ac54a8e8 + rubric criteria-v2-5ec3aca2688b:

- recommended · Senior Shopify Developer · Sanctuary Computer Inc ·
  https://remotive.com/remote-jobs/software-development/senior-shopify-developer-2091140 ·
  reasons: stack-fit.
- could_be_recommended · Senior Independent Software Developer ·
  A.Team · https://remotive.com/remote-jobs/software-development/senior-independent-software-developer-1919265 ·
  reasons: senior-scope-fit, missing-stack.
- probably_not_recommended · Senior AI Engineer · Lemon.io ·
  https://remotive.com/remote-jobs/artificial-intelligence/senior-ai-engineer-2091131 ·
  reasons: stack-fit, python-centric, missing-salary-hours.

Assessments: 4 rows (attempt 2 + 3), model jev-1.13.0, real
confidences (0.20–0.98), abstentions where unsupported. Reason text
is byte-verbatim from the catalog. Captures: 17 rows; observations:
22 rows. Residue: attempt-2 Creative Fabrica opportunity +
assessment rows persist without a finding (pre-fix failure); honest
residue, completable by a later classify pass.

## Bounds accounting (all within authorization)

| Dimension | Bound | Attempt 1 | Attempt 2 | Attempt 3 |
| --- | --- | --- | --- | --- |
| Model steps | ≤100 | ~15 | ~15 | ~20 |
| Tool calls | ≤400 | 14 | 14 | 21 |
| Retrieval fetches | ≤12 | 12 fenced | 12 | 13th stopped |
| Jev calls | ≤8 | 0 | 1 | 3 |
| Wall clock | 45 min | ~4 min | ~5 min | ~4 min |
| Standard/Codex/OpenRouter | 0 | 0 | 0 | 0 |
| Employer contact | 0 | 0 | 0 | 0 |

Route in all runs: meta/muse-spark-1.3-contributor (folder-enforced).
Task-boundary audit of every trace: only model.*, reminder.* and
tool.mcp__find_income_public__* tasks; zero shell, memory, subagent
or workflow tasks. No API-key override; retrieval stayed on
unconfigured-browse/exec fail-closed HTTP. Owner settings untouched
(isolated XDG homes per run).

## Prerequisites P1–P6

- P1 version/readiness: re-pin to 1.4.0 committed (`c90656d`);
  readiness reported ready before each commission (harness-asserted).
- P2 lane: validation trace + every run trace show
  meta/muse-spark-1.3-contributor; machine-checked before each run.
- P3 MCP scope: only the five public tools served; bearer-gated
  loopback endpoint; clean task audits above. Note: serve-sessionMcp
  is unusable on 1.4.0 (session_mcp_base_unavailable); the transport
  drives `muse exec` with settings-file MCP instead.
- P4 retrieval: HTTP search/fetch/api live; browse/exec fail closed;
  loopback refused; sandbox default on.
- P5 brief: v2 saved with readback match of the owner-confirmed
  values (Amsterdam + remote-EU, 32h, remote/hybrid, senior
  backend/platform/systems, Go/Rust/TS/Linux/MCP prefers, Python
  avoid, €4500 floor); catalog operator-authored, zero model calls.
- P6 stop: supervisor Stop fixtures green; the graceful SIGTERM path
  was genuinely exercised by the attempt-3 bound stop. Operator
  Stop() itself remains untriggered; recorded, not claimed.

## Gaps and next gates

- No employer questions captured on any saved page (0 saved, none
  invented) — E12 checks start from selected roles, not from
  discovery questions.
- Rust/remote-EU/platform coverage is thin; attempt-2 notes point at
  small company boards and Flexport pages 3–4 for a later run.
- E12 needs the owner to SELECT roles from the 3 findings (or the
  residue) plus a separate live-check authorization. E13 needs the
  isolated test SMTP sink, Standard authorization, and owner
  review/send. None of that is authorized by this record.

## Artifacts

- Traces: data/muse-runs/e11-first-discovery*/trace.jsonl (full
  JSONL per attempt, validation trace copied alongside).
- Harness: apps/api/cmd/e11run (refuses without machine-checked
  validation, exact brief match, readiness and Jev credentials).
- Commits: c90656d (re-pin), 1c1f528 (serve transport, superseded),
  ec0fbbd (exec transport), 6192d3e (event folding), 9841e3d
  (harness), 9a826c4 (scope), f42adee (sha evidence), f4cee84
  (stop cause).
