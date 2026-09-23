import React, { useCallback, useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  createAgentCredential,
  getHealth,
  getPreferences,
  getSession,
  isUnauthenticated,
  listAgentCredentials,
  login,
  logout,
  revokeAgentCredential,
  type AgentCredential,
  type Health,
  type Preferences,
  type Session,
} from './api';
import { Opportunities } from './opportunities';
import { CollectorBoards } from './collector-boards';
import { CodexConnectionPanel } from './codex-connection';
import { OrganisationCategories } from './organisation-categories';
import { PreferencesEditor } from './preferences-editor';
import './style.css';

const scopeOptions = [
  ['preferences:read', 'Read job preferences'],
  ['opportunities:read', 'Read opportunities'],
  ['opportunities:write', 'Edit companies and opportunities'],
  ['openings:ingest', 'Submit sourced openings'],
  ['evidence:write', 'Add sourced evidence'],
  ['actions:write', 'Manage follow-ups'],
  ['actions:read', 'Read follow-ups'],
  ['drafts:write', 'Prepare application drafts'],
  ['judgments:request', 'Request assessments'],
] as const;

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'Something went wrong. Try again.';
}

function protectedError(
  cause: unknown,
  onSessionLost: () => void,
  setError: (value: string) => void,
) {
  if (isUnauthenticated(cause)) {
    onSessionLost();
    return;
  }
  setError(message(cause));
}

function ServiceStatus() {
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [retry, setRetry] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true);
    setHealth(null);
    setError(null);
    getHealth(controller.signal)
      .then(setHealth)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(message(cause));
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [retry]);
  return (
    <section aria-live="polite" className="status">
      <h2>Service status</h2>
      {loading && <p>Checking the API…</p>}
      {!loading && health && (
        <p className="success">
          Connected to {health.service} v{health.version}.
        </p>
      )}
      {!loading && error && (
        <div role="alert">
          <p>Could not connect to the dashboard API. {error}</p>
          <button type="button" onClick={() => setRetry((value) => value + 1)}>
            Try again
          </button>
        </div>
      )}
    </section>
  );
}

function LoginPanel({ onLogin }: { onLogin: (session: Session) => void }) {
  const [password, setPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function submit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onLogin(await login(password));
    } catch (cause) {
      setError(message(cause));
    } finally {
      setPassword('');
      setBusy(false);
    }
  }
  return (
    <main className="login-page">
      <div className="login-card">
        <p className="eyebrow">Private workspace</p>
        <h1>Sign in to Jobseek</h1>
        <p>Use the administrator password configured on this device.</p>
        <form onSubmit={submit}>
          <label htmlFor="password">Password</label>
          <input
            id="password"
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
          <button disabled={busy} type="submit">
            {busy ? 'Signing in…' : 'Sign in'}
          </button>
        </form>
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        <p className="hint">
          First use: run <code>jobseek setup-admin</code> locally to set your password.
        </p>
      </div>
      <ServiceStatus />
    </main>
  );
}

function Today({ onSessionLost }: { onSessionLost: () => void }) {
  const [preferences, setPreferences] = useState<Preferences | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    getPreferences(controller.signal)
      .then(setPreferences)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) protectedError(cause, onSessionLost, setError);
      });
    return () => controller.abort();
  }, [onSessionLost]);
  return (
    <main id="today">
      <p className="eyebrow">Your workspace</p>
      <h1>Today</h1>
      <p>
        Open Opportunities to prepare a vacancy URL or text and review saved opportunities. Intake
        processing is currently unavailable.
      </p>
      <section className="status" aria-live="polite">
        <h2>Current search preferences</h2>
        {!preferences && !error && <p>Loading preferences…</p>}
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        {preferences && (
          <>
            <p>
              {preferences.targetHours} hours/week · at least{' '}
              {(preferences.minMonthlyBaseCents / 100).toLocaleString('en-US')}{' '}
              {preferences.salaryCurrency} gross monthly base
              {preferences.preferredLocation
                ? ` · preferred location ${preferences.preferredLocation}`
                : ''}
              ; remote {preferences.allowRemote ? 'allowed' : 'not accepted'}, hybrid{' '}
              {preferences.allowHybrid ? 'allowed' : 'not accepted'}.
            </p>
            <ul>
              {preferences.roleCriteria.map((item) => (
                <li key={item.id}>
                  {item.label} ({item.mode})
                </li>
              ))}
            </ul>
          </>
        )}
      </section>
      <ServiceStatus />
    </main>
  );
}

