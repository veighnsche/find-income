import {
  getApplicationPack,
  getOpportunity,
  getOpportunityOrganisation,
  getOpportunityScreening,
  getOwnerOpportunityDecision,
  listApplicationPacks,
  type Round,
} from './api';

export type HomeAction = 'discover' | 'review_opportunities' | 'prepare' | 'review_pack';
export type HomeRecommendation = {
  status: 'selected' | 'unresolved' | 'unavailable';
  code?: string;
  action?: HomeAction;
  target?: {
    kind: 'campaign' | 'round' | 'opportunity' | 'application_pack';
    id: string;
    revision: number;
    contentSha256?: string;
    opportunityId?: string;
    opportunityRevision?: number;
    ownerDecisionRevision?: number;
  };
  profileVersion: number;
  roundId: string;
  roundGeneration: number;
  reason?: string;
  sourceRefs?: {
    id: string;
    kind: string;
    revision: string;
    omittedBytes: number;
    opportunityRevision?: number;
    ownerDecisionRevision?: number;
    screeningAssessmentId?: string;
    organisationAssessmentId?: string;
    packId?: string;
    packVersion?: number;
    packContentSha256?: string;
  }[];
  unavailableActions?: string[];
  omittedOpportunities?: boolean;
};
export type RecommendationCurrentness = {
  status: 'current' | 'stale' | 'unavailable';
  code: string;
  checkedAt: string;
};

export function readRecommendationCurrentness(
  round: Round | null,
): RecommendationCurrentness | null {
  const value = round?.report.recommendationCurrentness;
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const item = value as Record<string, unknown>;
  if (
    !['current', 'stale', 'unavailable'].includes(String(item.status)) ||
    typeof item.code !== 'string' ||
    typeof item.checkedAt !== 'string'
  )
    return null;
  return item as RecommendationCurrentness;
}

const targetKinds: Record<HomeAction, NonNullable<HomeRecommendation['target']>['kind']> = {
  discover: 'campaign',
  review_opportunities: 'round',
  prepare: 'opportunity',
  review_pack: 'application_pack',
};

export function readHomeRecommendation(round: Round | null): HomeRecommendation | null {
  if (!round || round.outcome !== 'discover' || round.state !== 'completed') return null;
  const value = round.report.recommendation;
  if (!value || typeof value !== 'object' || Array.isArray(value)) return null;
  const item = value as Record<string, unknown>;
  if (
    !['selected', 'unresolved', 'unavailable'].includes(String(item.status)) ||
    !Number.isSafeInteger(item.profileVersion) ||
    typeof item.roundId !== 'string' ||
    !Number.isSafeInteger(item.roundGeneration)
  )
    return null;
  if (item.code !== undefined && typeof item.code !== 'string') return null;
  if (item.reason !== undefined && typeof item.reason !== 'string') return null;
  if (item.omittedOpportunities !== undefined && typeof item.omittedOpportunities !== 'boolean')
    return null;
  if (
    item.unavailableActions !== undefined &&
    (!Array.isArray(item.unavailableActions) ||
      item.unavailableActions.some((value) => typeof value !== 'string'))
  )
    return null;
  if (
    item.sourceRefs !== undefined &&
    (!Array.isArray(item.sourceRefs) ||
      item.sourceRefs.some(
        (value) =>
          !value ||
          typeof value !== 'object' ||
          Array.isArray(value) ||
          typeof value.id !== 'string' ||
          typeof value.kind !== 'string' ||
          typeof value.revision !== 'string' ||
          !Number.isSafeInteger(value.omittedBytes),
      ))
  )
    return null;
  if (item.status === 'selected') {
    if (
      !['discover', 'review_opportunities', 'prepare', 'review_pack'].includes(
        String(item.action),
      ) ||
      !item.target ||
      typeof item.target !== 'object' ||
      Array.isArray(item.target)
    )
      return null;
    const target = item.target as Record<string, unknown>;
    if (
      target.kind !== targetKinds[item.action as HomeAction] ||
      typeof target.id !== 'string' ||
      !Number.isSafeInteger(target.revision)
    )
      return null;
    if (item.action === 'discover' && target.id !== 'active') return null;
    if (item.action === 'review_opportunities' && target.id !== round.id) return null;
    if (item.action === 'prepare' && !Number.isSafeInteger(target.ownerDecisionRevision))
      return null;
    if (
      item.action === 'review_pack' &&
      (typeof target.contentSha256 !== 'string' ||
        typeof target.opportunityId !== 'string' ||
        !Number.isSafeInteger(target.opportunityRevision) ||
        !Number.isSafeInteger(target.ownerDecisionRevision))
    )
      return null;
  }
  if (item.roundId !== round.id || (item.roundGeneration as number) + 1 !== round.generation)
    return null;
  return item as HomeRecommendation;
}

