import type { components, paths } from "@jobseek/contracts"

export type Health =
  paths["/health"]["get"]["responses"][200]["content"]["application/json"]
export type Session = components["schemas"]["SessionResponse"]
export type Round = components["schemas"]["Round"]
export type LatestCompletedRound =
  paths["/rounds/latest-completed"]["get"]["responses"][200]["content"]["application/json"]
export type RoundResults = components["schemas"]["RoundResults"]
export type RoundHistoryEvent = components["schemas"]["RoundHistoryEvent"]
export type ProcessInputRequest = components["schemas"]["ProcessInputRequest"]
export type ProcessInputResponse = components["schemas"]["ProcessInputResponse"]
export type RoundScreeningView = components["schemas"]["RoundScreeningView"]
export type RelationshipCounterparty =
  components["schemas"]["RelationshipCounterparty"]
export type RelationshipEvent = components["schemas"]["RelationshipEvent"]
export type OpportunityRoute = components["schemas"]["OpportunityRoute"]
export type OwnerInstructionInput =
  components["schemas"]["OwnerInstructionInput"]
export type OwnerInstruction = components["schemas"]["OwnerInstruction"]
export type OwnerDecision = components["schemas"]["OwnerDecision"]
export type OwnerDecisionInput = components["schemas"]["OwnerDecisionInput"]
export type Preferences = components["schemas"]["PreferencesResponse"]
export type UpdatePreferencesRequest =
  components["schemas"]["UpdatePreferencesRequest"]
export type PreferencesMutation = components["schemas"]["PreferencesMutation"]
export type Company = components["schemas"]["Company"]
export type CompanyPage = components["schemas"]["CompanyPage"]
export type Opportunity = components["schemas"]["Opportunity"]
export type OpportunityView = components["schemas"]["OpportunityView"]
export type OpportunityPage = components["schemas"]["OpportunityPage"]
export type IngestionRequest = components["schemas"]["IngestionRequest"]
export type IngestionPage = components["schemas"]["IngestionPage"]
export type RuntimeStatus = components["schemas"]["RuntimeStatus"]
export type MuseReadiness = components["schemas"]["MuseReadiness"]
export type MuseRunCheckpoint = components["schemas"]["MuseRunCheckpoint"]
export type MuseRunReport = components["schemas"]["MuseRunReport"]
export type MuseCommissions = components["schemas"]["MuseCommissions"]
export type OrganisationCategory = components["schemas"]["OrganisationCategory"]
export type OrganisationView = components["schemas"]["OrganisationView"]
export type OrganisationSummary = components["schemas"]["OrganisationSummary"]
export type RecordChange = components["schemas"]["RecordChange"]
export type RecordChangePage = components["schemas"]["RecordChangePage"]
export type EvidenceSource = components["schemas"]["EvidenceSource"]
export type EvidenceClaim = components["schemas"]["EvidenceClaim"]
export type QualificationView = components["schemas"]["QualificationView"]
export type QualificationEvaluation =
  components["schemas"]["QualificationEvaluation"]
export type EvidenceSourcePage = components["schemas"]["EvidenceSourcePage"]
export type EvidencePage = components["schemas"]["EvidencePage"]
export type QualificationHistoryPage =
  components["schemas"]["QualificationHistoryPage"]
export class RequestError extends Error {
  readonly status: number
  readonly details?: Record<string, unknown>
  constructor(
    status: number,
    message: string,
    details?: Record<string, unknown>
  ) {
    super(message)
    this.name = "RequestError"
    this.status = status
    this.details = details
  }
}

export function isUnauthenticated(cause: unknown): boolean {
  return cause instanceof RequestError && cause.status === 401
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: "same-origin",
    ...options,
    headers: { Accept: "application/json", ...options.headers },
  })
  if (!response.ok) {
    let message = `The API returned HTTP ${response.status}.`
    let details: Record<string, unknown> | undefined
    try {
      const body = (await response.json()) as {
        error?: { message?: string; details?: Record<string, unknown> }
      }
      if (body.error?.message) message = body.error.message
      details = body.error?.details
    } catch {
      // An unavailable server may not return a JSON envelope.
    }
    throw new RequestError(response.status, message, details)
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export async function getHealth(signal?: AbortSignal): Promise<Health> {
  const data = await request<Health>("/health", { signal })
  if (
    !data ||
    data.status !== "ok" ||
    data.service !== "jobseek-api" ||
    !data.version
  ) {
    throw new Error("The API returned an unexpected health response.")
  }
  return data
}

export async function getSession(
  signal?: AbortSignal
): Promise<Session | null> {
  const response = await fetch("/api/v1/auth/session", {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  })
  if (response.status === 401) return null
  if (!response.ok)
    throw new RequestError(
      response.status,
      `Could not read the dashboard session (HTTP ${response.status}).`
    )
  return (await response.json()) as Session
}

export function login(password: string): Promise<Session> {
  return request<Session>("/auth/login", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ password }),
  })
}

