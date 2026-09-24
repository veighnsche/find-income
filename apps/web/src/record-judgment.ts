import {
  RequestError,
  type Opportunity,
  type QualificationEvaluation,
  type QualificationView,
  type ResearchIdentityView,
} from './api';

// T18 saves unassessed research roles with stage 'unassessed' and no
// qualification evaluation, so nothing presents them as qualified.
export const UNASSESSED_STAGE = 'unassessed';

export function isUnassessedStage(opportunity: Pick<Opportunity, 'stage'>): boolean {
  return opportunity.stage === UNASSESSED_STAGE;
}

export type FitStatus = 'assessed' | 'unassessed';

// An explicit fit label: assessed only when a saved evaluation exists.
// 'outdated' with a saved evaluation is still assessed (stale, not missing).
export function fitStatus(view: QualificationView | null): FitStatus {
  if (!view || view.status === 'not_assessed') return 'unassessed';
  if (view.current || view.latestHistorical) return 'assessed';
  return 'unassessed';
}

export function fitStatusLabel(status: FitStatus): string {
  return status === 'assessed' ? 'Assessed' : 'Unassessed — fit not yet judged';
}

// Dimensions with no usable evidence. Without an evaluation everything is
// unknown; with one, only the dimensions still in state 'unknown'.
export function openUnknowns(evaluation: QualificationEvaluation | null): string[] {
  if (!evaluation) return ['role fit', 'pay and hours', 'conflicts'];
  const unknowns = evaluation.criteria
    .filter((criterion) => criterion.state === 'unknown')
    .map((criterion) => criterion.label || criterion.criterion.replaceAll('_', ' '));
  if (evaluation.salary.state === 'unknown') unknowns.push('pay');
  return unknowns;
}

// Dimensions where saved evidence disagrees with itself. Reported alongside
// unknowns so a conflict is never hidden behind an aggregate verdict.
export function conflictingDimensions(evaluation: QualificationEvaluation | null): string[] {
  if (!evaluation) return [];
  const conflicts = evaluation.criteria
    .filter((criterion) => criterion.conflicting)
    .map((criterion) => criterion.label || criterion.criterion.replaceAll('_', ' '));
  if (evaluation.salary.conflicting) conflicts.push('pay');
  return conflicts;
}

export function identityHeadline(decision: ResearchIdentityView['decision']): string {
  switch (decision) {
    case 'same':
      return 'Same record — matched to a saved role';
    case 'new':
      return 'New record — no saved role matched';
    case 'unresolved':
      return 'Unresolved — needs owner judgment';
  }
}

export function sightingSummary(view: ResearchIdentityView): string {
  const count = view.sightings?.length ?? 0;
  if (count === 0) return 'No research sightings recorded.';
  return `${count} research sighting${count === 1 ? '' : 's'} recorded.`;
}

// GET /research/identity returns 503 until T23 wires the real supervisor
// (T10 contract: unwired service is "not connected yet", never fake data)
// and 404 for records with no identity decision (hand-saved or pre-research).
export function isServiceUnavailable(cause: unknown): boolean {
  return cause instanceof RequestError && cause.status === 503;
}

export function isMissingIdentity(cause: unknown): boolean {
  return cause instanceof RequestError && cause.status === 404;
}