export async function checkRecommendationTarget(
  advice: HomeRecommendation,
  round: Round,
  profileVersion: number,
  signal?: AbortSignal,
): Promise<string | null> {
  if (advice.profileVersion !== profileVersion) return 'The campaign brief has changed.';
  if (advice.roundId !== round.id || advice.roundGeneration + 1 !== round.generation)
    return 'The saved discovery round changed.';
  if (advice.status !== 'selected' || !advice.action || !advice.target) return null;
  const target = advice.target;
  if (
    advice.action === 'discover' &&
    (target.kind !== 'campaign' || target.id !== 'active' || target.revision !== profileVersion)
  )
    return 'The campaign brief no longer matches this advice.';
  if (
    advice.action === 'review_opportunities' &&
    (target.kind !== 'round' ||
      target.id !== round.id ||
      target.revision !== advice.roundGeneration)
  )
    return 'The saved round cards no longer match this advice.';
  if (advice.action === 'prepare' || advice.action === 'review_pack') {
    const opportunityId = advice.action === 'prepare' ? target.id : target.opportunityId!;
    const [view, decision] = await Promise.all([
      getOpportunity(opportunityId, signal),
      getOwnerOpportunityDecision(opportunityId, signal),
    ]);
    const opportunity = view.opportunity;
    const expectedRevision =
      advice.action === 'prepare' ? target.revision : target.opportunityRevision;
    if (
      opportunity.archivedAt ||
      opportunity.revision !== expectedRevision ||
      !decision ||
      decision.decision !== 'selected' ||
      decision.revision !== target.ownerDecisionRevision ||
      decision.opportunityRevision !== opportunity.revision
    )
      return 'The selected role or owner decision has changed.';
    if (advice.action === 'prepare' && (!opportunity.sourceUrl || !opportunity.originalText))
      return 'The role source needed for preparation is no longer complete.';
    if (advice.action === 'review_pack') {
      const [pack, packs] = await Promise.all([
        getApplicationPack(target.id, signal),
        listApplicationPacks(opportunityId, signal),
      ]);
      if (
        packs[0]?.id !== target.id ||
        pack.opportunityId !== opportunityId ||
        pack.version !== target.revision ||
        pack.contentSha256 !== target.contentSha256 ||
        pack.opportunityRevision !== opportunity.revision ||
        pack.profileRevision !== profileVersion
      )
        return 'The saved pack no longer matches this advice.';
    }
  }
  for (const ref of advice.sourceRefs || []) {
    if (ref.kind !== 'current_opportunity_state' || !ref.id.startsWith('opportunity:')) continue;
    const id = ref.id.slice('opportunity:'.length);
    const [view, decision, screening, organisation] = await Promise.all([
      getOpportunity(id, signal),
      getOwnerOpportunityDecision(id, signal),
      getOpportunityScreening(id, signal),
      getOpportunityOrganisation(id, signal),
    ]);
    if (
      view.opportunity.revision !== ref.opportunityRevision ||
      (decision?.revision || 0) !== (ref.ownerDecisionRevision || 0) ||
      (screening.current?.id || '') !== (ref.screeningAssessmentId || '') ||
      (organisation.current?.id || '') !== (ref.organisationAssessmentId || '')
    )
      return 'A saved opportunity source or assessment has changed.';
    const packs = await listApplicationPacks(id, signal);
    const currentPack =
      packs[0]?.opportunityRevision === view.opportunity.revision &&
      packs[0]?.profileRevision === profileVersion
        ? packs[0]
        : null;
    if ((currentPack?.id || '') !== (ref.packId || '')) return 'A saved pack source has changed.';
    if (ref.packId) {
      const pack = await getApplicationPack(ref.packId, signal);
      if (
        pack.version !== ref.packVersion ||
        pack.contentSha256 !== ref.packContentSha256 ||
        pack.opportunityId !== id ||
        pack.profileRevision !== profileVersion
      )
        return 'A saved pack source has changed.';
    }
  }
  return null;
}

export function recommendationExplanation(code: string | undefined): string {
  switch (code) {
    case 'jev_abstained':
      return 'The assessment did not choose a supported next action. Your saved roles and work remain available below.';
    case 'no_assessed_source':
      return 'No assessed source was available for a recommendation. Review any saved roles below or start another search when ready.';
    case 'recommendation_target_changed':
    case 'recommendation_state_changed':
      return 'Saved campaign facts changed while advice was prepared. Review current records before acting.';
    case 'recommendation_allowance_or_scope_unavailable':
    case 'recommendation_allowance_exhausted':
      return 'This commission could not spend a bounded assessment request on next-action advice. Its saved results remain available.';
    case 'recommendation_context_too_large':
      return 'The saved evidence exceeded the bounded assessment context. Review the source results directly.';
    default:
      return 'A supported next action was not saved. You can still review the completed work and choose an explicit action.';
  }
}
