import type { components, paths } from '@jobseek/contracts';

export type Health = paths['/health']['get']['responses'][200]['content']['application/json'];
export type Session = components['schemas']['SessionResponse'];
export type Preferences = components['schemas']['PreferencesResponse'];
export type PreferencesInput = components['schemas']['PreferencesInput'];
export type RoleCriterion = components['schemas']['RoleCriterion'];
export type AgentCredential = components['schemas']['AgentCredential'];
export type CreatedAgentCredential = components['schemas']['CreatedAgentCredential'];
export type Company = components['schemas']['Company'];
export type CompanyView = components['schemas']['CompanyView'];
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
export type OrganisationCategorySet = components['schemas']['OrganisationCategorySet'];
export type OrganisationView = components['schemas']['OrganisationView'];
export type OrganisationSummary = components['schemas']['OrganisationSummary'];
export type CollectorBoard = components['schemas']['CollectorBoard'];
export type RecordChange = components['schemas']['RecordChange'];
export type RecordChangePage = components['schemas']['RecordChangePage'];
export type CreateCompanyRequest = components['schemas']['CreateCompanyRequest'];
export type CreateOpportunityRequest = components['schemas']['CreateOpportunityRequest'];
export type PatchOpportunityRequest = components['schemas']['PatchOpportunityRequest'];
export type EvidenceSource = components['schemas']['EvidenceSource'];
export type EvidenceClaim = components['schemas']['EvidenceClaim'];
export type QualificationView = components['schemas']['QualificationView'];
export type QualificationEvaluation = components['schemas']['QualificationEvaluation'];
export type QualificationInputVersions = components['schemas']['QualificationInputVersions'];
export type CreateEvidenceSourceRequest = components['schemas']['CreateEvidenceSourceRequest'];
export type WriteEvidenceRequest = components['schemas']['WriteEvidenceRequest'];
export type EvidenceSourcePage = components['schemas']['EvidenceSourcePage'];
export type EvidencePage = components['schemas']['EvidencePage'];
export type QualificationHistoryPage = components['schemas']['QualificationHistoryPage'];
export type OfferOptionSet = components['schemas']['OfferOptionSet'];
export type CreateOfferOptionSetRequest = components['schemas']['CreateOfferOptionSetRequest'];

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

export function listCollectorBoards(signal?: AbortSignal): Promise<CollectorBoard[]> {
  return request<components['schemas']['CollectorBoardList']>('/collector-boards', { signal }).then(
    (page) => page.items,
  );
}

export function createCollectorBoard(
  input: components['schemas']['CreateCollectorBoardRequest'],
  csrfToken: string,
): Promise<CollectorBoard> {
  return request<CollectorBoard>('/collector-boards', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function updateCollectorBoard(
  id: string,
  input: components['schemas']['UpdateCollectorBoardRequest'],
  csrfToken: string,
): Promise<CollectorBoard> {
  return request<CollectorBoard>(`/collector-boards/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function getOrganisationCategories(signal?: AbortSignal): Promise<OrganisationCategorySet> {
  return request<OrganisationCategorySet>('/organisation/categories', { signal });
}

export function updateOrganisationCategories(
  expectedVersion: number,
  categories: OrganisationCategory[],
  csrfToken: string,
): Promise<OrganisationCategorySet> {
  return request<OrganisationCategorySet>('/organisation/categories', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ expectedVersion, categories }),
  });
}

export function updatePreferences(
  input: PreferencesInput,
  expectedVersion: number,
  csrfToken: string,
) {
  return request<components['schemas']['PreferencesMutation']>('/preferences', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ ...input, expectedVersion }),
  });
}

export async function listAgentCredentials(signal?: AbortSignal): Promise<AgentCredential[]> {
  const result = await request<components['schemas']['AgentCredentialList']>('/agent-credentials', {
    signal,
  });
  return result.items;
}

export function createAgentCredential(
  name: string,
  scopes: string[],
  expiresAt: string,
  csrfToken: string,
): Promise<CreatedAgentCredential> {
  return request<CreatedAgentCredential>('/agent-credentials', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify({ name, scopes, expiresAt }),
  });
}

export function revokeAgentCredential(id: string, csrfToken: string): Promise<void> {
  return request<void>(`/agent-credentials/${encodeURIComponent(id)}/revoke`, {
    method: 'POST',
    headers: { 'X-CSRF-Token': csrfToken },
  });
}

export function listCompanies(cursor = '', signal?: AbortSignal): Promise<CompanyPage> {
  const query = new URLSearchParams({ limit: '100', includeArchived: 'true' });
  if (cursor) query.set('cursor', cursor);
  return request<CompanyPage>(`/companies?${query}`, { signal });
}

export function createCompany(input: CreateCompanyRequest, csrfToken: string) {
  return request<components['schemas']['CompanyMutation']>('/companies', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
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

export function createOpportunity(input: CreateOpportunityRequest, csrfToken: string) {
  return request<components['schemas']['OpportunityMutation']>('/opportunities', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function patchOpportunity(id: string, input: PatchOpportunityRequest, csrfToken: string) {
  return request<components['schemas']['OpportunityMutation']>(
    `/opportunities/${encodeURIComponent(id)}`,
    {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify(input),
    },
  );
}

export function archiveOpportunity(id: string, expectedRevision: number, csrfToken: string) {
  return request<components['schemas']['OpportunityMutation']>(
    `/opportunities/${encodeURIComponent(id)}/archive`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify({ expectedRevision }),
    },
  );
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

export function createOfferOptionSet(
  id: string,
  input: CreateOfferOptionSetRequest,
  csrfToken: string,
  signal?: AbortSignal,
) {
  return request<components['schemas']['OfferOptionSetMutation']>(
    `${evidencePath(id)}/offer-option-sets`,
    {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify(input),
      signal,
    },
  );
}

export function getEvidenceSource(id: string, sourceId: string, signal?: AbortSignal) {
  return request<EvidenceSource>(
    `${evidencePath(id)}/evidence-sources/${encodeURIComponent(sourceId)}`,
    { signal },
  );
}

export function createEvidenceSource(
  id: string,
  input: CreateEvidenceSourceRequest,
  csrfToken: string,
  signal?: AbortSignal,
) {
  return request<components['schemas']['EvidenceSourceMutation']>(
    `${evidencePath(id)}/evidence-sources`,
    {
      method: 'POST',
      signal,
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
      body: JSON.stringify(input),
    },
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

export function createEvidence(
  id: string,
  input: WriteEvidenceRequest,
  csrfToken: string,
  priorId?: string,
  signal?: AbortSignal,
) {
  const path = priorId
    ? `${evidencePath(id)}/evidence/${encodeURIComponent(priorId)}/supersede`
    : `${evidencePath(id)}/evidence`;
  return request<components['schemas']['EvidenceMutation']>(path, {
    method: 'POST',
    signal,
    headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken },
    body: JSON.stringify(input),
  });
}

export function reevaluateQualification(id: string, csrfToken: string, signal?: AbortSignal) {
  return request<components['schemas']['QualificationMutation']>(
    `${evidencePath(id)}/qualification/reevaluate`,
    { method: 'POST', signal, headers: { 'X-CSRF-Token': csrfToken } },
  );
}
