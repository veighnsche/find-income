import {
  RequestError,
  getCurrentAnswerMatch,
  getCurrentOpportunityCheck,
  getCurrentQuestionAnswers,
  getOpportunity,
  getRoleWorkflowOrNull,
  getSavedAnswer,
  type AnswerMatchView,
  type CheckStatusView,
  type QuestionAnswerList,
  type QuestionAnswerValue,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import {
  ActivityDisclosure,
  type ActivityEntry,
} from "@/components/shared/activity-disclosure"
import { StageExplainer } from "@/components/shared/stage-explainer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import {
  useAnswerSave,
  type AnswerBoxState,
} from "@/features/answers/useAnswerSave"
import { formatDate } from "@/pages/format"
import { RoleStageIndicator } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

type CheckDetail = NonNullable<CheckStatusView["check"]>
type CheckQuestion = CheckDetail["questions"][number]
type MatchEntry = AnswerMatchView["matches"][number]

// AnswersPage is the E3 answer-questions surface for one role, reached by
// deep link (#/jobs/:id/answers, registered by the coordinator). Mount,
// reads and reloads are GET-only and commission nothing: no Codex, no LLM,
// no match POST. Every saved suggestion starts inside its own editable box,
// unanswered questions stay blank, and each save is an explicit per-question
// PUT with the version observed from the values read. All state is keyed by
// jobId and the detail section remounts per role.
export function AnswersPage({ jobId }: { jobId: string }) {
  const opportunity = useRead(`answers:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(`answers:${jobId}:workflow`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
  )
  const title =
    opportunity.status === "ready" && opportunity.data.opportunity.title !== ""
      ? opportunity.data.opportunity.title
      : jobId

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to job details
        </a>
      </p>

      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          Answer questions
        </h1>
        <p className="mt-1 text-sm wrap-break-word text-muted-foreground">
          {title} · opening this page only reads saved state; each answer is
          saved explicitly, exactly as typed.
        </p>
      </div>

      <StageExplainer stage="answer" />

      {opportunity.status === "loading" ? (
        <LoadingBlock label="Loading job details…" />
      ) : opportunity.status === "error" ? (
        <ErrorBlock
          title="Could not load this job"
          message={opportunity.error}
          onRetry={opportunity.retry}
        />
      ) : workflow.status === "loading" ? (
        <LoadingBlock label="Loading application stage…" />
      ) : workflow.status === "error" ? (
        <ErrorBlock
          title="Could not load the application stage"
          message={workflow.error}
          onRetry={workflow.retry}
        />
      ) : workflow.data === null ? (
        <EmptyBlock
          title="Role not selected"
          description="This role is not selected, so the server keeps no check, match, or answer state for it. Only chosen roles can be answered."
        />
      ) : (
        <>
          <RoleStageIndicator workflow={workflow.data} />
          <AnswersDetailSection key={jobId} jobId={jobId} />
        </>
      )}
    </div>
  )
}

async function readMatchOrNull(
  jobId: string,
  signal: AbortSignal
): Promise<AnswerMatchView | null> {
  try {
    return await getCurrentAnswerMatch(jobId, signal)
  } catch (cause: unknown) {
    if (cause instanceof RequestError && cause.status === 404) return null
    throw cause
  }
}

async function readValuesOrNull(
  jobId: string,
  signal: AbortSignal
): Promise<QuestionAnswerList | null> {
  try {
    return await getCurrentQuestionAnswers(jobId, signal)
  } catch (cause: unknown) {
    if (cause instanceof RequestError && cause.status === 404) return null
    throw cause
  }
}

function AnswersDetailSection({ jobId }: { jobId: string }) {
  const check = useRead(`answers:${jobId}:check`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )
  const match = useRead(`answers:${jobId}:match`, (signal) =>
    readMatchOrNull(jobId, signal)
  )
  const values = useRead(`answers:${jobId}:values`, (signal) =>
    readValuesOrNull(jobId, signal)
  )

  if (
    check.status === "loading" ||
    match.status === "loading" ||
    values.status === "loading"
  ) {
    return <LoadingBlock label="Loading questions and answers…" />
  }
  if (check.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the check"
        message={check.error}
        onRetry={check.retry}
      />
    )
  }
  if (match.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the matches"
        message={match.error}
        onRetry={match.retry}
      />
    )
  }
  if (values.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the saved answers"
        message={values.error}
        onRetry={values.retry}
      />
    )
  }
  return (
    <AnswersBody
      jobId={jobId}
      check={check.data}
      match={match.data}
      values={values.data}
    />
  )
}

function AnswersBody({
  jobId,
  check,
  match,
  values,
}: {
  jobId: string
  check: CheckStatusView
  match: AnswerMatchView | null
  values: QuestionAnswerList | null
}) {
  const detail = check.check ?? null
  if (check.status === "not_checked" || detail === null) {
    return (
      <EmptyBlock
        title="No check yet"
        description="Answering starts from a completed job check. Start a check first; its actual employer questions will appear here."
      />
    )
  }
  if (check.status === "checking") {
    return (
      <EmptyBlock
        title="Check in progress"
        description="The job check is still running. Its questions become answerable once the check completes."
      />
    )
  }
  if (check.status === "blocked") {
    return (
      <EmptyBlock
        title="Check blocked"
        description={
          detail.blockedReason !== undefined
            ? `The check could not complete (${detail.blockedReason.code}): ${detail.blockedReason.detail === "" ? "no detail recorded." : detail.blockedReason.detail} Resolve it with a recheck before answering.`
            : "The check could not complete. Resolve it with a recheck before answering."
        }
      />
    )
  }
  if (check.status === "outdated") {
    return (
      <EmptyBlock
        title="Check outdated"
        description="The role changed after this check completed. Start a recheck; answering reopens on the fresh questions."
      />
    )
  }
  if (detail.questions.length === 0) {
    return (
      <EmptyBlock
        title="No questions found"
        description="The completed check recorded no employer questions, so there is nothing to answer."
      />
    )
  }
  return (
    <AnswersList jobId={jobId} detail={detail} match={match} values={values} />
  )
}

// One fresh concrete suggestion per question, resolved from the match view.
// Outdated runs and other-check pins resolve to nothing: stale suggestions
// never prefill a box.
function matchEntryFor(
  match: AnswerMatchView | null,
  checkId: string,
  questionId: string
): MatchEntry | null {
  if (match === null || match.status === "outdated") return null
  if (match.checkId !== checkId) return null
  return match.matches.find((entry) => entry.questionId === questionId) ?? null
}

function suggestedAnswerRef(entry: MatchEntry | null): {
  answerId: string
  answerVersion: number
} | null {
  if (entry === null) return null
  const choice = entry.choice
  if (
    choice.noneFits === true ||
    choice.answerId === undefined ||
    choice.answerVersion === undefined
  ) {
    return null
  }
  return { answerId: choice.answerId, answerVersion: choice.answerVersion }
}

type SuggestionText =
  { status: "ready"; text: string } | { status: "unavailable" }

function AnswersList({
  jobId,
  detail,
  match,
  values,
}: {
  jobId: string
  detail: CheckDetail
  match: AnswerMatchView | null
  values: QuestionAnswerList | null
}) {
  const refs = new Map<string, number>()
  for (const question of detail.questions) {
    const ref = suggestedAnswerRef(matchEntryFor(match, detail.id, question.id))
    if (ref !== null && !refs.has(ref.answerId))
      refs.set(ref.answerId, ref.answerVersion)
  }
  const answerIds = [...refs.keys()].sort()
  // The read key binds the exact suggestion set, so a changed match refetches
  // and an empty set issues no request at all.
  const suggestions = useRead(
    `answers:${jobId}:suggestions:${answerIds.join(",")}`,
    async (signal) => {
      const loaded: Record<string, SuggestionText> = {}
      await Promise.all(
        answerIds.map(async (answerId) => {
          try {
            const answer = await getSavedAnswer(answerId, signal)
            const pinned = answer.versions.find(
              (version) => version.version === refs.get(answerId)
            )
            loaded[answerId] =
              pinned === undefined
                ? { status: "unavailable" }
                : { status: "ready", text: pinned.text }
          } catch {
            loaded[answerId] = { status: "unavailable" }
          }
        })
      )
      return loaded
    }
  )

  if (suggestions.status === "loading") {
    return <LoadingBlock label="Loading suggested answers…" />
  }
  if (suggestions.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the suggested answers"
        message={suggestions.error}
        onRetry={suggestions.retry}
      />
    )
  }
  const savedByQuestion = new Map<string, QuestionAnswerValue>()
  if (values !== null && values.checkId === detail.id) {
    for (const value of values.values)
      savedByQuestion.set(value.questionId, value)
  }
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <MatchBanner match={match} checkId={detail.id} />
      <AnswerActivityFeed
        questions={detail.questions}
        match={match}
        checkId={detail.id}
      />
      {detail.questions.map((question, index) => (
        <AnswerCard
          key={question.id}
          jobId={jobId}
          index={index}
          question={question}
          entry={matchEntryFor(match, detail.id, question.id)}
          suggestion={suggestionTextFor(
            matchEntryFor(match, detail.id, question.id),
            suggestions.data
          )}
          saved={savedByQuestion.get(question.id) ?? null}
        />
      ))}
      <PrepareContinuation jobId={jobId} />
    </div>
  )
}

// AnswerActivityFeed is the Jev match record for this check: which
// questions got a placed suggestion and which boxes start blank. It reads
// the saved match view only and selects nothing.
function AnswerActivityFeed({
  questions,
  match,
  checkId,
}: {
  questions: CheckDetail["questions"]
  match: AnswerMatchView | null
  checkId: string
}) {
  const current = match !== null && match.checkId === checkId
  const outdated = match !== null && match.status === "outdated"
  const entries: ActivityEntry[] = []
  if (current && !outdated) {
    questions.forEach((question, index) => {
      const entry = matchEntryFor(match, checkId, question.id)
      entries.push({
        id: question.id,
        kind: "result",
        text:
          entry === null
            ? `Question ${index + 1}: no match recorded; the box starts blank.`
            : entry.choice.noneFits === true
              ? `Question ${index + 1}: no saved answer fit, so the box starts blank.`
              : `Question ${index + 1}: a saved answer matched and was placed in the editable box.`,
      })
    })
  }
  const placed = entries.filter((entry) =>
    entry.text.includes("was placed")
  ).length
  const status =
    !current || match === null
      ? "No matches saved"
      : outdated
        ? "Matches outdated"
        : placed === 0
          ? "No saved answer fit"
          : `${placed} ${placed === 1 ? "suggestion" : "suggestions"} ready`
  return (
    <ActivityDisclosure
      actor="jev"
      phase="Answer questions"
      status={status}
      entries={entries}
      emptyText={
        current && !outdated
          ? "No questions recorded for this check."
          : "No usable matches for this check."
      }
    />
  )
}

// PrepareContinuation routes answered-or-blank boxes to preparation. It
// navigates only: preparation itself starts from its own explicit action,
// so this page stays GET-only apart from per-box saves.
function PrepareContinuation({ jobId }: { jobId: string }) {
  return (
    <section
      aria-label="Continue to preparation"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <p className="text-sm wrap-break-word">
        Leave a box blank if you want it drafted during Prepare. Personal
        facts are never guessed, and optional questions can stay blank.
      </p>
      <div>
        <Button
          render={
            <a href={`#/jobs/${encodeURIComponent(jobId)}/prepare`}>
              Prepare materials
            </a>
          }
        />
      </div>
      <p className="text-xs wrap-break-word text-muted-foreground">
        Nothing leaves this app. You keep the saved answers and apply
        manually from the Handoff page.
      </p>
    </section>
  )
}

