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
  type PrepareRoundRequest,
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
function readPendingPrepare(id: string): PrepareRoundRequest | null {
  try {
    return JSON.parse(localStorage.getItem(requestKey(id)) || 'null') as PrepareRoundRequest | null;
  } catch {
    return null;
  }
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
  startPreparation: (input: PrepareRoundRequest) => Promise<Round>;
  onRoundSettled: () => void;
  onSessionLost: () => void;
}) {
  const [round, setRound] = useState<Round | null>(null);
  const [otherActive, setOtherActive] = useState<Round | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [stalePrepare, setStalePrepare] = useState(false);
  const [pendingPrepare, setPendingPrepare] = useState<PrepareRoundRequest | null>(() =>
    readPendingPrepare(opportunityId),
  );
  const controller = useRef<AbortController | null>(null);
  const readVersion = useRef(0);
  const readPromise = useRef<Promise<boolean> | null>(null);
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
    (silent = false): Promise<boolean> => {
      if (!mounted.current || mutating.current) return Promise.resolve(false);
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
          if (!fresh()) return false;
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
          return true;
        } catch (cause) {
          if (!fresh()) return false;
          if (isUnauthenticated(cause)) onSessionLost();
          else
            setError(`${errorText(cause)} Saved pack versions remain below; refresh to try again.`);
          return false;
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
    const pausedRound =
      otherActive?.state === 'paused' ? otherActive : round?.state === 'paused' ? round : null;
    const replacePaused = pausedRound
      ? { roundId: pausedRound.id, expectedRevision: pausedRound.revision }
      : undefined;
    if (
      mutating.current ||
      (!pendingPrepare &&
        (!selected ||
          !sourceReady ||
          loading ||
          (otherActive && otherActive.state !== 'paused') ||
          (round && runningStates.has(round.state))))
    )
      return;
    mutating.current = true;
    invalidate();
    setBusy(true);
    setActionError(null);
    setStalePrepare(false);
    try {
      let input = pendingPrepare;
      if (!input) {
        input = {
          requestKey: crypto.randomUUID(),
          opportunityId,
          ...(replacePaused ? { replacePaused } : {}),
        };
        localStorage.setItem(requestKey(opportunityId), JSON.stringify(input));
        setPendingPrepare(input);
      }
      const created = await startPreparation(input);
      if (mounted.current) setRound(created);
      localStorage.setItem(roundKey(opportunityId), created.id);
      localStorage.removeItem(requestKey(opportunityId));
      setPendingPrepare(null);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409) {
        setStalePrepare(true);
        setActionError(
          'This Prepare request was rejected because the paused work changed. Review current work before starting a new request.',
        );
      } else if (mounted.current)
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

  async function reviewRejectedPrepare() {
    if (!pendingPrepare || !stalePrepare || busy) return;
    if (!(await refresh())) {
      setActionError('Current work could not be refreshed. The rejected request remains saved.');
      return;
    }
    localStorage.removeItem(requestKey(opportunityId));
    setPendingPrepare(null);
    setStalePrepare(false);
    setActionError(
      'Current work was refreshed. Review it before starting a new preparation request.',
    );
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
          {otherActive.state.replaceAll('_', ' ')}.{' '}
          {otherActive.state === 'paused'
            ? 'You can end it and start this preparation with its history retained.'
            : 'Finish or stop it from your campaign before preparing this role.'}
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
      {selected &&
        sourceReady &&
        !pendingPrepare &&
        (!otherActive || otherActive.state === 'paused') &&
        !active && (
          <button
            type="button"
            disabled={busy || loading || Boolean(error)}
            onClick={() => void prepare()}
          >
            {otherActive?.state === 'paused' || paused
              ? `End paused ${(otherActive || round)!.outcome.replaceAll('_', ' ')} round and prepare this application`
              : hasSavedPack || round
                ? 'Prepare a new version'
                : 'Prepare application'}
          </button>
        )}
      {pendingPrepare && (
        <div className="button-row">
          <p>
            The earlier Prepare response was not confirmed. Retry its saved request to check the
            same commission.
          </p>
          <button type="button" disabled={busy} onClick={() => void prepare()}>
            Retry same preparation request
          </button>
          {stalePrepare && (
            <button
              type="button"
              className="secondary"
              disabled={busy || loading}
              onClick={() => void reviewRejectedPrepare()}
            >
              Review work and start a new preparation request
            </button>
          )}
        </div>
      )}
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
    </section>
  );
}
