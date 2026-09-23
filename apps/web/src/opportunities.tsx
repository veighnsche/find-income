import { useEffect, useState } from 'react';
import { EvidencePanel } from './evidence-panel';
import { AgencyHome } from './agency-home';
import {
  getOpportunity,
  getIngestion,
  getOpportunityOrganisation,
  getOrganisationCategories,
  getOrganisationSummaries,
  getRuntimeStatus,
  isUnauthenticated,
  listCompanies,
  listIngestions,
  listOpportunityChanges,
  listOpportunities,
  retryIngestion,
  RequestError,
  submitIngestion,
  type Company,
  type IngestionRequest,
  type OrganisationCategory,
  type OrganisationSummary,
  type OrganisationView,
  type Opportunity,
  type OpportunityView,
  type RecordChange,
  type RuntimeStatus,
  type Session,
} from './api';
import './opportunities.css';

const draftKey = 'jobseek.vacancy-intake-draft';
const recoveryKey = 'jobseek.vacancy-recovery';
const selectedKey = 'jobseek.selected-opportunity';
const reviewSelectionKey = 'jobseek.review-selection';
const correctionKey = 'jobseek.correction-draft.';
const briefContextKey = 'jobseek.brief-instruction-active';

function recoveryDraftKey(item: IngestionRequest): string {
  return `${recoveryKey}.${item.id}`;
}

function needsOwnerText(item: IngestionRequest): boolean {
  return item.origin === 'owner' && item.status === 'needs_text' && !item.originalText.trim();
}

function retryable(item: IngestionRequest): boolean {
  return (
    (item.status === 'failed' || item.status === 'needs_text') &&
    ['failed', 'succeeded', 'cancelled'].includes(item.jobState)
  );
}

function extractionStatus(item: IngestionRequest): string {
  if (item.status === 'completed') return 'Extraction completed';
  if (item.status === 'needs_text')
    return item.origin === 'owner'
      ? 'Extraction needs full vacancy text'
      : 'Source text inaccessible';
  if (item.status === 'failed' || item.jobState === 'failed') return 'Extraction failed';
  if (item.status === 'processing' || item.jobState === 'running') return 'Extraction running';
  return 'Extraction queued';
}

function errorText(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Something went wrong. Try again.';
}