function suggestionTextFor(
  entry: MatchEntry | null,
  loaded: Record<string, SuggestionText>
): string | null {
  const ref = suggestedAnswerRef(entry)
  if (ref === null) return null
  const found = loaded[ref.answerId]
  if (found === undefined || found.status !== "ready") return null
  return found.text
}

function MatchBanner({
  match,
  checkId,
}: {
  match: AnswerMatchView | null
  checkId: string
}) {
  if (match === null || match.checkId !== checkId) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        No suggested answers yet for this check. Anything typed below is saved
        as owner-written.
      </p>
    )
  }
  if (match.status === "outdated") {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        The saved matches are outdated, so no suggestion prefills a box. Saved
        values below stay exactly as stored.
      </p>
    )
  }
  return null
}

function requiredLabel(required: CheckQuestion["required"]): string {
  switch (required) {
    case "required":
      return "Required"
    case "optional":
      return "Optional"
    default:
      return "Requiredness unknown"
  }
}

function originLabel(
  origin: QuestionAnswerValue["provenance"]["origin"]
): string {
  switch (origin) {
    case "jev_suggestion":
      return "Suggested match, saved unchanged"
    case "owner_edited":
      return "Suggested match, edited by owner"
    case "owner_written":
      return "Written by owner"
    case "carried_blank":
      return "Explicitly left blank"
  }
}

