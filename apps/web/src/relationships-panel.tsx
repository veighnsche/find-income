import { useEffect, useState } from 'react';
import {
  isUnauthenticated,
  listOpportunityRoutes,
  listRelationshipCounterparties,
  listRelationshipEvents,
  type OpportunityRoute,
  type RelationshipCounterparty,
  type RelationshipEvent,
  type Session,
} from './api';
import { OwnerInstructionPanel } from './owner-instruction';

function Source({
  kind,
  reference,
  excerpt,
  observedAt,
}: {
  kind: string;
  reference?: string;
  excerpt: string;
  observedAt: string;
}) {
  let safeLink: string | null = null;
  try {
    const url = new URL(reference || '');
    if ((url.protocol === 'http:' || url.protocol === 'https:') && !url.username && !url.password)
      safeLink = url.href;
  } catch {
    /* A source reference may be an internal identifier. */
  }
  return (
    <details>
      <summary>
        Recorded source · {kind || 'kind unknown'} ·{' '}
        {observedAt ? new Date(observedAt).toLocaleDateString() : 'date unknown'}
      </summary>
      <p>{excerpt || 'No source excerpt recorded.'}</p>
      {safeLink && (
        <a href={safeLink} target="_blank" rel="noopener noreferrer">
          Open source
        </a>
      )}
      {!safeLink && reference && <p className="hint">Reference: {reference}</p>}
    </details>
  );
}

type Correction = { id: string; revision: number; title: string };
export function RelationshipsPanel({
  opportunityId,
  session,
  onSessionLost,
}: {
  opportunityId?: string;
  session: Session;
  onSessionLost: () => void;
}) {
  const [counterparties, setCounterparties] = useState<RelationshipCounterparty[]>([]);
  const [events, setEvents] = useState<RelationshipEvent[]>([]);
  const [routes, setRoutes] = useState<OpportunityRoute[]>([]);
  const [counterpartiesAvailable, setCounterpartiesAvailable] = useState(false);
  const [eventsAvailable, setEventsAvailable] = useState(false);
  const [routesAvailable, setRoutesAvailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [correction, setCorrection] = useState<Correction | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setError(null);
    void Promise.allSettled([
      listRelationshipCounterparties(controller.signal),
      listRelationshipEvents(controller.signal),
      opportunityId
        ? listOpportunityRoutes(opportunityId, controller.signal)
        : Promise.resolve([] as OpportunityRoute[]),
    ]).then(([people, activity, paths]) => {
      if (controller.signal.aborted) return;
      if (people.status === 'fulfilled') {
        setCounterparties(people.value);
        setCounterpartiesAvailable(true);
      }
      if (activity.status === 'fulfilled') {
        setEvents(activity.value);
        setEventsAvailable(true);
      }
      if (paths.status === 'fulfilled') {
        setRoutes(paths.value);
        setRoutesAvailable(true);
      }
      const failed = [people, activity, paths].find((result) => result.status === 'rejected');
      if (failed?.status === 'rejected') {
        if (isUnauthenticated(failed.reason)) onSessionLost();
        else setError('Some relationship records are unavailable. Refresh to try again.');
      }
      setLoading(false);
    });
    return () => controller.abort();
  }, [opportunityId, refresh, onSessionLost]);
  const people = new Map(counterparties.map((person) => [person.id, person]));
  const relatedEvents = events.filter((event) =>
    opportunityId
      ? event.opportunityId === opportunityId
      : !event.opportunityId && event.kind === 'introduction',
  );
  return (
    <section
      className="op-card"
      aria-label={opportunityId ? 'Opportunity routes and introductions' : 'Introductions'}
    >
      <div className="op-heading-row">
        <h2>{opportunityId ? 'Routes and introductions' : 'Introductions without a role'}</h2>
        <button
          type="button"
          className="secondary"
          disabled={loading}
          onClick={() => setRefresh((old) => old + 1)}
        >
          Refresh relationships
        </button>
      </div>
      <p className="hint">
        These are sourced private records. A contact or referral route does not establish a
        qualified role, permission to send, or a referral advantage.
      </p>
      {loading && <p role="status">Reading sourced relationships…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {opportunityId && (
        <>
          <h3>Recorded routes</h3>
          {!routesAvailable ? (
            <p>Routes are unavailable.</p>
          ) : routes.length === 0 ? (
            <p>No route is recorded for this opportunity.</p>
          ) : (
            <ul className="op-list">
              {routes.map((route) => (
                <li key={route.id} className="op-card">
                  <h4>
                    {route.kind === 'direct'
                      ? 'Direct route'
                      : route.kind === 'referral'
                        ? 'Referral route'
                        : 'Recruiter route'}
                  </h4>
                  {route.destinationText && <p>Recorded destination: {route.destinationText}</p>}
                  {route.counterpartyId && (
                    <p>
                      Linked counterparty:{' '}
                      {people.get(route.counterpartyId)?.displayName || 'record unavailable'}
                    </p>
                  )}
                  {route.eventId && (
                    <p>
                      Linked event:{' '}
                      {events.find((event) => event.id === route.eventId)?.summary ||
                        'record unavailable'}
                    </p>
                  )}
                  <Source
                    kind={route.sourceKind}
                    reference={route.sourceRef}
                    excerpt={route.sourceExcerpt}
                    observedAt={route.observedAt}
                  />
                  <button
                    type="button"
                    className="secondary"
                    onClick={() =>
                      setCorrection({
                        id: route.id,
                        revision: route.revision,
                        title: 'Instruction about this recorded route',
                      })
                    }
                  >
                    Correct route context
                  </button>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      <h3>{opportunityId ? 'Related events' : 'Pre-vacancy introductions'}</h3>
      {!eventsAvailable ? (
        <p>Relationship events are unavailable.</p>
      ) : relatedEvents.length === 0 ? (
        <p>
          {opportunityId
            ? 'No introduction or conversation is linked to this role.'
            : 'No introduction without a role is recorded.'}
        </p>
      ) : (
        <ul className="op-list">
          {relatedEvents.map((event) => (
            <li key={event.id} className="op-card">
              <h4>{event.kind.replaceAll('_', ' ')}</h4>
              <p>{event.summary}</p>
              {event.counterpartyId && (
                <p>
                  Counterparty:{' '}
                  {people.get(event.counterpartyId)?.displayName || 'record unavailable'}
                </p>
              )}
              {!opportunityId && (
                <p className="hint">No opportunity is linked; this is not a qualified opening.</p>
              )}
              <Source
                kind={event.sourceKind}
                reference={event.sourceRef}
                excerpt={event.sourceExcerpt}
                observedAt={event.observedAt}
              />
              <button
                type="button"
                className="secondary"
                onClick={() =>
                  setCorrection({
                    id: event.id,
                    revision: event.revision,
                    title: 'Instruction about this recorded event',
                  })
                }
              >
                Correct event context
              </button>
            </li>
          ))}
        </ul>
      )}
      {!counterpartiesAvailable && <p className="hint">Counterparty names are unavailable.</p>}
      {correction && (
        <OwnerInstructionPanel
          key={correction.id}
          target={{
            targetKind: 'relationship',
            targetId: correction.id,
            expectedRevision: correction.revision,
          }}
          title={correction.title}
          session={session}
          onSessionLost={onSessionLost}
          onClose={() => setCorrection(null)}
          closeLabel="Close correction"
        />
      )}
    </section>
  );
}
