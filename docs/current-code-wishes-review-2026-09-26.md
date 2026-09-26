# Current code against the owner's wishes

26 September 2026 · Review only · Reviewed commit `ef463c48035f392848c676eea06d6f4e30751307`.

The resulting [implementation tasks and concurrent lanes](current-code-implementation-lanes-2026-09-26.md) map every finding below to ordered work and acceptance evidence.

## Conclusion

**The requested product is substantially more recognizable, but the implementation cannot yet complete the intended journey reliably.** All seven areas now have code. Deterministic goals, recommendation tabs, explicit selection, Contributor checking, Jev answer matching, exact artifact editors and an individual Handoff page are present. The remaining gap includes broken stage transitions, missing production dependencies and two separate preparation systems. It is larger than finishing the appearance of the screens.

The clearest blockers are:

1. Fresh searches and new goal versions have no production path to create the reason catalog required for Jev classification.
2. Answers navigates to Prepare without committing the backend workflow stage; both preparation paths reject that stage.
3. A verified vacancy with no employer questions is blocked, even when its email/upload route and required documents are known.
4. Main preparation/rewrite and the artifacts used by Handoff operate on different saved objects.
5. Stored artifacts can remain “ready” after their answers or checked vacancy change.

**There is no sound percentage-complete estimate from counting files, tasks or passing tests.** The useful distinction is that many individual controls now work, while the complete owner journey still fails. This review does not establish live Contributor, Jev or Standard acceptance, and makes no hours/cost promise.

## Scope and method

The authority is the owner's [seven-step product definition](product-vision.md), their [reference screenshot](assets/connected-prototype-first-use-2026-09-25.png), and the [UI/usability acceptance criteria](connected-prototype-ux-acceptance.md). The last step is manual Handoff. The owner personally opens the employer destination, fills fields, attaches files and sends/submits. The app does none of those actions.

The review covered the actual Vite routes/components/client, normal HTTP registrations and server wiring, Contributor/Standard execution adapters, Jev matching/classification, saved workflow/check/answer/artifact models and relevant tests. Three independent audits covered goals/discovery/selection, checking/answers, and preparation/Handoff; the coordinating review checked their cross-layer conclusions and the remaining runtime/scope surface. Existing backlog completion labels were not treated as proof.

The built My search screen was also inspected in a browser at its default desktop viewport and at **390 × 844** using the repository's synthetic HTTP fixture. Navigation, context order and narrow wrapping were observed; the narrow document width equalled its viewport width. The fixture lacks the experience endpoint and explicit wants, so its 503 experience panel is **fixture evidence only**, not a finding against the real API. No saved product data was changed. The temporary browser/server were closed.

Colors, fonts, borders, corner radii, shadows and exact pixel spacing were excluded. No implementation edits, new tests, provider calls or employer contact were performed. The reviewed app/contract/script sources remained unchanged from the reviewed commit.

### Evidence labels

- **Present:** the component or normal runtime path exists and has supporting source/test evidence.
- **Partial:** useful behavior exists, but a specified control, transition or state is incomplete.
- **Blocked:** a concrete code path prevents the requested normal journey.
- **Missing:** no implementing path was found.
- **Live unverified:** source and fixtures do not prove a real provider run or employer route walkthrough.

## Seven-step assessment

