import { useCallback, useEffect, useRef, useState } from 'react';
import {
  getActiveRound,
  getRoundCards,
  getRoundCapability,
  getRoundHistory,
  getPreferences,
  getRound,
  isUnauthenticated,
  RequestError,
  resumeRound,
  startRound,
  stopRound,
  type Preferences,
  type Round,
  type RoundCard,
  type RoundCapability,
  type RoundHistoryEvent,
  type Session,
} from './api';

const lastRoundKey = 'jobseek.last-round';
const startKey = 'jobseek.pending-round-start';
const activeStates = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
const pollIntervalMs = 5000;

function roundTitle(outcome: string): string {
  if (outcome === 'discover') return 'Find my next opportunities';
  if (outcome === 'prepare') return 'Prepare an application';
  return outcome.replaceAll('_', ' ');
}

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'The request could not be completed.';
}

function remaining(limit: number, used: number): number {
  return Math.max(0, limit - used);
}

function SafeSource({ value }: { value: string }) {
  try {
    const url = new URL(value);
    if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password)
      return (
        <a href={url.href} target="_blank" rel="noopener noreferrer">
          Original source
        </a>
      );
  } catch {
    /* Show no unsafe link. */
  }
  return <span>Source link unavailable</span>;
}

export function AgencyHome({
  session,
  onSessionLost,
  onOpenOpportunity,
  onEditBrief,
}: {
  session: Session;
  onSessionLost: () => void;
  onOpenOpportunity: (id: string) => void;
  onEditBrief: (version: number) => void;
}) {
  const [preferences, setPreferences] = useState<Preferences | null>(null);
  const [round, setRound] = useState<Round | null>(null);
  const [capability, setCapability] = useState<RoundCapability | null>(null);
  const [cards, setCards] = useState<RoundCard[]>([]);
  const [history, setHistory] = useState<RoundHistoryEvent[]>([]);
  const [cardsAvailable, setCardsAvailable] = useState(false);
  const [historyAvailable, setHistoryAvailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const readController = useRef<AbortController | null>(null);
  const readPromise = useRef<Promise<Round | null | undefined> | null>(null);
  const readVersion = useRef(0);
  const mutationInFlight = useRef(false);
  const mounted = useRef(false);
  const resultsRoundId = useRef<string | null>(null);

  const invalidateRead = useCallback(() => {
    readVersion.current += 1;
    readController.current?.abort();
    readController.current = null;
    readPromise.current = null;
  }, []);

  const refresh = useCallback(
    (silent = false): Promise<Round | null | undefined> => {
      if (!mounted.current || mutationInFlight.current) return Promise.resolve(undefined);
      if (readPromise.current) return readPromise.current;
      const controller = new AbortController();
      const version = ++readVersion.current;
      readController.current = controller;
      const currentRead = () =>
        mounted.current && !controller.signal.aborted && version === readVersion.current;
      if (!silent) {
        setLoading(true);
        setError(null);
      }
      const task = (async () => {
        try {
          const [brief, active, available] = await Promise.all([
            getPreferences(controller.signal),
            getActiveRound(controller.signal),
            getRoundCapability(controller.signal),
          ]);
          let current = active;
          if (!current) {
            try {
              const id = localStorage.getItem(lastRoundKey);
              if (id) current = await getRound(id, controller.signal);
            } catch (cause) {
              if (!(cause instanceof RequestError && cause.status === 404)) throw cause;
            }
          }
          if (!currentRead()) return undefined;
          setPreferences(brief);
          setCapability(available);
          setRound(current);
          setError(null);
          if (current) {
            try {
              localStorage.setItem(lastRoundKey, current.id);
            } catch {
              /* Read remains available in this page. */
            }
            if (resultsRoundId.current !== current.id) {
              resultsRoundId.current = current.id;
              setCards([]);
              setHistory([]);
              setCardsAvailable(false);
              setHistoryAvailable(false);
            }
            const [cardRead, historyRead] = await Promise.allSettled([
              current.outcome === 'discover'
                ? getRoundCards(current.id, controller.signal)
                : Promise.resolve([] as RoundCard[]),
              getRoundHistory(current.id, controller.signal),
            ]);
            if (currentRead()) {
              if (cardRead.status === 'fulfilled') {
                setCards(cardRead.value);
                setCardsAvailable(true);
              }
              if (historyRead.status === 'fulfilled') {
                setHistory(historyRead.value);
                setHistoryAvailable(true);
              }
              if (cardRead.status === 'rejected' || historyRead.status === 'rejected') {
                const cause =
                  cardRead.status === 'rejected'
                    ? cardRead.reason
                    : historyRead.status === 'rejected'
                      ? historyRead.reason
                      : undefined;
                setError(
                  `${message(cause)} The affected round view will retry on the next status read.`,
                );
              }
            }
          } else {
            resultsRoundId.current = null;
            setCards([]);
            setHistory([]);
            setCardsAvailable(false);
            setHistoryAvailable(false);
          }
          return current;
        } catch (cause) {
          if (!currentRead()) return undefined;
          if (isUnauthenticated(cause)) onSessionLost();
          else
            setError(
              `${message(cause)} Saved work remains visible; the next status read will retry.`,
            );
          return undefined;
        } finally {
          if (currentRead()) setLoading(false);
        }
      })();
      readPromise.current = task;
      void task.finally(() => {
        if (readPromise.current === task) readPromise.current = null;
        if (readController.current === controller) readController.current = null;
      });
      return task;
    },
    [onSessionLost],
  );

  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
      invalidateRead();
    };
  }, [refresh, invalidateRead]);

  useEffect(() => {
    if (!round || !activeStates.has(round.state)) return;
    let cancelled = false;
    let timer: number;
    const poll = async () => {
      const latest = await refresh(true);
      if (!cancelled && (latest === undefined || (latest && activeStates.has(latest.state))))
        timer = window.setTimeout(() => void poll(), pollIntervalMs);
    };
    timer = window.setTimeout(() => void poll(), pollIntervalMs);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [round?.id, round?.state, refresh]);

  async function act(operation: 'stop' | 'resume') {
    if (!round || mutationInFlight.current) return;
    mutationInFlight.current = true;
    invalidateRead();
    setBusy(true);
    setActionError(null);
    try {
      const changed =
        operation === 'stop'
          ? await stopRound(round.id, session.csrfToken)
          : await resumeRound(round.id, session.csrfToken);
      if (mounted.current) {
        setRound(changed);
        if (operation === 'resume' && changed.state === 'paused' && changed.reconciliationRequired)
          setActionError('Reconciliation remains unresolved. No new work was confirmed.');
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (mounted.current) setActionError(message(cause));
    } finally {
      mutationInFlight.current = false;
      if (mounted.current) {
        setBusy(false);
        void refresh(true);
      }
    }
  }

  async function start() {
    if (
      !capability?.canStart ||
      mutationInFlight.current ||
      (round && ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(round.state))
    )
      return;
    mutationInFlight.current = true;
    invalidateRead();
    setBusy(true);
    setActionError(null);
    try {
      let key = localStorage.getItem(startKey);
      if (!key) {
        key = crypto.randomUUID();
        localStorage.setItem(startKey, key);
      }
      const created = await startRound(key, session.csrfToken);
      if (mounted.current) setRound(created);
      localStorage.removeItem(startKey);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (mounted.current)
        setActionError(
          `${message(cause)} Refresh work before retrying; the same Start identity is retained.`,
        );
    } finally {
      mutationInFlight.current = false;
      if (mounted.current) {
        setBusy(false);
        void refresh(true);
      }
    }
  }

  const active = round && ['queued', 'running', 'awaiting_input', 'stopping'].includes(round.state);
  const paused = round?.state === 'paused';
  const reportSummary =
    round && typeof round.report.summary === 'string' ? round.report.summary : '';
  return (
    <>
      <section className="op-card agency-lead" aria-label="Agency work">
        <div className="op-heading-row">
          <div>
            <p className="eyebrow">Your recruitment agency</p>
            <h2>{roundTitle(round?.outcome || capability?.outcome || 'discover')}</h2>
          </div>
          <button
            className="secondary"
            type="button"
            disabled={loading || busy}
            onClick={() => void refresh()}
          >
            Refresh work
          </button>
        </div>
        {loading && <p role="status">Loading your campaign and saved work…</p>}
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        {round ? (
          <>
            <p>
              <strong>{round.state.replaceAll('_', ' ')}</strong> ·{' '}
              {round.step || 'Current step has not been reported.'}
            </p>
            <p>{round.intent}</p>
            {reportSummary && <p>{reportSummary}</p>}
            {round.stopReason && <p>Stopped because: {round.stopReason.replaceAll('_', ' ')}</p>}
            {round.deliverableStatus && (
              <p>Deliverable: {round.deliverableStatus.replaceAll('_', ' ')}</p>
            )}
            {round.reconciliationRequired && (
              <p role="status">
                An earlier action needs reconciliation before more work can resume.
              </p>
            )}
            {round.unresolved.length > 0 && (
              <p>
                {round.unresolved.length} unresolved item{round.unresolved.length === 1 ? '' : 's'}{' '}
                remain in the saved report.
              </p>
            )}
            <details>
              <summary>Scope and remaining allowance</summary>
              <p>
                Outcome: {round.outcome}. Deadline: {new Date(round.deadline).toLocaleString()}.
              </p>
              <p>
                Remaining: {remaining(round.limits.requests, round.used.requests)} requests,{' '}
                {remaining(round.limits.items, round.used.items)} items,{' '}
                {remaining(round.limits.tools, round.used.tools)} tools,{' '}
                {remaining(round.limits.turns, round.used.turns)} turns.
              </p>
              <p>Allowed operations: {round.scope.operations.join(', ') || 'none recorded'}.</p>
            </details>
            <div className="button-row">
              {active && (
                <button
                  type="button"
                  disabled={busy || round.state === 'stopping'}
                  onClick={() => void act('stop')}
                >
                  Stop this round
                </button>
              )}
              {paused && (
                <button type="button" disabled={busy} onClick={() => void act('resume')}>
                  {round.reconciliationRequired
                    ? 'Resume and check uncertain work'
                    : 'Resume this round'}
                </button>
              )}
            </div>
            {paused && round.reconciliationRequired && (
              <p className="hint">
                Resume asks the server to reconcile uncertain work before any new step. Page refresh
                does not restart the round.
              </p>
            )}
            {paused && !capability?.canStart && (
              <p className="hint">
                {round.outcome === 'prepare' ? 'Application preparation' : 'Discovery'} execution is
                currently unavailable. Resume may leave this round paused; saved results and
                remaining allowance remain readable.
              </p>
            )}
            {!active && !paused && capability && (
              <div className="agency-next">
                <h3>Next useful action</h3>
                <p>
                  {capability.intent} A new round receives its own server-selected scope and
                  allowance.
                </p>
                <button
                  type="button"
                  disabled={busy || !capability.canStart}
                  onClick={() => void start()}
                >
                  {capability.canStart
                    ? 'Find my next opportunities'
                    : 'Find my next opportunities — unavailable'}
                </button>
              </div>
            )}
          </>
        ) : !loading && !error ? (
          <>
            <p>
              {capability?.intent ||
                'A bounded search would return sourced roles and a coverage report.'}
            </p>
            {capability && (
              <p>
                Scope: the current campaign and {capability.sourceCount} board source
                {capability.sourceCount === 1 ? '' : 's'} currently in server scope; allowance up to{' '}
                {capability.limits.requests} requests, {capability.limits.items} item operations and{' '}
                {capability.limits.turns} Codex turns. The server checks the actual scope when you
                start.
              </p>
            )}
            {!capability?.canStart && (
              <p role="status">
                Round execution is unavailable. Your brief and saved sources remain readable.
              </p>
            )}
            <button
              type="button"
              disabled={busy || !capability?.canStart}
              onClick={() => void start()}
            >
              {capability?.canStart
                ? 'Find my next opportunities'
                : 'Find my next opportunities — unavailable'}
            </button>
          </>
        ) : null}
        {actionError && (
          <p role="alert" className="error">
            {actionError} Saved work remains visible; refresh its status before another action.
          </p>
        )}
      </section>
      <section className="op-card" aria-label="Campaign brief">
        <div className="op-heading-row">
          <h2>What we know about your search</h2>
          <button
            className="secondary"
            type="button"
            disabled={!preferences}
            onClick={() => preferences && onEditBrief(preferences.version)}
          >
            Correct this brief
          </button>
        </div>
        {preferences ? (
          <div className="agency-brief">
            <div>
              <h3>Your stated direction</h3>
              <p>
                {preferences.roleCriteria
                  .filter((item) => item.mode !== 'avoid')
                  .map((item) => item.label)
                  .join(', ') || 'No role direction saved.'}
              </p>
              <p>
                Work to avoid:{' '}
                {preferences.roleCriteria
                  .filter((item) => item.mode === 'avoid')
                  .map((item) => item.label)
                  .join(', ') || 'None recorded.'}
              </p>
              <p>
                {preferences.targetHours} hours/week · at least{' '}
                {(preferences.minMonthlyBaseCents / 100).toLocaleString()}{' '}
                {preferences.salaryCurrency} gross monthly base ·{' '}
                {preferences.preferredLocation || 'location not specified'}.
              </p>
              <p>
                Remote {preferences.allowRemote ? 'allowed' : 'not selected'}; hybrid{' '}
                {preferences.allowHybrid ? 'allowed' : 'not selected'}.
              </p>
            </div>
            <div>
              <h3>Documented history</h3>
              <p>
                Verified experience and source references are not yet available in this brief. Saved
                opportunity evidence appears below.
              </p>
              <h3>Proposals and unknowns</h3>
              <p>
                No inferred career preference has been confirmed here. Each role’s pay, hours and
                conflicts remain unknown until supported by its source.
              </p>
            </div>
          </div>
        ) : (
          !loading && <p>Campaign brief unavailable. Refresh to try again.</p>
        )}
      </section>
      {round?.outcome === 'prepare' && (
        <section className="op-card" aria-label="Application preparation">
          <h2>Application preparation</h2>
          <p>
            Review the private pack from its opportunity when preparation finishes. This round does
            not send an application.
          </p>
          {round.scope.resources
            .filter((resource) => resource.startsWith('opportunity:'))
            .map((resource) => (
              <button
                key={resource}
                className="op-text-button"
                type="button"
                onClick={() => onOpenOpportunity(resource.slice('opportunity:'.length))}
              >
                Open selected opportunity
              </button>
            ))}
        </section>
      )}
      {round?.outcome === 'discover' && (
        <section className="op-card" aria-label="Round results">
          <h2>Saved opportunity cards {cards.length ? `(${cards.length})` : ''}</h2>
          {cards.length === 0 && (
            <p>
              {cardsAvailable
                ? 'No opportunity cards have been saved for this round yet.'
                : 'Opportunity cards are unavailable. Refresh to try again.'}
            </p>
          )}
          <ul className="op-list">
            {cards.map((item) => (
              <li className="op-card" key={item.opportunityId}>
                <h3>{item.title}</h3>
                <p>
                  {item.companyName} · {item.kind} · {item.decision || 'No owner decision'}
                </p>
                <p>
                  Pay, hours and fit: unknown in this card. Inspect the saved opportunity for
                  recorded detail.
                </p>
                {item.sourceStale && (
                  <p role="status">
                    Source changed since this card was saved. Inspect the current opportunity.
                  </p>
                )}
                {item.sourceUrl && (
                  <p>
                    <SafeSource value={item.sourceUrl} />
                  </p>
                )}
                {item.sourceText && (
                  <details>
                    <summary>Saved source text</summary>
                    <pre className="op-source">{item.sourceText}</pre>
                  </details>
                )}
                <button
                  type="button"
                  className="op-text-button"
                  onClick={() => onOpenOpportunity(item.opportunityId)}
                >
                  Inspect saved opportunity
                </button>
              </li>
            ))}
          </ul>
          <details>
            <summary>Round history ({history.length})</summary>
            {!historyAvailable ? (
              <p>Round history is unavailable. Refresh to try again.</p>
            ) : history.length ? (
              <ol>
                {history.map((event) => (
                  <li key={event.auditId}>
                    {event.operation.replaceAll('_', ' ')} · {event.entityKind.replaceAll('_', ' ')}{' '}
                    · {new Date(event.occurredAt).toLocaleString()}
                  </li>
                ))}
              </ol>
            ) : (
              <p>No recorded round events are available yet.</p>
            )}
          </details>
        </section>
      )}
    </>
  );
}
