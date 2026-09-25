import { useCallback, useEffect, useMemo, useRef, useState } from "react"
import {
  commissionResearchRun,
  getResearchCapture,
  getResearchReport,
  getResearchRun,
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
  UnsupportedBlock,
} from "@/components/shared"
import { Button } from "@/components/ui/button"
import { Label } from "@/components/ui/label"
import { Textarea } from "@/components/ui/textarea"
import {
  defaultResearchAllowance,
  isActiveRunState,
  newIdempotencyKey,
  ResearchControls,
} from "@/features/discovery/research-controls"
import { RunActivityFeed } from "@/features/discovery/run-activity-feed"
import { RunReportView } from "@/features/discovery/run-report-view"
import {
  savedBriefReadiness,
  SavedBriefPanel,
  useSavedBrief,
} from "@/features/discovery/saved-brief"

export const discoveryRunStorageKey = "jobseek.research-run-id"
export const discoveryPollIntervalMs = 5000

type RunReadState =
  | { kind: "idle" }
  | { kind: "loading" }
  | { kind: "ready"; run: ResearchRunView }
  | { kind: "stale"; run: ResearchRunView; error: string }
  | { kind: "error"; error: string }

function requestMessage(cause: unknown): string {
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

function loadPersistedRunId(storage: Pick<Storage, "getItem">): string | null {
  try {
    const value = storage.getItem(discoveryRunStorageKey)
    return value !== null && value.trim() !== "" ? value : null
  } catch {
    return null
  }
}

function persistRunId(
  storage: Pick<Storage, "setItem" | "removeItem">,
  runId: string | null
): void {
  try {
    if (runId !== null) storage.setItem(discoveryRunStorageKey, runId)
    else storage.removeItem(discoveryRunStorageKey)
  } catch {
    // Private-mode storage failures must not break the discovery view.
  }
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
// stop/resume/steer, journaled activity and the evidence-backed report. Mount
// and navigation only read; every mutation needs an explicit owner action and
// carries a stable idempotency key per distinct intent.
export function DiscoverySection() {
  const { session, loseSession } = useSession()
  const [runId, setRunId] = useState<string | null>(() =>
    loadPersistedRunId(window.localStorage)
  )
  const [read, setRead] = useState<RunReadState>(() =>
    loadPersistedRunId(window.localStorage) === null
      ? { kind: "idle" }
      : { kind: "loading" }
  )
  const [events, setEvents] = useState<ResearchActivityEvent[]>([])
  const [nextCursor, setNextCursor] = useState("")
  const [loadingMore, setLoadingMore] = useState(false)
  const [loadMoreError, setLoadMoreError] = useState<string | null>(null)
  const [report, setReport] = useState<ResearchReportView | null>(null)
  const [reportLoading, setReportLoading] = useState(false)
  const [reportError, setReportError] = useState<string | null>(null)
  const [captureUrls, setCaptureUrls] = useState<Record<string, string>>({})
  const [noteText, setNoteText] = useState("")
  const savedBrief = useSavedBrief()
  const [steerText, setSteerText] = useState("")
  const [steerAck, setSteerAck] = useState<SteeringMessage | null>(null)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<string | null>(null)
  const commissionKeyRef = useRef<PendingKey | null>(null)
  const steerKeyRef = useRef<PendingKey | null>(null)
  const captureInflightRef = useRef<ReadonlySet<string>>(new Set())

  const captureUrlMap = useMemo(
    () => new Map(Object.entries(captureUrls)),
    [captureUrls]
  )

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

  // Read-only refresh: refetches the persisted projection, the first activity
  // page and (for terminal runs) the report. Never commissions work.
  const refresh = useCallback(
    async (id: string, signal?: AbortSignal) => {
      try {
        const [view, page] = await Promise.all([
          getResearchRun(id, signal),
          listResearchActivity(id, "", 25, signal),
        ])
        if (signal?.aborted === true) return
        setRead({ kind: "ready", run: view })
        setEvents((previous) => mergeActivityEvents(previous, page.events))
        setNextCursor(page.nextCursor ?? "")
        if (view.state === "completed" || view.state === "failed") {
          await readReport(id, signal)
        } else {
          setReport(null)
          setReportError(null)
        }
      } catch (cause) {
        if (signal?.aborted === true) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        const error = requestMessage(cause)
        setRead((current) =>
          current.kind === "ready" || current.kind === "stale"
            ? { kind: "stale", run: current.run, error }
            : { kind: "error", error }
        )
      }
    },
    [loseSession, readReport]
  )

  useEffect(() => {
    if (runId === null) return
    const controller = new AbortController()
    // eslint-disable-next-line react-hooks/set-state-in-effect -- read-only run restore on mount/run change
    void refresh(runId, controller.signal)
    return () => controller.abort()
  }, [runId, refresh])

  const run =
    read.kind === "ready" || read.kind === "stale" ? read.run : null
  const polling = run !== null && isActiveRunState(run.state)

  useEffect(() => {
    if (runId === null || !polling) return
    const timer = window.setInterval(() => {
      void refresh(runId)
    }, discoveryPollIntervalMs)
    return () => window.clearInterval(timer)
  }, [runId, polling, refresh])

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

  async function commission(kind: "start" | "find-more") {
    // Find jobs commissions only against the loaded, fresh saved brief. The
    // button is disabled otherwise; this guard covers programmatic clicks.
    if (kind === "start" && !savedBriefReadiness(savedBrief).ready) return
    setBusy(true)
    setActionError(null)
    try {
      const note = kind === "start" ? noteText.trim() : ""
      const basis =
        savedBrief.status === "ready" && savedBrief.data.brief !== null
          ? `v${savedBrief.data.brief.profileVersion}`
          : "unpinned"
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
      persistRunId(window.localStorage, view.runId)
      setEvents([])
      setNextCursor("")
      setLoadMoreError(null)
      setReport(null)
      setReportError(null)
      setSteerAck(null)
      setRunId(view.runId)
      setRead({ kind: "ready", run: view })
      if (view.state === "completed" || view.state === "failed") {
        await readReport(view.runId)
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function control(kind: "stop" | "resume") {
    if (runId === null) return
    setBusy(true)
    setActionError(null)
    try {
      if (kind === "stop") await stopRound(runId, csrfToken)
      else await resumeRound(runId, csrfToken)
      await refresh(runId)
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function steer() {
    if (runId === null || steerText.trim() === "") return
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
        runId,
        { body, idempotencyKey },
        csrfToken
      )
      steerKeyRef.current = null
      setSteerAck(ack)
      setSteerText("")
      await refresh(runId)
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setActionError(requestMessage(cause))
    } finally {
      setBusy(false)
    }
  }

  async function loadMore() {
    if (runId === null || nextCursor === "") return
    setLoadingMore(true)
    setLoadMoreError(null)
    try {
      const page = await listResearchActivity(runId, nextCursor, 25)
      setEvents((previous) => mergeActivityEvents(previous, page.events))
      setNextCursor(page.nextCursor ?? "")
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession()
      else setLoadMoreError(requestMessage(cause))
    } finally {
      setLoadingMore(false)
    }
  }

  function forgetRun() {
    persistRunId(window.localStorage, null)
    setRunId(null)
    setRead({ kind: "idle" })
    setEvents([])
    setNextCursor("")
    setReport(null)
    setReportError(null)
    setSteerAck(null)
    setActionError(null)
  }

  const terminal =
    run !== null && (run.state === "completed" || run.state === "failed")
  const readiness = savedBriefReadiness(savedBrief)
  const briefBasis =
    savedBrief.status === "ready" ? savedBrief.data.brief : null

  return (
    <section
      aria-labelledby="discovery-heading"
      className="flex min-w-0 flex-col gap-4"
    >
      <div className="min-w-0">
        <h2
          id="discovery-heading"
          className="font-heading text-lg font-medium"
        >
          Find jobs
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Codex collects listings from sources it chooses, then Jev classifies
          them. Reading this section starts nothing; a run begins only from an
          explicit action below.
        </p>
      </div>

      <SavedBriefPanel
        read={savedBrief}
        runBriefProfileVersion={run?.briefVersion.profileVersion ?? null}
      />

      {read.kind === "idle" ? (
        <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
          <h3 className="font-heading text-base font-medium">Find jobs</h3>
          <p className="text-sm text-muted-foreground">
            Codex chooses sources and queries from the saved search brief
            above, then Jev classifies what was collected. The allowance is
            finite: 15 minutes, 60 actions, 12 Jev assessments, 8 turns, at
            most 2 concurrent operations. Nothing starts until the button below
            is pressed.
          </p>
          {readiness.ready && briefBasis !== null ? (
            <p className="text-sm wrap-break-word">
              Searches with profile v{briefBasis.profileVersion} (
              {briefBasis.rubricVersion}).
            </p>
          ) : (
            <p className="text-sm text-muted-foreground wrap-break-word">
              {readiness.reason}
            </p>
          )}
          <div>
            <Button
              type="button"
              disabled={busy || !readiness.ready}
              onClick={() => void commission("start")}
            >
              {busy ? "Starting…" : "Find jobs"}
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
            <ErrorBlock
              title="Research action failed"
              message={actionError}
            />
          ) : null}
        </div>
      ) : null}

      {read.kind === "loading" ? (
        <LoadingBlock label="Loading saved research run…" />
      ) : null}

      {read.kind === "error" ? (
        <div className="flex min-w-0 flex-col gap-3">
          <ErrorBlock
            title="Could not load the research run"
            message={read.error}
            onRetry={() => {
              if (runId !== null) void refresh(runId)
            }}
          />
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={forgetRun}
            >
              Start a new run
            </Button>
          </div>
        </div>
      ) : null}

      {run !== null ? (
        <>
          {read.kind === "stale" ? (
            <p role="alert" className="text-sm wrap-break-word text-destructive">
              Showing last known state: {read.error}
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
              if (runId !== null) void refresh(runId)
            }}
            onSteer={() => void steer()}
            onFindMore={() => void commission("find-more")}
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
          {report === null && terminal && !reportLoading && reportError !== null ? (
            <ErrorBlock
              title="Could not load the research report"
              message={reportError}
              onRetry={() => {
                if (runId !== null) void readReport(runId)
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

      <UnsupportedBlock
        title="Run history and brief rewrite are not available yet"
        message="The API keeps one run per id and exposes no run list, so only the latest pass is kept here. Changing the search in plain language is not connected either: steering messages go to the current run only and never rewrite the saved brief."
      />
    </section>
  )
}
