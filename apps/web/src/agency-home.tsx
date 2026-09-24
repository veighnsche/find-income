import { useCallback, useEffect, useRef, useState } from 'react';
import { Briefing } from './briefing';
import { ProcessInputReport } from './process-input-report';
import { ResearchRunPanel } from './research-run';
import { OfferComparisonPanel } from './offer-comparison-panel';
import {
  checkRecommendationTarget,
  readHomeRecommendation,
  readRecommendationCurrentness,
} from './home-recommendation';
import {
  getActiveRound,
  getDeliveryReview,
  getLatestCompletedSavedRound,
  listInterviews,
  getPreferences,
  getRound,
  isUnauthenticated,
  RequestError,
  resumeRound,
  stopRound,
  type Preferences,
  type Round,
  type Session,
} from './api';

const lastRoundKey = 'jobseek.last-round';
const activeStates = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
const pollIntervalMs = 5000;

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'The request could not be completed.';
}

function DeliveryPausedRecovery({
  round,
  onOpenOpportunity,
  onSessionLost,
}: {
  round: Round;
  onOpenOpportunity: (id: string) => void;
  onSessionLost: () => void;
}) {
  const reviewId = round.scope.inputRefs
    .find((ref) => ref.startsWith('delivery_review:'))
    ?.slice('delivery_review:'.length);
  const [review, setReview] = useState<Awaited<ReturnType<typeof getDeliveryReview>> | null>(null);
  const [error, setError] = useState('');
  useEffect(() => {
    if (!reviewId) return;
    const controller = new AbortController();
    void getDeliveryReview(reviewId, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setReview(value);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(`${message(cause)} The saved delivery review is unavailable.`);
      });
    return () => controller.abort();
  }, [reviewId, onSessionLost]);
  return (
    <div className="agency-next" role="region" aria-label="Paused delivery recovery">
      <p>
        This delivery commission cannot resume. Review its exact saved items and use Close
        unresolved commission from that review when ready. A receipt check may be unavailable.
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {review?.items.map((item) => (
        <button
          key={item.id}
          type="button"
          className="secondary"
          onClick={() => {
            try {
              localStorage.setItem('jobseek.delivery-review-id', review.id);
            } catch {
              /* The saved review remains server readable. */
            }
            onOpenOpportunity(item.opportunityId);
          }}
        >
          Open saved delivery review for {item.title}
        </button>
      ))}
      {!review && !error && <p>Reading the saved delivery review…</p>}
    </div>
  );
}

