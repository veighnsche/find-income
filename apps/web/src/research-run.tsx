import { useCallback, useEffect, useRef, useState } from 'react';
import {
  commissionResearchRun,
  getResearchReport,
  getResearchRun,
  isUnauthenticated,
  listResearchActivity,
  resumeRound,
  steerResearchRun,
  stopRound,
  type ResearchActivityEvent,
  type ResearchReportView,
  type ResearchRunView,
  type Session,
  type SteeringMessage,
} from './api';
import {
  describeRunState,
  loadPersistedRunId,
  mergeActivityEvents,
  persistRunId,
  researchPollIntervalMs,
  runCounts,
  shouldPollRun,
  type RunReadState,
} from './research-run-state';
import './research-run.css';

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'The request could not be completed.';
}

// Reviewed finite defaults for the first commission (plan §8). Displayed,
// never silently changed; the supervisor owns the enforced values.
const defaultAllowance = {
  timeMs: 900000,
  maxActions: 60,
  maxJev: 12,
  maxTurns: 8,
  maxConcurrent: 2,
};

export function ResearchRunPanel({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [read, setRead] = useState<RunReadState>(() => {
    const restored = loadPersistedRunId(window.localStorage);
    return restored ? { kind: 'loading' } : { kind: 'idle' };
  });
  const [runId, setRunId] = useState<string | null>(() => loadPersistedRunId(window.localStorage));
  const [events, setEvents] = useState<ResearchActivityEvent[]>([]);
  const [cursor, setCursor] = useState('');
  const [report, setReport] = useState<ResearchReportView | null>(null);
  const [briefText, setBriefText] = useState('');
  const [steerText, setSteerText] = useState('');
  const [steerAck, setSteerAck] = useState<SteeringMessage | null>(null);
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState('');
  const readRef = useRef(read);
  readRef.current = read;

  const refresh = useCallback(
    async (id: string, signal: AbortSignal) => {
      try {
        const [view, page] = await Promise.all([
          getResearchRun(id, signal),
          listResearchActivity(id, '', 25, signal),
        ]);
        if (signal.aborted) return;
        setRead({ kind: 'ready', run: view });
        setEvents((prev) => mergeActivityEvents(prev, page.events));
        setCursor(page.nextCursor ?? '');
        if (view.state === 'completed' || view.state === 'failed') {
          try {
            const loaded = await getResearchReport(id, signal);
            if (!signal.aborted) setReport(loaded);
          } catch (cause) {
            if (!signal.aborted && !isUnauthenticated(cause)) setReport(null);
            if (!signal.aborted && isUnauthenticated(cause)) onSessionLost();
          }
        }
      } catch (cause) {
        if (signal.aborted) return;
        if (isUnauthenticated(cause)) {
          onSessionLost();
          return;
        }
        const current = readRef.current;
        // Status reads never commission work: a failed read while active
        // marks the view stale and keeps polling for recovery.
        if (current.kind === 'ready' || current.kind === 'stale') {
          setRead({ kind: 'stale', run: current.run, error: message(cause) });
        } else {
          setRead({ kind: 'error', error: message(cause) });
        }
      }
    },
    [onSessionLost],
  );

  useEffect(() => {
    if (!runId) return;
    const controller = new AbortController();
    void refresh(runId, controller.signal);
    return () => controller.abort();
  }, [runId, refresh]);

  useEffect(() => {
    if (!runId || !shouldPollRun(read)) return;
    const timer = window.setInterval(() => {
      const controller = new AbortController();
      void refresh(runId, controller.signal);
    }, researchPollIntervalMs);
    return () => window.clearInterval(timer);
  }, [runId, read, refresh]);

  async function start() {
    setBusy(true);
    setActionError('');
    try {
      const view = await commissionResearchRun(
        { briefText: briefText.trim() || undefined, allowance: defaultAllowance },
        session.csrfToken,
      );
      persistRunId(window.localStorage, view.runId);
      setEvents([]);
      setCursor('');
      setReport(null);
      setRunId(view.runId);
      setRead({ kind: 'ready', run: view });
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setActionError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function act(kind: 'stop' | 'resume' | 'steer') {
    if (!runId) return;
    setBusy(true);
    setActionError('');
    try {
      if (kind === 'stop') await stopRound(runId, session.csrfToken);
      else if (kind === 'resume') await resumeRound(runId, session.csrfToken);
      else {
        const ack = await steerResearchRun(runId, { body: steerText }, session.csrfToken);
        setSteerAck(ack);
        setSteerText('');
      }
      const controller = new AbortController();
      await refresh(runId, controller.signal);
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setActionError(message(cause));
    } finally {
      setBusy(false);
    }
  }

  async function loadMore() {
    if (!runId || !cursor) return;
    try {
      const page = await listResearchActivity(runId, cursor, 25);
      setEvents((prev) => mergeActivityEvents(prev, page.events));
      setCursor(page.nextCursor ?? '');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setActionError(message(cause));
    }
  }

  const run = read.kind === 'ready' || read.kind === 'stale' ? read.run : null;
  const counts = run ? runCounts(run) : null;

  return (
    <section className="research-run" aria-label="Autonomous research run">
      <h2>Research run</h2>
      {read.kind === 'idle' && (
        <div className="research-start">
          <p>
            Start a bounded research run. The agent chooses sources and queries; allowance is
            finite: 15 minutes, 60 actions, 12 Jev assessments, 8 turns, at most 2 concurrent
            operations.
          </p>
          <label htmlFor="research-brief">Recruitment intent (optional)</label>
          <textarea
            id="research-brief"
            rows={3}
            maxLength={20000}
            value={briefText}
            onChange={(event) => setBriefText(event.target.value)}
            placeholder="What should the agent look for?"
          />
          <button type="button" disabled={busy} onClick={() => void start()}>
            {busy ? 'Starting…' : 'Start research run'}
          </button>
        </div>
      )}
      {read.kind === 'loading' && <p>Loading saved run…</p>}
      {read.kind === 'error' && (
        <p role="alert" className="error">
          {read.error}{' '}
          <button
            type="button"
            className="secondary"
            onClick={() => {
              persistRunId(window.localStorage, null);
              setRunId(null);
              setRead({ kind: 'idle' });
            }}
          >
            Dismiss
          </button>
        </p>
      )}
      {run && (
        <>
          <RunSummary run={run} stale={read.kind === 'stale' ? read.error : null} />
          {counts && (
            <ul className="research-counts">
              <li>Saved records: {counts.saved}</li>
              <li>Unresolved findings: {counts.unresolved}</li>
              <li>Investigations: {counts.investigations}</li>
              <li>
                Actions observed {counts.observedActions}, reserved {counts.reservedActions}
                {counts.unknownUsage && ', plus unknown usage'}
              </li>
            </ul>
          )}
          <div className="research-controls">
            <button type="button" disabled={busy} onClick={() => void act('stop')}>
              Stop
            </button>
            <button type="button" disabled={busy} onClick={() => void act('resume')}>
              Resume
            </button>
            <button
              type="button"
              className="secondary"
              disabled={busy}
              onClick={() => {
                const controller = new AbortController();
                void refresh(run.runId, controller.signal);
              }}
            >
              Refresh
            </button>
          </div>
          <div className="research-steer">
            <label htmlFor="research-steer">Steer the run</label>
            <textarea
              id="research-steer"
              rows={2}
              maxLength={20000}
              value={steerText}
              onChange={(event) => setSteerText(event.target.value)}
              placeholder="Message or correction for the running agent"
            />
            <button
              type="button"
              disabled={busy || steerText.trim() === ''}
              onClick={() => void act('steer')}
            >
              Send steering message
            </button>
            {steerAck && <p className="hint">Message {steerAck.ack}.</p>}
          </div>
          <h3>Activity</h3>
          {events.length === 0 && <p className="hint">No activity recorded yet.</p>}
          <ol className="research-activity">
            {events.map((event) => (
              <li key={event.eventId}>
                <span className="research-kind">{event.kind}</span> {event.summary}
              </li>
            ))}
          </ol>
          {cursor && (
            <button type="button" className="secondary" onClick={() => void loadMore()}>
              Load more activity
            </button>
          )}
          {report && (
            <>
              <h3>Report</h3>
              <ReportView report={report} />
            </>
          )}
        </>
      )}
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
    </section>
  );
}

function RunSummary({ run, stale }: { run: ResearchRunView; stale: string | null }) {
  return (
    <div className="research-summary">
      <p>{describeRunState(run.state)}</p>
      <p className="hint">
        Brief v{run.briefVersion.profileVersion} ({run.briefVersion.rubricVersion})
        {run.stopReason ? ` — stopped: ${run.stopReason}` : ''}
      </p>
      {stale && (
        <p role="alert" className="error">
          Showing last known state: {stale}
        </p>
      )}
    </div>
  );
}

function ReportView({ report }: { report: ResearchReportView }) {
  return (
    <div className="research-report">
      {report.outcomes.length > 0 && (
        <>
          <h4>Outcomes</h4>
          <ul>
            {report.outcomes.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </>
      )}
      {report.uncertainty.length > 0 && (
        <>
          <h4>Remaining uncertainty</h4>
          <ul>
            {report.uncertainty.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </>
      )}
      <p className="hint">
        Searched {report.searched.length} sources, reused {report.reused.length} results.
        {report.budget.unknown && ' Unknown usage remains.'}
        {report.budget.stopReason && ` Stop reason: ${report.budget.stopReason}.`}
      </p>
      {(report.nextWork ?? []).length > 0 && (
        <>
          <h4>Suggested next work</h4>
          <ul>
            {(report.nextWork ?? []).map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </>
      )}
    </div>
  );
}