| Owner's step | What exists now | Remaining gap | Assessment |
| --- | --- | --- | --- |
| **1. Regular wants/don't-wants forms** | Labeled deterministic form, saved PUT contract, version conflict handling, separate experience/wants panels. | Saving does not refresh discovery's independent context. Experience is reusable answers rather than sourced CV context. Administration precedes the main action. | **Partial; core form present.** |
| **2. Free Contributor discovery + five Jev categories** | Normal HTTP discovery is connected to pinned Contributor `muse exec`; sources/queries are agent-selected. Finite scope, saved activity, stop/resume and five group views exist. | No production catalog authoring; duplicate vacancy identities across passes; browser-local run recovery; retained session/MCP machinery. | **Blocked for fresh/new-version classification; live unverified.** |
| **3. Owner selects interesting jobs** | Saved owner decisions across groups, saved reasons/evidence, explicit Check chosen jobs and View selection. Selection alone performs no deeper research. | Cards omit employer and lose arrangement data; action includes ineligible saved roles; choice model and growing sticky list differ from the prototype interaction. | **Partial, with working selection persistence.** |
| **4. Contributor deeper vacancy/application check** | Normal server wires selected-role Contributor checking. Source spans, route/documents/requirements and real employer questions are saved; selection/revision gates exist. | Questionless applications are blocked; completeness is unconditionally stamped complete; unavailable performer can leave checking forever; focused owner clarification is absent. | **Partial with a material route blocker; live unverified.** |
| **5. Jev prefill; owner edits/adds answers** | Real Jev API path, approved saved versions + no-fit, editable boxes and exact PUT saves. No LLM on this answer path; no silent reusable-library approval. | Untouched suggestions are not persisted, rematching/navigation loses dirty text, and continuation never calls workflow commit. Blank-required commit policy conflicts with Standard drafting. | **Blocked at the transition to step 6.** |
| **6. Standard drafts route-needed materials** | Direct Standard CLI wiring; independent CV/letter/email text versions, prefilled exact editors, matching text copy/download and activity journal. | Pack and route-artifact systems are disconnected; artifact rewrite missing; requiredness ignores optionality/negation; substantive vacancy requirements omitted; weak evidence validation; stale readiness. | **Partial and blocked by step 5; live unverified.** |
| **7. Manual instructions + list of jobs with artifacts** | Individual `#/jobs/:id/handoff` page, verified destination/excerpt, manual checklist, saved artifact content/version display and copy/download. No registered delivery/SMTP/employer submission path was found. | Advertised links open application detail, not Handoff; no separate saved-artifact job list; no reachable terminal Handoff workflow state; partial/stale saved work can be hidden. | **Partial; manual-only boundary present.** |

## Findings that prevent completion or affect accepted work

P1 here means a normal requested journey is blocked, owner choices can be lost, or supposedly current materials can be wrong. These are source-confirmed findings; they are not claims that a live provider was exercised in this review.

### R01 · P1 · No production authoring of the classification catalog

[ClassifyVacancy](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/classify.go:107) requires a reason catalog for the current profile version and returns an error when it is absent. Exhaustive production-source searches found the [AuthorReasonCatalog definition](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/reasoncatalog.go:220) and its SQL insert, but no production caller. [Goals saving](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/preferences.go:142) updates preferences; [commissioning](/Users/vince/Projects/find-income/dashboard/apps/api/internal/researchservice/researchservice.go:190) resolves the brief and starts discovery without authoring the catalog.

**Owner effect:** first use and a new saved goal version cannot classify returned vacancies unless someone externally seeds that version's catalog. The UI promise that the first run authors the necessary search basis is incomplete. Test fixtures seed catalogs, so their successful classification does not cover this missing step.

### R02 · P1 · Save goals does not refresh Find jobs readiness

[SearchPage](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/SearchPage.tsx:169) refreshes its own owner-context instance after a save. [DiscoverySection](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/discovery-section.tsx:140) creates another instance, backed by [component-local useRead state](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/useRead.tsx:30).

**Owner effect:** saving the first explicit want can leave Find jobs disabled with “No wants … saved” until remount/reload. Later edits leave old brief/version or correction-readiness information on the commission panel. The server resolves current preferences when commissioning; this is a stale UI gate, not proof that the server searches old goals.

### R03 · P1 · Answers → Prepare never commits the workflow

