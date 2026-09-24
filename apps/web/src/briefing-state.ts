// Pure mapping from round state to the six human briefing states.
// Every round situation falls into exactly one state; see briefing-state.test.ts.

export type BriefingState = 'fresh' | 'working' | 'waiting' | 'results' | 'empty' | 'failed';

export interface BriefingRound {
  state: 'queued' | 'running' | 'awaiting_input' | 'stopping' | 'paused' | 'completed' | 'failed';
  outcome: string;
  reconciliationRequired: boolean;
  unresolved: readonly unknown[];
}

export function briefingStateOf(round: BriefingRound | null, hasOutput: boolean): BriefingState {
  if (!round) return 'fresh';
  switch (round.state) {
    case 'queued':
    case 'running':
    case 'stopping':
      return 'working';
    case 'awaiting_input':
    case 'paused':
      return 'waiting';
    case 'failed':
      return 'failed';
    case 'completed':
      if (round.outcome === 'discover' && !hasOutput) return 'empty';
      return 'results';
  }
}

export type WaitingVariant = 'check' | 'resume' | 'stalled' | 'delivery';

export function waitingVariantOf(round: BriefingRound): WaitingVariant {
  if (round.state === 'awaiting_input') return 'stalled';
  if (round.outcome === 'deliver') return 'delivery';
  if (round.reconciliationRequired || round.unresolved.length > 0) return 'check';
  return 'resume';
}

export type WorkingPhase = 'starting' | 'stopping' | 'running';

export function workingPhaseOf(
  state: BriefingRound['state'] | null,
  startUnconfirmed: boolean,
): WorkingPhase {
  if (state === 'stopping') return 'stopping';
  if (state === 'queued' || state === null || startUnconfirmed) return 'starting';
  return 'running';
}

export function outcomeNoun(outcome: string): string {
  switch (outcome) {
    case 'discover':
      return 'search';
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
    default:
      return 'work';
  }
}

export type StageStatus = 'done' | 'active' | 'paused' | 'failed' | 'todo';

export interface PipelineStage {
  label: string;
  hint: string;
  status: StageStatus;
}

function plural(count: number, one: string, many: string): string {
  return count === 1 ? one : many;
}

function discoverStages(brief: BriefingState, cardCount: number): PipelineStage[] {
  switch (brief) {
    case 'fresh':
      return [
        {
          label: 'Searching for roles',
          hint: "We'll look across top job boards and company sites.",
          status: 'todo',
        },
        {
          label: 'Reviewing matches',
          hint: "We'll find the best opportunities for you.",
          status: 'todo',
        },
        {
          label: 'Preparing applications',
          hint: "We'll tailor your applications for each role.",
          status: 'todo',
        },
        {
          label: 'Interviews',
          hint: "We'll help you prepare and keep track.",
          status: 'todo',
        },
      ];
    case 'working':
      return [
        {
          label: 'Searching for roles',
          hint: 'Scanning job boards and company sites…',
          status: 'active',
        },
        {
          label: 'Reviewing matches',
          hint: 'Finding the best opportunities for you.',
          status: 'todo',
        },
        { label: 'Preparing applications', hint: 'Waiting for results.', status: 'todo' },
        { label: 'Interviews', hint: 'Waiting for results.', status: 'todo' },
      ];
    case 'waiting':
      return [
        {
          label: 'Searching for roles',
          hint: 'Found potential opportunities.',
          status: 'done',
        },
        {
          label: 'Reviewing matches',
          hint: 'Waiting for your decision.',
          status: 'paused',
        },
        { label: 'Preparing applications', hint: 'Not started yet.', status: 'todo' },
        { label: 'Interviews', hint: 'Not started yet.', status: 'todo' },
      ];
    case 'results':
      return [
        {
          label: 'Searching for roles',
          hint: 'Searched multiple sources.',
          status: 'done',
        },
        {
          label: 'Reviewing matches',
          hint: `Found ${cardCount} good ${plural(cardCount, 'match', 'matches')}.`,
          status: 'done',
        },
        {
          label: 'Preparing applications',
          hint: 'Waiting for your selection.',
          status: 'todo',
        },
        { label: 'Interviews', hint: 'Not started yet.', status: 'todo' },
      ];
    case 'empty':
      return [
        {
          label: 'Searching for roles',
          hint: 'We looked across top job boards and company sites.',
          status: 'done',
        },
        {
          label: 'Reviewing matches',
          hint: 'We screened roles against your preferences.',
          status: 'done',
        },
        {
          label: 'Preparing applications',
          hint: "No roles met our bar, so we didn't prepare any applications.",
          status: 'todo',
        },
        { label: 'Interviews', hint: 'None at this time.', status: 'todo' },
      ];
    case 'failed':
      return [
        { label: 'Searching for roles', hint: 'Started searching.', status: 'done' },
        {
          label: 'Reviewing matches',
          hint: 'Stopped due to an issue.',
          status: 'failed',
        },
        { label: 'Preparing applications', hint: 'Not completed.', status: 'todo' },
        { label: 'Interviews', hint: 'Not completed.', status: 'todo' },
      ];
  }
}

