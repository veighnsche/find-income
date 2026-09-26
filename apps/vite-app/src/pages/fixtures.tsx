import { vi } from "vitest"
import type {
  MuseReadiness,
  OpportunityView,
  OwnerDecision,
  Preferences,
  ResearchRunView,
  RoleWorkflowState,
  Round,
  RunHistoryItem,
  RuntimeStatus,
  SavedAnswerList,
  SavedJobEntry,
  Session,
  SourcedOwnerContext,
} from "@/api/client"

export const sessionFixture: Session = {
  actorKind: "administrator",
  actorId: "owner",
  csrfToken: "csrf-token",
  expiresAt: "2099-01-01T00:00:00Z",
}

export const preferencesFixture: Preferences = {
  version: 3,
  preferredLocation: "Berlin",
  allowRemote: true,
  allowHybrid: false,
  targetHours: "40",
  minMonthlyBaseCents: 450000,
  salaryCurrency: "EUR",
  timezone: "Europe/Berlin",
  roleCriteria: [
    {
      id: "criterion-1",
      label: "Backend engineer",
      description: "Server-side product work in TypeScript.",
      kind: "role",
      mode: "require",
      definitionHash: "hash-backend",
    },
    {
      id: "criterion-2",
      label: "On-call rotation",
      description: "No overnight on-call.",
      kind: "responsibility",
      mode: "avoid",
      definitionHash: "hash-oncall",
    },
  ],
}

function baseJob(id: string): OpportunityView["opportunity"] {
  return {
    id,
    companyId: "company-1",
    title: `Role ${id}`,
    kind: "employment",
    sourceUrl: `https://example.com/jobs/${id}`,
    originalText: `Original posting text for ${id}.`,
    notes: "",
    stage: "new",
    workPattern: "remote",
    locationText: "Berlin",
    postedOn: "2026-09-01",
    deadlineOn: "",
    revision: 2,
    createdAt: "2026-09-10T09:00:00Z",
    updatedAt: "2026-09-12T09:00:00Z",
    compensation: {},
  }
}

export const jobOneFixture: OpportunityView = {
  opportunity: {
    ...baseJob("job-1"),
    title: "Backend Engineer",
    notes: "Owner note: strong match.",
    compensation: {
      currency: "EUR",
      minAmountCents: 400000,
      maxAmountCents: 600000,
      period: "month",
      basis: "base",
    },
  },
  likelyDuplicates: [
    {
      opportunity: {
        ...baseJob("job-1-dup"),
        title: "Backend Engineer (mirror)",
      },
      reason: "same_source_url",
    },
  ],
}

export const jobTwoFixture: OpportunityView = {
  opportunity: {
    ...baseJob("job-2"),
    title: "Weekend Project",
    kind: "project",
    workPattern: "onsite",
    locationText: "",
    stage: "",
    sourceUrl: "",
    archivedAt: "2026-09-15T09:00:00Z",
  },
  likelyDuplicates: [],
}

export const decisionFixture: OwnerDecision = {
  id: "decision-1",
  opportunityId: "job-1",
  decision: "selected",
  revision: 1,
  opportunityRevision: 2,
  auditId: "audit-1",
  createdAt: "2026-09-18T10:00:00Z",
}

export const runtimeFixture: RuntimeStatus = {
  ingestionAvailable: true,
  organisationAvailable: false,
}

export function roleWorkflowFixture(
  opportunityId: string,
  stage: RoleWorkflowState["stage"],
  overrides: Partial<RoleWorkflowState> = {}
): RoleWorkflowState {
  return {
    opportunityId,
    stage,
    revision: stage === "selected" ? 0 : 1,
    updatedAt: "2026-09-18T10:00:00Z",
    decisionAt: "2026-09-18T10:00:00Z",
    opportunityRevision: 2,
    ...overrides,
  }
}

export const selectedWorkflowFixture: RoleWorkflowState = roleWorkflowFixture(
  "job-1",
  "selected"
)

