import { useEffect, useRef, useState } from 'react';
import {
  applicationPackPdfUrl,
  applicationPackSourceUrl,
  getApplicationPack,
  getPreferences,
  isUnauthenticated,
  listApplicationPacks,
  prepareApplicationRound,
  type ApplicationPackDetail,
  type ApplicationPackSummary,
  type Opportunity,
  type Session,
} from './api';
import { PackRoundPanel } from './pack-round';

function SourceLink({ value }: { value: string }) {
  try {
    const url = new URL(value);
    if ((url.protocol === 'https:' || url.protocol === 'http:') && !url.username && !url.password)
      return (
        <a href={url.href} target="_blank" rel="noopener noreferrer">
          Open recorded role source
        </a>
      );
  } catch {
    /* Show plain context below. */
  }
  return <span>Source link unavailable</span>;
}

type Manifest = ApplicationPackDetail['manifest'];
type Line = Manifest['draft']['focus'];
function DraftLines({ lines, sources }: { lines: Line[]; sources: Manifest['sources'] }) {
  const names = new Map(sources.map((source) => [source.id, source.name]));
  return (
    <ol className="pack-lines">
      {lines.map((line, index) => (
        <li key={`${index}-${line.text}`}>
          <p>{line.text}</p>
          <details>
            <summary>
              {line.citations.length} source citation{line.citations.length === 1 ? '' : 's'}
            </summary>
            <ul>
              {line.citations.map((citation, citationIndex) => (
                <li key={`${citation.sourceId}-${citationIndex}`}>
                  <strong>{names.get(citation.sourceId) || 'Source name unavailable'}:</strong> “
                  {citation.excerpt}”
                </li>
              ))}
            </ul>
          </details>
        </li>
      ))}
    </ol>
  );
}

function Freshness({
  pack,
  opportunityRevision,
  profileVersion,
}: {
  pack: ApplicationPackSummary;
  opportunityRevision: number;
  profileVersion: number | null;
}) {
  const sourceStale = pack.opportunityRevision !== opportunityRevision;
  const profileStale = profileVersion !== null && pack.profileRevision !== profileVersion;
  return (
    <p className={sourceStale || profileStale ? 'error' : 'hint'}>
      {sourceStale
        ? `Role source changed since this pack (revision ${pack.opportunityRevision} → ${opportunityRevision}). `
        : 'Role source revision matches this pack. '}
      {profileVersion === null
        ? 'Current brief revision unavailable; freshness is unknown.'
        : profileStale
          ? `Brief changed since this pack (version ${pack.profileRevision} → ${profileVersion}).`
          : 'Brief revision matches this pack.'}
    </p>
  );
}

