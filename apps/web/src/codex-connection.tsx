import { useEffect, useState } from 'react';
import {
  cancelCodexConnect,
  connectCodex,
  getCodexStatus,
  isUnauthenticated,
  type CodexConnection,
  type CodexStatus,
  type Session,
} from './api';

export function CodexConnectionPanel({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [status, setStatus] = useState<CodexStatus | null>(null);
  const [connection, setConnection] = useState<CodexConnection | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function refresh(signal?: AbortSignal) {
    setError(null);
    try {
      const next = await getCodexStatus(signal);
      setStatus(next);
      if (next.state === 'ready') setConnection(null);
    } catch (cause) {
      if (signal?.aborted) return;
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(cause instanceof Error ? cause.message : 'Could not check Codex connection.');
    }
  }
  useEffect(() => {
    const controller = new AbortController();
    void refresh(controller.signal);
    return () => controller.abort();
  }, [onSessionLost]);
  async function connect() {
    setBusy(true);
    setError(null);
    try {
      setConnection(await connectCodex(session.csrfToken));
      await refresh();
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(cause instanceof Error ? cause.message : 'Could not start Codex sign-in.');
    } finally {
      setBusy(false);
    }
  }
  async function cancel() {
    setBusy(true);
    setError(null);
    try {
      await cancelCodexConnect(session.csrfToken);
      setConnection(null);
      await refresh();
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(cause instanceof Error ? cause.message : 'Could not cancel sign-in.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="status">
      <h2>Codex processing</h2>
      <p>
        Codex reads queued vacancies and saves extracted opportunities when its isolated runner and
        scoped tools are ready.
      </p>
      <p role="status">
        {status
          ? `Connection: ${status.state.replaceAll('_', ' ')} · ${status.code.replaceAll('_', ' ')}. Ingestion worker: ${status.ingestionAvailable ? 'ready' : 'unavailable'}.`
          : 'Checking Codex connection…'}
      </p>
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {connection && (
        <div role="status">
          <p>Open the official sign-in page and enter this one-time code:</p>
          <p>
            <a href={connection.verificationUrl} target="_blank" rel="noopener noreferrer">
              Open Codex sign-in
            </a>{' '}
            · <strong>{connection.userCode}</strong>
          </p>
        </div>
      )}
      <div className="button-row">
        <button className="secondary" type="button" disabled={busy} onClick={() => void refresh()}>
          Refresh connection
        </button>
        {status?.state === 'needs_sign_in' && (
          <button type="button" disabled={busy} onClick={() => void connect()}>
            Connect Codex
          </button>
        )}
        {(connection || status?.state === 'connecting') && (
          <button className="secondary" type="button" disabled={busy} onClick={() => void cancel()}>
            Cancel sign-in
          </button>
        )}
      </div>
    </section>
  );
}
