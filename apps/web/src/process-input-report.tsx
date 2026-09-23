import type { Round } from './api';

type SavedChange = { operation: string; entityKind?: string; revisionAfter?: number };

function savedChange(value: unknown): value is SavedChange {
  return Boolean(
    value &&
    typeof value === 'object' &&
    'operation' in value &&
    typeof value.operation === 'string',
  );
}

function changeText(change: SavedChange): string {
  switch (change.operation) {
    case 'preferences.correct':
      return 'Updated your campaign brief';
    case 'company.create':
      return 'Added an employer record';
    case 'opportunity.source_save':
    case 'opportunity.source_create':
    case 'opportunity.source_refresh':
      return 'Saved the supplied vacancy as an opportunity';
    case 'opportunity.owner_correction':
      return 'Updated the selected opportunity';
    case 'evidence.correct':
      return 'Updated the selected evidence';
    case 'relationship.correct':
      return 'Updated the selected relationship';
    case 'application_pack.prepare':
      return 'Prepared a new private application pack version';
    default:
      return 'Saved a record change';
  }
}

function packLimit(code: unknown): string {
  switch (code) {
    case 'pack_correction_fenced':
    case 'selected_role_changed':
    case 'selected_role_decision_changed':
    case 'profile_changed':
      return 'The selected role, brief, or pack context changed before a new draft could be saved. Review current records and the selected pack.';
    case 'approved_career_material_unavailable':
      return 'Approved career material was unavailable, so a new draft was not saved.';
    case 'pack_not_prepared':
      return 'The commissioned preparation did not save a new pack version.';
    default:
      return 'No new pack version was confirmed in this round. Review preparation status and the saved pack versions.';
  }
}

export function ProcessInputReport({ round }: { round: Round }) {
  if (round.outcome !== 'process_input') return null;
  const report = round.report;
  const finished = round.state === 'completed' || round.state === 'failed';
  if (round.scope.operations.includes('application_pack.prepare')) {
    const version =
      typeof report.version === 'number' && report.version > 0 ? report.version : null;
    const saved = typeof report.packId === 'string' && report.packId.length > 0 && version !== null;
    const unknowns = Array.isArray(report.materialUnknowns)
      ? report.materialUnknowns.filter(
          (value): value is string => typeof value === 'string' && value.trim().length > 0,
        )
      : [];
    if (!finished && !saved && unknowns.length === 0) return null;
    return (
      <div className="input-report" aria-label="Input work result">
        {saved ? (
          <>
            <h3>Saved changes</h3>
            <p>
              Prepared private application pack version {version}. Review it under Private
              application packs.
            </p>
          </>
        ) : finished ? (
          <p>{packLimit(report.code)}</p>
        ) : null}
        {unknowns.length > 0 && (
          <>
            <h3>Unresolved facts and limits</h3>
            <ul>
              {unknowns.map((fact, index) => (
                <li key={index}>{fact}</li>
              ))}
            </ul>
          </>
        )}
      </div>
    );
  }
  const changes = Array.isArray(report.appliedChanges)
    ? report.appliedChanges.filter(savedChange)
    : [];
  const unresolved = Array.isArray(report.unresolved)
    ? report.unresolved.filter(
        (value): value is string => typeof value === 'string' && value.trim().length > 0,
      )
    : [];
  const original =
    typeof report.originalProfileVersion === 'number' ? report.originalProfileVersion : null;
  const effective =
    typeof report.effectiveProfileVersion === 'number' ? report.effectiveProfileVersion : null;
  if (!finished && changes.length === 0 && unresolved.length === 0) return null;
  return (
    <div className="input-report" aria-label="Input work result">
      {changes.length > 0 ? (
        <>
          <h3>Saved changes</h3>
          <ul>
            {changes.map((change, index) => (
              <li key={`${change.operation}-${index}`}>{changeText(change)}</li>
            ))}
          </ul>
        </>
      ) : finished ? (
        <p>No saved record change is listed in this round report.</p>
      ) : null}
      {original !== null && effective !== null && effective !== original && (
        <p>
          Your effective campaign brief is version {effective} (previously version {original}).
        </p>
      )}
      {unresolved.length > 0 && (
        <>
          <h3>Unresolved facts and limits</h3>
          <ul>
            {unresolved.map((fact, index) => (
              <li key={index}>{fact}</li>
            ))}
          </ul>
        </>
      )}
      {finished && changes.length === 0 && unresolved.length === 0 && (
        <p>
          The round ended without a detailed record result. Refresh work status or review the saved
          records.
        </p>
      )}
    </div>
  );
}
