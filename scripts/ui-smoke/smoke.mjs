import assert from 'node:assert/strict';
import { chromium } from 'playwright-core';
import { startFixture } from './fixture.mjs';
import { runDeliverySmoke } from './delivery-smoke.mjs';
import { runRecommendationSmoke } from './recommendation-smoke.mjs';
import { runInterviewSmoke } from './interview-smoke.mjs';
import { runOfferComparisonSmoke } from './offer-comparison-smoke.mjs';

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
    page.setDefaultTimeout(20_000);
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
    await page
      .getByRole('button', { name: 'End paused prepare round and prepare this application' })
      .waitFor();
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

    // The selected immutable pack is the exact correction target. An interrupted
    // response retains the request key and payload across a retry.
    fixture.pauseRound();
    const packArea = page.getByRole('region', { name: 'Application packs' });
    await packArea.getByText('Reading pack versions…').waitFor({ state: 'detached' });
    await packArea.getByText('Reading selected pack…').waitFor({ state: 'detached' });
    const selectedVersion = packArea.getByRole('button', { name: /Previous · version 2/ });
    assert.equal(await selectedVersion.getAttribute('aria-pressed'), 'true');
    const packCorrection = packArea.getByRole('region', { name: 'Owner instruction' });
    await packCorrection.getByRole('button', { name: 'Refresh work status' }).click();
    await packCorrection.getByText(/The paused prepare round will end/).waitFor();
    const editor = packCorrection.getByRole('textbox');
    const editorNode = await editor.elementHandle();
    await editor.fill('The cover text overstates my research experience.');
    assert.equal(
      await editorNode.evaluate((node) => node.isConnected),
      true,
      'selected pack editor stayed mounted during fill',
    );
    assert.equal(
      await selectedVersion.getAttribute('aria-pressed'),
      'true',
      'older selected pack remained selected',
    );
    assert.equal(await editor.inputValue(), 'The cover text overstates my research experience.');
    assert.match(
      (await page.evaluate(() =>
        localStorage.getItem('jobseek.process-input.application_pack.synthetic-pack-2'),
      )) || '',
      /cover text overstates/,
      'selected pack correction draft persisted before commission',
    );
    fixture.state.failFirstProcessResponse = true;
    await packCorrection
      .getByRole('button', { name: /End paused work and correct this pack/ })
      .click();
    await packCorrection.getByRole('button', { name: 'Retry same request' }).waitFor();
    const processCalls = () =>
      fixture.state.requests.filter(
        (item) => item.method === 'POST' && item.path === '/api/v1/rounds/process-input',
      );
    assert.equal(processCalls().length, 1);
    const first = processCalls()[0].payload;
    assert.deepEqual(first.replacePaused, { roundId: 'synthetic-prepare-1', expectedRevision: 6 });
    assert.equal(first.targetKind, 'application_pack');
    assert.equal(first.targetId, 'synthetic-pack-2');
    assert.equal(first.expectedRevision, 2);
    assert.equal(first.text, 'The cover text overstates my research experience.');
    await packCorrection.getByRole('button', { name: 'Retry same request' }).click();
    await packCorrection.getByText(/Request accepted. The agency is handling this input/).waitFor();
    assert.equal(processCalls().length, 2);
    assert.deepEqual(processCalls()[1].payload, first);
    fixture.completeInput();
    await packCorrection.getByRole('button', { name: 'Refresh work status' }).click();
    await packCorrection.getByText(/Prepared private application pack version 4/).waitFor();
    await packCorrection.getByText(/Application destination remains unconfirmed/).waitFor();
    await page
      .getByRole('group', { name: 'Pack versions' })
      .getByRole('button', { name: /Newest · version 4/ })
      .waitFor();
    assert.equal(fixture.state.round.report.summary, undefined);
    assert.equal(
      await page.getByRole('heading', { name: 'Version 2: Synthetic Research Role' }).count(),
      1,
    );

    // The home paste box commissions processing in the campaign context.
    await page.getByRole('button', { name: '← Back to opportunities' }).click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Refresh work' })
      .click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByText(/Prepared private application pack version 4/)
      .waitFor();
    const campaignInput = page.getByRole('region', { name: 'Owner instruction' });
    await campaignInput.getByRole('textbox').fill('https://example.invalid/jobs/second-role');
    await campaignInput.getByRole('button', { name: 'Handle this material' }).click();
    await campaignInput.getByText(/Request accepted. The agency is handling this input/).waitFor();
    const campaignCall = processCalls().at(-1).payload;
    assert.equal(campaignCall.targetKind, 'campaign');
    assert.equal(campaignCall.targetId, 'active');
    assert.equal(campaignCall.expectedRevision, 7);
    assert.equal(campaignCall.sourceUrl, 'https://example.invalid/jobs/second-role');
    assert.equal(campaignCall.originalText, undefined);

    fixture.completeInput();
    await campaignInput.getByRole('button', { name: 'Refresh work status' }).click();
    await campaignInput
      .getByText(/saved vacancy URL could not be verified from a supported source/)
      .waitFor();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Refresh work' })
      .click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByText(/saved vacancy URL could not be verified from a supported source/)
      .waitFor();

    await campaignInput.getByRole('textbox').fill('https://jobs.lever.co/example/role-2');
    await campaignInput.getByRole('button', { name: 'Handle this material' }).click();
    await campaignInput.getByText(/Request accepted. The agency is handling this input/).waitFor();
    fixture.state.organisationLimitNext = true;
    fixture.completeInput();
    await campaignInput.getByRole('button', { name: 'Refresh work status' }).click();
    await campaignInput.getByText('Saved the supplied vacancy as an opportunity').waitFor();
    await campaignInput
      .getByText(/vacancy was saved, but semantic organisation could not be recorded/)
      .waitFor();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Refresh work' })
      .click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByText(/vacancy was saved, but semantic organisation could not be recorded/)
      .waitFor();
    await page.getByRole('button', { name: 'Correct this brief' }).click();
    const profileInput = page.getByRole('region', { name: 'Owner instruction' });
    await profileInput.getByRole('textbox').fill('My target hours are now 30 each week.');
    fixture.state.failFirstProcessResponse = true;
    await profileInput.getByRole('button', { name: 'Update my brief' }).click();
    await profileInput.getByRole('button', { name: 'Retry same request' }).waitFor();
    const profileCall = processCalls().at(-1).payload;
    assert.equal(profileCall.targetKind, 'profile');
    assert.equal(profileCall.targetId, 'current');
    assert.equal(profileCall.expectedRevision, 7);
    assert.equal(profileCall.text, 'My target hours are now 30 each week.');
    assert.equal(profileCall.sourceUrl, undefined);
    assert.equal(
      fixture.state.profileVersion,
      8,
      'server committed the profile before its response was lost',
    );
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Correct this brief' }).click();
    await profileInput.getByRole('button', { name: 'Retry same request' }).waitFor();
    assert.equal(await profileInput.getByRole('textbox').inputValue(), profileCall.text);
    assert.equal(await profileInput.getByRole('textbox').isDisabled(), true);
    await profileInput.getByRole('button', { name: 'Retry same request' }).click();
    await profileInput.getByText(/Request accepted. The agency is handling this input/).waitFor();
    assert.deepEqual(processCalls().at(-1).payload, profileCall);
    assert.equal(await profileInput.getByRole('textbox').inputValue(), '');
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.process-input.profile.current')),
      null,
    );
    assert.equal(fixture.state.profileVersion, 8, 'replay must not apply the correction again');

    // A lost paused-replacement response remains replayable after the new round
    // becomes active, for both Prepare and discovery Start.
    fixture.completeInput();
    await profileInput.getByRole('button', { name: 'Refresh work status' }).click();
    await profileInput.getByText('Updated your campaign brief').waitFor();
    await profileInput.getByText(/effective campaign brief is version 8/).waitFor();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Refresh work' })
      .click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByText('Updated your campaign brief')
      .waitFor();
    fixture.pauseRound();
    const pausedInput = { id: fixture.state.round.id, revision: fixture.state.round.revision };
    await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
    const preparation = page.getByRole('region', { name: 'Application preparation' });
    await preparation
      .getByRole('button', { name: 'End paused process input round and prepare this application' })
      .waitFor();
    fixture.state.failFirstPrepareResponse = true;
    await preparation
      .getByRole('button', { name: 'End paused process input round and prepare this application' })
      .click();
    await preparation.getByRole('button', { name: 'Retry same preparation request' }).waitFor();
    const prepareReplay = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds/prepare')
      .at(-1).payload;
    assert.deepEqual(prepareReplay.replacePaused, {
      roundId: pausedInput.id,
      expectedRevision: pausedInput.revision,
    });
    await page.reload({ waitUntil: 'networkidle' });
    await preparation.getByRole('button', { name: 'Retry same preparation request' }).click();
    await preparation
      .getByRole('button', { name: 'Retry same preparation request' })
      .waitFor({ state: 'detached' });
    assert.deepEqual(
      fixture.state.requests
        .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds/prepare')
        .at(-1).payload,
      prepareReplay,
    );
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.prepare-request.synthetic-role-1')),
      null,
    );

    fixture.completeInput();
    fixture.pauseRound();
    const pausedPrepare = { id: fixture.state.round.id, revision: fixture.state.round.revision };
    fixture.state.discoveryReady = true;
    await page.getByRole('button', { name: '← Back to opportunities' }).click();
    const agency = page.getByRole('region', { name: 'Agency work' });
    await agency
      .getByRole('button', { name: 'End paused prepare round and find my next opportunities' })
      .waitFor();
    fixture.state.failFirstDiscoveryResponse = true;
    await agency
      .getByRole('button', { name: 'End paused prepare round and find my next opportunities' })
      .click();
    await agency.getByRole('button', { name: 'Retry same discovery request' }).waitFor();
    const discoverReplay = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds')
      .at(-1).payload;
    assert.deepEqual(discoverReplay.replacePaused, {
      roundId: pausedPrepare.id,
      expectedRevision: pausedPrepare.revision,
    });
    await page.reload({ waitUntil: 'networkidle' });
    await agency.getByRole('button', { name: 'Retry same discovery request' }).click();
    await agency
      .getByRole('button', { name: 'Retry same discovery request' })
      .waitFor({ state: 'detached' });
    assert.deepEqual(
      fixture.state.requests
        .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds')
        .at(-1).payload,
      discoverReplay,
    );
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.pending-round-start')),
      null,
    );

    // A definite stale-revision 409 permits review and a fresh request.
    fixture.completeInput();
    fixture.pauseRound();
    await agency.getByRole('button', { name: 'Refresh work' }).click();
    const replaceDiscovery = agency.getByRole('button', {
      name: 'End paused discover round and find my next opportunities',
    });
    await replaceDiscovery.waitFor();
    fixture.state.staleNextDiscovery = true;
    await replaceDiscovery.click();
    await agency.getByRole('button', { name: 'Review work and start a new request' }).click();
    await agency
      .getByRole('button', { name: 'Retry same discovery request' })
      .waitFor({ state: 'detached' });
    const rejectedDiscovery = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds')
      .at(-1).payload;
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.pending-round-start')),
      null,
    );
    await replaceDiscovery.click();
    await agency.getByRole('button', { name: 'Stop this round' }).waitFor();
    const freshDiscovery = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds')
      .at(-1).payload;
    assert.notEqual(freshDiscovery.requestKey, rejectedDiscovery.requestKey);
    assert.equal(
      freshDiscovery.replacePaused.expectedRevision,
      rejectedDiscovery.replacePaused.expectedRevision + 1,
    );

    fixture.completeInput();
    fixture.pauseRound();
    await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
    const preparationAgain = page.getByRole('region', { name: 'Application preparation' });
    await preparationAgain.getByRole('button', { name: 'Refresh preparation' }).click();
    const replacePrepare = preparationAgain.getByRole('button', {
      name: 'End paused discover round and prepare this application',
    });
    await replacePrepare.waitFor();
    fixture.state.staleNextPrepare = true;
    await replacePrepare.click();
    await preparationAgain
      .getByRole('button', { name: 'Review work and start a new preparation request' })
      .click();
    await preparationAgain
      .getByRole('button', { name: 'Retry same preparation request' })
      .waitFor({ state: 'detached' });
    const rejectedPrepare = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds/prepare')
      .at(-1).payload;
    assert.equal(
      await page.evaluate(() => localStorage.getItem('jobseek.prepare-request.synthetic-role-1')),
      null,
    );
    await replacePrepare.click();
    await preparationAgain.getByRole('button', { name: 'Stop preparation' }).waitFor();
    const freshPrepare = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path === '/api/v1/rounds/prepare')
      .at(-1).payload;
    assert.notEqual(freshPrepare.requestKey, rejectedPrepare.requestKey);
    assert.equal(
      freshPrepare.replacePaused.expectedRevision,
      rejectedPrepare.replacePaused.expectedRevision + 1,
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
    await runDeliverySmoke(browser);
    await runRecommendationSmoke(browser);
    await runInterviewSmoke(browser);
    await runOfferComparisonSmoke(browser);
    console.log(
      'UI fixture smoke passed: contextual input reports, exact lost-response replay, stale-409 recovery, 390px layout.',
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