[PrepareContinuation](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.tsx:599) saves only dirty boxes, then [changes the hash](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.tsx:622). No Vite caller invokes the registered `POST /answers/commit`. A successful check sets `checked`; [saving an answer](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/answervalues.go:302) advances only to `answering`. [Both preparation paths' shared gate](/Users/vince/Projects/find-income/dashboard/apps/api/internal/materialprep/service.go:281) accepts only `answered`, `preparing` or `prepared`.

**Owner effect:** even saving every answer through the UI cannot advance to normal preparation; the preparation request conflicts. The backend commit endpoint exists, but the UI does not use it.

There is also a policy conflict inside [CommitRoleAnswers](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/answervalues.go:388): every required answer must already have nonblank saved text. This prevents the advertised choice to leave a required box blank for Standard to draft from verified facts. Adding a commit call alone would not resolve that case.

### R04 · P1 · Keeping a Jev suggestion does not save that choice

[AnswerCard](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.tsx:738) seeds the box from the suggestion. [useAnswerSave](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/useAnswerSave.tsx:61) also seeds its “saved” baseline from that text, even when the saved answer version is zero. Untouched suggestions therefore appear clean, have Save disabled, and are skipped by continuation. An unchanged new blank is skipped too.

**Owner effect:** leaving a visible suggested answer alone does not persist acceptance of it. Backend commit/preparation reads `answer_values`, not the displayed match; it sees no saved choice. The [existing test](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.test.tsx:844) explicitly expects clean-suggestion continuation with GET-only traffic, so this defect currently passes the suite.

### R05 · P1 · Rematching or navigation can discard dirty answers

A successful [match action](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.tsx:457) retries the read. [useRead](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/useRead.tsx:32) switches to loading; [the answer list](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/answers/AnswersPage.tsx:157) unmounts, taking its local draft text with it. Ordinary hash/job/sidebar navigation also has no draft cache or dirty-state guard. Bulk continuation leaves textareas editable while awaiting previously captured save closures.

**Owner effect:** type an unsaved answer, then successfully rerun matching or change page: the text can disappear. Text entered during continuation can also be lost when navigation succeeds.

### R06 · P1 · A complete application with no questions is blocked

[The Contributor checker](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/check.go:247) treats zero verified employer questions as `questions_unresolved`, regardless of whether the vacancy really has no questions. [saveBlocked](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/check.go:392) saves the reason without the otherwise verified route, documents or requirements. The workflow becomes blocked; [Prepare](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/PreparePage.tsx:209) refuses that state.

**Owner effect:** a legitimate vacancy that simply asks for a CV/letter by email or upload cannot proceed. “No employer questions required” must be distinguishable from “questions could not be verified.” The owner wanted route-dependent materials; questions are not a universal prerequisite.

### R07 · P1 · Ready artifacts are not tied to their current supporting evidence

[Artifact readiness](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/artifact_readiness.go:259) treats an existing stored artifact as ready without comparing its answer references to current answer versions or pinning its originating check/profile/vacancy/source revisions. [ArtifactView](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/artifacts.go:59) lacks those revision pins. [Drafting](/Users/vince/Projects/find-income/dashboard/apps/api/internal/materialprep/artifactdraft.go:259) selects only required held types.

**Owner effect:** after changed answers or a completed recheck, an older CV/email can again appear ready/current. Draft held artifacts then skips it. Handoff can recommend the old content. Version storage exists, but readiness does not establish that the displayed version fits today's inputs.

### R08 · P1 · Main preparation and rewrite do not produce Handoff's object

The main Prepare application action creates combined material packs. [Request rewrite](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/PreparePage.tsx:841) calls `/materials/rewrite` for that pack. Handoff reads separate `opportunity_artifacts`; [the registered artifact endpoints](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/handler.go:197) have drafting/read/exact-save but no per-artifact rewrite. A separate Draft held artifacts action produces those route items.

**Owner effect:** one preparation action does not deliver the coherent route-needed set described by the workflow. Rewriting the motivation email in the pack does not update the email used by Handoff. There are two readiness/version histories. The available pack PDF is also not an export of the separately edited route CV; the latter currently downloads as text.

### R09 · P1 · Required materials are inferred without optionality or negation

[documentBasis](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/artifact_readiness.go:105) matches document labels, attachment questions and requirement keywords but ignores document/question requiredness and statement polarity. [Form-value derivation](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/artifact_readiness.go:271) includes every checked question, including attachments.

**Owner effect:** an optional CV can be demanded, or “cover letter is not required” can demand a letter. An upload question can appear as a text form value. This falls short of preparing only what the verified application actually calls for.

### R10 · P1 · The route artifact drafting request omits substantive vacancy context

[DraftOpportunityArtifacts](/Users/vince/Projects/find-income/dashboard/apps/api/internal/materialprep/artifactdraft.go:311) sends title/company, destination, document labels and owner facts/answers. It omits the resolved vacancy description and checked requirements, although those are available to preparation.

**Owner effect:** Standard is missing the duties, skill requirements and other detail needed to tailor the CV/motivation to the vacancy. An answer-less job would give it particularly little vacancy-specific material even after R06 is fixed.

### R11 · P1 · Artifact grounding validation accepts drafts without supporting facts

[checkModelArtifactDrafts](/Users/vince/Projects/find-income/dashboard/apps/api/internal/materialprep/artifactdraft.go:416) validates text bounds and membership of supplied fact/answer IDs. Empty reference lists are accepted; valid IDs do not establish that claims in the content follow from those sources. No claim/span verification or assessment is applied on this artifact path.

**Owner effect:** the code cannot establish the required “verified owner facts only” condition for accepted route drafts. This is a validation gap, not an observation that a live model hallucinated. The prompt asks for grounded output, and invalid IDs are rejected, but the current tests cover invalid identifiers rather than unsupported claims with valid/absent references.

### R12 · P1 · Repeat discovery creates another identity for the same job

[Classification](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/classify.go:128) always creates a company and opportunity. [CreateOpportunity](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/opportunities.go:326) inserts a fresh random ID without matching an existing vacancy. [Public saving](/Users/vince/Projects/find-income/dashboard/apps/api/internal/publicresearch/save.go:102) deduplicates an idempotency key within its run's server, not the job identity across runs.

**Owner effect:** Find more can show the same vacancy as another job, with its previous choice/check/artifacts attached to the older identity. Duplicate-free continuation and one durable owner decision per vacancy are not implemented.

### R13 · P1 · Exact search recovery depends on this browser's localStorage

[DiscoverySection](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/discovery-section.tsx:120) initializes the tracked run exclusively from localStorage. [Today](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/TodayPage.tsx:355) reads the active server round but links to unparameterized `#/search`; Search does not adopt that round. [The history text](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/discovery-section.tsx:593) admits only the locally tracked latest pass.

**Owner effect:** in a new browser/storage context, Today can say a search is running or paused, while My search opens an idle Find jobs panel with no corresponding activity/Resume. Durable server state exists but is not connected to exact run recovery.

## Remaining UI, usability and runtime gaps

P2 means a material mismatch or incomplete recovery/control surface, without claiming every case blocks the entire product. These are part of closing the UI/UX gap, not styling requests.

| ID | Finding and owner effect | Code evidence |
| --- | --- | --- |
| **R14 · Stale/false shell status and navigation** | Shell reads active round/workflows once and does not invalidate on mutations/navigation. Start/stop can leave stale status; selecting the first job does not add Applications until reload. “Ready to find jobs” ignores missing goals/Contributor availability. The browser fixture visibly showed Ready while Find jobs was disabled. | [App reads](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/App.tsx:82), [status fallback](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/App.tsx:203), [useRead lifecycle](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/useRead.tsx:59) |
| **R15 · Primary Find jobs action is displaced** | Profile facts and the large free-text correction form come before discovery; diagnostics and brief/version administration precede commission inside discovery. The reference's main task follows the two context panels. Change goals in commission points to the same page without opening/focusing its editor. | [Search render order](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/SearchPage.tsx:172), [discovery administration](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/discovery-section.tsx:419), [Change goals](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/discovery-section.tsx:470) |
| **R16 · Experience is not sourced CV context** | Your experience lists reusable answer text/tags/version, with no CV/source references. With CV facts but no reusable answers it shows no experience. The two panel separation exists; the left panel's content does not meet its purpose. | [ExperiencePanel](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/goals/ExperiencePanel.tsx:11), [rendered metadata](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/goals/ExperiencePanel.tsx:55) |
| **R17 · Progress is not the actual seven-step journey** | Search always marks Your goals active; Jobs has no stage row; completed discovery gives no explicit Select jobs continuation. The backend retains reviewing/sent instead of a terminal Handoff state, with no production transition into those removed contact states. An actual Handoff page for a prepared job still marks Prepare active. | [Search progress](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/SearchPage.tsx:142), [JobsPage](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/JobsPage.tsx:3), [role mapping](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/role-stages.tsx:66), [transition table](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/role_workflow.go:30) |
| **R18 · Handoff navigation/list is incomplete** | Open handoff and Saved materials and handoff open `#/applications/:id`, whose Saved packs list has no full-current-artifact links. Prepared-role continuation returns to Prepare. The real individual Handoff page exists, but the separate list of jobs with their saved artifact types/versions is missing. | [Prepare link](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/PreparePage.tsx:928), [Applications link](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/ApplicationsPage.tsx:116), [pack list](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/ApplicationsPage.tsx:178), [continuation](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/application-continue.tsx:51) |
| **R19 · Unavailable checker can be reported as checking forever** | HTTP persists a check before testing for an authorized performer. No performer, unauthorized performer, or a non-Muse-backed role can return successful checking without anyone doing the work. Normal UI readiness gating does not fix API truth. | [startOpportunityCheck](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/checkanswer.go:130), [deliberate pending test](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/checkperform_test.go:52) |
| **R20 · Check completeness overclaims evidence** | Capture metadata is discarded, then checked vacancy completeness is always `complete`. Adapter verification gaps become nonconsequential `other` gaps. Verified individual quotes do not establish that the whole vacancy/application form was captured. | [adapter read](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/checkadapter.go:100), [completeness stamp](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/check.go:267), [gap mapping](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/check.go:369) |
| **R21 · Focused owner clarification is missing** | There is no dedicated source/origin, saved question, owner input and dependent-work resume path for a genuinely unknown personal fact. `missing_fact` is a generic gap. Actual employer questions are evidence checked; this finding does not claim they are being invented or mislabeled. | [check prompt/output](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/live.go:99), [question model](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/jobcheck.go:110) |
| **R22 · Held/stale partial materials are hidden** | One missing required form value hides all saved partial values on Prepare/Handoff. An outdated/blocked check makes Handoff return before showing prior saved artifacts. The owner loses inspection/copy access instead of seeing saved work with honest labels. | [form rendering](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/ArtifactsSection.tsx:336), [Handoff early returns](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/handoff/HandoffPage.tsx:214) |
| **R23 · Artifact lost-response recovery is incomplete** | Exact-save retries create new request keys without reading whether the previous save committed. A lost acknowledgment can turn a successful edit into a conflict against the old version. The store also replays an existing key without checking changed content/basis. | [editor save](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/ArtifactsSection.tsx:483), [store replay](/Users/vince/Projects/find-income/dashboard/apps/api/internal/store/artifacts.go:137) |
| **R24 · Check action includes ineligible work and grows unbounded** | Every saved workflow enters the chosen list; no filter restricts the bulk action to fresh eligible choices. It can recheck/error on checked/preparing/archived jobs. The sticky area contains every selected-role row and can dominate a narrow viewport as selections grow. | [chosenRoles](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/grouped-jobs.tsx:666), [button gate](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/check-chosen-jobs.tsx:152), [sticky rows](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/check-chosen-jobs.tsx:179) |
| **R25 · Cards omit employer and lose work arrangement** | Job cards display title/kind/work pattern/location but no employer name. Public vacancy saving has no work-pattern field; classification leaves it unset, producing unknown even when captured text describes remote/hybrid. The owner must open evidence for essential comparison facts. | [card lead](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/grouped-jobs.tsx:172), [vacancy shape](/Users/vince/Projects/find-income/dashboard/apps/api/internal/publicresearch/save.go:30), [opportunity conversion](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/classify.go:193) |
| **R26 · Selection and result controls differ from the prototype** | Cards use Select/Shortlist/Pass instead of the consistent checkbox-style choice. Jobs has no Edit goals/Find more controls beside results. Run Stop/Resume/Find more/Steer buttons disable for busy state rather than valid run state, exposing irrelevant actions on terminal runs. | [choice buttons](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/grouped-jobs.tsx:367), [JobsPage](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/JobsPage.tsx:3), [run controls](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/research-controls.tsx:208) |
| **R27 · Recommendation tab keyboard semantics are incomplete** | Buttons with tab roles have no arrow-key/roving-focus behavior or tab-to-panel ID association. Tab/Enter access still works, but the advertised tab interface is incomplete for keyboard/assistive use. | [tablist/panel](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/grouped-jobs.tsx:870) |
| **R28 · Jev support appears measured when it is not supplied** | Classifier records support as zero and the UI calls it “Jev support signal.” This displays a number as if it were an assessment result rather than unavailable support. | [classifier mapping](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/classify.go:251), [reason display](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/discovery/grouped-jobs.tsx:331) |
| **R29 · Lexical prefilter can hide a relevant saved answer from Jev** | Candidates must share tokens with scope tags/context notes, and are truncated to eight. Answer text itself is not used for this ranking. A relevant answer with different wording or sparse tags can never reach Jev, leading to no-fit despite a usable approved answer. | [HTTP candidate construction](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/answermatch.go:212), [prefilter](/Users/vince/Projects/find-income/dashboard/apps/api/internal/jev/answer_match.go:506) |
| **R30 · Subtraction of session/duplicate runtime ceremony is incomplete** | Contributor and Standard do execute direct CLIs, but discovery/check still construct a required per-run loopback MCP server and isolated MCP configuration. Server still constructs lazy Codex/App Server, generic rounds and connects old research toolchain/supervisor machinery; Codex connect/MCP endpoints remain registered. This conflicts with the product definition's explicit removal, although direct CLI execution is now real code rather than a harness-only placeholder. | [direct exec](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/live.go:372), [loopback server](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/live.go:304), [required MCP config](/Users/vince/Projects/find-income/dashboard/apps/api/internal/musewire/live.go:655), [old runtime wiring](/Users/vince/Projects/find-income/dashboard/apps/api/cmd/server/main.go:81), [registered old endpoints](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/handler.go:113) |

### Smaller first-use differences

- The default route is [Today](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/routes/useRoute.tsx:29), rather than My search for the saved-before-run opening state.
- The sidebar foot shows [session expiry](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/App.tsx:138), not owner identity.
- The goals editor asks for [monthly pay in cents](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/goals/GoalsForm.tsx:311), requiring an implementation-unit conversion for an ordinary search choice.
- Find jobs' [stage actor label](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/pages/SearchPage.tsx:23) omits Jev's role there.
- The combined-pack [exact editor](/Users/vince/Projects/find-income/dashboard/apps/vite-app/src/features/prepare/PreparePage.tsx:774) still starts empty; the independent route-artifact editors are correctly prefilled.

## What is already worth retaining

- The deterministic goals read/write contract and owner choice/past-context separation.
- Source collection with agent-selected URLs/queries, public-page capture and explicit bounded starts.
- Five category rendering, counts, saved reason/evidence displays, and explanations that start no new model work.
- Saved owner decisions and selection/check separation; selection/revision gates and cross-job question isolation.
- Verbatim check-span verification, actual employer question extraction and saved route/document structures.
- Approved-answer/no-fit Jev matching and exact answer PUTs without an LLM or silent library approval.
- Immutable stored artifact versions, prefilled route-artifact exact editors and copy/download of the exact displayed text.
- Individual Handoff destination/evidence/manual checklist, GET-only passive reads, minimal authentication/CSRF.
- The removal of registered delivery/SMTP and reply/interview/offer workflows. [API registration](/Users/vince/Projects/find-income/dashboard/apps/api/internal/httpapi/handler.go:148) and [server wiring](/Users/vince/Projects/find-income/dashboard/apps/api/cmd/server/main.go:146) expose no explicit employer send/submit/autofill/attachment action in the inspected product paths. This is a meaningful completed scope change.

These pieces should be assessed by their current consumers. Their existence does not justify rebuilding everything, and their fixture passes do not establish complete product acceptance.

## Verification performed

| Check | Result | Practical limit |
| --- | --- | --- |
| Existing Vite suite | **301 tests / 28 files passed.** Command: `NODE_OPTIONS=--no-experimental-webstorage bun run --filter @jobseek/vite-app test`. | Host Node is 26.8.1, while repo pins 24.21.0. The first unadjusted run failed on unavailable experimental localStorage. The flag resolved that environment issue without changing code. Timeout-overflow warnings remain. |
| Existing Go suite | **27 tested packages passed; 2 command packages had no tests.** `go test ./...`, with `E11_VALIDATE`, `E11_ECHO`, `E13_ECHO`, `T16_LIVE_CHECK`, `T02_PROOF_LIVE` set to `0`. | Initial sandbox run failed in browser/Python fixture packages because nested `sandbox-exec` returned “Operation not permitted.” The same local suite passed outside that nested sandbox. Live provider tests stayed disabled. |
| Vite targeted typecheck | **Passed:** `bun run --filter @jobseek/vite-app typecheck`. | Root `bun run typecheck` fails because Turbo defines no typecheck task. This is a verification-script defect, not a product feature failure. |
| Vite production build | **Passed:** `bun run --filter @jobseek/vite-app build`. | Compilation/bundling does not verify runtime transitions or source-grounded output. |
| Generated TS contracts | **Passed:** `bun run --filter @jobseek/contracts check:generated`. | Matching generated types does not establish correct workflow semantics. |
| Browser opening screen | Desktop and 390px narrow fixture inspection completed; narrow page had no horizontal document overflow. | Synthetic fixture only. No full keyboard/screen-reader or many-selected-jobs viewport walkthrough. |
| Real Contributor/Jev/Standard journey | **Not performed by this review.** | No claim of live Q1–Q3 acceptance, real route coverage or model output quality. |

### Why passing tests do not close the gap

The suites mostly isolate surfaces and seed prerequisites. Classification fixtures author catalogs themselves. Prepare fixtures start at an already answered state. Answers tests explicitly permit an untouched suggestion to navigate without writing it. Browser [slice 3](/Users/vince/Projects/find-income/dashboard/scripts/ui-smoke/slice3-smoke.mjs:8) and [slice 4](/Users/vince/Projects/find-income/dashboard/scripts/ui-smoke/slice4-smoke.mjs:8) use separate canned HTTP doubles. They do not prove that the normal backend accepts the transition between those slices, or that the pack and Handoff artifacts refer to the same content.

## What must be demonstrated before calling the gap closed

This is a review outcome, not authorization to implement.

1. A fresh owner saves goals, immediately starts discovery, receives a generated once-per-version catalog and real classified deduplicated vacancies, then returns to the same run from another session.
2. The owner selects one real job and checks its real requirements/destination; a verified no-question route can proceed, while unknowns stay explicit.
3. The owner keeps a Jev suggestion, changes another answer and leaves an appropriate box blank; every choice survives navigation and the continuation advances the same job correctly.
4. One preparation flow produces the route-needed current materials using the substantive vacancy and verified owner facts. Exact editing and an explicit rewrite change the same artifacts later shown/exported by Handoff.
5. Changing an answer or rechecking the vacancy invalidates affected material readiness, while prior saved content remains inspectable.
6. The saved-artifact job list leads directly to each job's current Handoff content and verified manual steps, with accurate active stage/status and no app employer action.
7. The opening hierarchy, result controls, selection area, editors and Handoff work at desktop/narrow widths and by keyboard, using real backend state as well as fixtures.

**The current code is a useful partial implementation of the desired product. It is not yet the UI/UX and working end result the owner asked for.**
