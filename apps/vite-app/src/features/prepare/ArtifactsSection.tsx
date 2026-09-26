import { useState } from "react"
import {
  RequestError,
  draftOpportunityArtifacts,
  isUnauthenticated,
  listArtifactReadiness,
  listPrepareActivity,
  saveOpportunityArtifact,
  type ArtifactFormValue,
  type ArtifactReadinessEntry,
  type ArtifactReadinessSet,
  type ArtifactView,
  type CheckActivityPage,
} from "@/api/client"
import { useSession } from "@/api/session"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import {
  ActivityDisclosure,
  type ActivityEntry,
} from "@/components/shared/activity-disclosure"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import {
  isCheckBlockerEvent,
  toCheckActivityEntry,
} from "@/features/check/check-activity-feed"
import {
  buildMaterialPrepareRequest,
  newPrepareRequestKey,
} from "@/features/prepare/usePrepareActions"
import { formatDate } from "@/pages/format"
import { useRead } from "@/pages/useRead"

export type ArtifactType = ArtifactReadinessEntry["type"]

const artifactOrder: ReadonlyArray<ArtifactType> = [
  "cv",
  "cover_letter",
  "email_subject",
  "email_body",
  "form_values",
]

export function artifactTypeLabel(type: ArtifactType): string {
  switch (type) {
    case "cv":
      return "Tailored CV"
    case "cover_letter":
      return "Motivation letter"
    case "email_subject":
      return "Email subject"
    case "email_body":
      return "Motivation email"
    case "form_values":
      return "Form values"
  }
}

function artifactStateLabel(
  state: ArtifactReadinessEntry["state"]
): string {
  switch (state) {
    case "ready":
      return "Ready"
    case "held":
      return "Held"
    case "not_required":
      return "Not required"
    case "unresolved":
      return "Unresolved"
  }
}

async function copyText(text: string): Promise<boolean> {
  try {
    if (
      typeof navigator !== "undefined" &&
      navigator.clipboard?.writeText !== undefined
    ) {
      await navigator.clipboard.writeText(text)
      return true
    }
  } catch {
    // Fall through to the manual-copy message.
  }
  return false
}

function downloadText(filename: string, text: string): boolean {
  try {
    if (typeof URL.createObjectURL !== "function") return false
    const blob = new Blob([text], { type: "text/plain;charset=utf-8" })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = filename
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    URL.revokeObjectURL(url)
    return true
  } catch {
    return false
  }
}

function requestMessage(cause: unknown): string {
  if (cause instanceof RequestError && cause.status === 409)
    return "These artifacts changed elsewhere. Reload this section, then try again."
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

// ArtifactsSection is the E1–E4 route-mapped artifact surface for one
// checked role: Standard drafting of held types, per-type readable content,
// exact per-artifact editors, copy/download of the displayed version, and
// the prepare activity journal. Mount and reads are GET-only; drafting,
// edits, copies and downloads each need an explicit click.
export function ArtifactsSection({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
}) {
  const readiness = useRead(`artifacts:${jobId}:readiness`, (signal) =>
    listArtifactReadiness(jobId, signal)
  )
  const activity = useRead(`artifacts:${jobId}:activity`, (signal) =>
    listPrepareActivity(jobId, signal)
  )

  if (readiness.status === "loading" || activity.status === "loading") {
    return <LoadingBlock label="Loading route artifacts…" />
  }
  if (readiness.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the route artifacts"
        message={readiness.error}
        onRetry={readiness.retry}
      />
    )
  }
  if (activity.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the prepare activity"
        message={activity.error}
        onRetry={activity.retry}
      />
    )
  }
  return (
    <ArtifactsBody
      jobId={jobId}
      checkId={checkId}
      questionSetSha256={questionSetSha256}
      workflowRevision={workflowRevision}
      set={readiness.data}
      activity={activity.data}
      onChanged={() => {
        readiness.retry()
        activity.retry()
      }}
    />
  )
}

function ArtifactsBody({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  set,
  activity,
  onChanged,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  set: ArtifactReadinessSet
  activity: CheckActivityPage
  onChanged: () => void
}) {
  const entries = [...set.entries].sort(
    (left, right) =>
      artifactOrder.indexOf(left.type) - artifactOrder.indexOf(right.type)
  )
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <DraftPrompt
        jobId={jobId}
        checkId={checkId}
        questionSetSha256={questionSetSha256}
        workflowRevision={workflowRevision}
        onDone={onChanged}
      />
      {entries.length === 0 ? (
        <EmptyBlock
          title="No route items"
          description="The verified route requires no artifacts for this role."
        />
      ) : (
        entries.map((entry) => (
          <ArtifactEntrySection
            key={entry.type}
            jobId={jobId}
            entry={entry}
            onChanged={onChanged}
          />
        ))
      )}
      <PrepareActivityFeed activity={activity} />
    </div>
  )
}