function stageIndexForOutcome(outcome: string): number {
  if (outcome === 'interview_prepare' || outcome === 'interview_debrief') return 3;
  return 2;
}

function reviewWordForOutcome(outcome: string): string {
  switch (outcome) {
    case 'prepare':
      return 'Pack ready for review.';
    case 'compare_offers':
      return 'Ready to review.';
    case 'interview_prepare':
      return 'Brief ready.';
    case 'interview_debrief':
      return 'Debrief ready.';
    case 'deliver':
      return 'Finished — review needed.';
    default:
      return 'Handled — review below.';
  }
}

function otherStages(brief: BriefingState, outcome: string): PipelineStage[] {
  const labels = [
    'Searching for roles',
    'Reviewing matches',
    'Preparing applications',
    'Interviews',
  ];
  const at = stageIndexForOutcome(outcome);
  return labels.map((label, index): PipelineStage => {
    if (index < at) return { label, hint: 'Done earlier.', status: 'done' };
    if (index > at) return { label, hint: 'Not started yet.', status: 'todo' };
    switch (brief) {
      case 'working':
        return { label, hint: 'In progress.', status: 'active' };
      case 'waiting':
        return { label, hint: 'Paused.', status: 'paused' };
      case 'results':
        return { label, hint: reviewWordForOutcome(outcome), status: 'done' };
      case 'failed':
        return { label, hint: 'Stopped due to an issue.', status: 'failed' };
      default:
        return { label, hint: 'Not started yet.', status: 'todo' };
    }
  });
}

export function pipelineStages(
  brief: BriefingState,
  outcome: string,
  cardCount: number,
): PipelineStage[] {
  if (brief === 'fresh' || outcome === 'discover') return discoverStages(brief, cardCount);
  return otherStages(brief, outcome);
}

export interface BriefingLead {
  kind: string;
  status: string;
}

export function jobLeadsAllSaved(leads: readonly BriefingLead[]): boolean {
  const jobs = leads.filter((lead) => lead.kind === 'job');
  return jobs.length > 0 && jobs.every((lead) => lead.status === 'saved');
}

export function briefingTip(brief: BriefingState): string {
  switch (brief) {
    case 'fresh':
      return "The more details you share about what you're looking for, the better the results.";
    case 'working':
      return 'Searches usually take a few minutes. You can leave this page open — the status refreshes on its own.';
    case 'waiting':
      return 'Take your time — progress and allowance are saved until you resume.';
    case 'results':
      return "You don't have to apply to everything — pick the roles that feel right for you.";
    case 'empty':
      return 'Some periods are quieter than others. You can broaden your search (for example, more locations or related roles) to surface more opportunities.';
    case 'failed':
      return "This doesn't happen often. If it keeps happening, try again later — your saved work stays put.";
  }
}
