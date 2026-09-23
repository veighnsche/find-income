import React, { useEffect, useRef, useState } from 'react';
import {
  archiveOpportunity,
  createCompany,
  createOpportunity,
  getOpportunity,
  isUnauthenticated,
  listCompanies,
  listOpportunityChanges,
  listOpportunities,
  patchOpportunity,
  RequestError,
  type Company,
  type CreateOpportunityRequest,
  type Opportunity,
  type OpportunityView,
  type PatchOpportunityRequest,
  type RecordChange,
  type Session,
} from './api';
import './opportunities.css';

const stages = ['saved', 'researching', 'contacted', 'interviewing', 'offer', 'rejected', 'closed'];
const opportunityBatchPages = 5; // 500 records per deliberate load at the API's 100-record page size.

function errorText(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Something went wrong. Try again.';
}

function safeHTTPURL(value: string): string | null {
  try {
    const parsed = new URL(value);
    if (
      (parsed.protocol === 'http:' || parsed.protocol === 'https:') &&
      !parsed.username &&
      !parsed.password
    )
      return parsed.href;
  } catch {
    // Source text is untrusted; invalid URLs remain plain text.
  }
  return null;
}

function SourceLink({ value }: { value: string }) {
  const href = safeHTTPURL(value);
  return href ? (
    <a href={href} target="_blank" rel="noopener noreferrer">
      {value}
    </a>
  ) : (
    <span>{value || 'No source URL recorded'}</span>
  );
}

function centsInput(value: number | undefined): string {
  if (value === undefined || !Number.isSafeInteger(value) || value < 0) return '';
  const whole = Math.floor(value / 100);
  const cents = value % 100;
  return `${whole}.${String(cents).padStart(2, '0')}`;
}

function parseCents(value: string, label: string): number | undefined {
  const clean = value.trim();
  if (!clean) return undefined;
  if (clean.length > 20)
    throw new Error(`${label} is too large to save precisely in this browser.`);
  if (!/^\d+(?:[.,]\d{1,2})?$/.test(clean))
    throw new Error(`${label} must be a non-negative amount with at most two decimal places.`);
  const [whole, fraction = ''] = clean.replace(',', '.').split('.');
  const cents = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, '0'));
  if (cents > BigInt(Number.MAX_SAFE_INTEGER))
    throw new Error(`${label} is too large to save precisely in this browser.`);
  return Number(cents);
}

function moneyText(value: number | undefined, currency: string): string {
  if (value === undefined) return 'amount unknown';
  if (!Number.isSafeInteger(value))
    return 'amount exceeds browser precision; review the original source';
  return `${(value / 100).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })} ${currency}`;
}

function compensationText(value: Opportunity['compensation']): string {
  const currency = value.currency || 'unknown currency';
  const minimum = moneyText(value.minAmountCents, currency);
  const maximum =
    value.maxAmountCents === undefined ? '' : `–${moneyText(value.maxAmountCents, currency)}`;
  const period =
    value.period && value.period !== 'unknown' ? ` per ${value.period}` : ' (period unknown)';
  const hours = value.referenceHours ? ` at ${value.referenceHours} reference hours/week` : '';
  const basis =
    value.basis && value.basis !== 'unknown' ? `, ${value.basis} pay` : ', pay basis unknown';
  return `${minimum}${maximum}${period}${hours}${basis}`;
}

