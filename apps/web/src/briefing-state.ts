export interface BriefingRound {
  state: 'queued' | 'running' | 'awaiting_input' | 'stopping' | 'paused' | 'completed' | 'failed';
  outcome: string;
  reconciliationRequired: boolean;
  unresolved: readonly unknown[];
}

export type WaitingVariant = 'check' | 'resume' | 'stalled' | 'delivery';

export function waitingVariantOf(round: BriefingRound): WaitingVariant {
  if (round.state === 'awaiting_input') return 'stalled';
  if (round.outcome === 'deliver') return 'delivery';
  if (round.reconciliationRequired || round.unresolved.length > 0) return 'check';
  return 'resume';
}

export function outcomeNoun(outcome: string): string {
  switch (outcome) {
    case 'prepare':
      return 'application';
    case 'process_input':
      return 'input';
    case 'compare_offers':
      return 'comparison';
    case 'interview_prepare':
      return 'interview prep';
    case 'interview_debrief':
      return 'debrief';
    case 'deliver':
      return 'delivery';
    case 'process_replies':
      return 'reply';
    default:
      return 'work';
  }
}