export function logout(csrfToken: string): Promise<void> {
  return request<void>("/auth/logout", {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
  })
}

export function getPreferences(signal?: AbortSignal): Promise<Preferences> {
  return request<Preferences>("/preferences", { signal })
}

export function updatePreferences(
  input: UpdatePreferencesRequest,
  csrfToken: string
): Promise<PreferencesMutation> {
  return request<PreferencesMutation>("/preferences", {
    method: "PUT",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function getRuntimeStatus(signal?: AbortSignal): Promise<RuntimeStatus> {
  return request<RuntimeStatus>("/runtime-status", { signal })
}

export function getActiveRound(signal?: AbortSignal): Promise<Round | null> {
  return request<Round>("/rounds/active", { signal }).catch(
    (cause: unknown) => {
      if (cause instanceof RequestError && cause.status === 404) return null
      throw cause
    }
  )
}

export function getLatestCompletedSavedRound(
  signal?: AbortSignal
): Promise<Round | null> {
  return request<Round | null>("/rounds/latest-completed?outcome=all", {
    signal,
  })
}

export function processInput(
  input: ProcessInputRequest,
  csrfToken: string
): Promise<ProcessInputResponse> {
  return request<ProcessInputResponse>("/rounds/process-input", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function getOpportunityScreening(
  id: string,
  signal?: AbortSignal
): Promise<RoundScreeningView> {
  return request<RoundScreeningView>(
    `/opportunities/${encodeURIComponent(id)}/screening`,
    {
      signal,
    }
  )
}

export type RoleWorkflowState = components["schemas"]["RoleWorkflowState"]
export type RoleWorkflowList = components["schemas"]["RoleWorkflowList"]

export function listRoleWorkflows(
  signal?: AbortSignal
): Promise<RoleWorkflowState[]> {
  return request<RoleWorkflowList>("/workflow/roles", { signal }).then(
    (page) => page.items
  )
}

export function getRoleWorkflow(
  id: string,
  signal?: AbortSignal
): Promise<RoleWorkflowState> {
  return request<RoleWorkflowState>(
    `/opportunities/${encodeURIComponent(id)}/workflow`,
    {
      signal,
    }
  )
}

export function getRoleWorkflowOrNull(
  id: string,
  signal?: AbortSignal
): Promise<RoleWorkflowState | null> {
  return getRoleWorkflow(id, signal).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return null
    throw cause
  })
}

export function getRoundResults(
  id: string,
  signal?: AbortSignal
): Promise<RoundResults> {
  return request<RoundResults>(`/rounds/${encodeURIComponent(id)}/results`, {
    signal,
  })
}

export function getRoundHistory(
  id: string,
  signal?: AbortSignal
): Promise<RoundHistoryEvent[]> {
  return request<components["schemas"]["RoundHistory"]>(
    `/rounds/${encodeURIComponent(id)}/history`,
    { signal }
  ).then((page) => page.items)
}

export function listRelationshipCounterparties(
  signal?: AbortSignal
): Promise<RelationshipCounterparty[]> {
  return request<components["schemas"]["RelationshipCounterpartyList"]>(
    "/relationships/counterparties",
    { signal }
  ).then((page) => page.items)
}

export function listRelationshipEvents(
  signal?: AbortSignal
): Promise<RelationshipEvent[]> {
  return request<components["schemas"]["RelationshipEventList"]>(
    "/relationships/events",
    {
      signal,
    }
  ).then((page) => page.items)
}

export function listOpportunityRoutes(
  id: string,
  signal?: AbortSignal
): Promise<OpportunityRoute[]> {
  return request<components["schemas"]["OpportunityRouteList"]>(
    `/opportunities/${encodeURIComponent(id)}/routes`,
    { signal }
  ).then((page) => page.items)
}

export function listOwnerInstructions(
  roundId = "",
  signal?: AbortSignal
): Promise<OwnerInstruction[]> {
  const query = roundId ? `?${new URLSearchParams({ roundId })}` : ""
  return request<components["schemas"]["OwnerInstructions"]>(
    `/owner-instructions${query}`,
    {
      signal,
    }
  ).then((page) => page.items)
}

export function addOwnerInstruction(
  input: OwnerInstructionInput,
  csrfToken: string
): Promise<OwnerInstruction> {
  return request<OwnerInstruction>("/owner-instructions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function revokeOwnerInstruction(
  id: string,
  csrfToken: string
): Promise<void> {
  return request<void>(`/owner-instructions/${encodeURIComponent(id)}/revoke`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
  })
}

export function getOwnerOpportunityDecision(
  id: string,
  signal?: AbortSignal
): Promise<OwnerDecision | null> {
  return request<OwnerDecision>(
    `/opportunities/${encodeURIComponent(id)}/decision`,
    {
      signal,
    }
  ).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return null
    throw cause
  })
}

export function setOwnerOpportunityDecision(
  id: string,
  input: OwnerDecisionInput,
  csrfToken: string
): Promise<OwnerDecision> {
  return request<OwnerDecision>(
    `/opportunities/${encodeURIComponent(id)}/decision`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function getRound(id: string, signal?: AbortSignal): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}`, { signal })
}

export function stopRound(id: string, csrfToken: string): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}/stop`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
  })
}