function DraftPrompt({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  onDone,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  onDone: () => void
}) {
  const { session, loseSession } = useSession()
  const [drafting, setDrafting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function draft() {
    if (session === undefined || session === null || drafting) return
    setDrafting(true)
    setError(null)
    try {
      await draftOpportunityArtifacts(
        jobId,
        buildMaterialPrepareRequest(
          newPrepareRequestKey(),
          checkId,
          questionSetSha256,
          workflowRevision
        ),
        session.csrfToken
      )
      setDrafting(false)
      onDone()
    } catch (cause: unknown) {
      setDrafting(false)
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(requestMessage(cause))
    }
  }

  return (
    <section
      aria-label="Draft held artifacts"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Route-mapped artifacts
      </h2>
      <p className="text-sm wrap-break-word text-muted-foreground">
        One bounded Standard turn drafts the held types the verified route
        requires, grounded in verified facts and saved answers. Types with no
        known fact stay held; nothing is sent.
      </p>
      <div>
        <Button
          type="button"
          size="sm"
          disabled={drafting || session === undefined || session === null}
          onClick={() => void draft()}
        >
          {drafting ? "Drafting…" : "Draft held artifacts"}
        </Button>
      </div>
      {error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {error}
        </p>
      )}
    </section>
  )
}

function ArtifactEntrySection({
  jobId,
  entry,
  onChanged,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  onChanged: () => void
}) {
  const current = entry.current ?? null
  return (
    <section
      aria-label={artifactTypeLabel(entry.type)}
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
          {artifactTypeLabel(entry.type)}
        </h2>
        <Badge variant={entry.state === "ready" ? "default" : "secondary"}>
          {artifactStateLabel(entry.state)}
        </Badge>
        <Badge variant="secondary">
          {entry.required ? "Required" : "Optional"}
        </Badge>
      </div>
      <p className="text-sm wrap-break-word text-muted-foreground">
        {entry.reason}
      </p>
      {entry.basis === undefined || entry.basis === "" ? null : (
        <p className="text-xs wrap-break-word text-muted-foreground">
          {entry.basis}
        </p>
      )}
      {entry.state === "ready" && entry.type === "form_values" ? (
        <FormValuesPanel jobId={jobId} values={entry.formValues ?? []} />
      ) : entry.state === "ready" && current !== null ? (
        <ArtifactContentPanel
          jobId={jobId}
          entry={entry}
          current={current}
          onChanged={onChanged}
        />
      ) : entry.state === "ready" ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          Marked ready but no current version was returned. Reload this
          section; if this persists, the saved state needs attention.
        </p>
      ) : null}
    </section>
  )
}

function ArtifactBasisLine({ current }: { current: ArtifactView }) {
  const facts = current.basis.factIds.length
  const answers = current.basis.answerRefs.length
  const spans = current.basis.checkSpans.length
  return (
    <p className="text-xs wrap-break-word text-muted-foreground">
      {`v${current.version} · ${formatDate(current.createdAt)} by ${current.createdBy.actorId} (${current.createdBy.actorKind}) · ${facts} ${facts === 1 ? "fact" : "facts"} · ${answers} ${answers === 1 ? "answer" : "answers"} · ${spans} ${spans === 1 ? "check span" : "check spans"}`}
    </p>
  )
}

function ArtifactContentPanel({
  jobId,
  entry,
  current,
  onChanged,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  current: ArtifactView
  onChanged: () => void
}) {
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">(
    "idle"
  )
  const [downloadFailed, setDownloadFailed] = useState(false)
  const filename = `${jobId}-${entry.type}-v${current.version}.txt`

  async function copy() {
    setDownloadFailed(false)
    setCopyState((await copyText(current.content)) ? "copied" : "failed")
  }

  function download() {
    setCopyState("idle")
    setDownloadFailed(!downloadText(filename, current.content))
  }

  return (
    <div className="flex min-w-0 flex-col gap-3">
      <ArtifactBasisLine current={current} />
      <pre className="min-w-0 overflow-x-auto rounded-md border border-border bg-muted/40 p-3 text-sm wrap-break-word whitespace-pre-wrap">
        {current.content}
      </pre>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`Copy ${artifactTypeLabel(entry.type)} v${current.version}`}
          onClick={() => void copy()}
        >
          Copy
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`Download ${artifactTypeLabel(entry.type)} v${current.version}`}
          onClick={download}
        >
          Download
        </Button>
        {copyState === "copied" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copied.
          </p>
        ) : copyState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copy failed — select the text manually.
          </p>
        ) : null}
        {downloadFailed ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Download is unavailable in this browser.
          </p>
        ) : null}
      </div>
      <ArtifactEditor
        key={`${entry.type}:${current.version}`}
        jobId={jobId}
        entry={entry}
        current={current}
        onSaved={onChanged}
      />
    </div>
  )
}

