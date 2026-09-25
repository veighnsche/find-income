# RW-E12 · Selected-role check evidence

25 September 2026 · Lane M · Owner-authorized (role selection + live checks, this session).

## Outcome

The owner selected 2 of the 3 E11 findings (Shopify + A.Team,
not Lemon.io) and authorized live checks. Both checks completed
(`checked`, question-set v1) with byte-exact sourced spans. The
honest content finding: **neither page carries genuine employer
application questions** — both are external-apply remotive flows,
so the extractor kept one rhetorical fragment and two board-chrome
lines, and invented nothing. RW-G2 substance is met: selected
roles yielded sourced question rows with verified provenance;
empty-by-design answer boxes are the correct downstream state.

## Checks (verified in jobseek.sqlite)

| Role | Check | Set sha | Questions |
| --- | --- | --- | --- |
| Senior Shopify Developer · Sanctuary Computer Inc (b8a80e61) | 0deb9c23 | dcc451787a4e | 2: required fragment + optional chrome |
| Senior Independent Software Developer · A.Team (912decad) | 43259269 | 2bdf01628f17 | 1: optional chrome |

- Shopify q0 (required): "* Ideas & Products:** In our spare studio
  time, we work to build our own ope…" — rhetorical "Why?" fragment
  from the employer body, span [9690,9916].
- Both q-chrome (optional): "🙈Does this job need an edit?" —
  remotive board chrome, spans [11835,11866] / [5983,6014].
- Vacancy completeness: complete on both; documents []; route
  unresolved (recorded, not defaulted); one non-consequential gap
  each ("Requested documents are not assessed; this check covers
  sourced questions only").
- Captures: fetched_response/complete/200 via the reader proxy
  (r.jina.ai), 11867 + 6015 bytes, artifacts under
  data/research-artifacts/blobs/.

## Integrity verification

- All 3 question text_sha256 values recomputed OK.
- All 3 spans sliced byte-exact from the stored blobs.
- Recall audit (Shopify): the page holds 4 "?" occurrences; 2 are
  URL-embedded (correctly skipped), 2 extracted (fragment + chrome).
  Recall is correct — the page genuinely lacks application
  questions, so precision on chrome is the honest output. No
  chrome denylist was added: the Answer stage (Jev choice among
  saved answers plus no-fitting-answer, owner may leave blank)
  is the designed absorber.
- Harness: apps/api/cmd/e12run. First invocation died to the E09
  round-scoping error after starting both checks (2 orphaned
  `checking` rows: 31a4a0fe, 5fd4bf38 — residue, blocks nothing);
  second invocation completed both after the fix (`746c2a3`).

## Bounds accounting (all within authorization)

| Dimension | Bound | Used |
| --- | --- | --- |
| Live page fetches | checks only | 2 (saved pages, fresh captures) |
| Jev calls | 0 authorized | 0 (no matching run) |
| Standard/Codex/OpenRouter | 0 | 0 |
| Employer contact | 0 | 0 |

Route: checks are deterministic extraction over fetched pages; no
model retrieval beyond the authorized fetches.

## Gaps and next gates

- No genuine employer questions exist on either checked page, so
  there is nothing to answer-match; Jev Answer spend here would
  prove only the no-fitting-answer path and was not authorized.
- Lead, not followed (bounded scope): the Shopify page links the
  employer's own posting (garden3d.notion.site) — a later check
  could target it if the owner wants.
- E13 needs: the owner-approved answer catalog (who authors first
  drafts?), Standard authorization, the isolated test SMTP sink,
  and explicit owner review + send. E14 acceptance follows the
  reviewed safe test send. None of that is authorized by this
  record.

## Artifacts

- Harness: apps/api/cmd/e12run (reuses the E11 validation +
  readiness + credential gates).
- Commits: 746c2a3 (round-scoped E09 errors, e12run, sha:
  canonicalization in job_checks).
