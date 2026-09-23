import assert from 'node:assert/strict';
import { startFixture } from './fixture.mjs';

async function openRole(browser, fixture, width = 1180) {
  const context = await browser.newContext({ viewport: { width, height: 844 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  await page.goto(fixture.url, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
  const delivery = page.getByRole('region', { name: 'Application delivery' });
  await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).waitFor();
  await page.getByRole('button', { name: 'Select', exact: true }).click();
  return { context, page, delivery };
}

export async function runDeliverySmoke(browser) {
  // A lost Prepare response is recovered with the same request key and review ID.
  // A Send response lost after SMTP acceptance is resolved through read-only status.
  const single = await startFixture();
  try {
    single.state.delivery.failFirstPrepareResponse = true;
    const { context, page, delivery } = await openRole(browser, single, 390);
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    await delivery.getByRole('button', { name: 'Recover saved Prepare request' }).waitFor();
    const first = single.state.requests.find(
      (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
    );
    assert.deepEqual(first.payload.packIds, ['synthetic-pack-2']);
    assert.ok(first.payload.requestKey.length >= 20);
    await page.reload({ waitUntil: 'networkidle' });
    const recovered = page.getByRole('region', { name: 'Application delivery' });
    await recovered.getByRole('button', { name: 'Recover saved Prepare request' }).click();
    await recovered.getByText(/Your Send action approves/).waitFor();
    await recovered.getByText('Review audit details').click();
    assert.match(await recovered.innerText(), new RegExp('8'.repeat(64)));
    const prepares = single.state.requests.filter(
      (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
    );
    assert.equal(prepares.length, 2);
    assert.deepEqual(prepares[1].payload, prepares[0].payload);
    assert.equal(single.state.delivery.reviews.size, 1);
    assert.match(await recovered.innerText(), /jobs@example.invalid/);
    assert.match(await recovered.innerText(), /owner@example.invalid/);
    assert.match(await recovered.innerText(), /Send your application to jobs@example.invalid/);
    assert.match(await recovered.innerText(), /This synthetic research opening interests me/);
    assert.match(await recovered.innerText(), /PDF attachment/);
    single.state.delivery.failFirstSendResponse = true;
    await recovered.getByRole('button', { name: 'Send reviewed application', exact: true }).click();
    await recovered.getByText(/Accepted by the outgoing SMTP server/).waitFor();
    assert.match(await recovered.innerText(), /Employer receipt is unverified/);
    const approve = single.state.requests.find((request) => request.path.endsWith('/approve'));
    assert.deepEqual(approve.payload, { materialSha256: '8'.repeat(64) });
    assert.equal(
      single.state.requests.filter((request) => request.path.endsWith('/send')).length,
      1,
    );
    await page.reload({ waitUntil: 'networkidle' });
    await page
      .getByRole('region', { name: 'Application delivery' })
      .getByText(/A Send request was issued/)
      .waitFor();
    assert.equal(
      await page.getByRole('button', { name: 'Send reviewed application', exact: true }).count(),
      0,
    );
    assert.equal(
      await page
        .getByRole('button', { name: 'Recover exact Send command for this review' })
        .count(),
      0,
    );
    const dimensions = await page.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
    }));
    assert.equal(dimensions.client, 390);
    assert.ok(
      dimensions.scroll <= dimensions.client,
      `delivery horizontal overflow: ${dimensions.scroll} > ${dimensions.client}`,
    );
    await context.close();
  } finally {
    await single.close();
  }

  // If the first Send never arrived, the owner can explicitly replay only the
  // same approved review command. The backend then creates exactly one round.
  const beforeArrival = await startFixture();
  try {
    const { context, page, delivery } = await openRole(browser, beforeArrival);
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    beforeArrival.state.delivery.dropNextSendBeforeArrival = true;
    await delivery.getByRole('button', { name: 'Send reviewed application', exact: true }).click();
    await delivery
      .getByRole('button', { name: 'Recover exact Send command for this review' })
      .waitFor();
    assert.equal(beforeArrival.state.delivery.round, null);
    await page.reload({ waitUntil: 'networkidle' });
    await page
      .getByRole('region', { name: 'Application delivery' })
      .getByRole('button', { name: 'Recover exact Send command for this review' })
      .click();
    await page.getByText(/Accepted by the outgoing SMTP server/).waitFor();
    const sendCalls = beforeArrival.state.requests.filter((request) =>
      request.path.endsWith('/send'),
    );
    assert.equal(sendCalls.length, 2);
    assert.equal(sendCalls[0].path, sendCalls[1].path);
    assert.equal(beforeArrival.state.delivery.reviews.size, 1);
    await context.close();
  } finally {
    await beforeArrival.close();
  }

  // Batch preparation is exactly the owner-selected pack versions, and Stop
  // uses the commissioned delivery round rather than a local guess.
  const batch = await startFixture();
  try {
    const newest = { ...batch.state.packs[0], id: 'synthetic-pack-3', version: 3 };
    batch.state.packs.unshift(newest);
    batch.state.details.set(newest.id, {
      ...newest,
      manifest: batch.state.details.get('synthetic-pack-2').manifest,
    });
    batch.state.delivery.outcome = 'sending';
    batch.state.delivery.deferSendResponse = true;
    const { context, page, delivery } = await openRole(browser, batch);
    await delivery.getByRole('button', { name: 'Add selected pack to batch' }).click();
    await page
      .getByRole('group', { name: 'Pack versions' })
      .getByRole('button', { name: /Previous · version 2/ })
      .click();
    await delivery.getByRole('button', { name: 'Add selected pack to batch' }).click();
    await delivery.getByRole('button', { name: 'Prepare selected batch' }).click();
    const prepared = batch.state.requests.find(
      (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
    );
    assert.deepEqual(prepared.payload.packIds, ['synthetic-pack-3', 'synthetic-pack-2']);
    assert.equal(batch.state.delivery.reviews.values().next().value.items.length, 2);
    await delivery.getByRole('button', { name: 'Send reviewed applications' }).click();
    for (let attempt = 0; attempt < 100 && !batch.state.delivery.sendResponsePending; attempt++)
      await new Promise((resolve) => setTimeout(resolve, 20));
    assert.equal(
      batch.state.delivery.sendResponsePending,
      true,
      'original Send POST remains pending',
    );
    await delivery.getByRole('button', { name: 'Refresh delivery status' }).click();
    await delivery.getByText(/Send is still processing/).waitFor();
    await delivery.getByRole('button', { name: 'Stop delivery commission' }).click();
    await delivery
      .getByText(/Submission outcome unknown/)
      .first()
      .waitFor();
    assert.equal(
      batch.state.requests.filter((request) => request.path.endsWith('/send')).length,
      1,
    );
    assert.equal(
      batch.state.requests.filter((request) => request.path.endsWith('/stop')).length,
      1,
    );
    assert.equal(batch.state.delivery.reviews.values().next().value.items[1].state, 'prepared');
    await delivery.getByRole('button', { name: 'Check receipt capability' }).click();
    await delivery.getByText(/Read-only receipt check unsupported/).waitFor();
    await delivery.getByRole('button', { name: 'Close unresolved commission' }).click();
    assert.match(await delivery.innerText(), /Submission outcome unknown/);
    await delivery
      .getByRole('button', { name: /Keep this delivery history and start another review/ })
      .click();
    await delivery.getByText(/Earlier delivery reviews/).waitFor();
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).waitFor();
    assert.equal(
      batch.state.requests.filter((request) => request.path.endsWith('/send')).length,
      1,
    );
    await context.close();
  } finally {
    await batch.close();
  }

  // The server's currentness verdict fences stale approval, while missing
  // sender capability stays visible without hiding the saved pack review.
  const stale = await startFixture();
  try {
    const { context, page, delivery } = await openRole(browser, stale);
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    stale.state.delivery.staleNextApproval = true;
    await delivery.getByRole('button', { name: 'Send reviewed application', exact: true }).click();
    await delivery.getByText(/Review is stale or blocked/).waitFor();
    assert.equal(
      stale.state.requests.filter((request) => request.path.endsWith('/send')).length,
      0,
    );
    stale.state.delivery.senderAvailable = false;
    await delivery.getByRole('button', { name: 'Refresh delivery status' }).click();
    await delivery.getByText(/Sender unavailable/).waitFor();
    assert.equal(await page.getByRole('heading', { name: 'Private application packs' }).count(), 1);
    await delivery
      .getByRole('button', { name: 'Leave stale review and prepare current material' })
      .click();
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).waitFor();
    await context.close();
  } finally {
    await stale.close();
  }
  const unsupported = await startFixture();
  try {
    unsupported.state.delivery.routeSupported = false;
    const { context, page, delivery } = await openRole(browser, unsupported);
    await delivery.getByText(/No route record is saved/).waitFor();
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    await delivery
      .getByRole('button', { name: 'Review rejection and start a new Prepare request' })
      .waitFor();
    assert.equal(unsupported.state.delivery.reviews.size, 0);
    assert.equal(await page.getByRole('heading', { name: 'Private application packs' }).count(), 1);
    const rejectedKey = unsupported.state.requests.find(
      (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
    ).payload.requestKey;
    unsupported.state.delivery.routeSupported = true;
    const beforeReload = unsupported.state.requests.filter(
      (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
    ).length;
    await page.reload({ waitUntil: 'networkidle' });
    const reloaded = page.getByRole('region', { name: 'Application delivery' });
    await reloaded
      .getByRole('button', { name: 'Review rejection and start a new Prepare request' })
      .waitFor();
    assert.equal(
      await reloaded.getByRole('button', { name: 'Recover saved Prepare request' }).count(),
      0,
    );
    assert.equal(
      unsupported.state.requests.filter(
        (request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST',
      ).length,
      beforeReload,
      'known 422 reload does not retry delivery Prepare',
    );
    await delivery
      .getByRole('button', { name: 'Review rejection and start a new Prepare request' })
      .click();
    await delivery.getByRole('button', { name: 'Prepare exact selected pack' }).click();
    await delivery.getByText(/Your Send action approves/).waitFor();
    const revisedKey = unsupported.state.requests
      .filter((request) => request.path === '/api/v1/delivery/reviews' && request.method === 'POST')
      .at(-1).payload.requestKey;
    assert.notEqual(revisedKey, rejectedKey);
    await context.close();
  } finally {
    await unsupported.close();
  }
  console.log(
    'Delivery UI smoke passed: exact single/batch review, same-key Prepare recovery, definite rejection revision, Send replay before arrival, status recovery after acceptance, pending-POST Stop, partial-batch close/history, stale refusal, SMTP/receipt status, unsupported lookup, 390px.',
  );
}
