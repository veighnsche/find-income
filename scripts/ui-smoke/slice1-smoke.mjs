import assert from 'node:assert/strict';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';
import { startFixture } from './fixture.mjs';

// Slice 1 gate (G1): the first live Vite shell on real-shaped records. Signs
// in through the UI, exercises Today / My search / Jobs / Applications, deep
// links, reload persistence, browser back, keyboard focus, and unavailable
// states. Later capabilities stay honestly pending.
async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startFixture();
    fixture.state.expireSession = true;
    browser = await launchBrowser();
    const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const pageErrors = [];
    page.on('pageerror', (cause) => pageErrors.push(cause.message));
    await page.goto(fixture.url, { waitUntil: 'networkidle' });

    await page.getByLabel('Password').waitFor();
    await page.getByLabel('Password').fill('wrong');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('alert').waitFor();
    fixture.state.expireSession = false;
    await page.getByLabel('Password').fill('synthetic-owner-password');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('navigation', { name: 'Primary' }).waitFor();

    await page.getByRole('heading', { name: 'Today' }).waitFor();
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'My search' }).click();
    await page.getByRole('heading', { name: 'My search' }).waitFor();
    await page.getByText('Example City').first().waitFor();
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' }).click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Synthetic Research Role').first().waitFor();
    await page.getByText('Not yet classified (1)').waitFor();
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Applications' }).click();
    await page.getByRole('heading', { name: 'Applications' }).waitFor();

    await page.goto(`${fixture.url}#/jobs/synthetic-role-1`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Synthetic Research Role' }).waitFor();
    await page.getByText('No stage recorded').waitFor();
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Synthetic Research Role' }).waitFor();
    assert.match(page.url(), /#\/jobs\/synthetic-role-1/);
    await page.goBack({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Applications' }).waitFor();

    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' }).focus();
    await page.keyboard.press('Enter');
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    const focused = await page.evaluate(() => {
      const active = document.activeElement;
      return active ? `${active.tagName}:${active.textContent?.trim()}` : 'none';
    });
    assert.ok(focused.length > 0, 'focus observable after keyboard nav');

    const posts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && !item.path.startsWith('/api/v1/auth/'),
    );
    assert.equal(posts.length, 0, `reads must commission no work: ${JSON.stringify(posts)}`);
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log('slice1 smoke: login, four surfaces, deep links, reload/back, keyboard, unavailable states OK');
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice1 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('slice1 smoke failed:', cause);
  process.exitCode = 1;
});
