# RW-E13 · Preparation and reviewed safe test send evidence

25 September 2026 · Lane M · Owner-authorized (Standard for the Shopify
role only, local Mailpit sink, draft-then-approve answers, explicit
review approval of digest 1a587b23 — all this session).

## Outcome

Grounded materials v1 for Senior Shopify Developer (Sanctuary Computer
Inc) were prepared through the full production path (Standard draft,
Jev relevance, Typst render, committed pack), reviewed by the owner,
and test-sent byte-exact to the isolated sink. RW-G3 substance is met:
an explicitly reviewed safe test send with nothing leaving the machine
and zero employer contact.

## Build (all committed, tests green)

- Standard preparation transport with the E13 model-text return path
  (`ab92ca6`): StandardInput-only, no MCP tools, any tool call fatal,
  hardcoded draft/rewrite discipline, route pinned, EventModelText.
- Server wiring + legacy Codex binding removal (`77568c9`, `ae6dc6b`):
  StandardDrafter behind the per-operation factory; CodexDrafter,
  OneShotTurn, DraftInstructions, RewriteInstructions and their tests
  deleted with coverage ported to the Standard table.
- Pack description fallback (`2a2027f`): discovery-saved roles carry
  no opportunity text, which made every pack fail validation. Prepare,
  edit, and rewrite now describe the role from the verified vacancy
  capture bytes the check read (verbatim, 30KB fail-closed bound),
  via a stored-bytes-only Captures reader. Server gets it from the
  research stack; nil keeps the old honest failure.
- Harnesses: e13run (`8714dbc`) with brief/career/Typst gates, a
  machine-checked validation turn, and one bounded commission; e13send
  (`ed35e43`) with compose plus a digest-gated send that refuses on
  any byte drift or pack staleness (`9005c15` fixed an inverted flag).

## Run accounting (all within authorization)

| Dimension | Used |
| --- | --- |
| Standard turns | 1 failed validation (wrong model id) + 1 passed validation + 1 draft + 2 one-step id probes |
| Standard model id | bare `muse-spark-1.3`: the CLI rejects a `-standard` suffix ("does not exist or you lack access"); bare + `-contributor` naming is symmetric across 1.2/1.3, so bare is the full Standard surface. Owner can still correct from the TUI picker. |
| Jev calls | 6 relevance judgments (deduped from 8 citations), model jev-1.13.0, recorded as manifest entries; unpersisted as attempts by design (prepare opens no round) |
| Typst renders | 1 (pack 238039062a01632d4d9543d27bf9503a, PDF 70520 bytes) |
| Answer stage | vacuous advance checked→answering→answered: the approved-answer catalog is empty, so no match is possible; boxes stay blank for the owner. Zero Jev spent proving the deterministic outcome. |
| Sink messages | 1 probe + 1 approved test send; Mailpit loopback-only, TLS-terminated locally |
| Employer contact | 0 |

## Prepared materials v1 (verified in jobseek.sqlite)

- Status prepared, workflow prepared rev 10, question set dcc451787a4e.
- The required fragment got a 3-line grounded draft with exact CV
  excerpts (SodaOS, Can, recab); the chrome question is blank. The
  phrasing is repetitive — reported to the owner at review, who
  approved as-is for the test send.
- Manifest pins check 0deb9c23 + set dcc451787a4e, origin prepared,
  6 relevance entries (relevant/unrelated/uncertain judgments kept
  verbatim, not treated as correctness).

## Reviewed send (verified against the sink)

- Review: v1 materials plus the full composed message (envelope,
  body, attachment, digest 1a587b23…) presented; owner approved the
  digest with rewrite/edit options offered and declined.
- Send: e13send verified the stored bytes against the approved digest
  plus pack currency, then transmitted via the production SMTP client:
  accepted_by_smtp, data_reply, 250.
- Receipt: Mailpit holds message e13-73592bd3a0b6 with the approved
  subject and 1 attachment; the downloaded attachment sha256
  (7380ee96…) matches the composed pack PDF sha byte-exact.

## Gaps and next gates

- The production delivery chain (ReserveDeliveryAttempt) is correctly
  unreachable for this role: no verified employer route exists, and
  the route-excerpt containment rule cannot pass on empty employer
  text. Real sends need an employer-contact route (owner-supplied or
  captured by a future check). The sink send exercised production
  compose + SMTP bytes, not the fenced delivery attempt — recorded,
  not claimed.
- Approved-answer catalog authorship ("draft then approve") is
  deferred: with chrome questions it is moot for this role; the
  drafts above came from career sources, not the catalog.
- A.Team is checked but untouched by Standard (not authorized).
- E14 acceptance follows this record.

## Artifacts

- Traces: data/muse-runs/e13-shopify-prepare-validation/trace.jsonl,
  data/muse-sessions/standard/prepare-*/trace.jsonl (validation +
  draft turns), data/muse-runs/e13-shopify-send/{send-mime.bin,
  send-meta.json} (approved bytes).
- Sink: docker jobseek-mailpit (loopback 1025/8025, UI localhost:8025);
  TLS terminator was transient per send (no setsid on macOS).
- Commits: ab92ca6, 77568c9, ae6dc6b, 8714dbc, 78e65b1, 2a2027f,
  9005c15, ed35e43.
