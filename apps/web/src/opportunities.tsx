import { useEffect, useState } from 'react';
import { EvidencePanel } from './evidence-panel';
import { AgencyHome } from './agency-home';
import { OwnerDecisionControls } from './owner-decision';
import { OwnerInstructionPanel } from './owner-instruction';
import { RelationshipsPanel } from './relationships-panel';
import { ApplicationPackPanel } from './application-pack-panel';
import { InterviewPanel } from './interview-panel';
import { ScreeningPanel } from './screening-panel';
import {
  explainResearchIdentity,
  getOpportunity,
  getRuntimeStatus,
  getOpportunityOrganisation,
  getOrganisationCategories,
  getOrganisationSummaries,
  isUnauthenticated,
  listCompanies,
  listOpportunityChanges,
  listOpportunities,
  type Company,
  type OrganisationCategory,
  type OrganisationSummary,
  type OrganisationView,
  type OwnerDecision,
  type Opportunity,
  type OpportunityView,
  type RecordChange,
  type ResearchIdentityView,
  type RuntimeStatus,
  type Session,
} from './api';
import {
  identityHeadline,
  isMissingIdentity,
  isServiceUnavailable,
  isUnassessedStage,
  sightingSummary,
} from './record-judgment';
import './opportunities.css';

const selectedKey = 'jobseek.selected-opportunity';

