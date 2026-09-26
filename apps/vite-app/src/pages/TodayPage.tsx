import {
  getPreferences,
  getResearchRun,
  getRuntimeStatus,
  listOpportunities,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { discoveryRunStorageKey } from "@/features/discovery/discovery-section"
import { describeRunState } from "@/features/discovery/research-controls"
import { useRead } from "@/pages/useRead"

function savedRunId(): string | null {
  try {
    return window.localStorage.getItem(discoveryRunStorageKey)
  } catch {
    return null
  }
}

function SavedResearch({ runId }: { runId: string }) {
  const run = useRead(`today:research:${runId}`, (signal) =>
    getResearchRun(runId, signal)
  )

  return (
    <section
      aria-labelledby="today-research-heading"
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h2
        id="today-research-heading"
        className="font-heading text-lg font-medium"
      >
        Saved research
      </h2>
      {run.status === "loading" ? (
        <LoadingBlock label="Loading saved research…" />
      ) : run.status === "error" ? (
        <ErrorBlock
          title="Could not load saved research"
          message={run.error}
          onRetry={run.retry}
        />
      ) : (
        <div className="mt-2 flex flex-col gap-2 text-sm">
          <p>{describeRunState(run.data.state)}</p>
          <p>
            {run.data.savedIds.length} saved{" "}
            {run.data.savedIds.length === 1 ? "role" : "roles"}
            {run.data.unresolvedCount > 0
              ? ` · ${run.data.unresolvedCount} unresolved`
              : ""}
            .
          </p>
          <a
            href="#/search"
            className="font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Open this research run
          </a>
        </div>
      )}
    </section>
  )
}

export function TodayPage() {
  const runId = savedRunId()
  const opportunities = useRead("today:opportunities", (signal) =>
    listOpportunities("", signal)
  )
  const preferences = useRead("today:preferences", (signal) =>
    getPreferences(signal)
  )
  const runtime = useRead("today:runtime", (signal) => getRuntimeStatus(signal))

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Today</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Your saved work and the current state of your search. Opening a
          section reads saved state and starts nothing.
        </p>
      </div>

      {runId === null ? null : <SavedResearch runId={runId} />}

      <section
        aria-labelledby="today-roles-heading"
        className="rounded-2xl border bg-card px-4 py-4"
      >
        <h2
          id="today-roles-heading"
          className="font-heading text-lg font-medium"
        >
          Tracked roles
        </h2>
        <div className="mt-3">
          {opportunities.status === "loading" ? (
            <LoadingBlock label="Loading tracked roles…" />
          ) : opportunities.status === "error" ? (
            <ErrorBlock
              title="Could not load roles"
              message={opportunities.error}
              onRetry={opportunities.retry}
            />
          ) : opportunities.data.items.length === 0 ? (
            <EmptyBlock
              title="No roles tracked yet"
              description="The server returned an empty opportunity list."
            />
          ) : (
            <div className="flex flex-col gap-2">
              <p className="text-sm">
                {activeCount(opportunities.data.items)} active of{" "}
                {opportunities.data.items.length} tracked{" "}
                {opportunities.data.items.length === 1 ? "role" : "roles"}.
              </p>
              <p>
                <a
                  href="#/jobs"
                  className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Open Jobs
                </a>
              </p>
            </div>
          )}
        </div>
      </section>

      <section
        aria-labelledby="today-search-heading"
        className="rounded-xl border bg-card px-4 py-4"
      >
        <h2
          id="today-search-heading"
          className="font-heading text-lg font-medium"
        >
          Your search
        </h2>
        <div className="mt-3">
          {preferences.status === "loading" ? (
            <LoadingBlock label="Loading search preferences…" />
          ) : preferences.status === "error" ? (
            <ErrorBlock
              title="Could not load search preferences"
              message={preferences.error}
              onRetry={preferences.retry}
            />
          ) : (
            <div className="flex flex-col gap-2">
              <p className="text-sm">
                Search profile version {preferences.data.version} ·{" "}
                {preferences.data.preferredLocation === ""
                  ? "no preferred location recorded"
                  : preferences.data.preferredLocation}{" "}
                · {preferences.data.roleCriteria.length}{" "}
                {preferences.data.roleCriteria.length === 1
                  ? "criterion"
                  : "criteria"}
                .
              </p>
              <p>
                <a
                  href="#/search"
                  className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Open My search
                </a>
              </p>
            </div>
          )}
        </div>
      </section>

      <section
        aria-labelledby="today-status-heading"
        className="rounded-lg border bg-card px-4 py-4"
      >
        <h2
          id="today-status-heading"
          className="font-heading text-lg font-medium"
        >
          Service status
        </h2>
        <div className="mt-3">
          {runtime.status === "loading" ? (
            <LoadingBlock label="Loading service status…" />
          ) : runtime.status === "error" ? (
            <ErrorBlock
              title="Could not load service status"
              message={runtime.error}
              onRetry={runtime.retry}
            />
          ) : (
            <dl className="flex flex-col gap-1 text-sm">
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">Collection:</dt>
                <dd>
                  {runtime.data.ingestionAvailable
                    ? "Available"
                    : "Not available"}
                </dd>
              </div>
              <div className="flex flex-wrap gap-x-2">
                <dt className="text-muted-foreground">Organisation:</dt>
                <dd>
                  {runtime.data.organisationAvailable
                    ? "Available"
                    : "Not available"}
                </dd>
              </div>
            </dl>
          )}
          <p className="mt-2 text-xs text-muted-foreground">
            Only the two flags the server reports are shown; nothing else is
            inferred.
          </p>
        </div>
      </section>
    </div>
  )
}

function activeCount(
  items: { opportunity: { archivedAt?: string } }[]
): number {
  return items.filter((item) => item.opportunity.archivedAt === undefined)
    .length
}
