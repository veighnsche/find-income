import { describe, expect, it } from 'vitest';
import { outcomeNoun, waitingVariantOf, type BriefingRound } from './briefing-state';

const round: BriefingRound = {
  state: 'paused',
  outcome: 'prepare',
  reconciliationRequired: false,
  unresolved: [],
};

describe('paused work and outcomes', () => {
  it('identifies reconciliation and delivery recovery', () => {
    expect(waitingVariantOf(round)).toBe('resume');
    expect(waitingVariantOf({ ...round, reconciliationRequired: true })).toBe('check');
    expect(waitingVariantOf({ ...round, unresolved: ['uncertain'] })).toBe('check');
    expect(waitingVariantOf({ ...round, outcome: 'deliver' })).toBe('delivery');
    expect(waitingVariantOf({ ...round, state: 'awaiting_input' })).toBe('stalled');
  });

  it('names supported work without promising search', () => {
    expect(outcomeNoun('prepare')).toBe('application');
    expect(outcomeNoun('process_replies')).toBe('reply');
    expect(outcomeNoun('unknown')).toBe('work');
  });
});