export function resumeRound(id: string, csrfToken: string): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}/resume`, {
    method: "POST",
    headers: { "X-CSRF-Token": csrfToken },
  })
}

export function getOrganisationCategories(signal?: AbortSignal) {
  return request<components["schemas"]["OrganisationCategorySet"]>(
    "/organisation/categories",
    {
      signal,
    }
  )
}

export function listCompanies(
  cursor = "",
  signal?: AbortSignal
): Promise<CompanyPage> {
  const query = new URLSearchParams({ limit: "100", includeArchived: "true" })
  if (cursor) query.set("cursor", cursor)
  return request<CompanyPage>(`/companies?${query}`, { signal })
}

export function listOpportunities(
  cursor = "",
  signal?: AbortSignal
): Promise<OpportunityPage> {
  const query = new URLSearchParams({ limit: "100", includeArchived: "true" })
  if (cursor) query.set("cursor", cursor)
  return request<OpportunityPage>(`/opportunities?${query}`, { signal })
}

export function listIngestions(
  cursor = "",
  signal?: AbortSignal
): Promise<IngestionPage> {
  const query = new URLSearchParams({ limit: "25" })
  if (cursor) query.set("cursor", cursor)
  return request<IngestionPage>(`/ingestions?${query}`, { signal })
}

export function getIngestion(
  id: string,
  signal?: AbortSignal
): Promise<IngestionRequest> {
  return request<IngestionRequest>(`/ingestions/${encodeURIComponent(id)}`, {
    signal,
  })
}

export function submitIngestion(
  input: components["schemas"]["SubmitIngestionRequest"],
  csrfToken: string
): Promise<IngestionRequest> {
  return request<IngestionRequest>("/ingestions", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function retryIngestion(
  id: string,
  input: components["schemas"]["RetryIngestionRequest"],
  csrfToken: string
): Promise<IngestionRequest> {
  return request<IngestionRequest>(
    `/ingestions/${encodeURIComponent(id)}/retry`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function getOpportunity(
  id: string,
  signal?: AbortSignal
): Promise<OpportunityView> {
  return request<OpportunityView>(`/opportunities/${encodeURIComponent(id)}`, {
    signal,
  })
}

export function getOpportunityOrganisation(
  id: string,
  signal?: AbortSignal
): Promise<OrganisationView> {
  return request<OrganisationView>(
    `/opportunities/${encodeURIComponent(id)}/organisation`,
    {
      signal,
    }
  )
}

export function getOrganisationSummaries(
  ids: string[],
  signal?: AbortSignal
): Promise<OrganisationSummary[]> {
  if (ids.length === 0) return Promise.resolve([])
  const query = new URLSearchParams({ ids: ids.join(",") })
  return request<components["schemas"]["OrganisationSummaryList"]>(
    `/organisation/summaries?${query}`,
    { signal }
  ).then((page) => page.items)
}

export function listOpportunityChanges(
  cursor = "",
  signal?: AbortSignal
): Promise<RecordChangePage> {
  const query = new URLSearchParams({ limit: "100", entityKind: "opportunity" })
  if (cursor) query.set("cursor", cursor)
  else query.set("after", "0")
  return request<RecordChangePage>(`/changes?${query}`, { signal })
}

function evidencePath(id: string): string {
  return `/opportunities/${encodeURIComponent(id)}`
}

function pageQuery(cursor: string): string {
  const query = new URLSearchParams({ limit: "25" })
  if (cursor) query.set("cursor", cursor)
  return query.toString()
}

export function listEvidenceSources(
  id: string,
  cursor = "",
  signal?: AbortSignal
) {
  return request<EvidenceSourcePage>(
    `${evidencePath(id)}/evidence-sources?${pageQuery(cursor)}`,
    {
      signal,
    }
  )
}

export function listEvidence(id: string, cursor = "", signal?: AbortSignal) {
  const query = new URLSearchParams(pageQuery(cursor))
  query.set("includeSuperseded", "true")
  return request<EvidencePage>(`${evidencePath(id)}/evidence?${query}`, {
    signal,
  })
}

export function getQualification(
  id: string,
  signal?: AbortSignal
): Promise<QualificationView> {
  return fetch(`/api/v1${evidencePath(id)}/qualification`, {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  }).then(async (response) => {
    if (response.ok || response.status === 503) {
      const view = (await response.json()) as QualificationView
      if (response.status === 503 && view.current !== null) {
        throw new Error("The API returned an invalid refresh response.")
      }
      return view
    }
    let message = `The API returned HTTP ${response.status}.`
    try {
      const body = (await response.json()) as { error?: { message?: string } }
      if (body.error?.message) message = body.error.message
    } catch {
      // Network intermediaries may return non-JSON errors.
    }
    throw new RequestError(response.status, message)
  })
}

export function listQualificationHistory(
  id: string,
  cursor = "",
  signal?: AbortSignal
) {
  return request<QualificationHistoryPage>(
    `${evidencePath(id)}/qualification/history?${pageQuery(cursor)}`,
    { signal }
  )
}

export type ResearchRunView = components["schemas"]["ResearchRunView"]
export type ResearchActivityPage = components["schemas"]["ResearchActivityPage"]
export type ResearchActivityEvent =
  components["schemas"]["ResearchActivityEvent"]
export type ResearchCaptureView = components["schemas"]["ResearchCaptureView"]
export type ResearchIdentityView = components["schemas"]["ResearchIdentityView"]
export type ResearchReportView = components["schemas"]["ResearchReportView"]
export type CommissionResearchRequest =
  components["schemas"]["CommissionResearchRequest"]
export type SteerResearchRequest = components["schemas"]["SteerResearchRequest"]
export type SteeringMessage = components["schemas"]["SteeringMessage"]
export type ResearchAllowance = components["schemas"]["ResearchAllowance"]

export function commissionResearchRun(
  input: CommissionResearchRequest,
  csrfToken: string
): Promise<ResearchRunView> {
  return request<ResearchRunView>("/research/runs", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function getResearchRun(
  id: string,
  signal?: AbortSignal
): Promise<ResearchRunView> {
  return request<ResearchRunView>(`/research/runs/${encodeURIComponent(id)}`, {
    signal,
  })
}

export function steerResearchRun(
  id: string,
  input: SteerResearchRequest,
  csrfToken: string
): Promise<SteeringMessage> {
  return request<SteeringMessage>(
    `/research/runs/${encodeURIComponent(id)}/steer`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function listResearchActivity(
  id: string,
  cursor = "",
  limit = 25,
  signal?: AbortSignal
): Promise<ResearchActivityPage> {
  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) query.set("cursor", cursor)
  return request<ResearchActivityPage>(
    `/research/runs/${encodeURIComponent(id)}/activity?${query.toString()}`,
    { signal }
  )
}

export function getResearchReport(
  id: string,
  signal?: AbortSignal
): Promise<ResearchReportView> {
  return request<ResearchReportView>(
    `/research/runs/${encodeURIComponent(id)}/report`,
    {
      signal,
    }
  )
}

export function getResearchCapture(
  id: string,
  signal?: AbortSignal
): Promise<ResearchCaptureView> {
  return request<ResearchCaptureView>(
    `/research/captures/${encodeURIComponent(id)}`,
    { signal }
  )
}

// Server run history (F2 consolidation of the provisional G2/D3 module
// `features/discovery/run-history.ts`). G switches its import to these
// and deletes that module; see the I-side proposal.
export type RunHistoryItem = components["schemas"]["RunHistoryItem"]
export type RunHistoryPage = components["schemas"]["RunHistoryPage"]

/** Research commissions carry outcome `research_run` (rounds supervisor). */
export const researchRunOutcome = "research_run"

/** States worth resuming: active work plus stopped-but-resumable paused. */
const resumableRunStates: ReadonlySet<string> = new Set([
  "queued",
  "running",
  "awaiting_input",
  "stopping",
  "paused",
])

const terminalRunStates: ReadonlySet<string> = new Set([
  "completed",
  "failed",
])

/**
 * Newest-first run history for server-backed restoration. Always scoped to
 * the research outcome so profile-correction rounds never surface here.
 * Pure GET, zero model calls, commissions nothing.
 */
export function listResearchRuns(
  options: { limit?: number; cursor?: string } = {},
  signal?: AbortSignal
): Promise<RunHistoryPage> {
  const query = new URLSearchParams({ outcome: researchRunOutcome })
  if (options.limit !== undefined) query.set("limit", String(options.limit))
  if (options.cursor !== undefined && options.cursor !== "")
    query.set("cursor", options.cursor)
  return request<RunHistoryPage>(`/research/runs?${query.toString()}`, {
    signal,
  })
}

/**
 * Pick the run a fresh context restores: the newest resumable
 * (active/paused) run when one exists, else the newest terminal run, else
 * null (idle commission). The page is newest-first, so the first match
 * wins. Unknown states never match: they stay visible in history but are
 * never auto-restored.
 */
export function pickRestorableRunId(items: RunHistoryItem[]): string | null {
  const resumable = items.find((item) => resumableRunStates.has(item.state))
  if (resumable !== undefined) return resumable.runId
  const terminal = items.find((item) => terminalRunStates.has(item.state))
  return terminal?.runId ?? null
}

// Sourced owner context (F2 consolidation of the provisional G1/D4 module
// `features/owner-context/sourced-context.ts`). G switches its import to
// these and deletes that module; see the I-side proposal.
export type SourcedOwnerContext = components["schemas"]["SourcedOwnerContext"]
export type OwnerCareerSource = SourcedOwnerContext["sources"][number]

/** The server predates D4 or has no owner-context route: neutral absence. */
export class CareerSourceNotConnected extends Error {
  constructor() {
    super("Career sources are not available on this server.")
    this.name = "CareerSourceNotConnected"
  }
}

export function isCareerSourceNotConnected(cause: unknown): boolean {
  return cause instanceof CareerSourceNotConnected
}

/**
 * Read owner identity plus approved career sources with provenance (D4).
 * Pure GET, zero model calls; bodies serve verbatim. A 404 means the
 * endpoint is absent and maps to `CareerSourceNotConnected` (neutral);
 * every other failure keeps its `RequestError` status/message.
 */
export function getSourcedOwnerContext(
  signal?: AbortSignal
): Promise<SourcedOwnerContext> {
  return request<SourcedOwnerContext>("/research/owner-context", {
    signal,
  }).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404)
      throw new CareerSourceNotConnected()
    throw cause
  })
}

export function explainResearchIdentity(
  subjectKind: "employer" | "vacancy",
  subjectId: string,
  signal?: AbortSignal
): Promise<ResearchIdentityView> {
  const query = new URLSearchParams({ subjectKind, subjectId })
  return request<ResearchIdentityView>(
    `/research/identity?${query.toString()}`,
    { signal }
  )
}

export type SearchBriefView = components["schemas"]["SearchBriefView"]
export type ReasonCatalogView = components["schemas"]["ReasonCatalogView"]
export type FindingEntry = components["schemas"]["FindingEntry"]
export type FindingList = components["schemas"]["FindingList"]
export type CheckView = components["schemas"]["CheckView"]
export type CheckStatusView = components["schemas"]["CheckStatusView"]
export type CheckStartRequest = components["schemas"]["CheckStartRequest"]
export type CheckActivityPage = components["schemas"]["CheckActivityPage"]
export type SavedAnswer = components["schemas"]["SavedAnswer"]
export type SavedAnswerList = components["schemas"]["SavedAnswerList"]
export type SavedAnswerCreate = components["schemas"]["SavedAnswerCreate"]
export type SavedAnswerVersionCreate =
  components["schemas"]["SavedAnswerVersionCreate"]
export type AnswerMatchView = components["schemas"]["AnswerMatchView"]
export type AnswerMatchRequest = components["schemas"]["AnswerMatchRequest"]
export type QuestionAnswerList = components["schemas"]["QuestionAnswerList"]
export type QuestionAnswerValue = components["schemas"]["QuestionAnswerValue"]
export type AnswerValueSave = components["schemas"]["AnswerValueSave"]
export type MaterialPrepareRequest =
  components["schemas"]["MaterialPrepareRequest"]
export type ArtifactView = components["schemas"]["ArtifactView"]
export type ArtifactFormValue = components["schemas"]["ArtifactFormValue"]
export type ArtifactReadinessEntry =
  components["schemas"]["ArtifactReadinessEntry"]
export type ArtifactReadinessSet =
  components["schemas"]["ArtifactReadinessSet"]
export type ArtifactSaveRequest = components["schemas"]["ArtifactSaveRequest"]
export type MaterialRewriteRequest =
  components["schemas"]["MaterialRewriteRequest"]

export function getSearchBrief(signal?: AbortSignal): Promise<SearchBriefView> {
  return request<SearchBriefView>("/research/brief", { signal })
}

export function getReasonCatalog(
  version: number,
  signal?: AbortSignal
): Promise<ReasonCatalogView> {
  return request<ReasonCatalogView>(`/research/briefs/${version}/catalog`, {
    signal,
  })
}

export function listRunFindings(
  id: string,
  options: { cursor?: string; limit?: number; group?: string } = {},
  signal?: AbortSignal
): Promise<FindingList> {
  const query = new URLSearchParams()
  if (options.cursor) query.set("cursor", options.cursor)
  if (options.limit) query.set("limit", String(options.limit))
  if (options.group) query.set("group", options.group)
  const suffix = query.size > 0 ? `?${query}` : ""
  return request<FindingList>(
    `/research/runs/${encodeURIComponent(id)}/findings${suffix}`,
    { signal }
  )
}

export function getOpportunityFinding(
  id: string,
  signal?: AbortSignal
): Promise<FindingEntry> {
  return request<FindingEntry>(
    `/opportunities/${encodeURIComponent(id)}/finding`,
    { signal }
  )
}

export function startOpportunityCheck(
  id: string,
  input: CheckStartRequest,
  csrfToken: string
): Promise<CheckStatusView> {
  return request<CheckStatusView>(
    `/opportunities/${encodeURIComponent(id)}/checks`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function getCurrentOpportunityCheck(
  id: string,
  signal?: AbortSignal
): Promise<CheckStatusView> {
  return request<CheckStatusView>(
    `/opportunities/${encodeURIComponent(id)}/checks/current`,
    { signal }
  )
}

export function listOpportunityCheckActivity(
  id: string,
  cursor = "",
  limit = 25,
  signal?: AbortSignal
): Promise<CheckActivityPage> {
  const query = new URLSearchParams({ limit: String(limit) })
  if (cursor) query.set("cursor", cursor)
  return request<CheckActivityPage>(
    `/opportunities/${encodeURIComponent(id)}/checks/current/activity?${query}`,
    { signal }
  )
}

export function getOpportunityCheck(
  id: string,
  checkId: string,
  signal?: AbortSignal
): Promise<CheckView> {
  return request<CheckView>(
    `/opportunities/${encodeURIComponent(id)}/checks/${encodeURIComponent(checkId)}`,
    { signal }
  )
}

export function listSavedAnswers(
  options: { cursor?: string; limit?: number; scope?: string } = {},
  signal?: AbortSignal
): Promise<SavedAnswerList> {
  const query = new URLSearchParams()
  if (options.cursor) query.set("cursor", options.cursor)
  if (options.limit) query.set("limit", String(options.limit))
  if (options.scope) query.set("scope", options.scope)
  const suffix = query.size > 0 ? `?${query}` : ""
  return request<SavedAnswerList>(`/answers${suffix}`, { signal })
}

export function createSavedAnswer(
  input: SavedAnswerCreate,
  csrfToken: string
): Promise<SavedAnswer> {
  return request<SavedAnswer>("/answers", {
    method: "POST",
    headers: { "Content-Type": "application/json", "X-CSRF-Token": csrfToken },
    body: JSON.stringify(input),
  })
}

export function getSavedAnswer(
  answerId: string,
  signal?: AbortSignal
): Promise<SavedAnswer> {
  return request<SavedAnswer>(`/answers/${encodeURIComponent(answerId)}`, {
    signal,
  })
}

export function approveSavedAnswerVersion(
  answerId: string,
  input: SavedAnswerVersionCreate,
  csrfToken: string
): Promise<SavedAnswer> {
  return request<SavedAnswer>(
    `/answers/${encodeURIComponent(answerId)}/versions`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function matchOpportunityAnswers(
  id: string,
  input: AnswerMatchRequest,
  csrfToken: string
): Promise<AnswerMatchView> {
  return request<AnswerMatchView>(
    `/opportunities/${encodeURIComponent(id)}/answers/match`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function getCurrentAnswerMatch(
  id: string,
  signal?: AbortSignal
): Promise<AnswerMatchView> {
  return request<AnswerMatchView>(
    `/opportunities/${encodeURIComponent(id)}/answers/match/current`,
    { signal }
  )
}

export function getCurrentQuestionAnswers(
  id: string,
  signal?: AbortSignal
): Promise<QuestionAnswerList> {
  return request<QuestionAnswerList>(
    `/opportunities/${encodeURIComponent(id)}/answers/current`,
    { signal }
  )
}

export type AnswerCommitResult =
  paths["/opportunities/{id}/answers/commit"]["post"]["responses"][200]["content"]["application/json"]

export function commitRoleAnswers(
  id: string,
  csrfToken: string
): Promise<AnswerCommitResult> {
  return request<AnswerCommitResult>(
    `/opportunities/${encodeURIComponent(id)}/answers/commit`,
    {
      method: "POST",
      headers: { "X-CSRF-Token": csrfToken },
    }
  )
}

export function saveQuestionAnswer(
  id: string,
  questionId: string,
  input: AnswerValueSave,
  csrfToken: string
): Promise<QuestionAnswerValue> {
  return request<QuestionAnswerValue>(
    `/opportunities/${encodeURIComponent(id)}/questions/${encodeURIComponent(questionId)}/answer`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

// The duplicate combined-pack Prepare application and its `/materials/*`
// endpoints are gone (M1 cut the endpoints, E1 cut `usePrepareActions`):
// drafting reads `listArtifactReadiness`, writes `draftOpportunityArtifacts`,
// edits `saveOpportunityArtifact`, and rewrites `rewriteOpportunityArtifact`.
export function listArtifactReadiness(
  id: string,
  signal?: AbortSignal
): Promise<ArtifactReadinessSet> {
  return request<ArtifactReadinessSet>(
    `/opportunities/${encodeURIComponent(id)}/artifacts`,
    { signal }
  )
}

export function draftOpportunityArtifacts(
  id: string,
  input: MaterialPrepareRequest,
  csrfToken: string
): Promise<ArtifactReadinessSet> {
  return request<ArtifactReadinessSet>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/draft`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function listPrepareActivity(
  id: string,
  signal?: AbortSignal
): Promise<CheckActivityPage> {
  return request<CheckActivityPage>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/activity`,
    { signal }
  )
}

export function getArtifactReadiness(
  id: string,
  artifactType: string,
  signal?: AbortSignal
): Promise<ArtifactReadinessEntry> {
  return request<ArtifactReadinessEntry>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(artifactType)}`,
    { signal }
  )
}

export function saveOpportunityArtifact(
  id: string,
  artifactType: string,
  input: ArtifactSaveRequest,
  csrfToken: string
): Promise<ArtifactView> {
  return request<ArtifactView>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(artifactType)}`,
    {
      method: "PUT",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

// Per-artifact rewrite/versions/export + clarifications + Handoff (F2/F3
// consolidation of the provisional E module
// `features/prepare/artifactsApi.ts`, adopted verbatim so E can switch its
// imports to client reuse; see the I-side proposal).
export type Clarification = components["schemas"]["Clarification"]
export type ClarificationAnswer = components["schemas"]["ClarificationAnswer"]
export type ClarificationList = { items: Clarification[] }
export type ArtifactVersionList = { items: ArtifactView[] }
export type HandoffView = components["schemas"]["HandoffView"]
export type HandoffItem = components["schemas"]["HandoffItem"]
export type HandoffUpload = components["schemas"]["HandoffUpload"]
export type HandoffSave = components["schemas"]["HandoffSave"]
export type SavedJobEntry = components["schemas"]["SavedJobEntry"]
export type SavedJobItem = components["schemas"]["SavedJobItem"]
export type SavedJobList = { items: SavedJobEntry[] }

export function listClarifications(
  id: string,
  signal?: AbortSignal
): Promise<ClarificationList> {
  return request<ClarificationList>(
    `/opportunities/${encodeURIComponent(id)}/clarifications`,
    { signal }
  )
}

export function answerClarification(
  id: string,
  clarificationId: string,
  input: ClarificationAnswer,
  csrfToken: string
): Promise<Clarification> {
  return request<Clarification>(
    `/opportunities/${encodeURIComponent(id)}/clarifications/${encodeURIComponent(clarificationId)}/answer`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function rewriteOpportunityArtifact(
  id: string,
  artifactType: string,
  input: MaterialRewriteRequest,
  csrfToken: string
): Promise<ArtifactReadinessSet> {
  return request<ArtifactReadinessSet>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(artifactType)}/rewrite`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

export function listArtifactVersions(
  id: string,
  artifactType: string,
  signal?: AbortSignal
): Promise<ArtifactVersionList> {
  return request<ArtifactVersionList>(
    `/opportunities/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(artifactType)}/versions`,
    { signal }
  )
}

export interface ArtifactExport {
  text: string
  mediaType: string
  filename: string
}

// Extension from the export content type. The server renders markdown or
// plain text; anything unexpected stays a safe .txt rather than inventing
// a format.
export function exportFileExtension(mediaType: string): string {
  return mediaType.toLowerCase().includes("markdown") ? "md" : "txt"
}

// Filenames name the job, the item and the pinned version so a download
// taken out of the app stays attributable: job-1-cv-v2.md.
export function exportFilename(
  jobId: string,
  artifactType: string,
  version: number | "live",
  mediaType: string
): string {
  const tag = version === "live" ? "live" : `v${version}`
  return `${jobId}-${artifactType}-${tag}.${exportFileExtension(mediaType)}`
}

// Canonical file download for the shown version, rendered by the server
// from stored content with identity/version/checksum headers. Downloading
// never means applied. form_values omits the version and derives live.
export async function exportOpportunityArtifact(
  id: string,
  artifactType: string,
  version: number | null,
  signal?: AbortSignal
): Promise<ArtifactExport> {
  const query = version === null ? "" : `?version=${version}`
  const response = await fetch(
    `/api/v1/opportunities/${encodeURIComponent(id)}/artifacts/${encodeURIComponent(artifactType)}/export${query}`,
    { credentials: "same-origin", signal }
  )
  if (!response.ok) {
    let message = `The API returned HTTP ${response.status}.`
    let details: Record<string, unknown> | undefined
    try {
      const body = (await response.json()) as {
        error?: { message?: string; details?: Record<string, unknown> }
      }
      if (body.error?.message) message = body.error.message
      details = body.error?.details
    } catch {
      // An unavailable server may not return a JSON envelope.
    }
    throw new RequestError(response.status, message, details)
  }
  const mediaType =
    response.headers.get("Content-Type")?.split(";")[0]?.trim() ?? ""
  const text = await response.text()
  return {
    text,
    mediaType,
    filename: exportFilename(
      id,
      artifactType,
      version ?? "live",
      mediaType === "" ? "text/plain" : mediaType
    ),
  }
}

export function getOpportunityHandoff(
  id: string,
  signal?: AbortSignal
): Promise<HandoffView> {
  return request<HandoffView>(
    `/opportunities/${encodeURIComponent(id)}/handoff`,
    { signal }
  )
}

export function saveOpportunityHandoff(
  id: string,
  input: HandoffSave,
  csrfToken: string
): Promise<RoleWorkflowState> {
  return request<RoleWorkflowState>(
    `/opportunities/${encodeURIComponent(id)}/handoff`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "X-CSRF-Token": csrfToken,
      },
      body: JSON.stringify(input),
    }
  )
}

/**
 * Durable saved-job/artifact index (M6/F3): every non-archived role with
 * stored work, most recently touched first. Passive.
 */
