import { useCallback, useEffect, useRef, useState } from 'react';
import {
  compareOffersRound,
  getActiveRound,
  getLatestCompletedOfferRound,
  getOfferComparisonByRound,
  getRound,
  isUnauthenticated,
  RequestError,
  stopRound,
  type CompareOffersRoundRequest,
  type ExactOfferComparisonResult,
  type Round,
  type Session,
} from './api';

const draftKey = 'jobseek.offer-comparison-draft';
const pendingKey = 'jobseek.offer-comparison-pending';
const rejectionKey = 'jobseek.offer-comparison-rejected';
const roundKey = 'jobseek.offer-comparison-round';
const intakeKey = 'jobseek.offer-comparison-intake';
const activeStates = new Set<Round['state']>([
  'queued',
  'running',
  'awaiting_input',
  'stopping',
  'paused',
]);
const runningStates = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
const pollMs = 5000;
const message = (cause: unknown) =>
  cause instanceof Error ? cause.message : 'The request could not be completed.';
const definite = (cause: unknown) =>
  cause instanceof RequestError && [400, 403, 409, 422].includes(cause.status);

function stored(key: string) {
  try {
    return localStorage.getItem(key) || '';
  } catch {
    return '';
  }
}
function save(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    /* Current page input remains visible. */
  }
}
function readDraft(): { offers: string[]; prioritiesText: string } {
  try {
    const value = JSON.parse(stored(draftKey)) as { offers?: unknown; prioritiesText?: unknown };
    return {
      offers:
        Array.isArray(value.offers) &&
        value.offers.length >= 1 &&
        value.offers.length <= 5 &&
        value.offers.every((item) => typeof item === 'string')
          ? value.offers
          : [''],
      prioritiesText: typeof value.prioritiesText === 'string' ? value.prioritiesText : '',
    };
  } catch {
    return { offers: [''], prioritiesText: '' };
  }
}
function readPending(): CompareOffersRoundRequest | null {
  try {
    const value = JSON.parse(stored(pendingKey)) as CompareOffersRoundRequest;
    return typeof value.requestKey === 'string' &&
      Array.isArray(value.offers) &&
      value.offers.length >= 1 &&
      value.offers.length <= 5 &&
      value.offers.every((item) => typeof item === 'string')
      ? value
      : null;
  } catch {
    return null;
  }
}

type Comparison = ExactOfferComparisonResult;
type Citation = Comparison['comparison']['input']['offers'][number]['employerCitation'];

function CitationView({
  citation,
  sources,
}: {
  citation?: Citation;
  sources: Map<string, string>;
}) {
  if (!citation) return null;
  return (
    <details className="offer-citation">
      <summary>Source citation</summary>
      <p>
        {sources.get(citation.sourceId) || citation.sourceId}: “{citation.excerpt}”
      </p>
    </details>
  );
}

function scaledInteger(value: string, scale: bigint, digits: number): string {
  const integer = BigInt(value);
  const sign = integer < 0n ? '-' : '';
  const absolute = integer < 0n ? -integer : integer;
  const whole = (absolute / scale).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const fraction = (absolute % scale).toString().padStart(digits, '0');
  return `${sign}${whole}${fraction === '0'.repeat(digits) ? '' : `.${fraction.replace(/0+$/, '')}`}`;
}

function exactCents(
  value: NonNullable<Comparison['comparison']['views'][number]['reported']>['min'],
  currency?: string,
): string {
  const numerator = BigInt(value.numerator);
  const denominator = BigInt(value.denominator);
  if (denominator <= 0n) return 'Invalid exact amount';
  if (numerator % denominator !== 0n)
    return `${value.numerator}/${value.denominator} cents${currency ? ` ${currency}` : ''}`;
  const cents = numerator / denominator;
  const sign = cents < 0n ? '-' : '';
  const absolute = cents < 0n ? -cents : cents;
  const units = (absolute / 100n).toString().replace(/\B(?=(\d{3})+(?!\d))/g, ',');
  const decimals = (absolute % 100n).toString().padStart(2, '0');
  return `${sign}${units}.${decimals}${currency ? ` ${currency}` : ''}`;
}

function ExactRange({
  value,
  currency,
}: {
  value: Comparison['comparison']['views'][number]['reported'];
  currency?: string;
}) {
  if (!value) return <span>Not calculated</span>;
  return (
    <span>
      {value.kind === 'from' ? 'From ' : ''}
      {exactCents(value.min, currency)}
      {value.max ? ` to ${exactCents(value.max, currency)}` : ''}
    </span>
  );
}

