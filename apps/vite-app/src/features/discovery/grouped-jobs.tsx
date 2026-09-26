import { useCallback, useEffect, useMemo, useState } from "react"
import {
  getOpportunityFinding,
  getOwnerOpportunityDecision,
  getResearchCapture,
  getRoleWorkflowOrNull,
  listRoleWorkflows,
  isUnauthenticated,
  listOpportunities,
  listRunFindings,
  RequestError,
  setOwnerOpportunityDecision,
  type FindingEntry,
  type OpportunityView,
  type OwnerDecision,
  type RoleWorkflowState,
} from "@/api/client"
import { useSession } from "@/api/session"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"
import {
  CheckChosenJobs,
  type ChosenRoleInput,
} from "@/features/discovery/check-chosen-jobs"
import {
  type MuseScenario,
  useMuseState,
} from "@/features/discovery/muse-state"
import { discoveryRunStorageKey } from "@/features/discovery/discovery-section"
import { newIdempotencyKey } from "@/features/discovery/research-controls"
import { useOwnerContext } from "@/features/owner-context/useOwnerContext"
import { compactStageLabel } from "@/pages/role-stages"

export const jevGroupOrder: ReadonlyArray<FindingEntry["group"]> = [
  "recommended",
  "could_be_recommended",
  "probably_not_recommended",
  "not_recommended",
  "unknown",
]

export type JobsBucket = FindingEntry["group"] | "unclassified"

export const jobsBucketOrder: ReadonlyArray<JobsBucket> = [
  ...jevGroupOrder,
  "unclassified",
]

export function jevGroupLabel(bucket: JobsBucket): string {
  switch (bucket) {
    case "recommended":
      return "Recommended"
    case "could_be_recommended":
      return "Might recommend"
    case "probably_not_recommended":
      return "Might not recommend"
    case "not_recommended":
      return "Not recommended"
    case "unknown":
      return "Unknown — exceptional, needs a basis"
    case "unclassified":
      return "Not yet classified"
  }
}

type FindingReason = FindingEntry["reasons"][number]

function reasonKindLabel(kind: FindingReason["kind"]): string {
  switch (kind) {
    case "positive":
      return "Strength"
    case "negative":
      return "Concern"
    case "missing_information":
      return "Missing information"
  }
}

