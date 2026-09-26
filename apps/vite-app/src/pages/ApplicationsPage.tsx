import {
  getOpportunity,
  getOwnerOpportunityDecision,
  listOpportunities,
  listRoleWorkflows,
  type RoleWorkflowState,
} from "@/api/client"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
} from "@/components/shared"
import { ApplicationContinue } from "@/pages/application-continue"
import { formatDate } from "@/pages/format"
import { RoleStageSection, stageStatusText } from "@/pages/role-stages"
import { useRead } from "@/pages/useRead"

function continueHref(workflow: RoleWorkflowState): string {
  const id = encodeURIComponent(workflow.opportunityId)
  switch (workflow.stage) {
    case "checking":
    case "checked":
      return `#/jobs/${id}/check`
    case "answering":
    case "answered":
      return `#/jobs/${id}/answers`
    case "preparing":
    case "prepared":
      return `#/jobs/${id}/prepare`
    case "handoff_saved":
      return `#/applications/${id}`
    case "selected":
    case "blocked":
      return `#/jobs/${id}`
  }
}

export function ApplicationsPage({ jobId }: { jobId: string | null }) {
  if (jobId === null) return <ApplicationsList />
  return <ApplicationDetail jobId={jobId} />
}

function ApplicationsList() {
  const workflows = useRead(
    "applications:workflows",
    (signal) => listRoleWorkflows(signal),
    { scopes: ["selection"] }
  )
  const opportunities = useRead("applications:list", (signal) =>
    listOpportunities("", signal)
  )

  const titles =
    opportunities.status === "ready"
      ? new Map(
          opportunities.data.items.map((view) => [
            view.opportunity.id,
            view.opportunity.title,
          ])
        )
      : null

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Applications</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Your chosen roles and their current work. Open a role to continue
          its stage or review its saved history.
        </p>
      </div>

      {workflows.status === "loading" || opportunities.status === "loading" ? (
        <LoadingBlock label="Loading applications…" />
      ) : workflows.status === "error" || opportunities.status === "error" ? (
        <ErrorBlock
          title="Could not load applications"
          message={
            workflows.status === "error"
              ? workflows.error
              : opportunities.status === "error"
                ? opportunities.error
                : ""
          }
          onRetry={() => {
            workflows.retry()
            opportunities.retry()
          }}
        />
      ) : workflows.data.length === 0 ? (
        <EmptyBlock
          title="No chosen roles yet"
          description="Choose a job to start its application work."
        />
      ) : (
        <ol className="flex min-w-0 flex-col gap-2">
          {workflows.data.map((workflow) => {
            const title = titles?.get(workflow.opportunityId)
            return (
              <li
                key={workflow.opportunityId}
                className="rounded-xl border bg-card px-3 py-2.5"
              >
                <a
                  href={continueHref(workflow)}
                  className="text-sm font-medium underline-offset-4 outline-none wrap-break-word hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  {title === undefined || title === ""
                    ? workflow.opportunityId
                    : title}
                </a>
                <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                  {stageStatusText(workflow)}
                </p>
                <a
                  href={`#/applications/${encodeURIComponent(workflow.opportunityId)}`}
                  className="mt-1 inline-block text-xs text-muted-foreground underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Saved materials and handoff
                </a>
              </li>
            )
          })}
        </ol>
      )}
    </div>
  )
}

function ApplicationDetail({ jobId }: { jobId: string }) {
  const opportunity = useRead(`application:${jobId}:job`, (signal) =>
    getOpportunity(jobId, signal)
  )
  const decision = useRead(
    `application:${jobId}:decision`,
    (signal) => getOwnerOpportunityDecision(jobId, signal),
    { scopes: ["selection"] }
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex gap-4">
        <a
          href="#/applications"
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to Applications
        </a>
      </p>

      {opportunity.status === "loading" ? (
        <LoadingBlock label="Loading role…" />
      ) : opportunity.status === "error" ? (
        <ErrorBlock
          title="Could not load this role"
          message={opportunity.error}
          onRetry={opportunity.retry}
        />
      ) : (
        <div>
          <h1 className="font-heading text-2xl font-semibold wrap-break-word">
            {opportunity.data.opportunity.title === ""
              ? "(untitled role)"
              : opportunity.data.opportunity.title}
          </h1>
          <p className="mt-1 text-sm text-muted-foreground">
            Current work, saved materials and manual handoff for this role.
          </p>
        </div>
      )}

      <RoleStageSection jobId={jobId} />

      <ApplicationContinue jobId={jobId} />

      <section aria-labelledby="application-decision-heading">
        <h2
          id="application-decision-heading"
          className="font-heading text-lg font-medium"
        >
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
            <p className="rounded-md border bg-card px-3 py-2 text-sm">
              {decision.data.decision} · recorded{" "}
              <span title={decision.data.createdAt}>
                {formatDate(decision.data.createdAt)}
              </span>
            </p>
          )}
        </div>
      </section>

    </div>
  )
}
