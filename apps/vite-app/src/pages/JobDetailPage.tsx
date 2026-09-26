import {
  getOpportunity,
  getOwnerOpportunityDecision,
  type OpportunityView,
  type OwnerDecision,
} from "@/api/client"
import {
  ActivityDisclosure,
  type ActivityEntry,
} from "@/components/shared/activity-disclosure"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { describeCompensation, formatDate } from "@/pages/format"
import { RoleStageSection } from "@/pages/role-stages"
import { useRead, type ReadResult } from "@/pages/useRead"

export function JobDetailPage({ jobId }: { jobId: string }) {
  const opportunity = useRead(`job:${jobId}`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const decision = useRead(
    `job:${jobId}:decision`,
    (signal) => getOwnerOpportunityDecision(jobId, signal),
    { scopes: ["selection"] }
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex gap-4">
        <a
          href="#/jobs"
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to Jobs
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/check`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Check job details
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/answers`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Answer questions
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/prepare`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Prepare application
        </a>
        <a
          href={`#/jobs/${encodeURIComponent(jobId)}/handoff`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Handoff
        </a>
      </p>

      {opportunity.status === "loading" ? (
        <LoadingBlock label="Loading job details…" />
      ) : opportunity.status === "error" ? (
        <ErrorBlock
          title="Could not load this job"
          message={opportunity.error}
          onRetry={opportunity.retry}
        />
      ) : (
        <JobDetailBody view={opportunity.data} decision={decision} />
      )}
    </div>
  )
}

function JobDetailBody({
  view,
  decision,
}: {
  view: OpportunityView
  decision: ReadResult<OwnerDecision | null>
}) {
  const job = view.opportunity
  const compensation = describeCompensation(job.compensation)
  const entries: ActivityEntry[] = []
  if (decision.status === "ready" && decision.data !== null)
    entries.push({
      id: "decision",
      kind: "result",
      text: `Owner decision: ${decision.data.decision} (recorded ${formatDate(decision.data.createdAt)})`,
    })
  for (const duplicate of view.likelyDuplicates)
    entries.push({
      id: `duplicate-${duplicate.opportunity.id}`,
      kind: "result",
      text: `Possible duplicate (${duplicate.reason}): ${duplicate.opportunity.title === "" ? "(untitled role)" : duplicate.opportunity.title}`,
    })
  if (job.sourceUrl !== "")
    entries.push({
      id: "source",
      kind: "source",
      text: "Original posting",
      href: job.sourceUrl,
    })
  const disclosureStatus =
    decision.status === "ready" && decision.data !== null
      ? `Owner decision: ${decision.data.decision}`
      : decision.status === "error"
        ? "Decision read failed"
        : view.likelyDuplicates.length > 0
          ? `${view.likelyDuplicates.length} possible ${view.likelyDuplicates.length === 1 ? "duplicate" : "duplicates"} flagged`
          : "No review activity recorded"

  return (
    <>
      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          {job.title === "" ? "(untitled role)" : job.title}
        </h1>
        <p className="mt-1 text-sm text-muted-foreground wrap-break-word">
          {job.kind} · {job.workPattern}
          {job.locationText === "" ? "" : ` · ${job.locationText}`}
        </p>
      </div>

      <section
        aria-labelledby="job-facts-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h2 id="job-facts-heading" className="font-heading text-lg font-medium">
          Saved details
        </h2>
        <dl className="mt-3 flex min-w-0 flex-col gap-2 text-sm">
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Stage:</dt>
            <dd className="wrap-break-word">
              {job.stage === "" ? "Not recorded" : job.stage}
            </dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Posted:</dt>
            <dd>{job.postedOn === "" ? "Not recorded" : job.postedOn}</dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Deadline:</dt>
            <dd>{job.deadlineOn === "" ? "Not recorded" : job.deadlineOn}</dd>
          </div>
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Advertised pay:</dt>
            <dd className="wrap-break-word">
              {compensation ?? "No advertised compensation recorded."}
            </dd>
          </div>
          {job.compensation.benefitsText !== undefined &&
          job.compensation.benefitsText !== "" ? (
            <div className="min-w-0">
              <dt className="text-muted-foreground">Benefits text</dt>
              <dd className="wrap-break-word">
                {job.compensation.benefitsText}
              </dd>
            </div>
          ) : null}
          {job.notes !== "" ? (
            <div className="min-w-0">
              <dt className="text-muted-foreground">Notes</dt>
              <dd className="wrap-break-word">{job.notes}</dd>
            </div>
          ) : null}
          {job.sourceUrl !== "" ? (
            <div className="flex min-w-0 flex-wrap gap-x-2">
              <dt className="shrink-0 text-muted-foreground">Source:</dt>
              <dd className="min-w-0">
                <a
                  href={job.sourceUrl}
                  className="underline underline-offset-4 outline-none wrap-break-word focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  {job.sourceUrl}
                </a>
              </dd>
            </div>
          ) : null}
          <div className="flex min-w-0 flex-wrap gap-x-2">
            <dt className="shrink-0 text-muted-foreground">Record:</dt>
            <dd className="wrap-break-word">
              revision {job.revision}
              {job.archivedAt === undefined
                ? ""
                : ` · archived ${formatDate(job.archivedAt)}`}
            </dd>
          </div>
        </dl>
      </section>

      <section aria-labelledby="job-decision-heading">
        <h2 id="job-decision-heading" className="font-heading text-lg font-medium">
          Owner decision
        </h2>
        <div className="mt-3">
          {decision.status === "loading" ? (
            <LoadingBlock label="Loading owner decision…" />
          ) : decision.status === "error" ? (
            <ErrorBlock
              title="Could not load the owner decision"
              message={decision.error}
              onRetry={decision.retry}
            />
          ) : decision.data === null ? (
            <EmptyBlock
              title="No owner decision recorded"
              description="The server has no saved decision for this role."
            />
          ) : (
            <dl className="flex min-w-0 flex-col gap-1 rounded-md border bg-card px-3 py-2 text-sm">
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">Decision:</dt>
                <dd>{decision.data.decision}</dd>
              </div>
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">Recorded:</dt>
                <dd title={decision.data.createdAt}>
                  {formatDate(decision.data.createdAt)}
                </dd>
              </div>
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">Revision:</dt>
                <dd>{decision.data.revision}</dd>
              </div>
            </dl>
          )}
        </div>
      </section>

      <section aria-labelledby="job-review-heading">
        <h2 id="job-review-heading" className="font-heading text-lg font-medium">
          Review activity
        </h2>
        <div className="mt-3">
          <ActivityDisclosure
            actor="owner"
            phase="Role review"
            status={disclosureStatus}
            entries={entries}
          />
        </div>
      </section>

      <RoleStageSection jobId={view.opportunity.id} />
    </>
  )
}
