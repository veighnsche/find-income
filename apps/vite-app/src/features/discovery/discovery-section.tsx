import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  commissionResearchRun,
  getResearchCapture,
  getResearchReport,
  isUnauthenticated,
  listResearchActivity,
  resumeRound,
  steerResearchRun,
  stopRound,
  type ResearchActivityEvent,
  type ResearchReportView,
  type ResearchRunView,
  type SteeringMessage,
} from "@/api/client"
import { useSession } from "@/api/session"
import {
  EmptyBlock,
  ErrorBlock,
  LoadingBlock,
  useInvalidate,
  useServerRun,
} from "@/components/shared"
import { requestGoalEditorOpen } from "@/components/shared/goal-editor"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  type MuseScenario,
  useMuseState,
} from "@/features/discovery/muse-state"
import {
  MuseCheckpointsPanel,
  MuseReadinessPanel,
  MuseReportPanel,
} from "@/features/discovery/muse-panels"
import {
  defaultResearchAllowance,
  isActiveRunState,
  newIdempotencyKey,
  ResearchControls,
} from "@/features/discovery/research-controls"
import { RunActivityFeed } from "@/features/discovery/run-activity-feed"
import { RunReportView } from "@/features/discovery/run-report-view"
import {
  listResearchRuns,
  pickRestorableRunId,
} from "@/features/discovery/run-history"
import { SavedBriefPanel } from "@/features/discovery/saved-brief"
import { useOwnerContext } from "@/features/owner-context/useOwnerContext"
import { useRead } from "@/pages/useRead"
import { useRoute } from "@/routes/useRoute"

/**
 * @deprecated Nothing restores from this pointer anymore (G2/D3): the
 * route (`#/search?run=<id>`) plus the server run history are the recovery
 * source. Kept only so the F-owned SearchPage bridge keeps compiling until
 * F removes it; G code never reads or writes this key.
 */
export const discoveryRunStorageKey = "jobseek.research-run-id"
export const discoveryPollIntervalMs = 5000

