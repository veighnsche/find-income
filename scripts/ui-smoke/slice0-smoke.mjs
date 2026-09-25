import assert from 'node:assert/strict';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';
import { startFixture } from './fixture.mjs';

// Slice 0 gate (G0): the Vite bundle is the only active frontend. Serves the
// built Vite app with the synthetic API and proves, from a real browser
// context, that the bundle loads and the session contract round-trips.
// Feature scenarios that depend on later tasks are explicitly pending here.
async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startFixture();
    browser = await launchBrowser();
    const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const pageErrors = [];
    page.on('pageerror', (cause) => pageErrors.push(cause.message));
    await page.goto(fixture.url, { waitUntil: 'networkidle' });

    const scripts = await page.$$eval('script[src^="/assets/"]', (nodes) =>
      nodes.map((node) => node.getAttribute('src')),
    );
    assert.ok(scripts.length >= 1, 'Vite bundle script tag served');
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    const session = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/session', {
        headers: { Accept: 'application/json' },
      });
      return { status: response.status, body: await response.json() };
    });
    assert.equal(session.status, 200);
    assert.equal(session.body.actorKind, 'administrator');
    assert.ok(session.body.csrfToken.length > 0);

    const login = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: 'synthetic-owner-password' }),
      });
      return { status: response.status, body: await response.json() };
    });
    assert.equal(login.status, 200);
    assert.equal(login.body.actorId, 'synthetic-owner');

    const rejected = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ password: 'wrong' }),
      });
      return response.status;
    });
    assert.equal(rejected, 401);

    const logout = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/logout', {
        method: 'POST',
        headers: { 'X-CSRF-Token': 'synthetic-csrf' },
      });
      return response.status;
    });
    assert.equal(logout, 204);

    fixture.state.expireSession = true;
    const expired = await page.evaluate(async () => {
      const response = await fetch('/api/v1/auth/session', {
        headers: { Accept: 'application/json' },
      });
      return response.status;
    });
    assert.equal(expired, 401);

    console.log('slice0 smoke: Vite bundle served; session/login/logout/expired contract OK');
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice0 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error(cause);
  process.exit(1);
});
