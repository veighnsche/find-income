import {
  getOpportunity,
  getOwnerOpportunityDecision,
  listApplicationPacks,
  listOpportunities,
  listRoleWorkflows,
  type RoleWorkflowState,
} from "@/api/client"
import { ActivityDisclosure } from "@/components/shared/activity-disclosure"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
} from "@/components/shared"
import { AttemptHistory } from "@/features/attempts"
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
    case "reviewing":
      return `#/applications/${id}/review`
    case "sent":
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
  const workflows = useRead("applications:workflows", (signal) =>
    listRoleWorkflows(signal)
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
                  Saved packs and send history
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
  const packs = useRead(`application:${jobId}:packs`, (signal) =>
    listApplicationPacks(jobId, signal)
  )
  const decision = useRead(`application:${jobId}:decision`, (signal) =>
    getOwnerOpportunityDecision(jobId, signal)
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
        <a
          href={`#/applications/${encodeURIComponent(jobId)}/review`}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Review application
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
            Current work, saved packs and send history for this role.
          </p>
        </div>
      )}

      <RoleStageSection jobId={jobId} />

      <ApplicationContinue jobId={jobId} />

      <section aria-labelledby="application-packs-heading">
        <h2
          id="application-packs-heading"
          className="font-heading text-lg font-medium"
        >
          Saved packs
        </h2>
        <div className="mt-3">
          {packs.status === "loading" ? (
            <LoadingBlock label="Loading application packs…" />
          ) : packs.status === "error" ? (
            <ErrorBlock
              title="Could not load application packs"
              message={packs.error}
              onRetry={packs.retry}
            />
          ) : packs.data.length === 0 ? (
            <EmptyBlock
              title="No application packs saved for this role"
              description="Packs appear here after preparation saves them."
            />
          ) : (
            <ol className="flex min-w-0 flex-col gap-2">
              {packs.data.map((pack) => (
                <li
                  key={pack.id}
                  className="rounded-md border bg-card px-3 py-2 text-sm"
                >
                  <p className="font-medium">Version {pack.version}</p>
                  <p
                    className="mt-0.5 text-xs text-muted-foreground"
                    title={pack.createdAt}
                  >
                    Saved {formatDate(pack.createdAt)} · profile revision{" "}
                    {pack.profileRevision} · role revision{" "}
                    {pack.opportunityRevision}
                  </p>
                  <p
                    className="mt-0.5 truncate font-mono text-xs text-muted-foreground"
                    title={pack.contentSha256}
                  >
                    {pack.contentSha256}
                  </p>
                </li>
              ))}
            </ol>
          )}
        </div>
      </section>

      <section aria-labelledby="application-attempts-heading">
        <h2
          id="application-attempts-heading"
          className="font-heading text-lg font-medium"
        >
          Attempted submissions
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          The exact attempted material and actual service result for each
          send, unchanged by newer material versions.
        </p>
        <div className="mt-3">
          <AttemptHistory jobId={jobId} />
        </div>
      </section>

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

      {packs.status === "ready" ? (
        <section aria-labelledby="application-activity-heading">
          <h2
            id="application-activity-heading"
            className="font-heading text-lg font-medium"
          >
            Pack history
          </h2>
          <div className="mt-3">
            <ActivityDisclosure
              actor="owner"
              phase="Application packs"
              status={
                packs.data.length === 0
                  ? "No packs saved"
                  : `${packs.data.length} ${packs.data.length === 1 ? "pack" : "packs"} saved`
              }
              entries={packs.data.map((pack) => ({
                id: pack.id,
                kind: "result",
                text: `Version ${pack.version} saved ${formatDate(pack.createdAt)}`,
              }))}
            />
          </div>
        </section>
      ) : null}
    </div>
  )
}
