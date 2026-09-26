import {
  getActiveRound,
  getPreferences,
  getRuntimeStatus,
  listOpportunities,
  listResearchRuns,
  listRoleWorkflows,
  pickRestorableRunId,
  type OpportunityView,
  type RoleWorkflowState,
  type RunHistoryItem,
} from "@/api/client"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  useServerRun,
} from "@/components/shared"
import { Button } from "@/components/ui/button"
import { describeRunState } from "@/features/discovery/research-controls"
import { stageStatusText } from "@/pages/role-stages"
import { useRead, type ReadResult } from "@/pages/useRead"

// Saved research restores from the server run history (F2/D3/C1): the
// newest relevant run renders with its saved counts and stable deep link,
// and the recent-runs list below names every lifecycle state — including
// failed runs with their valid next action. No browser pointer is read;
// opening this section starts nothing.
function SavedResearch() {
  const history = useRead(
    "today:run-history",
    (signal) => listResearchRuns({ limit: 5 }, signal),
    { scopes: ["run"] }
  )
  const items = history.status === "ready" ? history.data.items : []
  const restoredId =
    history.status === "ready" ? pickRestorableRunId(items) : null
  const run = useServerRun(restoredId)

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
      <div className="mt-2">
        {history.status === "loading" ? (
          <LoadingBlock label="Loading saved research…" />
        ) : history.status === "error" ? (
          <div className="flex min-w-0 flex-col gap-2">
            <p className="text-sm wrap-break-word text-muted-foreground">
              Saved research is unavailable on this server.
            </p>
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={history.retry}
              >
                Retry saved research
              </Button>
            </div>
          </div>
        ) : items.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No research runs yet. The first Find jobs run appears here.
          </p>
        ) : (
          <div className="flex flex-col gap-3">
            <RestoredRunDetail runId={restoredId} run={run} />
            <RecentRunsList items={items} activeRunId={restoredId} />
          </div>
        )}
      </div>
    </section>
  )
}

function RestoredRunDetail({
  runId,
  run,
}: {
  runId: string | null
  run: ReturnType<typeof useServerRun>
}) {
  const linkClass =
    "font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
  if (runId === null) return null
  if (run.status === "loading")
    return <LoadingBlock label="Loading saved research…" />
  if (run.status === "error") {
    if (run.notFound)
      return (
        <p className="text-sm wrap-break-word text-muted-foreground">
          That research run is no longer on the server. The recent runs
          below still link to what is saved.
        </p>
      )
    return (
      <ErrorBlock
        title="Could not load saved research"
        message={run.error}
        onRetry={run.retry}
      />
    )
  }
  if (run.status === "idle") return null
  const data = run.data
  const failed = data.state === "failed"
  return (
    <div className="flex flex-col gap-2 text-sm">
      <p>{describeRunState(data.state)}</p>
      {failed && data.stopReason !== undefined && data.stopReason !== "" ? (
        <p className="wrap-break-word text-muted-foreground">
          {data.stopReason}
        </p>
      ) : null}
      <p>
        {data.savedIds.length} saved{" "}
        {data.savedIds.length === 1 ? "role" : "roles"}
        {data.unresolvedCount > 0
          ? ` · ${data.unresolvedCount} unresolved`
          : ""}
        .
      </p>
      <p>
        <a
          href={`#/search?run=${encodeURIComponent(runId)}`}
          className={linkClass}
        >
          {failed
            ? "Review this failed run"
            : data.state === "paused"
              ? "Resume this run"
              : "Open this research run"}
        </a>
      </p>
      {failed ? (
        <p className="text-xs wrap-break-word text-muted-foreground">
          A failed run cannot resume; Find more on My search starts a new
          pass without erasing saved work.
        </p>
      ) : null}
    </div>
  )
}

