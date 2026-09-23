import { useEffect, useState } from 'react';
import {
  getOwnerOpportunityDecision,
  isUnauthenticated,
  RequestError,
  setOwnerOpportunityDecision,
  type OwnerDecision,
  type OwnerDecisionInput,
  type Session,
} from './api';

export function OwnerDecisionControls({
  id,
  revision,
  session,
  onSessionLost,
}: {
  id: string;
  revision: number;
  session: Session;
  onSessionLost: () => void;
}) {
  const [current, setCurrent] = useState<OwnerDecision | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState<OwnerDecisionInput | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    getOwnerOpportunityDecision(id, controller.signal)
      .then((decision) => {
        setCurrent(decision);
        setPending(null);
        setError(null);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(cause instanceof Error ? cause.message : 'Decision unavailable.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [id, onSessionLost]);
  async function choose(decision: OwnerDecisionInput['decision']) {
    if (busy || loading || (current?.decision === decision && !pending)) return;
    const input =
      pending?.decision === decision
        ? pending
        : {
            requestKey: crypto.randomUUID(),
            expectedOpportunityRevision: revision,
            expectedDecisionRevision: current?.revision ?? 0,
            decision,
          };
    setPending(input);
    setBusy(true);
    setError(null);
    try {
      const saved = await setOwnerOpportunityDecision(id, input, session.csrfToken);
      setCurrent(saved);
      setPending(null);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409)
        setError(
          'The opportunity or decision changed. Refresh the opportunity before choosing again.',
        );
      else
        setError('The save was not confirmed. Check the saved decision or retry the same choice.');
    } finally {
      setBusy(false);
    }
  }
  async function check() {
    setBusy(true);
    try {
      const latest = await getOwnerOpportunityDecision(id);
      setCurrent(latest);
      setPending(null);
      setError(null);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(cause instanceof Error ? cause.message : 'Decision unavailable.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <div className="owner-decision">
      <p className="hint">
        Owner decision: {loading ? 'loading…' : current?.decision || 'none recorded'}
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      <div className="button-row">
        {(['selected', 'dismissed', 'acknowledged'] as const).map((choice) => (
          <button
            key={choice}
            type="button"
            className="secondary"
            disabled={
              busy ||
              loading ||
              (current?.decision === choice && !pending) ||
              Boolean(pending && pending.decision !== choice)
            }
            onClick={() => void choose(choice)}
          >
            {choice === 'selected' ? 'Select' : choice === 'dismissed' ? 'Dismiss' : 'Acknowledge'}
          </button>
        ))}
        {pending && (
          <button type="button" className="secondary" disabled={busy} onClick={() => void check()}>
            Check saved decision
          </button>
        )}
      </div>
    </div>
  );
}
