import { useEffect, useRef, useState } from 'react';
import { ProcessInputReport } from './process-input-report';
import {
  getActiveRound,
  getRound,
  isUnauthenticated,
  processInput,
  RequestError,
  resumeRound,
  stopRound,
  type ProcessInputRequest,
  type Round,
  type Session,
} from './api';

type Target = Pick<ProcessInputRequest, 'targetKind' | 'targetId' | 'expectedRevision'>;
type Draft = { text: string; pending: ProcessInputRequest | null };
const working = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
function draftKey(target: Target) {
  return `jobseek.process-input.${target.targetKind}.${target.targetId}`;
}
function readDraft(key: string): Draft {
  try {
    const value = JSON.parse(localStorage.getItem(key) || 'null') as Draft | null;
    if (value && typeof value.text === 'string') return value;
  } catch {
    /* Browser storage is optional. */
  }
  return { text: '', pending: null };
}
async function currentRound(signal?: AbortSignal, visibleId?: string): Promise<Round | null> {
  const active = await getActiveRound(signal);
  if (active) return active;
  let id = visibleId;
  if (!id) {
    try {
      id = localStorage.getItem('jobseek.last-round') || undefined;
    } catch {
      /* No saved round. */
    }
  }
  if (!id) return null;
  try {
    return await getRound(id, signal);
  } catch (cause) {
    if (cause instanceof RequestError && cause.status === 404) return null;
    throw cause;
  }
}
function material(
  text: string,
  kind: Target['targetKind'],
): Pick<ProcessInputRequest, 'text' | 'sourceUrl' | 'originalText'> {
  const trimmed = text.trim();
  if (kind === 'campaign' && /^https?:\/\//i.test(trimmed) && !/\s/.test(trimmed)) {
    const url = new URL(trimmed);
    if (!['http:', 'https:'].includes(url.protocol) || url.username || url.password)
      throw new Error('Enter a valid source URL or paste the full material.');
    return { sourceUrl: trimmed };
  }
  return kind === 'campaign' ? { originalText: text } : { text: trimmed };
}
function actionLabel(kind: Target['targetKind']): string {
  if (kind === 'campaign') return 'Handle this material';
  if (kind === 'profile') return 'Update my brief';
  if (kind === 'opportunity') return 'Update this opportunity';
  if (kind === 'application_pack') return 'Correct this pack in a new version';
  if (kind === 'relationship') return 'Update this relationship';
  return 'Update this evidence';
}

