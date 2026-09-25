import { useState } from "react"
import {
  getCurrentOpportunityCheck,
  getOpportunity,
  getRoleWorkflowOrNull,
  isUnauthenticated,
  listOpportunityCheckActivity,
  type CheckStatusView,
  type CheckView,
  type ResearchActivityEvent,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
} from "@/components/shared"
import { Button } from "@/components/ui/button"
import { CheckActivityFeed } from "@/features/check/check-activity-feed"
import { useCheckStart } from "@/features/check/useCheckStart"
import { formatDate } from "@/pages/format"
import { RoleStageIndicator } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

// CheckPage is the D2 check job details page for one role, reached by deep
// link (#/jobs/:id/check, registered by the coordinator). Mount, reads and
// reloads are GET-only and never start a check; the only mutation is the
// explicit start control, which posts the requestKey plus the expected
// revisions observed from the reads below. All state is keyed by jobId and
// the detail section remounts per role, so each role advances independently.
export function CheckPage({ jobId }: { jobId: string }) {
  const opportunity = useRead(`check:${jobId}:opportunity`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const workflow = useRead(`check:${jobId}:workflow`, (signal) =>
    getRoleWorkflowOrNull(jobId, signal)
  )
  const title =
    opportunity.status === "ready" &&
    opportunity.data.opportunity.title !== ""
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
          Check job details
        </h1>
        <p className="mt-1 text-sm text-muted-foreground wrap-break-word">
          {title} · opening this page only reads saved state; a check starts
          solely from the explicit action below.
        </p>
      </div>

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
          description="This role is not selected, so the server keeps no check state for it. Only chosen roles can be checked."
        />
      ) : (
        <>
          <RoleStageIndicator workflow={workflow.data} />
          <CheckDetailSection
            key={jobId}
            jobId={jobId}
            opportunityRevision={opportunity.data.opportunity.revision}
            workflowRevision={workflow.data.revision}
            onStarted={workflow.retry}
          />
        </>
      )}
    </div>
  )
}

function CheckDetailSection({
  jobId,
  opportunityRevision,
  workflowRevision,
  onStarted,
}: {
  jobId: string
  opportunityRevision: number
  workflowRevision: number
  onStarted: () => void
}) {
  const { loseSession } = useSession()
  const check = useRead(`check:${jobId}:current`, (signal) =>
    getCurrentOpportunityCheck(jobId, signal)
  )
  const firstActivityPage = useRead(`check:${jobId}:activity`, (signal) =>
    listOpportunityCheckActivity(jobId, "", 25, signal)
  )
  const [extraEvents, setExtraEvents] = useState<ResearchActivityEvent[]>([])
  const [extraCursor, setExtraCursor] = useState<string | null>(null)
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null)

  const events =
    firstActivityPage.status === "ready"
      ? [...firstActivityPage.data.events, ...extraEvents]
      : extraEvents
  const nextCursor =
    extraCursor ??
    (firstActivityPage.status === "ready"
      ? (firstActivityPage.data.nextCursor ?? "")
      : "")

  function refreshAll() {
    check.retry()
    firstActivityPage.retry()
    setExtraEvents([])
    setExtraCursor(null)
    setLoadMoreError(null)
  }

  async function loadMore() {
    if (nextCursor === "" || loadingMore) return
    setLoadingMore(true)
    setLoadMoreError(null)
    try {
      const page = await listOpportunityCheckActivity(jobId, nextCursor, 25)
      const seen = new Set(events.map((event) => event.eventId))
      const fresh = page.events.filter((event) => !seen.has(event.eventId))
      setExtraEvents((previous) => [...previous, ...fresh])
      setExtraCursor(page.nextCursor ?? "")
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setLoadMoreError(
        cause instanceof Error
          ? cause.message
          : "The request could not be completed."
      )
    } finally {
      setLoadingMore(false)
    }
  }

  if (check.status === "loading")
    return <LoadingBlock label="Loading saved check…" />
  if (check.status === "error")
    return (
      <ErrorBlock
        title="Could not load the saved check"
        message={check.error}
        onRetry={refreshAll}
      />
    )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <CheckStatusSection
        jobId={jobId}
        status={check.data}
        opportunityRevision={opportunityRevision}
        workflowRevision={workflowRevision}
        onStarted={() => {
          refreshAll()
          onStarted()
        }}
        onRefresh={refreshAll}
      />

      <section aria-labelledby="check-activity-heading">
        <h2
          id="check-activity-heading"
          className="font-heading text-lg font-medium"
        >
          Check activity
        </h2>
        <div className="mt-3">
          {firstActivityPage.status === "loading" ? (
            <LoadingBlock label="Loading check activity…" />
          ) : firstActivityPage.status === "error" ? (
            <ErrorBlock
              title="Could not load check activity"
              message={firstActivityPage.error}
              onRetry={refreshAll}
            />
          ) : (
            <CheckActivityFeed
              events={events}
              nextCursor={nextCursor}
              loadingMore={loadingMore}
              loadMoreError={loadMoreError}
              onLoadMore={() => void loadMore()}
            />
          )}
        </div>
      </section>
    </div>
  )
}

