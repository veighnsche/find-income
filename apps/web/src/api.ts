import type { components, paths } from '@jobseek/contracts';

export type Health = paths['/health']['get']['responses'][200]['content']['application/json'];
export type Session = components['schemas']['SessionResponse'];
export type Round = components['schemas']['Round'];
export type LatestCompletedRound =
  paths['/rounds/latest-completed']['get']['responses'][200]['content']['application/json'];
export type RoundResults = components['schemas']['RoundResults'];
export type RoundHistoryEvent = components['schemas']['RoundHistoryEvent'];
export type PrepareRoundRequest = components['schemas']['PrepareRoundRequest'];
export type ProcessInputRequest = components['schemas']['ProcessInputRequest'];
export type ProcessInputResponse = components['schemas']['ProcessInputResponse'];
export type ApplicationPackSummary = components['schemas']['ApplicationPackSummary'];
export type ApplicationPackDetail = components['schemas']['ApplicationPackDetail'];
export type RoundScreeningView = components['schemas']['RoundScreeningView'];
export type RelationshipCounterparty = components['schemas']['RelationshipCounterparty'];
export type RelationshipEvent = components['schemas']['RelationshipEvent'];
export type OpportunityRoute = components['schemas']['OpportunityRoute'];
export type OwnerInstructionInput = components['schemas']['OwnerInstructionInput'];
export type OwnerInstruction = components['schemas']['OwnerInstruction'];
export type OwnerDecision = components['schemas']['OwnerDecision'];
export type OwnerDecisionInput = components['schemas']['OwnerDecisionInput'];
export type Preferences = components['schemas']['PreferencesResponse'];
export type Company = components['schemas']['Company'];
export type CompanyPage = components['schemas']['CompanyPage'];
export type Opportunity = components['schemas']['Opportunity'];
export type OpportunityView = components['schemas']['OpportunityView'];
export type OpportunityPage = components['schemas']['OpportunityPage'];
export type IngestionRequest = components['schemas']['IngestionRequest'];
export type IngestionPage = components['schemas']['IngestionPage'];
export type RuntimeStatus = components['schemas']['RuntimeStatus'];
export type CodexStatus = components['schemas']['CodexStatus'];
export type CodexConnection = components['schemas']['CodexConnection'];
export type OrganisationCategory = components['schemas']['OrganisationCategory'];
export type OrganisationView = components['schemas']['OrganisationView'];
export type OrganisationSummary = components['schemas']['OrganisationSummary'];
export type RecordChange = components['schemas']['RecordChange'];
export type RecordChangePage = components['schemas']['RecordChangePage'];
export type EvidenceSource = components['schemas']['EvidenceSource'];
export type EvidenceClaim = components['schemas']['EvidenceClaim'];
export type QualificationView = components['schemas']['QualificationView'];
export type QualificationEvaluation = components['schemas']['QualificationEvaluation'];
export type EvidenceSourcePage = components['schemas']['EvidenceSourcePage'];
export type EvidencePage = components['schemas']['EvidencePage'];
export type QualificationHistoryPage = components['schemas']['QualificationHistoryPage'];
export type OfferOptionSet = components['schemas']['OfferOptionSet'];
export type DeliveryCapability = components['schemas']['DeliveryCapability'];
export type PrepareDeliveryReviewRequest = components['schemas']['PrepareDeliveryReviewRequest'];
export type DeliveryReview = components['schemas']['DeliveryReview'];
export type DeliveryItem = components['schemas']['DeliveryItem'];
export type DeliverySendResult = components['schemas']['DeliverySendResult'];
export type DeliveryReconciliation = components['schemas']['DeliveryReconciliation'];
export type PrepareInterviewRequest = components['schemas']['PrepareInterviewRequest'];
export type DebriefInterviewRequest = components['schemas']['DebriefInterviewRequest'];
export type InterviewCommissionResponse = components['schemas']['InterviewCommissionResponse'];
export type DebriefInterviewResponse = components['schemas']['DebriefInterviewResponse'];
export type InterviewView = components['schemas']['InterviewView'];
export type InterviewDetail = components['schemas']['InterviewDetail'];
export type CompareOffersRoundRequest = components['schemas']['CompareOffersRoundRequest'];
export type CompareOffersRoundResponse = components['schemas']['CompareOffersRoundResponse'];
export type OfferComparisonResult = components['schemas']['OfferComparisonResult'];
// JSON numbers representing int64 rationals must survive beyond Number.MAX_SAFE_INTEGER.
export type ExactOfferComparisonResult<T = OfferComparisonResult> = T extends number
  ? string
  : T extends Array<infer U>
    ? ExactOfferComparisonResult<U>[]
    : T extends object
      ? { [K in keyof T]: ExactOfferComparisonResult<T[K]> }
      : T;

