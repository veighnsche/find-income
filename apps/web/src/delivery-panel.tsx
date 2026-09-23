import { useCallback, useEffect, useRef, useState } from 'react';
import {
  applicationPackPdfUrl,
  approveDeliveryReview,
  closeDeliveryReview,
  getActiveRound,
  getDeliveryCapability,
  getDeliveryReview,
  getRound,
  isUnauthenticated,
  listOpportunityRoutes,
  prepareDeliveryReview,
  reconcileDeliveryReview,
  RequestError,
  sendDeliveryReview,
  stopRound,
  type ApplicationPackSummary,
  type DeliveryCapability,
  type DeliveryItem,
  type DeliveryReview,
  type Opportunity,
  type OpportunityRoute,
  type PrepareDeliveryReviewRequest,
  type Round,
  type Session,
} from './api';

const batchKey = 'jobseek.delivery-batch';
const reviewKey = 'jobseek.delivery-review-id';
const historyKey = 'jobseek.delivery-review-history';
const prepareKey = 'jobseek.delivery-prepare-request';
const sendKey = 'jobseek.delivery-send-requested';
const pollMs = 5000;
type BatchEntry = { packId: string; opportunityId: string; title: string; version: number };

function readBatch(): BatchEntry[] {
  try {
    const value: unknown = JSON.parse(localStorage.getItem(batchKey) || '[]');
    return Array.isArray(value)
      ? value
          .filter(
            (entry): entry is BatchEntry =>
              typeof entry?.packId === 'string' &&
              typeof entry?.opportunityId === 'string' &&
              typeof entry?.title === 'string' &&
              typeof entry?.version === 'number',
          )
          .slice(0, 3)
      : [];
  } catch {
    return [];
  }
}

function readValue(key: string): string {
  try {
    return localStorage.getItem(key) || '';
  } catch {
    return '';
  }
}

function readHistory(): string[] {
  try {
    const value: unknown = JSON.parse(readValue(historyKey) || '[]');
    return Array.isArray(value)
      ? value.filter((id): id is string => typeof id === 'string').slice(0, 20)
      : [];
  } catch {
    return [];
  }
}

function readPendingPrepare(): PrepareDeliveryReviewRequest | null {
  try {
    const value: unknown = JSON.parse(readValue(prepareKey) || 'null');
    if (
      value &&
      typeof value === 'object' &&
      'requestKey' in value &&
      'packIds' in value &&
      typeof value.requestKey === 'string' &&
      Array.isArray(value.packIds) &&
      value.packIds.length >= 1 &&
      value.packIds.length <= 3 &&
      value.packIds.every((id) => typeof id === 'string')
    )
      return value as PrepareDeliveryReviewRequest;
  } catch {
    /* A corrupt saved request cannot be replayed. */
  }
  return null;
}

function saveValue(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    /* The current page still retains the identity. */
  }
}

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'The request could not be completed.';
}

function stateText(item: DeliveryItem): string {
  switch (item.state) {
    case 'accepted_by_smtp':
      return 'Accepted by the outgoing SMTP server. Employer receipt is unverified.';
    case 'uncertain':
      return 'Submission outcome unknown. It may have been accepted; do not resend.';
    case 'sending':
      return 'Submission in progress. Its outcome is not yet known.';
    case 'failed':
      return 'Submission failed. This review will not be sent again.';
    default:
      return 'Prepared for owner review; no submission attempted.';
  }
}

