import type { components, paths } from '@jobseek/contracts';

export type Health = paths['/health']['get']['responses'][200]['content']['application/json'];
export type Session = components['schemas']['SessionResponse'];
export type Preferences = components['schemas']['PreferencesResponse'];
export type AgentCredential = components['schemas']['AgentCredential'];
export type CreatedAgentCredential = components['schemas']['CreatedAgentCredential'];

export class RequestError extends Error {
  constructor(
    readonly status: number,
    message: string,
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
    try {
      const body = (await response.json()) as { error?: { message?: string } };
      if (body.error?.message) message = body.error.message;
    } catch {
      // An unavailable server may not return a JSON envelope.
    }
    throw new RequestError(response.status, message);
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
