import { describe, expect, it } from 'vitest';
import {
  describeRunState,
  isActiveRunState,
  loadPersistedRunId,
  mergeActivityEvents,
  persistRunId,
  researchRunKey,
  runCounts,
  shouldPollRun,
  type RunReadState,
} from './research-run-state';
import type { ResearchActivityEvent, ResearchRunView } from './api';

function runView(state: ResearchRunView['state']): ResearchRunView {
  return {
    runId: 'run-1',
    state,
    briefVersion: { profileVersion: 3, rubricVersion: 'r2026-09' },
    allowance: { timeMs: 900000, maxActions: 60, maxJev: 12, maxTurns: 8, maxConcurrent: 2 },
    usage: {
      enforced: { timeMs: 900000, maxActions: 60, maxJev: 12, maxTurns: 8, maxConcurrent: 2 },
      reserved: { actions: 2, jev: 1, turns: 1 },
      observed: { actions: 17, jev: 3, turns: 2, bytes: 412090 },
      unknown: true,
    },
    investigations: [{ id: 'inv-1', intent: 'backend roles', status: 'active' }],
    savedIds: ['opp-1'],
    unresolvedCount: 1,
  };
}

function memoryStorage(): Storage {
  const values = new Map<string, string>();
  return {
    getItem: (key: string) => values.get(key) ?? null,
    setItem: (key: string, value: string) => void values.set(key, value),
    removeItem: (key: string) => void values.delete(key),
    clear: () => values.clear(),
    key: (index: number) => [...values.keys()][index] ?? null,
    length: 0,
  };
}

describe('research run polling', () => {
  it.each([
    ['queued', true],
    ['running', true],
    ['awaiting_input', true],
    ['stopping', true],
    ['paused', false],
    ['completed', false],
    ['failed', false],
  ] as const)('state %s polls=%s', (state, expected) => {
    expect(isActiveRunState(state)).toBe(expected);
    const read: RunReadState = { kind: 'ready', run: runView(state) };
    expect(shouldPollRun(read)).toBe(expected);
  });

  it('never polls without a loaded run', () => {
    expect(shouldPollRun({ kind: 'idle' })).toBe(false);
    expect(shouldPollRun({ kind: 'loading' })).toBe(false);
    expect(shouldPollRun({ kind: 'error', error: 'down' })).toBe(false);
  });

  it('keeps polling a stale active run so recovery refetches instead of dispatching', () => {
    const read: RunReadState = { kind: 'stale', run: runView('running'), error: 'timeout' };
    expect(shouldPollRun(read)).toBe(true);
    const terminal: RunReadState = { kind: 'stale', run: runView('completed'), error: 'timeout' };
    expect(shouldPollRun(terminal)).toBe(false);
  });
});

describe('activity merge', () => {
  const event = (id: string, at: string): ResearchActivityEvent => ({
    eventId: id,
    at,
    kind: 'claim',
    summary: id,
  });

  it('dedupes reconnect replays by event id and keeps time order', () => {
    const merged = mergeActivityEvents(
      [event('a', '2026-09-24T10:00:01Z'), event('b', '2026-09-24T10:00:02Z')],
      [event('b', '2026-09-24T10:00:02Z'), event('c', '2026-09-24T10:00:00Z')],
    );
    expect(merged.map((item) => item.eventId)).toEqual(['c', 'a', 'b']);
  });
});

describe('reload recovery', () => {
  it('round-trips the run id and clears it', () => {
    const storage = memoryStorage();
    expect(loadPersistedRunId(storage)).toBeNull();
    persistRunId(storage, 'run-9');
    expect(storage.getItem(researchRunKey)).toBe('run-9');
    expect(loadPersistedRunId(storage)).toBe('run-9');
    persistRunId(storage, null);
    expect(loadPersistedRunId(storage)).toBeNull();
  });

  it('treats blank storage as no run', () => {
    const storage = memoryStorage();
    storage.setItem(researchRunKey, '   ');
    expect(loadPersistedRunId(storage)).toBeNull();
  });
});

describe('counts and labels', () => {
  it('derives counts from the projection without invention', () => {
    expect(runCounts(runView('running'))).toEqual({
      saved: 1,
      unresolved: 1,
      investigations: 1,
      observedActions: 17,
      reservedActions: 2,
      unknownUsage: true,
    });
  });

  it('labels every run state', () => {
    for (const state of [
      'queued',
      'running',
      'awaiting_input',
      'stopping',
      'paused',
      'completed',
      'failed',
    ] as const) {
      expect(describeRunState(state).length).toBeGreaterThan(0);
    }
  });
});