function ComparisonView({ result }: { result: Comparison }) {
  const snapshot = result.comparison;
  const names = new Map(snapshot.input.sources.map((source) => [source.id, source.kind]));
  const offerNames = new Map(
    snapshot.input.offers.map((offer) => [offer.id, offer.employer || offer.id]),
  );
  const payViews = new Map(snapshot.views.map((view) => [view.offerId, view]));
  const missing = new Map(snapshot.missing.map((item) => [item.offerId, item.terms]));
  const selected =
    result.tradeoffStatus === 'selected' &&
    result.tradeoff?.selection.disposition === 'selected' &&
    result.tradeoff.alternative?.id === result.tradeoff.selection.selected_id
      ? result.tradeoff.alternative
      : null;
  return (
    <div className="offer-result">
      <h3>Saved whole-offer comparison</h3>
      <p className={result.current ? 'hint' : 'error'}>
        {result.current
          ? 'Current against the campaign brief used for this comparison.'
          : 'Historical comparison: the campaign brief changed. Review current context before relying on it.'}
      </p>
      <p>
        This is a private review of supplied terms. No offer has been accepted, negotiated, or sent.
      </p>
      <h4>Offered terms and exact pay basis</h4>
      <div className="offer-result-grid">
        {snapshot.input.offers.map((offer) => {
          const view = payViews.get(offer.id);
          return (
            <article className="offer-result-item" key={offer.id}>
              <h5>{offer.employer || 'Employer unconfirmed'}</h5>
              <CitationView citation={offer.employerCitation} sources={names} />
              <p>
                <strong>Engagement:</strong> {offer.engagement.replaceAll('_', ' ')}
              </p>
              <CitationView citation={offer.engagementCitation} sources={names} />
              <p>
                <strong>Reported pay:</strong>{' '}
                {view?.reported ? (
                  <ExactRange
                    value={view.reported}
                    currency={view.currency || offer.pay.currency}
                  />
                ) : (
                  `${view?.reportedText || offer.pay.rawAmountText || 'Unknown'} · ${view?.currency || offer.pay.currency || 'currency unknown'}`
                )}{' '}
                · {view?.period || offer.pay.period} · {view?.basis || offer.pay.basis}
              </p>
              <CitationView citation={offer.pay.citation} sources={names} />
              {view?.monthlyEquivalent && (
                <p>
                  <strong>Monthly equivalent:</strong>{' '}
                  <ExactRange value={view.monthlyEquivalent} currency={view.currency} />
                  {view.monthlyAssumption ? ` · ${view.monthlyAssumption}` : ''}
                </p>
              )}
              <CitationView citation={offer.pay.conversionCitation} sources={names} />
              <p>
                <strong>Weekly hours:</strong>{' '}
                {offer.hours.weeklyHundredths
                  ? `${scaledInteger(offer.hours.weeklyHundredths, 100n, 2)} hours`
                  : 'Unknown'}
                {' · '}
                <strong>Holiday:</strong> {offer.holiday.treatment}
                {offer.holiday.rateBps
                  ? ` · ${scaledInteger(offer.holiday.rateBps, 100n, 2)}%`
                  : ''}
              </p>
              <CitationView citation={offer.hours.citation} sources={names} />
              <CitationView citation={offer.holiday.citation} sources={names} />
              {[...(offer.benefits || []), ...(offer.arrangement || [])].map((term, index) => (
                <div key={`${index}-${term.text}`}>
                  <p>{term.text}</p>
                  {term.citations.map((citation, citationIndex) => (
                    <CitationView
                      key={`${citation.sourceId}-${citationIndex}`}
                      citation={citation}
                      sources={names}
                    />
                  ))}
                </div>
              ))}
              {[...(offer.unknowns || []), ...(missing.get(offer.id) || [])].length > 0 && (
                <div>
                  <strong>Missing or unconfirmed terms</strong>
                  <ul>
                    {[...(offer.unknowns || []), ...(missing.get(offer.id) || [])].map(
                      (term, index) => (
                        <li key={`${index}-${term}`}>{term}</li>
                      ),
                    )}
                  </ul>
                </div>
              )}
            </article>
          );
        })}
      </div>
      <h4>Server supplied pay comparisons</h4>
      {snapshot.pay.length ? (
        <ul>
          {snapshot.pay.map((pair) => (
            <li key={`${pair.leftId}-${pair.rightId}`}>
              <strong>
                {offerNames.get(pair.leftId) || pair.leftId} and{' '}
                {offerNames.get(pair.rightId) || pair.rightId}: {pair.status.replaceAll('_', ' ')}
              </strong>
              . {pair.reason}
              {pair.status === 'comparable' && pair.deltaRightMinusLeft && (
                <p>
                  Left: <ExactRange value={pair.left} currency={pair.currency} /> · right:{' '}
                  <ExactRange value={pair.right} currency={pair.currency} /> · right minus left:{' '}
                  <ExactRange value={pair.deltaRightMinusLeft} currency={pair.currency} /> ·{' '}
                  {pair.period || 'period unknown'}
                </p>
              )}
            </li>
          ))}
        </ul>
      ) : (
        <p>No pairwise pay comparison was saved.</p>
      )}
      <h4>Qualitative review</h4>
      <p role="status">
        {result.tradeoffStatus === 'selected' && selected
          ? `Jev selected one cited issue to review: ${selected.why.text}`
          : result.tradeoffStatus === 'selected'
            ? 'A selected issue was recorded, but its matching saved alternative is unavailable.'
            : result.tradeoffStatus === 'unresolved'
              ? 'Jev left the qualitative review unresolved. The sourced terms and exact pay comparison remain available.'
              : result.tradeoffStatus === 'pending'
                ? 'Qualitative review is pending.'
                : `Qualitative review status: ${result.tradeoffStatus.replaceAll('_', ' ')}.`}
      </p>
      {selected?.why.citations.map((citation, index) => (
        <CitationView key={`${citation.sourceId}-${index}`} citation={citation} sources={names} />
      ))}
      {snapshot.input.alternatives?.length ? (
        <details>
          <summary>Saved review alternatives and evidence</summary>
          <ul>
            {snapshot.input.alternatives.map((alternative) => (
              <li key={alternative.id}>
                {alternative.kind}: {alternative.why.text}
                {alternative.why.citations.map((citation, index) => (
                  <CitationView
                    key={`${citation.sourceId}-${index}`}
                    citation={citation}
                    sources={names}
                  />
                ))}
              </li>
            ))}
          </ul>
        </details>
      ) : null}
      <details>
        <summary>Original offer sources and audit</summary>
        <p>
          Comparison <code>{result.id}</code> · round <code>{result.roundId}</code> · input SHA-256{' '}
          <code>{snapshot.inputSha256}</code>
        </p>
        {snapshot.input.sources.map((source) => (
          <div key={source.id}>
            <p>
              {source.kind} · revision {source.revision} · <code>{source.id}</code>
            </p>
            <pre className="op-source">{source.body}</pre>
          </div>
        ))}
      </details>
    </div>
  );
}

