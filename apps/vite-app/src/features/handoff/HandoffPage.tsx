import { useState } from "react"
import {
  exportOpportunityArtifact,
  getCurrentOpportunityCheck,
  getOpportunity,
  getOpportunityHandoff,
  getRoleWorkflowOrNull,
  isUnauthenticated,
  saveOpportunityHandoff,
  type ArtifactFormValue,
  type CheckStatusView,
  type HandoffItem,
  type HandoffView,
  type OpportunityView,
  type RoleWorkflowState,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  notifyAccepted,
} from "@/components/shared"
import { StageExplainer } from "@/components/shared/stage-explainer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  artifactStateLabel,
  artifactTypeLabel,
  copyText,
  downloadBlob,
} from "@/features/prepare/ArtifactsSection"
import { buildHandoffSaveRequest } from "@/features/prepare/artifactsApi"
import { mutationMessage } from "@/features/prepare/useDraftArtifacts"
import { RoleStageIndicator } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

type CheckDetail = NonNullable<CheckStatusView["check"]>
type RouteKind = CheckDetail["route"]["kind"]

function routeKindLabel(kind: RouteKind): string {
  switch (kind) {
    case "direct":
      return "Direct application"
    case "referral":
      return "Referral"
    case "recruiter":
      return "Recruiter"
    case "unsupported":
      return "Unsupported route"
    default:
      return "Route kind unknown"
  }
}

function handoffStateLabel(state: string): string {
  switch (state) {
    case "ready":
    case "held":
    case "not_required":
    case "unresolved":
      return artifactStateLabel(state)
    case "outdated":
      return artifactStateLabel("outdated")
    default:
      return state === "" ? "Unknown" : state
  }
}

function destinationHref(destination: string): string | null {
  const trimmed = destination.trim()
  if (/^https?:\/\//i.test(trimmed)) return trimmed
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed))
    return `mailto:${trimmed}`
  return null
}