export function compareOffersRound(
  input: CompareOffersRoundRequest,
  csrfToken: string,
): Promise<CompareOffersRoundResponse> {
  return request<CompareOffersRoundResponse>('/rounds/compare-offers', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function getLatestCompletedOfferRound(signal?: AbortSignal): Promise<Round | null> {
  return request<Round | null>('/rounds/latest-completed?outcome=compare_offers', { signal });
}

function parseExactIntegers(json: string): unknown {
  let quoted = '';
  let inside = false;
  let escaped = false;
  for (let index = 0; index < json.length; index += 1) {
    const char = json[index];
    if (inside) {
      quoted += char;
      if (escaped) escaped = false;
      else if (char === '\\') escaped = true;
      else if (char === '"') inside = false;
      continue;
    }
    if (char === '"') {
      inside = true;
      quoted += char;
      continue;
    }
    if (char === '-' || (char >= '0' && char <= '9')) {
      const token = json.slice(index).match(/^-?(?:0|[1-9]\d*)(?:\.\d+)?(?:[eE][+-]?\d+)?/);
      if (!token) throw new Error('Offer comparison contains invalid JSON number syntax.');
      quoted += /^-?(?:0|[1-9]\d*)$/.test(token[0]) ? `"${token[0]}"` : token[0];
      index += token[0].length - 1;
      continue;
    }
    quoted += char;
  }
  return JSON.parse(quoted) as unknown;
}

async function readExactOfferComparison(path: string, signal?: AbortSignal) {
  const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    headers: { Accept: 'application/json' },
    signal,
  });
  if (!response.ok) {
    let message = `Offer comparison read failed (HTTP ${response.status}).`;
    try {
      const body = (await response.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch {
      /* The status remains available. */
    }
    throw new RequestError(response.status, message);
  }
  return parseExactIntegers(await response.text()) as ExactOfferComparisonResult;
}

export function getOfferComparisonByRound(roundId: string, signal?: AbortSignal) {
  return readExactOfferComparison(
    `/offer-comparisons/by-round/${encodeURIComponent(roundId)}`,
    signal,
  );
}

export function getOfferComparison(id: string, signal?: AbortSignal) {
  return readExactOfferComparison(`/offer-comparisons/${encodeURIComponent(id)}`, signal);
}

export function listInterviews(signal?: AbortSignal): Promise<InterviewView[]> {
  return request<components['schemas']['InterviewList']>('/interviews', { signal }).then(
    (page) => page.items,
  );
}

export function getInterview(id: string, signal?: AbortSignal): Promise<InterviewDetail> {
  return request<InterviewDetail>(`/interviews/${encodeURIComponent(id)}`, { signal });
}

export function prepareInterview(
  input: PrepareInterviewRequest,
  csrfToken: string,
): Promise<InterviewCommissionResponse> {
  return request<InterviewCommissionResponse>('/interviews/prepare', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function debriefInterview(
  id: string,
  input: DebriefInterviewRequest,
  csrfToken: string,
): Promise<DebriefInterviewResponse> {
  return request<DebriefInterviewResponse>(`/interviews/${encodeURIComponent(id)}/debrief`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export class RequestError extends Error {
  constructor(
    readonly status: number,
    message: string,
    readonly details?: Record<string, unknown>,
  ) {
    super(message);
    this.name = 'RequestError';
  }
}

export function isUnauthenticated(cause: unknown): boolean {
  return cause instanceof RequestError && cause.status === 401;
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const response = await fetch(`/api/v1${path}`, {
    credentials: 'same-origin',
    ...options,
    headers: { Accept: 'application/json', ...options.headers },
  });
  if (!response.ok) {
    let message = `The API returned HTTP ${response.status}.`;
    let details: Record<string, unknown> | undefined;
    try {
      const body = (await response.json()) as {
        error?: { message?: string; details?: Record<string, unknown> };
      };
      if (body.error?.message) message = body.error.message;
      details = body.error?.details;
    } catch {
      // An unavailable server may not return a JSON envelope.
    }
    throw new RequestError(response.status, message, details);
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

export async function getHealth(signal?: AbortSignal): Promise<Health> {
  const data = await request<Health>('/health', { signal });
  if (!data || data.status !== 'ok' || data.service !== 'jobseek-api' || !data.version) {
    throw new Error('The API returned an unexpected health response.');
  }
  return data;
}

export async function getSession(signal?: AbortSignal): Promise<Session | null> {
  const response = await fetch('/api/v1/auth/session', {
    credentials: 'same-origin',
    signal,
    headers: { Accept: 'application/json' },
  });
  if (response.status === 401) return null;
  if (!response.ok)
    throw new RequestError(
      response.status,
      `Could not read the dashboard session (HTTP ${response.status}).`,
    );
  return (await response.json()) as Session;
}

export function login(password: string): Promise<Session> {
  return request<Session>('/auth/login', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ password }),
  });
}

export function logout(csrfToken: string): Promise<void> {
  return request<void>('/auth/logout', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function getPreferences(signal?: AbortSignal): Promise<Preferences> {
  return request<Preferences>('/preferences', { signal });
}

export function getRuntimeStatus(signal?: AbortSignal): Promise<RuntimeStatus> {
  return request<RuntimeStatus>('/runtime-status', { signal });
}

export function getActiveRound(signal?: AbortSignal): Promise<Round | null> {
  return request<Round>('/rounds/active', { signal }).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return null;
    throw cause;
  });
}

export function getLatestCompletedSavedRound(signal?: AbortSignal): Promise<Round | null> {
  return request<Round | null>('/rounds/latest-completed?outcome=all', { signal });
}

export function getDeliveryCapability(signal?: AbortSignal): Promise<DeliveryCapability> {
  return request<DeliveryCapability>('/delivery/capability', { signal });
}

export function prepareDeliveryReview(
  input: PrepareDeliveryReviewRequest,
  csrfToken: string,
): Promise<DeliveryReview> {
  return request<DeliveryReview>('/delivery/reviews', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function getDeliveryReview(id: string, signal?: AbortSignal): Promise<DeliveryReview> {
  return request<DeliveryReview>(`/delivery/reviews/${encodeURIComponent(id)}`, { signal });
}

export function approveDeliveryReview(
  id: string,
  materialSha256: string,
  csrfToken: string,
): Promise<DeliveryReview> {
  return request<DeliveryReview>(`/delivery/reviews/${encodeURIComponent(id)}/approve`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ materialSha256 }),
  });
}

export function sendDeliveryReview(id: string, csrfToken: string): Promise<DeliverySendResult> {
  return request<DeliverySendResult>(`/delivery/reviews/${encodeURIComponent(id)}/send`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function reconcileDeliveryReview(
  id: string,
  csrfToken: string,
): Promise<DeliveryReconciliation> {
  return request<DeliveryReconciliation>(`/delivery/reviews/${encodeURIComponent(id)}/reconcile`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function closeDeliveryReview(id: string, csrfToken: string): Promise<Round> {
  return request<Round>(`/delivery/reviews/${encodeURIComponent(id)}/close`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function prepareApplicationRound(
  input: PrepareRoundRequest,
  csrfToken: string,
): Promise<Round> {
  return request<Round>('/rounds/prepare', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function processInput(
  input: ProcessInputRequest,
  csrfToken: string,
): Promise<ProcessInputResponse> {
  return request<ProcessInputResponse>('/rounds/process-input', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function listApplicationPacks(
  opportunityId: string,
  signal?: AbortSignal,
): Promise<ApplicationPackSummary[]> {
  return request<components['schemas']['ApplicationPackList']>(
    `/opportunities/${encodeURIComponent(opportunityId)}/application-packs`,
    { signal },
  ).then((page) => page.items);
}

export function getApplicationPack(
  id: string,
  signal?: AbortSignal,
): Promise<ApplicationPackDetail> {
  return request<ApplicationPackDetail>(`/application-packs/${encodeURIComponent(id)}`, { signal });
}

export function getOpportunityScreening(
  id: string,
  signal?: AbortSignal,
): Promise<RoundScreeningView> {
  return request<RoundScreeningView>(`/opportunities/${encodeURIComponent(id)}/screening`, {
    signal,
  });
}

export function applicationPackPdfUrl(id: string): string {
  return `/api/v1/application-packs/${encodeURIComponent(id)}/pdf`;
}

export function applicationPackSourceUrl(id: string): string {
  return `/api/v1/application-packs/${encodeURIComponent(id)}/source.zip`;
}

export function getRoundResults(id: string, signal?: AbortSignal): Promise<RoundResults> {
  return request<RoundResults>(`/rounds/${encodeURIComponent(id)}/results`, { signal });
}

export function getRoundHistory(id: string, signal?: AbortSignal): Promise<RoundHistoryEvent[]> {
  return request<components['schemas']['RoundHistory']>(
    `/rounds/${encodeURIComponent(id)}/history`,
    { signal },
  ).then((page) => page.items);
}

export function listRelationshipCounterparties(
  signal?: AbortSignal,
): Promise<RelationshipCounterparty[]> {
  return request<components['schemas']['RelationshipCounterpartyList']>(
    '/relationships/counterparties',
    { signal },
  ).then((page) => page.items);
}

export function listRelationshipEvents(signal?: AbortSignal): Promise<RelationshipEvent[]> {
  return request<components['schemas']['RelationshipEventList']>('/relationships/events', {
    signal,
  }).then((page) => page.items);
}

export function listOpportunityRoutes(
  id: string,
  signal?: AbortSignal,
): Promise<OpportunityRoute[]> {
  return request<components['schemas']['OpportunityRouteList']>(
    `/opportunities/${encodeURIComponent(id)}/routes`,
    { signal },
  ).then((page) => page.items);
}

export function listOwnerInstructions(
  roundId = '',
  signal?: AbortSignal,
): Promise<OwnerInstruction[]> {
  const query = roundId ? `?${new URLSearchParams({ roundId })}` : '';
  return request<components['schemas']['OwnerInstructions']>(`/owner-instructions${query}`, {
    signal,
  }).then((page) => page.items);
}

export function addOwnerInstruction(
  input: OwnerInstructionInput,
  csrfToken: string,
): Promise<OwnerInstruction> {
  return request<OwnerInstruction>('/owner-instructions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function revokeOwnerInstruction(id: string, csrfToken: string): Promise<void> {
  return request<void>(`/owner-instructions/${encodeURIComponent(id)}/revoke`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function getOwnerOpportunityDecision(
  id: string,
  signal?: AbortSignal,
): Promise<OwnerDecision | null> {
  return request<OwnerDecision>(`/opportunities/${encodeURIComponent(id)}/decision`, {
    signal,
  }).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404) return null;
    throw cause;
  });
}

export function setOwnerOpportunityDecision(
  id: string,
  input: OwnerDecisionInput,
  csrfToken: string,
): Promise<OwnerDecision> {
  return request<OwnerDecision>(`/opportunities/${encodeURIComponent(id)}/decision`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function getRound(id: string, signal?: AbortSignal): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}`, { signal });
}

export function stopRound(id: string, csrfToken: string): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}/stop`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function resumeRound(id: string, csrfToken: string): Promise<Round> {
  return request<Round>(`/rounds/${encodeURIComponent(id)}/resume`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function getCodexStatus(signal?: AbortSignal): Promise<CodexStatus> {
  return request<CodexStatus>('/codex/status', { signal });
}

export function connectCodex(csrfToken: string): Promise<CodexConnection> {
  return request<CodexConnection>('/codex/connect', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function cancelCodexConnect(csrfToken: string): Promise<void> {
  return request<void>('/codex/connect/cancel', {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function getOrganisationCategories(signal?: AbortSignal) {
  return request<components['schemas']['OrganisationCategorySet']>('/organisation/categories', {
    signal,
  });
}

export function listCompanies(cursor = '', signal?: AbortSignal): Promise<CompanyPage> {
  const query = new URLSearchParams({ limit: '100', includeArchived: 'true' });
  if (cursor) query.set('cursor', cursor);
  return request<CompanyPage>(`/companies?${query}`, { signal });
}

export function listOpportunities(cursor = '', signal?: AbortSignal): Promise<OpportunityPage> {
  const query = new URLSearchParams({ limit: '100', includeArchived: 'true' });
  if (cursor) query.set('cursor', cursor);
  return request<OpportunityPage>(`/opportunities?${query}`, { signal });
}

export function listIngestions(cursor = '', signal?: AbortSignal): Promise<IngestionPage> {
  const query = new URLSearchParams({ limit: '25' });
  if (cursor) query.set('cursor', cursor);
  return request<IngestionPage>(`/ingestions?${query}`, { signal });
}

export function getIngestion(id: string, signal?: AbortSignal): Promise<IngestionRequest> {
  return request<IngestionRequest>(`/ingestions/${encodeURIComponent(id)}`, { signal });
}

export function submitIngestion(
  input: components['schemas']['SubmitIngestionRequest'],
  csrfToken: string,
): Promise<IngestionRequest> {
  return request<IngestionRequest>('/ingestions', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function retryIngestion(
  id: string,
  input: components['schemas']['RetryIngestionRequest'],
  csrfToken: string,
): Promise<IngestionRequest> {
  return request<IngestionRequest>(`/ingestions/${encodeURIComponent(id)}/retry`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function getOpportunity(id: string, signal?: AbortSignal): Promise<OpportunityView> {
  return request<OpportunityView>(`/opportunities/${encodeURIComponent(id)}`, { signal });
}

export function getOpportunityOrganisation(
  id: string,
  signal?: AbortSignal,
): Promise<OrganisationView> {
  return request<OrganisationView>(`/opportunities/${encodeURIComponent(id)}/organisation`, {
    signal,
  });
}

export function getOrganisationSummaries(
  ids: string[],
  signal?: AbortSignal,
): Promise<OrganisationSummary[]> {
  if (ids.length === 0) return Promise.resolve([]);
  const query = new URLSearchParams({ ids: ids.join(',') });
  return request<components['schemas']['OrganisationSummaryList']>(
    `/organisation/summaries?${query}`,
    { signal },
  ).then((page) => page.items);
}

export function listOpportunityChanges(
  cursor = '',
  signal?: AbortSignal,
): Promise<RecordChangePage> {
  const query = new URLSearchParams({ limit: '100', entityKind: 'opportunity' });
  if (cursor) query.set('cursor', cursor);
  else query.set('after', '0');
  return request<RecordChangePage>(`/changes?${query}`, { signal });
}

function evidencePath(id: string): string {
  return `/opportunities/${encodeURIComponent(id)}`;
}

function pageQuery(cursor: string): string {
  const query = new URLSearchParams({ limit: '25' });
  if (cursor) query.set('cursor', cursor);
  return query.toString();
}

export function listEvidenceSources(id: string, cursor = '', signal?: AbortSignal) {
  return request<EvidenceSourcePage>(`${evidencePath(id)}/evidence-sources?${pageQuery(cursor)}`, {
    signal,
  });
}

export function listOfferOptionSets(id: string, signal?: AbortSignal) {
  return request<components['schemas']['OfferOptionSetList']>(
    `${evidencePath(id)}/offer-option-sets`,
    { signal },
  );
}

export function listEvidence(id: string, cursor = '', signal?: AbortSignal) {
  const query = new URLSearchParams(pageQuery(cursor));
  query.set('includeSuperseded', 'true');
  return request<EvidencePage>(`${evidencePath(id)}/evidence?${query}`, { signal });
}

export function getQualification(id: string, signal?: AbortSignal): Promise<QualificationView> {
  return fetch(`/api/v1${evidencePath(id)}/qualification`, {
    credentials: 'same-origin',
    signal,
    headers: { Accept: 'application/json' },
  }).then(async (response) => {
    if (response.ok || response.status === 503) {
      const view = (await response.json()) as QualificationView;
      if (response.status === 503 && view.current !== null) {
        throw new Error('The API returned an invalid refresh response.');
      }
      return view;
    }
    let message = `The API returned HTTP ${response.status}.`;
    try {
      const body = (await response.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch {
      // Network intermediaries may return non-JSON errors.
    }
    throw new RequestError(response.status, message);
  });
}

export function listQualificationHistory(id: string, cursor = '', signal?: AbortSignal) {
  return request<QualificationHistoryPage>(
    `${evidencePath(id)}/qualification/history?${pageQuery(cursor)}`,
    { signal },
  );
}
