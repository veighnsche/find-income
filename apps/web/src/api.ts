import type { paths } from '@jobseek/contracts';

export type Health = paths['/health']['get']['responses'][200]['content']['application/json'];

export async function getHealth(signal?: AbortSignal): Promise<Health> {
  const response = await fetch('/api/v1/health', {
    credentials: 'same-origin',
    signal,
    headers: { Accept: 'application/json' },
  });
  if (!response.ok) throw new Error(`The API returned HTTP ${response.status}.`);
  const data: unknown = await response.json();
  if (!data || typeof data !== 'object' || !('status' in data) || data.status !== 'ok') {
    throw new Error('The API returned an unexpected health response.');
  }
  return data as Health;
}
