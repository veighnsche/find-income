import { useEffect, useState } from 'react';
import { EvidencePanel } from './evidence-panel';
import {
  archiveOpportunity,
  getOpportunity,
  getOpportunityOrganisation,
  getOrganisationCategories,
  getOrganisationSummaries,
  getRuntimeStatus,
  isUnauthenticated,
  listCompanies,
  listIngestions,
  listOpportunityChanges,
  listOpportunities,
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

function errorText(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Something went wrong. Try again.';
}

function IngestionActivity({
  onSessionLost,
  onOpen,
  refresh,
}: {
  onSessionLost: () => void;
  onOpen: (id: string) => void;
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
              Status: {item.opportunityId ? 'Opportunity saved' : words(item.status)} · job{' '}
              {words(item.jobState)}
              {item.safeErrorCode && ` · ${words(item.safeErrorCode)}`}
            </p>
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
              <p className="hint">The URL needs full vacancy text before it can be processed.</p>
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
}: {
  session: Session;
  onSessionLost: () => void;
  onSubmitted: () => void;
}) {
  const [draft, setDraft] = useState(() => {
    return savedDraft();
  });
  const [stored, setStored] = useState(() => {
    try {
      return localStorage.getItem(draftKey) !== null;
    } catch {
      return false;
    }
  });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [submitted, setSubmitted] = useState<IngestionRequest | null>(null);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
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
    const next = { value, key: value ? crypto.randomUUID() : '' };
    setDraft(next);
    setError(null);
    setSubmitted(null);
    try {
      if (value) localStorage.setItem(draftKey, JSON.stringify(next));
      else localStorage.removeItem(draftKey);
      setStored(Boolean(value));
    } catch {
      setStored(false);
    }
  }

  async function submit() {
    const value = draft.value.trim();
    if (!value || busy) return;
    setError(null);
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
      <h2>Add a link or vacancy</h2>
      <label htmlFor="vacancy-intake">Job URL or full vacancy text</label>
      <textarea
        id="vacancy-intake"
        rows={8}
        value={draft.value}
        disabled={busy}
        onChange={(event) => update(event.target.value)}
        placeholder="Paste a job URL or the complete vacancy here"
      />
      <p role="status" className="hint">
        {draft.value
          ? stored
            ? 'Draft saved in this browser until submission is confirmed.'
            : 'Draft is held in this open page only. Browser storage is unavailable.'
          : 'Paste a URL or full vacancy to prepare a draft.'}
      </p>
      <p className="muted">
        Saving adds the source to a durable queue; it does not create an opportunity.{' '}
        {runtime === null
          ? 'Worker availability has not been confirmed.'
          : runtime.ingestionAvailable
            ? 'Processing worker has started.'
            : 'Processing worker has not started; saved requests will stay queued.'}
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {submitted && (
        <p role="status" className="success">
          Source saved as request {submitted.id}. Status: {words(submitted.status)}; job{' '}
          {words(submitted.jobState)}. Processing has not been confirmed.
        </p>
      )}
      <div className="button-row">
        <button type="button" disabled={!draft.value.trim() || busy} onClick={() => void submit()}>
          {busy ? 'Saving…' : 'Save source for processing'}
        </button>
        {draft.value && (
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
            <p>Define categories in Settings to organise saved opportunities.</p>
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
  session,
  onSessionLost,
  onOpen,
  onBack,
}: {
  id: string;
  companies: Company[];
  session: Session;
  onSessionLost: () => void;
  onOpen: (id: string) => void;
  onBack: () => void;
}) {
  const [view, setView] = useState<OpportunityView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [confirmArchive, setConfirmArchive] = useState(false);
  const [busy, setBusy] = useState(false);
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
  async function archive() {
    if (!view) return;
    setBusy(true);
    setError(null);
    try {
      await archiveOpportunity(id, view.opportunity.revision, session.csrfToken);
      setConfirmArchive(false);
      setRefresh((value) => value + 1);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      setBusy(false);
    }
  }
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
            <section className="op-card">
              <h2>Archive</h2>
              <p>Keep the record and its history, but hide it from the active list.</p>
              {confirmArchive ? (
                <div className="button-row">
                  <button type="button" disabled={busy} onClick={() => void archive()}>
                    Archive now
                  </button>
                  <button
                    className="secondary"
                    type="button"
                    onClick={() => setConfirmArchive(false)}
                  >
                    Cancel
                  </button>
                </div>
              ) : (
                <button className="secondary" type="button" onClick={() => setConfirmArchive(true)}>
                  Archive opportunity…
                </button>
              )}
            </section>
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
  const [selected, setSelected] = useState<string | null>(null);
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
      <OpportunityDetail
        id={selected}
        companies={companies}
        session={session}
        onSessionLost={onSessionLost}
        onOpen={setSelected}
        onBack={() => {
          setSelected(null);
          void load();
        }}
      />
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
      <h1>Opportunities</h1>
      <VacancyIntake
        session={session}
        onSessionLost={onSessionLost}
        onSubmitted={() => setIngestionRefresh((value) => value + 1)}
      />
      <IngestionActivity
        onSessionLost={onSessionLost}
        onOpen={setSelected}
        refresh={ingestionRefresh}
      />
      <section className="op-card" aria-label="Opportunity filters">
        <h2>Saved opportunities</h2>
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
                  onClick={() => setSelected(opportunity.id)}
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
