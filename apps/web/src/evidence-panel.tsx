import { useEffect, useState } from 'react';
import { OwnerInstructionPanel } from './owner-instruction';
import {
  getQualification,
  isUnauthenticated,
  listEvidence,
  listEvidenceSources,
  listOfferOptionSets,
  listQualificationHistory,
  type EvidenceClaim,
  type EvidenceSource,
  type OfferOptionSet,
  type Opportunity,
  type QualificationEvaluation,
  type QualificationView,
  type Session,
} from './api';
import './evidence-panel.css';

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Could not load this information.';
}

function words(value: string): string {
  return value.replaceAll('_', ' ');
}

function Evaluation({ value }: { value: QualificationEvaluation }) {
  return (
    <div className="evidence-evaluation">
      <p>
        <strong>Overall:</strong> {words(value.overall)} · saved{' '}
        {new Date(value.createdAt).toLocaleString()}
      </p>
      {value.optionSetStatus !== 'none' && <p>Offer options: {words(value.optionSetStatus)}</p>}
      <ul>
        {value.criteria.map((criterion, index) => (
          <li key={`${criterion.criterion}:${criterion.criterionId || index}`}>
            <strong>{criterion.label || words(criterion.criterion)}:</strong>{' '}
            {words(criterion.state)} — {criterion.reason}
            {criterion.conflicting && ' (conflicting evidence)'}
          </li>
        ))}
      </ul>
      <p>
        <strong>Salary:</strong> {words(value.salary.state)} — {value.salary.reason}
      </p>
      {value.salary.estimate && (
        <p>
          Estimated base pay at {value.salary.estimate.targetHours} hours/week:{' '}
          {(value.salary.estimate.minDisplayCents / 100).toLocaleString(undefined, {
            minimumFractionDigits: 2,
            maximumFractionDigits: 2,
          })}
          –
          {(value.salary.estimate.maxDisplayCents / 100).toLocaleString(undefined, {
            minimumFractionDigits: 2,
            maximumFractionDigits: 2,
          })}{' '}
          {value.salary.estimate.currency} monthly.{' '}
          {value.salary.confirmedActual ? 'Actual hours confirmed.' : 'Actual hours unconfirmed.'}
        </p>
      )}
      {value.optionResults.map((option) => (
        <div key={option.optionId} className="evidence-option">
          <h4>
            {option.label} · {words(option.overall)}
          </h4>
          <ul>
            {option.criteria.map((criterion, index) => (
              <li key={`${criterion.criterion}:${criterion.criterionId || index}`}>
                <strong>{criterion.label || words(criterion.criterion)}:</strong>{' '}
                {words(criterion.state)} — {criterion.reason}
              </li>
            ))}
          </ul>
          <p>
            <strong>Salary:</strong> {words(option.salary.state)} — {option.salary.reason}
          </p>
        </div>
      ))}
    </div>
  );
}

function Claim({ value, onCorrect }: { value: EvidenceClaim; onCorrect: () => void }) {
  return (
    <li>
      <strong>{value.roleDefinition?.label || words(value.criterion)}:</strong>{' '}
      {words(value.finding)}
      {value.rolePresence && ` · ${words(value.rolePresence)}`}
      {value.offerOptionId && ` · offer option ${value.offerOptionId}`}
      <p>{value.observedValue}</p>
      <blockquote>{value.sourceExcerpt || 'Source excerpt unavailable'}</blockquote>
      {value.hours && (
        <p>
          Hours: {value.hours.minWeekly}–{value.hours.maxWeekly} per week
          {value.hours.hardBounds ? ' (hard bounds)' : ''}
        </p>
      )}
      {value.arrangement && (
        <p>
          Arrangement: {value.arrangement.pattern}
          {value.arrangement.baseLocation && ` · ${value.arrangement.baseLocation}`}
        </p>
      )}
      {value.salary && (
        <p>
          Salary:{' '}
          {(value.salary.amountCents / 100).toLocaleString(undefined, {
            minimumFractionDigits: 2,
            maximumFractionDigits: 2,
          })}{' '}
          {value.salary.currency} per {value.salary.period} · {value.salary.basis} ·{' '}
          {value.salary.actualWeeklyHours} hours/week
        </p>
      )}
      <p className="hint">
        Source {value.sourceId} · recorded {new Date(value.createdAt).toLocaleString()}
        {value.supersedesId && ` · supersedes ${value.supersedesId}`}
      </p>
      <button type="button" className="secondary" onClick={onCorrect}>
        Correct this evidence claim
      </button>
    </li>
  );
}

