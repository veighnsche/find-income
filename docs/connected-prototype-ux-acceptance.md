# Seven-step UI and manual handoff acceptance

26 September 2026 · Planning and acceptance only. No app implementation, code deletion, paid service call or employer contact is authorized by this document.

## Scope and reference

This is the definition of done for the [concurrent implementation task list](connected-prototype-ux-parity-tasks.md), applying the owner's [latest product definition](product-vision.md). The [prototype screenshot](assets/connected-prototype-first-use-2026-09-25.png) specifies the opening screen's structural grouping and information priority, **not** its styling or the meaning of its last stage. The approved [B first-use flow](../../design/frontend/05-b-refinement/walkthrough.md) and [connected prototype](../../design/frontend/15-connected-design/README.md) offer historical UI examples; the owner's current seven-step instruction prevails where they conflict. The [earlier difference audit](prototype-ui-ux-differences.md) documents useful present-screen observations but still describes a superseded six-step/always-three-artifact target.

The required stages are **Your goals → Find jobs → Select jobs → Check job details → Answer questions → Prepare materials → Handoff**. “Handoff” is a working label; the seventh stage must be a **separate saved-artifacts/how-to-apply page**, never Review & send. Step 1 uses ordinary deterministic wants/don't-wants form controls. Steps 2 and 4 invoke **Muse Spark 1.3 Contributor CLI directly**. Step 2 uses Jev's five result categories. Step 5 uses Jev for saved-answer suggestions with owner editing/addition. Step 6 invokes **Muse Spark 1.3 Standard CLI directly** for the materials the verified vacancy route actually needs. Step 7 saves those artifacts per job and tells the owner how to apply manually. The app **never** fills/autofills an employer-site form, attaches files there, emails, submits a portal or contacts an employer. The owner does those external steps personally.

Pass means matching screen groups, relative order, task visibility, controls, transitions, evidence and owner effort through these seven stages. Colors, fonts, borders, corner radii, shadows and pixel spacing need not match. Fictional Alex/jobs, preview banners, checkpoint/reset/layout controls, scripted completions and illustrative Jev scores are not product requirements. A screenshot match or fixture click does not prove a live CLI run.

**Status:** Every check below needs implementation evidence. Existing components may meet part of a check; the task list separates reuse, adaptation, missing capability and runtime blocker.

## Blocking subtraction gate before seven-step feature work

First complete the small P0 retain/cut/shared-dependency map, then remove the complete out-of-scope workflows in S1–S5 and pass S6. **No first-use, discovery, Check, Answers, Prepare or Handoff feature task starts before this gate.** The map protects the seven-step data and runtime pieces that the new flow needs; it does not set a deletion percentage.

| ID | Required evidence for Q-S | Tasks |
| --- | --- | --- |
| **S01 · Concrete map** | List exact retained, cut and shared files, registered routes, tables/foreign keys/triggers, generated contracts, tests and active docs; record import/call consumers and measured candidate lines before deletion. Assign one owner for each shared file. | P0 |
| **S02 · Contact workflow gone** | Review, approval, send, SMTP, attempt/outcome/reconcile/retry and contact-only UI/API/service/store/schema/test/contract paths are removed as a complete workflow. No old route or callable endpoint can email, submit, attach or fill an employer site. If no employer-site autofill module exists, record the negative search and reachability probe rather than inventing a deletion. | S1, S2, S5 |
| **S03 · Later lifecycle gone** | Reply/correspondence, interview and offer workflows are absent across their registered API, services, stores, schema, tests, generated contracts and active guidance. Any generic rounds/actions retained have a documented seven-step consumer. | S3, S5 |
| **S04 · One necessary runtime path** | Duplicate commissioning, unsupported profile/session ceremony and redundant research data paths are removed where P0 proves they are unnecessary. Retained public capture, Jev evidence, bounded direct CLI execution, authentication/CSRF and saved owner/job/material records still work or report a truthful unavailable state. | S4, S5 |
| **S05 · Clean retained base** | Compile/route/schema checks pass, active docs describe this product, and the actual diff reports measured cuts by subsystem without a target percentage or compatibility path. Saved goals, listings/evidence, choices, checks, answers and material reads are not orphaned. | S5, S6 |

## 0. Opening structure from the screenshot