export interface FetchStubOptions {
  session?: Session | null
  preferences?: Preferences
  opportunities?: OpportunityView[]
  opportunityById?: Record<string, OpportunityView | null>
  decisionByOpportunity?: Record<string, OwnerDecision | null>
  workflowsByOpportunity?: Record<string, RoleWorkflowState | null>
  runtime?: RuntimeStatus
  activeRound?: Round | null
  savedAnswers?: SavedAnswerList
  preferencesConflict?: boolean
  /** Newest-first research run history; absent keeps the 404 default. */
  runHistory?: RunHistoryItem[]
  researchRunById?: Record<string, ResearchRunView | null>
  sourcedContext?: SourcedOwnerContext
  /** Saved-job index; defaults to an empty index (no existing consumer). */
  savedJobs?: SavedJobEntry[]
  museReadinessByTier?: Partial<Record<"contributor" | "standard", MuseReadiness>>
}

export function runHistoryFixture(
  runId: string,
  state: string,
  updatedAt = "2026-09-26T10:00:00Z"
): RunHistoryItem {
  return {
    runId,
    requestKey: `key-${runId}`,
    intent: "find_jobs",
    outcome: "research_run",
    state,
    stopReason: "",
    createdAt: "2026-09-26T09:00:00Z",
    updatedAt,
  }
}

export function researchRunFixture(
  runId: string,
  state: ResearchRunView["state"],
  overrides: Partial<ResearchRunView> = {}
): ResearchRunView {
  return {
    runId,
    state,
    briefVersion: { profileVersion: 3, rubricVersion: "criteria-v3-abc123" },
    allowance: { maxInvestigations: 20 },
    usage: { investigations: 1 },
    investigations: [],
    savedIds: [],
    unresolvedCount: 0,
    ...overrides,
  } as ResearchRunView
}

export function sourcedContextFixture(
  overrides: Partial<SourcedOwnerContext> = {}
): SourcedOwnerContext {
  return {
    owner: { kind: "administrator", id: "owner" },
    sourcesConnected: true,
    sources: [],
    ...overrides,
  }
}

export function savedJobFixture(
  opportunityId: string,
  overrides: Partial<SavedJobEntry> = {}
): SavedJobEntry {
  return {
    opportunityId,
    title: "Backend Engineer",
    companyName: "Acme",
    checkStatus: "complete",
    items: [],
    ...overrides,
  }
}

export function museReadinessFixture(
  tier: MuseReadiness["tier"],
  state: MuseReadiness["state"] = "ready",
  detail = ""
): MuseReadiness {
  return {
    state,
    code: state === "ready" ? "muse_ready" : "muse_unavailable",
    detail: detail === "" && state === "ready" ? "Muse is ready." : detail,
    tier,
  }
}

