# A4 artifact-readiness mapping: Jev design consultation

Decision: which requiredness rule the deterministic per-type readiness read
should apply for verified application routes.

Options: `evidence_keyword` (deterministic keyword tests over verified check
data, each citing its source), `defer_to_prepare` (leave unproduced types
unresolved until the Standard drafting turn judges them), `fixed_kind_map`
(static required-type table keyed off route kind only).

Requests `request-1/2/3.json` carry identical facts and option keys with fully
rewritten explanatory prose (context, instructions, question, descriptions);
equivalence was machine-checked before sending. Raw responses are
`response-1/2/3.json` (model `jev-1.13.0`, 3,036 tokens total).

| Run | Choice | Confidence | P(evidence) | P(defer) | P(fixed) |
| --- | --- | --- | --- | --- | --- |
| 1 | evidence_keyword | 0.99 | 0.99 | 0.01 | 0.00 |
| 2 | evidence_keyword | 0.95 | 0.97 | 0.03 | 0.00 |
| 3 | evidence_keyword | 1.00 | 1.00 | 0.00 | 0.00 |

All three runs agree on `evidence_keyword`; there is no choice disagreement
to investigate. Confidence varies slightly (0.95–1.00) but every run rejects
the fixed kind table outright (0.00) and gives deferral at most 0.03. The
agreement is advisory, not proof: the mapping is still implemented as plain
deterministic code with cited sources, and the A4 gate tests (unused type
marked not required, never fabricated) must pass on their own evidence.

Adopted rule: form values required iff employer questions exist; email
subject/body required iff the route destination names an email address; CV and
cover letter required iff a sourced requirement statement or attachment
question names them; every other type not required with a stated reason; an
unresolved route leaves every type unresolved.
