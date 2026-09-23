import { useCallback, useEffect, useRef, useState } from 'react';
import {
  debriefInterview,
  getActiveRound,
  getInterview,
  getRound,
  isUnauthenticated,
  listInterviews,
  prepareInterview,
  RequestError,
  resumeRound,
  stopRound,
  type DebriefInterviewRequest,
  type InterviewDetail,
  type InterviewView,
  type Opportunity,
  type PrepareInterviewRequest,
  type Round,
  type Session,
} from './api';

const pollMs = 5000;
const occupied = new Set<Round['state']>([
  'queued',
  'running',
  'awaiting_input',
  'stopping',
  'paused',
]);
const running = new Set<Round['state']>(['queued', 'running', 'awaiting_input', 'stopping']);
const definiteRejection = (cause: unknown) =>
  cause instanceof RequestError && [400, 403, 409, 422].includes(cause.status);
const errorText = (cause: unknown) =>
  cause instanceof Error ? cause.message : 'The request could not be completed.';
const contextDraftKey = (id: string) => `jobseek.interview-context.${id}`;
const prepareKey = (id: string) => `jobseek.interview-prepare.${id}`;
const prepareRejectionKey = (id: string) => `jobseek.interview-prepare-rejected.${id}`;
const debriefDraftKey = (id: string) => `jobseek.interview-debrief-draft.${id}`;
const debriefKey = (id: string) => `jobseek.interview-debrief-request.${id}`;
const debriefRejectionKey = (id: string) => `jobseek.interview-debrief-rejected.${id}`;

function readStored(key: string): string {
  try {
    return localStorage.getItem(key) || '';
  } catch {
    return '';
  }
}
function writeStored(key: string, value: string) {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    /* The current page retains the entered text. */
  }
}
function readRequest<T>(key: string, valid: (value: Record<string, unknown>) => boolean): T | null {
  try {
    const parsed: unknown = JSON.parse(readStored(key) || 'null');
    return parsed &&
      typeof parsed === 'object' &&
      !Array.isArray(parsed) &&
      valid(parsed as Record<string, unknown>)
      ? (parsed as T)
      : null;
  } catch {
    return null;
  }
}
const readPrepare = (id: string) =>
  readRequest<PrepareInterviewRequest>(
    prepareKey(id),
    (value) =>
      typeof value.requestKey === 'string' &&
      value.opportunityId === id &&
      typeof value.context === 'string',
  );
const readDebrief = (id: string) =>
  readRequest<DebriefInterviewRequest>(
    debriefKey(id),
    (value) => typeof value.requestKey === 'string' && typeof value.notes === 'string',
  );

type Citation = { sourceId: string; excerpt: string };
type Cited = { text: string; citations: Citation[] };
function CitedText({ value, sourceNames }: { value: Cited; sourceNames: Map<string, string> }) {
  return (
    <>
      <span>{value.text}</span>
      {value.citations.length > 0 && (
        <details className="interview-citations">
          <summary>
            {value.citations.length} source citation{value.citations.length === 1 ? '' : 's'}
          </summary>
          <ul>
            {value.citations.map((citation, index) => (
              <li key={`${citation.sourceId}-${index}`}>
                <strong>{sourceNames.get(citation.sourceId) || citation.sourceId}:</strong> “
                {citation.excerpt}”
              </li>
            ))}
          </ul>
        </details>
      )}
    </>
  );
}