export interface FetchCall {
  url: string
  method: string
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

export function stubFetch(options: FetchStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let currentPreferences = options.preferences ?? preferencesFixture
  const decisions = new Map<string, OwnerDecision>()
  if (options.decisionByOpportunity === undefined) {
    if (options.opportunityById?.["job-1"] !== null)
      decisions.set("job-1", decisionFixture)
  } else {
    for (const [id, entry] of Object.entries(options.decisionByOpportunity)) {
      if (entry !== null) decisions.set(id, entry)
    }
  }
  const opportunities = options.opportunities ?? [jobOneFixture, jobTwoFixture]
  const byId = new Map<string, OpportunityView>(
    opportunities.map((view) => [view.opportunity.id, view])
  )
  const workflows = new Map<string, RoleWorkflowState>()
  if (options.workflowsByOpportunity === undefined) {
    if (options.opportunityById?.["job-1"] !== null)
      workflows.set("job-1", selectedWorkflowFixture)
  } else {
    for (const [id, entry] of Object.entries(
      options.workflowsByOpportunity
    )) {
      if (entry !== null) workflows.set(id, entry)
    }
  }

  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({ url, method })
      const path = new URL(url, "http://localhost").pathname

      if (path === "/api/v1/auth/session")
        return options.session === null
          ? jsonResponse(401, {})
          : jsonResponse(200, options.session ?? sessionFixture)
      if (path === "/api/v1/auth/login" && method === "POST")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/preferences" && method === "PUT") {
        if (options.preferencesConflict === true)
          return jsonResponse(409, {
            error: { code: "conflict", message: "Profile version moved." },
          })
        const body = JSON.parse((init?.body as string | null) ?? "{}") as Record<
          string,
          unknown
        >
        currentPreferences = {
          ...currentPreferences,
          ...body,
          version: currentPreferences.version + 1,
        }
        return jsonResponse(200, {
          preferences: currentPreferences,
          changeId: "change-stub",
        })
      }
      if (path === "/api/v1/preferences")
        return jsonResponse(200, currentPreferences)
      if (path === "/api/v1/answers")
        return jsonResponse(200, options.savedAnswers ?? { items: [] })
      if (path === "/api/v1/opportunities")
        return jsonResponse(200, { items: opportunities })
      if (path === "/api/v1/runtime-status")
        return jsonResponse(200, options.runtime ?? runtimeFixture)
      if (path === "/api/v1/workflow/roles")
        return jsonResponse(200, { items: [...workflows.values()] })
      if (path === "/api/v1/rounds/active")
        return options.activeRound === undefined ||
          options.activeRound === null
          ? jsonResponse(404, { error: { message: "No active round." } })
          : jsonResponse(200, options.activeRound)
      if (path === "/api/v1/research/runs" && method === "GET")
        return options.runHistory === undefined
          ? jsonResponse(404, { error: { message: "No run history." } })
          : jsonResponse(200, { items: options.runHistory })
      if (path === "/api/v1/research/owner-context")
        return options.sourcedContext === undefined
          ? jsonResponse(404, { error: { message: "No such route." } })
          : jsonResponse(200, options.sourcedContext)
      if (path === "/api/v1/saved-jobs")
        return jsonResponse(200, { items: options.savedJobs ?? [] })
      if (path === "/api/v1/muse/readiness") {
        const tier = new URL(url, "http://localhost").searchParams.get("tier")
        const readiness =
          tier === "contributor" || tier === "standard"
            ? options.museReadinessByTier?.[tier]
            : undefined
        return readiness === undefined
          ? jsonResponse(404, { error: { message: "No readiness." } })
          : jsonResponse(200, readiness)
      }

      const researchRunMatch = path.match(
        /^\/api\/v1\/research\/runs\/([^/]+)$/
      )
      if (researchRunMatch?.[1] !== undefined) {
        const run = options.researchRunById?.[
          decodeURIComponent(researchRunMatch[1])
        ]
        return run === undefined || run === null
          ? jsonResponse(404, { error: { message: "Research run not found." } })
          : jsonResponse(200, run)
      }

      const match = path.match(/^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/)
      if (match?.[1] !== undefined) {
        const id = decodeURIComponent(match[1])
        const suffix = match[2] ?? ""
        const override = options.opportunityById?.[id]
        const view = override === null ? undefined : (override ?? byId.get(id))
        if (view === undefined)
          return jsonResponse(404, {
            error: { message: "Opportunity not found." },
          })
        if (suffix === "") return jsonResponse(200, view)
        if (suffix === "/decision" && method === "POST") {
          const body = JSON.parse((init?.body as string | null) ?? "{}") as {
            decision?: OwnerDecision["decision"]
            expectedDecisionRevision?: number
          }
          const previous = decisions.get(id) ?? null
          if ((previous?.revision ?? 0) !== (body.expectedDecisionRevision ?? 0))
            return jsonResponse(409, {
              error: { code: "conflict", message: "Decision moved." },
            })
          const saved: OwnerDecision = {
            id: `decision-${id}`,
            opportunityId: id,
            decision: body.decision ?? "selected",
            revision: (previous?.revision ?? 0) + 1,
            opportunityRevision: view.opportunity.revision,
            auditId: "audit-stub",
            createdAt: "2026-09-26T10:00:00Z",
          }
          decisions.set(id, saved)
          if (saved.decision === "selected")
            workflows.set(
              id,
              options.workflowsByOpportunity?.[id] ??
                roleWorkflowFixture(id, "selected")
            )
          else workflows.delete(id)
          return jsonResponse(previous === null ? 201 : 200, saved)
        }
        if (suffix === "/decision") {
          const decision = decisions.get(id) ?? null
          return decision === null
            ? jsonResponse(404, { error: { message: "Decision not found." } })
            : jsonResponse(200, decision)
        }
        if (suffix === "/workflow") {
          const entry = workflows.get(id)
          return entry === undefined
            ? jsonResponse(404, {
                error: { message: "Role is not selected." },
              })
            : jsonResponse(200, entry)
        }
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}
