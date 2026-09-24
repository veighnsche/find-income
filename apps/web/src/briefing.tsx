import type { ReactNode } from 'react';
import type { Round, RoundCandidateLead, RoundCandidateSearch, RoundCapability } from './api';
import {
  briefingStateOf,
  briefingTip,
  jobLeadsAllSaved,
  outcomeNoun,
  pipelineStages,
  waitingVariantOf,
  workingPhaseOf,
  type BriefingState,
} from './briefing-state';
import './briefing.css';

export interface BriefingAdvice {
  label: string;
  available: boolean;
  busy: boolean;
}

interface BriefingAction {
  label: string;
  onSelect: () => void;
  disabled?: boolean;
  note?: string;
}

export interface BriefingProps {
  round: Round | null;
  capability: RoundCapability | null;
  hasOutput: boolean;
  cardCount: number;
  leads: RoundCandidateLead[];
  searches: RoundCandidateSearch[];
  reportSummary: string;
  advice: BriefingAdvice | null;
  adviceCode?: string;
  loading: boolean;
  busy: boolean;
  error: string | null;
  actionError: string | null;
  adviceError: string;
  startUnconfirmed: boolean;
  staleStart: boolean;
  pollSeq: number;
  pollingActive: boolean;
  pollIntervalMs: number;
  onRefresh: () => void;
  onStart: () => void;
  onStop: () => void;
  onResume: () => void;
  onReviewRejected: () => void;
  onFollowAdvice: () => void;
  onEditBrief: () => void;
  onShowResult: () => void;
  onShowCards: () => void;
  onShowComparison: () => void;
  onShowPreparation: () => void;
  onShowLeads: () => void;
  deliveryRecovery: ReactNode;
}

function sourceName(host: string): string {
  const bare = host.startsWith('mcp.') ? host.slice(4) : host;
  const first = bare.split('.')[0] || bare;
  return first.charAt(0).toUpperCase() + first.slice(1);
}

function methodName(method: string): string {
  if (method === 'search_jobs') return 'job search';
  if (method === 'get_company_details') return 'company check';
  return method.replaceAll('_', ' ');
}

function checkedSummary(searches: RoundCandidateSearch[], leadCount: number): string {
  if (searches.length === 0) return '';
  const sources = [...new Set(searches.map((read) => sourceName(read.source)))].join(', ');
  const ok = searches.filter((read) => read.succeeded).length;
  const failed = searches.length - ok;
  const reads =
    failed > 0
      ? `${searches.length} ${searches.length === 1 ? 'read' : 'reads'} (${failed} failed)`
      : `${searches.length} ${searches.length === 1 ? 'read' : 'reads'}`;
  return `${sources} · ${reads} · ${leadCount} ${leadCount === 1 ? 'lead' : 'leads'}`;
}

function leadStatusLabel(status: string): string {
  switch (status) {
    case 'saved':
      return 'Saved as opportunity';
    case 'verified':
      return 'Employer verified';
    case 'checked':
      return "Checked — couldn't verify automatically";
    default:
      return 'Found, not checked yet';
  }
}

function StateIcon({ brief }: { brief: BriefingState }) {
  const common = {
    width: 44,
    height: 44,
    viewBox: '0 0 24 24',
    fill: 'none',
    stroke: 'currentColor',
    strokeWidth: 1.8,
    strokeLinecap: 'round' as const,
    strokeLinejoin: 'round' as const,
    'aria-hidden': true,
  };
  switch (brief) {
    case 'fresh':
      return (
        <svg {...common}>
          <circle cx="11" cy="11" r="6" />
          <path d="M15.5 15.5 20 20" />
        </svg>
      );
    case 'working':
      return (
        <svg {...common}>
          <path d="M20 12a8 8 0 1 1-2.34-5.66" />
          <path d="M20 3v4h-4" />
        </svg>
      );
    case 'waiting':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="8.5" />
          <path d="M9.5 9.5a2.5 2.5 0 1 1 3.6 2.24c-.8.4-1.1.9-1.1 1.76" />
          <circle cx="12" cy="16.8" r="0.4" fill="currentColor" />
        </svg>
      );
    case 'results':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="8.5" />
          <path d="M8.5 12.5l2.5 2.5 4.5-5.5" />
        </svg>
      );
    case 'empty':
      return (
        <svg {...common}>
          <circle cx="12" cy="12" r="8.5" />
          <path d="M8.5 12h7" />
        </svg>
      );
    case 'failed':
      return (
        <svg {...common}>
          <path d="M12 3.5 21 20H3z" />
          <path d="M12 9.5v4.5" />
          <circle cx="12" cy="16.8" r="0.4" fill="currentColor" />
        </svg>
      );
  }
}