function CheckStatusSection({
  jobId,
  status,
  opportunityRevision,
  workflowRevision,
  onStarted,
  onRefresh,
}: {
  jobId: string
  status: CheckStatusView
  opportunityRevision: number
  workflowRevision: number
  onStarted: () => void
  onRefresh: () => void
}) {
  switch (status.status) {
    case "not_checked":
      return (
        <section
          aria-labelledby="check-status-heading"
          className="flex min-w-0 flex-col gap-3"
        >
          <h2
            id="check-status-heading"
            className="font-heading text-lg font-medium"
          >
            Saved check
          </h2>
          <EmptyBlock
            title="No check yet"
            description="Codex has not checked this role. Starting a check reads the live posting and records the vacancy, route, gaps and employer questions."
          />
          <StartCheckControls
            jobId={jobId}
            label="Start check"
            opportunityRevision={opportunityRevision}
            workflowRevision={workflowRevision}
            onStarted={onStarted}
          />
        </section>
      )
    case "checking":
      return (
        <section
          aria-labelledby="check-status-heading"
          className="flex min-w-0 flex-col gap-3"
        >
          <h2
            id="check-status-heading"
            className="font-heading text-lg font-medium"
          >
            Saved check
          </h2>
          <LoadingBlock
            label="Check in progress…"
            detail="Codex is checking this role. This page only reads saved state — refresh to see the latest."
          />
          {status.check !== undefined ? (
            <CheckEvidence check={status.check} />
          ) : null}
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onRefresh}
            >
              Refresh saved check
            </Button>
          </div>
        </section>
      )
    case "checked":
      return (
        <section
          aria-labelledby="check-status-heading"
          className="flex min-w-0 flex-col gap-3"
        >
          <h2
            id="check-status-heading"
            className="font-heading text-lg font-medium"
          >
            Saved check
          </h2>
          {status.check === undefined ? (
            <ErrorBlock
              title="Saved check details missing"
              message="The server reported “checked” without saved check details."
              onRetry={onRefresh}
            />
          ) : (
            <>
              <CheckEvidence check={status.check} />
              <p>
                <a
                  href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
                  className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Answer questions
                </a>
              </p>
            </>
          )}
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={onRefresh}
            >
              Refresh saved check
            </Button>
          </div>
        </section>
      )
    case "blocked":
      return (
        <section
          aria-labelledby="check-status-heading"
          className="flex min-w-0 flex-col gap-3"
        >
          <h2
            id="check-status-heading"
            className="font-heading text-lg font-medium"
          >
            Saved check
          </h2>
          <ErrorBlock
            title="Check blocked"
            message={
              status.check?.blockedReason === undefined
                ? "Blocked — the server recorded no reason."
                : `Blocked — ${status.check.blockedReason.code}: ${status.check.blockedReason.detail}`
            }
          />
          {status.check !== undefined ? (
            <CheckEvidence check={status.check} />
          ) : null}
          <StartCheckControls
            jobId={jobId}
            label="Retry check"
            opportunityRevision={opportunityRevision}
            workflowRevision={workflowRevision}
            onStarted={onStarted}
          />
        </section>
      )
    case "outdated":
      return (
        <section
          aria-labelledby="check-status-heading"
          className="flex min-w-0 flex-col gap-3"
        >
          <h2
            id="check-status-heading"
            className="font-heading text-lg font-medium"
          >
            Saved check
          </h2>
          <EmptyBlock
            title="Saved check is outdated"
            description="The role changed since this check was saved. Run a new check to refresh the vacancy, route, gaps and employer questions."
          />
          {status.check !== undefined ? (
            <>
              <CheckEvidence check={status.check} />
              <p>
                <a
                  href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
                  className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Answer questions
                </a>
              </p>
            </>
          ) : null}
          <StartCheckControls
            jobId={jobId}
            label="Run a new check"
            opportunityRevision={opportunityRevision}
            workflowRevision={workflowRevision}
            onStarted={onStarted}
          />
        </section>
      )
  }
}

