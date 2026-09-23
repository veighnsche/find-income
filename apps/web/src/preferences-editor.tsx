import React, { useEffect, useState } from 'react';
import {
  getPreferences,
  isUnauthenticated,
  RequestError,
  updatePreferences,
  type Preferences,
  type PreferencesInput,
  type RoleCriterion,
  type Session,
} from './api';

function editable(value: Preferences): PreferencesInput {
  return {
    preferredLocation: value.preferredLocation,
    allowRemote: value.allowRemote,
    allowHybrid: value.allowHybrid,
    targetHours: value.targetHours,
    minMonthlyBaseCents: value.minMonthlyBaseCents,
    salaryCurrency: value.salaryCurrency,
    timezone: value.timezone,
    roleCriteria: value.roleCriteria.map(({ id, label, description, kind, mode }) => ({
      id,
      label,
      description,
      kind,
      mode,
    })),
  };
}

function validate(value: PreferencesInput): string | null {
  const hours = value.targetHours.trim();
  if (!/^(?:[1-9][0-9]?|1[0-5][0-9]|16[0-8])(?:\.[0-9]{1,2})?$/.test(hours) || Number(hours) > 168)
    return 'Weekly hours must be between 1 and 168 with at most two decimal places.';
  if (!value.timezone.trim()) return 'Enter a time zone.';
  if (
    !['EUR', 'GBP', 'USD', 'CAD', 'AUD', 'CHF', 'NZD'].includes(
      value.salaryCurrency.trim().toUpperCase(),
    )
  )
    return 'Choose a supported two-decimal currency: EUR, GBP, USD, CAD, AUD, CHF, or NZD.';
  if (!Number.isSafeInteger(value.minMonthlyBaseCents) || value.minMonthlyBaseCents < 0)
    return 'Enter a valid minimum monthly amount.';
  if (value.roleCriteria.length > 32) return 'Use at most 32 role criteria.';
  const ids = new Set<string>();
  for (const item of value.roleCriteria) {
    if (!item.id || ids.has(item.id)) return 'Each role criterion needs a unique stable ID.';
    ids.add(item.id);
    if (
      !item.label.trim() ||
      item.label !== item.label.trim() ||
      item.label.length > 100 ||
      !item.kind ||
      !item.mode
    )
      return 'Give each criterion a label, kind, and preference mode.';
  }
  return null;
}

function moneyInput(cents: number): string {
  return Number.isSafeInteger(cents) ? (cents / 100).toFixed(2) : '';
}

function parseMoney(raw: string): number | null {
  if (!/^\d+(?:\.\d{1,2})?$/.test(raw.trim())) return null;
  const [whole, fraction = ''] = raw.trim().split('.');
  const cents = BigInt(whole) * 100n + BigInt(fraction.padEnd(2, '0'));
  return cents <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(cents) : null;
}