When the owner has saved search choices and no active run, My search opens in the screenshot's task-first arrangement. The last stage label/action is changed to manual Handoff. When explicit choices are missing, the wants/don't-wants form becomes the active content within What you want next; it is not a chat or profile-approval ceremony.

```text
┌─ product sidebar ──────────────┬─ product work-status strip ─────────────────────┐
│ Today                          │ Your job search             Ready to find jobs │
│ My search  ← active             ├────────────────────────────────────────────────┤
│ Jobs                           │ Let's find your next role                      │
│ Applications after selection   │ seven stages; Your goals active               │
│                                │ ┌ Your experience ┐ ┌ What you want next ┐     │
│                                │ └─────────────────┘ └────────────────────┘     │
│                                │ ┌ Find jobs worth a closer look ─────────┐     │
│                                │ │ scope · saved progress · no contact    │     │
│                                │ │ Find jobs       Edit/Change goals       │     │
│                                │ └────────────────────────────────────────┘     │
└────────────────────────────────┴────────────────────────────────────────────────┘
```

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **F01 · Frame** | A desktop left sidebar puts product identity above Today/My search/Jobs, owner identity at the foot, and Applications after chosen work exists. A separate slim top strip names Your job search and reports accurate work status. My search is active in the before-run state. Narrow-screen navigation remains usable. | B1 |
| **F02 · Orientation** | Directly below a short introduction is a seven-stage row with **Handoff** (or equivalent manual label) in the seventh position. Exactly Your goals is active initially; Contributor/Jev/Standard work is labeled at the relevant stages. There is no Review & send action. | B1 |
| **F03 · Distinct context** | Desktop panels place sourced **Your experience** beside **What you want next**. The former contains supported past facts and source; the latter shows saved explicit wants/don't-wants and an edit path. They stack in that order on narrow screens. Past experience is not silently turned into a future preference. | A1, B2 |
| **F04 · Deterministic choices** | Regular form controls capture what the owner wants and rejects in the search, with clear labels, saved values, edit/revisit and explicit status. A missing consequential preference is requested in this form; known CV facts are shown/prefilled where appropriate, not retyped. The form does not require a chat prompt or approval of a generated profile. | A1, B2 |
| **F05 · Primary commission** | After the contexts, one prominent panel explains what the public search will return, its true finite scope, saved progress and no employer contact. Find jobs is the main action with Edit/Change goals beside it on desktop. With saved choices and comparable content, both are in the opening desktop view; technical IDs, quotas and service diagnostics do not displace them. An unavailable CLI or missing consequential choice gives an exact blocker, not false readiness. | A2, B2 |

The older B study specified a 20-minute scope while the screenshot omitted it. Show the **actual** finite search boundary before commission; reconcile its exact value with the direct CLI behavior instead of silently promising 20 minutes or allowing an unbounded run.

## 1. Goals, real discovery and selection

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **G01 · Saved goals** | Explicit wants/don't-wants survive refresh and govern the search brief. The owner can change them deterministically; a prior inference conflicting with an explicit choice is corrected/identified without requiring the owner to restate it. Opening/reading the page starts no research. | A1, B2 |
| **T01 · Exact return** | Today leads to the same idle/running/stopped/empty run, results, selected-job task, missing owner fact, preparation or saved Handoff package. One role may be ready while another waits. Passive return commissions nothing. | A6, B3 |
| **J01 · Direct Contributor discovery** | For discovery, only Find jobs starts direct Muse Spark 1.3 Contributor CLI work. Contributor chooses public boards, APIs, company pages and queries without a fixed source universe or owner source form, saves actual listing URLs/evidence and deduplicates. Jev classification follows collection. Readiness/activity truthfully shows source, phase, result, status/error/input; no simulated percentages or false live claim. | A2, C1 |
| **J02 · Stop/continue** | Stop/Pause preserves assessed partial work; Resume continues the same bounded run. Empty, disconnected, failed and stopped states say what is saved and what can happen next. A repeat Find more pass adds deduplicated jobs without erasing choices/artifacts. | A2, C1, C4 |
| **J03 · Five Jev groups** | Results use **Recommended, Might recommend, Might not recommend, Not recommended, Unknown** as five supported categories. Switchable views show counts/chosen counts and preserve choices. Unknown is used rarely, only for a genuine lack of basis for even a provisional classification; it appears as a view when populated. No known hard conflict is hidden by a numeric score. | C2 |
| **J04 · Cards and evidence** | Cards foreground employer/title/arrangement, principal saved reason, material conflict or unknown, status and the same checkbox-style choice. Why this job opens saved up-to-three independently supported catalog reasons, Jev support and listing evidence **without** a new model call or a hiring-probability claim. Raw IDs/hashes stay secondary. | C2, C3 |
| **J05 · Persistent selection** | A bottom action area stays reachable on long/narrow lists, shows fresh chosen count, View selection/current work and Check chosen jobs disabled until a fresh eligible choice exists. Choosing alone causes no deep research. A conflicting job can be chosen for that job only without relaxing standing goals. | C3 |
| **J06 · Search change** | Editing deterministic goals/Find more beside results preserves older findings, selections and packages while only affected work is reassessed. A new pass is visibly Find jobs, then returns to Select jobs. | C4 |

