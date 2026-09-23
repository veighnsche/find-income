import { useEffect, useState } from 'react';
import {
  addOwnerInstruction,
  getActiveRound,
  isUnauthenticated,
  listOwnerInstructions,
  RequestError,
  revokeOwnerInstruction,
  type OwnerInstruction,
  type OwnerInstructionInput,
  type Session,
} from './api';

type Target = Pick<OwnerInstructionInput, 'targetKind' | 'targetId' | 'expectedRevision'>;
function draftStoreKey(target: Target) {
  return `jobseek.owner-instruction.${target.targetKind}.${target.targetId}`;
}

export function OwnerInstructionPanel({
  target,
  title,
  session,
  onSessionLost,
  onClose,
}: {
  target: Target;
  title: string;
  session: Session;
  onSessionLost: () => void;
  onClose?: () => void;
}) {
  const [text, setText] = useState('');
  const [pending, setPending] = useState<OwnerInstructionInput | null>(null);
  const [items, setItems] = useState<OwnerInstruction[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const key = draftStoreKey(target);
  useEffect(() => {
    try {
      const saved = JSON.parse(localStorage.getItem(key) || 'null') as {
        text?: string;
        pending?: OwnerInstructionInput;
      } | null;
      setText(saved?.text || '');
      setPending(saved?.pending || null);
    } catch {
      setText('');
      setPending(null);
    }
    const controller = new AbortController();
    listOwnerInstructions('', controller.signal)
      .then((all) =>
        setItems(
          all.filter(
            (item) => item.targetKind === target.targetKind && item.targetId === target.targetId,
          ),
        ),
      )
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError('Saved instructions are unavailable.');
      });
    return () => controller.abort();
  }, [key, target.targetKind, target.targetId, onSessionLost]);
  function saveDraft(value: string, input: OwnerInstructionInput | null) {
    setText(value);
    setPending(input);
    try {
      if (value) localStorage.setItem(key, JSON.stringify({ text: value, pending: input }));
      else localStorage.removeItem(key);
    } catch {
      /* Draft remains in this page. */
    }
  }
  async function submit() {
    if (busy || !text.trim()) return;
    if (pending && pending.expectedRevision !== target.expectedRevision) {
      setError(
        'This record changed since the instruction was prepared. Review it and revise the instruction before saving.',
      );
      return;
    }
    setBusy(true);
    setError(null);
    setSuccess(null);
    try {
      let input = pending;
      if (!input) {
        const round = await getActiveRound();
        input = {
          ...target,
          requestKey: crypto.randomUUID(),
          text: text.trim(),
          ...(round && ['running', 'awaiting_input', 'paused'].includes(round.state)
            ? { roundId: round.id }
            : {}),
        };
        saveDraft(text, input);
      }
      const saved = await addOwnerInstruction(input, session.csrfToken);
      setItems((old) => [saved, ...old.filter((item) => item.id !== saved.id)]);
      saveDraft('', null);
      setSuccess(
        saved.roundId
          ? 'Instruction saved for this round. Inspect round results for any resulting changes.'
          : 'Instruction saved as owner context for a future round.',
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409)
        setError(
          'The record revision changed. Refresh and review it before writing a new instruction.',
        );
      else setError('Save was not confirmed. Check saved instructions or retry the same request.');
    } finally {
      setBusy(false);
    }
  }
  async function check() {
    setBusy(true);
    try {
      const all = await listOwnerInstructions();
      const scoped = all.filter(
        (item) => item.targetKind === target.targetKind && item.targetId === target.targetId,
      );
      setItems(scoped);
      if (pending && scoped.some((item) => item.requestKey === pending.requestKey)) {
        saveDraft('', null);
        setSuccess('Instruction is saved. Inspect round results for any resulting changes.');
        setError(null);
      }
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError('Could not check saved instructions.');
    } finally {
      setBusy(false);
    }
  }
  async function revoke(item: OwnerInstruction) {
    setBusy(true);
    try {
      await revokeOwnerInstruction(item.id, session.csrfToken);
      setItems((old) =>
        old.map((value) =>
          value.id === item.id ? { ...value, revokedAt: new Date().toISOString() } : value,
        ),
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError('Could not revoke this instruction. Refresh and try again.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="op-card" aria-label="Owner instruction">
      <h2>{title}</h2>
      <p className="hint">
        Tell the agency what to correct or consider. One instruction can include several facts.
        Saving gives the agency context; it does not apply a change immediately.
      </p>
      <label htmlFor={`instruction-${target.targetKind}`}>Your instruction</label>
      <textarea
        id={`instruction-${target.targetKind}`}
        rows={6}
        value={text}
        disabled={busy}
        onChange={(event) => {
          saveDraft(event.target.value, null);
          setError(null);
          setSuccess(null);
        }}
        placeholder="Describe the correction, context, or question in your own words"
      />
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {success && (
        <p role="status" className="success">
          {success}
        </p>
      )}
      <div className="button-row">
        <button type="button" disabled={busy || !text.trim()} onClick={() => void submit()}>
          {busy ? 'Saving…' : 'Save instruction'}
        </button>
        {pending && (
          <button type="button" className="secondary" disabled={busy} onClick={() => void check()}>
            Check saved instructions
          </button>
        )}
        {onClose && (
          <button type="button" className="secondary" onClick={onClose}>
            Return to source intake
          </button>
        )}
      </div>
      <details>
        <summary>Saved instructions ({items.length})</summary>
        {items.length ? (
          <ul>
            {items.map((item) => (
              <li key={item.id}>
                <p>{item.text}</p>
                <p className="hint">
                  {item.revokedAt
                    ? 'Revoked'
                    : item.roundId
                      ? 'Saved for a round'
                      : 'Saved for a future round'}{' '}
                  · {new Date(item.createdAt).toLocaleString()}
                </p>
                {!item.revokedAt && (
                  <button
                    type="button"
                    className="secondary"
                    disabled={busy}
                    onClick={() => void revoke(item)}
                  >
                    Revoke
                  </button>
                )}
              </li>
            ))}
          </ul>
        ) : (
          <p>No saved instructions for this context.</p>
        )}
      </details>
    </section>
  );
}