export function PreferencesEditor({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [saved, setSaved] = useState<Preferences | null>(null);
  const [draft, setDraft] = useState<PreferencesInput | null>(null);
  const [expectedVersion, setExpectedVersion] = useState<number | null>(null);
  const [conflict, setConflict] = useState<Preferences | null>(null);
  const [amount, setAmount] = useState('');
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    getPreferences(controller.signal)
      .then((value) => {
        setSaved(value);
        setDraft(editable(value));
        setAmount(moneyInput(value.minMonthlyBaseCents));
        setExpectedVersion(value.version);
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(cause instanceof Error ? cause.message : 'Could not load preferences.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [onSessionLost]);

  function update(patch: Partial<PreferencesInput>) {
    setDraft((value) => (value ? { ...value, ...patch } : value));
    setSuccess(null);
  }

  function editCriterion(id: string, patch: Partial<RoleCriterion>) {
    if (!draft) return;
    update({
      roleCriteria: draft.roleCriteria.map((item) =>
        item.id === id ? { ...item, ...patch } : item,
      ),
    });
  }

  async function save(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!draft || expectedVersion === null) return;
    const cents = parseMoney(amount);
    const next = {
      ...draft,
      minMonthlyBaseCents: cents ?? -1,
      salaryCurrency: draft.salaryCurrency.trim().toUpperCase(),
    };
    const problem = validate(next);
    if (problem) {
      setError(problem);
      return;
    }
    setBusy(true);
    setError(null);
    setSuccess(null);
    try {
      const result = await updatePreferences(next, expectedVersion, session.csrfToken);
      setSaved(result.preferences);
      setDraft(editable(result.preferences));
      setAmount(moneyInput(result.preferences.minMonthlyBaseCents));
      setExpectedVersion(result.preferences.version);
      setConflict(null);
      setSuccess(
        `Saved preference version ${result.preferences.version}. Current qualifications will need review.`,
      );
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409) {
        try {
          setConflict(await getPreferences());
          setError(
            'Preferences changed while you were editing. Your draft is preserved. Review the current version before retrying.',
          );
        } catch (reloadCause) {
          setError(
            reloadCause instanceof Error
              ? reloadCause.message
              : 'Could not load current preferences.',
          );
        }
      } else setError(cause instanceof Error ? cause.message : 'Could not save preferences.');
    } finally {
      setBusy(false);
    }
  }

  if (loading)
    return (
      <section className="status">
        <h2>Search preferences</h2>
        <p>Loading preferences…</p>
      </section>
    );
  if (!draft)
    return (
      <section className="status">
        <h2>Search preferences</h2>
        <p role="alert" className="error">
          {error || 'Preferences are unavailable.'}
        </p>
      </section>
    );

  return (
    <section className="status">
      <h2>Search preferences</h2>
      <p className="hint">
        Version {expectedVersion}. These choices guide new qualification reviews. Prior reviews keep
        their recorded policy.
      </p>
      <form onSubmit={(event) => void save(event)}>
        <div className="preference-grid">
          <label>
            Target hours per week
            <input
              inputMode="decimal"
              required
              value={draft.targetHours}
              onChange={(event) => update({ targetHours: event.target.value })}
            />
          </label>
          <label>
            Minimum gross monthly base at those hours
            <input
              inputMode="decimal"
              required
              value={amount}
              onChange={(event) => {
                setAmount(event.target.value);
                setSuccess(null);
              }}
            />
          </label>
          <label>
            Salary currency
            <input
              required
              maxLength={3}
              value={draft.salaryCurrency}
              onChange={(event) => update({ salaryCurrency: event.target.value })}
            />
          </label>
          <label>
            Preferred location (optional)
            <input
              value={draft.preferredLocation}
              onChange={(event) => update({ preferredLocation: event.target.value })}
            />
          </label>
          <label>
            Time zone
            <input
              required
              value={draft.timezone}
              onChange={(event) => update({ timezone: event.target.value })}
            />
          </label>
        </div>
        <div className="preference-options">
          <label className="checkbox">
            <input
              type="checkbox"
              checked={draft.allowRemote}
              onChange={(event) => update({ allowRemote: event.target.checked })}
            />{' '}
            Remote work acceptable
          </label>
          <label className="checkbox">
            <input
              type="checkbox"
              checked={draft.allowHybrid}
              onChange={(event) => update({ allowHybrid: event.target.checked })}
            />{' '}
            Hybrid work acceptable
          </label>
        </div>
        <fieldset className="preference-criteria">
          <legend>Role, responsibility, and technology criteria</legend>
          <p className="hint">
            Require must be supported by evidence. Avoid flags a relevant duty or technology when it
            is actually part of the role. Prefer informs review without blocking it.
          </p>
          {draft.roleCriteria.map((item) => (
            <div className="preference-criterion" key={item.id}>
              <label>
                Label
                <input
                  required
                  maxLength={100}
                  value={item.label}
                  onChange={(event) => editCriterion(item.id, { label: event.target.value })}
                />
              </label>
              <label>
                Kind
                <select
                  required
                  value={item.kind}
                  onChange={(event) =>
                    editCriterion(item.id, { kind: event.target.value as RoleCriterion['kind'] })
                  }
                >
                  <option value="">Choose kind</option>
                  <option value="role">Role</option>
                  <option value="responsibility">Responsibility</option>
                  <option value="technology">Technology</option>
                </select>
              </label>
              <label>
                Preference
                <select
                  required
                  value={item.mode}
                  onChange={(event) =>
                    editCriterion(item.id, { mode: event.target.value as RoleCriterion['mode'] })
                  }
                >
                  <option value="">Choose mode</option>
                  <option value="require">Require</option>
                  <option value="avoid">Avoid</option>
                  <option value="prefer">Prefer</option>
                </select>
              </label>
              <label>
                Description
                <textarea
                  maxLength={1000}
                  value={item.description}
                  onChange={(event) => editCriterion(item.id, { description: event.target.value })}
                />
              </label>
              <button
                type="button"
                className="secondary"
                onClick={() =>
                  update({
                    roleCriteria: draft.roleCriteria.filter((entry) => entry.id !== item.id),
                  })
                }
              >
                Remove criterion
              </button>
            </div>
          ))}
          <button
            type="button"
            className="secondary"
            disabled={draft.roleCriteria.length >= 32}
            onClick={() =>
              update({
                roleCriteria: [
                  ...draft.roleCriteria,
                  {
                    id: crypto.randomUUID(),
                    label: '',
                    description: '',
                    kind: '' as RoleCriterion['kind'],
                    mode: '' as RoleCriterion['mode'],
                  },
                ],
              })
            }
          >
            Add criterion
          </button>
        </fieldset>
        {conflict && (
          <div className="preference-conflict" role="alert">
            <h3>Review current version {conflict.version}</h3>
            <p>Your draft remains in the form above. Current saved choices are:</p>
            <p>
              {conflict.targetHours} hours/week · {moneyInput(conflict.minMonthlyBaseCents)}{' '}
              {conflict.salaryCurrency} monthly base · {conflict.preferredLocation} ·{' '}
              {conflict.timezone} · remote {conflict.allowRemote ? 'yes' : 'no'} · hybrid{' '}
              {conflict.allowHybrid ? 'yes' : 'no'}.
            </p>
            <ul>
              {conflict.roleCriteria.map((item) => (
                <li key={item.id}>
                  {item.label} ({item.kind}, {item.mode}): {item.description || 'No description'}
                </li>
              ))}
            </ul>
            <button
              type="button"
              className="secondary"
              onClick={() => {
                setExpectedVersion(conflict.version);
                setSaved(conflict);
                setConflict(null);
                setError(null);
              }}
            >
              I reviewed the current version; keep my draft
            </button>
            <button
              type="button"
              className="secondary"
              onClick={() => {
                setDraft(editable(conflict));
                setAmount(moneyInput(conflict.minMonthlyBaseCents));
                setExpectedVersion(conflict.version);
                setSaved(conflict);
                setConflict(null);
                setError(null);
              }}
            >
              Discard my draft and use current
            </button>
          </div>
        )}
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
        <button type="submit" disabled={busy || Boolean(conflict)}>
          {busy ? 'Saving…' : 'Save preferences'}
        </button>
      </form>
      {saved && <p className="hint">Saved policy version {saved.version}.</p>}
    </section>
  );
}