interface BriefingCopy {
  eyebrow: string;
  title: string;
  body: string[];
  calloutTitle?: string;
  calloutBody?: string;
  cardTitle: string;
  cardBody?: string;
  primary: BriefingAction | null;
  secondary: BriefingAction[];
}

export function Briefing(props: BriefingProps) {
  const {
    round,
    capability,
    hasOutput,
    cardCount,
    adviceCode,
    loading,
    busy,
    error,
    actionError,
    adviceError,
    startUnconfirmed,
    pollSeq,
    pollingActive,
    pollIntervalMs,
  } = props;
  const outcome = round?.outcome ?? capability?.outcome ?? 'discover';
  const noun = outcomeNoun(outcome);
  const canStart = capability?.canStart === true;
  const canReplacePaused =
    canStart || (round?.state === 'paused' && capability?.reason === 'round_active');
  const unavailable =
    capability !== null && !capability.canStart && capability.reason !== 'round_active';

  const showStarting = startUnconfirmed && (!round || round.state === 'queued');
  const showRetry =
    startUnconfirmed &&
    round !== null &&
    (round.state === 'paused' || round.state === 'completed' || round.state === 'failed');
  let brief = briefingStateOf(round, hasOutput);
  if (showStarting) brief = 'working';

  const copy = briefingCopy(props, brief, outcome, noun, canStart, canReplacePaused);
  const stages = pipelineStages(brief, outcome, cardCount);
  const tip = briefingTip(brief);
  const stoppedEarly = brief === 'empty' && round !== null && round.used.turns < round.limits.turns;

  return (
    <section className="op-card agency-lead briefing" aria-label="Your briefing">
      <div className="op-heading-row">
        <div>
          <p className="eyebrow">Your recruitment agency</p>
        </div>
        <button
          className="secondary"
          type="button"
          disabled={loading || busy}
          onClick={props.onRefresh}
        >
          Refresh work
        </button>
      </div>
      {unavailable && (
        <p role="status" className="brief-banner">
          <strong>Job search is currently unavailable.</strong> You can set everything up now and
          start a search in a little while. Saved work below remains readable.
        </p>
      )}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {adviceError && (
        <p role="alert" className="error">
          {adviceError}
        </p>
      )}
      {loading && !round ? (
        <p role="status">Loading your campaign and saved work…</p>
      ) : (
        <>
          <div className="brief-hero">
            <div className={`brief-icon brief-icon-${brief}`} aria-hidden="true">
              <StateIcon brief={brief} />
            </div>
            <div className="brief-main">
              <p className="eyebrow">{copy.eyebrow}</p>
              <h2>{copy.title}</h2>
              {copy.body.map((line, index) => (
                <p key={index}>{line}</p>
              ))}
              {stoppedEarly && copy.calloutTitle && (
                <div className="brief-callout">
                  <p>
                    <strong>{copy.calloutTitle}</strong>
                    <br />
                    {copy.calloutBody}
                  </p>
                </div>
              )}
            </div>
            <aside className="brief-actions" aria-label="Next step">
              <h3>{copy.cardTitle}</h3>
              {copy.cardBody && <p>{copy.cardBody}</p>}
              {busy ? (
                <p role="status">Working on it…</p>
              ) : (
                <>
                  {copy.primary && (
                    <div>
                      <button
                        type="button"
                        disabled={copy.primary.disabled}
                        onClick={copy.primary.onSelect}
                      >
                        {copy.primary.label}
                      </button>
                      {copy.primary.note && <p className="hint">{copy.primary.note}</p>}
                    </div>
                  )}
                  {copy.secondary.map((action) => (
                    <div key={action.label}>
                      <button
                        type="button"
                        className="secondary brief-row"
                        disabled={action.disabled}
                        onClick={action.onSelect}
                      >
                        {action.label}
                      </button>
                      {action.note && <p className="hint">{action.note}</p>}
                    </div>
                  ))}
                  {!copy.primary && copy.secondary.length === 0 && (
                    <p>Nothing needed from you right now.</p>
                  )}
                </>
              )}
              {brief === 'waiting' &&
                round?.state === 'paused' &&
                round.outcome === 'deliver' &&
                props.deliveryRecovery}
            </aside>
          </div>
          <div className="brief-pipeline">
            <h3>Your job search pipeline</h3>
            <ol>
              {stages.map((stage) => (
                <li key={stage.label} className={`brief-stage-${stage.status}`}>
                  {pollingActive && (stage.status === 'active' || stage.status === 'paused') ? (
                    <svg
                      key={pollSeq}
                      className="brief-dot brief-dot-live"
                      viewBox="0 0 20 20"
                      role="img"
                      aria-label={`Refreshing every ${Math.round(pollIntervalMs / 1000)} seconds`}
                      style={{ ['--brief-poll' as string]: `${pollIntervalMs}ms` }}
                    >
                      <circle className="brief-dot-track" cx="10" cy="10" r="9" />
                      <circle className="brief-dot-fill" cx="10" cy="10" r="9" />
                    </svg>
                  ) : (
                    <span className="brief-dot" aria-hidden="true" />
                  )}
                  <strong>{stage.label}</strong>
                  <span>{stage.hint}</span>
                </li>
              ))}
            </ol>
          </div>
          <div className="brief-tip">
            <p>
              <strong>Good to know. </strong>
              {tip}
            </p>
          </div>
          {round && (
            <details className="brief-tech">
              <summary>Technical details</summary>
              <p>
                Round <code>{round.id}</code> · {round.outcome} · {round.state.replaceAll('_', ' ')}
                . Used {round.used.turns} of {round.limits.turns} turns, {round.used.requests} of{' '}
                {round.limits.requests} requests.
                {round.stopReason ? ` Stopped: ${round.stopReason.replaceAll('_', ' ')}.` : ''}
                {adviceCode ? ` Advice: ${adviceCode}.` : ''}
              </p>
            </details>
          )}
        </>
      )}
      {showRetry && (
        <p role="alert" className="error">
          The start request didn&apos;t go through. Retry it below.
        </p>
      )}
    </section>
  );
}

