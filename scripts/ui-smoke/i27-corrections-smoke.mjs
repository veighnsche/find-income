import assert from 'node:assert/strict';
import { startFixture } from './fixture.mjs';

export async function runI27CorrectionsSmoke(browser) {
  const discovery = await startFixture();
  try {
    discovery.state.round = { ...discovery.state.round, state: 'paused' };
    discovery.state.discoveryReady = true;
    const context = await browser.newContext({ viewport: { width: 1180, height: 844 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    await page.goto(discovery.url, { waitUntil: 'networkidle' });
    const home = page.getByRole('region', { name: 'Agency work' });
    discovery.state.staleNextDiscovery = true;
    await home
      .getByRole('button', { name: 'End paused prepare round and find my next opportunities' })
      .click();
    await home.getByRole('button', { name: 'Review work and start a new request' }).waitFor();
    const calls = () =>
      discovery.state.requests.filter(
        (item) => item.method === 'POST' && item.path === '/api/v1/rounds',
      );
    const rejectedKey = calls()[0].payload.requestKey;
    await page.reload({ waitUntil: 'networkidle' });
    await home.getByRole('button', { name: 'Review work and start a new request' }).click();
    await page.waitForFunction(() => localStorage.getItem('jobseek.pending-round-start') === null);
    assert.equal(calls().length, 1, 'reviewing the known 409 does not repeat Start');
    await home
      .getByRole('button', { name: 'End paused prepare round and find my next opportunities' })
      .click();
    await home.getByRole('button', { name: 'Stop this round' }).waitFor();
    assert.notEqual(calls()[1].payload.requestKey, rejectedKey);
    await context.close();
  } finally {
    await discovery.close();
  }

  const instruction = await startFixture();
  try {
    const context = await browser.newContext({ viewport: { width: 1180, height: 844 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    await page.goto(instruction.url, { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Correct this brief' }).click();
    const panel = page.getByRole('region', { name: 'Owner instruction' });
    const original = 'My preferred weekly hours are 30. Please correct the campaign brief.';
    await panel.getByRole('textbox').fill(original);
    instruction.state.profileVersion = 8;
    await panel.getByRole('button', { name: 'Update my brief' }).click();
    await panel.getByRole('button', { name: 'Revise after reviewing work status' }).waitFor();
    const calls = () =>
      instruction.state.requests.filter(
        (item) => item.method === 'POST' && item.path === '/api/v1/rounds/process-input',
      );
    assert.equal(calls().length, 1);
    const rejected = calls()[0].payload;
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Correct this brief' }).click();
    const restored = page.getByRole('region', { name: 'Owner instruction' });
    await restored.getByRole('button', { name: 'Revise after reviewing work status' }).waitFor();
    assert.equal(await restored.getByRole('button', { name: 'Retry same request' }).count(), 0);
    assert.equal(await restored.getByRole('textbox').inputValue(), original);
    assert.equal(calls().length, 1, 'known 409 reload must not repeat ProcessInput');
    await restored.getByRole('button', { name: 'Revise after reviewing work status' }).click();
    assert.equal(await restored.getByRole('textbox').isEnabled(), true);
    await restored.getByRole('textbox').fill(`${original} This is the revised request.`);
    await restored.getByRole('button', { name: 'Update my brief' }).click();
    await restored.getByText(/Request accepted/).waitFor();
    assert.equal(calls().length, 2);
    assert.notEqual(calls()[1].payload.requestKey, rejected.requestKey);
    assert.equal(calls()[1].payload.expectedRevision, 8);
    await context.close();
  } finally {
    await instruction.close();
  }

  const delivery = await startFixture();
  try {
    const context = await browser.newContext({ viewport: { width: 1180, height: 844 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    await page.goto(delivery.url, { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
    await page.getByRole('button', { name: 'Select', exact: true }).click();
    const deliveryPanel = page.getByRole('region', { name: 'Application delivery' });
    await deliveryPanel.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    await deliveryPanel.getByText(/Your Send action approves/).waitFor();
    delivery.state.delivery.outcome = 'sending';
    await deliveryPanel
      .getByRole('button', { name: 'Send reviewed application', exact: true })
      .click();
    await deliveryPanel.getByRole('button', { name: 'Stop delivery commission' }).waitFor();
    await deliveryPanel.getByRole('button', { name: 'Stop delivery commission' }).click();
    await deliveryPanel.getByText(/Delivery commission: paused/).waitFor();
    await page.evaluate(() => localStorage.removeItem('jobseek.delivery-review-id'));
    await page.getByRole('button', { name: '← Back to opportunities' }).click();
    const home = page.getByRole('region', { name: 'Agency work' });
    const recovery = home.getByRole('region', { name: 'Paused delivery recovery' });
    await recovery
      .getByRole('button', { name: /Open saved delivery review for Synthetic Research Role/ })
      .waitFor();
    assert.equal(await home.getByRole('button', { name: 'Resume this round' }).count(), 0);
    assert.equal(delivery.state.requests.filter((item) => item.path.endsWith('/resume')).length, 0);
    await recovery
      .getByRole('button', { name: /Open saved delivery review for Synthetic Research Role/ })
      .click();
    await page
      .getByRole('region', { name: 'Application delivery' })
      .getByRole('button', { name: 'Close unresolved commission' })
      .waitFor();
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.delivery-review-id')),
      'synthetic-delivery-1',
    );
    await context.close();
  } finally {
    await delivery.close();
  }
  console.log(
    'I27 correction smoke passed: known discovery and ProcessInput rejections survive reload, paused delivery opens exact close controls without Resume.',
  );
}