function Settings({ session, onSessionLost }: { session: Session; onSessionLost: () => void }) {
  const [agents, setAgents] = useState<AgentCredential[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [name, setName] = useState('');
  const [scopes, setScopes] = useState<string[]>(['preferences:read', 'opportunities:read']);
  const [days, setDays] = useState(30);
  const [newToken, setNewToken] = useState<string | null>(null);

  async function refresh() {
    setAgents(await listAgentCredentials());
  }
  useEffect(() => {
    const controller = new AbortController();
    listAgentCredentials(controller.signal)
      .then(setAgents)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) protectedError(cause, onSessionLost, setError);
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [onSessionLost]);
  async function create(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(null);
    setNewToken(null);
    try {
      const expiresAt = new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString();
      const created = await createAgentCredential(name, scopes, expiresAt, session.csrfToken);
      setNewToken(created.token);
      setName('');
      await refresh();
    } catch (cause) {
      protectedError(cause, onSessionLost, setError);
    } finally {
      setBusy(false);
    }
  }
  async function revoke(id: string) {
    setBusy(true);
    setError(null);
    try {
      await revokeAgentCredential(id, session.csrfToken);
      await refresh();
    } catch (cause) {
      protectedError(cause, onSessionLost, setError);
    } finally {
      setBusy(false);
    }
  }
  return (
    <main id="settings">
      <p className="eyebrow">Owner controls</p>
      <h1>Settings</h1>
      <PreferencesEditor session={session} onSessionLost={onSessionLost} />
      <OrganisationCategories session={session} onSessionLost={onSessionLost} />
      <CollectorBoards session={session} onSessionLost={onSessionLost} />
      <CodexConnectionPanel session={session} onSessionLost={onSessionLost} />
      <section className="status">
        <h2>Agent access</h2>
        <p>
          Create a separate token for each agent. Its permitted actions and expiry are shown below.
        </p>
        <form onSubmit={create} className="token-form">
          <label htmlFor="agent-name">Agent name</label>
          <input
            id="agent-name"
            required
            maxLength={80}
            value={name}
            onChange={(event) => setName(event.target.value)}
            placeholder="Research assistant"
          />
          <fieldset>
            <legend>Permitted actions</legend>
            {scopeOptions.map(([scope, label]) => (
              <label className="checkbox" key={scope}>
                <input
                  type="checkbox"
                  checked={scopes.includes(scope)}
                  onChange={(event) => {
                    setScopes((current) =>
                      event.target.checked
                        ? [...current, scope]
                        : current.filter((item) => item !== scope),
                    );
                  }}
                />
                <span>{label}</span>
              </label>
            ))}
          </fieldset>
          <label htmlFor="agent-expiry">Expires after</label>
          <select
            id="agent-expiry"
            value={days}
            onChange={(event) => setDays(Number(event.target.value))}
          >
            <option value={7}>7 days</option>
            <option value={30}>30 days</option>
            <option value={90}>90 days</option>
          </select>
          <button type="submit" disabled={busy || scopes.length === 0}>
            Create token
          </button>
        </form>
        {newToken && (
          <div className="new-token" role="status">
            <h3>Copy this token now</h3>
            <p>It will not appear again after you leave this page.</p>
            <code>{newToken}</code>
            <div className="button-row">
              <button
                type="button"
                onClick={() =>
                  navigator.clipboard
                    .writeText(newToken)
                    .catch((cause: unknown) => setError(message(cause)))
                }
              >
                Copy token
              </button>
              <button type="button" className="secondary" onClick={() => setNewToken(null)}>
                Done
              </button>
            </div>
          </div>
        )}
        {error && (
          <p role="alert" className="error">
            {error}
          </p>
        )}
        <h3>Existing agents</h3>
        {loading && <p>Loading agents…</p>}
        {!loading && agents.length === 0 && <p>No agents have access yet.</p>}
        <ul className="agent-list">
          {agents.map((agent) => (
            <li key={agent.id}>
              <div>
                <strong>{agent.name}</strong>{' '}
                <span className="muted">
                  {agent.revoked
                    ? 'Revoked'
                    : `Expires ${new Date(agent.expiresAt).toLocaleDateString()}`}
                </span>
                <p>{agent.scopes.join(', ')}</p>
              </div>
              {!agent.revoked && (
                <button
                  type="button"
                  className="secondary"
                  disabled={busy}
                  onClick={() => revoke(agent.id)}
                >
                  Revoke
                </button>
              )}
            </li>
          ))}
        </ul>
      </section>
    </main>
  );
}

function App() {
  const [session, setSession] = useState<Session | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState<'today' | 'opportunities' | 'settings'>('today');
  const [signingOut, setSigningOut] = useState(false);
  const loseSession = useCallback(() => {
    setSession(null);
    setPage('today');
    setError(null);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    getSession(controller.signal)
      .then(setSession)
      .catch((cause: unknown) => {
        if (!controller.signal.aborted) setError(message(cause));
      });
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (!session) return;
    const expiresAt = Date.parse(session.expiresAt);
    const remaining = expiresAt - Date.now();
    if (!Number.isFinite(remaining) || remaining <= 0) {
      loseSession();
      return;
    }
    const timer = window.setTimeout(loseSession, remaining);
    const checkExpiry = () => {
      if (Date.now() >= expiresAt) loseSession();
    };
    window.addEventListener('focus', checkExpiry);
    document.addEventListener('visibilitychange', checkExpiry);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener('focus', checkExpiry);
      document.removeEventListener('visibilitychange', checkExpiry);
    };
  }, [session, loseSession]);
  async function signOut() {
    if (!session) return;
    setSigningOut(true);
    setError(null);
    try {
      await logout(session.csrfToken);
      setSession(null);
      setPage('today');
    } catch (cause) {
      if (isUnauthenticated(cause)) loseSession();
      else setError(message(cause));
    } finally {
      setSigningOut(false);
    }
  }
  return (
    <div className="app">
      <header className="topbar">
        <strong>Jobseek</strong>
        <span>Private workspace</span>
      </header>
      {session === undefined && !error && (
        <main>
          <p>Checking your session…</p>
        </main>
      )}
      {session === undefined && error && (
        <main role="alert">
          <p>{error}</p>
          <button onClick={() => window.location.reload()}>Try again</button>
        </main>
      )}
      {session === null && (
        <LoginPanel
          onLogin={(next) => {
            setError(null);
            setSession(next);
          }}
        />
      )}
      {session && (
        <div className="layout">
          <nav aria-label="Main navigation">
            <button
              type="button"
              className={page === 'today' ? 'active' : 'nav-button'}
              onClick={() => setPage('today')}
              aria-current={page === 'today' ? 'page' : undefined}
            >
              Today
            </button>
            <button
              type="button"
              className={page === 'opportunities' ? 'active' : 'nav-button'}
              onClick={() => setPage('opportunities')}
              aria-current={page === 'opportunities' ? 'page' : undefined}
            >
              Opportunities
            </button>
            <span>People</span>
            <span>Applications</span>
            <button
              type="button"
              className={page === 'settings' ? 'active' : 'nav-button'}
              onClick={() => setPage('settings')}
              aria-current={page === 'settings' ? 'page' : undefined}
            >
              Settings
            </button>
            <button type="button" className="sign-out" disabled={signingOut} onClick={signOut}>
              Sign out
            </button>
          </nav>
          {page === 'today' && <Today onSessionLost={loseSession} />}
          {page === 'opportunities' && (
            <Opportunities session={session} onSessionLost={loseSession} />
          )}
          {page === 'settings' && <Settings session={session} onSessionLost={loseSession} />}
          {error && (
            <p role="alert" className="global-error">
              {error}
            </p>
          )}
        </div>
      )}
    </div>
  );
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
