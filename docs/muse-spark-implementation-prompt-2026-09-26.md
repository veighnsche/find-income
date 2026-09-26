# Muse Spark 1.3 implementation prompt

Paste the following into the Muse Spark 1.3 coding agent in this repository. This file is a prompt for that agent; writing it does not start implementation.

```text
You are the Muse Spark 1.3 coding agent responsible for completing the find-income implementation plan. Act as the coordinator and integrator, and use concurrent implementation workers where available.

Repository: /Users/vince/Projects/find-income
Application workspace: /Users/vince/Projects/find-income/dashboard

CREATE THE GOAL FIRST

Create an explicit persistent goal using your available goal mechanism:

"Complete all 40 tasks in dashboard/docs/current-code-implementation-lanes-2026-09-26.md, close the mapped current-code findings, and deliver the owner's seven-step UI and usability through a verified connected journey from saved goals to manual per-job Handoff. Commit coherent progress early and often, and preserve durable task, dependency and acceptance evidence until the goal is complete."

If you have no native persistent goal mechanism, record this objective and its progress in a durable implementation ledger and use it as the controlling objective across continuations. Do not invent a time, spending or token budget.

This prompt authorizes implementation of that plan. The documents' planning-only notices describe the turns when they were written; they do not prohibit execution under this instruction. Continue through the whole plan rather than stopping after planning, one lane, one wave or a passing isolated screen test.

READ THE AUTHORITIES

Read the applicable AGENTS.md instructions and these files before edits:

1. dashboard/docs/product-vision.md
2. dashboard/docs/current-code-implementation-lanes-2026-09-26.md
3. dashboard/docs/current-code-wishes-review-2026-09-26.md
4. dashboard/docs/connected-prototype-ux-acceptance.md
5. dashboard/docs/assets/connected-prototype-first-use-2026-09-25.png

The owner's product definition governs conflicts. The current implementation-lanes document is the work queue. The earlier connected-prototype-ux-parity-tasks.md is superseded; do not execute its entire historical backlog again.

Match the prototype's UI structure, information priority, grouping, controls, navigation and usability. Exclude the top preview bar. Colors, fonts, border thickness, roundedness, shadows and exact pixel spacing are outside this goal. The seventh step follows the owner's updated manual Handoff definition.

The product journey is:
Your goals → Find jobs → Select jobs → Check job details → Answer questions → Prepare materials → Handoff.

Contributor and Standard are direct Muse Spark 1.3 CLI invocations. Discovery may freely choose public sources and queries. Jev provides the specified classifications and approved-answer choices. Answer questions invokes no LLM. Exact owner edits invoke no model. Preparation uses verified owner facts and actual checked vacancy requirements. The app never fills an employer website, attaches files there, sends email, submits an application or contacts employers. The owner performs the external application personally.

EXECUTE IN DEPENDENCY ORDER

Begin with I0: inspect HEAD and the worktree, preserve unrelated existing changes, and compare against reviewed commit ef463c48035f392848c676eea06d6f4e30751307. Retain valid intervening fixes. Use the existing review rather than restarting a full audit.

Complete I0/I1, then R1 and M1, then R2/I2, then I3. Respect the global contract and subtraction gates. The remaining subtraction concerns rejected runtime machinery and the duplicate preparation workflow; do not repeat already completed contact/lifecycle removal.

After the gates, dispatch ready D/K/F tasks, followed by M/C/A/G, then E and remaining F integration, according to the plan's actual task dependencies. A wave is scheduling guidance, not permission to ignore a dependency. Finish Q0–Q4 with connected, UI and live evidence.

Do not rebuild working functionality, add compatibility/backfills/legacy paths, hard-code the source universe, fabricate questions/facts/evidence, or introduce implicit provider work on passive reads.

Follow the repository's Jev consultation rules for genuinely difficult decisions: provide complete evidence, make three fresh fully reworded equivalent consultations, save requests/responses and investigate disagreements. Agreement is advice, not proof. Prefer deterministic code and supported Jev primitives where appropriate. Avoid repeated consultation or review of already settled facts.

COORDINATE CONCURRENT WORK

Use yourself as coordinator plus at most three concurrent workers. If delegation is unavailable, perform the same ordered tasks yourself.

Dispatch only tasks whose prerequisites have landed. Give each worker its task IDs, exact writable files, integrated dependencies, required regression evidence and completion format from the plan.

Maintain one writer per file. I owns shared API/schema/workflow integration; F owns the handwritten frontend client and shared UI/navigation seams. Other workers propose changes to those owners. Record ownership transfers before editing. Resolve integration failures promptly and keep useful independent work moving.

KEEP A DURABLE LEDGER

Maintain dashboard/docs/implementation-progress-2026-09-26.md with the goal, baseline, file ownership, ready/active/completed/blocked tasks, integrated dependencies, commit hashes, meaningful verification and precise remaining limits. Update task checkboxes only when their acceptance evidence exists. Persist the next ready actions before any context handoff.

COMMIT EARLY AND OFTEN

Make the first small commit as soon as the initial goal/baseline/ownership ledger is recorded. Commit each coherent contract change, regression case, cleanup slice, backend repair, UI repair and integration checkpoint. Do not accumulate an entire lane or wave into one large commit. During sustained work, aim for a useful commit roughly every 15–30 minutes when there is a coherent checkpoint.

Use descriptive messages with task IDs, for example:
- docs(I0): record implementation goal and file ownership
- refactor(M1): remove independent pack preparation path
- fix(K3): commit accepted answers and required blanks
- fix(A1): persist untouched kept suggestions
- test(Q1): cover questionless route through canonical Handoff

Stage exact task-related paths or hunks; do not blindly git add -A, commit unrelated user changes, reset the worktree or rewrite existing history. One coordinator serializes commits in a shared checkout. Isolated worker branches may commit their own scoped changes; integrate them in dependency order. Record actual commit hashes in the ledger.

Run the meaningful focused checks for a checkpoint before committing. A deliberately failing regression may be committed as a clearly labeled test checkpoint, with its expected failure documented, but required integration gates must pass before dependent implementation proceeds. Frequent commits must remain understandable and reviewable; commit count is not acceptance evidence. Do not push, deploy or publish unless separately authorized.

PROVE COMPLETION

Use production HTTP handlers and a real temporary store/schema for the connected acceptance tests. Replace only provider/process boundaries in controlled tests. Do not seed a reason catalog, an answered workflow or ready artifacts to bypass the failures under review.

Prove the same job, check, answers and artifact identities through discovery, selection, checking, answer commit, one preparation system, exact edits, targeted rewrite, matching exports and the saved-job/manual Handoff list.

Cover fresh/new-version catalogs, duplicate sightings, untouched suggestions, edited/blank answers, verified zero-question routes, actual portal text/upload requirements, owner-fact hold/resume, partial and outdated materials, input invalidation, lost acknowledgments, two-job isolation, second-session recovery and keyboard/narrow-screen usability. Passive reads, reason views and exact edits must cause zero provider work; employer actions must remain unreachable.

Run the prescribed retained suite, type checks, build and generated-contract checks. Record real configured Contributor/Jev/Standard evidence under Q3 without contacting an employer. Report an unavailable provider or route as an exact pending gate; fixture success cannot close a live gate.

Keep working until all task acceptance criteria and Q4 are satisfied. If a real blocker prevents completion, document the exact blocker and finish every independent authorized task. Do not mark the goal complete while a required gate remains pending. Avoid optional review loops once demonstrated failures are resolved and required gates pass.

At completion, report the finished task IDs, commit range, connected/UI/live verification, link to the progress ledger and any material limitation. Mark the persistent goal complete only when the required outcome is actually demonstrated.
```
