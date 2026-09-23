import { useEffect, useState, type FormEvent } from 'react';
import {
  getOrganisationCategories,
  isUnauthenticated,
  RequestError,
  updateOrganisationCategories,
  type OrganisationCategory,
  type OrganisationCategorySet,
  type Session,
} from './api';

export function OrganisationCategories({
  session,
  onSessionLost,
}: {
  session: Session;
  onSessionLost: () => void;
}) {
  const [saved, setSaved] = useState<OrganisationCategorySet | null>(null);
  const [draft, setDraft] = useState<OrganisationCategory[]>([]);
  const [version, setVersion] = useState(0);
  const [currentConflict, setCurrentConflict] = useState<OrganisationCategorySet | null>(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [success, setSuccess] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    getOrganisationCategories(controller.signal)
      .then((value) => {
        if (controller.signal.aborted) return;
        setSaved(value);
        setDraft(value.categories);
        setVersion(value.version);
      })
      .catch((cause) => {
        if (controller.signal.aborted) return;
        if (isUnauthenticated(cause)) onSessionLost();
        else setError(cause instanceof Error ? cause.message : 'Could not load categories.');
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [onSessionLost]);
  function edit(index: number, patch: Partial<OrganisationCategory>) {
    setDraft((old) => old.map((item, at) => (at === index ? { ...item, ...patch } : item)));
    setSuccess(null);
  }
  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    for (const item of draft) {
      if (!item.description || item.description.trim() !== item.description) {
        setError('Give each category a name or description without surrounding spaces.');
        return;
      }
    }
    setBusy(true);
    setError(null);
    setSuccess(null);
    try {
      const result = await updateOrganisationCategories(version, draft, session.csrfToken);
      setSaved(result);
      setDraft(result.categories);
      setVersion(result.version);
      setCurrentConflict(null);
      setSuccess('Categories saved. Saved opportunities may be reorganised.');
    } catch (cause) {
      if (isUnauthenticated(cause)) onSessionLost();
      else if (cause instanceof RequestError && cause.status === 409) {
        try {
          setCurrentConflict(await getOrganisationCategories());
          setError(
            'Categories changed elsewhere while you were editing. Your edits are still here. Review the currently saved categories before retrying.',
          );
        } catch (reloadCause) {
          setError(
            reloadCause instanceof Error
              ? reloadCause.message
              : 'Could not load current categories.',
          );
        }
      } else setError(cause instanceof Error ? cause.message : 'Could not save categories.');
    } finally {
      setBusy(false);
    }
  }
  return (
    <section className="status">
      <h2>Organisation categories</h2>
      <p>
        Adjust how saved opportunities are grouped. Categories do not change fit or application
        stage. Removing every category pauses new organisation.
      </p>
      {loading && <p>Loading categories…</p>}
      {error && (
        <p role="alert" className="error">
          {error}
        </p>
      )}
      {saved && (
        <form onSubmit={(event) => void save(event)}>
          {draft.map((item, index) => (
            <div className="preference-grid" key={item.id}>
              <label>
                Category name or description
                <input
                  required
                  disabled={busy}
                  maxLength={1000}
                  value={item.description}
                  onChange={(event) => edit(index, { description: event.target.value })}
                />
              </label>
              <button
                className="secondary"
                type="button"
                disabled={busy}
                onClick={() => setDraft((old) => old.filter((_, at) => at !== index))}
              >
                Remove
              </button>
            </div>
          ))}
          {draft.length < 64 && (
            <button
              className="secondary"
              type="button"
              disabled={busy}
              onClick={() =>
                setDraft((old) => [...old, { id: crypto.randomUUID(), description: '' }])
              }
            >
              Add category
            </button>
          )}
          <button type="submit" disabled={busy}>
            {busy ? 'Saving…' : 'Save categories'}
          </button>
        </form>
      )}
      {currentConflict && (
        <div role="status">
          <p>Categories currently saved elsewhere:</p>
          <ul>
            {currentConflict.categories.map((item) => (
              <li key={item.id}>{item.description}</li>
            ))}
          </ul>
          <button
            className="secondary"
            type="button"
            onClick={() => {
              setVersion(currentConflict.version);
              setCurrentConflict(null);
            }}
          >
            I reviewed these; retry saving my edits
          </button>
        </div>
      )}
      {success && (
        <p role="status" className="success">
          {success}
        </p>
      )}
    </section>
  );
}
