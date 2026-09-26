import {
  getOpportunity,
  getOwnerOpportunityDecision,
  listOpportunities,
  listRoleWorkflows,
  listSavedJobs,
  type RoleWorkflowState,
  type SavedJobItem,
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
      return `#/jobs/${id}/prepare`
    case "prepared":
    case "handoff_saved":
      return `#/jobs/${id}/handoff`
    case "selected":
    case "blocked":
      return `#/jobs/${id}`
  }
}

function handoffHref(opportunityId: string): string {
  return `#/jobs/${encodeURIComponent(opportunityId)}/handoff`
}

// One saved-job index line (F3/H01): item type, current version and
// ready/held/outdated state with its reason. Partial and stale sets stay
// listed with honest labels instead of hiding.
function SavedItemLine({ item }: { item: SavedJobItem }) {
  return (
    <li className="text-xs wrap-break-word text-muted-foreground">
      {`${item.type} · version ${item.version} · ${item.state}`}
      {item.required ? "" : " · optional"}
      {item.reason === "" ? "" : ` — ${item.reason}`}
    </li>
  )
}

export function ApplicationsPage({ jobId }: { jobId: string | null }) {
  if (jobId === null) return <ApplicationsList />
  return <ApplicationDetail jobId={jobId} />
}

function ApplicationsList() {
  const workflows = useRead(
    "applications:workflows",
    (signal) => listRoleWorkflows(signal),
    { scopes: ["selection", "workflows"] }
  )
  const opportunities = useRead("applications:list", (signal) =>
    listOpportunities("", signal)
  )
  // The saved-job/material index (F3/M6): item types, current versions
  // and ready/held/outdated status per chosen role. It is auxiliary —
  // when it cannot load, the chosen roles still list honestly below.
  const savedJobs = useRead(
    "applications:saved-jobs",
    (signal) => listSavedJobs(signal),
    { scopes: ["materials"] }
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
  const savedById =
    savedJobs.status === "ready"
      ? new Map(
          savedJobs.data.items.map((entry) => [entry.opportunityId, entry])
        )
      : null

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Applications</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Your chosen roles and their current work. Open a role to continue
          its stage; roles with saved materials link to their per-job
          Handoff, which you fill in manually.
        </p>
      </div>

      {workflows.status === "loading" ||
      opportunities.status === "loading" ||
      savedJobs.status === "loading" ? (
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
        <div className="flex min-w-0 flex-col gap-3">
          {savedJobs.status === "error" ? (
            <p className="text-xs wrap-break-word text-muted-foreground">
              The saved materials index is unavailable right now; the roles
              below list without their item details.{" "}
              <button
                type="button"
                onClick={savedJobs.retry}
                className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                Try again
              </button>
            </p>
          ) : null}
          <ol className="flex min-w-0 flex-col gap-2">
            {workflows.data.map((workflow) => {
              const title = titles?.get(workflow.opportunityId)
              const saved = savedById?.get(workflow.opportunityId)
              const hasItems = (saved?.items.length ?? 0) > 0
              const showHandoff =
                hasItems ||
                workflow.stage === "prepared" ||
                workflow.stage === "handoff_saved"
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
                  {saved !== undefined && saved.items.length > 0 ? (
                    <ul className="mt-1 flex min-w-0 flex-col gap-0.5">
                      {saved.items.map((item) => (
                        <SavedItemLine
                          key={item.type}
                          item={item}
                        />
                      ))}
                    </ul>
                  ) : null}
                  <p className="mt-1 flex flex-wrap gap-x-3 gap-y-1">
                    {showHandoff ? (
                      <a
                        href={handoffHref(workflow.opportunityId)}
                        className="inline-block text-xs text-muted-foreground underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                      >
                        Open handoff
                      </a>
                    ) : null}
                    <a
                      href={`#/applications/${encodeURIComponent(workflow.opportunityId)}`}
                      className="inline-block text-xs text-muted-foreground underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                    >
                      Application details
                    </a>
                  </p>
                </li>
              )
            })}
          </ol>
        </div>
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
            Current work, saved materials and the steps you fill in
            manually for this role. The app never submits anything.
          </p>
        </div>
      )}

      <RoleStageSection jobId={jobId} />

      <ApplicationContinue jobId={jobId} />

      <SavedJobSection jobId={jobId} />

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

// Per-role saved materials from the durable index (F3/H01/R22): every
// saved item with its current version and ready/held/outdated state, a
// link to the full current text on the per-job Handoff route, and an
// explicit fill-manually note. Partial sets stay listed; opening the
// Handoff link never means applied.
function SavedJobSection({ jobId }: { jobId: string }) {
  const savedJobs = useRead(
    `application:${jobId}:saved-jobs`,
    (signal) => listSavedJobs(signal),
    { scopes: ["materials"] }
  )

  return (
    <section aria-labelledby="application-saved-heading">
      <h2
        id="application-saved-heading"
        className="font-heading text-lg font-medium"
      >
        Saved materials
      </h2>
      <div className="mt-3">
        {savedJobs.status === "loading" ? (
          <LoadingBlock label="Loading saved materials…" />
        ) : savedJobs.status === "error" ? (
          <ErrorBlock
            title="Could not load saved materials"
            message={savedJobs.error}
            onRetry={savedJobs.retry}
          />
        ) : (
          <SavedJobBody
            jobId={jobId}
            items={
              savedJobs.data.items.find(
                (entry) => entry.opportunityId === jobId
              )?.items ?? []
            }
          />
        )}
      </div>
    </section>
  )
}

function SavedJobBody({
  jobId,
  items,
}: {
  jobId: string
  items: SavedJobItem[]
}) {
  if (items.length === 0)
    return (
      <EmptyBlock
        title="No saved materials yet"
        description="Preparation has not saved materials for this role yet."
      />
    )
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <ul className="flex min-w-0 flex-col gap-1 rounded-xl border bg-card px-4 py-3">
        {items.map((item) => (
          <SavedItemLine key={item.type} item={item} />
        ))}
      </ul>
      <p>
        <a
          href={handoffHref(jobId)}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Open handoff — you fill in manually
        </a>
      </p>
      <p className="text-xs text-muted-foreground">
        The Handoff page shows the full current text of each item above.
        Opening it never means applied.
      </p>
    </div>
  )
}