export function OfferComparisonPanel({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [draft, setDraft] = useState(readDraft);
  const [pending, setPending] = useState(readPending);
  const [rejected, setRejected] = useState(() => stored(rejectionKey) === 'true');
  const [round, setRound] = useState<Round | null>(null);
  const [otherActive, setOtherActive] = useState<Round | null>(null);
  const [result, setResult] = useState<Comparison | null>(null);
  const [loading, setLoading] = useState(true);
  const [commissionBusy, setCommissionBusy] = useState(false);
  const [controlBusy, setControlBusy] = useState(false);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [notice, setNotice] = useState('');
  const commissionLock = useRef(false);
  const controlLock = useRef(false);
  const readVersion = useRef(0);

  const refresh = useCallback(
    async (quiet = false): Promise<boolean> => {
      const version = ++readVersion.current;
      if (!quiet) {
        setLoading(true);
        setError('');
      }
      try {
        const [active, latest] = await Promise.all([
          getActiveRound(),
          getLatestCompletedOfferRound(),
        ]);
        const rememberedId = stored(roundKey);
        let remembered: Round | null = null;
        if (rememberedId && rememberedId !== active?.id && rememberedId !== latest?.id) {
          try {
            remembered = await getRound(rememberedId);
          } catch (cause) {
            if (!(cause instanceof RequestError && cause.status === 404)) throw cause;
          }
        }
        const knownIntake = stored(intakeKey);
        const savedRequest = readPending();
        const ownActive =
          active?.outcome === 'compare_offers' &&
          (savedRequest
            ? active.requestKey === savedRequest.requestKey
            : active.id === rememberedId ||
              (knownIntake && active.scope.inputRefs.includes(`offer_intake:${knownIntake}`)))
            ? active
            : null;
        const chosen = ownActive || remembered || latest;
        let comparison: Comparison | null = null;
        if (chosen?.outcome === 'compare_offers') {
          try {
            comparison = await getOfferComparisonByRound(chosen.id);
          } catch (cause) {
            if (!(cause instanceof RequestError && cause.status === 404)) throw cause;
          }
        }
        if (version !== readVersion.current) return false;
        setRound(chosen?.outcome === 'compare_offers' ? chosen : null);
        setOtherActive(active && active.id !== ownActive?.id ? active : null);
        setResult(comparison);
        setError('');
        return true;
      } catch (cause) {
        if (version !== readVersion.current) return false;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(`${message(cause)} Saved offer comparison can be retried.`);
        return false;
      } finally {
        if (version === readVersion.current) setLoading(false);
      }
    },
    [onSessionLost],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);
  useEffect(() => {
    if (
      !pending &&
      !(round && activeStates.has(round.state)) &&
      !(otherActive && activeStates.has(otherActive.state))
    )
      return;
    const timer = window.setInterval(() => void refresh(true), pollMs);
    return () => window.clearInterval(timer);
  }, [pending, round?.id, round?.state, otherActive?.id, otherActive?.state, refresh]);

  function changeDraft(next: typeof draft) {
    setDraft(next);
    save(draftKey, JSON.stringify(next));
  }

  async function commission() {
    if (
      commissionLock.current ||
      controlLock.current ||
      (!pending && otherActive && activeStates.has(otherActive.state))
    )
      return;
    const input = pending || {
      requestKey: crypto.randomUUID(),
      offers: draft.offers,
      ...(draft.prioritiesText.trim() ? { prioritiesText: draft.prioritiesText } : {}),
    };
    const bytes = new TextEncoder();
    if (
      input.offers.some((offer) => !offer.trim() || bytes.encode(offer).length > 20000) ||
      input.offers.length < 1 ||
      input.offers.length > 5 ||
      bytes.encode(input.prioritiesText || '').length > 5000 ||
      input.offers.reduce(
        (sum, offer) => sum + bytes.encode(offer).length,
        bytes.encode(input.prioritiesText || '').length,
      ) > 28000
    ) {
      setActionError(
        'Use one to five complete offers, up to 20,000 bytes each and 28,000 bytes total. Priorities can use up to 5,000 bytes.',
      );
      return;
    }
    commissionLock.current = true;
    setCommissionBusy(true);
    setActionError('');
    setNotice('');
    save(pendingKey, JSON.stringify(input));
    save(rejectionKey, '');
    setPending(input);
    setRejected(false);
    try {
      const created = await compareOffersRound(input, session.csrfToken);
      save(roundKey, created.round.id);
      save(intakeKey, created.intakeId);
      save(pendingKey, '');
      save(rejectionKey, '');
      save(draftKey, '');
      setPending(null);
      setDraft({ offers: [''], prioritiesText: '' });
      setRound(created.round);
      window.dispatchEvent(new Event('jobseek:round-commissioned'));
      setNotice('Offer comparison commissioned. The saved result will appear when ready.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else {
        const isRejected = definite(cause);
        setRejected(isRejected);
        save(rejectionKey, isRejected ? 'true' : '');
        setActionError(
          isRejected
            ? `${message(cause)} The server rejected this request. Review current work before revising the saved offers.`
            : `${message(cause)} The response is uncertain. Recover with the same key and exact offer text.`,
        );
      }
    } finally {
      commissionLock.current = false;
      setCommissionBusy(false);
      void refresh(true);
    }
  }

  async function revise() {
    if (!pending || !rejected || commissionBusy || !(await refresh())) return;
    save(pendingKey, '');
    save(rejectionKey, '');
    setPending(null);
    setRejected(false);
    setActionError('Current work was refreshed. Revise the saved offer text before a new request.');
  }

  async function stop() {
    if (!round || controlLock.current) return;
    controlLock.current = true;
    setControlBusy(true);
    setActionError('');
    try {
      const changed = await stopRound(round.id, session.csrfToken);
      setRound(changed);
      window.dispatchEvent(new Event('jobseek:round-commissioned'));
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setActionError(message(cause));
    } finally {
      controlLock.current = false;
      setControlBusy(false);
      void refresh(true);
    }
  }

  const blocked = Boolean(
    (otherActive && activeStates.has(otherActive.state)) ||
    (round && activeStates.has(round.state)),
  );
  return (
    <section className="op-card" aria-label="Whole-offer comparison">
      <div className="op-heading-row">
        <h2>Compare whole offers</h2>
        <button
          className="secondary"
          type="button"
          disabled={loading}
          onClick={() => void refresh()}
        >
          Refresh offer comparison
        </button>
      </div>
      <p>
        Paste each complete offer. Codex extracts cited terms and the server compares exact pay
        where the terms permit it. Optional priorities are context for private review.
      </p>
      {loading && <p role="status">Reading saved comparison…</p>}
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
      {notice && <p role="status">{notice}</p>}
      {otherActive && activeStates.has(otherActive.state) && (
        <p role="status">
          Another {otherActive.outcome.replaceAll('_', ' ')} round is{' '}
          {otherActive.state.replaceAll('_', ' ')}. Your offer text remains saved.{' '}
          <button
            className="secondary"
            type="button"
            onClick={() =>
              document
                .querySelector('[aria-label="Agency work"]')
                ?.scrollIntoView({ behavior: 'smooth' })
            }
          >
            Open current round controls
          </button>
        </p>
      )}
      {round && (
        <div className="offer-round">
          <p role="status">
            Comparison work: {round.state.replaceAll('_', ' ')} ·{' '}
            {round.step || 'step not reported'}
          </p>
          {runningStates.has(round.state) && (
            <button
              type="button"
              disabled={controlBusy || round.state === 'stopping'}
              onClick={() => void stop()}
            >
              Stop offer comparison
            </button>
          )}
          {round.state === 'paused' && (
            <p>Resume this round from the agency work controls above.</p>
          )}
        </div>
      )}
      <div className="offer-input-list">
        {draft.offers.map((offer, index) => (
          <label className="interview-input" key={index}>
            Complete offer {index + 1}
            <textarea
              value={offer}
              rows={5}
              maxLength={20000}
              readOnly={Boolean(pending)}
              onChange={(event) =>
                changeDraft({
                  ...draft,
                  offers: draft.offers.map((value, at) =>
                    at === index ? event.target.value : value,
                  ),
                })
              }
              placeholder="Paste the whole offer including pay, hours, holiday, and conditions."
            />
          </label>
        ))}
      </div>
      <div className="button-row">
        <button
          type="button"
          className="secondary"
          disabled={Boolean(pending) || draft.offers.length >= 5}
          onClick={() => changeDraft({ ...draft, offers: [...draft.offers, ''] })}
        >
          Add another whole offer
        </button>
        {draft.offers.length > 1 && (
          <button
            type="button"
            className="secondary"
            disabled={Boolean(pending)}
            onClick={() => changeDraft({ ...draft, offers: draft.offers.slice(0, -1) })}
          >
            Remove last offer
          </button>
        )}
      </div>
      <label className="interview-input">
        Priorities context (optional)
        <textarea
          value={draft.prioritiesText}
          rows={3}
          maxLength={5000}
          readOnly={Boolean(pending)}
          onChange={(event) => changeDraft({ ...draft, prioritiesText: event.target.value })}
          placeholder="Paste the complete priorities you want considered."
        />
      </label>
      {pending && (
        <p role="status">
          An exact offer comparison request is saved.{' '}
          {rejected
            ? 'The server rejected it; review current work before revising.'
            : 'Its response is uncertain; recovery uses the same key and text.'}
        </p>
      )}
      <div className="button-row">
        {!pending && (
          <button
            type="button"
            disabled={
              commissionBusy || loading || blocked || draft.offers.some((offer) => !offer.trim())
            }
            onClick={() => void commission()}
          >
            Compare whole offers
          </button>
        )}
        {pending && !rejected && (
          <button type="button" disabled={commissionBusy} onClick={() => void commission()}>
            Recover same offer comparison request
          </button>
        )}
        {pending && rejected && (
          <button
            type="button"
            className="secondary"
            disabled={commissionBusy || loading || blocked}
            onClick={() => void revise()}
          >
            Review rejection and revise offers
          </button>
        )}
      </div>
      {result ? (
        <ComparisonView result={result} />
      ) : round ? (
        <p>No saved comparison is available for this round yet. Refresh after work completes.</p>
      ) : (
        <p>No offer comparison has been saved yet.</p>
      )}
    </section>
  );
}