function InterviewBriefView({ interview }: { interview: InterviewView }) {
  const brief = interview.brief;
  if (!brief)
    return (
      <p>
        A sourced interview brief has not been saved yet. The invitation remains recorded below.
      </p>
    );
  const input = brief.input;
  const names = new Map([
    ...input.context.map(
      (source) => [source.id, `${source.kind.replaceAll('_', ' ')} source`] as const,
    ),
    ...input.careerSources.map((source) => [source.id, source.name] as const),
  ]);
  const selected =
    interview.focus?.disposition === 'selected'
      ? input.draft.focus.find((focus) => focus.id === interview.focus?.selectedId)
      : undefined;
  return (
    <div className="interview-brief">
      <h3>Prepared interview brief</h3>
      <p>
        <strong>{input.roleTitle}</strong> · {input.employerName}
      </p>
      <p className={interview.current ? 'hint' : 'error'}>
        {interview.current
          ? 'This brief matches the current role and campaign brief.'
          : 'Historical brief: the role or campaign brief changed. Review current context before relying on it.'}
      </p>
      <h4>Interview focus</h4>
      {selected ? (
        <p>
          <strong>Selected focus:</strong> <CitedText value={selected.why} sourceNames={names} />
        </p>
      ) : (
        <p role="status">
          {interview.focus?.disposition === 'unresolved'
            ? 'Focus unresolved by Jev. The sourced brief and questions remain useful.'
            : interview.focus
              ? 'Saved focus selection is unavailable in this brief; review the source material.'
              : 'Focus not available yet. The sourced brief remains available.'}
        </p>
      )}
      {input.draft.focus.length > 0 && (
        <details>
          <summary>Focus alternatives and evidence</summary>
          <ul>
            {input.draft.focus.map((focus) => (
              <li key={focus.id}>
                <CitedText value={focus.why} sourceNames={names} />
              </li>
            ))}
          </ul>
        </details>
      )}
      <h4>Questions to prepare</h4>
      {input.draft.questions.length ? (
        <ol>
          {input.draft.questions.map((question, index) => (
            <li key={`${index}-${question.text}`}>
              <strong>{question.text}</strong>
              <p>
                <CitedText value={question.why} sourceNames={names} />
              </p>
            </li>
          ))}
        </ol>
      ) : (
        <p>No questions were saved.</p>
      )}
      <h4>Approved experience examples</h4>
      {input.draft.examples.length ? (
        <ul>
          {input.draft.examples.map((example, index) => (
            <li key={`${index}-${example.title}`}>
              <strong>{example.title}</strong> · {example.experienceKind.replaceAll('_', ' ')}
              <p>
                Situation: <CitedText value={example.situation} sourceNames={names} />
              </p>
              <p>
                Action: <CitedText value={example.action} sourceNames={names} />
              </p>
              <p>
                Result:{' '}
                {example.result ? (
                  <CitedText value={example.result} sourceNames={names} />
                ) : (
                  example.unknownResult || 'Outcome not established.'
                )}
              </p>
              <details>
                <summary>Context basis</summary>
                <p>
                  {names.get(example.contextBasis.sourceId) || example.contextBasis.sourceId}: “
                  {example.contextBasis.excerpt}”
                </p>
              </details>
            </li>
          ))}
        </ul>
      ) : (
        <p>No approved experience example was saved.</p>
      )}
      <h4>Schedule claims from supplied context</h4>
      {input.draft.scheduleClaims?.length ? (
        <ul>
          {input.draft.scheduleClaims.map((claim, index) => (
            <li key={index}>
              {claim.startRfc3339
                ? new Date(claim.startRfc3339).toLocaleString()
                : 'Start time unknown'}
              {claim.endRfc3339 ? ` – ${new Date(claim.endRfc3339).toLocaleString()}` : ''}
              {claim.mode ? ` · ${claim.mode.replaceAll('_', ' ')}` : ''}
              {claim.venue ? ` · ${claim.venue}` : ''}
              <details>
                <summary>Claim source</summary>
                <p>
                  {names.get(claim.citation.sourceId) || claim.citation.sourceId}: “
                  {claim.citation.excerpt}”
                </p>
              </details>
            </li>
          ))}
        </ul>
      ) : (
        <p>No schedule claim was extracted. No calendar booking is implied.</p>
      )}
      {input.draft.unknowns?.length ? (
        <>
          <h4>Unknowns to verify</h4>
          <ul>
            {input.draft.unknowns.map((unknown, index) => (
              <li key={`${index}-${unknown}`}>{unknown}</li>
            ))}
          </ul>
        </>
      ) : null}
      <details>
        <summary>Source snapshots and brief audit</summary>
        <p>
          Brief input SHA-256 <code>{brief.inputSha256}</code>
        </p>
        <h5>Invitation and role sources</h5>
        <ul>
          {input.context.map((source) => (
            <li key={source.id}>
              <strong>{source.kind.replaceAll('_', ' ')}</strong> · <code>{source.id}</code> ·
              revision {source.revision}
              <pre className="op-source">{source.body}</pre>
            </li>
          ))}
        </ul>
        <h5>Career sources</h5>
        <ul>
          {input.careerSources.map((source) => (
            <li key={source.id}>
              <strong>{source.name}</strong> ·{' '}
              {source.approved ? 'approved source' : 'approval unknown'}
              <pre className="op-source">{source.body}</pre>
            </li>
          ))}
        </ul>
      </details>
    </div>
  );
}