// HandoffPage is the E4 seventh-stage surface for one role: the verified
// destination, the canonical Handoff projection (which real values to paste,
// which files to download and attach yourself, upload mapping, unknowns
// explicit), and the explicit manual-handoff save. Mount and reads are
// GET-only; copy/download buttons only move saved bytes to the owner's
// clipboard or disk, and opening a link never claims submission. The app
// never fills an employer form, attaches, sends, or submits anything —
// there is no such control, API call, or automation here. handoff_saved is
// terminal: it only records the owner's manual list for revisits.
export function HandoffPage({ jobId }: { jobId: string }) {
  const opportunity = useRead(`handoff:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(
    `handoff:${jobId}:workflow`,
    (signal) => getRoleWorkflowOrNull(jobId, signal),
    { scopes: ["workflows"] }
  )
  const title =
    opportunity.status === "ready" && opportunity.data.opportunity.title !== ""
      ? opportunity.data.opportunity.title
      : jobId

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex min-w-0 flex-wrap gap-x-4 gap-y-1">
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to job details
        </a>
        <a
          href="#/applications"
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to Applications
        </a>
      </p>

      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          Handoff
        </h1>
        <p className="mt-1 text-sm wrap-break-word text-muted-foreground">
          {title} · your saved materials and the manual steps to send them
          yourself.
        </p>
      </div>

      <StageExplainer stage="handoff" />

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
          description="This role is not selected, so the server keeps no check or artifact state for it. Only chosen roles reach handoff."
        />
      ) : (
        <>
          <RoleStageIndicator workflow={workflow.data} />
          <HandoffDetailSection
            key={jobId}
            jobId={jobId}
            view={opportunity.data}
            workflow={workflow.data}
          />
        </>
      )}
    </div>
  )
}

function HandoffDetailSection({
  jobId,
  view,
  workflow,
}: {
  jobId: string
  view: OpportunityView
  workflow: RoleWorkflowState
}) {
  const check = useRead(`handoff:${jobId}:check`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )
  const handoff = useRead(
    `handoff:${jobId}:projection`,
    (signal) => getOpportunityHandoff(jobId, signal),
    { scopes: ["materials"] }
  )

  if (check.status === "loading" || handoff.status === "loading") {
    return <LoadingBlock label="Loading handoff state…" />
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
  if (handoff.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the saved handoff"
        message={handoff.error}
        onRetry={handoff.retry}
      />
    )
  }
  return (
    <HandoffBody
      jobId={jobId}
      view={view}
      workflow={workflow}
      check={check.data}
      handoff={handoff.data}
    />
  )
}

function HandoffBody({
  jobId,
  view,
  workflow,
  check,
  handoff,
}: {
  jobId: string
  view: OpportunityView
  workflow: RoleWorkflowState
  check: CheckStatusView
  handoff: HandoffView
}) {
  const detail = check.check ?? null
  if (check.status === "not_checked" || detail === null) {
    return (
      <EmptyBlock
        title="No check yet"
        description="Handoff starts from a completed job check. Start a check first; the destination and materials appear here."
      />
    )
  }
  if (check.status === "checking") {
    return (
      <EmptyBlock
        title="Check in progress"
        description="The job check is still running. Handoff opens once the check completes."
      />
    )
  }
  // Blocked and outdated checks keep prior handoff content readable with
  // an honest banner; nothing is displayed as ready.
  const stale =
    check.status === "blocked" ||
    check.status === "outdated" ||
    (handoff.checkId !== undefined &&
      handoff.checkId !== "" &&
      handoff.checkId !== detail.id)
  return (
    <div className="flex min-w-0 flex-col gap-6">
      {stale ? (
        <p
          role="status"
          className="rounded-xl border border-border p-4 text-sm wrap-break-word"
        >
          {check.status === "outdated"
            ? "The role changed after these materials were saved. They stay readable below but are outdated — nothing here counts as ready. Start a recheck."
            : check.status === "blocked"
              ? "The latest check is blocked, so this handoff shows the last saved materials, which are not current. Resolve the block with a recheck."
              : "This handoff belongs to an older check. It stays readable but is outdated — nothing here counts as ready."}
        </p>
      ) : null}
      <DestinationSection view={view} detail={detail} />
      <ManualChecklist jobId={jobId} handoff={handoff} stale={stale} />
      <SavedItemsSection jobId={jobId} handoff={handoff} stale={stale} />
      <SaveHandoffSection jobId={jobId} workflow={workflow} />
    </div>
  )
}

function DestinationSection({
  view,
  detail,
}: {
  view: OpportunityView
  detail: CheckDetail
}) {
  const route = detail.route
  const destination = route.destinationText?.trim() ?? ""
  const href = destination === "" ? null : destinationHref(destination)
  const verified =
    route.judgment === "application_route" && destination !== ""
  return (
    <section
      aria-label="Verified destination"
      className="flex min-w-0 flex-col gap-2 rounded-xl border border-border p-4"
    >
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <h2 className="min-w-0 flex-1 text-base font-semibold wrap-break-word">
          Where you apply
        </h2>
        <Badge variant={verified ? "default" : "secondary"}>
          {verified ? routeKindLabel(route.kind) : "No verified destination"}
        </Badge>
      </div>
      {verified ? (
        <>
          <p className="text-sm wrap-break-word">
            {href === null ? (
              destination
            ) : (
              <a
                href={href}
                target="_blank"
                rel="noreferrer"
                className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                {destination}
              </a>
            )}
          </p>
          <p className="text-xs wrap-break-word text-muted-foreground">
            Open this yourself in your browser or email app. The app opens
            nothing for you and an opened link is not a submission.
          </p>
        </>
      ) : (
        <>
          <p className="text-sm wrap-break-word">
            {route.judgment === "unresolved"
              ? "The check could not verify an application destination."
              : route.judgment === "other_contact"
                ? "The check found only a general contact, not an application destination."
                : "The check recorded no application destination."}{" "}
            Go back to the check before applying anywhere.
          </p>
          <p className="text-sm wrap-break-word">
            Listing page (not the application destination):{" "}
            <a
              href={view.opportunity.sourceUrl}
              target="_blank"
              rel="noreferrer"
              className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              {view.opportunity.sourceUrl}
            </a>
          </p>
        </>
      )}
      <p className="text-xs wrap-break-word text-muted-foreground">
        {`Route evidence: “${route.sourceExcerpt}”`}
      </p>
    </section>
  )
}

interface ChecklistStep {
  id: string
  text: string
}

function ManualChecklist({
  jobId,
  handoff,
  stale,
}: {
  jobId: string
  handoff: HandoffView
  stale: boolean
}) {
  const prepareHref = `#/jobs/${encodeURIComponent(jobId)}/prepare`
  const answersHref = `#/jobs/${encodeURIComponent(jobId)}/answers`
  const steps: ChecklistStep[] = []
  const blockers: string[] = []
  const optionalHeld: string[] = []

  const byType = new Map(handoff.items.map((entry) => [entry.type, entry]))
  const subject = byType.get("email_subject")
  const body = byType.get("email_body")
  const cv = byType.get("cv")
  const letter = byType.get("cover_letter")
  const forms = byType.get("form_values")

  const usable = (item: HandoffItem | undefined): boolean =>
    !stale && item?.state === "ready"

  if (usable(subject) && subject?.content !== undefined) {
    steps.push({
      id: "subject",
      text: `Copy the email subject (v${subject.version ?? "?"}) into your email's subject line.`,
    })
  } else if (subject !== undefined && subject.state !== "not_required") {
    ;(subject.required ? blockers : optionalHeld).push(
      `Email subject: ${subject.reason}`
    )
  }
  if (usable(body) && body?.content !== undefined) {
    steps.push({
      id: "body",
      text: `Paste motivation email v${body.version ?? "?"} into your email body. The full text is below.`,
    })
  } else if (body !== undefined && body.state !== "not_required") {
    ;(body.required ? blockers : optionalHeld).push(
      `Motivation email: ${body.reason}`
    )
  }
  if (usable(cv) && cv?.version !== undefined) {
    steps.push({
      id: "cv",
      text: `Download tailored CV v${cv.version} and attach the file yourself.`,
    })
  } else if (cv !== undefined && cv.state !== "not_required") {
    ;(cv.required ? blockers : optionalHeld).push(
      `Tailored CV: ${cv.reason}`
    )
  }
  if (usable(letter) && letter?.version !== undefined) {
    steps.push({
      id: "letter",
      text: `Download motivation letter v${letter.version} and attach the file yourself.`,
    })
  } else if (letter !== undefined && letter.state !== "not_required") {
    ;(letter.required ? blockers : optionalHeld).push(
      `Motivation letter: ${letter.reason}`
    )
  }
  const filledForms =
    usable(forms) && forms !== undefined
      ? ((forms.formValues ?? []).filter(
          (value) => value.text !== ""
        ) as ArtifactFormValue[])
      : []
  if (usable(forms)) {
    steps.push({
      id: "forms",
      text:
        filledForms.length === 0
          ? "All form values are blank — nothing to paste. Answer them first if the portal requires them."
          : `Fill the portal form fields yourself with the ${filledForms.length} ready ${filledForms.length === 1 ? "value" : "values"} below.`,
    })
  } else if (forms !== undefined && forms.state !== "not_required") {
    ;(forms.required ? blockers : optionalHeld).push(
      `Form values: ${forms.reason}`
    )
  }
  for (const upload of handoff.uploads) {
    const mapped =
      upload.artifactType !== undefined &&
      upload.artifactType !== "" &&
      upload.version !== undefined
    if (mapped && !stale && upload.state === "ready") {
      steps.push({
        id: `upload-${upload.questionId}`,
        text: `Attach your downloaded ${artifactTypeLabel(upload.artifactType ?? "")} (v${upload.version}) to “${upload.questionText}” yourself.`,
      })
    } else {
      blockers.push(
        upload.artifactType === undefined || upload.artifactType === ""
          ? `Upload for “${upload.questionText}”: no file is mapped — see Prepare.`
          : `Upload for “${upload.questionText}”: ${artifactTypeLabel(upload.artifactType)} is ${handoffStateLabel(upload.state).toLowerCase()}.`
      )
    }
  }

  return (
    <section
      aria-label="Manual checklist"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Your checklist
      </h2>
      {blockers.length > 0 ? (
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-sm font-medium wrap-break-word">
            Still needed before you can send:
          </p>
          <ul className="flex min-w-0 flex-col gap-1 text-sm wrap-break-word">
            {blockers.map((blocker) => (
              <li key={blocker}>{blocker}</li>
            ))}
          </ul>
          <p className="text-sm wrap-break-word">
            <a
              href={prepareHref}
              className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              Open Prepare
            </a>{" "}
            or{" "}
            <a
              href={answersHref}
              className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              Open answers
            </a>{" "}
            to resolve them.
          </p>
        </div>
      ) : null}
      {optionalHeld.length > 0 ? (
        <div className="flex min-w-0 flex-col gap-1">
          <p className="text-sm font-medium wrap-break-word">
            Optional, not ready:
          </p>
          <ul className="flex min-w-0 flex-col gap-1 text-sm wrap-break-word text-muted-foreground">
            {optionalHeld.map((held) => (
              <li key={held}>{held}</li>
            ))}
          </ul>
        </div>
      ) : null}
      {steps.length === 0 ? (
        <p className="text-sm wrap-break-word text-muted-foreground">
          Nothing is ready to send yet. Prepare the required items first.
        </p>
      ) : (
        <ol className="flex min-w-0 list-decimal flex-col gap-1 pl-5 text-sm wrap-break-word">
          {steps.map((step) => (
            <li key={step.id}>{step.text}</li>
          ))}
          <li>
            You press send or submit in your own email app or browser. The
            app cannot do this for you.
          </li>
        </ol>
      )}
      <ReadyTexts jobId={jobId} handoff={handoff} stale={stale} />
    </section>
  )
}

function ReadyTexts({
  jobId,
  handoff,
  stale,
}: {
  jobId: string
  handoff: HandoffView
  stale: boolean
}) {
  const texts: Array<{
    id: string
    label: string
    type: string
    version: number | null
    content: string
  }> = []
  for (const entry of handoff.items) {
    if (stale || entry.state !== "ready") continue
    if (
      (entry.type === "email_subject" || entry.type === "email_body") &&
      entry.content !== undefined
    ) {
      texts.push({
        id: entry.type,
        label: `${artifactTypeLabel(entry.type)} v${entry.version ?? "?"}`,
        type: entry.type,
        version: entry.version ?? null,
        content: entry.content,
      })
    }
  }
  const forms = handoff.items.find((entry) => entry.type === "form_values")
  const formValues =
    !stale && forms?.state === "ready"
      ? ((forms.formValues ?? []).filter(
          (value) => value.text !== ""
        ) as ArtifactFormValue[])
      : []
  if (texts.length === 0 && formValues.length === 0) return null
  return (
    <div className="flex min-w-0 flex-col gap-3">
      <h3 className="text-sm font-medium wrap-break-word">
        Ready texts to copy
      </h3>
      {texts.map((text) => (
        <CopyableText
          key={text.id}
          jobId={jobId}
          label={text.label}
          artifactType={text.type}
          version={text.version}
          content={text.content}
        />
      ))}
      {formValues.map((value) => (
        <CopyableText
          key={value.questionId}
          jobId={jobId}
          label={value.questionText}
          artifactType={null}
          version={null}
          content={value.text}
        />
      ))}
    </div>
  )
}

function CopyableText({
  jobId,
  label,
  artifactType,
  version,
  content,
}: {
  jobId: string
  label: string
  artifactType: string | null
  version: number | null
  content: string
}) {
  const { loseSession } = useSession()
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)
  const [exportState, setExportState] = useState<
    "idle" | "working" | "failed"
  >("idle")

  async function copy() {
    if (await copyText(content)) {
      setCopied(true)
      setCopyFailed(false)
    } else {
      setCopied(false)
      setCopyFailed(true)
    }
  }

  async function download() {
    if (
      artifactType === null ||
      version === null ||
      exportState === "working"
    )
      return
    setExportState("working")
    try {
      const exported = await exportOpportunityArtifact(
        jobId,
        artifactType,
        version
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
    setCopied(false)
  }

  return (
    <div className="flex min-w-0 flex-col gap-1 rounded-md border border-border p-3">
      <p className="text-sm font-medium wrap-break-word">{label}</p>
      <pre className="min-w-0 overflow-x-auto text-sm wrap-break-word whitespace-pre-wrap">
        {content}
      </pre>
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          aria-label={`Copy ${label}`}
          onClick={() => void copy()}
        >
          Copy
        </Button>
        {artifactType === null || version === null ? null : (
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label={`Download ${label}`}
            disabled={exportState === "working"}
            onClick={() => void download()}
          >
            {exportState === "working" ? "Exporting…" : "Download"}
          </Button>
        )}
        {copied ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copied.
          </p>
        ) : null}
        {copyFailed ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Copy failed — select the text manually.
          </p>
        ) : null}
        {exportState === "failed" ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Download failed — copy the text instead.
          </p>
        ) : null}
      </div>
    </div>
  )
}