## 2. Selected-only deep dive and editable Jev answers

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **AP01 · Chosen-only start** | Check chosen jobs moves selected/started roles into Applications. Exactly Check job details is active for the foreground item; other selected roles have compact independent status/direct routes. No unselected role, including Recommended, receives Contributor deeper research. | A3, D1 |
| **AP02 · Source-grounded vacancy** | Direct Contributor CLI checks the full selected vacancy, consequential requirements, verified application route/destination, required documents and **actual employer form/questions** with source references. A generated question asking the owner for a missing personal fact is labeled **owner clarification**, not falsely presented as an employer question. An unavailable/ambiguous route remains held with its saved evidence; no address or question is invented. | A3, D2 |
| **AP03 · Jev prefill** | For each actual checked question, the UI invokes existing Jev matching over relevant owner-approved saved answers plus no-fit. Exact suggestion appears in an open editable box or remains blank. The owner can keep, edit, replace, clear or add relevant answer text. The Answer stage makes **zero Contributor/Standard/LLM calls** and does not silently add owner text to the reusable approved library. | D3 |
| **AP04 · One safe continuation** | One Prepare materials action commits all visible changed/new/blank answers before Standard drafting. Dirty text is not lost through navigation; failed saves do not falsely advance. Answering or continuing contacts nobody. | A6, D4 |
| **AP05 · One missing fact** | A genuinely unknown personal fact appears as a focused job-specific question with reason/source and preserved work. Today links to it. One saved clear answer resumes only dependent preparation; another ready job remains accessible. | B3, D5 |

## 3. Route-dependent Standard drafting

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **P01 · Direct grounded Standard work** | Step 6 invokes Muse Spark 1.3 **Standard CLI directly** with verified owner facts, saved answer choices and actual route requirements. Its activity names facts/answers used, produced items, checks, errors and ready/held outcome. It may draft a blank required answer only from verified facts, leave optional fields blank and ask for a necessary unknown; it never invents owner claims. | A4, A5, E1 |
| **P02 · Appropriate artifacts** | The app produces **the materials this vacancy actually calls for**: a tailored CV when required; a motivation email or other email when the verified route uses one; a motivation letter when required by the vacancy/route; and exact form-field values for actual fields. Each required item is a distinct complete inspectable artifact/value. Not-required types are labeled or absent honestly; no universal three-artifact checklist is imposed. | A4, E2 |
| **P03 · Exact owner control** | Each produced CV/email/letter/actual form value and answer has a prefilled exact editor. Saving direct edits makes no model call and preserves literal text/version. A separate explicit Standard rewrite creates a grounded, inspectable new version without silently overwriting a usable old one or changing an unrelated job. | A4, E3 |
| **P04 · Take materials out** | Each produced artifact can be copied or downloaded in a useful format; actual form values are saved, editable, copyable text with their question/field labels. Export matches displayed current bytes/version and has clear names. Held partial work remains inspectable without fabricated completion. No automatic employer-site fill is initiated. | E4 |

## 4. Separate Handoff and durable artifacts

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **H01 · Per-job saved list** | Stage 7 is a separate page listing jobs with prepared/held artifacts, types, versions and links to each full current item. It survives refresh/new session and is reachable from Today/Applications without restarting search/check/preparation. | A6, H1 |
| **H02 · Verified manual steps** | For each job, the page shows the actual employer website or email destination, what the **owner** should paste/upload/attach, subject if applicable, and which prepared form values/files map to which real fields. The owner personally opens the site, fills its fields, attaches files, or sends an email. Unknowns are explicit. Opening a link/copying a file never claims submission. | D2, H2 |
| **H03 · No app autofill or contact** | The final state is saved artifacts plus manual instructions. There is no Review & send, Approve, Send, employer-site fill/autofill, automated attachment, portal submission, app email, attempt outcome or callable product endpoint that performs any of those actions. The P0 map precedes removal of complete out-of-scope contact and later-lifecycle workflows. | S01–S05, H3 |

