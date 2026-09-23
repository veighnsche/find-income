import { useEffect, useState, type FormEvent } from 'react';
import {
  createCollectorBoard,
  getRuntimeStatus,
  isUnauthenticated,
  listCollectorBoards,
  RequestError,
  updateCollectorBoard,
  type CollectorBoard,
  type RuntimeStatus,
  type Session,
} from './api';

function BoardRow({
  board,
  session,
  onSessionLost,
  onUpdated,
  onRefresh,
}: {
  board: CollectorBoard;
  session: Session;
  onSessionLost: () => void;
  onUpdated: (value: CollectorBoard) => void;
  onRefresh: () => void;
}) {
  const [enabled, setEnabled] = useState(board.enabled);
  const [interval, setInterval] = useState(String(board.intervalMinutes));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const minutes = Number(interval);
    if (!Number.isInteger(minutes) || minutes < 15 || minutes > 10080) {
      setError('Choose an interval from 15 minutes to 7 days.');
      return;
    }
    setBusy(true);
    setError(null);
    setSuccess(null);
    try {
      const saved = await updateCollectorBoard(
        board.id,
        { expectedRevision: board.revision, enabled, intervalMinutes: minutes },
        session.csrfToken,
      );
      onUpdated(saved);
      setSuccess('Collection settings saved. A new scan has not been confirmed.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409)
        setError(
          'This board changed elsewhere. Your edits are still here; refresh board status before retrying.',
        );
      else setError(cause instanceof Error ? cause.message : 'Could not save this board.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <article className="op-card">
      <h3>
        {board.displayName} · Lever ({board.region === 'eu' ? 'EU' : 'Global'})
      </h3>
      <p className="hint">
        Site: {board.site} ·{' '}
        {board.verifiedAt
          ? `Official careers source verified ${new Date(board.verifiedAt).toLocaleDateString()}`
          : 'Owner configured; official source not verified by this app'}
      </p>
      {board.officialCareersUrl && (
        <p>
          <a href={board.officialCareersUrl} target="_blank" rel="noopener noreferrer">
            Official careers page
          </a>
        </p>
      )}
      <p>
        Last scan: {board.lastRunAt ? new Date(board.lastRunAt).toLocaleString() : 'none recorded'}{' '}
        · Last successful scan:{' '}
        {board.lastSuccessAt ? new Date(board.lastSuccessAt).toLocaleString() : 'none recorded'}
      </p>
      {board.lastErrorCode && (
        <p role="status">Last scan issue: {board.lastErrorCode.replaceAll('_', ' ')}</p>
      )}
      {board.enabled && <p>Next scheduled scan: {new Date(board.nextScanAt).toLocaleString()}</p>}
      <form onSubmit={(event) => void save(event)} className="preference-grid">
        <label className="checkbox">
          <input
            type="checkbox"
            checked={enabled}
            disabled={busy}
            onChange={(event) => setEnabled(event.target.checked)}
          />{' '}
          Enabled
        </label>
        <label>
          Scan every (minutes)
          <input
            type="number"
            min={15}
            max={10080}
            required
            value={interval}
            disabled={busy}
            onChange={(event) => setInterval(event.target.value)}
          />
        </label>
        <button type="submit" disabled={busy}>
          {busy ? 'Saving…' : 'Save board settings'}
        </button>
      </form>
      {error && (
        <p role="alert" className="error">
          {error}{' '}
          <button className="secondary" type="button" onClick={onRefresh}>
            Refresh board status
          </button>
        </p>
      )}
      {success && (
        <p role="status" className="success">
          {success}
        </p>
      )}
    </article>
  );
}

export function CollectorBoards({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [boards, setBoards] = useState<CollectorBoard[]>([]);
  const [runtime, setRuntime] = useState<RuntimeStatus | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [site, setSite] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [region, setRegion] = useState<'global' | 'eu'>('global');
  const [interval, setInterval] = useState('60');
  const [enabled, setEnabled] = useState(true);
  async function refresh() {
    setLoading(true);
    setError(null);
    try {
      const [items, status] = await Promise.all([listCollectorBoards(), getRuntimeStatus()]);
      setBoards(items);
      setRuntime(status);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setError(cause instanceof Error ? cause.message : 'Could not load collection settings.');
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void refresh();
  }, [onSessionLost]);
  async function add(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const minutes = Number(interval);
    if (
      !/^[A-Za-z0-9][A-Za-z0-9_-]{0,99}$/.test(site) ||
      !Number.isInteger(minutes) ||
      minutes < 15 ||
      minutes > 10080
    ) {
      setError('Enter a valid Lever site and an interval from 15 minutes to 7 days.');
      return;
    }
    setBusy(true);
    setError(null);
    setSuccess(null);
    try {
      const board = await createCollectorBoard(
        {
          provider: 'lever',
          site,
          displayName: displayName.trim() || undefined,
          region,
          enabled,
          intervalMinutes: minutes,
        },
        session.csrfToken,
      );
      setBoards((old) => [...old, board]);
      setSite('');
      setDisplayName('');
      setSuccess('Board saved. No scan or vacancy discovery has been confirmed yet.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409)
        setError('That Lever board is already configured.');
      else setError(cause instanceof Error ? cause.message : 'Could not save the board.');
    } finally {
      setBusy(false);
    }
  }
  const enabledCount = boards.filter((board) => board.enabled).length;
  return (
    <section className="status">
      <div className="op-heading-row">
        <h2>Automatic collection</h2>
        <button
          className="secondary"
          type="button"
          disabled={loading}
          onClick={() => void refresh()}
        >
          Refresh status
        </button>
      </div>
      <p>
        Configure public Lever job boards for scheduled discovery. Collected vacancies enter the
        same processing queue as a pasted link.
      </p>
      <p role="status">
        Collector service:{' '}
        {runtime === null
          ? 'availability unknown'
          : runtime.collectionAvailable
            ? 'started'
            : 'not started'}{' '}
        · {enabledCount} enabled board{enabledCount === 1 ? '' : 's'}.{' '}
        {boards.every((board) => !board.lastRunAt) ? 'No scan has been recorded.' : ''}
      </p>
      {loading && <p>Loading collection boards…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {!loading && boards.length === 0 && <p>No boards configured yet.</p>}
      {boards.map((board) => (
        <BoardRow
          key={board.id}
          board={board}
          session={session}
          onSessionLost={onSessionLost}
          onUpdated={(saved) =>
            setBoards((old) => old.map((item) => (item.id === saved.id ? saved : item)))
          }
          onRefresh={() => void refresh()}
        />
      ))}
      <h3>Add a Lever board</h3>
      <form onSubmit={(event) => void add(event)} className="preference-grid">
        <label>
          Lever site
          <input
            required
            maxLength={100}
            value={site}
            disabled={busy}
            onChange={(event) => setSite(event.target.value)}
            placeholder="Site from jobs.lever.co/site"
          />
        </label>
        <label>
          Board name (optional)
          <input
            maxLength={200}
            value={displayName}
            disabled={busy}
            onChange={(event) => setDisplayName(event.target.value)}
            placeholder="Company name"
          />
        </label>
        <label>
          Region
          <select
            value={region}
            disabled={busy}
            onChange={(event) => setRegion(event.target.value as 'global' | 'eu')}
          >
            <option value="global">Global</option>
            <option value="eu">EU</option>
          </select>
        </label>
        <label>
          Scan every (minutes)
          <input
            type="number"
            min={15}
            max={10080}
            required
            value={interval}
            disabled={busy}
            onChange={(event) => setInterval(event.target.value)}
          />
        </label>
        <label className="checkbox">
          <input
            type="checkbox"
            checked={enabled}
            disabled={busy}
            onChange={(event) => setEnabled(event.target.checked)}
          />{' '}
          Start collection
        </label>
        <button type="submit" disabled={busy}>
          {busy ? 'Saving…' : 'Add board'}
        </button>
      </form>
      {success && (
        <p role="status" className="success">
          {success}
        </p>
      )}
    </section>
  );
}