export function EvidencePanel({
  opportunity,
  session,
  onSessionLost,
}: {
  opportunity: Opportunity;
  session: Session;
  onSessionLost: () => void;
}) {
  const [correction, setCorrection] = useState<EvidenceClaim | null>(null);
  const [qualification, setQualification] = useState<QualificationView | null>(null);
  const [sources, setSources] = useState<EvidenceSource[]>([]);
  const [claims, setClaims] = useState<EvidenceClaim[]>([]);
  const [optionSets, setOptionSets] = useState<OfferOptionSet[]>([]);
  const [history, setHistory] = useState<QualificationEvaluation[]>([]);
  const [sourceCursor, setSourceCursor] = useState('');
  const [claimCursor, setClaimCursor] = useState('');
  const [historyCursor, setHistoryCursor] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const id = opportunity.id;

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    Promise.all([
      getQualification(id, controller.signal),
      listEvidenceSources(id, '', controller.signal),
      listEvidence(id, '', controller.signal),
      listOfferOptionSets(id, controller.signal),
      listQualificationHistory(id, '', controller.signal),
    ])
      .then(([assessment, sourcePage, claimPage, sets, historyPage]) => {
        if (controller.signal.aborted) return;
        setQualification(assessment);
        setSources(sourcePage.items);
        setSourceCursor(sourcePage.nextCursor || '');
        setClaims(claimPage.items);
        setClaimCursor(claimPage.nextCursor || '');
        setOptionSets(sets.items);
        setHistory(historyPage.items);
        setHistoryCursor(historyPage.nextCursor || '');
      })
      .catch((cause) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [id, onSessionLost, refresh]);

  async function loadMore(kind: 'sources' | 'claims' | 'history') {
    const cursor =
      kind === 'sources' ? sourceCursor : kind === 'claims' ? claimCursor : historyCursor;
    setError(null);
    try {
      if (kind === 'sources') {
        const page = await listEvidenceSources(id, cursor);
        setSources((old) => [...old, ...page.items]);
        setSourceCursor(page.nextCursor || '');
      } else if (kind === 'claims') {
        const page = await listEvidence(id, cursor);
        setClaims((old) => [...old, ...page.items]);
        setClaimCursor(page.nextCursor || '');
      } else {
        const page = await listQualificationHistory(id, cursor);
        setHistory((old) => [...old, ...page.items]);
        setHistoryCursor(page.nextCursor || '');
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(message(cause));
    }
  }

  return (
    <section className="op-card" aria-label="Qualification and evidence">
      <h2>Qualification and evidence</h2>
      {loading && <p role="status">Loading qualification and sources…</p>}
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
      {!loading && qualification && (
        <>
          <section>
            <h3>Current assessment</h3>
            <p>Status: {words(qualification.status)}</p>
            {qualification.refreshError && <p role="alert">{qualification.refreshError}</p>}
            {qualification.current ? (
              <Evaluation value={qualification.current} />
            ) : (
              <p>No current assessment is available.</p>
            )}
            {qualification.status === 'outdated' && qualification.latestHistorical && (
              <details>
                <summary>Latest saved assessment</summary>
                <Evaluation value={qualification.latestHistorical} />
              </details>
            )}
          </section>
          <section>
            <h3>Offer options</h3>
            {optionSets.length === 0 ? (
              <p>No sourced offer options recorded.</p>
            ) : (
              <ul>
                {optionSets.map((set) => (
                  <li key={set.id}>
                    <strong>{set.options.map((option) => option.label).join(' / ')}</strong>
                    <blockquote>{set.sourceExcerpt}</blockquote>
                    <p className="hint">
                      Source {set.sourceId} · recorded {new Date(set.createdAt).toLocaleString()}
                    </p>
                  </li>
                ))}
              </ul>
            )}
          </section>
          <section>
            <h3>Evidence claims</h3>
            {claims.length === 0 ? (
              <p>No evidence claims recorded.</p>
            ) : (
              <ol>
                {claims.map((claim) => (
                  <Claim key={claim.id} value={claim} onCorrect={() => setCorrection(claim)} />
                ))}
              </ol>
            )}
            {claimCursor && (
              <button className="secondary" type="button" onClick={() => void loadMore('claims')}>
                Load more claims
              </button>
            )}
          </section>
          <section>
            <h3>Sources</h3>
            {sources.length === 0 ? (
              <p>No evidence sources recorded.</p>
            ) : (
              <ol>
                {sources.map((source) => (
                  <li key={source.id}>
                    <strong>{words(source.sourceKind)}</strong> ·{' '}
                    {new Date(source.recordedAt).toLocaleString()}
                    {source.sourceUrl && <p>{source.sourceUrl}</p>}
                    {source.speakerName && (
                      <p>
                        {source.speakerName}
                        {source.speakerRole && `, ${source.speakerRole}`}
                        {source.speakerOrganisation && ` at ${source.speakerOrganisation}`}
                      </p>
                    )}
                    <details>
                      <summary>Original source text</summary>
                      <pre className="op-source">{source.originalText}</pre>
                    </details>
                  </li>
                ))}
              </ol>
            )}
            {sourceCursor && (
              <button className="secondary" type="button" onClick={() => void loadMore('sources')}>
                Load more sources
              </button>
            )}
          </section>
          {correction && (
            <OwnerInstructionPanel
              key={correction.id}
              target={{
                targetKind: 'evidence',
                targetId: correction.id,
                expectedRevision: 1,
              }}
              title="Correct evidence claim"
              session={session}
              onSessionLost={onSessionLost}
              onClose={() => setCorrection(null)}
            />
          )}
          <section>
            <h3>Assessment history</h3>
            {history.length === 0 ? (
              <p>No saved assessments.</p>
            ) : (
              <ol>
                {history.map((evaluation) => (
                  <li key={evaluation.id}>
                    <details>
                      <summary>
                        {words(evaluation.overall)} ·{' '}
                        {new Date(evaluation.createdAt).toLocaleString()}
                      </summary>
                      <Evaluation value={evaluation} />
                    </details>
                  </li>
                ))}
              </ol>
            )}
            {historyCursor && (
              <button className="secondary" type="button" onClick={() => void loadMore('history')}>
                Load more assessments
              </button>
            )}
          </section>
        </>
      )}
    </section>
  );
}