export function listSavedJobs(signal?: AbortSignal): Promise<SavedJobList> {
  return request<SavedJobList>("/saved-jobs", { signal })
}

export type MuseTierParam = "contributor" | "standard"

export function getMuseReadiness(
  tier: MuseTierParam,
  signal?: AbortSignal
): Promise<MuseReadiness> {
  const query = `?${new URLSearchParams({ tier })}`
  return request<MuseReadiness>(`/muse/readiness${query}`, { signal })
}

export function getMuseCheckpoints(
  runRef: string,
  signal?: AbortSignal
): Promise<MuseRunCheckpoint[]> {
  const query = `?${new URLSearchParams({ runRef })}`
  return request<MuseRunCheckpoint[]>(`/muse/checkpoints${query}`, {
    signal,
  }).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return []
    throw cause
  })
}

export function getMuseReport(
  runRef: string,
  signal?: AbortSignal
): Promise<MuseRunReport | null> {
  const query = `?${new URLSearchParams({ runRef })}`
  return request<MuseRunReport>(`/muse/report${query}`, { signal }).catch(
    (cause: unknown) => {
      if (cause instanceof RequestError && cause.status === 404) return null
      throw cause
    }
  )
}

export function getMuseCommissions(signal?: AbortSignal): Promise<number> {
  return request<MuseCommissions>("/muse/commissions", { signal }).then(
    (page) => page.count
  )
}
