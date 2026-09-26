import { useState } from "react"
import {
  answerClarification,
  isUnauthenticated,
  listClarifications,
  type Clarification,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  ErrorBlock,
  LoadingBlock,
  notifyAccepted,
} from "@/components/shared"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import {
  buildClarificationAnswerRequest,
  countRunes,
  newPrepareRequestKey,
} from "@/features/prepare/artifactsApi"
import { mutationMessage, useDraftArtifacts } from "@/features/prepare/useDraftArtifacts"
import { formatDate } from "@/pages/format"
import { useRead } from "@/pages/useRead"

export const CLARIFICATION_ANSWER_RUNE_LIMIT = 2000

function affectedWorkLabel(kind: string, id: string): string {
  return `${kind}: ${id}`
}

// ClarificationsSection is the E1 owner-question surface for one role: the
// focused K4 questions preparation asked when a necessary personal fact was
// unknown, distinct from employer questions. Mount and reads are GET-only.
// Answering saves the owner's exact text verbatim (no model call, never
// silently library-approved); Resume preparation then re-runs the single
// draft so the server resumes only the dependent items. Everything is
// job-scoped, so another ready job stays usable while a fact is held here.
export function ClarificationsSection({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  stale,
  staleReason,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  stale: boolean
  staleReason: string | null
}) {
  const clarifications = useRead(
    `clarifications:${jobId}`,
    (signal) => listClarifications(jobId, signal),
    { scopes: ["materials"] }
  )

  if (clarifications.status === "loading") {
    return <LoadingBlock label="Loading owner questions…" />
  }
  if (clarifications.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the owner questions"
        message={clarifications.error}
        onRetry={clarifications.retry}
      />
    )
  }

  const items = clarifications.data.items
  const openCount = items.filter((item) => item.status === "open").length
  return (
    <section
      aria-label="Owner questions"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
          Owner questions
        </h2>
        <Badge variant={openCount === 0 ? "secondary" : "default"}>
          {openCount === 0 ? "None open" : `${openCount} open`}
        </Badge>
      </div>
      <p className="text-sm wrap-break-word text-muted-foreground">
        Preparation asks here when a vacancy requirement needs a personal
        fact only you know. These are not employer questions; your answer is
        saved exactly and stays job-scoped.
      </p>
      {items.length === 0 ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          No owner questions. Items below are held only on their route
          reasons.
        </p>
      ) : (
        <ul className="flex min-w-0 flex-col gap-3">
          {items.map((item) => (
            <li
              key={item.id}
              className="flex min-w-0 flex-col gap-2 rounded-md border border-border p-3"
            >
              <ClarificationCard
                jobId={jobId}
                item={item}
                checkId={checkId}
                questionSetSha256={questionSetSha256}
                workflowRevision={workflowRevision}
                stale={stale}
                staleReason={staleReason}
              />
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function ClarificationCard({
  jobId,
  item,
  checkId,
  questionSetSha256,
  workflowRevision,
  stale,
  staleReason,
}: {
  jobId: string
  item: Clarification
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  stale: boolean
  staleReason: string | null
}) {
  const currentCheck = item.checkId === checkId
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-sm font-medium wrap-break-word">
          {item.prompt}
        </p>
        <Badge variant={item.status === "open" ? "default" : "secondary"}>
          {item.status === "open" ? "Open" : "Answered"}
        </Badge>
      </div>
      <p className="text-sm wrap-break-word text-muted-foreground">
        {`Needed for: ${item.requirement.statement}`}
      </p>
      <p className="text-xs wrap-break-word text-muted-foreground">
        {`Vacancy evidence ${item.requirement.captureId} · characters ${item.requirement.spanStart}–${item.requirement.spanEnd} · asked ${formatDate(item.createdAt)}`}
      </p>
      {item.affectedWork.length === 0 ? null : (
        <p className="text-xs wrap-break-word text-muted-foreground">
          {`Holds: ${item.affectedWork.map((ref) => affectedWorkLabel(ref.kind, ref.id)).join(", ")}`}
        </p>
      )}
      {!currentCheck ? (
        <p className="text-xs wrap-break-word text-muted-foreground">
          Asked under an older check; answering still saves your words, but
          preparation runs against the current check.
        </p>
      ) : null}
      {item.status === "answered" ? (
        <AnsweredBlock item={item} />
      ) : (
        <AnswerBox jobId={jobId} item={item} />
      )}
      {item.status === "answered" && currentCheck ? (
        <ResumeRow
          jobId={jobId}
          checkId={checkId}
          questionSetSha256={questionSetSha256}
          workflowRevision={workflowRevision}
          stale={stale}
          staleReason={staleReason}
        />
      ) : null}
    </div>
  )
}

function AnsweredBlock({ item }: { item: Clarification }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <p className="text-sm font-medium wrap-break-word">Your saved answer</p>
      <pre className="min-w-0 overflow-x-auto rounded-md border border-border bg-muted/40 p-2 text-sm wrap-break-word whitespace-pre-wrap">
        {item.answer ?? ""}
      </pre>
      <p className="text-xs wrap-break-word text-muted-foreground">
        {item.answeredAt !== undefined
          ? `Saved ${formatDate(item.answeredAt)}${item.answeredBy !== undefined ? ` by ${item.answeredBy.actorId} (${item.answeredBy.actorKind})` : ""}. Saved exactly as typed; not added to the reusable library.`
          : "Saved exactly as typed; not added to the reusable library."}
      </p>
    </div>
  )
}

function AnswerBox({ jobId, item }: { jobId: string; item: Clarification }) {
  const { session, loseSession } = useSession()
  const [text, setText] = useState("")
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pendingKey, setPendingKey] = useState<string | null>(null)
  const runes = countRunes(text)
  const tooLong = runes > CLARIFICATION_ANSWER_RUNE_LIMIT
  const empty = text === ""
  const boxId = `clarification-answer-${item.id}`

  async function save() {
    if (
      session === undefined ||
      session === null ||
      saving ||
      empty ||
      tooLong
    )
      return
    // Stable key per answer attempt: an uncertain acknowledgment retries as
    // an exact idempotent replay instead of a second answer.
    const requestKey = pendingKey ?? newPrepareRequestKey()
    setPendingKey(requestKey)
    setSaving(true)
    setError(null)
    try {
      await answerClarification(
        jobId,
        item.id,
        buildClarificationAnswerRequest(requestKey, text),
        session.csrfToken
      )
      setSaving(false)
      setPendingKey(null)
      notifyAccepted("materials", "workflows")
    } catch (cause: unknown) {
      setSaving(false)
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(mutationMessage(cause))
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
        Your answer (saved exactly, no model call)
      </label>
      <Textarea
        id={boxId}
        value={text}
        onChange={(event) => setText(event.target.value)}
        rows={3}
      />
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={
            saving ||
            empty ||
            tooLong ||
            session === undefined ||
            session === null
          }
          onClick={() => void save()}
        >
          {saving ? "Saving…" : "Save answer"}
        </Button>
        <p className="text-sm wrap-break-word text-muted-foreground">
          {tooLong
            ? `Over the ${CLARIFICATION_ANSWER_RUNE_LIMIT.toLocaleString()} character limit.`
            : empty
              ? "Type your answer, then save."
              : "Unsaved answer."}
        </p>
      </div>
      {error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}

function ResumeRow({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  stale,
  staleReason,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  stale: boolean
  staleReason: string | null
}) {
  const resume = useDraftArtifacts({
    jobId,
    checkId,
    questionSetSha256,
    workflowRevision,
    disabledReason: stale
      ? (staleReason ?? "Preparation is paused until a fresh check completes.")
      : null,
  })
  return (
    <div className="flex min-w-0 flex-wrap items-center gap-3">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={!resume.canDraft}
        onClick={resume.draft}
      >
        {resume.drafting ? "Resuming…" : "Resume preparation"}
      </Button>
      <p className="text-sm wrap-break-word text-muted-foreground">
        {resume.unavailableReason ??
          "Drafts only the items this answer depended on."}
      </p>
      {resume.error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {resume.error}
        </p>
      )}
    </div>
  )
}