function briefingCopy(
  props: BriefingProps,
  brief: BriefingState,
  outcome: string,
  noun: string,
  canStart: boolean,
  canReplacePaused: boolean,
): BriefingCopy {
  const { round, advice, reportSummary, cardCount, startUnconfirmed, staleStart } = props;

  if (staleStart) {
    return {
      eyebrow: 'Start needs review',
      title: 'The saved start request was rejected.',
      body: [
        'The server refused the saved request. Review the current work, then start a fresh search.',
      ],
      cardTitle: 'Next step',
      primary: { label: 'Review current work', onSelect: props.onReviewRejected },
      secondary: [],
    };
  }

  if (
    startUnconfirmed &&
    round !== null &&
    (round.state === 'paused' || round.state === 'completed' || round.state === 'failed')
  ) {
    const base = briefingCopy(
      { ...props, startUnconfirmed: false },
      brief,
      outcome,
      noun,
      canStart,
      canReplacePaused,
    );
    return {
      ...base,
      primary: { label: 'Retry start', onSelect: props.onStart },
      secondary: [],
    };
  }

  switch (brief) {
    case 'fresh':
      return {
        eyebrow: "Let's get started",
        title: 'Find your next opportunity, on autopilot.',
        body: [
          "Tell us what you're looking for. We'll search for roles, prepare applications, and track interviews — so you can focus on what matters.",
        ],
        cardTitle: 'Ready to start?',
        primary: {
          label: 'Start your job search',
          onSelect: props.onStart,
          disabled: !canStart,
          note: canStart ? undefined : 'Search is currently unavailable',
        },
        secondary: [{ label: "Review what you're looking for", onSelect: props.onEditBrief }],
      };
    case 'working': {
      const phase = workingPhaseOf(round?.state ?? null, startUnconfirmed);
      if (phase === 'starting') {
        return {
          eyebrow: 'Starting',
          title: `Starting your ${noun}…`,
          body: ['Setting up — nothing needed from you.'],
          cardTitle: 'You can let this run',
          cardBody: 'It will pick up work on its own.',
          primary: null,
          secondary: [],
        };
      }
      if (phase === 'stopping') {
        return {
          eyebrow: 'Stopping',
          title: 'Stopping…',
          body: ['Winding down safely — nothing needed from you.'],
          cardTitle: 'You can let this run',
          primary: null,
          secondary: [],
        };
      }
      return {
        eyebrow: workingEyebrow(outcome),
        title: workingTitle(outcome),
        body: [
          workingBody(outcome),
          `Nothing needed from you right now — the status refreshes automatically every ${Math.round(props.pollIntervalMs / 1000)} seconds.`,
          ...(props.searches.length > 0
            ? [`So far: ${checkedSummary(props.searches, props.leads.length)}.`]
            : []),
        ],
        cardTitle: 'You can let this run',
        cardBody: 'It will finish on its own. You can also stop it if you need to.',
        primary:
          round && round.state !== 'stopping'
            ? { label: `Stop ${noun}`, onSelect: props.onStop }
            : null,
        secondary: [],
      };
    }
    case 'waiting': {
      if (!round) {
        return {
          eyebrow: 'Paused',
          title: 'Nothing is running.',
          body: ['Saved work below remains readable.'],
          cardTitle: 'Next step',
          primary: null,
          secondary: [],
        };
      }
      const variant = waitingVariantOf(round);
      if (variant === 'stalled') {
        const items = round.unresolved.filter(
          (value): value is string => typeof value === 'string' && value.trim().length > 0,
        );
        return {
          eyebrow: 'Waiting',
          title: 'Paused waiting for a detail.',
          body: [
            items.length > 0
              ? `The ${noun} needs the following before it can continue: ${items.join('; ')}.`
              : `The ${noun} fenced itself waiting for a missing detail.`,
            'You can stop it and start fresh — saved work is kept.',
          ],
          cardTitle: 'What you can do',
          primary: { label: `Stop this ${noun}`, onSelect: props.onStop },
          secondary: [],
        };
      }
      if (variant === 'delivery') {
        return {
          eyebrow: 'Delivery paused',
          title: 'Delivery needs your review.',
          body: [
            "This delivery can't resume on its own. Review its exact saved items to close it out.",
          ],
          cardTitle: 'Next step',
          primary: null,
          secondary: [],
        };
      }
      if (variant === 'check') {
        return {
          eyebrow: 'We need your input',
          title: 'We need a quick decision from you to continue.',
          body: [
            'Some earlier work is uncertain. Resume to let the agency check it before continuing — nothing new starts until the check passes.',
          ],
          cardTitle: 'Resume when ready',
          primary: { label: 'Resume and check', onSelect: props.onResume },
          secondary: endAndStart(props, canReplacePaused),
        };
      }
      return {
        eyebrow: 'Paused',
        title: `Your ${noun} is paused.`,
        body: ['Your progress and remaining allowance are saved. Resume when ready.'],
        cardTitle: 'Resume when ready',
        primary: { label: 'Resume', onSelect: props.onResume },
        secondary: endAndStart(props, canReplacePaused),
      };
    }
    case 'results':
      return resultsCopy(props, outcome, noun, canStart);
    case 'empty': {
      const leadCount = props.leads.length;
      const startAction: BriefingAction = {
        label: 'Start a new search',
        onSelect: props.onStart,
        disabled: !canStart,
        note: canStart ? undefined : 'Search is currently unavailable',
      };
      if (props.searches.length > 0 && jobLeadsAllSaved(props.leads)) {
        const jobCount = props.leads.filter((lead) => lead.kind === 'job').length;
        const one = jobCount === 1;
        return {
          eyebrow: 'Latest search',
          title: 'Nothing new since your last search.',
          body: [
            `Checked ${checkedSummary(props.searches, jobCount)} — ${one ? 'this role is' : 'these roles are'} already in your saved opportunities.`,
            'New postings appear over time — searching again later can surface them.',
          ],
          cardTitle: 'What would you like to do next?',
          cardBody: 'Review the checked leads, or adjust your search and try again.',
          primary: {
            label: `Review ${jobCount} checked ${one ? 'lead' : 'leads'}`,
            onSelect: props.onShowLeads,
          },
          secondary: [
            startAction,
            { label: "Refine what you're looking for", onSelect: props.onEditBrief },
          ],
        };
      }
      if (leadCount > 0) {
        const one = leadCount === 1;
        return {
          eyebrow: 'Latest search',
          title: one
            ? 'We found 1 lead, but it could not be verified automatically.'
            : `We found ${leadCount} leads, but none could be verified automatically.`,
          body: [
            `Checked ${checkedSummary(props.searches, leadCount)}. We looked at each employer's official site, but none publish their jobs in a way we can verify on our own — so nothing was saved.`,
            `The ${one ? 'lead below is still a real job' : 'leads below are still real jobs'} worth a look. Open anything interesting directly.`,
          ],
          calloutTitle: 'We stopped early to save your allowance',
          calloutBody: 'Better to be selective than send you poor matches.',
          cardTitle: 'What would you like to do next?',
          cardBody: 'Review the leads, or adjust your search and try again.',
          primary: {
            label: `Review ${leadCount} ${one ? 'lead' : 'leads'}`,
            onSelect: props.onShowLeads,
          },
          secondary: [
            startAction,
            { label: "Refine what you're looking for", onSelect: props.onEditBrief },
            { label: 'View the partial report', onSelect: props.onShowResult },
          ],
        };
      }
      return {
        eyebrow: 'Latest search',
        title: "We didn't find any roles to recommend this time.",
        body: [
          'We searched a wide range of relevant opportunities, but nothing met your criteria well enough to recommend.',
          props.searches.length > 0
            ? `Checked ${checkedSummary(props.searches, 0)}.`
            : 'No source was checked — the search step failed before reaching any job board.',
        ],
        calloutTitle: 'We stopped early to save your allowance',
        calloutBody: 'Better to be selective than send you poor matches.',
        cardTitle: 'What would you like to do next?',
        cardBody: 'You can adjust your search or try again. It only takes a minute.',
        primary: startAction,
        secondary: [
          { label: "Refine what you're looking for", onSelect: props.onEditBrief },
          { label: 'View the partial report', onSelect: props.onShowResult },
        ],
      };
    }
    case 'failed': {
      const hasPartial = cardCount > 0 || reportSummary.length > 0;
      const secondary: BriefingAction[] = [];
      if (advice?.available) {
        secondary.push({
          label: advice.label,
          onSelect: props.onFollowAdvice,
          disabled: advice.busy,
        });
      }
      if (hasPartial) {
        secondary.push({ label: 'View partial results', onSelect: props.onShowResult });
      }
      return {
        eyebrow: 'Something went wrong',
        title: `We couldn't complete the ${noun}.`,
        body: [
          "An unexpected issue stopped the work. This isn't your fault.",
          'Starting again begins a fresh attempt with the same preferences. Saved work is kept.',
        ],
        cardTitle: 'Try again?',
        primary: {
          label: 'Try again',
          onSelect: props.onStart,
          disabled: !canStart,
          note: canStart ? undefined : 'Search is currently unavailable',
        },
        secondary,
      };
    }
  }
}