function IngestionActivity({
  onSessionLost,
  onOpen,
  onRecover,
  activeRequest,
  refresh,
}: {
  onSessionLost: () => void;
  onOpen: (id: string) => void;
  onRecover: (item: IngestionRequest | null) => void;
  activeRequest: IngestionRequest | null;
  refresh: number;
}) {
  const [items, setItems] = useState<IngestionRequest[]>([]);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  async function load(next = '', append = false) {
    setLoading(true);
    setError(null);
    try {
      const page = await listIngestions(next);
      setItems((old) => (append ? [...old, ...page.items] : page.items));
      setCursor(page.nextCursor || '');
      if (activeRequest) {
        const updated = page.items.find((item) => item.id === activeRequest.id);
        if (updated) onRecover(updated);
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void load();
  }, [onSessionLost, refresh]);
  useEffect(() => {
    let stored: { id: string; sourceUrl: string } | null = null;
    try {
      stored = JSON.parse(localStorage.getItem(recoveryKey) || 'null') as {
        id: string;
        sourceUrl: string;
      } | null;
    } catch {
      return;
    }
    if (!stored?.id || !stored.sourceUrl) return;
    let cancelled = false;
    getIngestion(stored.id)
      .then((item) => {
        if (
          !cancelled &&
          JSON.parse(localStorage.getItem(recoveryKey) || 'null')?.id === stored.id &&
          item.origin === 'owner' &&
          item.sourceUrl === stored?.sourceUrl &&
          retryable(item)
        )
          onRecover(item);
        else if (!cancelled) localStorage.removeItem(recoveryKey);
      })
      .catch((cause) => {
        if (!cancelled && isUnauthenticated(cause)) onSessionLost();
      });
    return () => {
      cancelled = true;
    };
  }, []);
  return (
    <section className="op-card" aria-label="Vacancy processing status">
      <div className="op-heading-row">
        <h2>Collection and processing</h2>
        <button className="secondary" type="button" disabled={loading} onClick={() => void load()}>
          Refresh status
        </button>
      </div>
      {loading && <p role="status">Loading processing status…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!loading && !error && items.length === 0 && (
        <p>No collected or submitted vacancies are recorded yet.</p>
      )}
      <ol className="op-history">
        {items.map((item) => (
          <li key={item.id}>
            <strong>
              {item.sourceUrl ||
                `${item.originalText.slice(0, 90)}${item.originalText.length > 90 ? '…' : ''}`}
            </strong>
            <p>
              {item.origin === 'collector'
                ? 'Collected'
                : item.origin === 'owner'
                  ? 'Added by you'
                  : 'Added by agent'}{' '}
              · {new Date(item.createdAt).toLocaleString()}
            </p>
            <p>
              {extractionStatus(item)} · job {words(item.jobState)}
              {item.safeErrorCode && ` · ${words(item.safeErrorCode)}`}
            </p>
            {item.opportunityId && (
              <p className="hint">Opportunity saved. Extraction status is shown separately.</p>
            )}
            {item.opportunityId && (
              <button
                className="op-text-button"
                type="button"
                onClick={() => onOpen(item.opportunityId!)}
              >
                Open saved opportunity
              </button>
            )}
            {item.opportunityId && item.jobState !== 'succeeded' && (
              <p className="hint">
                The opportunity is saved; later processing may still be running or may need
                attention.
              </p>
            )}
            {item.status === 'needs_text' && (
              <p className="hint">
                {needsOwnerText(item)
                  ? 'Paste the full vacancy text for this URL in the input above.'
                  : 'Source text was inaccessible. This source needs a collection retry or diagnostic review.'}
              </p>
            )}
            {item.origin === 'collector' && item.status === 'failed' && (
              <p className="hint">
                Automatic collection or extraction failed. Review the source status and retry when
                available; no pasted text is required from you.
              </p>
            )}
            {item.origin === 'owner' && retryable(item) && (
              <button className="secondary" type="button" onClick={() => onRecover(item)}>
                {activeRequest?.id === item.id
                  ? 'Selected for retry'
                  : needsOwnerText(item)
                    ? 'Provide full text'
                    : 'Retry this import'}
              </button>
            )}
          </li>
        ))}
      </ol>
      {cursor && (
        <button
          className="secondary"
          type="button"
          disabled={loading}
          onClick={() => void load(cursor, true)}
        >
          Load more processing records
        </button>
      )}
    </section>
  );
}

function words(value: string): string {
  return value.replaceAll('_', ' ');
}

function SourceLink({ value }: { value: string }) {
  let href: string | undefined;
  try {
    const parsed = new URL(value);
    if (['http:', 'https:'].includes(parsed.protocol) && !parsed.username && !parsed.password)
      href = parsed.href;
  } catch {
    // Untrusted source URLs remain plain text.
  }
  return href ? (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {value}
    </a>
  ) : (
    <span>{value || 'Not recorded'}</span>
  );
}

function compensationText(value: Opportunity['compensation']): string {
  const display = (cents: number) =>
    Number.isSafeInteger(cents)
      ? (cents / 100).toLocaleString(undefined, {
          minimumFractionDigits: 2,
          maximumFractionDigits: 2,
        })
      : 'amount exceeds browser precision';
  const amount =
    value.minAmountCents === undefined
      ? 'Amount unknown'
      : `${display(value.minAmountCents)} ${value.currency || ''}`;
  const maximum =
    value.maxAmountCents === undefined
      ? ''
      : `–${display(value.maxAmountCents)} ${value.currency || ''}`;
  return `${amount}${maximum} ${value.period && value.period !== 'unknown' ? `per ${value.period}` : '(period unknown)'}${value.referenceHours ? ` at ${value.referenceHours} hours/week` : ''}`;
}

type IntakeDraft = { value: string; key: string };

function savedDraft(): IntakeDraft {
  try {
    const raw = localStorage.getItem(draftKey);
    if (!raw) return { value: '', key: '' };
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return { value: '', key: '' };
    }
    if (
      parsed &&
      typeof parsed === 'object' &&
      'value' in parsed &&
      'key' in parsed &&
      typeof parsed.value === 'string' &&
      typeof parsed.key === 'string' &&
      (!parsed.value || parsed.key.trim().length > 0)
    )
      return parsed as IntakeDraft;
    return { value: '', key: '' };
  } catch {
    return { value: '', key: '' };
  }
}