function requestMessage(cause: unknown): string {
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

function readRunId(): string | null {
  try {
    const value = window.localStorage.getItem(discoveryRunStorageKey)
    return value !== null && value.trim() !== "" ? value : null
  } catch {
    return null
  }
}

interface TrackedFinding {
  entry: FindingEntry
  fromTrackedRun: boolean
}

interface GroupedJobsData {
  runId: string | null
  runFindingsError: string | null
  findingFailures: number
  decisionFailures: number
  opportunities: OpportunityView[]
  findings: Map<string, TrackedFinding>
  workflows: Map<string, RoleWorkflowState>
  decisions: Map<string, OwnerDecision>
  captureUrls: Map<string, string>
}

export function decisionLabel(
  decision: OwnerDecision["decision"]
): string {
  switch (decision) {
    case "selected":
      return "Selected"
    case "acknowledged":
      return "Shortlisted"
    case "dismissed":
      return "Passed"
  }
}

type LoadState =
  | { kind: "loading" }
  | { kind: "error"; error: string }
  | { kind: "ready"; data: GroupedJobsData }

interface SelectionState {
  busy: boolean
  error: string | null
  saved: OwnerDecision | null
}

interface JobCardProps {
  view: OpportunityView
  tracked: TrackedFinding | null
  runId: string | null
  workflow: RoleWorkflowState | null
  decision: OwnerDecision | null
  captureUrls: Map<string, string>
  expanded: boolean
  onToggleWhy: () => void
  selection: SelectionState | undefined
  onDecide: (decision: OwnerDecision["decision"]) => void
}

function JobCard({
  view,
  tracked,
  runId,
  workflow,
  decision,
  captureUrls,
  expanded,
  onToggleWhy,
  selection,
  onDecide,
}: JobCardProps) {
  const job = view.opportunity
  const archived = job.archivedAt !== undefined
  const href = `#/jobs/${encodeURIComponent(job.id)}`
  const title = job.title === "" ? "(untitled role)" : job.title
  const panelId = `why-${job.id}`
  const finding = tracked?.entry ?? null
  const effective = selection?.saved ?? decision
  const busy = selection?.busy === true
  const locked = workflow !== null && workflow.stage === "sent"

  return (
    <li className="rounded-lg border bg-card px-3 py-2.5">
      <p className="flex min-w-0 flex-wrap items-baseline gap-x-2">
        <a
          href={href}
          className="text-sm font-medium wrap-break-word underline-offset-4 outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          {title}
        </a>
        {workflow === null ? null : (
          <a
            href={href}
            aria-label={`Open ${title} at stage ${compactStageLabel(workflow.stage)}`}
            className="rounded-full border px-2 py-0.5 text-xs wrap-break-word outline-none hover:underline focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            {compactStageLabel(workflow.stage)}
          </a>
        )}
      </p>
      <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
        {`${job.kind} · ${job.workPattern}${job.locationText === "" ? "" : ` · ${job.locationText}`}${archived ? " · archived" : ""}`}
      </p>
      {finding === null || expanded ? null : (
        <div className="mt-1 flex min-w-0 flex-col gap-1">
          {finding.reasons.length === 0 ? null : (
            <p className="text-sm wrap-break-word">
              {`${reasonKindLabel(finding.reasons[0]!.kind)}: ${finding.reasons[0]!.label}`}
            </p>
          )}
          {finding.conflict === undefined ? null : (
            <p className="text-sm wrap-break-word">
              {`Conflicting consideration: ${finding.conflict.label}`}
            </p>
          )}
          {finding.group === "unknown" ? (
            <p className="text-sm wrap-break-word">
              {finding.unknownBasis === undefined
                ? "Jev could not place this role and recorded no basis."
                : `Jev could not place this role: ${finding.unknownBasis}`}
            </p>
          ) : null}
        </div>
      )}

      {finding === null ? (
        <p className="mt-2 text-sm text-muted-foreground">
          No saved Jev classification yet — it appears after the next Find jobs
          pass.
        </p>
      ) : (
        <div className="mt-2 flex min-w-0 flex-col gap-2">
          <p className="text-xs wrap-break-word text-muted-foreground">
            {finding.stale
              ? `Stale — ${finding.staleBasis ?? "outdated"}`
              : "Current"}
          </p>
          {runId !== null && tracked?.fromTrackedRun === false ? (
            <p className="text-xs wrap-break-word text-muted-foreground">
              Latest saved finding from outside the tracked run.
            </p>
          ) : null}
          <p className="text-xs wrap-break-word text-muted-foreground">
            {`Brief profile v${finding.profileVersion} · rubric ${finding.rubricVersion} · catalog ${finding.catalogVersion}`}
          </p>

          {finding.evidenceLinks.length === 0 ? null : (
            <div className="min-w-0">
              <p className="text-xs font-medium">
                {`Evidence (${finding.evidenceLinks.length})`}
              </p>
              <ul className="mt-1 flex min-w-0 flex-col gap-1">
                {finding.evidenceLinks.map((link) => {
                  const url = captureUrls.get(link.captureId)
                  return (
                    <li
                      key={`${link.captureId}:${link.spanStart}-${link.spanEnd}`}
                      className="text-xs wrap-break-word text-muted-foreground"
                    >
                      {url === undefined ? (
                        <code>{link.captureId}</code>
                      ) : (
                        <a
                          href={url}
                          target="_blank"
                          rel="noreferrer"
                          className="underline underline-offset-4"
                        >
                          {link.captureId}
                        </a>
                      )}
                      {` · chars ${link.spanStart}–${link.spanEnd} · excerpt `}
                      <code className="wrap-break-word">
                        {link.excerptSha256}
                      </code>
                    </li>
                  )
                })}
              </ul>
            </div>
          )}

          {finding.sourceRef === undefined ? null : (
            <p className="text-xs wrap-break-word text-muted-foreground">
              {`Source: ${finding.sourceRef.sourceId} (rev ${finding.sourceRef.sourceRevision})`}
              {finding.sourceRef.observedUrl === undefined ? null : (
                <>
                  {" · "}
                  <a
                    href={finding.sourceRef.observedUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="underline underline-offset-4"
                  >
                    Open source
                  </a>
                </>
              )}
            </p>
          )}

          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              aria-expanded={expanded}
              aria-controls={panelId}
              onClick={onToggleWhy}
            >
              {expanded ? "Hide why" : "Why this job"}
            </Button>
          </div>

          {expanded ? (
            <div
              id={panelId}
              className="flex min-w-0 flex-col gap-2 rounded-md border px-3 py-2"
            >
              {finding.group === "unknown" ? (
                <p className="text-sm wrap-break-word">
                  {finding.unknownBasis === undefined
                    ? "Jev could not place this role and recorded no basis."
                    : `Jev could not place this role: ${finding.unknownBasis}`}
                </p>
              ) : null}
              {finding.reasons.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  No saved reasons.
                </p>
              ) : (
                <ul className="flex min-w-0 flex-col gap-2">
                  {finding.reasons.map((reason) => (
                    <li
                      key={reason.reasonId}
                      className="flex min-w-0 flex-col gap-1"
                    >
                      <p className="text-sm font-medium wrap-break-word">
                        {`${reasonKindLabel(reason.kind)}: ${reason.label}`}
                      </p>
                      <p className="text-sm wrap-break-word">{reason.detail}</p>
                      <p className="text-xs wrap-break-word text-muted-foreground">
                        {`Jev support signal: ${reason.jevSupport}`}
                      </p>
                    </li>
                  ))}
                </ul>
              )}
              {finding.conflict === undefined ? null : (
                <p className="text-sm wrap-break-word">
                  {`Conflicting consideration: ${finding.conflict.label} — ${finding.conflict.detail}`}
                </p>
              )}
              {finding.missingFact === undefined ? null : (
                <p className="text-sm wrap-break-word">
                  {`Missing information: ${finding.missingFact.label} — ${finding.missingFact.detail}`}
                </p>
              )}
              <p className="text-xs wrap-break-word text-muted-foreground">
                {`Assessment ${finding.assessmentId} · opportunity rev ${finding.opportunityRevision}`}
              </p>
              <p className="text-xs wrap-break-word text-muted-foreground">
                Support signals record Jev&apos;s assessment strength; they are
                not verified correctness.
              </p>
            </div>
          ) : null}
        </div>
      )}

      <div className="mt-2 flex min-w-0 flex-col gap-1">
        {effective === null ? (
          <p className="text-sm text-muted-foreground">No owner decision yet.</p>
        ) : (
          <p className="text-sm">
            {`${decisionLabel(effective.decision)} — decision rev ${effective.revision}.`}
          </p>
        )}
        {locked ? null : (
          <div className="flex min-w-0 flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              aria-label={`Select ${title}`}
              onClick={() => onDecide("selected")}
            >
              Select
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              aria-label={`Shortlist ${title}`}
              onClick={() => onDecide("acknowledged")}
            >
              Shortlist
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              aria-label={`Pass on ${title}`}
              onClick={() => onDecide("dismissed")}
            >
              Pass
            </Button>
          </div>
        )}
        {selection?.error === undefined || selection.error === null ? null : (
          <p role="alert" className="text-sm wrap-break-word text-destructive">
            {selection.error}
          </p>
        )}
      </div>
    </li>
  )
}

// GroupedJobs renders tracked roles grouped by their SAVED Jev group. No
// run-list endpoint exists, so the latest tracked run id is read from the
// same localStorage key DiscoverySection persists: when present,
// listRunFindings pages that run's latest-per-opportunity findings and roles
// missing from the run fall back to per-role getOpportunityFinding; without
// a tracked run every role resolves through getOpportunityFinding (404 means
// not yet classified). Saved brief/catalog identity comes from the single
// useOwnerContext read for this surface. Loading is GET-only, explanation
// expansion re-reads nothing, stale findings are labeled and retained, and
// selecting a role POSTs only an owner decision — never a check. The sticky
// Check action receives the Contributor readiness from the muse-state
// view-model ("live" in production), so a blocked tier disables checks plainly.
export function GroupedJobs({
  museScenario = "ready",
}: {
  museScenario?: MuseScenario
} = {}) {
  const { session, loseSession } = useSession()
  const owner = useOwnerContext()
  const muse = useMuseState(museScenario)
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<LoadState>({ kind: "loading" })
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  const [selections, setSelections] = useState<Record<string, SelectionState>>(
    {}
  )

  const loadAll = useCallback(
    async (signal: AbortSignal) => {
      setLoad({ kind: "loading" })
      const runId = readRunId()
      try {
        const opportunities: OpportunityView[] = []
        let cursor = ""
        for (let page = 0; page < 20; page += 1) {
          const result = await listOpportunities(cursor, signal)
          opportunities.push(...result.items)
          if (result.nextCursor === undefined || result.nextCursor === "") break
          cursor = result.nextCursor
        }

        const findings = new Map<string, TrackedFinding>()
        let runFindingsError: string | null = null
        if (runId !== null) {
          try {
            let findingCursor = ""
            for (let page = 0; page < 20; page += 1) {
              const pageResult = await listRunFindings(
                runId,
                findingCursor === ""
                  ? { limit: 100 }
                  : { cursor: findingCursor, limit: 100 },
                signal
              )
              for (const entry of pageResult.items) {
                findings.set(entry.opportunityId, {
                  entry,
                  fromTrackedRun: true,
                })
              }
              if (
                pageResult.nextCursor === undefined ||
                pageResult.nextCursor === ""
              )
                break
              findingCursor = pageResult.nextCursor
            }
          } catch (cause) {
            if (signal.aborted) return
            if (isUnauthenticated(cause)) {
              loseSession()
              return
            }
            runFindingsError = requestMessage(cause)
          }
        }

        let sawAuth = false
        let findingFailures = 0
        let decisionFailures = 0
        const missing = opportunities.filter(
          (view) => !findings.has(view.opportunity.id)
        )
        await Promise.all(
          missing.map(async (view) => {
            const id = view.opportunity.id
            try {
              const entry = await getOpportunityFinding(id, signal)
              findings.set(id, { entry, fromTrackedRun: false })
            } catch (cause) {
              if (signal.aborted) return
              if (isUnauthenticated(cause)) {
                sawAuth = true
                return
              }
              if (cause instanceof RequestError && cause.status === 404) return
              findingFailures += 1
            }
          })
        )
        if (signal.aborted) return
        if (sawAuth) {
          loseSession()
          return
        }

        const workflows = new Map<string, RoleWorkflowState>()
        try {
          const roles = await listRoleWorkflows(signal)
          for (const workflow of roles) {
            workflows.set(workflow.opportunityId, workflow)
          }
        } catch (cause) {
          if (signal.aborted) return
          if (isUnauthenticated(cause)) {
            loseSession()
            return
          }
          throw cause
        }

        const decisions = new Map<string, OwnerDecision>()
        await Promise.all(
          opportunities.map(async (view) => {
            const id = view.opportunity.id
            try {
              const decision = await getOwnerOpportunityDecision(id, signal)
              if (decision !== null) decisions.set(id, decision)
            } catch (cause) {
              if (signal.aborted) return
              if (isUnauthenticated(cause)) {
                sawAuth = true
                return
              }
              decisionFailures += 1
            }
          })
        )
        if (signal.aborted) return
        if (sawAuth) {
          loseSession()
          return
        }

        const captureIds = new Set<string>()
        for (const tracked of findings.values()) {
          for (const link of tracked.entry.evidenceLinks) {
            captureIds.add(link.captureId)
          }
        }
        const captureUrls = new Map<string, string>()
        await Promise.all(
          [...captureIds].map(async (captureId) => {
            try {
              const capture = await getResearchCapture(captureId, signal)
              captureUrls.set(
                captureId,
                capture.finalUrl ?? capture.observedUrl
              )
            } catch (cause) {
              if (signal.aborted) return
              if (isUnauthenticated(cause)) {
                sawAuth = true
                return
              }
            }
          })
        )
        if (signal.aborted) return
        if (sawAuth) {
          loseSession()
          return
        }

        setLoad({
          kind: "ready",
          data: {
            runId,
            runFindingsError,
            findingFailures,
            decisionFailures,
            opportunities,
            findings,
            workflows,
            decisions,
            captureUrls,
          },
        })
      } catch (cause) {
        if (signal.aborted) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setLoad({ kind: "error", error: requestMessage(cause) })
      }
    },
    [loseSession]
  )

  const signedIn = session !== undefined && session !== null
  useEffect(() => {
    if (!signedIn) return
    const controller = new AbortController()
    void loadAll(controller.signal)
    return () => controller.abort()
  }, [signedIn, attempt, loadAll])

  const buckets = useMemo(() => {
    if (load.kind !== "ready") return []
    const grouped = new Map<JobsBucket, OpportunityView[]>()
    for (const view of load.data.opportunities) {
      const tracked = load.data.findings.get(view.opportunity.id)
      const key: JobsBucket =
        tracked === undefined ? "unclassified" : tracked.entry.group
      const list = grouped.get(key) ?? []
      list.push(view)
      grouped.set(key, list)
    }
    return jobsBucketOrder.map((bucket) => {
      const items = grouped.get(bucket) ?? []
      let chosen = 0
      for (const view of items) {
        const decision =
          selections[view.opportunity.id]?.saved ??
          load.data.decisions.get(view.opportunity.id)
        if (decision?.decision === "selected") chosen += 1
      }
      return { bucket, items, chosen }
    })
  }, [load, selections])

  const [activeBucket, setActiveBucket] = useState<JobsBucket>("recommended")
  // The four main groups always render as switchable views; Unknown and
  // Not-yet-classified appear only when populated.
  const switchedBuckets = useMemo(
    () =>
      buckets.filter(
        ({ bucket, items }) =>
          (bucket !== "unknown" && bucket !== "unclassified") ||
          items.length > 0
      ),
    [buckets]
  )
  const active =
    switchedBuckets.find(({ bucket }) => bucket === activeBucket) ??
    switchedBuckets[0] ?? { bucket: activeBucket, items: [], chosen: 0 }

  // Chosen roles for the C5 fixed action: opportunities with a saved server
  // workflow (selected decision). Computing this list starts nothing; only an
  // explicit click in CheckChosenJobs posts checks, one per role.
  const chosenRoles = useMemo<ChosenRoleInput[]>(() => {
    if (load.kind !== "ready") return []
    const roles: ChosenRoleInput[] = []
    for (const view of load.data.opportunities) {
      const workflow = load.data.workflows.get(view.opportunity.id)
      if (workflow === undefined) continue
      const title =
        view.opportunity.title === ""
          ? "(untitled role)"
          : view.opportunity.title
      roles.push({
        jobId: view.opportunity.id,
        title,
        opportunityRevision: view.opportunity.revision,
        workflowRevision: workflow.revision,
      })
    }
    return roles
  }, [load])

  if (session === undefined)
    return <LoadingBlock label="Checking dashboard session…" />
  if (session === null)
    return (
      <EmptyBlock
        title="Sign in to review jobs"
        description="Grouped recommendations need an owner session."
      />
    )
  const csrfToken = session.csrfToken

  function toggleWhy(id: string) {
    setOpen((previous) => {
      const next = new Set(previous)
      if (next.has(id)) next.delete(id)
      else next.add(id)
      return next
    })
  }

  async function changeSelection(
    opportunityId: string,
    revision: number,
    expectedDecisionRevision: number,
    decision: OwnerDecision["decision"]
  ) {
    setSelections((previous) => ({
      ...previous,
      [opportunityId]: {
        busy: true,
        error: null,
        saved: previous[opportunityId]?.saved ?? null,
      },
    }))
    try {
      const saved = await setOwnerOpportunityDecision(
        opportunityId,
        {
          requestKey: newIdempotencyKey(),
          expectedOpportunityRevision: revision,
          expectedDecisionRevision,
          decision,
        },
        csrfToken
      )
      setSelections((previous) => ({
        ...previous,
        [opportunityId]: { busy: false, error: null, saved },
      }))
      setLoad((current) => {
        if (current.kind !== "ready") return current
        const decisions = new Map(current.data.decisions)
        decisions.set(opportunityId, saved)
        return { kind: "ready", data: { ...current.data, decisions } }
      })
      try {
        const workflow = await getRoleWorkflowOrNull(opportunityId)
        setLoad((current) => {
          if (current.kind !== "ready") return current
          const workflows = new Map(current.data.workflows)
          if (workflow !== null) workflows.set(opportunityId, workflow)
          else workflows.delete(opportunityId)
          return { kind: "ready", data: { ...current.data, workflows } }
        })
      } catch (cause) {
        if (isUnauthenticated(cause)) loseSession()
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setSelections((previous) => ({
        ...previous,
        [opportunityId]: {
          busy: false,
          error: requestMessage(cause),
          saved: previous[opportunityId]?.saved ?? null,
        },
      }))
    }
  }

  if (load.kind === "loading")
    return <LoadingBlock label="Loading grouped jobs…" />
  if (load.kind === "error")
    return (
      <ErrorBlock
        title="Could not load grouped jobs"
        message={load.error}
        onRetry={() => setAttempt((value) => value + 1)}
      />
    )
  const data = load.data

  return (
    <section
      aria-labelledby="grouped-jobs-heading"
      className="flex min-w-0 flex-col gap-4"
    >
      <div className="min-w-0">
        <h2
          id="grouped-jobs-heading"
          className="font-heading text-lg font-medium"
        >
          Jobs by recommendation
        </h2>
        <p className="mt-1 text-sm text-muted-foreground">
          Roles grouped by their saved Jev classification. Opening an
          explanation rereads nothing: every reason below is saved text.
        </p>
        {data.runId === null ? (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            No tracked research run — showing the latest saved finding per role.
            Start a Find jobs pass to collect new sources.
          </p>
        ) : (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            {`Tracked research run: ${data.runId}`}
          </p>
        )}
        {owner.contextState === "loading" ? (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            Loading saved search context…
          </p>
        ) : owner.context === null ? (
          <div className="mt-1 flex min-w-0 flex-col gap-1">
            <p className="text-xs wrap-break-word text-muted-foreground">
              {`Search brief versions unavailable: ${owner.contextError ?? "The request could not be completed."} Saved findings below are unaffected.`}
            </p>
            <div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={owner.retryContext}
              >
                Retry saved context
              </Button>
            </div>
          </div>
        ) : owner.context.rubricVersion === null ? (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            {`No saved search brief yet — the first Find jobs run authors one from profile v${owner.context.profileVersion}.`}
          </p>
        ) : (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            {`Search brief: profile v${owner.context.profileVersion} · rubric ${owner.context.rubricVersion}${owner.context.catalogVersion === null ? "" : ` · catalog ${owner.context.catalogVersion}`}`}
          </p>
        )}
        {owner.context !== null && owner.context.briefStale ? (
          <p className="mt-1 text-xs wrap-break-word text-muted-foreground">
            {`The saved brief is behind profile v${owner.context.profileVersion}; findings below keep their saved basis until the next Find jobs run.`}
          </p>
        ) : null}
        {data.runFindingsError === null ? null : (
          <p
            role="alert"
            className="mt-1 text-xs wrap-break-word text-destructive"
          >
            {`Could not page the tracked run's findings (${data.runFindingsError}); showing per-role latest findings instead.`}
          </p>
        )}
        {data.findingFailures === 0 ? null : (
          <p
            role="alert"
            className="mt-1 text-xs wrap-break-word text-destructive"
          >
            {`${data.findingFailures} ${data.findingFailures === 1 ? "role" : "roles"} failed to load a saved finding.`}
          </p>
        )}
        {data.decisionFailures === 0 ? null : (
          <p
            role="alert"
            className="mt-1 text-xs wrap-break-word text-destructive"
          >
            {`${data.decisionFailures} ${data.decisionFailures === 1 ? "role" : "roles"} failed to load a saved decision.`}
          </p>
        )}
      </div>

      {data.opportunities.length === 0 ? (
        <EmptyBlock
          title="No jobs tracked yet"
          description="The server returned an empty opportunity list."
        />
      ) : (
        <>
          <div
            role="tablist"
            aria-label="Recommendation groups"
            className="flex min-w-0 flex-wrap gap-2"
          >
            {switchedBuckets.map(({ bucket, items, chosen }) => (
              <Button
                key={bucket}
                type="button"
                role="tab"
                aria-selected={active.bucket === bucket}
                variant={active.bucket === bucket ? "default" : "outline"}
                size="sm"
                onClick={() => setActiveBucket(bucket)}
              >
                {`${jevGroupLabel(bucket)} (${items.length} · ${chosen} chosen)`}
              </Button>
            ))}
          </div>
          <section
            key={active.bucket}
            role="tabpanel"
            aria-label={jevGroupLabel(active.bucket)}
            className="flex min-w-0 flex-col gap-2"
          >
            <h3 className="text-sm font-medium">
              {`${jevGroupLabel(active.bucket)} (${active.items.length})`}
            </h3>
            {active.items.length === 0 ? (
              <p className="text-sm text-muted-foreground">None.</p>
            ) : (
              <ol className="flex min-w-0 flex-col gap-2">
                {active.items.map((view) => (
                  <JobCard
                    key={view.opportunity.id}
                    view={view}
                    tracked={data.findings.get(view.opportunity.id) ?? null}
                    runId={data.runId}
                    workflow={data.workflows.get(view.opportunity.id) ?? null}
                    decision={data.decisions.get(view.opportunity.id) ?? null}
                    captureUrls={data.captureUrls}
                    expanded={open.has(view.opportunity.id)}
                    onToggleWhy={() => toggleWhy(view.opportunity.id)}
                    selection={selections[view.opportunity.id]}
                    onDecide={(decision) =>
                      void changeSelection(
                        view.opportunity.id,
                        view.opportunity.revision,
                        selections[view.opportunity.id]?.saved?.revision ??
                          data.decisions.get(view.opportunity.id)?.revision ??
                          0,
                        decision
                      )
                    }
                  />
                ))}
              </ol>
            )}
          </section>
        </>
      )}

      <CheckChosenJobs roles={chosenRoles} contributor={muse.contributor} />
    </section>
  )
}