function endAndStart(props: BriefingProps, canReplacePaused: boolean): BriefingAction[] {
  return [
    {
      label: 'End this and start a new search',
      onSelect: props.onStart,
      disabled: !canReplacePaused,
      note: canReplacePaused ? undefined : 'Search is currently unavailable',
    },
  ];
}

function workingEyebrow(outcome: string): string {
  switch (outcome) {
    case 'discover':
      return 'Search in progress';
    case 'prepare':
      return 'Application in progress';
    case 'process_input':
      return 'Working on your input';
    case 'compare_offers':
      return 'Comparison in progress';
    case 'interview_prepare':
      return 'Interview prep in progress';
    case 'interview_debrief':
      return 'Debrief in progress';
    case 'deliver':
      return 'Delivery in progress';
    default:
      return 'Work in progress';
  }
}

function workingTitle(outcome: string): string {
  switch (outcome) {
    case 'discover':
      return "We're looking for great opportunities for you right now.";
    case 'prepare':
      return "We're preparing your application right now.";
    case 'process_input':
      return "We're handling your input right now.";
    case 'compare_offers':
      return "We're comparing your offers right now.";
    case 'interview_prepare':
      return "We're preparing your interview right now.";
    case 'interview_debrief':
      return "We're writing up your interview right now.";
    case 'deliver':
      return "We're delivering your application right now.";
    default:
      return "We're working for you right now.";
  }
}

