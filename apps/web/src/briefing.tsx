import type { ReactNode } from 'react';
import type { Round } from './api';
import { outcomeNoun, waitingVariantOf } from './briefing-state';
import './briefing.css';

export interface BriefingAdvice {
  label: string;
  available: boolean;
  busy: boolean;
}

export interface BriefingProps {
  round: Round | null;
  reportSummary: string;
  advice: BriefingAdvice | null;
  adviceCode?: string;
  loading: boolean;
  busy: boolean;
  error: string | null;
  actionError: string | null;
  adviceError: string;
  pollingActive: boolean;
  pollIntervalMs: number;
  briefReady: boolean;
  onRefresh: () => void;
  onStop: () => void;
  onResume: () => void;
  onFollowAdvice: () => void;
  onEditBrief: () => void;
  onShowResult: () => void;
  deliveryRecovery: ReactNode;
}

export function Briefing(props: BriefingProps) {
  const { round, advice, busy, loading } = props;
  const noun = outcomeNoun(round?.outcome ?? '');
  const active = round && ['queued', 'running', 'awaiting_input', 'stopping'].includes(round.state);
  const paused = round?.state === 'paused';
  const complete = round?.state === 'completed';
  const failed = round?.state === 'failed';
  const title = !round
    ? 'Your recruitment work'
    : active
      ? round.state === 'stopping'
        ? 'Stopping saved work…'
        : `Your ${noun} is in progress.`
      : paused
        ? `Your ${noun} is paused.`
        : complete
          ? `Your ${noun} is ready to review.`
          : `Your ${noun} stopped before completion.`;
  const explanation = !round
    ? 'Review your campaign brief and saved opportunities. Opportunity discovery has been removed from this app.'
    : active
      ? `The current work is saved. Status refreshes every ${Math.round(props.pollIntervalMs / 1000)} seconds.`
      : paused
        ? round.outcome === 'deliver'
          ? 'Review the saved delivery items below to close this commission.'
          : 'Progress and remaining allowance are saved. Resume when ready.'
        : props.reportSummary || round.intent;
  const recommendationAvailable = advice?.available && !advice.busy;
  const showResult = () => props.onShowResult();

  return (
    <section className="op-card agency-lead briefing" aria-label="Agency work">
      <div className="op-heading-row">
        <p className="eyebrow">Your recruitment agency</p>
        <button
          className="secondary"
          type="button"
          disabled={loading || busy}
          onClick={props.onRefresh}
        >
          Refresh work
        </button>
      </div>
      {props.error && (
        <p role="alert" className="error">
          {props.error}
        </p>
      )}
      {props.actionError && (
        <p role="alert" className="error">
          {props.actionError}
        </p>
      )}
      {props.adviceError && (
        <p role="alert" className="error">
          {props.adviceError}
        </p>
      )}
      {loading && !round ? (
        <p role="status">Loading your campaign and saved work…</p>
      ) : (
        <div className="brief-hero">
          <div className="brief-main">
            <p className="eyebrow">
              {!round ? 'Campaign overview' : round.state.replaceAll('_', ' ')}
            </p>
            <h2>{title}</h2>
            <p>{explanation}</p>
            {!round && (
              <p>
                Saved roles can still be reviewed, selected, prepared, compared, and followed
                through interviews and replies.
              </p>
            )}
            {props.pollingActive && <p role="status">Checking status automatically.</p>}
          </div>
          <aside className="brief-actions" aria-label="Next step">
            <h3>Next step</h3>
            {busy ? (
              <p role="status">Working on it…</p>
            ) : (
              <>
                {active && round?.state !== 'stopping' && (
                  <button type="button" onClick={props.onStop}>
                    Stop {noun}
                  </button>
                )}
                {paused && round.outcome !== 'deliver' && (
                  <button type="button" onClick={props.onResume}>
                    {waitingVariantOf(round) === 'check' ? 'Resume and check' : 'Resume this round'}
                  </button>
                )}
                {(complete || failed) && advice && (
                  <>
                    <button
                      type="button"
                      disabled={!recommendationAvailable}
                      onClick={props.onFollowAdvice}
                    >
                      {advice.label}
                    </button>
                    {!recommendationAvailable && (
                      <p className="hint">
                        Historical advice: {props.adviceCode || 'review current work before acting'}
                        .
                      </p>
                    )}
                  </>
                )}
                {(complete || failed) && (
                  <button type="button" className="secondary brief-row" onClick={showResult}>
                    View saved report
                  </button>
                )}
                {!round && (
                  <button
                    type="button"
                    className="secondary brief-row"
                    disabled={loading || !props.briefReady}
                    onClick={props.onEditBrief}
                  >
                    Review campaign brief
                  </button>
                )}
              </>
            )}
            {paused && round.outcome === 'deliver' && props.deliveryRecovery}
          </aside>
        </div>
      )}
      {round && (
        <details className="brief-tech">
          <summary>Technical details</summary>
          <p>
            Round <code>{round.id}</code> · {round.outcome} · {round.state.replaceAll('_', ' ')}.
            Used {round.used.turns} of {round.limits.turns} turns, {round.used.requests} of{' '}
            {round.limits.requests} requests.
            {round.stopReason ? ` Stopped: ${round.stopReason.replaceAll('_', ' ')}.` : ''}
            {props.adviceCode ? ` Advice: ${props.adviceCode}.` : ''}
          </p>
        </details>
      )}
    </section>
  );
}