function visibleDate(value: string | undefined): string {
  if (!value) return 'Unknown';
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

type Draft = {
  companyId: string;
  title: string;
  kind: 'employment' | 'project';
  sourceUrl: string;
  originalText: string;
  notes: string;
  stage: string;
  workPattern: 'unknown' | 'onsite' | 'hybrid' | 'remote';
  locationText: string;
  postedOn: string;
  deadlineOn: string;
  currency: string;
  minAmount: string;
  maxAmount: string;
  period: 'month' | 'year' | 'hour' | 'project' | 'unknown';
  referenceHours: string;
  basis: 'base' | 'inclusive' | 'unknown';
  benefitsText: string;
};

const blankDraft: Draft = {
  companyId: '',
  title: '',
  kind: 'employment',
  sourceUrl: '',
  originalText: '',
  notes: '',
  stage: 'saved',
  workPattern: 'unknown',
  locationText: '',
  postedOn: '',
  deadlineOn: '',
  currency: 'unknown',
  minAmount: '',
  maxAmount: '',
  period: 'unknown',
  referenceHours: '',
  basis: 'unknown',
  benefitsText: '',
};

function fromOpportunity(value: Opportunity): Draft {
  return {
    companyId: value.companyId,
    title: value.title,
    kind: value.kind,
    sourceUrl: value.sourceUrl,
    originalText: value.originalText,
    notes: value.notes,
    stage: value.stage,
    workPattern: value.workPattern,
    locationText: value.locationText,
    postedOn: value.postedOn,
    deadlineOn: value.deadlineOn,
    currency: value.compensation.currency || 'unknown',
    minAmount: centsInput(value.compensation.minAmountCents),
    maxAmount: centsInput(value.compensation.maxAmountCents),
    period: value.compensation.period || 'unknown',
    referenceHours: value.compensation.referenceHours?.toString() || '',
    basis: value.compensation.basis || 'unknown',
    benefitsText: value.compensation.benefitsText || '',
  };
}

const compensationFields: (keyof Draft)[] = [
  'currency',
  'minAmount',
  'maxAmount',
  'period',
  'referenceHours',
  'basis',
  'benefitsText',
];
const recordFields: (keyof Draft)[] = [
  'companyId',
  'title',
  'kind',
  'sourceUrl',
  'originalText',
  'notes',
  'stage',
  'workPattern',
  'locationText',
  'postedOn',
  'deadlineOn',
];
const fieldLabels: Record<keyof Draft, string> = {
  companyId: 'Company',
  title: 'Title',
  kind: 'Type',
  sourceUrl: 'Source URL',
  originalText: 'Original vacancy text',
  notes: 'Your notes',
  stage: 'Stage',
  workPattern: 'Work pattern',
  locationText: 'Location',
  postedOn: 'Posted date',
  deadlineOn: 'Deadline',
  currency: 'Currency',
  minAmount: 'Advertised minimum',
  maxAmount: 'Advertised maximum',
  period: 'Pay period',
  referenceHours: 'Reference hours',
  basis: 'Pay basis',
  benefitsText: 'Benefits text',
};

function changedFields(baseline: Draft, draft: Draft): (keyof Draft)[] {
  return [...recordFields, ...compensationFields].filter((key) => baseline[key] !== draft[key]);
}

function hasUnsafeAmount(record: Opportunity): boolean {
  return [record.compensation.minAmountCents, record.compensation.maxAmountCents].some(
    (value) => value !== undefined && !Number.isSafeInteger(value),
  );
}

function rebaseDraft(base: Opportunity, draft: Draft, current: Opportunity): Draft {
  const updated = fromOpportunity(current);
  for (const key of changedFields(fromOpportunity(base), draft))
    Object.assign(updated, { [key]: draft[key] });
  return updated;
}

function inputFromDraft(draft: Draft): CreateOpportunityRequest {
  if (!draft.companyId) throw new Error('Choose or create a company.');
  if (!draft.title.trim()) throw new Error('Enter an opportunity title.');
  if (!draft.sourceUrl.trim() && !draft.originalText.trim())
    throw new Error('Enter a source URL or original vacancy text.');
  const minAmountCents = parseCents(draft.minAmount, 'Advertised minimum');
  const maxAmountCents = parseCents(draft.maxAmount, 'Advertised maximum');
  if (
    maxAmountCents !== undefined &&
    (minAmountCents === undefined || maxAmountCents < minAmountCents)
  )
    throw new Error('Advertised maximum must be at least the minimum.');
  const currency = draft.currency.trim().toUpperCase();
  if (
    (minAmountCents !== undefined || maxAmountCents !== undefined) &&
    (!currency || currency === 'UNKNOWN')
  )
    throw new Error('Choose a currency for an advertised amount.');
  const referenceHours = draft.referenceHours.trim() ? Number(draft.referenceHours) : undefined;
  if (
    referenceHours !== undefined &&
    (!Number.isInteger(referenceHours) || referenceHours < 1 || referenceHours > 168)
  )
    throw new Error('Reference hours must be a whole number from 1 to 168.');
  return {
    companyId: draft.companyId,
    title: draft.title,
    kind: draft.kind,
    sourceUrl: draft.sourceUrl.trim(),
    originalText: draft.originalText,
    notes: draft.notes,
    stage: draft.stage,
    workPattern: draft.workPattern,
    locationText: draft.locationText,
    postedOn: draft.postedOn,
    deadlineOn: draft.deadlineOn,
    compensation: {
      currency: currency === 'UNKNOWN' || !currency ? 'unknown' : currency,
      minAmountCents,
      maxAmountCents,
      period: draft.period,
      referenceHours,
      basis: draft.basis,
      benefitsText: draft.benefitsText,
    },
  };
}

function patchFromDraft(
  baseline: Draft,
  draft: Draft,
  expectedRevision: number,
): PatchOpportunityRequest {
  const full = inputFromDraft(draft);
  const changed = changedFields(baseline, draft);
  if (changed.length === 0) throw new Error('No changes to save.');
  const patch: PatchOpportunityRequest = { expectedRevision };
  for (const key of recordFields) {
    if (changed.includes(key)) {
      // These field names and value types are identical in the generated create and patch contracts.
      Object.assign(patch, { [key]: full[key as keyof CreateOpportunityRequest] });
    }
  }
  if (changed.some((key) => compensationFields.includes(key)))
    patch.compensation = full.compensation;
  return patch;
}

function isOpportunity(value: unknown): value is Opportunity {
  if (!value || typeof value !== 'object') return false;
  const record = value as Record<string, unknown>;
  return (
    typeof record.id === 'string' &&
    typeof record.revision === 'number' &&
    typeof record.title === 'string'
  );
}

function OpportunityEditor({
  baseline,
  companies,
  companyCursor,
  companyLoading,
  companyError,
  onLoadMoreCompanies,
  onRetryCompanies,
  session,
  onSessionLost,
  onCompanyCreated,
  onSaved,
  onCancel,
}: {
  baseline?: Opportunity;
  companies: Company[];
  companyCursor: string;
  companyLoading: boolean;
  companyError: string | null;
  onLoadMoreCompanies: () => void;
  onRetryCompanies: () => void;
  session: Session;
  onSessionLost: () => void;
  onCompanyCreated: (company: Company) => void;
  onSaved: (id: string) => void;
  onCancel: () => void;
}) {
  const [baseRecord, setBaseRecord] = useState<Opportunity | undefined>(baseline);
  const [draft, setDraft] = useState<Draft>(() =>
    baseline ? fromOpportunity(baseline) : { ...blankDraft },
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [conflict, setConflict] = useState<Opportunity | null>(null);
  const [addingCompany, setAddingCompany] = useState(false);
  const [companyName, setCompanyName] = useState('');
  const [companyWebsite, setCompanyWebsite] = useState('');
  const [companyNotes, setCompanyNotes] = useState('');
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  const initial = baseRecord ? fromOpportunity(baseRecord) : null;
  const changes = initial ? changedFields(initial, draft) : [];
  const unsafeCompensation = !!baseRecord && hasUnsafeAmount(baseRecord);
  const rebased = conflict && baseRecord ? rebaseDraft(baseRecord, draft, conflict) : null;
  const effectiveChanges =
    conflict && rebased ? changedFields(fromOpportunity(conflict), rebased) : [];
  const cannotRebaseCompensation =
    !!conflict &&
    hasUnsafeAmount(conflict) &&
    effectiveChanges.some((key) => compensationFields.includes(key));

  function update<K extends keyof Draft>(key: K, value: Draft[K]) {
    setDraft((previous) => ({ ...previous, [key]: value }));
  }

  async function save(reviewedCurrent?: Opportunity) {
    setBusy(true);
    setError(null);
    try {
      const saveBase = reviewedCurrent || baseRecord;
      const saveDraft =
        reviewedCurrent && baseRecord ? rebaseDraft(baseRecord, draft, reviewedCurrent) : draft;
      if (
        saveBase &&
        hasUnsafeAmount(saveBase) &&
        changedFields(fromOpportunity(saveBase), saveDraft).some((key) =>
          compensationFields.includes(key),
        )
      )
        throw new Error(
          'The saved advertised amount exceeds browser precision. Compensation edits are disabled; you can still save other fields.',
        );
      const result = saveBase
        ? await patchOpportunity(
            saveBase.id,
            patchFromDraft(fromOpportunity(saveBase), saveDraft, saveBase.revision),
            session.csrfToken,
          )
        : await createOpportunity(inputFromDraft(draft), session.csrfToken);
      if (mounted.current) onSaved(result.opportunity.id);
    } catch (cause) {
      if (!mounted.current) return;
      if (isUnauthenticated(cause)) {
        onSessionLost();
        return;
      }
      if (
        cause instanceof RequestError &&
        cause.status === 409 &&
        isOpportunity(cause.details?.currentOpportunity)
      ) {
        setConflict(cause.details.currentOpportunity);
        setError(
          'This opportunity changed since you opened it. Your edits are still here. Compare them with the saved values below.',
        );
      } else setError(errorText(cause));
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  async function addCompany(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await createCompany(
        { name: companyName, website: companyWebsite, notes: companyNotes },
        session.csrfToken,
      );
      if (!mounted.current) return;
      onCompanyCreated(result.company);
      update('companyId', result.company.id);
      setAddingCompany(false);
      setCompanyName('');
      setCompanyWebsite('');
      setCompanyNotes('');
    } catch (cause) {
      if (!mounted.current) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  return (
    <div className="op-editor">
      <div className="op-heading-row">
        <div>
          <p className="eyebrow">{baseRecord ? `Revision ${baseRecord.revision}` : 'New record'}</p>
          <h2>{baseRecord ? 'Edit opportunity' : 'Save an opportunity'}</h2>
        </div>
        <button type="button" className="secondary" onClick={onCancel}>
          Cancel
        </button>
      </div>
      <section className="op-card" aria-label="Company selection">
        <label htmlFor="op-company">Company</label>
        <select
          id="op-company"
          value={draft.companyId}
          onChange={(event) => update('companyId', event.target.value)}
          required
        >
          <option value="">Choose a company</option>
          {companies.map((company) => (
            <option
              key={company.id}
              value={company.id}
              disabled={!!company.archivedAt && draft.companyId !== company.id}
            >
              {company.name}
              {company.archivedAt ? ' (archived)' : ''}
            </option>
          ))}
        </select>
        <div className="button-row">
          <button
            type="button"
            className="secondary"
            onClick={() => setAddingCompany((value) => !value)}
          >
            {addingCompany ? 'Close company form' : 'Create company'}
          </button>
          {companyCursor && (
            <button
              type="button"
              className="secondary"
              disabled={companyLoading}
              onClick={onLoadMoreCompanies}
            >
              Load more companies
            </button>
          )}
        </div>
        {companyLoading && <p role="status">Loading companies…</p>}
        {companyError && (
          <p role="alert" className="error">
            Could not load companies: {companyError}{' '}
            <button type="button" className="secondary" onClick={onRetryCompanies}>
              Try again
            </button>
          </p>
        )}
        {addingCompany && (
          <form className="op-subform" onSubmit={addCompany}>
            <h3>New company</h3>
            <label htmlFor="company-name">Name</label>
            <input
              id="company-name"
              value={companyName}
              onChange={(event) => setCompanyName(event.target.value)}
              required
              maxLength={200}
            />
            <label htmlFor="company-website">Website (optional)</label>
            <input
              id="company-website"
              type="url"
              value={companyWebsite}
              onChange={(event) => setCompanyWebsite(event.target.value)}
            />
            <label htmlFor="company-notes">Company notes (optional)</label>
            <textarea
              id="company-notes"
              value={companyNotes}
              onChange={(event) => setCompanyNotes(event.target.value)}
              maxLength={10000}
              rows={3}
            />
            <button type="submit" disabled={busy}>
              Save company and select it
            </button>
          </form>
        )}
      </section>
      <form
        className="op-form"
        onSubmit={(event) => {
          event.preventDefault();
          void save();
        }}
      >
        <section className="op-card">
          <h3>Vacancy and source</h3>
          <div className="op-grid">
            <label>
              Title
              <input
                value={draft.title}
                onChange={(event) => update('title', event.target.value)}
                required
                maxLength={300}
              />
            </label>
            <label>
              Type
              <select
                value={draft.kind}
                onChange={(event) => update('kind', event.target.value as Draft['kind'])}
              >
                <option value="employment">Employment</option>
                <option value="project">Project</option>
              </select>
            </label>
            <label>
              Stage
              <select value={draft.stage} onChange={(event) => update('stage', event.target.value)}>
                {!stages.includes(draft.stage) && (
                  <option value={draft.stage}>{draft.stage}</option>
                )}
                {stages.map((stage) => (
                  <option key={stage} value={stage}>
                    {stage.replaceAll('_', ' ')}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Work pattern
              <select
                value={draft.workPattern}
                onChange={(event) =>
                  update('workPattern', event.target.value as Draft['workPattern'])
                }
              >
                <option value="unknown">Unknown</option>
                <option value="onsite">On site</option>
                <option value="hybrid">Hybrid</option>
                <option value="remote">Remote</option>
              </select>
            </label>
            <label>
              Location
              <input
                value={draft.locationText}
                onChange={(event) => update('locationText', event.target.value)}
                maxLength={500}
              />
            </label>
            <label>
              Source URL
              <input
                type="url"
                value={draft.sourceUrl}
                onChange={(event) => update('sourceUrl', event.target.value)}
                maxLength={2048}
                placeholder="https://…"
              />
            </label>
            <label>
              Posted date
              <input
                type="date"
                value={draft.postedOn}
                onChange={(event) => update('postedOn', event.target.value)}
              />
            </label>
            <label>
              Deadline
              <input
                type="date"
                value={draft.deadlineOn}
                onChange={(event) => update('deadlineOn', event.target.value)}
              />
            </label>
          </div>
          <label>
            Original vacancy text
            <textarea
              value={draft.originalText}
              onChange={(event) => update('originalText', event.target.value)}
              maxLength={200000}
              rows={8}
            />
          </label>
          <p className="hint">
            Keep the wording from the source. Your own notes go below. A source URL or original text
            is required.
          </p>
          <label>
            Your notes
            <textarea
              value={draft.notes}
              onChange={(event) => update('notes', event.target.value)}
              maxLength={10000}
              rows={5}
            />
          </label>
        </section>
        <section className="op-card">
          <h3>Advertised compensation</h3>
          <p className="hint">
            These are advertised terms, not a confirmed offer for 32 hours. Unknown details stay
            unknown.
          </p>
          {unsafeCompensation && (
            <p role="alert" className="error">
              The saved amount exceeds browser precision. Compensation editing is disabled here; you
              can still edit the other fields.
            </p>
          )}
          <fieldset className="op-comp-fields" disabled={unsafeCompensation}>
            <div className="op-grid">
              <label>
                Currency
                <input
                  value={draft.currency}
                  onChange={(event) => update('currency', event.target.value)}
                  maxLength={7}
                  placeholder="EUR or unknown"
                />
              </label>
              <label>
                Minimum amount
                <input
                  inputMode="decimal"
                  value={draft.minAmount}
                  onChange={(event) => update('minAmount', event.target.value)}
                  placeholder="e.g. 6000.00"
                />
              </label>
              <label>
                Maximum amount
                <input
                  inputMode="decimal"
                  value={draft.maxAmount}
                  onChange={(event) => update('maxAmount', event.target.value)}
                  placeholder="optional"
                />
              </label>
              <label>
                Period
                <select
                  value={draft.period}
                  onChange={(event) => update('period', event.target.value as Draft['period'])}
                >
                  <option value="unknown">Unknown</option>
                  <option value="month">Month</option>
                  <option value="year">Year</option>
                  <option value="hour">Hour</option>
                  <option value="project">Project</option>
                </select>
              </label>
              <label>
                Reference hours/week
                <input
                  type="number"
                  min={1}
                  max={168}
                  step={1}
                  value={draft.referenceHours}
                  onChange={(event) => update('referenceHours', event.target.value)}
                  placeholder="unknown"
                />
              </label>
              <label>
                Pay basis
                <select
                  value={draft.basis}
                  onChange={(event) => update('basis', event.target.value as Draft['basis'])}
                >
                  <option value="unknown">Unknown</option>
                  <option value="base">Base</option>
                  <option value="inclusive">Inclusive</option>
                </select>
              </label>
            </div>
            <label>
              Benefits or pay context
              <textarea
                value={draft.benefitsText}
                onChange={(event) => update('benefitsText', event.target.value)}
                maxLength={10000}
                rows={3}
              />
            </label>
          </fieldset>
        </section>
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        {conflict && initial && rebased && (
          <section className="op-conflict" aria-label="Revision conflict">
            <h3>Compare before saving</h3>
            <p>
              Your draft is unchanged. The saved opportunity is now revision {conflict.revision}.
              Only these effective field changes will be sent if you choose to retry. Other saved
              fields, including unedited pay inputs, stay as they are.
            </p>
            <div className="op-compare-header">
              <strong>Field</strong>
              <strong>Your draft</strong>
              <strong>Current saved value</strong>
            </div>
            {effectiveChanges.map((key) => (
              <div className="op-compare-row" key={key}>
                <strong>{fieldLabels[key]}</strong>
                <span>{rebased[key] || 'Empty'}</span>
                <span>{fromOpportunity(conflict)[key] || 'Empty'}</span>
              </div>
            ))}
            {effectiveChanges.length === 0 && (
              <p>
                Your edits already match the saved version. Choose “Use saved version” to continue.
              </p>
            )}
            {cannotRebaseCompensation && (
              <p role="alert" className="error">
                The current advertised amount exceeds browser precision, so compensation changes
                cannot be safely rebased here.
              </p>
            )}
            <div className="button-row">
              <button
                type="button"
                disabled={busy || effectiveChanges.length === 0 || cannotRebaseCompensation}
                onClick={() => void save(conflict)}
              >
                Apply my changed fields to revision {conflict.revision}
              </button>
              <button
                type="button"
                className="secondary"
                onClick={() => {
                  setBaseRecord(conflict);
                  setDraft(fromOpportunity(conflict));
                  setConflict(null);
                  setError(null);
                }}
              >
                Use saved version
              </button>
            </div>
          </section>
        )}
        <div className="button-row">
          <button type="submit" disabled={busy || (!!baseRecord && changes.length === 0)}>
            {busy ? 'Saving…' : baseRecord ? 'Save changes' : 'Save opportunity'}
          </button>
        </div>
      </form>
    </div>
  );
}

function OpportunityHistory({
  id,
  refresh,
  onSessionLost,
}: {
  id: string;
  refresh: number;
  onSessionLost: () => void;
}) {
  const [changes, setChanges] = useState<RecordChange[]>([]);
  const [nextCursor, setNextCursor] = useState('');
  const [scanned, setScanned] = useState(0);
  const [watermark, setWatermark] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const controller = useRef<AbortController | null>(null);
  const generation = useRef(0);

  async function load(cursor = '', append = false) {
    controller.current?.abort();
    const current = ++generation.current;
    const request = new AbortController();
    controller.current = request;
    setLoading(true);
    setError(null);
    try {
      const page = await listOpportunityChanges(cursor, request.signal);
      if (request.signal.aborted || current !== generation.current) return;
      setChanges((prior) =>
        append
          ? [...prior, ...page.items.filter((item) => item.entityId === id)]
          : page.items.filter((item) => item.entityId === id),
      );
      setScanned((prior) => (append ? prior + page.items.length : page.items.length));
      setNextCursor(page.nextCursor || '');
      setWatermark(page.watermark);
    } catch (cause) {
      if (request.signal.aborted || current !== generation.current) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(errorText(cause));
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    return () => {
      generation.current++;
      controller.current?.abort();
    };
  }, [id, refresh]);

  return (
    <section className="op-card" aria-label="Opportunity history">
      <h2>Source and change history</h2>
      <p className="hint">
        Changes are scanned in sequence across all opportunities. A page can contain no changes for
        this one; continue to scan for later events.
      </p>
      {loading && <p role="status">Scanning changes… {scanned} global changes checked.</p>}
      {error && (
        <p role="alert" className="error">
          {error}{' '}
          <button
            type="button"
            className="secondary"
            onClick={() => void load(nextCursor, scanned > 0)}
          >
            Try again
          </button>
        </p>
      )}
      {!loading && changes.length === 0 && (
        <p>
          No changes for this opportunity in the {scanned} scanned events{nextCursor ? ' yet' : ''}.
        </p>
      )}
      <ol className="op-history">
        {changes.map((change) => (
          <li key={change.changeId}>
            <strong>{change.operation.replaceAll('.', ' ')}</strong> · revision{' '}
            {change.revisionAfter ?? 'unknown'} · {visibleDate(change.occurredAt)}
            <p className="muted">
              Recorded by{' '}
              {change.actorKind === 'administrator' ? 'owner' : `agent ${change.actorId}`} · change{' '}
              {change.changeId}
            </p>
            {change.snapshotState === 'unavailable_historical' ? (
              <p>Historical source snapshot unavailable for this older event.</p>
            ) : (
              <details>
                <summary>Source and notes at this revision</summary>
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
                <p>
                  <strong>Original vacancy text:</strong>
                </p>
                <pre className="op-source">
                  {typeof change.snapshot?.originalText === 'string'
                    ? change.snapshot.originalText
                    : 'Not recorded'}
                </pre>
                <p>
                  <strong>Notes:</strong>
                </p>
                <pre className="op-source">
                  {typeof change.snapshot?.notes === 'string'
                    ? change.snapshot.notes
                    : 'Not recorded in this snapshot'}
                </pre>
              </details>
            )}
          </li>
        ))}
      </ol>
      {nextCursor && (
        <button
          type="button"
          className="secondary"
          disabled={loading}
          onClick={() => void load(nextCursor, true)}
        >
          Load more history
        </button>
      )}
      {!nextCursor && !loading && !error && (
        <p className="hint">Scanned through change sequence {watermark}.</p>
      )}
    </section>
  );
}

function OpportunityDetail({
  id,
  companies,
  companyCursor,
  companyLoading,
  companyError,
  onLoadMoreCompanies,
  onRetryCompanies,
  session,
  onSessionLost,
  onCompanyCreated,
  onOpen,
  onBack,
}: {
  id: string;
  companies: Company[];
  companyCursor: string;
  companyLoading: boolean;
  companyError: string | null;
  onLoadMoreCompanies: () => void;
  onRetryCompanies: () => void;
  session: Session;
  onSessionLost: () => void;
  onCompanyCreated: (company: Company) => void;
  onOpen: (id: string) => void;
  onBack: () => void;
}) {
  const [view, setView] = useState<OpportunityView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [editing, setEditing] = useState(false);
  const [archiveConfirm, setArchiveConfirm] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    setView(null);
    getOpportunity(id, controller.signal)
      .then((result) => {
        if (!controller.signal.aborted) setView(result);
      })
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) {
          if (isUnauthenticated(cause)) onSessionLost();
          else setError(errorText(cause));
        }
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
      if (!mounted.current) return;
      setArchiveConfirm(false);
      setRefresh((value) => value + 1);
    } catch (cause) {
      if (!mounted.current) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else
        setError(
          errorText(cause) +
            (cause instanceof RequestError && cause.status === 409
              ? ' Refresh this record before trying again.'
              : ''),
        );
    } finally {
      if (mounted.current) setBusy(false);
    }
  }

  const opportunity = view?.opportunity;
  const company = companies.find((item) => item.id === opportunity?.companyId);
  return (
    <main className="op-main">
      <button type="button" className="secondary" onClick={onBack}>
        ← Back to opportunities
      </button>
      {loading && <p role="status">Loading opportunity…</p>}
      {error && (
        <p role="alert" className="error">
          {error}{' '}
          <button
            type="button"
            className="secondary"
            onClick={() => setRefresh((value) => value + 1)}
          >
            Try again
          </button>
        </p>
      )}
      {opportunity && (
        <>
          {editing ? (
            <OpportunityEditor
              key={opportunity.id}
              baseline={opportunity}
              companies={companies}
              companyCursor={companyCursor}
              companyLoading={companyLoading}
              companyError={companyError}
              onLoadMoreCompanies={onLoadMoreCompanies}
              onRetryCompanies={onRetryCompanies}
              session={session}
              onSessionLost={onSessionLost}
              onCompanyCreated={onCompanyCreated}
              onSaved={() => {
                setEditing(false);
                setRefresh((value) => value + 1);
              }}
              onCancel={() => {
                setEditing(false);
                setRefresh((value) => value + 1);
              }}
            />
          ) : (
            <>
              <div className="op-heading-row">
                <div>
                  <p className="eyebrow">
                    {opportunity.kind} · revision {opportunity.revision}
                    {opportunity.archivedAt ? ' · archived' : ''}
                  </p>
                  <h1>{opportunity.title}</h1>
                  <p>
                    {company?.name || `Company ${opportunity.companyId}`} ·{' '}
                    {opportunity.stage.replaceAll('_', ' ')} · {opportunity.workPattern} ·{' '}
                    {opportunity.locationText || 'Location unknown'}
                  </p>
                </div>
                {!opportunity.archivedAt && (
                  <button type="button" onClick={() => setEditing(true)}>
                    Edit
                  </button>
                )}
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
                <h2>Your notes</h2>
                <pre className="op-source">{opportunity.notes || 'No notes yet.'}</pre>
              </section>
              <section className="op-card">
                <h2>Advertised compensation</h2>
                <p>{compensationText(opportunity.compensation)}</p>
                {opportunity.compensation.benefitsText && (
                  <p>Benefits/context: {opportunity.compensation.benefitsText}</p>
                )}
                <p className="hint">
                  Advertised terms are unconfirmed. Qualification and actual pay at 32 hours have
                  not been assessed here.
                </p>
              </section>
              <section className="op-card">
                <h2>Possible duplicates</h2>
                {view!.likelyDuplicates.length === 0 ? (
                  <p>No likely duplicates found.</p>
                ) : (
                  <ul>
                    {view!.likelyDuplicates.map((duplicate) => (
                      <li key={duplicate.opportunity.id}>
                        <button
                          type="button"
                          className="op-text-button"
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
                  <p>Keep the record and its history, but hide it from the default active list.</p>
                  {archiveConfirm ? (
                    <div className="button-row">
                      <button type="button" disabled={busy} onClick={() => void archive()}>
                        Archive now
                      </button>
                      <button
                        type="button"
                        className="secondary"
                        onClick={() => setArchiveConfirm(false)}
                      >
                        Cancel
                      </button>
                    </div>
                  ) : (
                    <button
                      type="button"
                      className="secondary"
                      onClick={() => setArchiveConfirm(true)}
                    >
                      Archive opportunity…
                    </button>
                  )}
                </section>
              )}
            </>
          )}
          <OpportunityHistory
            key={`${id}:${refresh}`}
            id={id}
            refresh={refresh}
            onSessionLost={onSessionLost}
          />
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
  const [view, setView] = useState<
    { kind: 'list' } | { kind: 'new' } | { kind: 'detail'; id: string }
  >({ kind: 'list' });
  const [items, setItems] = useState<OpportunityView[]>([]);
  const [listCursor, setListCursor] = useState('');
  const [listLoading, setListLoading] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [progress, setProgress] = useState(0);
  const [companies, setCompanies] = useState<Company[]>([]);
  const [companyCursor, setCompanyCursor] = useState('');
  const [companyLoading, setCompanyLoading] = useState(false);
  const [companyError, setCompanyError] = useState<string | null>(null);
  const [search, setSearch] = useState('');
  const [stageFilter, setStageFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [archiveFilter, setArchiveFilter] = useState<'active' | 'archived' | 'all'>('active');
  const listRequest = useRef<AbortController | null>(null);
  const listGeneration = useRef(0);
  const companyRequest = useRef<AbortController | null>(null);
  const companyGeneration = useRef(0);

  async function loadCompanyPage(cursor = '') {
    companyRequest.current?.abort();
    const generation = ++companyGeneration.current;
    const controller = new AbortController();
    companyRequest.current = controller;
    setCompanyLoading(true);
    setCompanyError(null);
    try {
      const page = await listCompanies(cursor, controller.signal);
      if (controller.signal.aborted || generation !== companyGeneration.current) return;
      setCompanies((previous) => {
        const merged = new Map(previous.map((company) => [company.id, company]));
        for (const item of page.items) merged.set(item.company.id, item.company);
        return [...merged.values()];
      });
      setCompanyCursor(page.nextCursor || '');
    } catch (cause) {
      if (controller.signal.aborted || generation !== companyGeneration.current) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else setCompanyError(errorText(cause));
    } finally {
      if (generation === companyGeneration.current) setCompanyLoading(false);
    }
  }

  async function loadOpportunityBatch(cursor = '', append = false) {
    listRequest.current?.abort();
    const generation = ++listGeneration.current;
    const controller = new AbortController();
    listRequest.current = controller;
    setListLoading(true);
    setListError(null);
    setProgress(append ? items.length : 0);
    if (!append) {
      setItems([]);
      setListCursor('');
      setLoaded(false);
    }
    try {
      const collected: OpportunityView[] = append ? [...items] : [];
      let next = cursor;
      for (let pageNumber = 0; pageNumber < opportunityBatchPages; pageNumber++) {
        const page = await listOpportunities(next, controller.signal);
        if (controller.signal.aborted || generation !== listGeneration.current) return;
        collected.push(...page.items);
        setProgress(collected.length);
        next = page.nextCursor || '';
        if (!next) break;
      }
      setItems(collected);
      setListCursor(next);
      setLoaded(true);
    } catch (cause) {
      if (controller.signal.aborted || generation !== listGeneration.current) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else setListError(errorText(cause));
    } finally {
      if (generation === listGeneration.current) setListLoading(false);
    }
  }

  useEffect(() => {
    void loadCompanyPage();
    void loadOpportunityBatch();
    return () => {
      listGeneration.current++;
      companyGeneration.current++;
      listRequest.current?.abort();
      companyRequest.current?.abort();
    };
  }, []);

  function openDetail(id: string) {
    listRequest.current?.abort();
    listGeneration.current++;
    setView({ kind: 'detail', id });
  }
  function backToList() {
    setView({ kind: 'list' });
    void loadOpportunityBatch();
  }
  function companyCreated(company: Company) {
    setCompanies((prior) => [company, ...prior]);
  }

  if (view.kind === 'new')
    return (
      <main className="op-main">
        <OpportunityEditor
          companies={companies}
          companyCursor={companyCursor}
          companyLoading={companyLoading}
          companyError={companyError}
          onLoadMoreCompanies={() => void loadCompanyPage(companyCursor)}
          onRetryCompanies={() => void loadCompanyPage(companyCursor)}
          session={session}
          onSessionLost={onSessionLost}
          onCompanyCreated={companyCreated}
          onSaved={openDetail}
          onCancel={backToList}
        />
      </main>
    );
  if (view.kind === 'detail')
    return (
      <OpportunityDetail
        key={view.id}
        id={view.id}
        companies={companies}
        companyCursor={companyCursor}
        companyLoading={companyLoading}
        companyError={companyError}
        onLoadMoreCompanies={() => void loadCompanyPage(companyCursor)}
        onRetryCompanies={() => void loadCompanyPage(companyCursor)}
        session={session}
        onSessionLost={onSessionLost}
        onCompanyCreated={companyCreated}
        onOpen={openDetail}
        onBack={backToList}
      />
    );

  const companyNames = new Map(companies.map((company) => [company.id, company.name]));
  const query = search.trim().toLowerCase();
  const filtered = items.filter(({ opportunity }) => {
    if (
      (archiveFilter === 'active' && opportunity.archivedAt) ||
      (archiveFilter === 'archived' && !opportunity.archivedAt)
    )
      return false;
    if (
      (stageFilter && opportunity.stage !== stageFilter) ||
      (kindFilter && opportunity.kind !== kindFilter)
    )
      return false;
    return (
      !query ||
      [opportunity.title, opportunity.locationText, opportunity.sourceUrl].some((value) =>
        value.toLowerCase().includes(query),
      )
    );
  });
  const stageValues = [...new Set([...stages, ...items.map((item) => item.opportunity.stage)])];
  return (
    <main className="op-main">
      <div className="op-heading-row">
        <div>
          <p className="eyebrow">Private workspace</p>
          <h1>Opportunities</h1>
          <p>
            Track sourced vacancies and your own notes. Fit and actual 32-hour pay still need
            evidence.
          </p>
        </div>
        <button
          type="button"
          onClick={() => {
            listRequest.current?.abort();
            listGeneration.current++;
            setView({ kind: 'new' });
          }}
        >
          Save opportunity
        </button>
      </div>
      <section className="op-card" aria-label="Opportunity filters">
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
              {stageValues.map((stage) => (
                <option key={stage} value={stage}>
                  {stage.replaceAll('_', ' ')}
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
        </div>
        <p className="hint">
          {listLoading
            ? `Loading records… ${progress} received in this batch.`
            : loaded
              ? `${filtered.length} matching records among ${items.length} loaded${listCursor ? '; more records are available. Search covers loaded records only.' : '; all records loaded.'}`
              : 'Waiting for records.'}
        </p>
        {listCursor && !listLoading && (
          <button
            type="button"
            className="secondary"
            onClick={() => void loadOpportunityBatch(listCursor, true)}
          >
            Load next 500 records
          </button>
        )}
        {listError && (
          <p role="alert" className="error">
            {listError}{' '}
            <button
              type="button"
              className="secondary"
              onClick={() => void loadOpportunityBatch(listCursor, items.length > 0)}
            >
              Try again
            </button>
          </p>
        )}
        {companyLoading && <p role="status">Loading companies…</p>}
        {companyError && (
          <p role="alert" className="error">
            Could not load companies: {companyError}{' '}
            <button
              type="button"
              className="secondary"
              onClick={() => void loadCompanyPage(companyCursor)}
            >
              Try again
            </button>
          </p>
        )}
        {companyCursor && (
          <button
            type="button"
            className="secondary"
            onClick={() => void loadCompanyPage(companyCursor)}
          >
            Load more company names
          </button>
        )}
      </section>
      {!listLoading && loaded && filtered.length === 0 && (
        <section className="op-card">
          <h2>No matching opportunities</h2>
          <p>
            {items.length === 0
              ? 'Save the first sourced vacancy to begin.'
              : 'Try a different search or filter.'}
          </p>
        </section>
      )}
      {!listLoading && (
        <ul className="op-list">
          {filtered.map(({ opportunity, likelyDuplicates }) => (
            <li key={opportunity.id} className="op-card">
              <div className="op-heading-row">
                <div>
                  <button
                    type="button"
                    className="op-text-button"
                    onClick={() => openDetail(opportunity.id)}
                  >
                    {opportunity.title}
                  </button>
                  <p>
                    {companyNames.get(opportunity.companyId) || `Company ${opportunity.companyId}`}{' '}
                    · {opportunity.kind} · {opportunity.stage.replaceAll('_', ' ')}
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
                {likelyDuplicates.length
                  ? `${likelyDuplicates.length} possible duplicate${likelyDuplicates.length === 1 ? '' : 's'} to review`
                  : 'No likely duplicate found'}{' '}
                · Fit not assessed
              </p>
            </li>
          ))}
        </ul>
      )}
    </main>
  );
}
