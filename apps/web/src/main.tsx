import React, { useCallback, useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import {
  getHealth,
  getSession,
  isUnauthenticated,
  login,
  logout,
  type Health,
  type Session,
} from './api';
import { CodexConnectionPanel } from './codex-connection';
import { Opportunities } from './opportunities';
import './style.css';

function message(cause: unknown): string {
  return cause instanceof Error ? cause.message : 'The request could not be completed.';
}

function ServiceStatus() {
  const [health, setHealth] = useState<Health | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [refresh, setRefresh] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    getHealth(controller.signal)
      .then(setHealth)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      });
    return () => controller.abort();
  }, [refresh]);
  return (
    <section className="status" aria-live="polite">
      <h2>Service status</h2>
      {health && (
        <p>
          Dashboard API connected (v{health.version}). Recruitment execution readiness is separate.
        </p>
      )}
      {error && (
        <p role="alert" className="error">
          Could not connect to the dashboard API: {error}
        </p>
      )}
      <button
        className="secondary"
        type="button"
        onClick={() => {
          setError(null);
          setRefresh((value) => value + 1);
        }}
      >
        Refresh service status
      </button>
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
        <p className="eyebrow">Private recruitment workspace</p>
        <h1>Sign in</h1>
        <form onSubmit={(event) => void submit(event)}>
          <label htmlFor="password">Administrator password</label>
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
          First use: run <code>jobseek setup-admin</code> locally.
        </p>
      </div>
      <ServiceStatus />
    </main>
  );
}

function App() {
  const [session, setSession] = useState<Session | null | undefined>(undefined);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState<'agency' | 'account'>('agency');
  const [signingOut, setSigningOut] = useState(false);
  const loseSession = useCallback(() => {
    setSession(null);
    setPage('agency');
    setError(null);
  }, []);
  useEffect(() => {
    const controller = new AbortController();
    getSession(controller.signal)
      .then(setSession)
      .catch((cause) => {
        if (!controller.signal.aborted) setError(message(cause));
      });
    return () => controller.abort();
  }, []);
  useEffect(() => {
    if (!session) return;
    const expiresAt = Date.parse(session.expiresAt);
    const check = () => {
      if (Date.now() >= expiresAt) loseSession();
    };
    check();
    const timer = window.setTimeout(check, Math.max(0, expiresAt - Date.now()));
    window.addEventListener('focus', check);
    document.addEventListener('visibilitychange', check);
    return () => {
      window.clearTimeout(timer);
      window.removeEventListener('focus', check);
      document.removeEventListener('visibilitychange', check);
    };
  }, [session, loseSession]);
  async function signOut() {
    if (!session) return;
    setSigningOut(true);
    setError(null);
    try {
      await logout(session.csrfToken);
      loseSession();
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
        <span>Personal recruitment agency</span>
      </header>
      {session === undefined && !error && (
        <main>
          <p>Checking your session…</p>
        </main>
      )}
      {session === undefined && error && (
        <main role="alert">
          <p>{error}</p>
          <button type="button" onClick={() => window.location.reload()}>
            Try again
          </button>
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
              className={page === 'agency' ? 'active' : 'nav-button'}
              aria-current={page === 'agency' ? 'page' : undefined}
              onClick={() => setPage('agency')}
            >
              Agency
            </button>
            <button
              type="button"
              className={page === 'account' ? 'active' : 'nav-button'}
              aria-current={page === 'account' ? 'page' : undefined}
              onClick={() => setPage('account')}
            >
              Account
            </button>
            <button
              type="button"
              className="sign-out"
              disabled={signingOut}
              onClick={() => void signOut()}
            >
              Sign out
            </button>
          </nav>
          {page === 'agency' && <Opportunities session={session} onSessionLost={loseSession} />}
          {page === 'account' && (
            <main>
              <p className="eyebrow">Owner account</p>
              <h1>Connection and service</h1>
              <CodexConnectionPanel session={session} onSessionLost={loseSession} />
              <ServiceStatus />
            </main>
          )}
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