function errorText(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Something went wrong. Try again.';
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

function ResearchIdentityPanel({
  opportunity,
  session,
  onSessionLost,
  onOpen,
}: {
  opportunity: Opportunity;
  session: Session;
  onSessionLost: () => void;
  onOpen: (id: string) => void;
}) {
  const [view, setView] = useState<ResearchIdentityView | null>(null);
  const [state, setState] = useState<'loading' | 'ready' | 'unavailable' | 'missing' | 'error'>(
    'loading',
  );
  const [error, setError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [correcting, setCorrecting] = useState(false);
  const id = opportunity.id;
  useEffect(() => {
    const controller = new AbortController();
    setState('loading');
    setError(null);
    explainResearchIdentity('vacancy', id, controller.signal)
      .then((result) => {
        if (controller.signal.aborted) return;
        setView(result);
        setState('ready');
      })
      .catch((cause) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else if (isServiceUnavailable(cause)) setState('unavailable');
        else if (isMissingIdentity(cause)) setState('missing');
        else {
          setError(errorText(cause));
          setState('error');
        }
      });
    return () => controller.abort();
  }, [id, onSessionLost, refresh]);
  return (
    <section className="op-card" aria-label="Research identity and sightings">
      <div className="op-heading-row">
        <h2>Research identity and sightings</h2>
        {state !== 'loading' && (
          <button
            className="secondary"
            type="button"
            onClick={() => setRefresh((value) => value + 1)}
          >
            Refresh
          </button>
        )}
      </div>
      {state === 'loading' && <p role="status">Loading identity explanation…</p>}
      {state === 'unavailable' && (
        <p className="hint">
          Research supervision is not connected yet. The identity explanation and source sightings
          for this record will appear here once it is.
        </p>
      )}
      {state === 'missing' && (
        <p className="hint">
          No research identity record exists for this opportunity. It was saved by hand or before
          research saves began.
        </p>
      )}
      {state === 'error' && (
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
      {state === 'ready' && view && (
        <>
          <p>
            <strong>{identityHeadline(view.decision)}</strong>
          </p>
          <p>{view.basis}</p>
          <h3>Compared candidates</h3>
          {view.candidates.length === 0 ? (
            <p className="hint">No other saved roles were compared.</p>
          ) : (
            <ul>
              {view.candidates.map((candidate) => (
                <li key={candidate.recordId}>
                  <button
                    className="op-text-button"
                    type="button"
                    onClick={() => onOpen(candidate.recordId)}
                  >
                    {candidate.recordId}
                  </button>{' '}
                  · revision {candidate.revision}
                </li>
              ))}
            </ul>
          )}
          {view.assessmentId ? (
            <p>
              Supporting assessment: <code>{view.assessmentId}</code>
            </p>
          ) : (
            <p className="hint">No supporting assessment was recorded.</p>
          )}
          <h3>Source sightings</h3>
          <p className="hint">{sightingSummary(view)}</p>
          {(view.sightings?.length ?? 0) > 0 && (
            <ol className="op-history">
              {(view.sightings ?? []).map((sighting) => (
                <li key={`${sighting.captureId}-${sighting.observedAt}`}>
                  <SourceLink value={sighting.observedUrl} />
                  <p className="hint">
                    Observed {new Date(sighting.observedAt).toLocaleString()} · capture{' '}
                    <code>{sighting.captureId}</code>
                  </p>
                </li>
              ))}
            </ol>
          )}
          {correcting ? (
            <OwnerInstructionPanel
              target={{
                targetKind: 'opportunity',
                targetId: id,
                expectedRevision: opportunity.revision,
              }}
              title="Correct this identity decision"
              session={session}
              onSessionLost={onSessionLost}
              onClose={() => setCorrecting(false)}
            />
          ) : (
            <button className="secondary" type="button" onClick={() => setCorrecting(true)}>
              Question this identity decision
            </button>
          )}
        </>
      )}
    </section>
  );
}

function OpportunityDetail({
  id,
  initialPackId,
  focusPreparation,
  initialInterviewId,
  initialDebriefId,
  session,
  companies,
  onSessionLost,
  onOpen,
  onBack,
}: {
  id: string;
  initialPackId?: string;
  focusPreparation?: boolean;
  initialInterviewId?: string;
  initialDebriefId?: string;
  session: Session;
  companies: Company[];
  onSessionLost: () => void;
  onOpen: (id: string) => void;
  onBack: () => void;
}) {
  const [view, setView] = useState<OpportunityView | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [refresh, setRefresh] = useState(0);
  const [ownerDecision, setOwnerDecision] = useState<OwnerDecision | null>(null);
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
                · {opportunity.stage.replaceAll('_', ' ')}{' '}
                {isUnassessedStage(opportunity) && (
                  <span className="op-fit-badge">Unassessed — fit not yet judged</span>
                )}{' '}
                · {opportunity.workPattern} · {opportunity.locationText || 'Location unknown'}
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
          <EvidencePanel
            opportunity={opportunity}
            session={session}
            onSessionLost={onSessionLost}
          />
          <ScreeningPanel opportunityId={id} onSessionLost={onSessionLost} />
          <OwnerDecisionControls
            id={id}
            revision={opportunity.revision}
            session={session}
            onSessionLost={onSessionLost}
            onChanged={setOwnerDecision}
          />
          <ApplicationPackPanel
            opportunity={opportunity}
            selected={ownerDecision?.decision === 'selected'}
            initialPackId={initialPackId}
            focusPreparation={focusPreparation}
            session={session}
            onSessionLost={onSessionLost}
          />
          <InterviewPanel
            opportunity={opportunity}
            initialInterviewId={initialInterviewId}
            initialDebriefId={initialDebriefId}
            session={session}
            onSessionLost={onSessionLost}
            onOpenCampaign={onBack}
          />
          <RelationshipsPanel opportunityId={id} session={session} onSessionLost={onSessionLost} />
          <OwnerInstructionPanel
            key={`${id}-${opportunity.revision}`}
            target={{
              targetKind: 'opportunity',
              targetId: id,
              expectedRevision: opportunity.revision,
            }}
            title="Instruction for this opportunity"
            session={session}
            onSessionLost={onSessionLost}
          />
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
          <ResearchIdentityPanel
            opportunity={opportunity}
            session={session}
            onSessionLost={onSessionLost}
            onOpen={onOpen}
          />
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
  const [initialPackId, setInitialPackId] = useState('');
  const [focusPreparation, setFocusPreparation] = useState(false);
  const [briefVersion, setBriefVersion] = useState<number | null>(null);
  const [currentProfileVersion, setCurrentProfileVersion] = useState<number | null>(null);
  const [briefInstruction, setBriefInstruction] = useState(false);
  useEffect(() => {
    if (
      briefInstruction &&
      currentProfileVersion !== null &&
      currentProfileVersion !== briefVersion
    )
      setBriefVersion(currentProfileVersion);
  }, [briefInstruction, briefVersion, currentProfileVersion]);
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
  const [initialInterviewId, setInitialInterviewId] = useState('');
  const [initialDebriefId, setInitialDebriefId] = useState('');
  const [stageFilter, setStageFilter] = useState('');
  const [kindFilter, setKindFilter] = useState('');
  const [archiveFilter, setArchiveFilter] = useState<'active' | 'archived' | 'all'>('active');
  function openOpportunity(id: string | null) {
    setSelected(id);
    setInitialPackId('');
    setFocusPreparation(false);
    setInitialInterviewId('');
    setInitialDebriefId('');
    try {
      if (id) localStorage.setItem(selectedKey, id);
      else localStorage.removeItem(selectedKey);
    } catch {
      /* Selection remains available in this page. */
    }
  }
  function openRecommendedPack(opportunityId: string, packId: string) {
    openOpportunity(opportunityId);
    setInitialPackId(packId);
  }
  function openRecommendedPreparation(opportunityId: string) {
    openOpportunity(opportunityId);
    setFocusPreparation(true);
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
        <OpportunityDetail
          key={selected}
          id={selected}
          initialPackId={initialPackId}
          focusPreparation={focusPreparation}
          initialInterviewId={initialInterviewId}
          initialDebriefId={initialDebriefId}
          session={session}
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
        onBriefLoaded={setCurrentProfileVersion}
        onOpenOpportunity={(id) => openOpportunity(id)}
        onOpenPack={openRecommendedPack}
        onOpenPreparation={openRecommendedPreparation}
        onOpenInterview={(opportunityId, interviewId, debriefId) => {
          openOpportunity(opportunityId);
          setInitialInterviewId(interviewId);
          setInitialDebriefId(debriefId || '');
        }}
        onEditBrief={(version) => {
          setBriefInstruction(true);
          setBriefVersion(version);
        }}
      />
      {briefInstruction && briefVersion !== null ? (
        <OwnerInstructionPanel
          target={{ targetKind: 'profile', targetId: 'current', expectedRevision: briefVersion }}
          title="Instruction for your campaign brief"
          session={session}
          onSessionLost={onSessionLost}
          onClose={() => {
            setBriefInstruction(false);
          }}
        />
      ) : currentProfileVersion !== null ? (
        <OwnerInstructionPanel
          target={{
            targetKind: 'campaign',
            targetId: 'active',
            expectedRevision: currentProfileVersion,
          }}
          title="Add a link or vacancy"
          session={session}
          onSessionLost={onSessionLost}
        />
      ) : (
        <section className="op-card">
          <h2>Add a link or vacancy</h2>
          <p>Reading the current brief before accepting material…</p>
        </section>
      )}
      <RelationshipsPanel session={session} onSessionLost={onSessionLost} />
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
                  onClick={() => openOpportunity(opportunity.id)}
                >
                  {opportunity.title}
                </button>
                <p>
                  {names.get(opportunity.companyId) || `Company ${opportunity.companyId}`} ·{' '}
                  {opportunity.kind} · {opportunity.stage.replaceAll('_', ' ')}{' '}
                  {isUnassessedStage(opportunity) && (
                    <span className="op-fit-badge">Unassessed — fit not yet judged</span>
                  )}
                  {opportunity.archivedAt ? ' · archived' : ''}
                </p>
              </div>
              <span className="op-revision">Revision {opportunity.revision}</span>
            </div>
            <OwnerDecisionControls
              id={opportunity.id}
              revision={opportunity.revision}
              session={session}
              onSessionLost={onSessionLost}
            />
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