export function OwnerInstructionPanel({
  target,
  title,
  session,
  onSessionLost,
  onRoundSettled,
  onClose,
  closeLabel = 'Close correction',
}: {
  target: Target;
  title: string;
  session: Session;
  onSessionLost: () => void;
  onRoundSettled?: (round: Round) => void;
  onClose?: () => void;
  closeLabel?: string;
}) {
  const key = draftKey(target);
  const [draft, setDraft] = useState<Draft>(() => readDraft(key));
  const [round, setRound] = useState<Round | null>(null);
  const [roundLoading, setRoundLoading] = useState(true);
  const [roundError, setRoundError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [accepted, setAccepted] = useState(false);
  const [reviseAllowed, setReviseAllowed] = useState(false);
  const lastSettled = useRef('');
  const onSettledRef = useRef(onRoundSettled);
  onSettledRef.current = onRoundSettled;
  useEffect(() => {
    if (!round || !['completed', 'failed'].includes(round.state)) return;
    const identity = `${round.id}:${round.revision}`;
    if (lastSettled.current === identity) return;
    lastSettled.current = identity;
    onSettledRef.current?.(round);
  }, [round]);
  useEffect(() => {
    setDraft(readDraft(key));
    setAccepted(false);
    setReviseAllowed(false);
    setError(null);
    setRoundLoading(true);
    const controller = new AbortController();
    currentRound(controller.signal)
      .then(setRound)
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setRoundError('Current agency work is unavailable. Refresh before commissioning.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setRoundLoading(false);
      });
    return () => controller.abort();
  }, [key, onSessionLost]);
  useEffect(() => {
    if (!round || !working.has(round.state)) return;
    const timer = window.setInterval(() => {
      void getRound(round.id)
        .then(setRound)
        .catch((cause: unknown) => {
          if (isUnauthenticated(cause)) onSessionLost();
          else setRoundError('Round status is unavailable. Refresh to retry.');
        });
    }, 5000);
    return () => window.clearInterval(timer);
  }, [round?.id, round?.state, onSessionLost]);
  function save(next: Draft) {
    setDraft(next);
    try {
      if (next.text) localStorage.setItem(key, JSON.stringify(next));
      else localStorage.removeItem(key);
    } catch {
      /* Keep the in-page draft. */
    }
  }
  async function refreshRound() {
    setRoundError(null);
    setRoundLoading(true);
    try {
      setRound(await currentRound(undefined, round?.id));
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setRoundError('Current agency work is unavailable. Refresh before commissioning.');
    } finally {
      setRoundLoading(false);
    }
  }
  async function submit() {
    if (busy || roundLoading || !draft.text.trim() || (roundError && !draft.pending)) return;
    if (!draft.pending && round && working.has(round.state)) {
      setError('Agency work is already running. Stop it before commissioning different work.');
      return;
    }
    const replacement =
      round?.state === 'paused'
        ? { roundId: round.id, expectedRevision: round.revision }
        : undefined;
    setBusy(true);
    setError(null);
    setReviseAllowed(false);
    try {
      let input = draft.pending;
      if (!input) {
        input = {
          ...target,
          requestKey: crypto.randomUUID(),
          ...material(draft.text, target.targetKind),
          ...(replacement ? { replacePaused: replacement } : {}),
        };
        save({ ...draft, pending: input });
      }
      const response = await processInput(input, session.csrfToken);
      setRound(response.round);
      setAccepted(true);
      save({ text: '', pending: null });
      try {
        localStorage.setItem('jobseek.last-round', response.round.id);
      } catch {
        /* In-page status remains. */
      }
      window.dispatchEvent(new Event('jobseek:round-commissioned'));
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409) {
        setReviseAllowed(true);
        setError(
          'The target or paused round changed. Refresh the record and review before a new request.',
        );
      } else if (cause instanceof RequestError && cause.status >= 400 && cause.status < 500) {
        setReviseAllowed(true);
        setError(`${cause.message} Review the material and revise it before submitting again.`);
      } else
        setError(
          'The response was not confirmed. Your text and request identity are saved; retry the same request.',
        );
    } finally {
      setBusy(false);
    }
  }
  async function control(operation: 'stop' | 'resume') {
    if (!round || busy) return;
    setBusy(true);
    setRoundError(null);
    try {
      setRound(
        operation === 'stop'
          ? await stopRound(round.id, session.csrfToken)
          : await resumeRound(round.id, session.csrfToken),
      );
      window.dispatchEvent(new Event('jobseek:round-commissioned'));
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setRoundError('Round action was not confirmed. Refresh its status before retrying.');
    } finally {
      setBusy(false);
    }
  }
  const label = actionLabel(target.targetKind);
  const replace = round?.state === 'paused';
  return (
    <section className="op-card" aria-label="Owner instruction">
      <h2>{title}</h2>
      <p className="hint">
        Tell the agency what to handle here. It will report changed records or unresolved facts in a
        finite round.
      </p>
      <label htmlFor={`instruction-${target.targetKind}-${target.targetId}`}>
        {target.targetKind === 'campaign'
          ? 'Job URL or full material'
          : 'Your correction or context'}
      </label>
      <textarea
        id={`instruction-${target.targetKind}-${target.targetId}`}
        rows={6}
        value={draft.text}
        disabled={busy || Boolean(draft.pending)}
        onChange={(event) => {
          save({ text: event.target.value, pending: null });
          setError(null);
          setAccepted(false);
        }}
        placeholder={
          target.targetKind === 'campaign'
            ? 'Paste a job URL or the complete vacancy text'
            : 'Describe the correction in your own words'
        }
      />
      {draft.pending && (
        <p className="hint">
          This submitted text and request identity are held for the same-request retry. Check work
          status or retry before editing a new request.
        </p>
      )}
      {roundError && (
        <p role="alert" className="error">
          {roundError}
        </p>
      )}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {round && (
        <div role="status">
          <p>
            Agency work: {round.outcome.replaceAll('_', ' ')} · {round.state.replaceAll('_', ' ')}
          </p>
          <ProcessInputReport round={round} />
          {round.unresolved.length > 0 && (
            <p>
              Execution reconciliation has {round.unresolved.length} unresolved attempt
              {round.unresolved.length === 1 ? '' : 's'}.
            </p>
          )}
          {accepted && (
            <p>
              {round.state === 'completed' || round.state === 'failed'
                ? 'Round finished. Review its saved changes and unresolved facts above.'
                : 'Request accepted. The agency is handling this input.'}
            </p>
          )}
        </div>
      )}
      {replace && (
        <p className="hint">
          The paused {round.outcome.replaceAll('_', ' ')} round will end with its saved history and
          uncertainty. This starts new work: {label.toLowerCase()}.
        </p>
      )}
      {round && working.has(round.state) && !draft.pending && (
        <p className="hint">
          Current work must finish or be stopped before starting different work. Your draft remains
          here.
        </p>
      )}
      <div className="button-row">
        <button
          type="button"
          disabled={
            busy ||
            roundLoading ||
            !draft.text.trim() ||
            Boolean(roundError && !draft.pending) ||
            Boolean(!draft.pending && round && working.has(round.state))
          }
          onClick={() => void submit()}
        >
          {busy
            ? 'Working…'
            : draft.pending
              ? 'Retry same request'
              : replace
                ? `End paused work and ${label.toLowerCase()}`
                : label}
        </button>
        {round && working.has(round.state) && round.state !== 'stopping' && (
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={() => void control('stop')}
          >
            Stop current work
          </button>
        )}
        {round?.state === 'paused' && (
          <button
            type="button"
            className="secondary"
            disabled={busy}
            onClick={() => void control('resume')}
          >
            Resume paused work
          </button>
        )}
        {draft.pending && reviseAllowed && (
          <button
            type="button"
            className="secondary"
            disabled={busy || roundLoading}
            onClick={() => {
              save({ ...draft, pending: null });
              setError(null);
            }}
          >
            Revise after reviewing work status
          </button>
        )}
        <button
          type="button"
          className="secondary"
          disabled={busy}
          onClick={() => void refreshRound()}
        >
          Refresh work status
        </button>
        {onClose && (
          <button type="button" className="secondary" onClick={onClose}>
            {closeLabel}
          </button>
        )}
      </div>
    </section>
  );
}
