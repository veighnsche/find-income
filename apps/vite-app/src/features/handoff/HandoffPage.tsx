import { useState } from "react"
import {
  getCurrentOpportunityCheck,
  getOpportunity,
  getRoleWorkflowOrNull,
  listArtifactReadiness,
  type ArtifactFormValue,
  type ArtifactReadinessEntry,
  type ArtifactReadinessSet,
  type CheckStatusView,
  type OpportunityView,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { StageExplainer } from "@/components/shared/stage-explainer"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  artifactTypeLabel,
  copyText,
  downloadText,
} from "@/features/prepare/ArtifactsSection"
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

function entryStateLabel(state: ArtifactReadinessEntry["state"]): string {
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

function destinationHref(destination: string): string | null {
  const trimmed = destination.trim()
  if (/^https?:\/\//i.test(trimmed)) return trimmed
  if (/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed))
    return `mailto:${trimmed}`
  return null
}

// HandoffPage is the H1–H3 seventh-stage surface for one role: the saved
// artifact list with types/versions, the verified destination, and the
// manual checklist the owner works through themselves. Mount and reads are
// GET-only; copy/download buttons only move saved bytes to the owner's
// clipboard or disk. The app never fills an employer form, attaches,
// sends, or submits anything.
export function HandoffPage({ jobId }: { jobId: string }) {
  const opportunity = useRead(`handoff:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(`handoff:${jobId}:workflow`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
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
          />
        </>
      )}
    </div>
  )
}

function HandoffDetailSection({
  jobId,
  view,
}: {
  jobId: string
  view: OpportunityView
}) {
  const check = useRead(`handoff:${jobId}:check`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )
  const readiness = useRead(`handoff:${jobId}:readiness`, (signal) =>
    listArtifactReadiness(jobId, signal)
  )

  if (check.status === "loading" || readiness.status === "loading") {
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
  if (readiness.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the saved artifacts"
        message={readiness.error}
        onRetry={readiness.retry}
      />
    )
  }
  return (
    <HandoffBody jobId={jobId} view={view} check={check.data} set={readiness.data} />
  )
}

function HandoffBody({
  jobId,
  view,
  check,
  set,
}: {
  jobId: string
  view: OpportunityView
  check: CheckStatusView
  set: ArtifactReadinessSet
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
  if (check.status === "blocked") {
    return (
      <EmptyBlock
        title="Check blocked"
        description="The check could not complete, so there is no verified destination. Resolve it with a recheck before handoff."
      />
    )
  }
  if (check.status === "outdated") {
    return (
      <EmptyBlock
        title="Check outdated"
        description="The role changed after this check completed. Start a recheck; handoff reopens on the fresh destination."
      />
    )
  }
  return (
    <div className="flex min-w-0 flex-col gap-6">
      <DestinationSection view={view} detail={detail} />
      <ManualChecklist jobId={jobId} set={set} />
      <SavedArtifactsSection jobId={jobId} set={set} />
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
  set,
}: {
  jobId: string
  set: ArtifactReadinessSet
}) {
  const prepareHref = `#/jobs/${encodeURIComponent(jobId)}/prepare`
  const answersHref = `#/jobs/${encodeURIComponent(jobId)}/answers`
  const steps: ChecklistStep[] = []
  const blockers: string[] = []

  const byType = new Map(set.entries.map((entry) => [entry.type, entry]))
  const subject = byType.get("email_subject")
  const body = byType.get("email_body")
  const cv = byType.get("cv")
  const letter = byType.get("cover_letter")
  const forms = byType.get("form_values")

  if (subject?.state === "ready" && subject.current !== undefined) {
    steps.push({
      id: "subject",
      text: `Copy the email subject (v${subject.current.version}) into your email's subject line.`,
    })
  } else if (subject?.required === true) {
    blockers.push(`Email subject: ${subject.reason}`)
  }
  if (body?.state === "ready" && body.current !== undefined) {
    steps.push({
      id: "body",
      text: `Paste motivation email v${body.current.version} into your email body. The full text is below.`,
    })
  } else if (body?.required === true) {
    blockers.push(`Motivation email: ${body.reason}`)
  }
  if (cv?.state === "ready" && cv.current !== undefined) {
    steps.push({
      id: "cv",
      text: `Download tailored CV v${cv.current.version} from Prepare and attach the file yourself.`,
    })
  } else if (cv?.required === true) {
    blockers.push(`Tailored CV: ${cv.reason}`)
  }
  if (letter?.state === "ready" && letter.current !== undefined) {
    steps.push({
      id: "letter",
      text: `Download motivation letter v${letter.current.version} from Prepare and attach the file yourself.`,
    })
  } else if (letter?.required === true) {
    blockers.push(`Motivation letter: ${letter.reason}`)
  }
  if (forms?.state === "ready") {
    const filled = (forms.formValues ?? []).filter(
      (value) => value.text !== ""
    ).length
    steps.push({
      id: "forms",
      text:
        filled === 0
          ? "All form values are blank — nothing to paste. Answer them first if the portal requires them."
          : `Fill the portal form fields yourself with the ${filled} ready ${filled === 1 ? "value" : "values"} below.`,
    })
  } else if (forms?.required === true) {
    blockers.push(`Form values: ${forms.reason}`)
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
      <ReadyTexts jobId={jobId} set={set} />
    </section>
  )
}

function ReadyTexts({
  jobId,
  set,
}: {
  jobId: string
  set: ArtifactReadinessSet
}) {
  const texts: Array<{
    id: string
    label: string
    content: string
    filename: string
  }> = []
  for (const entry of set.entries) {
    if (entry.state !== "ready") continue
    if (
      (entry.type === "email_subject" || entry.type === "email_body") &&
      entry.current !== undefined
    ) {
      texts.push({
        id: entry.type,
        label: `${artifactTypeLabel(entry.type)} v${entry.current.version}`,
        content: entry.current.content,
        filename: `${jobId}-${entry.type}-v${entry.current.version}.txt`,
      })
    }
  }
  const forms = set.entries.find((entry) => entry.type === "form_values")
  const formValues =
    forms?.state === "ready"
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
          label={text.label}
          content={text.content}
          filename={text.filename}
        />
      ))}
      {formValues.map((value) => (
        <CopyableText
          key={value.questionId}
          label={value.questionText}
          content={value.text}
          filename={null}
        />
      ))}
    </div>
  )
}

