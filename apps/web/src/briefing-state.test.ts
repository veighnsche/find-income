import { describe, expect, it } from 'vitest';
import {
  briefingStateOf,
  briefingTip,
  jobLeadsAllSaved,
  outcomeNoun,
  pipelineStages,
  waitingVariantOf,
  workingPhaseOf,
  type BriefingRound,
  type BriefingState,
} from './briefing-state';

function round(
  state: BriefingRound['state'],
  outcome = 'discover',
  extra: Partial<BriefingRound> = {},
): BriefingRound {
  return { state, outcome, reconciliationRequired: false, unresolved: [], ...extra };
}

const allStates: BriefingRound['state'][] = [
  'queued',
  'running',
  'awaiting_input',
  'stopping',
  'paused',
  'completed',
  'failed',
];

describe('briefingStateOf', () => {
  it('maps no round to fresh', () => {
    expect(briefingStateOf(null, false)).toBe('fresh');
    expect(briefingStateOf(null, true)).toBe('fresh');
  });

  it('maps every machine state to exactly one briefing state', () => {
    for (const state of allStates) {
      for (const outcome of ['discover', 'prepare']) {
        for (const hasOutput of [false, true]) {
          const brief = briefingStateOf(round(state, outcome), hasOutput);
          expect(
            ['fresh', 'working', 'waiting', 'results', 'empty', 'failed'],
            `${state}/${outcome}/${hasOutput}`,
          ).toContain(brief);
        }
      }
    }
  });

  it('maps active progress to working', () => {
    for (const state of ['queued', 'running', 'stopping'] as const) {
      expect(briefingStateOf(round(state), false)).toBe('working');
    }
  });

  it('maps stalled states to waiting', () => {
    expect(briefingStateOf(round('awaiting_input'), false)).toBe('waiting');
    expect(briefingStateOf(round('paused'), false)).toBe('waiting');
  });

  it('maps failure to failed', () => {
    expect(briefingStateOf(round('failed'), false)).toBe('failed');
    expect(briefingStateOf(round('failed'), true)).toBe('failed');
  });

  it('splits completed discovery by output', () => {
    expect(briefingStateOf(round('completed', 'discover'), false)).toBe('empty');
    expect(briefingStateOf(round('completed', 'discover'), true)).toBe('results');
  });

  it('maps completed non-discovery work to results', () => {
    for (const outcome of ['prepare', 'process_input', 'compare_offers', 'deliver']) {
      expect(briefingStateOf(round('completed', outcome), false)).toBe('results');
    }
  });
});

describe('waitingVariantOf', () => {
  it('distinguishes the four waiting situations', () => {
    expect(waitingVariantOf(round('awaiting_input'))).toBe('stalled');
    expect(waitingVariantOf(round('paused', 'deliver'))).toBe('delivery');
    expect(waitingVariantOf(round('paused', 'discover'))).toBe('resume');
    expect(waitingVariantOf(round('paused', 'discover', { reconciliationRequired: true }))).toBe(
      'check',
    );
    expect(waitingVariantOf(round('paused', 'discover', { unresolved: ['x'] }))).toBe('check');
  });
});

describe('workingPhaseOf', () => {
  it('names starting, stopping, and running', () => {
    expect(workingPhaseOf('queued', false)).toBe('starting');
    expect(workingPhaseOf(null, true)).toBe('starting');
    expect(workingPhaseOf('running', true)).toBe('starting');
    expect(workingPhaseOf('stopping', false)).toBe('stopping');
    expect(workingPhaseOf('running', false)).toBe('running');
  });
});

describe('outcomeNoun', () => {
  it('falls back to work for unknown outcomes', () => {
    expect(outcomeNoun('discover')).toBe('search');
    expect(outcomeNoun('prepare')).toBe('application');
    expect(outcomeNoun('something_new')).toBe('work');
  });
});

describe('pipeline and tips', () => {
  it('renders four stages for every briefing state', () => {
    const briefs: BriefingState[] = ['fresh', 'working', 'waiting', 'results', 'empty', 'failed'];
    for (const brief of briefs) {
      for (const outcome of ['discover', 'prepare', 'interview_prepare']) {
        const stages = pipelineStages(brief, outcome, 3);
        expect(stages).toHaveLength(4);
        for (const stage of stages) {
          expect(stage.label.length).toBeGreaterThan(0);
          expect(stage.hint.length).toBeGreaterThan(0);
        }
      }
    }
  });

  it('has a tip for every briefing state', () => {
    const briefs: BriefingState[] = ['fresh', 'working', 'waiting', 'results', 'empty', 'failed'];
    for (const brief of briefs) {
      expect(briefingTip(brief).length).toBeGreaterThan(0);
    }
  });
});

describe('jobLeadsAllSaved', () => {
  it('detects a re-search that found only already-saved roles', () => {
    expect(jobLeadsAllSaved([])).toBe(false);
    expect(jobLeadsAllSaved([{ kind: 'company', status: 'saved' }])).toBe(false);
    expect(
      jobLeadsAllSaved([
        { kind: 'job', status: 'saved' },
        { kind: 'job', status: 'staged' },
      ]),
    ).toBe(false);
    expect(jobLeadsAllSaved([{ kind: 'job', status: 'saved' }])).toBe(true);
    expect(
      jobLeadsAllSaved([
        { kind: 'job', status: 'saved' },
        { kind: 'company', status: 'staged' },
      ]),
    ).toBe(true);
  });
});