function SavedItemsSection({
  jobId,
  handoff,
  stale,
}: {
  jobId: string
  handoff: HandoffView
  stale: boolean
}) {
  const prepareHref = `#/jobs/${encodeURIComponent(jobId)}/prepare`
  if (handoff.items.length === 0) {
    return (
      <EmptyBlock
        title="No route items"
        description="The verified route requires no artifacts for this role."
      />
    )
  }
  return (
    <section
      aria-label="Saved artifacts"
      className="flex min-w-0 flex-col gap-3"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Saved artifacts
      </h2>
      <ul className="flex min-w-0 flex-col gap-2">
        {handoff.items.map((entry) => {
          const effective = stale ? "outdated" : entry.state
          return (
            <li
              key={entry.type}
              className="flex min-w-0 flex-col gap-1 rounded-xl border border-border p-4"
            >
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <p className="min-w-0 flex-1 text-sm font-medium wrap-break-word">
                  {artifactTypeLabel(entry.type)}
                  {entry.version !== undefined ? ` · v${entry.version}` : ""}
                </p>
                <Badge
                  variant={effective === "ready" ? "default" : "secondary"}
                >
                  {handoffStateLabel(effective)}
                </Badge>
                <Badge variant="secondary">
                  {entry.required ? "Required" : "Optional"}
                </Badge>
              </div>
              <p className="text-sm wrap-break-word text-muted-foreground">
                {stale
                  ? `Outdated — last saved state was ${handoffStateLabel(entry.state).toLowerCase()}: ${entry.reason}`
                  : entry.reason}
              </p>
              {entry.contentSha256 !== undefined &&
              entry.contentSha256 !== "" ? (
                <p className="text-xs wrap-break-word text-muted-foreground">
                  {`Checksum ${entry.contentSha256.slice(0, 16)}…`}
                </p>
              ) : null}
              {entry.state === "ready" && !stale ? (
                <p className="text-sm wrap-break-word">
                  <a
                    href={prepareHref}
                    aria-label={`Open full ${artifactTypeLabel(entry.type)} in Prepare`}
                    className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                  >
                    Open full material in Prepare
                  </a>
                </p>
              ) : null}
            </li>
          )
        })}
      </ul>
      {handoff.uploads.length === 0 ? null : (
        <div className="flex min-w-0 flex-col gap-2">
          <h3 className="text-sm font-medium wrap-break-word">
            Upload mapping
          </h3>
          <ul className="flex min-w-0 flex-col gap-2">
            {handoff.uploads.map((upload) => (
              <li
                key={upload.questionId}
                className="flex min-w-0 flex-col gap-1 rounded-xl border border-border p-4"
              >
                <p className="text-sm font-medium wrap-break-word">
                  {upload.questionText}
                </p>
                <p className="text-xs wrap-break-word text-muted-foreground">
                  {`${upload.required} · ${upload.artifactType !== undefined && upload.artifactType !== "" ? artifactTypeLabel(upload.artifactType) : "no file mapped"}${upload.version !== undefined ? ` v${upload.version}` : ""} · ${handoffStateLabel(stale ? "outdated" : upload.state).toLowerCase()}`}
                </p>
              </li>
            ))}
          </ul>
        </div>
      )}
    </section>
  )
}

