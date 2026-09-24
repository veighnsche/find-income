import assert from 'node:assert/strict';
import { startFixture } from './fixture.mjs';

async function openComparison(browser, fixture, width = 1180) {
  const context = await browser.newContext({ viewport: { width, height: 844 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  await page.goto(fixture.url, { waitUntil: 'networkidle' });
  const panel = page.getByRole('region', { name: 'Whole-offer comparison' });
  await panel.getByRole('heading', { name: 'Compare whole offers' }).waitFor();
  return { context, page, panel };
}

export async function runOfferComparisonSmoke(browser) {
  const fixture = await startFixture();
  try {
    const { context, page, panel } = await openComparison(browser, fixture, 390);
    const texts = [
      'Example Labs employment offer: gross EUR 5,000 each month, 32 hours weekly, holiday included. Marker "7e3" and path C:\\offers\\2026.',
      'Research Studio employment offer: gross EUR 6,000 each month, 32 hours weekly, holiday included.',
      'Project Client contract project: USD 7,000 fee. Costs and holiday terms are unspecified.',
    ];
    await panel.getByRole('textbox', { name: 'Complete offer 1' }).fill(texts[0]);
    await panel.getByRole('button', { name: 'Add another whole offer' }).click();
    await panel.getByRole('textbox', { name: 'Complete offer 2' }).fill(texts[1]);
    await panel.getByRole('button', { name: 'Add another whole offer' }).click();
    await panel.getByRole('textbox', { name: 'Complete offer 3' }).fill(texts[2]);
    await panel
      .getByRole('textbox', { name: 'Priorities context (optional)' })
      .fill('I value predictable hours and clear project costs.');
    fixture.state.failFirstOfferResponse = true;
    await panel.getByRole('button', { name: 'Compare whole offers', exact: true }).click();
    await panel.getByRole('button', { name: 'Recover same offer comparison request' }).waitFor();
    const calls = () =>
      fixture.state.requests.filter((request) => request.path === '/api/v1/rounds/compare-offers');
    assert.equal(calls().length, 1);
    assert.deepEqual(Object.keys(calls()[0].payload).sort(), [
      'offers',
      'prioritiesText',
      'requestKey',
    ]);
    assert.deepEqual(calls()[0].payload.offers, texts);
    await page.reload({ waitUntil: 'networkidle' });
    const reloaded = page.getByRole('region', { name: 'Whole-offer comparison' });
    assert.equal(calls().length, 1, 'reload only reads saved work');
    assert.equal(
      await reloaded.getByRole('textbox', { name: 'Complete offer 3' }).inputValue(),
      texts[2],
    );
    await reloaded.getByRole('button', { name: 'Recover same offer comparison request' }).click();
    await reloaded.getByText(/Offer comparison commissioned/).waitFor();
    assert.equal(calls().length, 2);
    assert.deepEqual(calls()[1].payload, calls()[0].payload);
    fixture.completeOfferComparison();
    await reloaded.getByRole('button', { name: 'Refresh offer comparison' }).click();
    await reloaded.getByRole('heading', { name: 'Saved whole-offer comparison' }).waitFor();
    const body = await reloaded.innerText();
    assert.match(body, /90,071,992,547,409\.93 EUR/);
    assert.match(body, /9007199254740993\/2 cents EUR/);
    assert.match(body, /6,000\.00 EUR/);
    assert.match(body, /32 hours/);
    assert.match(body, /8%/);
    assert.match(body, /project economics/);
    assert.match(body, /Project delivery costs are unknown/);
    assert.match(body, /Tax treatment not stated/);
    assert.match(body, /Jev left the qualitative review unresolved/);
    assert.equal(
      fixture.state.offerComparison.tradeoff.selection.provider_result.confidence,
      1.2e-7,
    );
    assert.match(body, /currency unknown|EUR/);
    await reloaded.getByText('Original offer sources and audit').click();
    assert.match(await reloaded.innerText(), /Example Labs employment offer/);
    assert.match(await reloaded.innerText(), /Marker "7e3" and path C:\\offers\\2026/);

    // A fresh browser with no local key restores the last completed comparison from the server.
    const fresh = await browser.newContext({ viewport: { width: 390, height: 844 } });
    const freshPage = await fresh.newPage();
    await freshPage.goto(fixture.url, { waitUntil: 'networkidle' });
    const freshPanel = freshPage.getByRole('region', { name: 'Whole-offer comparison' });
    await freshPanel.getByRole('heading', { name: 'Saved whole-offer comparison' }).waitFor();
    assert.match(await freshPanel.innerText(), /9007199254740993\/2 cents EUR/);
    assert.equal(calls().length, 2);
    fixture.state.offerComparison.tradeoffStatus = 'selected';
    fixture.state.offerComparison.tradeoff = {
      comparison: fixture.state.offerComparison.comparison,
      alternative: fixture.state.offerComparison.comparison.input.alternatives[0],
      selection: {
        disposition: 'selected',
        selected_id: 'clarify-project-costs',
        input_sha256: 'b'.repeat(64),
        request_snapshot: { sample_probability: 0.125 },
        provider_result: { probability: 0.72, confidence: 1.2e-7, tiny_exponent: 2e-30 },
      },
    };
    assert.notEqual(
      fixture.state.offerComparison.tradeoff.selection.input_sha256,
      fixture.state.offerComparison.comparison.inputSha256,
    );
    await freshPanel.getByRole('button', { name: 'Refresh offer comparison' }).click();
    await freshPanel.getByText(/Jev selected one cited issue to review/).waitFor();
    fixture.state.offerComparison.tradeoffStatus = 'failed';
    fixture.state.offerComparison.tradeoff = undefined;
    fixture.state.offerComparison.current = false;
    await freshPanel.getByRole('button', { name: 'Refresh offer comparison' }).click();
    await freshPanel.getByText(/Qualitative review status: failed/).waitFor();
    await freshPanel.getByText(/Historical comparison: the campaign brief changed/).waitFor();
    const dimensions = await freshPage.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
    }));
    assert.equal(dimensions.client, 390);
    assert.ok(
      dimensions.scroll <= dimensions.client,
      `offer comparison horizontal overflow: ${dimensions.scroll} > ${dimensions.client}`,
    );
    await fresh.close();
    await context.close();
  } finally {
    await fixture.close();
  }

  const held = await startFixture();
  try {
    const { context, page, panel } = await openComparison(browser, held);
    await panel
      .getByRole('textbox', { name: 'Complete offer 1' })
      .fill('Complete offer while POST remains open.');
    held.state.deferOfferResponse = true;
    held.state.failFirstOfferResponse = true;
    await panel.getByRole('button', { name: 'Compare whole offers', exact: true }).click();
    const request = held.state.requests.find(
      (item) => item.path === '/api/v1/rounds/compare-offers',
    ).payload;
    held.state.round.requestKey = 'another-request-key';
    await panel.getByRole('button', { name: 'Refresh offer comparison' }).click();
    await panel.getByText(/Another compare offers round is running/).waitFor();
    assert.equal(await panel.getByRole('button', { name: 'Stop offer comparison' }).count(), 0);
    held.state.round.requestKey = request.requestKey;
    await panel.getByRole('button', { name: 'Refresh offer comparison' }).click();
    await panel.getByRole('button', { name: 'Stop offer comparison' }).waitFor();
    assert.equal(held.state.offerResponsePending, true);
    assert.deepEqual(
      await page.evaluate(() =>
        JSON.parse(localStorage.getItem('jobseek.offer-comparison-pending')),
      ),
      request,
    );
    await panel.getByRole('button', { name: 'Stop offer comparison' }).click();
    assert.equal(held.state.round.state, 'paused');
    assert.equal(held.state.offerResponsePending, true);
    held.state.releaseOfferResponse();
    await panel.getByRole('button', { name: 'Recover same offer comparison request' }).waitFor();
    await panel.getByRole('button', { name: 'Recover same offer comparison request' }).click();
    await panel.getByText(/Offer comparison commissioned/).waitFor();
    const calls = held.state.requests.filter(
      (item) => item.path === '/api/v1/rounds/compare-offers',
    );
    assert.equal(calls.length, 2);
    assert.deepEqual(calls[1].payload, calls[0].payload);
    await context.close();
  } finally {
    held.state.releaseOfferResponse?.();
    await held.close();
  }

  const conflict = await startFixture();
  try {
    conflict.state.round = { ...conflict.state.round, outcome: 'prepare', state: 'paused' };
    const { context, page, panel } = await openComparison(browser, conflict);
    await panel
      .getByRole('textbox', { name: 'Complete offer 1' })
      .fill('Full offer draft survives conflict.');
    await panel.getByText(/Another prepare round is paused/).waitFor();
    assert.equal(
      await panel.getByRole('button', { name: 'Compare whole offers', exact: true }).isDisabled(),
      true,
    );
    await page.reload({ waitUntil: 'networkidle' });
    const same = page.getByRole('region', { name: 'Whole-offer comparison' });
    assert.equal(
      await same.getByRole('textbox', { name: 'Complete offer 1' }).inputValue(),
      'Full offer draft survives conflict.',
    );
    await same.getByRole('button', { name: 'Open current round controls' }).click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Resume this round' })
      .waitFor();
    await context.close();
  } finally {
    await conflict.close();
  }

  const rejected = await startFixture();
  try {
    const { context, page, panel } = await openComparison(browser, rejected);
    await panel
      .getByRole('textbox', { name: 'Complete offer 1' })
      .fill('First full offer request.');
    rejected.state.rejectNextOfferRequest = true;
    await panel.getByRole('button', { name: 'Compare whole offers', exact: true }).click();
    await panel.getByRole('button', { name: 'Review rejection and revise offers' }).waitFor();
    const first = rejected.state.requests.find(
      (item) => item.path === '/api/v1/rounds/compare-offers',
    ).payload;
    await page.reload({ waitUntil: 'networkidle' });
    const same = page.getByRole('region', { name: 'Whole-offer comparison' });
    await same.getByRole('button', { name: 'Review rejection and revise offers' }).click();
    await same
      .getByRole('textbox', { name: 'Complete offer 1' })
      .fill('Revised complete offer request.');
    await same.getByRole('button', { name: 'Compare whole offers', exact: true }).click();
    const next = rejected.state.requests
      .filter((item) => item.path === '/api/v1/rounds/compare-offers')
      .at(-1).payload;
    assert.notEqual(first.requestKey, next.requestKey);
    assert.match(next.offers[0], /^Revised complete/);
    await context.close();
  } finally {
    await rejected.close();
  }
  console.log(
    'Offer comparison UI smoke passed: complete intake, exact rational display, project economics, Jev states, historical/fresh-browser restore, same-key recovery, held-POST Stop with exact key, conflict/revision, 390px.',
  );
}