export function AgencyHome({
  session,
  onSessionLost,
  onOpenOpportunity,
  onOpenPack,
  onOpenPreparation,
  onOpenInterview,
  onEditBrief,
  onBriefLoaded,
}: {
  session: Session;
  onSessionLost: () => void;
  onOpenOpportunity: (id: string) => void;
  onOpenPack: (opportunityId: string, packId: string) => void;
  onOpenPreparation: (opportunityId: string) => void;
  onOpenInterview: (opportunityId: string, interviewId: string, debriefId?: string) => void;
  onEditBrief: (version: number) => void;
  onBriefLoaded?: (version: number) => void;
}) {
  const [preferences, setPreferences] = useState<Preferences | null>(null);
  const [round, setRound] = useState<Round | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [adviceCheck, setAdviceCheck] = useState<{ key: string; reason: string | null } | null>(
    null,
  );
  const [adviceBusy, setAdviceBusy] = useState(false);
  const [adviceError, setAdviceError] = useState('');
  const [focusedComparisonId, setFocusedComparisonId] = useState('');
  const [reviewedResultId, setReviewedResultId] = useState('');
  useEffect(() => setFocusedComparisonId(''), [round?.id]);
  const readController = useRef<AbortController | null>(null);
  const readPromise = useRef<Promise<Round | null | undefined> | null>(null);
  const readVersion = useRef(0);
  const mutationInFlight = useRef(false);
  const mounted = useRef(false);

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
          const [brief, active] = await Promise.all([
            getPreferences(controller.signal),
            getActiveRound(controller.signal),
          ]);
          let current = active;
          if (!current) {
            let id = '';
            try {
              id = localStorage.getItem(lastRoundKey) || '';
            } catch {
              /* The server can still find completed work without browser storage. */
            }
            if (id) {
              try {
                current = await getRound(id, controller.signal);
              } catch (cause) {
                if (!(cause instanceof RequestError && cause.status === 404)) throw cause;
              }
            }
            if (!current) current = await getLatestCompletedSavedRound(controller.signal);
          }
          if (!currentRead()) return undefined;
          setPreferences(brief);
          onBriefLoaded?.(brief.version);
          setRound(current);
          setError(null);
          if (current) {
            try {
              localStorage.setItem(lastRoundKey, current.id);
            } catch {
              /* Server read remains available. */
            }
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
          if (currentRead()) {
            setLoading(false);
          }
        }
      })();
      readPromise.current = task;
      void task.finally(() => {
        if (readPromise.current === task) readPromise.current = null;
        if (readController.current === controller) readController.current = null;
      });
      return task;
    },
    [onSessionLost, onBriefLoaded],
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
    const onCommissioned = () => {
      invalidateRead();
      void refresh();
    };
    window.addEventListener('jobseek:round-commissioned', onCommissioned);
    return () => window.removeEventListener('jobseek:round-commissioned', onCommissioned);
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
    if (
      !round ||
      mutationInFlight.current ||
      (operation === 'resume' && round.outcome === 'deliver')
    )
      return;
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

  const recommendation = readHomeRecommendation(round);
  const currentness = readRecommendationCurrentness(round);
  const adviceKey =
    recommendation && preferences
      ? `${round?.id}:${round?.revision}:${preferences.version}:${recommendation.action || recommendation.status}:${recommendation.target?.id || ''}`
      : '';
  useEffect(() => {
    if (!recommendation || !round || !preferences || currentness?.status !== 'current') {
      setAdviceCheck(null);
      return;
    }
    const controller = new AbortController();
    void checkRecommendationTarget(recommendation, round, preferences.version, controller.signal)
      .then((reason) => {
        if (!controller.signal.aborted) setAdviceCheck({ key: adviceKey, reason });
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else
          setAdviceCheck({
            key: adviceKey,
            reason: 'Current source records could not be verified.',
          });
      });
    return () => controller.abort();
  }, [
    round,
    preferences,
    recommendation?.status,
    recommendation?.action,
    recommendation?.target?.id,
    currentness?.status,
    adviceKey,
    onSessionLost,
  ]);

  async function followRecommendation() {
    if (
      !recommendation ||
      !round ||
      !preferences ||
      !recommendation.action ||
      !recommendation.target ||
      currentness?.status !== 'current' ||
      adviceCheck?.key !== adviceKey ||
      adviceCheck.reason ||
      adviceBusy
    )
      return;
    setAdviceBusy(true);
    setAdviceError('');
    try {
      const [freshRound, freshProfile] = await Promise.all([getRound(round.id), getPreferences()]);
      const freshAdvice = readHomeRecommendation(freshRound);
      const verdict = readRecommendationCurrentness(freshRound);
      if (
        !freshAdvice ||
        verdict?.status !== 'current' ||
        freshAdvice.action !== recommendation.action ||
        freshAdvice.target?.id !== recommendation.target.id ||
        freshAdvice.target?.revision !== recommendation.target.revision ||
        freshAdvice.target?.contentSha256 !== recommendation.target.contentSha256 ||
        freshAdvice.target?.updatedAt !== recommendation.target.updatedAt ||
        freshAdvice.target?.kind !== recommendation.target.kind
      ) {
        setRound(freshRound);
        setAdviceError(
          'Saved advice changed or is now historical. Refresh and review the current work.',
        );
        return;
      }
      const stale = await checkRecommendationTarget(freshAdvice, freshRound, freshProfile.version);
      if (stale) {
        setPreferences(freshProfile);
        setRound(freshRound);
        setAdviceError(stale);
        return;
      }
      if (freshAdvice.action === 'prepare') {
        onOpenPreparation(freshAdvice.target!.id);
      } else if (freshAdvice.action === 'review_pack') {
        onOpenPack(freshAdvice.target!.opportunityId!, freshAdvice.target!.id);
      } else if (freshAdvice.action === 'review_result') {
        setReviewedResultId(freshRound.id);
        window.requestAnimationFrame(() =>
          document.getElementById('saved-round-result')?.scrollIntoView(),
        );
      } else if (freshAdvice.action === 'review_comparison') {
        setFocusedComparisonId(freshAdvice.target!.id);
        window.requestAnimationFrame(() =>
          document.getElementById('saved-offer-comparison')?.scrollIntoView(),
        );
      } else if (freshAdvice.action === 'review_delivery') {
        const review = await getDeliveryReview(freshAdvice.target!.id);
        const opportunityId = review.items.find(
          (item) => item.roundId === freshRound.id,
        )?.opportunityId;
        if (!opportunityId)
          throw new Error('The saved delivery review has no item for this round.');
        localStorage.setItem('jobseek.delivery-review-id', review.id);
        onOpenOpportunity(opportunityId);
      } else if (
        freshAdvice.action === 'review_interview' ||
        freshAdvice.action === 'review_debrief'
      ) {
        const interviewId =
          freshAdvice.action === 'review_interview'
            ? freshAdvice.target!.id
            : freshRound.scope.resources
                .find((ref) => ref.startsWith('interview:'))
                ?.slice('interview:'.length);
        const saved = await listInterviews();
        const interview = saved.find((item) => item.id === interviewId);
        if (!interview) throw new Error('The saved interview is unavailable.');
        onOpenInterview(
          interview.opportunityId,
          interview.id,
          freshAdvice.action === 'review_debrief' ? freshAdvice.target!.id : undefined,
        );
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else
        setAdviceError(`${message(cause)} No recommended action was taken; refresh current work.`);
    } finally {
      setAdviceBusy(false);
    }
  }

  const recommendedActionLabel = {
    prepare: 'Open selected role to prepare its application',
    review_pack: 'Open exact saved application pack',
    review_result: 'Review this saved round result',
    review_comparison: 'Open exact saved offer comparison',
    review_delivery: 'Open exact saved delivery review',
    review_interview: 'Open exact saved interview brief',
    review_debrief: 'Open exact saved interview debrief',
  }[recommendation?.action || 'review_result'];

  const reportSummary =
    round && typeof round.report.summary === 'string' ? round.report.summary : '';
  return (
    <>
      <Briefing
        round={round}
        reportSummary={reportSummary}
        advice={
          recommendation?.status === 'selected'
            ? {
                label: recommendedActionLabel,
                available:
                  recommendation.status === 'selected' &&
                  currentness?.status === 'current' &&
                  adviceCheck?.key === adviceKey &&
                  !adviceCheck.reason,
                busy: adviceBusy || busy,
              }
            : null
        }
        adviceCode={currentness?.status === 'stale' ? currentness.code : recommendation?.code}
        loading={loading}
        busy={busy}
        error={error}
        actionError={actionError}
        adviceError={
          adviceError ||
          (adviceCheck?.key === adviceKey ? adviceCheck.reason || '' : '') ||
          (recommendation?.status === 'unresolved'
            ? 'The assessment did not choose a next action.'
            : recommendation?.status === 'unavailable'
              ? 'No recommended next action is available.'
              : '')
        }
        pollingActive={Boolean(round && activeStates.has(round.state))}
        pollIntervalMs={pollIntervalMs}
        briefReady={Boolean(preferences)}
        onRefresh={() => void refresh()}
        onStop={() => void act('stop')}
        onResume={() => void act('resume')}
        onFollowAdvice={() => void followRecommendation()}
        onEditBrief={() => preferences && onEditBrief(preferences.version)}
        onShowResult={() => {
          if (!round) return;
          setReviewedResultId(round.id);
          window.requestAnimationFrame(() =>
            document.getElementById('saved-round-result')?.scrollIntoView(),
          );
        }}
        deliveryRecovery={
          round ? (
            <DeliveryPausedRecovery
              key={round.id}
              round={round}
              onOpenOpportunity={onOpenOpportunity}
              onSessionLost={onSessionLost}
            />
          ) : null
        }
      />
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
                  .join('; ') || 'No role direction saved.'}
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
      {round && reviewedResultId === round.id && (
        <section className="op-card" id="saved-round-result" aria-label="Saved round result">
          <h2>Saved result from this round</h2>
          <p>{reportSummary || round.intent}</p>
          {typeof round.report.code === 'string' && (
            <p>Result: {round.report.code.replaceAll('_', ' ')}.</p>
          )}
          {typeof round.report.packId === 'string' && (
            <p>
              Saved pack: <code>{round.report.packId}</code>
              {typeof round.report.version === 'number' ? ` · version ${round.report.version}` : ''}
              .
            </p>
          )}
          {typeof round.report.comparisonId === 'string' && (
            <p>
              Saved offer comparison: <code>{round.report.comparisonId}</code> · qualitative status{' '}
              {String(round.report.tradeoffStatus || 'unknown').replaceAll('_', ' ')}.
            </p>
          )}
          {typeof round.report.interviewId === 'string' && (
            <p>
              Saved interview brief: <code>{round.report.interviewId}</code>.
            </p>
          )}
          {typeof round.report.debriefId === 'string' && (
            <p>
              Saved owner-reported debrief: <code>{round.report.debriefId}</code>.
            </p>
          )}
          {round.outcome === 'deliver' && (
            <p>
              Delivery status: {round.deliverableStatus.replaceAll('_', ' ') || 'unverified'}.
              Employer receipt is not confirmed here.
            </p>
          )}
          {round.outcome === 'process_input' && <ProcessInputReport round={round} />}
          {Array.isArray(round.report.unknowns) && round.report.unknowns.length > 0 && (
            <div>
              <h3>Stated unknowns</h3>
              <ul>
                {round.report.unknowns
                  .filter((value): value is string => typeof value === 'string')
                  .map((value, index) => (
                    <li key={index}>{value}</li>
                  ))}
              </ul>
            </div>
          )}
          {Array.isArray(round.report.unresolved) && round.report.unresolved.length > 0 && (
            <div>
              <h3>Unresolved facts</h3>
              <ul>
                {round.report.unresolved
                  .filter((value): value is string => typeof value === 'string')
                  .map((value, index) => (
                    <li key={index}>{value}</li>
                  ))}
              </ul>
            </div>
          )}
          {round.unresolved.length > 0 && (
            <p>
              {round.unresolved.length} execution attempt{round.unresolved.length === 1 ? '' : 's'}{' '}
              remain unresolved.
            </p>
          )}
          <p className="hint">
            This view opens saved work only. Any new commission or delivery remains a separate owner
            action.
          </p>
        </section>
      )}
      <div id="saved-offer-comparison">
        <OfferComparisonPanel
          session={session}
          onSessionLost={onSessionLost}
          focusResultId={focusedComparisonId}
        />
      </div>
      <ResearchRunPanel session={session} onSessionLost={onSessionLost} />
      {round?.outcome === 'prepare' && (
        <section className="op-card" id="saved-preparation" aria-label="Application preparation">
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
    </>
  );
}
