import type { ResearchActivityEvent, ResearchRunView } from './api';

// Pure run-UI state helpers (T15). Reads never dispatch work: polling and
// reload recovery only refetch persisted projections. No phase progress is
// derived here; counts come straight from the run view.

export const researchRunKey = 'jobseek.research-run-id';
export const researchPollIntervalMs = 5000;

const activeRunStates: ReadonlySet<ResearchRunView['state']> = new Set([
  'queued',
  'running',
  'awaiting_input',
  'stopping',
]);

export type RunReadState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'ready'; run: ResearchRunView }
  | { kind: 'stale'; run: ResearchRunView; error: string }
  | { kind: 'error'; error: string };

export function isActiveRunState(state: ResearchRunView['state']): boolean {
  return activeRunStates.has(state);
}

// shouldPollRun decides read-only refetching. It returns true only for
// active runs; terminal, missing, and errored reads never poll, and no read
// path commissions work.
export function shouldPollRun(read: RunReadState): boolean {
  if (read.kind !== 'ready' && read.kind !== 'stale') return false;
  return isActiveRunState(read.run.state);
}

export function mergeActivityEvents(
  existing: ResearchActivityEvent[],
  incoming: ResearchActivityEvent[],
): ResearchActivityEvent[] {
  const seen = new Set(existing.map((event) => event.eventId));
  const merged = [...existing];
  for (const event of incoming) {
    if (!seen.has(event.eventId)) {
      seen.add(event.eventId);
      merged.push(event);
    }
  }
  return merged.sort((a, b) => a.at.localeCompare(b.at));
}

export function loadPersistedRunId(storage: Pick<Storage, 'getItem'>): string | null {
  try {
    const value = storage.getItem(researchRunKey);
    return value && value.trim() !== '' ? value : null;
  } catch {
    return null;
  }
}

export function persistRunId(
  storage: Pick<Storage, 'setItem' | 'removeItem'>,
  runId: string | null,
): void {
  try {
    if (runId) storage.setItem(researchRunKey, runId);
    else storage.removeItem(researchRunKey);
  } catch {
    // Private-mode storage failures must not break the run view.
  }
}

export interface RunCounts {
  saved: number;
  unresolved: number;
  investigations: number;
  observedActions: number;
  reservedActions: number;
  unknownUsage: boolean;
}

// runCounts derives truthful counts from the persisted projection only.
export function runCounts(run: ResearchRunView): RunCounts {
  return {
    saved: run.savedIds.length,
    unresolved: run.unresolvedCount,
    investigations: run.investigations.length,
    observedActions: run.usage.observed.actions,
    reservedActions: run.usage.reserved.actions,
    unknownUsage: run.usage.unknown,
  };
}

export function describeRunState(state: ResearchRunView['state']): string {
  switch (state) {
    case 'queued':
      return 'Queued — work has not started.';
    case 'running':
      return 'Running — research is underway.';
    case 'awaiting_input':
      return 'Awaiting input — the run needs an owner decision.';
    case 'stopping':
      return 'Stopping — new work is fenced while execution settles.';
    case 'paused':
      return 'Paused — resume continues with the remaining allowance.';
    case 'completed':
      return 'Completed — outcomes and report are saved.';
    case 'failed':
      return 'Failed — see the report for what is known.';
  }
}