function RecentRunsList({
  items,
  activeRunId,
}: {
  items: RunHistoryItem[]
  activeRunId: string | null
}) {
  return (
    <div className="min-w-0">
      <h3 className="text-sm font-medium">{`Recent runs (${items.length})`}</h3>
      <ul className="mt-2 flex min-w-0 flex-col gap-1">
        {items.map((item) => (
          <li key={item.runId} className="text-sm wrap-break-word">
            {item.runId === activeRunId ? (
              <span className="font-medium">
                {`${item.runId} · ${item.state} · current`}
              </span>
            ) : (
              <a
                href={`#/search?run=${encodeURIComponent(item.runId)}`}
                className="underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
              >
                {`${item.runId} · ${item.state}`}
              </a>
            )}{" "}
            <span title={item.updatedAt} className="text-muted-foreground">
              {item.updatedAt.slice(0, 10)}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

export function TodayPage() {
  const opportunities = useRead(
    "today:opportunities",
    (signal) => listOpportunities("", signal),
    { scopes: ["run"] }
  )
  const preferences = useRead(
    "today:preferences",
    (signal) => getPreferences(signal),
    { scopes: ["goals"] }
  )
  const runtime = useRead("today:runtime", (signal) => getRuntimeStatus(signal))
  const activeRound = useRead(
    "today:active-round",
    (signal) => getActiveRound(signal),
    { scopes: ["run"] }
  )
  const workflows = useRead(
    "today:workflows",
    (signal) => listRoleWorkflows(signal),
    { scopes: ["selection", "workflows"] }
  )
  const runHistory = useRead(
    "today:continue-history",
    (signal) => listResearchRuns({ limit: 5 }, signal),
    { scopes: ["run"] }
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <div>
        <h1 className="font-heading text-2xl font-semibold">Today</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Your saved work and the current state of your search. Opening a
          section reads saved state and starts nothing.
        </p>
      </div>

      <ContinueSection
        activeRound={activeRound}
        opportunities={opportunities}
        preferences={preferences}
        workflows={workflows}
        runHistory={runHistory}
      />

      <SavedResearch />

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
            <div className="flex flex-col gap-2">
              <EmptyBlock
                title="No roles tracked yet"
                description="The server returned an empty opportunity list."
              />
              <p>
                <a
                  href="#/search"
                  className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
                >
                  Find jobs on My search
                </a>
              </p>
            </div>
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

function mostRecent(items: OpportunityView[]): OpportunityView | null {
  let best: OpportunityView | null = null
  for (const item of items) {
    if (item.opportunity.archivedAt !== undefined) continue
    if (best === null || item.opportunity.updatedAt > best.opportunity.updatedAt)
      best = item
  }
  return best
}

/**
 * Continue where the owner left off (B3/F2). Priority: the exact active
 * or paused search, then the newest failed run, then the most recently
 * updated role, then the goals form when no explicit choices exist.
 * Every state links somewhere useful; nothing renders a dead end. The
 * run history is auxiliary: when it cannot load, Continue still works
 * from the other reads and simply omits the failed-run branch.
 */
function ContinueSection({
  activeRound,
  opportunities,
  preferences,
  workflows,
  runHistory,
}: {
  activeRound: ReadResult<Awaited<ReturnType<typeof getActiveRound>>>
  opportunities: ReadResult<Awaited<ReturnType<typeof listOpportunities>>>
  preferences: ReadResult<Awaited<ReturnType<typeof getPreferences>>>
  workflows: ReadResult<Awaited<ReturnType<typeof listRoleWorkflows>>>
  runHistory: ReadResult<Awaited<ReturnType<typeof listResearchRuns>>>
}) {
  const loading =
    activeRound.status === "loading" ||
    opportunities.status === "loading" ||
    preferences.status === "loading" ||
    workflows.status === "loading" ||
    runHistory.status === "loading"
  const failed =
    activeRound.status === "error"
      ? activeRound
      : opportunities.status === "error"
        ? opportunities
        : preferences.status === "error"
          ? preferences
          : workflows.status === "error"
            ? workflows
            : null

  return (
    <section
      aria-labelledby="today-continue-heading"
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <h2
        id="today-continue-heading"
        className="font-heading text-lg font-medium"
      >
        Continue
      </h2>
      <div className="mt-2">
        {loading ? (
          <LoadingBlock label="Loading where you left off…" />
        ) : failed !== null ? (
          <ErrorBlock
            title="Could not load where you left off"
            message={failed.error ?? "The request could not be completed."}
            onRetry={() => {
              activeRound.retry()
              opportunities.retry()
              preferences.retry()
              workflows.retry()
            }}
          />
        ) : (
          <ContinueBody
            round={activeRound.data}
            items={opportunities.data?.items ?? []}
            criteria={preferences.data?.roleCriteria.length ?? 0}
            workflows={workflows.data ?? []}
            runs={runHistory.status === "ready" ? runHistory.data.items : []}
          />
        )}
      </div>
    </section>
  )
}

function ContinueBody({
  round,
  items,
  criteria,
  workflows,
  runs,
}: {
  round: Awaited<ReturnType<typeof getActiveRound>>
  items: OpportunityView[]
  criteria: number
  workflows: Awaited<ReturnType<typeof listRoleWorkflows>>
  runs: RunHistoryItem[]
}) {
  const linkClass =
    "text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
  if (round !== null) {
    const paused = round.state === "paused"
    return (
      <div className="flex flex-col gap-2 text-sm">
        <p>
          {paused
            ? "Your search is paused. Resume it exactly where it stopped."
            : "Your search is running. Follow it live."}
        </p>
        <p>
          <a href="#/search" className={linkClass}>
            {paused ? "Resume search on My search" : "Open the running search"}
          </a>
        </p>
      </div>
    )
  }
  const failedRun = runs.find((run) => run.state === "failed")
  if (failedRun !== undefined) {
    return (
      <div className="flex flex-col gap-2 text-sm">
        <p>
          Your latest search failed. Saved work is kept; review the run
          before starting a new pass.
        </p>
        <p>
          <a
            href={`#/search?run=${encodeURIComponent(failedRun.runId)}`}
            className={linkClass}
          >
            Review this failed run
          </a>
        </p>
      </div>
    )
  }
  const recent = mostRecent(items)
  if (recent !== null) {
    const workflow = workflows.find(
      (entry) => entry.opportunityId === recent.opportunity.id
    )
    const { href, label } = continueTarget(recent.opportunity.id, workflow)
    return (
      <div className="flex flex-col gap-2 text-sm">
        <p>
          Most recent role: {recent.opportunity.title}
          {workflow === undefined
            ? "."
            : ` — ${stageStatusText(workflow)}.`}
        </p>
        <p>
          <a href={href} className={linkClass}>
            {label}
          </a>
        </p>
      </div>
    )
  }
  if (criteria === 0) {
    return (
      <div className="flex flex-col gap-2 text-sm">
        <p>
          No explicit wants or don&apos;t-wants are saved yet. The goals form
          is open and waiting.
        </p>
        <p>
          <a href="#/search" className={linkClass}>
            Set your goals
          </a>
        </p>
      </div>
    )
  }
  return (
    <div className="flex flex-col gap-2 text-sm">
      <p>Your goals are saved and no search is running.</p>
      <p>
        <a href="#/search" className={linkClass}>
          Find jobs on My search
        </a>
      </p>
    </div>
  )
}

// Where Today continues a recent role (F3): unchosen roles open the job,
// prepared and terminally saved roles open the actual per-job Handoff
// route, and everything else continues through its application detail.
function continueTarget(
  opportunityId: string,
  workflow: RoleWorkflowState | undefined
): { href: string; label: string } {
  const id = encodeURIComponent(opportunityId)
  if (workflow === undefined)
    return { href: `#/jobs/${id}`, label: "Open this role" }
  if (workflow.stage === "handoff_saved")
    return { href: `#/jobs/${id}/handoff`, label: "Open the saved handoff" }
  if (workflow.stage === "prepared")
    return {
      href: `#/jobs/${id}/handoff`,
      label: "Open materials and handoff",
    }
  return {
    href: `#/applications/${id}`,
    label: "Continue this application",
  }
}