function workingBody(outcome: string): string {
  if (outcome === 'discover')
    return 'Checking job boards, company sites, and our network. This usually takes a while.';
  return 'This usually takes a while.';
}

function resultsCopy(
  props: BriefingProps,
  outcome: string,
  noun: string,
  canStart: boolean,
): BriefingCopy {
  const { advice, cardCount } = props;
  const advised: BriefingAction | null =
    advice?.available && advice
      ? { label: advice.label, onSelect: props.onFollowAdvice, disabled: advice.busy }
      : null;
  const startNew: BriefingAction = {
    label: 'Start a new search',
    onSelect: props.onStart,
    disabled: !canStart,
    note: canStart ? undefined : 'Search is currently unavailable',
  };
  const viewReport: BriefingAction = {
    label: 'View saved report',
    onSelect: props.onShowResult,
  };

  if (outcome === 'discover' && cardCount > 0) {
    const one = cardCount === 1;
    const otherLeads = props.leads.filter((lead) => lead.status !== 'saved');
    const checked =
      props.searches.length > 0
        ? [`Checked ${checkedSummary(props.searches, props.leads.length)}.`]
        : [];
    return {
      eyebrow: 'We found some great opportunities',
      title: `We found ${cardCount} ${one ? 'role' : 'roles'} worth reviewing.`,
      body: [
        "These match your preferences and have a good chance of being a great fit. Take a look and tell us which ones you'd like to apply for.",
        ...checked,
      ],
      cardTitle: 'Review the roles',
      primary: advised ?? {
        label: `Review ${cardCount} ${one ? 'role' : 'roles'}`,
        onSelect: props.onShowCards,
      },
      secondary: [
        ...(otherLeads.length > 0
          ? [
              {
                label: `Review ${otherLeads.length} other ${otherLeads.length === 1 ? 'lead' : 'leads'}`,
                onSelect: props.onShowLeads,
              } satisfies BriefingAction,
            ]
          : []),
        startNew,
        viewReport,
      ],
    };
  }
  if (outcome === 'discover') {
    return {
      eyebrow: 'Work complete',
      title: 'Your search is complete.',
      body: ['No new role cards were saved, but there is a suggested next step.'],
      cardTitle: 'Your next step',
      primary: advised ?? viewReport,
      secondary: [startNew],
    };
  }
  if (outcome === 'prepare') {
    return {
      eyebrow: 'Application ready',
      title: 'Your application is ready to review.',
      body: [
        'Preparation finished. Review the private pack before anything is sent — this step never sends anything by itself.',
      ],
      cardTitle: 'Review the application',
      primary: advised ?? { label: 'Review application', onSelect: props.onShowPreparation },
      secondary: [startNew, viewReport],
    };
  }
  if (outcome === 'compare_offers') {
    return {
      eyebrow: 'Comparison ready',
      title: 'Your offer comparison is ready.',
      body: [
        'The trade-offs are laid out. Open it to review unknown terms before deciding anything.',
      ],
      cardTitle: 'Review the comparison',
      primary: advised ?? {
        label: 'Open offer comparison',
        onSelect: props.onShowComparison,
      },
      secondary: [startNew, viewReport],
    };
  }
  if (outcome === 'interview_prepare') {
    return {
      eyebrow: 'Interview prep ready',
      title: 'Your interview brief is ready.',
      body: ['Preparation finished. Open the saved brief to review it.'],
      cardTitle: 'Review the brief',
      primary: advised ?? viewReport,
      secondary: [startNew],
    };
  }
  if (outcome === 'interview_debrief') {
    return {
      eyebrow: 'Debrief ready',
      title: 'Your interview debrief is ready.',
      body: ['The write-up finished. Open the saved result to review it.'],
      cardTitle: 'Review the debrief',
      primary: advised ?? viewReport,
      secondary: [startNew],
    };
  }
  if (outcome === 'deliver') {
    return {
      eyebrow: 'Delivery finished',
      title: 'Delivery finished.',
      body: [
        'The delivery round completed. Employer receipt is not confirmed here — review the saved result.',
      ],
      cardTitle: 'Review the delivery',
      primary: advised ?? viewReport,
      secondary: [startNew],
    };
  }
  return {
    eyebrow: 'Work complete',
    title: `Your ${noun} is complete.`,
    body: ['Review the saved result below.'],
    cardTitle: 'Your next step',
    primary: advised ?? viewReport,
    secondary: [startNew],
  };
}

