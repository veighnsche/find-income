import { useState } from "react"
import {
  exportOpportunityArtifact,
  isUnauthenticated,
  listArtifactReadiness,
  listPrepareActivity,
  saveOpportunityArtifact,
  type ArtifactFormValue,
  type ArtifactReadinessEntry,
  type ArtifactReadinessSet,
  type ArtifactView,
  type CheckActivityPage,
  type CheckStatusView,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  notifyAccepted,
} from "@/components/shared"
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
import { RewritePanel } from "@/features/prepare/ArtifactRewrite"
import { VersionsPanel } from "@/features/prepare/ArtifactVersions"
import {
  newPrepareRequestKey,
  type StoredArtifactType,
} from "@/features/prepare/artifactsApi"
import { ClarificationsSection } from "@/features/prepare/ClarificationsSection"
import {
  mutationMessage,
  useDraftArtifacts,
} from "@/features/prepare/useDraftArtifacts"
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

export function artifactTypeLabel(type: ArtifactType | string): string {
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
    default:
      return type
  }
}

export type EffectiveArtifactState =
  | ArtifactReadinessEntry["state"]
  | "outdated"

export function artifactStateLabel(state: EffectiveArtifactState): string {
  switch (state) {
    case "ready":
      return "Ready"
    case "held":
      return "Held"
    case "not_required":
      return "Not required"
    case "unresolved":
      return "Unresolved"
    case "outdated":
      return "Outdated"
  }
}

function isStoredType(type: ArtifactType): type is StoredArtifactType {
  return type !== "form_values"
}