function DebriefView({ detail }: { detail: InterviewDetail }) {
  if (!detail.debriefs.length) return <p>No debrief has been saved for this interview.</p>;
  return (
    <div>
      {detail.debriefs.map((debrief) => {
        const names = new Map<string, string>([['owner_interview_notes', 'Owner-reported notes']]);
        return (
          <article className="interview-debrief" key={debrief.id}>
            <h4>Debrief · {new Date(debrief.createdAt).toLocaleString()}</h4>
            <p className="hint">
              {debrief.attribution === 'owner_reported'
                ? 'Observations are attributed to your reported notes, not an employer decision.'
                : 'Processing or attribution is not yet complete.'}
            </p>
            {debrief.observations?.length ? (
              <ul>
                {debrief.observations.map((observation, index) => (
                  <li key={`${index}-${observation.kind}`}>
                    <strong>{observation.kind.replaceAll('_', ' ')}:</strong>{' '}
                    <CitedText value={observation.detail} sourceNames={names} />
                  </li>
                ))}
              </ul>
            ) : (
              <p>Attributed observations have not been saved yet.</p>
            )}
            {debrief.unknowns?.length ? (
              <ul>
                {debrief.unknowns.map((unknown, index) => (
                  <li key={`${index}-${unknown}`}>Unknown: {unknown}</li>
                ))}
              </ul>
            ) : null}
            <details>
              <summary>Original debrief notes and audit</summary>
              <pre className="op-source">{debrief.notes}</pre>
              <p>
                Debrief <code>{debrief.id}</code>
                {debrief.roundId ? (
                  <>
                    {' '}
                    · round <code>{debrief.roundId}</code>
                  </>
                ) : null}
              </p>
            </details>
          </article>
        );
      })}
    </div>
  );
}