function SafeLeadLink({ value, label }: { value: string; label: string }) {
  let href: string | undefined;
  try {
    const parsed = new URL(value);
    if (['http:', 'https:'].includes(parsed.protocol) && !parsed.username && !parsed.password)
      href = parsed.href;
  } catch {
    // Untrusted lead URLs remain plain text.
  }
  return href ? (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {label}
    </a>
  ) : (
    <span>{label}</span>
  );
}

function readAt(iso: string): string {
  const at = new Date(iso);
  return Number.isNaN(at.getTime()) ? '' : at.toLocaleString();
}

export function BriefingLeads({
  leads,
  searches,
}: {
  leads: RoundCandidateLead[];
  searches: RoundCandidateSearch[];
}) {
  return (
    <section className="op-card" id="saved-round-leads" aria-label="Leads from this search">
      <h2>Leads from this search{leads.length ? ` (${leads.length})` : ''}</h2>
      {searches.length > 0 && (
        <>
          <h3>What we checked</h3>
          <ul className="brief-checks">
            {searches.map((read) => (
              <li key={`${read.source}-${read.method}-${read.observedAt}`}>
                {sourceName(read.source)} · {methodName(read.method)} ·{' '}
                {read.succeeded ? 'completed' : 'failed'} · {read.candidates}{' '}
                {read.candidates === 1 ? 'lead' : 'leads'}
                {readAt(read.observedAt) ? ` · ${readAt(read.observedAt)}` : ''}
              </li>
            ))}
          </ul>
        </>
      )}
      {leads.length === 0 ? (
        <p>No leads were staged from these reads.</p>
      ) : (
        <ul className="op-list">
          {leads.map((lead) => (
            <li className="op-card" key={lead.id}>
              <h3>
                <SafeLeadLink value={lead.url} label={lead.title} />
              </h3>
              <p>
                <strong>{leadStatusLabel(lead.status)}</strong> · {lead.statusReason}
              </p>
              {lead.companyUrl && (
                <p>
                  Employer site: <SafeLeadLink value={lead.companyUrl} label={lead.companyUrl} />
                </p>
              )}
              {lead.evidenceQuote && (
                <details>
                  <summary>Saved evidence quote</summary>
                  <pre className="op-source">{lead.evidenceQuote}</pre>
                </details>
              )}
            </li>
          ))}
        </ul>
      )}
      <p className="hint">
        Leads are raw findings, not verified opportunities. Saved opportunities appear as cards
        below once their employer checks pass.
      </p>
    </section>
  );
}