const ARTIFACT_CONTENT_RUNE_LIMIT = 65536

function ArtifactEditor({
  jobId,
  entry,
  current,
  onSaved,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  current: ArtifactView
  onSaved: () => void
}) {
  const { session, loseSession } = useSession()
  const [text, setText] = useState(current.content)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const dirty = text !== current.content
  const tooLong = Array.from(text).length > ARTIFACT_CONTENT_RUNE_LIMIT
  const empty = text === ""
  const boxId = `artifact-edit-${entry.type}`

  async function save() {
    if (
      session === undefined ||
      session === null ||
      saving ||
      !dirty ||
      tooLong ||
      empty
    )
      return
    setSaving(true)
    setError(null)
    try {
      await saveOpportunityArtifact(
        jobId,
        entry.type,
        {
          requestKey: newPrepareRequestKey(),
          expectedVersion: current.version,
          content: text,
        },
        session.csrfToken
      )
      setSaving(false)
      onSaved()
    } catch (cause: unknown) {
      setSaving(false)
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(requestMessage(cause))
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
        {`Exact edit (replaces v${current.version} byte-exact, no model call)`}
      </label>
      <Textarea
        id={boxId}
        value={text}
        onChange={(event) => setText(event.target.value)}
        rows={6}
      />
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={
            saving ||
            !dirty ||
            tooLong ||
            empty ||
            session === undefined ||
            session === null
          }
          onClick={() => void save()}
        >
          {saving ? "Saving…" : "Save exact edit"}
        </Button>
        <p className="text-sm wrap-break-word text-muted-foreground">
          {tooLong
            ? `Over the ${ARTIFACT_CONTENT_RUNE_LIMIT.toLocaleString()} character limit.`
            : empty
              ? "Content cannot be empty."
              : !dirty
                ? "No changes to save."
                : "Unsaved changes."}
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

function FormValuesPanel({
  jobId,
  values,
}: {
  jobId: string
  values: ArtifactFormValue[]
}) {
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [copyFailed, setCopyFailed] = useState(false)

  async function copy(value: ArtifactFormValue) {
    if (value.text === "") return
    if (await copyText(value.text)) {
      setCopiedId(value.questionId)
      setCopyFailed(false)
    } else {
      setCopiedId(null)
      setCopyFailed(true)
    }
  }

  if (values.length === 0) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        No form values derived for this route.
      </p>
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <p className="text-xs wrap-break-word text-muted-foreground">
        Derived from saved answers at read time — edit the answers, not this
        list.{" "}
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
          className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Open answers
        </a>
      </p>
      <ul className="flex min-w-0 flex-col gap-3">
        {values.map((value) => (
          <li
            key={value.questionId}
            className="flex min-w-0 flex-col gap-1 rounded-md border border-border p-3"
          >
            <p className="text-sm font-medium wrap-break-word">
              {value.questionText}
            </p>
            <p className="text-xs wrap-break-word text-muted-foreground">
              {`${value.required} · ${value.kind} · ${value.state}`}
            </p>
            {value.text === "" ? (
              <p className="text-sm wrap-break-word text-muted-foreground">
                Blank — nothing copyable.
              </p>
            ) : (
              <>
                <pre className="min-w-0 overflow-x-auto rounded-md border border-border bg-muted/40 p-2 text-sm wrap-break-word whitespace-pre-wrap">
                  {value.text}
                </pre>
                <div className="flex min-w-0 flex-wrap items-center gap-3">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    aria-label={`Copy value for ${value.questionText}`}
                    onClick={() => void copy(value)}
                  >
                    Copy value
                  </Button>
                  {copiedId === value.questionId ? (
                    <p className="text-sm wrap-break-word text-muted-foreground">
                      Copied.
                    </p>
                  ) : null}
                </div>
              </>
            )}
          </li>
        ))}
      </ul>
      {copyFailed ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          Copy failed — select the text manually.
        </p>
      ) : null}
    </div>
  )
}

function PrepareActivityFeed({ activity }: { activity: CheckActivityPage }) {
  const entries: ActivityEntry[] = activity.events.map(toCheckActivityEntry)
  const blockers = activity.events.filter(isCheckBlockerEvent).length
  return (
    <ActivityDisclosure
      actor="codex"
      phase="Prepare materials · activity"
      status={
        entries.length === 0
          ? "No prepare activity recorded"
          : blockers === 0
            ? `${entries.length} recorded`
            : `${entries.length} recorded · ${blockers} blockers`
      }
      entries={entries}
      emptyText="No prepare activity recorded yet."
    />
  )
}