function AnswerCard({
  jobId,
  index,
  question,
  entry,
  suggestion,
  saved,
}: {
  jobId: string
  index: number
  question: CheckQuestion
  entry: MatchEntry | null
  suggestion: string | null
  saved: QuestionAnswerValue | null
}) {
  const savedState: AnswerBoxState =
    saved === null ? "unset" : saved.state === "blank" ? "blank" : "answered"
  const initialText = saved !== null ? saved.text : (suggestion ?? "")
  const save = useAnswerSave({
    jobId,
    questionId: question.id,
    initialText,
    initial: {
      version: saved?.version ?? 0,
      state: savedState,
      provenance: saved?.provenance ?? null,
    },
  })
  const boxId = `answers-${question.id}`

  return (
    <section
      aria-label={`Question ${index + 1}`}
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <div className="flex min-w-0 flex-wrap items-start justify-between gap-2">
        <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
          {index + 1}. {question.text}
        </h2>
        <Badge
          variant={question.required === "required" ? "default" : "secondary"}
        >
          {requiredLabel(question.required)}
        </Badge>
      </div>
      <p className="text-xs wrap-break-word text-muted-foreground">
        Source:{" "}
        {question.sourceExcerpt === ""
          ? "excerpt not recorded"
          : question.sourceExcerpt}{" "}
        · {question.sourceSpan.captureId} · chars {question.sourceSpan.start}–
        {question.sourceSpan.end}
      </p>
      <SuggestionNote
        entry={entry}
        suggestion={suggestion}
        saved={saved !== null}
      />
      <div className="flex min-w-0 flex-col gap-2">
        <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
          Your answer
        </label>
        <Textarea
          id={boxId}
          value={save.text}
          onChange={(event) => save.setText(event.target.value)}
          rows={4}
          placeholder="Leave blank when there is nothing to say."
          aria-describedby={`${boxId}-status`}
        />
      </div>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={!save.canSave}
          onClick={save.save}
        >
          {save.saving ? "Saving…" : "Save answer"}
        </Button>
        <p
          id={`${boxId}-status`}
          className="text-sm wrap-break-word text-muted-foreground"
        >
          <AnswerStatus save={save} savedVersion={saved?.version ?? 0} />
        </p>
      </div>
      {save.error !== null ? (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {save.error}
        </p>
      ) : null}
      {save.saved.provenance !== null && save.saved.version > 0 ? (
        <p className="text-xs wrap-break-word text-muted-foreground">
          {originLabel(save.saved.provenance.origin)} · v{save.saved.version} ·
          edited {formatDate(save.saved.provenance.editedAt)} by{" "}
          {save.saved.provenance.editedBy.actorId}
          {save.saved.provenance.matchChoice?.answerId !== undefined
            ? " · based on a saved suggestion"
            : null}
        </p>
      ) : null}
    </section>
  )
}

