# Complete the owner's contextual input

Historical evidence and design notes. The fixed-board discovery implementation and its execution plan were removed on 24 September 2026; see [current remaining work](remaining-work.md). References below to discovery tasks are not current implementation instructions.

The current input handlers save material, but the agency does not reliably finish its requested record work. Add an owner-commissioned `process_input` outcome, with a contextual label such as “Update my brief” or “Handle this opportunity”. One action must produce changed records or explicit unresolved facts, using Codex row operations and Jev semantic organisation. External-agent intake remains inert; no chat, row-entry forms or unrelated discovery prerequisite.

The context supplies target identity and revision. Preserve input/request identity across retry and interruption. Supported links use bounded verified reads; full pasted material needs no fetching first. Ask one focused question only when the intended scope or a necessary personal fact is genuinely missing. Profile, opportunity, evidence, relationship and immutable pack corrections belong to this flow.

An authorised profile correction records the original and new effective profile, fences affected older execution/decisions, and uses the remaining commissioned allowance. It may continue only already-authorised input work; a profile-only update finishes after the change. Do not automatically start discovery or widen the scope.

When another round is paused, clearly name both ending it and starting the replacement in the owner control. Validate its expected ID/revision and atomically retain its history/uncertainty, end its authority and create the new finite commission. Starting the worker occurs after commit. A stale request cannot end a different round, and a new commission cannot blindly repeat an uncertain external action.

Three independently worded Jev SystemOne consultations recommended these choices; all requests and raw responses are in [the evidence record](/Users/vince/Projects/find-income/implementation-notes/implementation/I11-input-consultation/review.md). The calls reported 4,282 tokens. Agreement is advisory; paused-switch confidence varied materially. Implementation must prove real controller/tool processing, not merely successful input submission, and pass interruption, stale revision, profile change and uncertain-action checks. Native runner and live account acceptance remain separate gates.