export async function copyText(text: string): Promise<boolean> {
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

export function downloadBlob(
  filename: string,
  text: string,
  mediaType: string
): boolean {
  try {
    if (typeof URL.createObjectURL !== "function") return false
    const blob = new Blob([text], { type: `${mediaType};charset=utf-8` })
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

export function downloadText(filename: string, text: string): boolean {
  return downloadBlob(filename, text, "text/plain")
}

// ArtifactsSection is the E1–E3 route-mapped artifact surface for one
// checked role: the one explicit Prepare action, owner-question holds with
// dependent resume, per-type readable content with honest ready/held/outdated
// reasons, canonical prefilled exact editors, separate explicit rewrites,
// inspectable version history, canonical export downloads of the shown
// version, and the prepare activity journal. Mount and reads are GET-only;
// drafting, answers, edits, rewrites, copies and downloads each need an
// explicit click. Changed inputs never display stale content as ready: a
// blocked/outdated check, or materials pinned to an older check, renders
// prior bytes read-only under an Outdated badge with mutations disabled.
export function ArtifactsSection({
  jobId,
  checkId,
  checkStatus,
  questionSetSha256,
  workflowRevision,
}: {
  jobId: string
  checkId: string
  checkStatus: CheckStatusView["status"]
  questionSetSha256: string
  workflowRevision: number
}) {
  const readiness = useRead(
    `artifacts:${jobId}:readiness`,
    (signal) => listArtifactReadiness(jobId, signal),
    { scopes: ["materials"] }
  )
  const activity = useRead(
    `artifacts:${jobId}:activity`,
    (signal) => listPrepareActivity(jobId, signal),
    { scopes: ["materials"] }
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
      checkStatus={checkStatus}
      questionSetSha256={questionSetSha256}
      workflowRevision={workflowRevision}
      set={readiness.data}
      activity={activity.data}
    />
  )
}

function staleReasonText(
  checkStatus: CheckStatusView["status"],
  setCheckId: string | undefined,
  checkId: string
): string | null {
  if (checkStatus === "outdated")
    return "The role changed after these materials were saved. They stay readable below but are outdated — nothing here counts as ready. Start a recheck; preparation reopens on the fresh check."
  if (checkStatus === "blocked")
    return "The latest check is blocked, so these last saved materials stay readable but are not current. Resolve the block with a recheck before preparing again."
  if (setCheckId !== undefined && setCheckId !== "" && setCheckId !== checkId)
    return `These materials belong to an older check (${setCheckId}); the current check is ${checkId}. They stay readable but are outdated — nothing here counts as ready.`
  return null
}

function ArtifactsBody({
  jobId,
  checkId,
  checkStatus,
  questionSetSha256,
  workflowRevision,
  set,
  activity,
}: {
  jobId: string
  checkId: string
  checkStatus: CheckStatusView["status"]
  questionSetSha256: string
  workflowRevision: number
  set: ArtifactReadinessSet
  activity: CheckActivityPage
}) {
  const entries = [...set.entries].sort(
    (left, right) =>
      artifactOrder.indexOf(left.type) - artifactOrder.indexOf(right.type)
  )
  const staleReason = staleReasonText(checkStatus, set.checkId, checkId)
  const stale = staleReason !== null
  const mutationBlock = stale
    ? "Resolve the check first — these materials are outdated."
    : null
  return (
    <div className="flex min-w-0 flex-col gap-6">
      {staleReason === null ? null : (
        <p
          role="status"
          className="rounded-xl border border-border p-4 text-sm wrap-break-word"
        >
          {staleReason}
        </p>
      )}
      <DraftPrompt
        jobId={jobId}
        checkId={checkId}
        questionSetSha256={questionSetSha256}
        workflowRevision={workflowRevision}
        disabledReason={mutationBlock}
      />
      <ClarificationsSection
        jobId={jobId}
        checkId={checkId}
        questionSetSha256={questionSetSha256}
        workflowRevision={workflowRevision}
        stale={stale}
        staleReason={mutationBlock}
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
            stale={stale}
            mutationBlock={mutationBlock}
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
  disabledReason,
}: {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  disabledReason: string | null
}) {
  const draft = useDraftArtifacts({
    jobId,
    checkId,
    questionSetSha256,
    workflowRevision,
    disabledReason,
  })

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
        known fact stay held and ask an owner question below; nothing is
        sent.
      </p>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          size="sm"
          disabled={!draft.canDraft}
          onClick={draft.draft}
        >
          {draft.drafting ? "Drafting…" : "Draft held artifacts"}
        </Button>
        {draft.unavailableReason === null ? null : (
          <p className="text-sm wrap-break-word text-muted-foreground">
            {draft.unavailableReason}
          </p>
        )}
      </div>
      {draft.error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {draft.error}
        </p>
      )}
    </section>
  )
}

function ArtifactEntrySection({
  jobId,
  entry,
  stale,
  mutationBlock,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  stale: boolean
  mutationBlock: string | null
}) {
  const current = entry.current ?? null
  const effective: EffectiveArtifactState = stale ? "outdated" : entry.state
  return (
    <section
      aria-label={artifactTypeLabel(entry.type)}
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
          {artifactTypeLabel(entry.type)}
        </h2>
        <Badge variant={effective === "ready" ? "default" : "secondary"}>
          {artifactStateLabel(effective)}
        </Badge>
        <Badge variant="secondary">
          {entry.required ? "Required" : "Optional"}
        </Badge>
      </div>
      <p className="text-sm wrap-break-word text-muted-foreground">
        {stale
          ? `Outdated — last saved state was ${artifactStateLabel(entry.state).toLowerCase()}: ${entry.reason}`
          : entry.reason}
      </p>
      {entry.basis === undefined || entry.basis === "" ? null : (
        <p className="text-xs wrap-break-word text-muted-foreground">
          {entry.basis}
        </p>
      )}
      {entry.type === "form_values" ? (
        <FormValuesPanel
          jobId={jobId}
          values={entry.formValues ?? []}
          showValues={entry.state === "ready"}
        />
      ) : current !== null ? (
        <ArtifactContentPanel
          jobId={jobId}
          entry={entry}
          current={current}
          mutationBlock={mutationBlock}
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
  mutationBlock,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  current: ArtifactView
  mutationBlock: string | null
}) {
  const { loseSession } = useSession()
  const [copyState, setCopyState] = useState<"idle" | "copied" | "failed">(
    "idle"
  )
  const [exportState, setExportState] = useState<
    "idle" | "working" | "failed"
  >("idle")

  async function copy() {
    setExportState("idle")
    setCopyState((await copyText(current.content)) ? "copied" : "failed")
  }

  // Canonical download: the server renders the pinned current version from
  // stored content with identity/version/checksum headers. Downloading
  // never means applied.
  async function download() {
    if (exportState === "working") return
    setCopyState("idle")
    setExportState("working")
    try {
      const exported = await exportOpportunityArtifact(
        jobId,
        entry.type,
        current.version
      )
      const ok = downloadBlob(
        exported.filename,
        exported.text,
        exported.mediaType === "" ? "text/plain" : exported.mediaType
      )
      setExportState(ok ? "idle" : "failed")
    } catch (cause: unknown) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setExportState("failed")
    }
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
          disabled={exportState === "working"}
          onClick={() => void download()}
        >
          {exportState === "working" ? "Exporting…" : "Download"}
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
        {exportState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Export failed — try again or copy the text instead.
          </p>
        ) : null}
      </div>
      {isStoredType(entry.type) ? (
        <>
          <ArtifactEditor
            key={`${entry.type}:${current.version}`}
            jobId={jobId}
            entry={entry}
            current={current}
            disabledReason={mutationBlock}
          />
          <RewritePanel
            jobId={jobId}
            artifactType={entry.type}
            expectedVersion={current.version}
            disabledReason={mutationBlock}
          />
          <VersionsPanel
            jobId={jobId}
            artifactType={entry.type}
            current={current}
          />
        </>
      ) : null}
    </div>
  )
}

const ARTIFACT_CONTENT_RUNE_LIMIT = 65536

function ArtifactEditor({
  jobId,
  entry,
  current,
  disabledReason,
}: {
  jobId: string
  entry: ArtifactReadinessEntry
  current: ArtifactView
  disabledReason: string | null
}) {
  const { session, loseSession } = useSession()
  const [text, setText] = useState(current.content)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pendingKey, setPendingKey] = useState<string | null>(null)
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
      empty ||
      disabledReason !== null
    )
      return
    // Stable key per save attempt: an uncertain acknowledgment retries as
    // an exact idempotent replay instead of a second version.
    const requestKey = pendingKey ?? newPrepareRequestKey()
    setPendingKey(requestKey)
    setSaving(true)
    setError(null)
    try {
      await saveOpportunityArtifact(
        jobId,
        entry.type,
        {
          requestKey,
          expectedVersion: current.version,
          content: text,
        },
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
            disabledReason !== null ||
            session === undefined ||
            session === null
          }
          onClick={() => void save()}
        >
          {saving ? "Saving…" : "Save exact edit"}
        </Button>
        <p className="text-sm wrap-break-word text-muted-foreground">
          {disabledReason ??
            (tooLong
              ? `Over the ${ARTIFACT_CONTENT_RUNE_LIMIT.toLocaleString()} character limit.`
              : empty
                ? "Content cannot be empty."
                : !dirty
                  ? "No changes to save."
                  : "Unsaved changes.")}
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
  showValues,
}: {
  jobId: string
  values: ArtifactFormValue[]
  showValues: boolean
}) {
  const { loseSession } = useSession()
  const [copiedId, setCopiedId] = useState<string | null>(null)
  const [copyFailed, setCopyFailed] = useState(false)
  const [exportState, setExportState] = useState<
    "idle" | "working" | "failed"
  >("idle")

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

  // Whole-set download, derived live by the server from saved answers.
  async function downloadAll() {
    if (exportState === "working") return
    setExportState("working")
    try {
      const exported = await exportOpportunityArtifact(
        jobId,
        "form_values",
        null
      )
      const ok = downloadBlob(
        exported.filename,
        exported.text,
        exported.mediaType === "" ? "text/plain" : exported.mediaType
      )
      setExportState(ok ? "idle" : "failed")
    } catch (cause: unknown) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setExportState("failed")
    }
  }

  if (!showValues || values.length === 0) {
    return (
      <p className="text-sm wrap-break-word text-muted-foreground">
        {values.length === 0
          ? "No form values derived for this route."
          : "Form values are not ready; the reason above says why."}{" "}
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
          className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Open answers
        </a>
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
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={exportState === "working"}
          onClick={() => void downloadAll()}
        >
          {exportState === "working"
            ? "Exporting…"
            : "Download all values"}
        </Button>
        {exportState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Export failed — try again or copy the values above.
          </p>
        ) : null}
      </div>
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