function SuggestionNote({
  entry,
  suggestion,
  saved,
}: {
  entry: MatchEntry | null
  suggestion: string | null
  saved: boolean
}) {
  if (entry === null) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        No suggestion for this question; the box starts blank.
      </p>
    )
  }
  if (entry.choice.noneFits === true) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        No saved answer fit this question; the box starts blank.
      </p>
    )
  }
  if (suggestion === null) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        The saved suggestion could not be loaded; the box starts blank.
      </p>
    )
  }
  return (
    <p className="text-sm wrap-break-word text-muted-foreground">
      {saved
        ? "A saved suggestion exists for this question; the box shows the stored value."
        : "Prefilled from a saved suggestion; change it or clear the box before saving."}
    </p>
  )
}

function AnswerStatus({
  save,
  savedVersion,
}: {
  save: ReturnType<typeof useAnswerSave>
  savedVersion: number
}) {
  if (save.saving) return <>Saving…</>
  if (save.dirty) return <>Unsaved changes.</>
  if (save.saved.version > 0 || savedVersion > 0) {
    const version = save.saved.version > 0 ? save.saved.version : savedVersion
    return (
      <>
        {save.saved.state === "blank" ? "Blank saved" : "Saved"} · v{version}.
      </>
    )
  }
  return <>Not answered yet.</>
}
