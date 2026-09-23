import { useEffect, useState } from 'react';
import { getOpportunityScreening, isUnauthenticated, type RoundScreeningView } from './api';

const scopeLabels: Record<string, string> = {
  present: 'Present in the supplied role evidence',
  explicit_absent: 'Explicitly excluded by supplied role evidence',
  mention_only: 'Mentioned without the requested duty scope',
  ambiguous: 'Ambiguous scope',
  conflicting: 'Conflicting source statements',
  no_relevant_evidence: 'No relevant supplied evidence',
};

export function ScreeningPanel({
  opportunityId,
  onSessionLost,
}: {
  opportunityId: string;
  onSessionLost: () => void;
}) {
  const [view, setView] = useState<RoundScreeningView | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    getOpportunityScreening(opportunityId, controller.signal)
      .then(setView)
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError('Sourced screening is unavailable. Refresh to try again.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [opportunityId, refresh, onSessionLost]);
  return (
    <section className="op-card" aria-label="Sourced screening proposal">
      <div className="op-heading-row">
        <h2>Sourced screening proposal</h2>
        <button
          className="secondary"
          type="button"
          disabled={loading}
          onClick={() => setRefresh((old) => old + 1)}
        >
          Refresh screening
        </button>
      </div>
      <p className="hint">
        Jev assesses supplied vacancy facts. This proposal is not a confirmed qualification,
        employer term, or application decision.
      </p>
      {loading && <p role="status">Reading screening…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!view && !loading && !error && <p>No screening response is available.</p>}
      {view && (
        <>
          <p>
            Status: <strong>{view.status.replaceAll('_', ' ')}</strong> · confirmed qualification:
            no
          </p>
          {view.status === 'not_assessed' && (
            <p>No sourced screening proposal has been recorded for the current role.</p>
          )}
          {view.status === 'outdated' && (
            <p>
              Earlier screening is outdated by source, role, or profile changes; it is not a current
              judgment.
            </p>
          )}
          {view.status === 'unresolved' && (
            <p>
              Supplied facts remain insufficient or conflicting; inspect the source and ask the
              agency to investigate.
            </p>
          )}
          {view.current && (
            <>
              <p>
                Assessed role revision {view.current.opportunityRevision} against brief version{' '}
                {view.current.profileVersion}, saved{' '}
                {new Date(view.current.createdAt).toLocaleString()}.
              </p>
              {view.current.omittedBytes > 0 && (
                <p>
                  {view.current.omittedBytes} source bytes were omitted from the bounded assessment.
                </p>
              )}
              <h3>Factual scope observations</h3>
              {view.current.result.observations.length === 0 && (
                <p>No criterion observations were saved.</p>
              )}
              <ul className="op-list">
                {view.current.result.observations.map((observation) => {
                  const criterion = view.current!.input.criteria.find(
                    (item) => item.id === observation.criterion_id,
                  );
                  const supports = observation.proposed_support.map((support) =>
                    view.current!.input.spans.find((span) => span.id === support.span_id),
                  );
                  return (
                    <li className="op-card" key={observation.criterion_id}>
                      <h4>{criterion?.label || 'Criterion label unavailable'}</h4>
                      {criterion && (
                        <p className="hint">Requested scope: {criterion.description}</p>
                      )}
                      <p>
                        <strong>Jev observation:</strong>{' '}
                        {scopeLabels[observation.scope] || observation.scope.replaceAll('_', ' ')}.
                      </p>
                      {(observation.scope === 'ambiguous' ||
                        observation.scope === 'conflicting' ||
                        observation.scope === 'no_relevant_evidence') && (
                        <p role="status">
                          This is unknown or conflicting; it must not be treated as a confirmed
                          match or exclusion.
                        </p>
                      )}
                      <p className="hint">
                        Support state: {observation.support_state.replaceAll('_', ' ')}. Selected
                        excerpts are proposed citations only.
                      </p>
                      {supports.length ? (
                        <ul>
                          {supports.map((span, index) => (
                            <li key={`${observation.criterion_id}-${index}`}>
                              {span ? (
                                <>
                                  <strong>{span.source_kind}:</strong> “{span.excerpt}”
                                </>
                              ) : (
                                'Selected source span is unavailable.'
                              )}
                            </li>
                          ))}
                        </ul>
                      ) : (
                        <p>No supporting excerpt was selected.</p>
                      )}
                    </li>
                  );
                })}
              </ul>
              <details>
                <summary>Recorded assessment provenance</summary>
                <p>
                  Source and model references are stored for review; scope labels do not establish
                  qualification.
                </p>
                <p>
                  Input digest: {view.current.result.input_sha256}. Source revision:{' '}
                  {view.current.sourceRevision}.
                </p>
              </details>
            </>
          )}
        </>
      )}
    </section>
  );
}