function requestMessage(cause: unknown): string {
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

function mergeActivityEvents(
  existing: ResearchActivityEvent[],
  incoming: ResearchActivityEvent[]
): ResearchActivityEvent[] {
  const seen = new Set(existing.map((event) => event.eventId))
  const merged = [...existing]
  for (const event of incoming) {
    if (!seen.has(event.eventId)) {
      seen.add(event.eventId)
      merged.push(event)
    }
  }
  return merged.sort((a, b) => a.at.localeCompare(b.at))
}

interface PendingKey {
  key: string
  intent: string
}

// DiscoverySection connects the Find jobs research run: explicit commission,
// stop/resume/steer, journaled activity and the evidence-backed report.
//
// G2/D3: the tracked run resolves from an explicit prop, then the
// `#/search?run=<id>` deep link, then the newest relevant server run —
// never from a browser pointer. `useServerRun` restores it GET-only, a
// commission navigates to the new run link, and a compact recent-runs
// history replaces the old unavailable-history notice. Mount, navigation,
// refresh and polling only read; every mutation needs an explicit owner
// action and carries a stable idempotency key per distinct intent.
//
// Task-first layout (G2/R15): the main action (Find jobs or the run
// controls) follows the contexts above; brief/profile/readiness details
// sit in secondary disclosures.
export function DiscoverySection({
  museScenario = "ready",
  runId: runIdProp = null,
}: {
  museScenario?: MuseScenario
  /** Explicit run to show; wins over the route and server restore. */
  runId?: string | null
} = {}) {
  const { session, loseSession } = useSession()
  const [route, navigate] = useRoute()
  // Live reads on "live" (GETs only); fixtures otherwise.
  const muse = useMuseState(museScenario)
  const contributorReady = muse.contributor.state === "ready"
  // Single saved-context read for this surface: Lane B's hook supplies the
  // effective profile/brief/catalog identity plus the correction-aware Find
  // jobs gate. Mount and navigation only issue GETs.
  const owner = useOwnerContext()
  const invalidate = useInvalidate()
  const routeRunId = route.page === "search" ? route.runId : null
  const history = useRead(
    "discovery:run-history",
    (signal) => listResearchRuns({ limit: 25 }, signal),
    { scopes: ["run"] }
  )

  const linkedRunId = runIdProp ?? routeRunId
  const restoredRunId =
    linkedRunId ??
    (history.status === "ready"
      ? pickRestorableRunId(history.data.items)
      : null)
  const serverRun = useServerRun(restoredRunId)

  // Stale-while-revalidate over the server read: polling refreshes the
  // saved projection without flashing the panel back to loading.
  const [shownRun, setShownRun] = useState<ResearchRunView | null>(null)
  const [events, setEvents] = useState<ResearchActivityEvent[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null)
  const [report, setReport] = useState<ResearchReportView | null>(null)
  const [reportLoading, setReportLoading] = useState(false)
  const [reportError, setReportError] = useState<string | null>(null)
  const [captureUrls, setCaptureUrls] = useState<Record<string, string>>({})
  const [noteText, setNoteText] = useState("")
  const [steerText, setSteerText] = useState("")
  const [steerAck, setSteerAck] = useState<SteeringMessage | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const commissionKeyRef = useRef<PendingKey | null>(null)
  const steerKeyRef = useRef<PendingKey | null>(null)
  const captureInflightRef = useRef<ReadonlySet<string>>(new Set())

  // A new tracked run drops the previous run's activity/report/steering.
  useEffect(() => {
    // eslint-disable-next-line react-hooks/set-state-in-effect -- identity-keyed cache reset, equivalent to a key remount without splitting the run view
    setShownRun(null)
    setEvents([])
    setNextCursor("")
    setLoadMoreError(null)
    setReport(null)
    setReportError(null)
    setSteerAck(null)
    setSteerText("")
    setNoteText("")
    setActionError(null)
  }, [restoredRunId])

  const captureUrlMap = useMemo(
    () => new Map(Object.entries(captureUrls)),
    [captureUrls]
  )

  const serverRunStatus = serverRun.status
  const serverRunData = serverRun.status === "ready" ? serverRun.data : null
  useEffect(() => {
    if (serverRunStatus === "ready" && serverRunData !== null)
      // eslint-disable-next-line react-hooks/set-state-in-effect -- adopts the server snapshot into view state once per ready transition
      setShownRun(serverRunData)
  }, [serverRunStatus, serverRunData])

  const readReport = useCallback(
    async (id: string, signal?: AbortSignal) => {
      setReportLoading(true)
      setReportError(null)
      try {
        const loaded = await getResearchReport(id, signal)
        if (signal?.aborted === true) return
        setReport(loaded)
      } catch (cause) {
        if (signal?.aborted === true) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setReport(null)
        setReportError(requestMessage(cause))
      } finally {
        if (signal?.aborted !== true) setReportLoading(false)
      }
    },
    [loseSession]
  )

  // Read-only activity refresh: merges the head page into the journal.
  // Never commissions work.
  const refreshActivity = useCallback(
    async (id: string, signal?: AbortSignal) => {
      try {
        const page = await listResearchActivity(id, "", 25, signal)
        if (signal?.aborted === true) return
        setEvents((previous) => mergeActivityEvents(previous, page.events))
        setNextCursor(page.nextCursor ?? "")
      } catch (cause) {
        if (signal?.aborted === true) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
      }
    },
    [loseSession]
  )

  const run = shownRun
  const polling = run !== null && isActiveRunState(run.state)

  useEffect(() => {
    if (restoredRunId === null) return
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- read-only fetch with abort cleanup; state settles in the async continuation, never commissions
    void refreshActivity(restoredRunId, controller.signal)
    return () => controller.abort()
  }, [restoredRunId, refreshActivity])

  const serverRetry = serverRun.retry
  useEffect(() => {
    if (restoredRunId === null || !polling) return
    const timer = window.setInterval(() => {
      serverRetry()
      void refreshActivity(restoredRunId)
    }, discoveryPollIntervalMs)
    return () => window.clearInterval(timer)
  }, [restoredRunId, polling, refreshActivity, serverRetry])

  const terminal =
    run !== null && (run.state === "completed" || run.state === "failed")
  useEffect(() => {
    if (restoredRunId === null) return
    if (!terminal) {
      // eslint-disable-next-line react-hooks/set-state-in-effect -- clears the terminal report cache when the run leaves terminal state; the fetch below settles async
      setReport(null)
      setReportError(null)
      return
    }
    const controller = new AbortController()
    void readReport(restoredRunId, controller.signal)
    return () => controller.abort()
  }, [restoredRunId, terminal, readReport])

  // Resolve capture references to their observed URLs through the read-only
  // capture endpoint. Unresolvable captures stay visible as bare ids.
  useEffect(() => {
    const pending = events
      .map((event) => event.refs?.captureId)
      .filter(
        (captureId): captureId is string =>
          captureId !== undefined &&
          captureUrls[captureId] === undefined &&
          !captureInflightRef.current.has(captureId)
      )
    if (pending.length === 0) return
    const unique = [...new Set(pending)]
    captureInflightRef.current = new Set([
      ...captureInflightRef.current,
      ...unique,
    ])
    for (const captureId of unique) {
      void getResearchCapture(captureId)
        .then((capture) => {
          setCaptureUrls((previous) => ({
            ...previous,
            [captureId]: capture.finalUrl ?? capture.observedUrl,
          }))
        })
        .catch((cause: unknown) => {
          if (isUnauthenticated(cause)) loseSession()
        })
    }
  }, [events, captureUrls, loseSession])

  if (session === undefined)
    return <LoadingBlock label="Checking dashboard session…" />
  if (session === null)
    return (
      <EmptyBlock
        title="Sign in to run job discovery"
        description="Research runs need an owner session."
      />
    )
  const csrfToken = session.csrfToken

  function openGoalEditor() {
    // The deterministic editor opens and takes focus; without a mounted
    // editor (saved context failed to load) fall back to the search page.
    if (!requestGoalEditorOpen("discovery:change-goals"))
      navigate({ page: "search", runId: null })
  }

  async function commission(kind: "start" | "find-more") {
    // Find jobs commissions only against the loaded, fresh saved context with
    // a ready Contributor tier, including while a correction is still saving.
    // The button is disabled otherwise; this guard covers programmatic clicks.
    if (kind === "start" && !owner.discoveryReady) return
    if (!contributorReady) return
    setBusy(true)
    setActionError(null)
    try {
      const note = kind === "start" ? noteText.trim() : ""
      const basis =
        owner.context !== null ? `v${owner.context.profileVersion}` : "unpinned"
      const intent = `${kind}:${basis}:${note}`
      const pending = commissionKeyRef.current
      const idempotencyKey =
        pending !== null && pending.intent === intent
          ? pending.key
          : newIdempotencyKey()
      commissionKeyRef.current = { key: idempotencyKey, intent }
      const view = await commissionResearchRun(
        {
          ...(note === "" ? {} : { briefText: note }),
          allowance: defaultResearchAllowance,
          idempotencyKey,
        },
        csrfToken
      )
      commissionKeyRef.current = null
      invalidate("run")
      // The run link is the recovery pointer: a reload, a second browser or
      // a shared link restores this exact run from the server (GET-only).
      navigate({ page: "search", runId: view.runId })
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function control(kind: "stop" | "resume") {
    if (restoredRunId === null) return
    setBusy(true)
    setActionError(null)
    try {
      if (kind === "stop") await stopRound(restoredRunId, csrfToken)
      else await resumeRound(restoredRunId, csrfToken)
      invalidate("run")
      serverRun.retry()
      await refreshActivity(restoredRunId)
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function steer() {
    if (restoredRunId === null || steerText.trim() === "") return
    setBusy(true)
    setActionError(null)
    try {
      const body = steerText
      const pending = steerKeyRef.current
      const idempotencyKey =
        pending !== null && pending.intent === body
          ? pending.key
          : newIdempotencyKey()
      steerKeyRef.current = { key: idempotencyKey, intent: body }
      const ack = await steerResearchRun(
        restoredRunId,
        { body, idempotencyKey },
        csrfToken
      )
      steerKeyRef.current = null
      setSteerAck(ack)
      setSteerText("")
      invalidate("run")
      serverRun.retry()
      await refreshActivity(restoredRunId)
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function loadMore() {
    if (restoredRunId === null || nextCursor === "") return
    setLoadingMore(true)
    setLoadMoreError(null)
    try {
      const page = await listResearchActivity(restoredRunId, nextCursor, 25)
      setEvents((previous) => mergeActivityEvents(previous, page.events))
      setNextCursor(page.nextCursor ?? "")
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setLoadMoreError(requestMessage(cause))
    } finally {
      setLoadingMore(false)
    }
  }

  function backToFindJobs() {
    navigate({ page: "search", runId: null })
  }

  const readiness = {
    ready: owner.discoveryReady,
    reason: owner.discoveryBlockedReason,
  }
  const briefBasis = owner.context
  const runError =
    serverRun.status === "error" ? (serverRun.error ?? "Unknown error.") : null
  const historyItems = history.status === "ready" ? history.data.items : []

  return (
    <section
      aria-labelledby="discovery-heading"
      className="flex min-w-0 flex-col gap-4"
    >
      <div className="min-w-0">
        <h2 id="discovery-heading" className="font-heading text-lg font-medium">
          Find jobs
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Muse Contributor collects listings from sources it chooses, then Jev classifies
          them. Reading this section starts nothing; a run begins only from an
          explicit action below.
        </p>
      </div>

      {readiness.ready && briefBasis !== null ? (
        <p className="text-sm wrap-break-word">
          {briefBasis.rubricVersion === null
            ? `Searches with profile v${briefBasis.profileVersion} — the first run authors the search brief.`
            : `Searches with profile v${briefBasis.profileVersion} (${briefBasis.rubricVersion}).`}
        </p>
      ) : (
        <p className="text-sm wrap-break-word text-muted-foreground">
          {readiness.reason}
        </p>
      )}
      {run !== null &&
      briefBasis !== null &&
      run.briefVersion.profileVersion !== briefBasis.profileVersion ? (
        <p className="text-xs wrap-break-word text-muted-foreground">
          {`This run searched profile v${run.briefVersion.profileVersion}; the saved profile is now v${briefBasis.profileVersion}.`}
        </p>
      ) : null}

      {history.status === "loading" && linkedRunId === null ? (
        <LoadingBlock label="Loading saved runs…" />
      ) : restoredRunId === null ? (
        <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
          <h3 className="font-heading text-base font-medium">Find jobs</h3>
          <p className="text-sm text-muted-foreground">
            Search public job sources under the saved brief above, then return
            assessed roles with source references and important unknowns. The
            agent chooses sources and queries; Jev classifies collected roles.
            This search cannot contact employers. It has a finite allowance: 15
            minutes, 60 actions, 12 Jev assessments, 8 turns, and at most 2
            concurrent operations. Work starts only when you select Find jobs.
          </p>
          {contributorReady ? null : (
            <p
              className="text-sm wrap-break-word text-destructive"
              role="alert"
            >
              {`Find jobs is blocked: Muse Contributor is ${muse.contributor.state} (${muse.contributor.code}) — ${muse.contributor.detail}.`}
            </p>
          )}
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <Button
              type="button"
              disabled={busy || !readiness.ready || !contributorReady}
              onClick={() => void commission("start")}
            >
              {busy ? "Starting…" : "Find jobs"}
            </Button>
            <Button
              type="button"
              variant="outline"
              disabled={busy}
              onClick={openGoalEditor}
            >
              Change goals
            </Button>
          </div>
          <details className="min-w-0">
            <summary className="cursor-pointer text-sm text-muted-foreground">
              Extra note for this run only (optional)
            </summary>
            <div className="mt-2 flex min-w-0 flex-col gap-2">
              <Label htmlFor="discovery-note">
                Extra note for this run only (optional)
              </Label>
              <Textarea
                id="discovery-note"
                rows={3}
                maxLength={20000}
                value={noteText}
                onChange={(event) => setNoteText(event.target.value)}
                placeholder="One-off context for this run — the saved brief stays the search basis."
                disabled={busy}
              />
            </div>
          </details>
          {actionError !== null ? (
            <ErrorBlock title="Research action failed" message={actionError} />
          ) : null}
          <RecentRunsBlock
            status={history.status}
            items={historyItems}
            activeRunId={null}
            onRetry={history.retry}
          />
        </div>
      ) : null}

      {restoredRunId !== null &&
      run === null &&
      serverRun.status === "loading" ? (
        <LoadingBlock label="Loading saved research run…" />
      ) : null}

      {restoredRunId !== null && run === null && runError !== null ? (
        <div className="flex min-w-0 flex-col gap-3">
          <ErrorBlock
            title={
              serverRun.status === "error" && serverRun.notFound
                ? "This run is not on the server"
                : "Could not load the research run"
            }
            message={runError}
            onRetry={serverRun.retry}
          />
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={backToFindJobs}
            >
              Back to Find jobs
            </Button>
          </div>
        </div>
      ) : null}

      {run !== null ? (
        <>
          {runError !== null ? (
            <p
              role="alert"
              className="text-sm wrap-break-word text-destructive"
            >
              Showing last known state: {runError}
            </p>
          ) : null}
          <ResearchControls
            run={run}
            busy={busy}
            actionError={actionError}
            briefText={noteText}
            onBriefTextChange={setNoteText}
            steerText={steerText}
            onSteerTextChange={setSteerText}
            steerAck={steerAck}
            onStart={() => void commission("start")}
            onStop={() => void control("stop")}
            onResume={() => void control("resume")}
            onRefresh={() => {
              serverRun.retry()
              if (restoredRunId !== null) void refreshActivity(restoredRunId)
            }}
            onSteer={() => void steer()}
            onFindMore={() => void commission("find-more")}
            onEditGoals={openGoalEditor}
          />

          <RunActivityFeed
            events={events}
            captureUrls={captureUrlMap}
            nextCursor={nextCursor}
            loadingMore={loadingMore}
            loadMoreError={loadMoreError}
            onLoadMore={() => void loadMore()}
          />

          {report !== null ? <RunReportView report={report} /> : null}
          {report === null && terminal && reportLoading ? (
            <LoadingBlock label="Loading research report…" />
          ) : null}
          {report === null &&
          terminal &&
          !reportLoading &&
          reportError !== null ? (
            <ErrorBlock
              title="Could not load the research report"
              message={reportError}
              onRetry={() => {
                if (restoredRunId !== null) void readReport(restoredRunId)
              }}
            />
          ) : null}
          {report === null && !terminal ? (
            <p className="text-xs text-muted-foreground">
              The report loads when the run completes or fails.
            </p>
          ) : null}
        </>
      ) : null}

      <details className="min-w-0 rounded-2xl border bg-card px-4 py-3">
        <summary className="cursor-pointer text-sm font-medium">
          Saved brief details
        </summary>
        <div className="mt-3">
          <SavedBriefPanel
            context={owner.context}
            contextState={owner.contextState}
            contextError={owner.contextError}
            retryContext={owner.retryContext}
            runBriefProfileVersion={run?.briefVersion.profileVersion ?? null}
          />
        </div>
      </details>

      <details className="min-w-0 rounded-2xl border bg-card px-4 py-3">
        <summary className="cursor-pointer text-sm font-medium">
          Muse Contributor readiness
        </summary>
        <div className="mt-3">
          <MuseReadinessPanel readiness={muse.contributor} />
        </div>
      </details>

      {restoredRunId !== null ? (
        <details className="min-w-0 rounded-2xl border bg-card px-4 py-3">
          <summary className="cursor-pointer text-sm font-medium">
            Recent runs
          </summary>
          <div className="mt-3">
            <RecentRunsBlock
              status={history.status}
              items={historyItems}
              activeRunId={restoredRunId}
              onRetry={history.retry}
            />
          </div>
        </details>
      ) : null}

      <details className="min-w-0 rounded-2xl border bg-card px-4 py-3">
        <summary className="cursor-pointer text-sm font-medium">
          Checkpoints and run report
        </summary>
        <div className="mt-3 flex min-w-0 flex-col gap-3">
          <MuseCheckpointsPanel checkpoints={muse.checkpoints} />
          {muse.report !== null ? (
            <MuseReportPanel report={muse.report} />
          ) : null}
        </div>
      </details>
    </section>
  )
}

// RecentRunsBlock lists the server run history newest-first (D3): every
// entry links to its stable run deep link, so returning to old work never
// needs this browser's storage. A missing history endpoint is neutral
// (older servers predate D3); other failures retry honestly.
function RecentRunsBlock({
  status,
  items,
  activeRunId,
  onRetry,
}: {
  status: "loading" | "ready" | "error"
  items: { runId: string; state: string; updatedAt: string }[]
  activeRunId: string | null
  onRetry: () => void
}) {
  if (status === "loading") return <LoadingBlock label="Loading recent runs…" />
  if (status === "error")
    return (
      <div className="flex min-w-0 flex-col gap-2">
        <p className="text-sm wrap-break-word text-muted-foreground">
          Recent runs are unavailable on this server.
        </p>
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={onRetry}
          >
            Retry recent runs
          </Button>
        </div>
      </div>
    )
  if (items.length === 0)
    return (
      <p className="text-sm text-muted-foreground">
        No research runs yet. The first Find jobs run appears here.
      </p>
    )
  return (
    <div className="min-w-0">
      <h4 className="text-sm font-medium">
        {`Recent runs (${items.length})`}
      </h4>
      <ul className="mt-2 flex min-w-0 flex-col gap-1">
        {items.slice(0, 5).map((item) => (
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
      {items.length > 5 ? (
        <p className="mt-1 text-xs text-muted-foreground">
          {`Showing the 5 newest of ${items.length} runs.`}
        </p>
      ) : null}
    </div>
  )
}