function VacancyIntake({
  session,
  onSessionLost,
  onSubmitted,
  recovery,
  onClearRecovery,
  onUpdateRecovery,
  correction,
}: {
  session: Session;
  onSessionLost: () => void;
  onSubmitted: () => void;
  recovery: IngestionRequest | null;
  onClearRecovery: () => void;
  onUpdateRecovery: (item: IngestionRequest) => void;
  correction?: string;
}) {
  const [draft, setDraft] = useState(() => {
    if (correction) {
      try {
        return { value: localStorage.getItem(correctionKey + correction) || '', key: '' };
      } catch {
        return { value: '', key: '' };
      }
    }
    if (!recovery) return savedDraft();
    try {
      const saved = JSON.parse(localStorage.getItem(recoveryDraftKey(recovery)) || 'null') as {
        sourceUrl?: string;
        value?: string;
      } | null;
      return {
        value:
          saved?.sourceUrl === recovery.sourceUrl && typeof saved.value === 'string'
            ? saved.value
            : '',
        key: '',
      };
    } catch {
      return { value: '', key: '' };
    }
  });
  const [stored, setStored] = useState(() => {
    try {
      return (
        localStorage.getItem(
          correction
            ? correctionKey + correction
            : recovery
              ? recoveryDraftKey(recovery)
              : draftKey,
        ) !== null
      );
    } catch {
      return false;
    }
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState<IngestionRequest | null>(null);
  const [uncertain, setUncertain] = useState(false);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
  useEffect(() => {
    if (correction) document.getElementById('vacancy-intake')?.focus();
  }, [correction]);
  useEffect(() => {
    const controller = new AbortController();
    getRuntimeStatus(controller.signal)
      .then(setRuntime)
      .catch((cause) => {
        if (!controller.signal.aborted && isUnauthenticated(cause)) onSessionLost();
      });
    return () => controller.abort();
  }, [onSessionLost]);

  function update(value: string) {
    const next = { value, key: recovery || correction ? '' : value ? crypto.randomUUID() : '' };
    setDraft(next);
    setError(null);
    setSubmitted(null);
    try {
      const key = correction
        ? correctionKey + correction
        : recovery
          ? recoveryDraftKey(recovery)
          : draftKey;
      if (value)
        localStorage.setItem(
          key,
          correction
            ? value
            : recovery
              ? JSON.stringify({ sourceUrl: recovery.sourceUrl, value })
              : JSON.stringify(next),
        );
      else localStorage.removeItem(key);
      setStored(Boolean(value));
    } catch {
      setStored(false);
    }
  }

  async function submit() {
    if (correction) {
      setError(
        'The scoped instruction service is not available yet. Your instruction remains saved for this opportunity in this browser.',
      );
      return;
    }
    const value = draft.value.trim();
    if (
      busy ||
      uncertain ||
      (recovery && needsOwnerText(recovery) && !value) ||
      (!recovery && !value)
    )
      return;
    setError(null);
    if (recovery) {
      if (!retryable(recovery)) {
        setError('Refresh processing status before retrying this import.');
        return;
      }
      if (
        needsOwnerText(recovery) &&
        (value.length > 200000 || (/^https?:\/\//i.test(value) && !/\s/.test(value)))
      ) {
        setError('Paste the full vacancy text, not a URL (up to 200,000 characters).');
        return;
      }
      setBusy(true);
      try {
        const result = await retryIngestion(
          recovery.id,
          needsOwnerText(recovery) ? { originalText: draft.value } : {},
          session.csrfToken,
        );
        setSubmitted(result);
        setUncertain(false);
        setDraft({ value: '', key: '' });
        try {
          localStorage.removeItem(recoveryDraftKey(recovery));
        } catch {
          /* Server accepted the text. */
        }
        onSubmitted();
      } catch (cause) {
        if (isUnauthenticated(cause)) onSessionLost();
        else if (cause instanceof RequestError && cause.status >= 400 && cause.status < 500) {
          if (cause.status === 409) setUncertain(true);
          setError(`${errorText(cause)} Refresh status before retrying.`);
        } else {
          setUncertain(true);
          setError(
            'The retry response was interrupted. Its outcome is unknown. Check this request’s status before trying again; your text is saved in this browser.',
          );
        }
      } finally {
        setBusy(false);
      }
      return;
    }
    let sourceUrl: string | undefined;
    let originalText: string | undefined;
    if (/^https?:\/\//i.test(value) && !/\s/.test(value)) {
      try {
        const url = new URL(value);
        if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password)
          throw new Error();
        sourceUrl = value;
      } catch {
        setError('Enter a valid job URL or paste the full vacancy text.');
        return;
      }
    } else {
      originalText = draft.value;
    }
    const snapshot = draft;
    try {
      localStorage.setItem(draftKey, JSON.stringify(snapshot));
      setStored(true);
    } catch {
      setStored(false);
    }
    setBusy(true);
    try {
      const result = await submitIngestion(
        { idempotencyKey: snapshot.key, sourceUrl, originalText },
        session.csrfToken,
      );
      setSubmitted(result);
      setDraft({ value: '', key: '' });
      try {
        localStorage.removeItem(draftKey);
      } catch {
        /* The response confirms durable server storage. */
      }
      setStored(false);
      onSubmitted();
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else
        setError(
          `${errorText(cause)} Your draft is still here. Retry with the same content to check the same submission.`,
        );
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="op-card" aria-label="Vacancy intake">
      <h2>
        {correction
          ? correction === 'brief'
            ? 'Instruction for your campaign brief'
            : 'Instruction for this opportunity'
          : recovery
            ? needsOwnerText(recovery)
              ? 'Provide full text for this URL'
              : 'Retry this import'
            : 'Add a link or vacancy'}
      </h2>
      {recovery && !correction && (
        <p>
          <strong>Original URL:</strong> <SourceLink value={recovery.sourceUrl} />
        </p>
      )}
      <label htmlFor="vacancy-intake">
        {correction
          ? correction === 'brief'
            ? 'Correction to your stated direction'
            : 'Correction or question about the selected opportunity'
          : recovery
            ? needsOwnerText(recovery)
              ? 'Full vacancy text for this request'
              : 'This request is ready to retry'
            : 'Job URL or full vacancy text'}
      </label>
      <textarea
        id="vacancy-intake"
        rows={8}
        value={draft.value}
        disabled={busy || Boolean(recovery && !needsOwnerText(recovery) && !correction)}
        onChange={(event) => update(event.target.value)}
        placeholder={
          correction
            ? correction === 'brief'
              ? 'Describe the change to your campaign brief'
              : 'Describe the correction or missing fact for this opportunity'
            : recovery
              ? needsOwnerText(recovery)
                ? 'Paste the complete vacancy text for the URL above'
                : 'No additional text is needed for this retry'
              : 'Paste a job URL or the complete vacancy here'
        }
      />
      <p role="status" className="hint">
        {correction
          ? draft.value
            ? stored
              ? correction === 'brief'
                ? 'Instruction saved in this browser for your campaign brief.'
                : 'Instruction saved in this browser for this opportunity.'
              : 'Instruction is held in this page only.'
            : 'Only you can supply a correction or owner-held fact; the agency handles record edits when the instruction service is available.'
          : recovery && !needsOwnerText(recovery)
            ? 'This retries the original import and keeps any saved opportunity linked.'
            : draft.value
              ? stored
                ? 'Draft saved in this browser until submission is confirmed.'
                : 'Draft is held in this open page only. Browser storage is unavailable.'
              : recovery
                ? 'Paste the complete text for the original URL.'
                : 'Paste a URL or full vacancy to prepare a draft.'}
      </p>
      {!recovery && !correction && (
        <p className="muted">
          Saving records the source for a future commissioned round; it does not start recruitment
          or create an opportunity.{' '}
          {runtime === null
            ? 'Execution readiness has not been confirmed.'
            : runtime.ingestionAvailable
              ? 'An intake worker is connected, but round execution readiness is separate.'
              : 'The intake worker is unavailable; saved requests remain pending.'}
        </p>
      )}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {submitted && (
        <p role="status" className="success">
          {submitted.status === 'completed'
            ? 'Extraction completed.'
            : 'Source saved for a commissioned round.'}{' '}
          {submitted.opportunityId
            ? 'The saved opportunity remains available in processing status.'
            : 'Processing has not been confirmed; check status for a later result.'}
        </p>
      )}
      <div className="button-row">
        <button
          type="button"
          disabled={
            busy ||
            uncertain ||
            Boolean(submitted && recovery) ||
            Boolean(correction) ||
            (recovery
              ? !retryable(recovery) || (needsOwnerText(recovery) && !draft.value.trim())
              : !draft.value.trim())
          }
          onClick={() => void submit()}
        >
          {correction
            ? 'Instruction service unavailable'
            : busy
              ? 'Saving…'
              : recovery
                ? needsOwnerText(recovery)
                  ? 'Submit text and retry'
                  : 'Retry original import'
                : 'Save source for a round'}
        </button>
        {recovery && !correction && (
          <button className="secondary" type="button" disabled={busy} onClick={onClearRecovery}>
            Return to new intake
          </button>
        )}
        {correction === 'brief' && (
          <button className="secondary" type="button" onClick={onClearRecovery}>
            Return to source intake
          </button>
        )}
        {uncertain && recovery && (
          <button
            className="secondary"
            type="button"
            disabled={busy}
            onClick={() => {
              void getIngestion(recovery.id)
                .then((current) => {
                  if (
                    current.attemptsStarted > recovery.attemptsStarted ||
                    current.jobId !== recovery.jobId
                  ) {
                    setSubmitted(current);
                    setUncertain(false);
                    onUpdateRecovery(current);
                    onSubmitted();
                  } else {
                    setError(
                      'No accepted retry is visible yet. Check status again before submitting another retry.',
                    );
                  }
                })
                .catch((cause) => setError(errorText(cause)));
            }}
          >
            Check retry status
          </button>
        )}
        {!recovery && draft.value && (
          <button className="secondary" type="button" disabled={busy} onClick={() => update('')}>
            Clear draft
          </button>
        )}
      </div>
    </section>
  );
}

function OrganisationPanel({ id, onSessionLost }: { id: string; onSessionLost: () => void }) {
  const [view, setView] = useState<OrganisationView | null>(null);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getOpportunityOrganisation(id, controller.signal)
      .then(setView)
      .catch((cause) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(errorText(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    getRuntimeStatus(controller.signal)
      .then(setRuntime)
      .catch(() => {
        /* Availability can remain unknown. */
      });
    return () => controller.abort();
  }, [id, onSessionLost, refresh]);
  const assessment = view?.current;
  return (
    <section className="op-card" aria-label="Opportunity organisation">
      <div className="op-heading-row">
        <h2>Organisation</h2>
        <button
          className="secondary"
          type="button"
          onClick={() => setRefresh((value) => value + 1)}
        >
          Refresh
        </button>
      </div>
      {loading && <p role="status">Loading organisation…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {view && (
        <>
          <p>
            Status: <strong>{words(view.status)}</strong>. Organisation worker:{' '}
            {runtime === null
              ? 'availability unknown'
              : runtime.organisationAvailable
                ? 'started'
                : 'not started'}
            .
          </p>
          {view.status === 'unconfigured' && (
            <p>Organisation is not configured. No category result has been produced.</p>
          )}
          {view.status === 'pending' && !view.jobId && (
            <p>No organisation job is queued for this opportunity.</p>
          )}
          {assessment ? (
            <>
              <h3>Current category</h3>
              <p>
                {assessment.disposition === 'uncertain' ? (
                  'Uncertain'
                ) : (
                  <strong>{assessment.categoryDescription || 'Selected category'}</strong>
                )}
              </p>
              <details>
                <summary>Input and source provenance</summary>
                <h4>Input excerpts</h4>
                <ul>
                  {assessment.sourceFacts.map((fact) => (
                    <li key={fact.id}>
                      <blockquote>{fact.excerpt}</blockquote>
                      <p className="hint">
                        Source {fact.sourceId} · {words(fact.sourceKind)} · revision{' '}
                        {fact.sourceRevision}
                      </p>
                    </li>
                  ))}
                </ul>
                <h4>Recorded source references</h4>
                <ul>
                  {assessment.sourceRefs.map((ref) => (
                    <li key={ref.factId}>
                      {ref.factId} → source {ref.sourceId} · {words(ref.sourceKind)} · revision{' '}
                      {ref.sourceRevision}
                    </li>
                  ))}
                </ul>
                <p className="hint">No model explanation was saved.</p>
              </details>
            </>
          ) : (
            <p>No current category result has been saved.</p>
          )}
          {view.latestHistorical && view.latestHistorical.id !== assessment?.id && (
            <details>
              <summary>Latest historical result</summary>
              <p>
                {view.latestHistorical.categoryDescription ||
                  words(view.latestHistorical.disposition)}
              </p>
            </details>
          )}
        </>
      )}
    </section>
  );
}

function OpportunityHistory({ id, onSessionLost }: { id: string; onSessionLost: () => void }) {
  const [changes, setChanges] = useState<RecordChange[]>([]);
  const [cursor, setCursor] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  async function load(next = '', append = false) {
    setLoading(true);
    setError(null);
    try {
      const page = await listOpportunityChanges(next);
      const matching = page.items.filter((change) => change.entityId === id);
      setChanges((old) => (append ? [...old, ...matching] : matching));
      setCursor(page.nextCursor || '');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void load();
  }, [id]);
  return (
    <section className="op-card">
      <h2>Source and change history</h2>
      <p className="hint">
        Changes are scanned across all opportunities. Continue to scan for later events.
      </p>
      {loading && <p role="status">Loading history…</p>}
      {error && (
        <p role="alert" className="error">
          {error}{' '}
          <button
            className="secondary"
            type="button"
            onClick={() => void load(cursor, changes.length > 0)}
          >
            Try again
          </button>
        </p>
      )}
      {!loading && !error && changes.length === 0 && <p>No changes found in the scanned pages.</p>}
      <ol className="op-history">
        {changes.map((change) => (
          <li key={change.changeId}>
            <strong>{change.operation.replaceAll('.', ' ')}</strong> · revision{' '}
            {change.revisionAfter ?? 'unknown'} · {new Date(change.occurredAt).toLocaleString()}
            <details>
              <summary>Source and notes at this revision</summary>
              {change.snapshotState === 'unavailable_historical' ? (
                <p>Historical snapshot unavailable.</p>
              ) : (
                <>
                  <p>
                    <strong>Source URL:</strong>{' '}
                    <SourceLink
                      value={
                        typeof change.snapshot?.sourceUrl === 'string'
                          ? change.snapshot.sourceUrl
                          : ''
                      }
                    />
                  </p>
                  <pre className="op-source">
                    {typeof change.snapshot?.originalText === 'string'
                      ? change.snapshot.originalText
                      : 'Not recorded'}
                  </pre>
                  <pre className="op-source">
                    {typeof change.snapshot?.notes === 'string'
                      ? change.snapshot.notes
                      : 'No notes recorded'}
                  </pre>
                </>
              )}
            </details>
          </li>
        ))}
      </ol>
      {cursor && (
        <button
          className="secondary"
          type="button"
          disabled={loading}
          onClick={() => void load(cursor, true)}
        >
          Load more history
        </button>
      )}
    </section>
  );
}

function OpportunityDetail({
  id,
  companies,
  onSessionLost,
  onOpen,
  onBack,
}: {
  id: string;
  companies: Company[];
  onSessionLost: () => void;
  onOpen: (id: string) => void;
  onBack: () => void;
}) {
  const [view, setView] = useState<OpportunityView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getOpportunity(id, controller.signal)
      .then(setView)
      .catch((cause) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(errorText(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [id, refresh, onSessionLost]);
  const opportunity = view?.opportunity;
  return (
    <main className="op-main">
      <button className="secondary" type="button" onClick={onBack}>
        ← Back to opportunities
      </button>
      {loading && <p role="status">Loading opportunity…</p>}
      {error && (
        <p role="alert" className="error">
          {error}{' '}
          <button
            className="secondary"
            type="button"
            onClick={() => setRefresh((value) => value + 1)}
          >
            Try again
          </button>
        </p>
      )}
      {opportunity && (
        <>
          <div className="op-heading-row">
            <div>
              <p className="eyebrow">
                {opportunity.kind} · revision {opportunity.revision}
                {opportunity.archivedAt ? ' · archived' : ''}
              </p>
              <h1>{opportunity.title}</h1>
              <p>
                {companies.find((item) => item.id === opportunity.companyId)?.name ||
                  `Company ${opportunity.companyId}`}{' '}
                · {opportunity.stage.replaceAll('_', ' ')} · {opportunity.workPattern} ·{' '}
                {opportunity.locationText || 'Location unknown'}
              </p>
            </div>
          </div>
          <section className="op-card">
            <h2>Original vacancy source</h2>
            <p>
              <strong>URL:</strong> <SourceLink value={opportunity.sourceUrl} />
            </p>
            <pre className="op-source">
              {opportunity.originalText || 'No original text recorded.'}
            </pre>
            <p className="muted">
              Posted: {opportunity.postedOn || 'unknown'} · Deadline:{' '}
              {opportunity.deadlineOn || 'unknown'}
            </p>
          </section>
          <section className="op-card">
            <h2>Notes</h2>
            <pre className="op-source">{opportunity.notes || 'No notes recorded.'}</pre>
          </section>
          <section className="op-card">
            <h2>Advertised compensation</h2>
            <p>{compensationText(opportunity.compensation)}</p>
            {opportunity.compensation.benefitsText && (
              <p>Benefits/context: {opportunity.compensation.benefitsText}</p>
            )}
          </section>
          <OrganisationPanel id={id} onSessionLost={onSessionLost} />
          <EvidencePanel opportunity={opportunity} onSessionLost={onSessionLost} />
          <section className="op-card">
            <h2>Possible duplicates</h2>
            {view!.likelyDuplicates.length === 0 ? (
              <p>No likely duplicates found.</p>
            ) : (
              <ul>
                {view!.likelyDuplicates.map((duplicate) => (
                  <li key={duplicate.opportunity.id}>
                    <button
                      className="op-text-button"
                      type="button"
                      onClick={() => onOpen(duplicate.opportunity.id)}
                    >
                      {duplicate.opportunity.title}
                    </button>{' '}
                    — {duplicate.reason.replaceAll('_', ' ')}
                  </li>
                ))}
              </ul>
            )}
          </section>
          {!opportunity.archivedAt && (
            <p className="hint">
              Dismissing this role awaits a scoped owner action. A dismissal will not change your
              campaign preferences.
            </p>
          )}
          <OpportunityHistory id={id} onSessionLost={onSessionLost} />
        </>
      )}
    </main>
  );
}

export function Opportunities({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [selected, setSelected] = useState<string | null>(() => {
    try {
      return localStorage.getItem(selectedKey);
    } catch {
      return null;
    }
  });
  const [reviewSelection, setReviewSelection] = useState<string[]>(() => {
    try {
      const value: unknown = JSON.parse(localStorage.getItem(reviewSelectionKey) || '[]');
      return Array.isArray(value) ? value.filter((id): id is string => typeof id === 'string') : [];
    } catch {
      return [];
    }
  });
  const [recovery, setRecovery] = useState<IngestionRequest | null>(null);
  const [briefInstruction, setBriefInstruction] = useState(() => {
    try {
      return localStorage.getItem(briefContextKey) === 'true';
    } catch {
      return false;
    }
  });
  const [ingestionRefresh, setIngestionRefresh] = useState(0);
  const [items, setItems] = useState<OpportunityView[]>([]);
  const [organisationSummaries, setOrganisationSummaries] = useState<
    Record<string, OrganisationSummary>
  >({});
  const [organisationCategories, setOrganisationCategories] = useState<OrganisationCategory[]>([]);
  const [organisationLoading, setOrganisationLoading] = useState(false);
  const [organisationError, setOrganisationError] = useState<string | null>(null);
  const [organisationFilter, setOrganisationFilter] = useState('');
  const [companies, setCompanies] = useState<Company[]>([]);
  const [companyCursor, setCompanyCursor] = useState('');
  const [companyError, setCompanyError] = useState<string | null>(null);
  const [companyLoading, setCompanyLoading] = useState(false);
  const [cursor, setCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [stageFilter, setStageFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [archiveFilter, setArchiveFilter] = useState<'active' | 'archived' | 'all'>('active');
  function openOpportunity(id: string | null) {
    setSelected(id);
    try {
      if (id) localStorage.setItem(selectedKey, id);
      else localStorage.removeItem(selectedKey);
    } catch {
      /* Selection remains available in this page. */
    }
  }
  function toggleReviewSelection(id: string) {
    setReviewSelection((old) => {
      const next = old.includes(id) ? old.filter((value) => value !== id) : [...old, id];
      try {
        localStorage.setItem(reviewSelectionKey, JSON.stringify(next));
      } catch {
        /* Selection remains in this page. */
      }
      return next;
    });
  }
  function selectRecovery(item: IngestionRequest | null) {
    if (item) {
      setBriefInstruction(false);
      try {
        localStorage.removeItem(briefContextKey);
      } catch {
        /* Context remains in this page. */
      }
    }
    setRecovery(item);
    try {
      if (item)
        localStorage.setItem(
          recoveryKey,
          JSON.stringify({ id: item.id, sourceUrl: item.sourceUrl }),
        );
      else localStorage.removeItem(recoveryKey);
    } catch {
      /* Context remains bound in this page. */
    }
  }
  async function loadSummaries(views: OpportunityView[], append: boolean) {
    setOrganisationLoading(true);
    setOrganisationError(null);
    try {
      const ids = views.map((item) => item.opportunity.id);
      const all: OrganisationSummary[] = [];
      for (let start = 0; start < ids.length; start += 100) {
        all.push(...(await getOrganisationSummaries(ids.slice(start, start + 100))));
      }
      setOrganisationSummaries((old) => {
        const next = append ? { ...old } : ({} as Record<string, OrganisationSummary>);
        for (const item of all) next[item.opportunityId] = item;
        return next;
      });
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setOrganisationError(errorText(cause));
    } finally {
      setOrganisationLoading(false);
    }
  }
  async function load(next = '', append = false) {
    setLoading(true);
    setError(null);
    try {
      const page = await listOpportunities(next);
      setItems((old) => (append ? [...old, ...page.items] : page.items));
      setCursor(page.nextCursor || '');
      void loadSummaries(page.items, append);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      setLoading(false);
    }
  }
  async function loadCompanies(next = '') {
    setCompanyLoading(true);
    setCompanyError(null);
    try {
      const page = await listCompanies(next);
      setCompanies((old) => {
        const merged = new Map(old.map((item) => [item.id, item]));
        for (const item of page.items) merged.set(item.company.id, item.company);
        return [...merged.values()];
      });
      setCompanyCursor(page.nextCursor || '');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setCompanyError(errorText(cause));
    } finally {
      setCompanyLoading(false);
    }
  }
  useEffect(() => {
    void loadCompanies();
    void load();
    getOrganisationCategories()
      .then((set) => setOrganisationCategories(set.categories))
      .catch((cause) => {
        if (isUnauthenticated(cause)) onSessionLost();
        else setOrganisationError(errorText(cause));
      });
  }, [onSessionLost]);
  if (selected)
    return (
      <>
        <VacancyIntake
          key={`correction-${selected}`}
          session={session}
          onSessionLost={onSessionLost}
          onSubmitted={() => setIngestionRefresh((value) => value + 1)}
          recovery={null}
          correction={selected}
          onClearRecovery={() => selectRecovery(null)}
          onUpdateRecovery={selectRecovery}
        />
        <OpportunityDetail
          id={selected}
          companies={companies}
          onSessionLost={onSessionLost}
          onOpen={(id) => openOpportunity(id)}
          onBack={() => {
            openOpportunity(null);
            void load();
          }}
        />
      </>
    );
  const names = new Map(companies.map((company) => [company.id, company.name]));
  const query = search.trim().toLowerCase();
  const filtered = items.filter(
    ({ opportunity }) =>
      (archiveFilter === 'all' ||
        Boolean(opportunity.archivedAt) === (archiveFilter === 'archived')) &&
      (!stageFilter || opportunity.stage === stageFilter) &&
      (!kindFilter || opportunity.kind === kindFilter) &&
      (!organisationFilter ||
        (organisationFilter.startsWith('category:')
          ? organisationSummaries[opportunity.id]?.categoryId === organisationFilter.slice(9)
          : organisationSummaries[opportunity.id]?.status === organisationFilter.slice(7))) &&
      (!query ||
        [opportunity.title, opportunity.locationText, opportunity.sourceUrl].some((value) =>
          value.toLowerCase().includes(query),
        )),
  );
  const stages = [...new Set(items.map((item) => item.opportunity.stage))];
  return (
    <main className="op-main">
      <p className="eyebrow">Private workspace</p>
      <h1>Your campaign</h1>
      <AgencyHome
        session={session}
        onSessionLost={onSessionLost}
        onOpenOpportunity={(id) => openOpportunity(id)}
        onEditBrief={() => {
          selectRecovery(null);
          setBriefInstruction(true);
          try {
            localStorage.setItem(briefContextKey, 'true');
          } catch {
            /* Context remains in this page. */
          }
        }}
      />
      <VacancyIntake
        key={briefInstruction ? 'brief' : recovery?.id || 'new'}
        session={session}
        onSessionLost={onSessionLost}
        onSubmitted={() => setIngestionRefresh((value) => value + 1)}
        recovery={briefInstruction ? null : recovery}
        correction={briefInstruction ? 'brief' : undefined}
        onClearRecovery={() => {
          selectRecovery(null);
          setBriefInstruction(false);
          try {
            localStorage.removeItem(briefContextKey);
          } catch {
            /* Context remains in this page. */
          }
        }}
        onUpdateRecovery={selectRecovery}
      />
      <IngestionActivity
        onSessionLost={onSessionLost}
        onOpen={(id) => openOpportunity(id)}
        onRecover={selectRecovery}
        activeRequest={recovery}
        refresh={ingestionRefresh}
      />
      <section className="op-card" aria-label="Opportunity filters">
        <h2>Saved opportunities</h2>
        {reviewSelection.length > 0 && (
          <p className="hint">
            {reviewSelection.length} selected for your review in this browser. Preparing
            applications from this selection awaits a server-supported round.
          </p>
        )}
        <div className="op-grid op-filters">
          <label>
            Search title, location or source URL
            <input
              type="search"
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Search loaded opportunities"
            />
          </label>
          <label>
            Stage
            <select value={stageFilter} onChange={(event) => setStageFilter(event.target.value)}>
              <option value="">All stages</option>
              {stages.map((stage) => (
                <option key={stage} value={stage}>
                  {words(stage)}
                </option>
              ))}
            </select>
          </label>
          <label>
            Type
            <select value={kindFilter} onChange={(event) => setKindFilter(event.target.value)}>
              <option value="">All types</option>
              <option value="employment">Employment</option>
              <option value="project">Project</option>
            </select>
          </label>
          <label>
            Archive
            <select
              value={archiveFilter}
              onChange={(event) => setArchiveFilter(event.target.value as typeof archiveFilter)}
            >
              <option value="active">Active</option>
              <option value="archived">Archived</option>
              <option value="all">All</option>
            </select>
          </label>
          <label>
            Organisation
            <select
              value={organisationFilter}
              onChange={(event) => setOrganisationFilter(event.target.value)}
            >
              <option value="">All categories and statuses</option>
              {organisationCategories.map((category) => (
                <option key={category.id} value={`category:${category.id}`}>
                  {category.description}
                </option>
              ))}
              {[
                'pending',
                'processing',
                'selected',
                'uncertain',
                'failed',
                'outdated',
                'unconfigured',
              ].map((status) => (
                <option key={status} value={`status:${status}`}>
                  Status: {words(status)}
                </option>
              ))}
            </select>
          </label>
        </div>
        {organisationLoading && (
          <p role="status">Loading organisation status for saved opportunities…</p>
        )}
        {organisationError && (
          <p role="alert" className="error">
            Could not load organisation status: {organisationError}{' '}
            <button
              className="secondary"
              type="button"
              onClick={() => void loadSummaries(items, false)}
            >
              Try again
            </button>
          </p>
        )}
        <p className="hint">
          {items.length} records loaded{cursor ? '; search covers loaded records only.' : '.'}
        </p>
        {cursor && (
          <button
            className="secondary"
            type="button"
            disabled={loading}
            onClick={() => void load(cursor, true)}
          >
            Load more
          </button>
        )}
        {error && (
          <p role="alert" className="error">
            {error}{' '}
            <button
              className="secondary"
              type="button"
              onClick={() => void load(cursor, items.length > 0)}
            >
              Try again
            </button>
          </p>
        )}
        {companyLoading && <p role="status">Loading company names…</p>}
        {companyError && (
          <p role="alert" className="error">
            Could not load company names: {companyError}{' '}
            <button
              className="secondary"
              type="button"
              onClick={() => void loadCompanies(companyCursor)}
            >
              Try again
            </button>
          </p>
        )}
        {companyCursor && (
          <button
            className="secondary"
            type="button"
            disabled={companyLoading}
            onClick={() => void loadCompanies(companyCursor)}
          >
            Load more company names
          </button>
        )}
      </section>
      {loading && <p role="status">Loading opportunities…</p>}
      {!loading && filtered.length === 0 && (
        <section className="op-card">
          <h2>No matching opportunities</h2>
          <p>
            {items.length
              ? 'Try a different search or filter.'
              : 'No opportunities have been saved yet.'}
          </p>
        </section>
      )}
      <ul className="op-list">
        {filtered.map(({ opportunity, likelyDuplicates }) => (
          <li key={opportunity.id} className="op-card">
            <div className="op-heading-row">
              <div>
                <button
                  className="op-text-button"
                  type="button"
                  onClick={() => openOpportunity(opportunity.id)}
                >
                  {opportunity.title}
                </button>
                <p>
                  {names.get(opportunity.companyId) || `Company ${opportunity.companyId}`} ·{' '}
                  {opportunity.kind} · {opportunity.stage.replaceAll('_', ' ')}
                  {opportunity.archivedAt ? ' · archived' : ''}
                </p>
              </div>
              <span className="op-revision">Revision {opportunity.revision}</span>
            </div>
            <button
              className="secondary"
              type="button"
              aria-pressed={reviewSelection.includes(opportunity.id)}
              onClick={() => toggleReviewSelection(opportunity.id)}
            >
              {reviewSelection.includes(opportunity.id)
                ? 'Deselect for review'
                : 'Select for review'}
            </button>
            <p>
              {opportunity.locationText || 'Location unknown'} · {opportunity.workPattern} ·{' '}
              {compensationText(opportunity.compensation)}
            </p>
            <p className="hint">
              Organisation:{' '}
              {organisationSummaries[opportunity.id]?.categoryId
                ? organisationCategories.find(
                    (category) => category.id === organisationSummaries[opportunity.id]?.categoryId,
                  )?.description || 'Selected category'
                : words(organisationSummaries[opportunity.id]?.status || 'loading')}
            </p>
            {likelyDuplicates.length > 0 && (
              <p className="hint">
                {likelyDuplicates.length} possible duplicate
                {likelyDuplicates.length === 1 ? '' : 's'} to review
              </p>
            )}
          </li>
        ))}
      </ul>
    </main>
  );
}