export function ApplicationPackPanel({
  opportunity,
  selected,
  session,
  onSessionLost,
}: {
  opportunity: Opportunity;
  selected: boolean;
  session: Session;
  onSessionLost: () => void;
}) {
  const [packs, setPacks] = useState<ApplicationPackSummary[]>([]);
  const [packListAvailable, setPackListAvailable] = useState(false);
  const [packError, setPackError] = useState<string | null>(null);
  const [packLoading, setPackLoading] = useState(true);
  const [profileVersion, setProfileVersion] = useState<number | null>(null);
  const [profileError, setProfileError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  const [selectedId, setSelectedId] = useState('');
  const [detail, setDetail] = useState<ApplicationPackDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const latestId = useRef('');

  useEffect(() => {
    const controller = new AbortController();
    setPackLoading(true);
    setPackError(null);
    setProfileError(null);
    void Promise.allSettled([
      listApplicationPacks(opportunity.id, controller.signal),
      getPreferences(controller.signal),
    ]).then(([packRead, profileRead]) => {
      if (controller.signal.aborted) return;
      if (packRead.status === 'fulfilled') {
        setPacks(packRead.value);
        setPackListAvailable(true);
        const newest = packRead.value[0]?.id || '';
        setSelectedId((old) =>
          !old || old === latestId.current || !packRead.value.some((pack) => pack.id === old)
            ? newest
            : old,
        );
        latestId.current = newest;
      } else if (isUnauthenticated(packRead.reason)) onSessionLost();
      else setPackError('Pack versions are unavailable. Refresh to try again.');
      if (profileRead.status === 'fulfilled') setProfileVersion(profileRead.value.version);
      else if (isUnauthenticated(profileRead.reason)) onSessionLost();
      else {
        setProfileVersion(null);
        setProfileError('Current brief revision is unavailable.');
      }
      setPackLoading(false);
    });
    return () => controller.abort();
  }, [opportunity.id, refresh, onSessionLost]);

  useEffect(() => {
    if (!selectedId) {
      setDetail(null);
      return;
    }
    const controller = new AbortController();
    setDetailLoading(true);
    setDetailError(null);
    setDetail(null);
    getApplicationPack(selectedId, controller.signal)
      .then((value) => {
        if (!controller.signal.aborted) setDetail(value);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setDetailError('This pack version is unavailable. Select another version or refresh.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setDetailLoading(false);
      });
    return () => controller.abort();
  }, [selectedId, refresh, onSessionLost]);

  const manifest = detail?.manifest;
  return (
    <>
      <PackRoundPanel
        opportunityId={opportunity.id}
        selected={selected}
        hasSavedPack={packListAvailable && packs.length > 0}
        sourceReady={Boolean(
          opportunity.sourceUrl.trim() &&
          opportunity.originalText.trim() &&
          !opportunity.archivedAt,
        )}
        session={session}
        startPreparation={(requestKey) =>
          prepareApplicationRound({ requestKey, opportunityId: opportunity.id }, session.csrfToken)
        }
        onRoundSettled={() => setRefresh((old) => old + 1)}
        onSessionLost={onSessionLost}
      />
      <section className="op-card" aria-label="Application packs">
        <div className="op-heading-row">
          <h2>Private application packs</h2>
          <button
            type="button"
            className="secondary"
            disabled={packLoading}
            onClick={() => setRefresh((old) => old + 1)}
          >
            Refresh packs
          </button>
        </div>
        <p className="hint">Packs are prepared drafts. No version here is approved or sent.</p>
        {packLoading && <p role="status">Reading pack versions…</p>}
        {packError && (
          <p role="alert" className="error">
            {packError}
          </p>
        )}
        {profileError && <p role="status">{profileError}</p>}
        {!packListAvailable && !packLoading && <p>Pack versions are unavailable.</p>}
        {packListAvailable && packs.length === 0 && (
          <p>No application pack has been saved for this opportunity.</p>
        )}
        {packs.length > 0 && (
          <>
            <div className="pack-versions" role="group" aria-label="Pack versions">
              {packs.map((pack, index) => (
                <button
                  key={pack.id}
                  type="button"
                  className={selectedId === pack.id ? 'pack-version active' : 'pack-version'}
                  aria-pressed={selectedId === pack.id}
                  onClick={() => setSelectedId(pack.id)}
                >
                  {index === 0 ? 'Newest' : 'Previous'} · version {pack.version} ·{' '}
                  {new Date(pack.createdAt).toLocaleString()}
                </button>
              ))}
            </div>
            {packs.find((pack) => pack.id === selectedId) && (
              <Freshness
                pack={packs.find((pack) => pack.id === selectedId)!}
                opportunityRevision={opportunity.revision}
                profileVersion={profileVersion}
              />
            )}
          </>
        )}
        {detailLoading && <p role="status">Reading selected pack…</p>}
        {detailError && (
          <p role="alert" className="error">
            {detailError}
          </p>
        )}
        {detail && manifest && (
          <div className="pack-review">
            <h3>
              Version {detail.version}: {manifest.role.title}
            </h3>
            <p>
              {manifest.role.company} · prepared {new Date(detail.createdAt).toLocaleString()} ·
              private draft
            </p>
            <p>
              <SourceLink value={manifest.role.sourceUrl} />
            </p>
            <p>
              <strong>Recorded application route:</strong>{' '}
              {manifest.role.destination || 'Unknown or unsupported. No destination is confirmed.'}
            </p>
            <p>
              <strong>Required questions:</strong>{' '}
              {manifest.draft.answers.length
                ? `${manifest.draft.answers.length} captured in this pack.`
                : 'None recorded in this pack; verify the actual route before sending.'}
            </p>
            <div className="button-row">
              <a
                className="button-link"
                href={applicationPackPdfUrl(detail.id)}
                target="_blank"
                rel="noopener noreferrer"
              >
                Open PDF preview
              </a>
              <a
                className="button-link secondary"
                href={applicationPackPdfUrl(detail.id)}
                download={`application-pack-v${detail.version}.pdf`}
              >
                Download PDF
              </a>
              <a
                className="button-link secondary"
                href={applicationPackSourceUrl(detail.id)}
                download
              >
                Download Typst source
              </a>
            </div>
            <iframe
              className="pack-pdf-preview"
              title={`Private PDF preview, version ${detail.version}`}
              src={applicationPackPdfUrl(detail.id)}
              loading="lazy"
            />
            <h4>CV focus</h4>
            <DraftLines lines={[manifest.draft.focus]} sources={manifest.sources} />
            <h4>Cover text</h4>
            <DraftLines lines={manifest.draft.cover} sources={manifest.sources} />
            <h4>Prepared answers</h4>
            {manifest.draft.answers.length ? (
              manifest.draft.answers.map((answer, index) => (
                <section key={`${index}-${answer.question}`} className="pack-answer">
                  <h5>{answer.question}</h5>
                  <DraftLines lines={answer.lines} sources={manifest.sources} />
                </section>
              ))
            ) : (
              <p>No answers were saved in this pack.</p>
            )}
            <h4>Material unknowns and questions</h4>
            {manifest.draft.materialUnknowns.length ? (
              <ul>
                {manifest.draft.materialUnknowns.map((item, index) => (
                  <li key={`${index}-${item}`}>{item}</li>
                ))}
              </ul>
            ) : (
              <p>
                No material unknown was recorded; review the role and route before treating the pack
                as complete.
              </p>
            )}
            <details>
              <summary>Immutable source snapshots ({manifest.sources.length})</summary>
              <ul>
                {manifest.sources.map((source) => (
                  <li key={source.id}>
                    <strong>{source.name}</strong> ·{' '}
                    {source.approved ? 'approved input snapshot' : 'approval unknown'}
                    <pre className="op-source">{source.body}</pre>
                  </li>
                ))}
              </ul>
            </details>
            <details>
              <summary>Jev relevance assessments ({manifest.draft.relevance.length})</summary>
              <p>
                These classify supplied evidence against a requirement; they do not confirm
                qualification or the truth of a drafted claim.
              </p>
              {manifest.draft.relevance.length ? (
                <ul>
                  {manifest.draft.relevance.map((value, index) => (
                    <li key={`${index}-${value.requirement}-${value.sourceId}`}>
                      {value.requirement} · {value.scope} ·{' '}
                      {manifest.sources.find((source) => source.id === value.sourceId)?.name ||
                        'source unavailable'}
                    </li>
                  ))}
                </ul>
              ) : (
                <p>No relevance assessments were recorded.</p>
              )}
            </details>
            <p className="hint">
              To change this draft, prepare a new immutable version. Pack-specific owner correction
              context is not yet available in this review.
            </p>
          </div>
        )}
      </section>
    </>
  );
}
