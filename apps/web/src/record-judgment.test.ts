import { describe, expect, it } from 'vitest';
import {
  conflictingDimensions,
  fitStatus,
  fitStatusLabel,
  identityHeadline,
  isMissingIdentity,
  isServiceUnavailable,
  isUnassessedStage,
  openUnknowns,
  sightingSummary,
  UNASSESSED_STAGE,
} from './record-judgment';
import {
  RequestError,
  type QualificationEvaluation,
  type QualificationView,
  type ResearchIdentityView,
} from './api';

function versions(): QualificationView['currentInputVersions'] {
  return {
    opportunityId: 'opp-1',
    opportunityRevision: 2,
    companyId: 'co-1',
    opportunityKind: 'employment',
    materialVersion: 1,
    evidenceVersion: 3,
    contextVersion: 1,
    preferencesVersion: 7,
    rulesVersion: 'r2026-09',
  };
}

function evaluation(): QualificationEvaluation {
  return {
    id: 'eval-1',
    opportunityId: 'opp-1',
    opportunityRevision: 2,
    materialVersion: 1,
    evidenceVersion: 3,
    contextVersion: 1,
    preferencesVersion: 7,
    rulesVersion: 'r2026-09',
    overall: 'unresolved',
    criteria: [
      {
        criterion: 'backend_depth',
        label: 'Backend depth',
        state: 'match',
        reason: 'cited',
        conflicting: false,
      },
      {
        criterion: 'on_call',
        state: 'unknown',
        reason: 'no evidence either way',
        conflicting: false,
      },
      { criterion: 'clearance', state: 'mismatch', reason: 'requires SC', conflicting: true },
    ],
    salary: {
      state: 'unknown',
      reason: 'no pay cited',
      confirmedActual: false,
      conflicting: false,
    },
    sourceRefs: [],
    optionSetStatus: 'none',
    optionSetIds: [],
    optionResults: [],
    createdAt: '2026-09-24T10:00:00Z',
    actorKind: 'owner',
    actorId: 'owner',
  };
}

describe('unassessed stage', () => {
  it('matches the T18 stage label exactly', () => {
    expect(UNASSESSED_STAGE).toBe('unassessed');
    expect(isUnassessedStage({ stage: 'unassessed' })).toBe(true);
    expect(isUnassessedStage({ stage: 'discovered' })).toBe(false);
  });
});

describe('fit status', () => {
  it('is unassessed without a view or evaluation', () => {
    expect(fitStatus(null)).toBe('unassessed');
    expect(
      fitStatus({
        status: 'not_assessed',
        current: null,
        latestHistorical: null,
        currentInputVersions: versions(),
      }),
    ).toBe('unassessed');
  });

  it('is assessed when a current or historical evaluation exists', () => {
    const current = evaluation();
    expect(
      fitStatus({
        status: 'current',
        current,
        latestHistorical: null,
        currentInputVersions: versions(),
      }),
    ).toBe('assessed');
    expect(
      fitStatus({
        status: 'outdated',
        current: null,
        latestHistorical: current,
        currentInputVersions: versions(),
      }),
    ).toBe('assessed');
  });

  it('labels assessed vs unassessed explicitly', () => {
    expect(fitStatusLabel('assessed')).toBe('Assessed');
    expect(fitStatusLabel('unassessed')).toContain('Unassessed');
  });
});

describe('unknowns and conflicts', () => {
  it('lists every dimension as unknown without an evaluation', () => {
    expect(openUnknowns(null)).toEqual(['role fit', 'pay and hours', 'conflicts']);
    expect(conflictingDimensions(null)).toEqual([]);
  });

  it('names only the still-unknown dimensions of an evaluation', () => {
    expect(openUnknowns(evaluation())).toEqual(['on call', 'pay']);
  });

  it('surfaces conflicting dimensions separately from unknowns', () => {
    expect(conflictingDimensions(evaluation())).toEqual(['clearance']);
  });
});

describe('identity explanation', () => {
  it('headlines same/new/unresolved distinctly', () => {
    expect(identityHeadline('same')).toContain('Same record');
    expect(identityHeadline('new')).toContain('New record');
    expect(identityHeadline('unresolved')).toContain('Unresolved');
  });

  it.each([
    [
      {
        subjectId: 'opp-1',
        candidates: [],
        decision: 'new',
        basis: 'no match',
      } satisfies ResearchIdentityView,
      'No research sightings recorded.',
    ],
    [
      {
        subjectId: 'opp-1',
        candidates: [],
        decision: 'same',
        basis: 'matched',
        sightings: [
          {
            captureId: 'c1',
            observedUrl: 'https://example.test/jobs/1',
            observedAt: '2026-09-24T10:00:00Z',
          },
        ],
      } satisfies ResearchIdentityView,
      '1 research sighting recorded.',
    ],
  ])('summarizes sighting counts', (view, expected) => {
    expect(sightingSummary(view)).toBe(expected);
  });
});

describe('identity endpoint states', () => {
  it('distinguishes unwired service from missing record from failure', () => {
    expect(isServiceUnavailable(new RequestError(503, 'unavailable'))).toBe(true);
    expect(isServiceUnavailable(new RequestError(500, 'boom'))).toBe(false);
    expect(isMissingIdentity(new RequestError(404, 'no decision'))).toBe(true);
    expect(isMissingIdentity(new RequestError(500, 'boom'))).toBe(false);
    expect(isServiceUnavailable(new Error('network down'))).toBe(false);
  });
});