function DeliveryMaterial({ item }: { item: DeliveryItem }) {
  return (
    <article className="delivery-item">
      <h3>
        {item.title} · {item.companyName}
      </h3>
      <p role="status">
        <strong>{stateText(item)}</strong>
      </p>
      <p className={item.current ? 'hint' : 'error'}>
        {item.current
          ? 'Saved pack, role source, brief and route match the current records.'
          : `Review is stale or blocked: ${item.blockingReason || 'currentness could not be confirmed'}.`}
      </p>
      <dl className="delivery-facts">
        <div>
          <dt>To</dt>
          <dd>{item.recipient}</dd>
        </div>
        <div>
          <dt>From</dt>
          <dd>{item.sender}</dd>
        </div>
        <div>
          <dt>Subject</dt>
          <dd>{item.subject}</dd>
        </div>
        <div>
          <dt>Route evidence</dt>
          <dd>{item.routeExcerpt || 'No excerpt recorded.'}</dd>
        </div>
        <div>
          <dt>PDF attachment</dt>
          <dd>
            <a href={applicationPackPdfUrl(item.packId)} target="_blank" rel="noopener noreferrer">
              Open saved pack PDF
            </a>
          </dd>
        </div>
      </dl>
      <h4>Exact message body</h4>
      <pre className="delivery-body">{item.body}</pre>
      <details>
        <summary>Delivery audit details</summary>
        <p>
          Pack <code>{item.packId}</code> · route <code>{item.routeId}</code> revision{' '}
          {item.routeRevision}
        </p>
        <p>
          Attached PDF SHA-256 <code>{item.attachmentSha256}</code>
        </p>
        <p>
          Canonical MIME SHA-256 <code>{item.mimeSha256}</code> · message ID{' '}
          <code>{item.messageId}</code>
        </p>
      </details>
      {(item.smtpStage || item.smtpCode || item.outcomeDetail) && (
        <p className="hint">
          SMTP {item.smtpStage || 'stage unknown'}
          {item.smtpCode ? ` · code ${item.smtpCode}` : ''}
          {item.outcomeDetail ? ` · ${item.outcomeDetail}` : ''}
        </p>
      )}
    </article>
  );
}

