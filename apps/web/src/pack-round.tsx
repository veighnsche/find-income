import { useCallback, useEffect, useRef, useState } from 'react';
import {
  getActiveRound,
  getRound,
  isUnauthenticated,
  RequestError,
  resumeRound,
  stopRound,
  type Round,
  type Session,
} from './api';

const runningStates = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
const occupiedStates = new Set<Round['state']>([
  'queued',
  'running',
  'awaiting_input',
  'stopping',
  'paused',
]);
const pollMs = 5000;
function roundKey(id: string) {
  return `jobseek.prepare-round.${id}`;
}
function requestKey(id: string) {
  return `jobseek.prepare-request.${id}`;
}
function errorText(cause: unknown) {
  return cause instanceof Error ? cause.message : 'Request failed.';
}

export function PackRoundPanel({
  opportunityId,
  selected,
  sourceReady,
  hasSavedPack,
  session,
  startPreparation,
  onRoundSettled,
  onSessionLost,
}: {
  opportunityId: string;
  selected: boolean;
  sourceReady: boolean;
  hasSavedPack: boolean;
  session: Session;
  startPreparation: (requestKey: string) => Promise<Round>;
  onRoundSettled: () => void;
  onSessionLost: () => void;
}) {
  const [round, setRound] = useState<Round | null>(null);
  const [otherActive, setOtherActive] = useState<Round | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const controller = useRef<AbortController | null>(null);
  const readVersion = useRef(0);
  const readPromise = useRef<Promise<void> | null>(null);
  const mounted = useRef(false);
  const mutating = useRef(false);
  const lastSettled = useRef('');
  const onSettledRef = useRef(onRoundSettled);
  onSettledRef.current = onRoundSettled;

  const invalidate = useCallback(() => {
    readVersion.current++;
    controller.current?.abort();
    controller.current = null;
    readPromise.current = null;
  }, []);

  const refresh = useCallback(
    (silent = false): Promise<void> => {
      if (!mounted.current || mutating.current) return Promise.resolve();
      if (readPromise.current) return readPromise.current;
      const current = new AbortController();
      const version = ++readVersion.current;
      controller.current = current;
      const fresh = () =>
        mounted.current && !current.signal.aborted && version === readVersion.current;
      if (!silent) {
        setLoading(true);
        setError(null);
      }
      const task = (async () => {
        try {
          const active = await getActiveRound(current.signal);
          const own =
            active?.outcome === 'prepare' &&
            active.scope.resources.includes(`opportunity:${opportunityId}`)
              ? active
              : null;
          let remembered = own;
          if (!remembered) {
            const id = localStorage.getItem(roundKey(opportunityId));
            if (id) {
              try {
                remembered = await getRound(id, current.signal);
              } catch (cause) {
                if (!(cause instanceof RequestError && cause.status === 404)) throw cause;
              }
            }
          }
          if (!fresh()) return;
          setRound(remembered);
          setOtherActive(active && !own ? active : null);
          setError(null);
          if (remembered && !occupiedStates.has(remembered.state)) {
            const identity = `${remembered.id}:${remembered.revision}`;
            if (lastSettled.current !== identity) {
              lastSettled.current = identity;
              onSettledRef.current();
            }
          }
        } catch (cause) {
          if (!fresh()) return;
          if (isUnauthenticated(cause)) onSessionLost();
          else
            setError(`${errorText(cause)} Saved pack versions remain below; refresh to try again.`);
        } finally {
          if (fresh()) setLoading(false);
        }
      })();
      readPromise.current = task;
      void task.finally(() => {
        if (readPromise.current === task) readPromise.current = null;
        if (controller.current === current) controller.current = null;
      });
      return task;
    },
    [opportunityId, onSessionLost],
  );

  useEffect(() => {
    mounted.current = true;
    void refresh();
    return () => {
      mounted.current = false;
      invalidate();
    };
  }, [refresh, invalidate]);
  useEffect(() => {
    if (
      !(round && runningStates.has(round.state)) &&
      !(otherActive && occupiedStates.has(otherActive.state))
    )
      return;
    let cancelled = false;
    let timer: number;
    const poll = async () => {
      await refresh(true);
      if (!cancelled) timer = window.setTimeout(() => void poll(), pollMs);
    };
    timer = window.setTimeout(() => void poll(), pollMs);
    return () => {
      cancelled = true;
      window.clearTimeout(timer);
    };
  }, [round?.id, round?.state, otherActive?.id, otherActive?.state, refresh]);

  async function prepare() {
    if (
      !selected ||
      !sourceReady ||
      loading ||
      mutating.current ||
      otherActive ||
      (round && occupiedStates.has(round.state))
    )
      return;
    mutating.current = true;
    invalidate();
    setBusy(true);
    setActionError(null);
    try {
      let key = localStorage.getItem(requestKey(opportunityId));
      if (!key) {
        key = crypto.randomUUID();
        localStorage.setItem(requestKey(opportunityId), key);
      }
      const created = await startPreparation(key);
      if (mounted.current) setRound(created);
      localStorage.setItem(roundKey(opportunityId), created.id);
      localStorage.removeItem(requestKey(opportunityId));
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (mounted.current)
        setActionError(
          `${errorText(cause)} Refresh status before retrying; this Prepare identity is retained.`,
        );
    } finally {
      mutating.current = false;
      if (mounted.current) {
        setBusy(false);
        void refresh(true);
      }
    }
  }

  async function act(operation: 'stop' | 'resume') {
    if (!round || mutating.current) return;
    mutating.current = true;
    invalidate();
    setBusy(true);
    setActionError(null);
    try {
      const changed =
        operation === 'stop'
          ? await stopRound(round.id, session.csrfToken)
          : await resumeRound(round.id, session.csrfToken);
      if (mounted.current) setRound(changed);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (mounted.current) setActionError(errorText(cause));
    } finally {
      mutating.current = false;
      if (mounted.current) {
        setBusy(false);
        void refresh(true);
      }
    }
  }

  const active = round && runningStates.has(round.state);
  const paused = round?.state === 'paused';
  return (
    <section className="op-card" aria-label="Application preparation">
      <div className="op-heading-row">
        <h2>Prepare this application</h2>
        <button
          className="secondary"
          type="button"
          disabled={loading || busy}
          onClick={() => void refresh()}
        >
          Refresh preparation
        </button>
      </div>
      <p>
        A preparation round researches this role and drafts a private review pack. It does not
        approve or send an application.
      </p>
      {loading && <p role="status">Reading preparation status…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {otherActive && (
        <p role="status">
          Another {otherActive.outcome.replaceAll('_', ' ')} round is{' '}
          {otherActive.state.replaceAll('_', ' ')}. Finish or stop it from your campaign before
          preparing this role.
        </p>
      )}
      {round && (
        <>
          <p>
            <strong>{round.state.replaceAll('_', ' ')}</strong> ·{' '}
            {round.step || 'Step not reported'} · Deliverable:{' '}
            {round.deliverableStatus || 'not reported'}
          </p>
          {typeof round.report.summary === 'string' && <p>{round.report.summary}</p>}
          {round.unresolved.length > 0 && (
            <p>
              {round.unresolved.length} unresolved item{round.unresolved.length === 1 ? '' : 's'} in
              the round report.
            </p>
          )}
          {round.reconciliationRequired && (
            <p role="status">Earlier work needs reconciliation before more preparation can run.</p>
          )}
          <details>
            <summary>Scope and remaining allowance</summary>
            <p>
              Deadline: {new Date(round.deadline).toLocaleString()}. Remaining:{' '}
              {Math.max(0, round.limits.requests - round.used.requests)} requests,{' '}
              {Math.max(0, round.limits.items - round.used.items)} items,{' '}
              {Math.max(0, round.limits.tools - round.used.tools)} tools,{' '}
              {Math.max(0, round.limits.turns - round.used.turns)} turns.
            </p>
          </details>
          <div className="button-row">
            {active && (
              <button
                type="button"
                disabled={busy || round.state === 'stopping'}
                onClick={() => void act('stop')}
              >
                Stop preparation
              </button>
            )}
            {paused && (
              <button type="button" disabled={busy} onClick={() => void act('resume')}>
                {round.reconciliationRequired
                  ? 'Resume and check uncertain work'
                  : 'Resume preparation'}
              </button>
            )}
          </div>
        </>
      )}
      {!selected && <p className="hint">Select this opportunity to commission a pack.</p>}
      {selected && !sourceReady && (
        <p className="hint">A saved source URL and vacancy text are needed before preparation.</p>
      )}
      {selected && sourceReady && !otherActive && !active && !paused && (
        <button
          type="button"
          disabled={busy || loading || Boolean(error)}
          onClick={() => void prepare()}
        >
          {hasSavedPack || round ? 'Prepare a new version' : 'Prepare application'}
        </button>
      )}
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
    </section>
  );
}
