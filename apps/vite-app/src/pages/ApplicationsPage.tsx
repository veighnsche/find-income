import {
  getOpportunity,
  getOwnerOpportunityDecision,
  listApplicationPacks,
  listOpportunities,
} from "@/api/client"
import { ActivityDisclosure } from "@/components/shared/activity-disclosure"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
} from "@/components/shared"
import { AttemptHistory } from "@/features/attempts"
import { formatDate } from "@/pages/format"
import { useRead } from "@/pages/useRead"

export function ApplicationsPage({ jobId }: { jobId: string | null }) {
  if (jobId === null) return <ApplicationsList />
  return <ApplicationDetail jobId={jobId} />
}

function ApplicationsList() {
  const opportunities = useRead("applications:list", (signal) =>
    listOpportunities("", signal)
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Applications</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Saved application packs, one page per role. Choose a role to see
          its packs.
        </p>
      </div>

      {opportunities.status === "loading" ? (
        <LoadingBlock label="Loading applications…" />
      ) : opportunities.status === "error" ? (
        <ErrorBlock
          title="Could not load applications"
          message={opportunities.error}
          onRetry={opportunities.retry}
        />
      ) : opportunities.data.items.length === 0 ? (
        <EmptyBlock
          title="No roles tracked yet"
          description="The server returned an empty opportunity list."
        />
      ) : (
        <ol className="flex min-w-0 flex-col gap-2">
          {opportunities.data.items.map((view) => {
            const job = view.opportunity
            return (
              <li
                key={job.id}
                className="rounded-xl border bg-card px-3 py-2.5"
              >
                <a
                  href={`#/applications/${encodeURIComponent(job.id)}`}
                  className="text-sm font-medium underline-offset-4 outline-none wrap-break-word hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  {job.title === "" ? "(untitled role)" : job.title}
                </a>
                <p className="mt-1 text-xs text-muted-foreground wrap-break-word">
                  {job.kind}
                  {job.locationText === "" ? "" : ` · ${job.locationText}`}
                  {job.archivedAt === undefined ? "" : " · archived"}
                </p>
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
            Saved application packs for this role.
          </p>
        </div>
      )}

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