function SaveHandoffSection({
  jobId,
  workflow,
}: {
  jobId: string
  workflow: RoleWorkflowState
}) {
  const { session, loseSession } = useSession()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (workflow.stage === "handoff_saved") {
    return (
      <section
        aria-label="Handoff record"
        className="flex min-w-0 flex-col gap-2 rounded-xl border border-border p-4"
      >
        <h2 className="text-base font-semibold wrap-break-word">
          Handoff saved
        </h2>
        <p className="text-sm wrap-break-word text-muted-foreground">
          This manual list is saved and stays here for you to revisit. It
          never sent anything — you apply yourself.
        </p>
      </section>
    )
  }

  if (workflow.stage !== "prepared") {
    return (
      <section
        aria-label="Handoff record"
        className="flex min-w-0 flex-col gap-2 rounded-xl border border-border p-4"
      >
        <h2 className="text-base font-semibold wrap-break-word">
          Handoff record
        </h2>
        <p className="text-sm wrap-break-word text-muted-foreground">
          {`Handoff saves once preparation completes (current stage: ${workflow.stage}). `}
          <a
            href={`#/jobs/${encodeURIComponent(jobId)}/prepare`}
            className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Open Prepare
          </a>
        </p>
      </section>
    )
  }

  async function save() {
    if (session === undefined || session === null || saving) return
    setSaving(true)
    setError(null)
    try {
      await saveOpportunityHandoff(
        jobId,
        buildHandoffSaveRequest(workflow.revision),
        session.csrfToken
      )
      setSaving(false)
      notifyAccepted("workflows", "materials")
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
    <section
      aria-label="Handoff record"
      className="flex min-w-0 flex-col gap-3 rounded-xl border border-border p-4"
    >
      <h2 className="text-base font-semibold wrap-break-word">
        Save this handoff
      </h2>
      <p className="text-sm wrap-break-word text-muted-foreground">
        This only records your manual list here so you can revisit it. You
        still apply yourself — the app never fills, attaches, sends or
        submits anything.
      </p>
      <div>
        <Button
          type="button"
          size="sm"
          disabled={saving || session === undefined || session === null}
          onClick={() => void save()}
        >
          {saving ? "Saving…" : "Save handoff record"}
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
