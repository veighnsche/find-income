import React, { useEffect, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { getHealth, type Health } from './api';
import './style.css';

function App() {
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
        if (!controller.signal.aborted) {
          setError(cause instanceof Error ? cause.message : 'Could not reach the API.');
        }
      })
      .finally(() => {
        if (!controller.signal.aborted) setLoading(false);
      });
    return () => controller.abort();
  }, [retry]);

  return (
    <div className="app">
      <header className="topbar">
        <strong>Jobseek</strong>
        <span>Private workspace</span>
      </header>
      <div className="layout">
        <nav aria-label="Main navigation">
          <a href="#today" aria-current="page">
            Today
          </a>
          <span>Opportunities</span>
          <span>People</span>
          <span>Applications</span>
          <span>Settings</span>
        </nav>
        <main id="today">
          <p className="eyebrow">Foundation</p>
          <h1>Today</h1>
          <p>
            The dashboard connection is ready. Opportunity tracking will appear here as the service
            is built.
          </p>
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
                <button onClick={() => setRetry((value) => value + 1)}>Try again</button>
              </div>
            )}
          </section>
        </main>
      </div>
    </div>
  );
}

createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
);