## Cross-screen evidence and accessibility

| ID | Owner-visible pass condition | Tasks |
| --- | --- | --- |
| **X01 · Saved state** | Refresh/deep link/new session recover the same saved goals, run, choices, checked vacancy, answers, current artifacts and Handoff list. Lost acknowledgments are checked against accepted work before another submission. Passive reads start no work. | A6, B3, H1 |
| **X02 · Owner effort** | The owner enters their explicit search wants/don't-wants in deterministic forms, chooses jobs, supplies genuinely unknown personal facts and optionally edits answers/materials. Contributor gathers source and route facts; Standard drafts grounded materials. The owner does not transcribe known CV facts, board lists or vacancy text into administrative forms. | A1, A3, B2, D2–D5 |
| **X03 · Narrow/keyboard usability** | Navigation, goal form, group switcher, job choices/action bar, activity, answer/editor, copy/download and Handoff instructions work at narrow widths and by keyboard with visible focus. Long documents, actual route and held status remain readable without horizontal-only access. Important status persists in text rather than color or a transient toast alone. | B1, C2–C3, D3, E2–E4, H1–H2 |
| **X04 · Proof levels** | Subtraction, screenshot/fixture UI evidence, real direct Contributor research/check, real Jev classification/matching, real Standard drafting and an owner walkthrough are recorded separately. Unsupported CLI readiness is a blocker, not a simulated pass. | P0, S6, A2, A3, A5, Q-S–Q3 |

## Demonstration gates

Record each start state, action, resulting screen, saved identity, source/material evidence and deviation. **Q-S blocks Q0–Q3**. None of these gates is marked complete by this document.

| Gate | Demonstration | Acceptance IDs |
| --- | --- | --- |
| **Q-S · Subtraction first** | Review P0 ledger and measured candidate lines; inspect actual S1–S5 diff; verify deleted UI/API/service/store/schema/test/contract/doc paths, retained imports/data/CSRF/public capture/Jev/bounded CLI pieces, and absence of contact or obsolete lifecycle reachability. Run focused compile/route/schema and negative-contact checks. | S01–S05 |
| **Q0 · Goals and opening view** | Compare desktop/narrow first view to screenshot structure, with Handoff replacing Review & send. Enter/edit deterministic wants/don't-wants, refresh and inspect separate sourced experience. | F01–F05, G01, X01–X03 |
| **Q1 · Real jobs and five groups** | Run direct Contributor on public sources, observe Jev five-category classification/rare justified Unknown, inspect sources/reasons, stop/return/resume, Find more/edit goals and select across groups. | J01–J06, T01, X04 |
| **Q2 · Real selected vacancy and answers** | Choose one real job, invoke direct Contributor deeper check, inspect actual requirements/questions/route, see separate owner clarification if necessary, invoke Jev match, edit/add answer and continue once. | AP01–AP05, X02, X04 |
| **Q3 · Standard materials and manual Handoff** | On that job, invoke direct Standard, inspect/edit/copy/download only route-needed materials and form values, then reopen them from the separate Handoff list and follow verified manual steps. Exercise an email and a portal route when both are available; record absent route coverage rather than inventing a fixture pass. Probe UI/API employer-site autofill, send and submit paths. | P01–P04, H01–H03, X01–X04 |

Closure requires Q-S first, then all four seven-step gates with a real selected listing, plus truthful limits for any unexercised route. A fixed hours budget or a proposed code-deletion percentage is not acceptance evidence.

## Exclusions and remaining wording choice

The prototype's Review & send, SMTP/portal execution, attempt outcomes and later replies/interviews/offers are outside the desired seven-step product. P0 maps dependencies, then S1–S5 remove those complete workflows before feature work; shared code survives only where the map proves the seven steps need it. There is no automatic deletion quota. The exact owner-facing copy for the seventh tab is not settled; **Handoff** is a working label and its separate saved-artifacts/manual-instructions behavior is fixed. The user instruction to extract actual employer questions is reconciled with possible owner clarification prompts by labeling those two question sources distinctly.