function StartCheckControls({
  jobId,
  label,
  opportunityRevision,
  workflowRevision,
  onStarted,
}: {
  jobId: string
  label: string
  opportunityRevision: number
  workflowRevision: number
  onStarted: () => void
}) {
  const action = useCheckStart({
    jobId,
    opportunityRevision,
    workflowRevision,
    onStarted,
  })
  return (
    <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
      <p className="text-sm text-muted-foreground">
        Uses opportunity revision {opportunityRevision} and workflow revision{" "}
        {workflowRevision}. The server replays an identical request instead of
        starting a duplicate check.
      </p>
      <div>
        <Button
          type="button"
          disabled={!action.canStart}
          onClick={action.start}
        >
          {action.starting ? "Starting…" : label}
        </Button>
      </div>
      {action.unavailableReason !== null && !action.starting ? (
        <p className="text-xs text-muted-foreground">
          {action.unavailableReason}
        </p>
      ) : null}
      {action.error !== null ? (
        <ErrorBlock title="Could not start the check" message={action.error} />
      ) : null}
    </div>
  )
}

function CheckEvidence({ check }: { check: CheckView }) {
  const route = check.route
  const sortedQuestions = [...check.questions].sort(
    (a, b) => a.ordinal - b.ordinal
  )
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <section
        aria-labelledby="check-vacancy-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h3
          id="check-vacancy-heading"
          className="font-heading text-base font-medium"
        >
          Saved vacancy
        </h3>
        <dl className="mt-3 flex min-w-0 flex-col gap-2 text-sm">
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Source:</dt>
            <dd className="min-w-0">
              {check.vacancy.sourceUrl === "" ? (
                "Not recorded"
              ) : (
                <a
                  href={check.vacancy.sourceUrl}
                  className="underline underline-offset-4 outline-none wrap-break-word focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  {check.vacancy.sourceUrl}
                </a>
              )}
            </dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Completeness:</dt>
            <dd className="wrap-break-word">{check.vacancy.completeness}</dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Retrieved:</dt>
            <dd title={check.vacancy.retrievedAt}>
              {formatDate(check.vacancy.retrievedAt)}
            </dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Evidence:</dt>
            <dd className="wrap-break-word">
              {check.vacancy.captureIds.length}{" "}
              {check.vacancy.captureIds.length === 1 ? "capture" : "captures"},{" "}
              {check.vacancy.evidenceSourceIds.length} evidence{" "}
              {check.vacancy.evidenceSourceIds.length === 1
                ? "source"
                : "sources"}
            </dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Record:</dt>
            <dd className="wrap-break-word">
              opportunity revision {check.opportunityRevision}, workflow
              revision {check.workflowRevision}, question set v
              {check.questionSetVersion}
            </dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Recorded:</dt>
            <dd title={check.createdAt}>{formatDate(check.createdAt)}</dd>
          </div>
        </dl>
      </section>

      <section
        aria-labelledby="check-route-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h3
          id="check-route-heading"
          className="font-heading text-base font-medium"
        >
          Application route
        </h3>
        <dl className="mt-3 flex min-w-0 flex-col gap-2 text-sm">
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Judgment:</dt>
            <dd className="wrap-break-word">
              {route.judgment}
              {route.kind === undefined ? "" : ` (${route.kind})`}
            </dd>
          </div>
          {route.destinationText === undefined ||
          route.destinationText === "" ? null : (
            <div className="min-w-0">
              <dt className="text-muted-foreground">Destination</dt>
              <dd className="wrap-break-word">{route.destinationText}</dd>
            </div>
          )}
          <div className="min-w-0">
            <dt className="text-muted-foreground">Source excerpt</dt>
            <dd className="wrap-break-word">
              {route.sourceExcerpt === "" ? "Not recorded" : route.sourceExcerpt}
            </dd>
          </div>
        </dl>
      </section>

      <section
        aria-labelledby="check-questions-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h3
          id="check-questions-heading"
          className="font-heading text-base font-medium"
        >
          Employer questions
        </h3>
        {sortedQuestions.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">
            No employer questions recorded.
          </p>
        ) : (
          <ol className="mt-3 flex min-w-0 flex-col gap-3">
            {sortedQuestions.map((question) => (
              <li
                key={question.id}
                className="min-w-0 rounded-lg border px-3 py-2 text-sm"
              >
                <p className="text-xs text-muted-foreground">
                  Question {question.ordinal + 1}
                </p>
                <p className="wrap-break-word">{question.text}</p>
                <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                  {question.required}
                  {question.kind === undefined ? "" : ` · ${question.kind}`}
                </p>
                {question.sourceExcerpt === "" ? null : (
                  <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                    Excerpt: {question.sourceExcerpt}
                  </p>
                )}
              </li>
            ))}
          </ol>
        )}
      </section>

      <section
        aria-labelledby="check-documents-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h3
          id="check-documents-heading"
          className="font-heading text-base font-medium"
        >
          Requested documents
        </h3>
        {check.requestedDocuments.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">
            No requested documents recorded.
          </p>
        ) : (
          <ul className="mt-3 flex min-w-0 flex-col gap-2 text-sm">
            {check.requestedDocuments.map((document, index) => (
              <li
                key={`${document.label}-${index}`}
                className="min-w-0 rounded-lg border px-3 py-2"
              >
                <p className="wrap-break-word">
                  {document.label} ·{" "}
                  {document.required ? "required" : "optional"}
                </p>
                {document.sourceExcerpt === "" ? null : (
                  <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                    Excerpt: {document.sourceExcerpt}
                  </p>
                )}
              </li>
            ))}
          </ul>
        )}
      </section>

      <section
        aria-labelledby="check-gaps-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h3
          id="check-gaps-heading"
          className="font-heading text-base font-medium"
        >
          Gaps
        </h3>
        {check.gaps.length === 0 ? (
          <p className="mt-3 text-sm text-muted-foreground">
            No gaps recorded.
          </p>
        ) : (
          <ul className="mt-3 flex min-w-0 flex-col gap-2 text-sm">
            {check.gaps.map((gap) => (
              <li
                key={gap.id}
                className="min-w-0 rounded-lg border px-3 py-2"
              >
                <p className="wrap-break-word">{gap.description}</p>
                <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                  {gap.kind}
                  {gap.consequential ? " · consequential" : ""}
                </p>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  )
}
