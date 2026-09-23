import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { startFixture } from './fixture.mjs';

async function launchBrowser() {
  const executablePath = process.env.PLAYWRIGHT_CHROMIUM_EXECUTABLE_PATH;
  if (executablePath) return chromium.launch({ executablePath, headless: true });
  try {
    return await chromium.launch({ channel: 'chrome', headless: true });
  } catch {
    return chromium.launch({ headless: true });
  }
}

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startFixture();
    browser = await launchBrowser();
    const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(10_000);
    const pageErrors = [];
    page.on('pageerror', (cause) => pageErrors.push(cause.message));
    await page.goto(fixture.url, { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
    await page.getByRole('heading', { name: 'Private application packs' }).waitFor();

    // A prior immutable version remains selectable and visibly stale.
    await page.getByRole('button', { name: /Previous · version 1/ }).click();
    await page.getByRole('heading', { name: 'Version 1: Synthetic Research Role' }).waitFor();
    assert.match(
      await page.getByText('Role source changed since this pack').innerText(),
      /revision 2 → 3/,
    );
    assert.match(
      await page.getByText('Brief changed since this pack').innerText(),
      /version 6 → 7/,
    );

    // Selection is a server-backed direct decision; Prepare has no owner data form.
    assert.equal(await page.getByRole('button', { name: 'Prepare a new version' }).count(), 0);
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    await page.getByRole('button', { name: 'Prepare a new version' }).waitFor();
    await page.getByRole('button', { name: 'Prepare a new version' }).click();
    await page.getByRole('button', { name: 'Stop preparation' }).waitFor();
    const prepareCalls = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/rounds/prepare',
    );
    assert.equal(prepareCalls.length, 1, 'one Prepare commission');
    assert.deepEqual(Object.keys(prepareCalls[0].payload).sort(), ['opportunityId', 'requestKey']);
    assert.equal(prepareCalls[0].payload.opportunityId, 'synthetic-role-1');
    assert.ok(prepareCalls[0].payload.requestKey.length >= 20, 'durable request identity present');

    await page.getByRole('button', { name: 'Stop preparation' }).click();
    await page.getByRole('button', { name: 'Resume preparation' }).waitFor();
    assert.equal(
      await page.getByRole('heading', { name: 'Version 1: Synthetic Research Role' }).count(),
      1,
      'old pack retained after Stop',
    );
    await page.getByRole('button', { name: 'Resume preparation' }).click();
    await page.getByRole('button', { name: 'Stop preparation' }).waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.path.endsWith('/stop') && item.method === 'POST')
        .length,
      1,
    );
    assert.equal(
      fixture.state.requests.filter(
        (item) => item.path.endsWith('/resume') && item.method === 'POST',
      ).length,
      1,
    );

    fixture.completePreparation();
    await page.getByRole('button', { name: /Newest · version 3/ }).waitFor({ timeout: 15_000 });
    assert.equal(
      await page.getByRole('heading', { name: 'Version 1: Synthetic Research Role' }).count(),
      1,
      'manual old-version selection remains stable when a new version arrives',
    );
    assert.match(
      await page.getByRole('region', { name: 'Application packs' }).innerText(),
      /No version here is approved or sent/,
    );

    // A failed detail read is actionable: Refresh packs retries the selected detail.
    fixture.state.failDetail = true;
    await page.getByRole('button', { name: /Previous · version 2/ }).click();
    await page
      .getByText('This pack version is unavailable. Select another version or refresh.')
      .waitFor();
    fixture.state.failDetail = false;
    await page.getByRole('button', { name: 'Refresh packs' }).click();
    await page.getByRole('heading', { name: 'Version 2: Synthetic Research Role' }).waitFor();
    assert.match(
      await page.getByRole('region', { name: 'Application packs' }).innerText(),
      /Actual weekly hours remain unconfirmed/,
    );

    await page.setViewportSize({ width: 390, height: 844 });
    const width = await page.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
    }));
    assert.equal(width.client, 390);
    assert.ok(
      width.scroll <= width.client,
      `horizontal overflow: ${width.scroll} > ${width.client}`,
    );
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);
    console.log(
      'UI fixture smoke passed: Prepare payload, Stop/Resume, pack versions, read retry, 390px layout.',
    );
    console.log(
      'Synthetic HTTP fixture only; no live API, Codex runner, provider, account, or employer call.',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('UI fixture cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('UI fixture smoke failed:', cause);
  process.exitCode = 1;
});