export function DeliveryPanel({
  opportunity,
  selected,
  selectedPack,
  selectedPackCurrent,
  session,
  onSessionLost,
}: {
  opportunity: Opportunity;
  selected: boolean;
  selectedPack: ApplicationPackSummary | null;
  selectedPackCurrent: boolean;
  session: Session;
  onSessionLost: () => void;
}) {
  const [batch, setBatch] = useState<BatchEntry[]>(readBatch);
  const [reviewId, setReviewId] = useState(() => readValue(reviewKey));
  const [history, setHistory] = useState<string[]>(readHistory);
  const [pendingPrepare, setPendingPrepare] = useState<PrepareDeliveryReviewRequest | null>(
    readPendingPrepare,
  );
  const [review, setReview] = useState<DeliveryReview | null>(null);
  const [round, setRound] = useState<Round | null>(null);
  const [otherActive, setOtherActive] = useState<Round | null>(null);
  const [capability, setCapability] = useState<DeliveryCapability | null>(null);
  const [routes, setRoutes] = useState<OpportunityRoute[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [sendPending, setSendPending] = useState(false);
  const [statusRead, setStatusRead] = useState(false);
  const [prepareRejected, setPrepareRejected] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [sendRequested, setSendRequested] = useState(() => readValue(sendKey));
  const inFlight = useRef(false);
  const sendInFlight = useRef(false);

  const rememberReview = (id: string) => {
    saveValue(reviewKey, id);
    setReviewId(id);
  };
  const rememberHistory = (id: string) => {
    const next = [id, ...history.filter((old) => old !== id)].slice(0, 20);
    saveValue(historyKey, JSON.stringify(next));
    setHistory(next);
  };
  const rememberSend = (id: string) => {
    saveValue(sendKey, id);
    setSendRequested(id);
  };
  const updateBatch = (next: BatchEntry[]) => {
    setBatch(next);
    saveValue(batchKey, JSON.stringify(next));
  };

  const refresh = useCallback(
    async (id = reviewId, quiet = false) => {
      if (inFlight.current) return;
      if (!quiet) {
        setLoading(true);
        setError('');
      }
      try {
        const [available, routeList, currentReview, active] = await Promise.all([
          getDeliveryCapability(),
          listOpportunityRoutes(opportunity.id),
          id ? getDeliveryReview(id) : Promise.resolve(null),
          getActiveRound(),
        ]);
        setCapability(available);
        setRoutes(routeList);
        setReview(currentReview);
        let ownRound =
          active?.outcome === 'deliver' && active.scope.inputRefs.includes(`delivery_review:${id}`)
            ? active
            : null;
        const savedRoundId = currentReview?.items.find((item) => item.roundId)?.roundId;
        if (!ownRound && savedRoundId) ownRound = await getRound(savedRoundId);
        setRound(ownRound);
        setOtherActive(active && active.id !== ownRound?.id ? active : null);
        setStatusRead(true);
        return true;
      } catch (cause) {
        if (isUnauthenticated(cause)) onSessionLost();
        else {
          setStatusRead(false);
          setError(
            `${message(cause)} The saved review identity remains available for another status read.`,
          );
        }
        return false;
      } finally {
        setLoading(false);
      }
    },
    [reviewId, opportunity.id, onSessionLost],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);
  useEffect(() => {
    if (
      !reviewId ||
      (round && !['queued', 'running', 'stopping', 'awaiting_input'].includes(round.state))
    )
      return;
    const timer = window.setInterval(() => void refresh(reviewId, true), pollMs);
    return () => window.clearInterval(timer);
  }, [reviewId, round?.state, refresh]);

  async function prepare(packIds: string[]) {
    if (
      inFlight.current ||
      reviewId ||
      packIds.length < 1 ||
      packIds.length > 3 ||
      (pendingPrepare && pendingPrepare.packIds.join('\0') !== packIds.join('\0'))
    )
      return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    setNotice('');
    setPrepareRejected(false);
    try {
      const input = pendingPrepare || { requestKey: crypto.randomUUID(), packIds };
      saveValue(prepareKey, JSON.stringify(input));
      setPendingPrepare(input);
      const prepared = await prepareDeliveryReview(input, session.csrfToken);
      rememberReview(prepared.id);
      saveValue(prepareKey, '');
      setPendingPrepare(null);
      setReview(prepared);
      setRound(null);
      setNotice('Exact material prepared. Read every item before using Send.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else {
        const rejected = cause instanceof RequestError && [400, 409, 422].includes(cause.status);
        setPrepareRejected(rejected);
        setError(
          rejected
            ? `${message(cause)} The server rejected this Prepare request. Review current packs and routes before starting a revised request.`
            : `${message(cause)} The result is uncertain. Recover this exact Prepare request with its saved key.`,
        );
      }
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  async function reviewRejectedPrepare() {
    if (!pendingPrepare || !prepareRejected || inFlight.current) return;
    if (!(await refresh())) return;
    saveValue(prepareKey, '');
    setPendingPrepare(null);
    setPrepareRejected(false);
    setNotice(
      'Current delivery status was refreshed. Choose exact current packs for a new review.',
    );
  }

  async function send(replay = false) {
    if (
      inFlight.current ||
      sendInFlight.current ||
      !review ||
      (replay
        ? sendRequested !== review.id || !statusRead || Boolean(round)
        : sendRequested === review.id || !capability?.submissionAvailable) ||
      otherActive ||
      !review.items.every((item) => item.current && item.state === 'prepared')
    )
      return;
    inFlight.current = true;
    setBusy(true);
    setStatusRead(false);
    setError('');
    setNotice('');
    try {
      // The server checks this exact digest again before any submission can start.
      const approved =
        review.approvedSha256 === review.materialSha256
          ? review
          : await approveDeliveryReview(review.id, review.materialSha256, session.csrfToken);
      setReview(approved);
      if (
        approved.materialSha256 !== review.materialSha256 ||
        approved.approvedSha256 !== review.materialSha256 ||
        !approved.items.every((item) => item.current && item.state === 'prepared')
      ) {
        setError(
          'The saved review changed or became stale. Nothing was sent. Refresh and inspect the server review.',
        );
        return;
      }
      // The review ID is the backend's stable Send key. A replay uses this same command only.
      rememberSend(review.id);
      inFlight.current = false;
      setBusy(false);
      sendInFlight.current = true;
      setSendPending(true);
      const result = await sendDeliveryReview(review.id, session.csrfToken);
      setReview(result.review);
      setRound(result.round);
      window.dispatchEvent(new Event('jobseek:round-commissioned'));
      setNotice(
        'Submission attempt recorded. Read each item outcome; SMTP acceptance does not prove employer receipt.',
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else
        setError(
          sendRequested === review.id || readValue(sendKey) === review.id
            ? `${message(cause)} Send may have reached the server. Read this review and round before explicitly recovering the same Send command.`
            : `${message(cause)} Approval may have reached the server. Refresh this same review before deciding what to do next.`,
        );
    } finally {
      inFlight.current = false;
      sendInFlight.current = false;
      setBusy(false);
      setSendPending(false);
      void refresh(review.id, true);
    }
  }

  async function act(kind: 'stop' | 'close' | 'reconcile') {
    if (inFlight.current || !review) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    setNotice('');
    try {
      if (kind === 'stop' && round) setRound(await stopRound(round.id, session.csrfToken));
      if (kind === 'close') setRound(await closeDeliveryReview(review.id, session.csrfToken));
      if (kind === 'reconcile') {
        const result = await reconcileDeliveryReview(review.id, session.csrfToken);
        setNotice(
          result.supported
            ? result.reason
            : `Read-only receipt check unsupported: ${result.reason}`,
        );
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(message(cause));
    } finally {
      inFlight.current = false;
      setBusy(false);
      void refresh(review.id, true);
    }
  }

  const chosen = selectedPack && batch.some((item) => item.packId === selectedPack.id);
  const prepared = review?.items.every((item) => item.state === 'prepared');
  const canSend = Boolean(
    review &&
    !round &&
    prepared &&
    review.items.every((item) => item.current) &&
    capability?.submissionAvailable &&
    !otherActive &&
    sendRequested !== review.id,
  );
  const canReplay = Boolean(
    review &&
    sendRequested === review.id &&
    statusRead &&
    !sendPending &&
    !round &&
    !otherActive &&
    review.approvedSha256 === review.materialSha256 &&
    review.items.every((item) => item.current && item.state === 'prepared'),
  );
  const staleBeforeSend = Boolean(
    review && !sendRequested && review.items.some((item) => !item.current) && !round,
  );
  const canStartAnother = Boolean(
    staleBeforeSend ||
    (review &&
      review.items.every((item) => item.state !== 'sending') &&
      round &&
      ['completed', 'failed'].includes(round.state)),
  );
  return (
    <section className="op-card" aria-label="Application delivery">
      <div className="op-heading-row">
        <h2>Review and send applications</h2>
        <button
          type="button"
          className="secondary"
          disabled={busy || loading}
          onClick={() => void refresh()}
        >
          Refresh delivery status
        </button>
      </div>
      <p>
        Prepare builds an immutable message from saved packs and evidenced email routes. Only your
        Send action commissions delivery.
      </p>
      {loading && <p role="status">Reading delivery status…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {otherActive && (
        <p role="status">
          Another {otherActive.outcome.replaceAll('_', ' ')} commission is{' '}
          {otherActive.state.replaceAll('_', ' ')}. Finish or stop it before Send.
        </p>
      )}
      {capability && !capability.submissionAvailable && (
        <p role="status">
          Sender unavailable: an authenticated sender is not configured. Pack review is still
          available.
        </p>
      )}
      {capability && !capability.receiptLookup && (
        <p className="hint">Read-only employer receipt check unavailable. {capability.reason}</p>
      )}
      {!selected && (
        <p className="hint">
          Select this opportunity before preparing its delivery review or adding its pack to a
          batch.
        </p>
      )}
      {selectedPack && !selectedPackCurrent && (
        <p className="hint">
          This pack's role or brief snapshot is stale or could not be checked. Choose a current
          saved version before delivery review.
        </p>
      )}
      <p>
        <strong>Current route evidence:</strong>{' '}
        {routes.length
          ? `${routes.length} saved route record${routes.length === 1 ? '' : 's'}. Preparation requires one supported, current email route.`
          : 'No route record is saved. Delivery preparation is unsupported until a route is evidenced.'}
      </p>
      {routes.length > 0 && (
        <details>
          <summary>Saved route records</summary>
          <ul>
            {routes.map((route) => (
              <li key={route.id}>
                {route.kind}: {route.destinationText || 'destination unknown'} ·{' '}
                {route.sourceExcerpt || 'no source excerpt'} · revision {route.revision}
              </li>
            ))}
          </ul>
        </details>
      )}
      {!reviewId && pendingPrepare && (
        <p role="status">
          {prepareRejected
            ? 'The server rejected Prepare.'
            : 'A Prepare response was not confirmed.'}{' '}
          The exact request for {pendingPrepare.packIds.length} pack
          {pendingPrepare.packIds.length === 1 ? '' : 's'} is saved.{' '}
          {prepareRejected
            ? 'Review the current records, then start a revised request.'
            : 'Recovering it reuses its original request key and cannot create a replacement review.'}
        </p>
      )}
      {!reviewId && pendingPrepare && !prepareRejected && (
        <button type="button" disabled={busy} onClick={() => void prepare(pendingPrepare.packIds)}>
          Recover saved Prepare request
        </button>
      )}
      {!reviewId && pendingPrepare && prepareRejected && (
        <button
          type="button"
          className="secondary"
          disabled={busy || loading}
          onClick={() => void reviewRejectedPrepare()}
        >
          Review rejection and start a new Prepare request
        </button>
      )}
      {!reviewId && !pendingPrepare && (
        <>
          <div className="button-row">
            <button
              type="button"
              disabled={!selected || !selectedPack || !selectedPackCurrent || busy || loading}
              onClick={() => selectedPack && void prepare([selectedPack.id])}
            >
              Prepare exact selected pack
            </button>
            {selectedPack && (
              <button
                type="button"
                className="secondary"
                disabled={
                  !selected || !selectedPackCurrent || busy || (!chosen && batch.length >= 3)
                }
                onClick={() =>
                  updateBatch(
                    chosen
                      ? batch.filter((item) => item.packId !== selectedPack.id)
                      : [
                          ...batch,
                          {
                            packId: selectedPack.id,
                            opportunityId: opportunity.id,
                            title: opportunity.title,
                            version: selectedPack.version,
                          },
                        ],
                  )
                }
              >
                {chosen ? 'Remove selected pack from batch' : 'Add selected pack to batch'}
              </button>
            )}
          </div>
          <h3>Selected batch ({batch.length}/3)</h3>
          {batch.length ? (
            <>
              <ol>
                {batch.map((item) => (
                  <li key={item.packId}>
                    {item.title} · pack version {item.version}{' '}
                    <button
                      type="button"
                      className="secondary"
                      disabled={busy}
                      onClick={() =>
                        updateBatch(batch.filter((entry) => entry.packId !== item.packId))
                      }
                    >
                      Remove
                    </button>
                  </li>
                ))}
              </ol>
              <button
                type="button"
                disabled={busy || loading}
                onClick={() => void prepare(batch.map((item) => item.packId))}
              >
                Prepare selected batch
              </button>
            </>
          ) : (
            <p className="hint">
              Open each opportunity and add its selected saved pack. Up to three exact versions can
              be reviewed together.
            </p>
          )}
        </>
      )}
      {reviewId && !review && (
        <p>
          Recovering saved review <code>{reviewId}</code>. Preparation and Send stay unavailable
          until the server state is read.
        </p>
      )}
      {history.length > 0 && (
        <details>
          <summary>Earlier delivery reviews ({history.length})</summary>
          <ul>
            {history.map((id) => (
              <li key={id}>
                <button
                  type="button"
                  className="secondary"
                  disabled={busy || sendPending || id === reviewId}
                  onClick={() => {
                    if (reviewId) rememberHistory(reviewId);
                    rememberReview(id);
                    setReview(null);
                    setRound(null);
                    setStatusRead(false);
                  }}
                >
                  {id}
                </button>
              </li>
            ))}
          </ul>
        </details>
      )}
      {review && (
        <>
          <p className="hint">
            Your Send action approves the exact saved recipients, message bodies, and attached PDF
            bytes shown below.
          </p>
          <details>
            <summary>Review audit details</summary>
            <p>
              Review ID <code>{review.id}</code> · exact material SHA-256{' '}
              <code>{review.materialSha256}</code>
            </p>
            {review.approvedAt && <p>Approved {new Date(review.approvedAt).toLocaleString()}.</p>}
          </details>
          {review.items.map((item) => (
            <DeliveryMaterial key={item.id} item={item} />
          ))}
          {round && (
            <p role="status">
              Delivery commission: <strong>{round.state.replaceAll('_', ' ')}</strong> ·{' '}
              {round.step || 'step unavailable'} · {round.deliverableStatus || 'status unavailable'}
              {round.reconciliationRequired ? ' · reconciliation required' : ''}
            </p>
          )}
          {sendRequested === review.id && (
            <p role="status">
              A Send request was issued for this review. Status reads reveal its round and item
              outcomes. If it never arrived, an explicit recovery repeats only this review's
              idempotent Send command.
            </p>
          )}
          {sendPending && (
            <p role="status">
              Send is still processing. Status reads and Stop remain available for this exact
              commission.
            </p>
          )}
          <div className="button-row">
            {canSend && (
              <button type="button" disabled={busy || loading} onClick={() => void send()}>
                Send reviewed application{review.items.length === 1 ? '' : 's'}
              </button>
            )}
            {canReplay && (
              <button
                type="button"
                className="secondary"
                disabled={busy || loading}
                onClick={() => void send(true)}
              >
                Recover exact Send command for this review
              </button>
            )}
            {round && ['queued', 'running', 'awaiting_input'].includes(round.state) && (
              <button type="button" disabled={busy} onClick={() => void act('stop')}>
                Stop delivery commission
              </button>
            )}
            {review.items.some((item) => item.state === 'uncertain') && (
              <button
                type="button"
                className="secondary"
                disabled={busy}
                onClick={() => void act('reconcile')}
              >
                Check receipt capability
              </button>
            )}
            {round?.state === 'paused' &&
              !review.items.some((item) => item.state === 'sending') && (
                <button
                  type="button"
                  className="secondary"
                  disabled={busy}
                  onClick={() => void act('close')}
                >
                  Close unresolved commission
                </button>
              )}
            {canStartAnother && (
              <button
                type="button"
                className="secondary"
                disabled={busy}
                onClick={() => {
                  rememberHistory(review.id);
                  rememberReview('');
                  rememberSend('');
                  setReview(null);
                  setRound(null);
                }}
              >
                {staleBeforeSend
                  ? 'Leave stale review and prepare current material'
                  : 'Keep this delivery history and start another review'}
              </button>
            )}
            {canStartAnother && review.items.some((item) => item.state === 'prepared') && round && (
              <p className="hint">
                Prepared batch items that were not attempted remain unsent in this review. Starting
                another review does not send them.
              </p>
            )}
          </div>
          {!canSend && prepared && (
            <p className="hint">
              Send is unavailable while material is stale, the sender is unavailable, or this review
              already has a Send request. Refresh and inspect the exact server state.
            </p>
          )}
        </>
      )}
    </section>
  );
}