function CopyableText({
  label,
  content,
  filename,
}: {
  label: string
  content: string
  filename: string | null
}) {
  const [copied, setCopied] = useState(false)
  const [copyFailed, setCopyFailed] = useState(false)
  const [downloadFailed, setDownloadFailed] = useState(false)

  async function copy() {
    if (await copyText(content)) {
      setCopied(true)
      setCopyFailed(false)
    } else {
      setCopied(false)
      setCopyFailed(true)
    }
  }

  function download() {
    if (filename === null) return
    setDownloadFailed(!downloadText(filename, content))
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
        {filename === null ? null : (
          <Button
            type="button"
            variant="outline"
            size="sm"
            aria-label={`Download ${label}`}
            onClick={download}
          >
            Download
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
        {downloadFailed ? (
          <p className="text-sm wrap-break-word text-muted-foreground">
            Download failed — copy the text instead.
          </p>
        ) : null}
      </div>
    </div>
  )
}

function SavedArtifactsSection({
  jobId,
  set,
}: {
  jobId: string
  set: ArtifactReadinessSet
}) {
  const prepareHref = `#/jobs/${encodeURIComponent(jobId)}/prepare`
  if (set.entries.length === 0) {
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
        {set.entries.map((entry) => (
          <li
            key={entry.type}
            className="flex min-w-0 flex-col gap-1 rounded-xl border border-border p-4"
          >
            <div className="flex min-w-0 flex-wrap items-center gap-2">
              <p className="min-w-0 flex-1 text-sm font-medium wrap-break-word">
                {artifactTypeLabel(entry.type)}
                {entry.current !== undefined
                  ? ` · v${entry.current.version}`
                  : ""}
              </p>
              <Badge variant={entry.state === "ready" ? "default" : "secondary"}>
                {entryStateLabel(entry.state)}
              </Badge>
              <Badge variant="secondary">
                {entry.required ? "Required" : "Optional"}
              </Badge>
            </div>
            <p className="text-sm wrap-break-word text-muted-foreground">
              {entry.reason}
            </p>
            {entry.state === "ready" ? (
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
        ))}
      </ul>
    </section>
  )
}