export function InterviewPanel({
  opportunity,
  session,
  onSessionLost,
  onOpenCampaign,
}: {
  opportunity: Opportunity;
  session: Session;
  onSessionLost: () => void;
  onOpenCampaign: () => void;
}) {
  const [interviews, setInterviews] = useState<InterviewView[]>([]);
  const [selectedId, setSelectedId] = useState('');
  const [detail, setDetail] = useState<InterviewDetail | null>(null);
  const [round, setRound] = useState<Round | null>(null);
  const [otherActive, setOtherActive] = useState<Round | null>(null);
  const [context, setContext] = useState(() => readStored(contextDraftKey(opportunity.id)));
  const [pendingPrepare, setPendingPrepare] = useState<PrepareInterviewRequest | null>(() =>
    readPrepare(opportunity.id),
  );
  const [prepareRejected, setPrepareRejected] = useState(
    () => readStored(prepareRejectionKey(opportunity.id)) === 'true',
  );
  const [notes, setNotes] = useState('');
  const [pendingDebrief, setPendingDebrief] = useState<DebriefInterviewRequest | null>(null);
  const [debriefRejected, setDebriefRejected] = useState(false);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [controlBusy, setControlBusy] = useState(false);
  const [error, setError] = useState('');
  const [actionError, setActionError] = useState('');
  const [notice, setNotice] = useState('');
  const commissionInFlight = useRef(false);
  const controlInFlight = useRef(false);
  const readVersion = useRef(0);

  const refresh = useCallback(
    async (id = selectedId, quiet = false): Promise<boolean> => {
      const version = ++readVersion.current;
      if (!quiet) {
        setLoading(true);
        setError('');
      }
      try {
        const [all, active] = await Promise.all([listInterviews(), getActiveRound()]);
        const own = all.filter((item) => item.opportunityId === opportunity.id);
        const chosen = id && own.some((item) => item.id === id) ? id : own[0]?.id || '';
        const saved = chosen ? await getInterview(chosen) : null;
        let ownRound: Round | null = null;
        const prepareRequest = readPrepare(opportunity.id);
        const debriefRequest = saved ? readDebrief(saved.interview.id) : null;
        const pendingKeys = [prepareRequest?.requestKey, debriefRequest?.requestKey].filter(
          (key): key is string => Boolean(key),
        );
        const matchesPending =
          !active || pendingKeys.length === 0 || pendingKeys.includes(active.requestKey);
        if (
          active &&
          saved &&
          matchesPending &&
          (active.scope.inputRefs.includes(`interview:${saved.interview.id}`) ||
            saved.debriefs.some((item) => active.scope.inputRefs.includes(`debrief:${item.id}`)))
        )
          ownRound = active;
        const savedRoundId =
          saved?.debriefs.filter((item) => item.roundId).at(-1)?.roundId ||
          saved?.interview.roundId;
        if (!ownRound && savedRoundId && (savedRoundId !== active?.id || matchesPending))
          ownRound = await getRound(savedRoundId);
        if (version !== readVersion.current) return false;
        setInterviews(own);
        setSelectedId(chosen);
        setDetail(saved);
        setRound(ownRound);
        setOtherActive(active && active.id !== ownRound?.id ? active : null);
        setError('');
        return true;
      } catch (cause) {
        if (version !== readVersion.current) return false;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(`${errorText(cause)} Saved interview details can be retried.`);
        return false;
      } finally {
        if (version === readVersion.current) setLoading(false);
      }
    },
    [opportunity.id, selectedId, onSessionLost],
  );

  useEffect(() => {
    void refresh();
  }, [refresh]);
  useEffect(() => {
    if (
      !pendingPrepare &&
      !pendingDebrief &&
      !(round && occupied.has(round.state)) &&
      !(otherActive && occupied.has(otherActive.state))
    )
      return;
    const timer = window.setInterval(() => void refresh(selectedId, true), pollMs);
    return () => window.clearInterval(timer);
  }, [
    pendingPrepare,
    pendingDebrief,
    round?.id,
    round?.state,
    otherActive?.id,
    otherActive?.state,
    selectedId,
    refresh,
  ]);
  useEffect(() => {
    setPendingDebrief(selectedId ? readDebrief(selectedId) : null);
    setNotes(selectedId ? readStored(debriefDraftKey(selectedId)) : '');
    setDebriefRejected(selectedId ? readStored(debriefRejectionKey(selectedId)) === 'true' : false);
  }, [selectedId]);

  async function prepare() {
    if (
      commissionInFlight.current ||
      controlInFlight.current ||
      (!pendingPrepare &&
        ((otherActive && occupied.has(otherActive.state)) || (round && occupied.has(round.state))))
    )
      return;
    const input = pendingPrepare || {
      requestKey: crypto.randomUUID(),
      opportunityId: opportunity.id,
      context,
    };
    if (!input.context.trim() || input.context.length > 30000) {
      setActionError('Enter the complete invitation and context (up to 30,000 characters).');
      return;
    }
    commissionInFlight.current = true;
    setBusy(true);
    setActionError('');
    setNotice('');
    setPrepareRejected(false);
    writeStored(prepareRejectionKey(opportunity.id), '');
    writeStored(prepareKey(opportunity.id), JSON.stringify(input));
    setPendingPrepare(input);
    try {
      const created = await prepareInterview(input, session.csrfToken);
      writeStored(prepareKey(opportunity.id), '');
      writeStored(prepareRejectionKey(opportunity.id), '');
      setPendingPrepare(null);
      writeStored(contextDraftKey(opportunity.id), '');
      setContext('');
      setSelectedId(created.interviewId);
      setRound(created.round);
      setNotice('Interview preparation commissioned. The saved brief will appear here when ready.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else {
        const rejected = definiteRejection(cause);
        setPrepareRejected(rejected);
        writeStored(prepareRejectionKey(opportunity.id), rejected ? 'true' : '');
        setActionError(
          rejected
            ? `${errorText(cause)} The server rejected this request. Your context and request identity remain saved.`
            : `${errorText(cause)} The response is uncertain. Recover with the same request key and exact context.`,
        );
      }
    } finally {
      commissionInFlight.current = false;
      setBusy(false);
      void refresh();
    }
  }

  async function revisePrepare() {
    if (!prepareRejected || !pendingPrepare || busy || !(await refresh())) return;
    writeStored(prepareKey(opportunity.id), '');
    writeStored(prepareRejectionKey(opportunity.id), '');
    setPendingPrepare(null);
    setPrepareRejected(false);
    setActionError(
      'Current work was refreshed. Revise the saved context before commissioning a new request.',
    );
  }

  async function debrief() {
    if (
      !detail ||
      !detail.interview.current ||
      commissionInFlight.current ||
      controlInFlight.current ||
      (!pendingDebrief &&
        ((otherActive && occupied.has(otherActive.state)) || (round && occupied.has(round.state))))
    )
      return;
    const id = detail.interview.id;
    const input = pendingDebrief || { requestKey: crypto.randomUUID(), notes };
    if (!input.notes.trim() || input.notes.length > 30000) {
      setActionError('Enter complete debrief notes (up to 30,000 characters).');
      return;
    }
    commissionInFlight.current = true;
    setBusy(true);
    setActionError('');
    setNotice('');
    setDebriefRejected(false);
    writeStored(debriefRejectionKey(id), '');
    writeStored(debriefKey(id), JSON.stringify(input));
    setPendingDebrief(input);
    try {
      const created = await debriefInterview(id, input, session.csrfToken);
      writeStored(debriefKey(id), '');
      writeStored(debriefRejectionKey(id), '');
      setPendingDebrief(null);
      writeStored(debriefDraftKey(id), '');
      setNotes('');
      setRound(created.round);
      setNotice(
        'Debrief commissioned. Attributed owner-reported observations will appear here when saved.',
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else {
        const rejected = definiteRejection(cause);
        setDebriefRejected(rejected);
        writeStored(debriefRejectionKey(id), rejected ? 'true' : '');
        setActionError(
          rejected
            ? `${errorText(cause)} The server rejected this debrief. Your notes and request identity remain saved.`
            : `${errorText(cause)} The response is uncertain. Recover the exact saved debrief request.`,
        );
      }
    } finally {
      commissionInFlight.current = false;
      setBusy(false);
      void refresh(id);
    }
  }

  async function reviseDebrief() {
    if (
      !detail ||
      !debriefRejected ||
      !pendingDebrief ||
      busy ||
      !(await refresh(detail.interview.id))
    )
      return;
    writeStored(debriefKey(detail.interview.id), '');
    writeStored(debriefRejectionKey(detail.interview.id), '');
    setPendingDebrief(null);
    setDebriefRejected(false);
    setActionError(
      'Current work was refreshed. Revise the notes before commissioning a new debrief request.',
    );
  }

  async function control(action: 'stop' | 'resume') {
    if (!round || controlInFlight.current) return;
    controlInFlight.current = true;
    setControlBusy(true);
    setActionError('');
    try {
      setRound(
        action === 'stop'
          ? await stopRound(round.id, session.csrfToken)
          : await resumeRound(round.id, session.csrfToken),
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else setActionError(errorText(cause));
    } finally {
      controlInFlight.current = false;
      setControlBusy(false);
      void refresh();
    }
  }

  const blocked = Boolean(
    (otherActive && occupied.has(otherActive.state)) || (round && occupied.has(round.state)),
  );
  return (
    <section className="op-card" aria-label="Interview preparation">
      <div className="op-heading-row">
        <h2>Prepare for an interview</h2>
        <button
          type="button"
          className="secondary"
          disabled={loading}
          onClick={() => void refresh()}
        >
          Refresh interviews
        </button>
      </div>
      <p>
        Paste the complete invitation and relevant context. Codex prepares a sourced private brief;
        no calendar booking or message is sent.
      </p>
      {loading && <p role="status">Reading saved interviews…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {notice && <p role="status">{notice}</p>}
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {otherActive && occupied.has(otherActive.state) && (
        <p role="status">
          Another {otherActive.outcome.replaceAll('_', ' ')} round is{' '}
          {otherActive.state.replaceAll('_', ' ')}. Your interview draft remains saved.{' '}
          <button type="button" className="secondary" onClick={onOpenCampaign}>
            Open campaign work
          </button>{' '}
          to control that round before retrying.
        </p>
      )}
      {round && (
        <div className="interview-round">
          <p role="status">
            Interview work: <strong>{round.state.replaceAll('_', ' ')}</strong> ·{' '}
            {round.step || 'step not reported'} · {round.deliverableStatus || 'status not reported'}
          </p>
          <div className="button-row">
            {running.has(round.state) && (
              <button
                type="button"
                disabled={controlBusy || round.state === 'stopping'}
                onClick={() => void control('stop')}
              >
                Stop interview work
              </button>
            )}
            {round.state === 'paused' && (
              <button type="button" disabled={controlBusy} onClick={() => void control('resume')}>
                {round.reconciliationRequired
                  ? 'Resume and check uncertain work'
                  : 'Resume interview work'}
              </button>
            )}
          </div>
          <details>
            <summary>Round details</summary>
            <p>
              Round <code>{round.id}</code> · {round.outcome.replaceAll('_', ' ')} · deadline{' '}
              {new Date(round.deadline).toLocaleString()}
            </p>
          </details>
        </div>
      )}
      <label className="interview-input">
        Invitation and context
        <textarea
          value={context}
          readOnly={Boolean(pendingPrepare)}
          maxLength={30000}
          rows={5}
          onChange={(event) => {
            setContext(event.target.value);
            writeStored(contextDraftKey(opportunity.id), event.target.value);
          }}
          placeholder="Paste the complete invitation and anything you want considered."
        />
      </label>
      {pendingPrepare && (
        <p role="status">
          An exact Prepare request is saved for this opportunity.{' '}
          {prepareRejected
            ? 'The server rejected it; review work before revising.'
            : 'Its response is uncertain; retrying uses the same key and context.'}
        </p>
      )}
      <div className="button-row">
        {!pendingPrepare && (
          <button
            type="button"
            disabled={busy || loading || blocked || !context.trim()}
            onClick={() => void prepare()}
          >
            Prepare interview brief
          </button>
        )}
        {pendingPrepare && !prepareRejected && (
          <button type="button" disabled={busy} onClick={() => void prepare()}>
            Recover same interview Prepare request
          </button>
        )}
        {pendingPrepare && prepareRejected && (
          <button
            type="button"
            className="secondary"
            disabled={busy || loading || blocked}
            onClick={() => void revisePrepare()}
          >
            Review rejection and revise context
          </button>
        )}
      </div>
      <h3>Saved interviews</h3>
      {interviews.length ? (
        <div className="pack-versions" role="group" aria-label="Saved interviews">
          {interviews.map((item) => (
            <button
              key={item.id}
              type="button"
              className={selectedId === item.id ? 'pack-version active' : 'pack-version'}
              aria-pressed={selectedId === item.id}
              onClick={() => setSelectedId(item.id)}
            >
              {new Date(item.createdAt).toLocaleString()} ·{' '}
              {item.brief ? 'brief ready' : 'preparing'}
              {item.current ? '' : ' · historical'}
            </button>
          ))}
        </div>
      ) : (
        <p>No interview has been saved for this opportunity.</p>
      )}
      {detail && (
        <div className="interview-detail">
          <p>
            <strong>Saved invitation:</strong> {detail.interview.context}
          </p>
          <InterviewBriefView interview={detail.interview} />
          <details>
            <summary>Interview record audit</summary>
            <p>
              Interview <code>{detail.interview.id}</code> · role revision{' '}
              {detail.interview.opportunityRevision} · brief profile version{' '}
              {detail.interview.profileVersion} · context SHA-256{' '}
              <code>{detail.interview.contextSha256}</code>
            </p>
          </details>
          <h3>Record what happened</h3>
          <p>
            Paste complete notes in your own words. Codex will attribute observations to those
            notes; it will not infer an employer decision or send a follow-up.
          </p>
          <label className="interview-input">
            Debrief notes
            <textarea
              value={notes}
              readOnly={Boolean(pendingDebrief)}
              maxLength={30000}
              rows={5}
              onChange={(event) => {
                setNotes(event.target.value);
                writeStored(debriefDraftKey(detail.interview.id), event.target.value);
              }}
              placeholder="What was discussed, what went well, unclear points, and follow-up ideas."
            />
          </label>
          {!detail.interview.current && (
            <p className="error">
              This interview context is stale after a role or profile change. Saved brief and notes
              remain readable; debrief commission is unavailable.
            </p>
          )}
          {pendingDebrief && (
            <p role="status">
              An exact debrief request is saved.{' '}
              {debriefRejected
                ? 'The server rejected it; review work before revising.'
                : 'Its response is uncertain; retrying uses the same key and notes.'}
            </p>
          )}
          <div className="button-row">
            {!pendingDebrief && (
              <button
                type="button"
                disabled={busy || loading || blocked || !detail.interview.current || !notes.trim()}
                onClick={() => void debrief()}
              >
                Record interview debrief
              </button>
            )}
            {pendingDebrief && !debriefRejected && (
              <button
                type="button"
                disabled={busy || !detail.interview.current}
                onClick={() => void debrief()}
              >
                Recover same debrief request
              </button>
            )}
            {pendingDebrief && debriefRejected && (
              <button
                type="button"
                className="secondary"
                disabled={busy || loading || blocked}
                onClick={() => void reviseDebrief()}
              >
                Review rejection and revise notes
              </button>
            )}
          </div>
          <DebriefView detail={detail} />
        </div>
      )}
    </section>
  );
}
