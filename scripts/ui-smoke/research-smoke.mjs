import assert from 'node:assert/strict';
import { execFileSync, spawn } from 'node:child_process';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import {
  assertKeychainsUntouched,
  launchSilentBrowser,
  snapshotKeychains,
} from './browser.mjs';

// T27 research/product acceptance smoke (lane A). Drives the real built web
// bundle in silent headless Chromium against the real T23 wired backend
// (apps/api/cmd/researchsmoke): a disposable store, controlled fixture
// sources and a scripted zero-spend Jev provider. Agent tool calls run
// through the real supervisor/executor/memory/Jev/identity/save backends
// via the helper's /test/agent/* endpoints; no live model drives turns
// here (live behavior is the T30 canary). Non-research rounds commission
// against the real store/HTTP with a disclosed no-op execution worker, so
// correction intake and pack preparation verify durability, not runner
// completion.
//
// Two isolated backend universes run in sequence: the results run (with
// handoff and corrections) and the zero-result run. The single active
// slot — paused rounds included — admits one research run per universe;
// production frees the slot through runner completion (T29/T30), which no
// smoke can claim without the runner.
//
// Run from the dashboard root after building the web bundle:
//   pnpm --filter @jobseek/web build && node scripts/ui-smoke/research-smoke.mjs
// or: pnpm e2e:research

const smokeDir = dirname(fileURLToPath(import.meta.url));
const dashboardRoot = resolve(smokeDir, '../..');
const apiDir = join(dashboardRoot, 'apps/api');
const webDist = join(dashboardRoot, 'apps/web/dist');

function apiJson(page, path) {
  return page.evaluate(async (target) => {
    const response = await fetch(target, {
      credentials: 'same-origin',
      headers: { Accept: 'application/json' },
    });
    if (!response.ok) throw new Error(`GET ${target}: HTTP ${response.status}`);
    return response.json();
  }, path);
}

async function testPost(baseUrl, path, payload) {
  const response = await fetch(`${baseUrl}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
  });
  const body = await response.json();
  if (!response.ok) throw new Error(`${path}: HTTP ${response.status}: ${JSON.stringify(body)}`);
  if (body.error) throw new Error(`${path}: ${body.error}`);
  return body;
}

async function testState(baseUrl, runId) {
  const response = await fetch(`${baseUrl}/test/state?runId=${encodeURIComponent(runId)}`);
  const body = await response.json();
  if (!response.ok) throw new Error(`/test/state: ${JSON.stringify(body)}`);
  return body;
}

async function startHelper(helperBin) {
  const helper = spawn(helperBin, [], {
    cwd: apiDir,
    env: { ...process.env, RESEARCHSMOKE_WEB_DIST: webDist },
    stdio: ['ignore', 'pipe', 'inherit'],
  });
  const firstLine = await new Promise((resolveLine, rejectLine) => {
    let buffered = '';
    const timer = setTimeout(
      () => rejectLine(new Error('researchsmoke produced no URL line')),
      60_000,
    );
    helper.stdout.on('data', (chunk) => {
      buffered += chunk.toString();
      const end = buffered.indexOf('\n');
      if (end >= 0) {
        clearTimeout(timer);
        resolveLine(buffered.slice(0, end));
      }
    });
    helper.on('error', rejectLine);
    helper.on('exit', (code) => rejectLine(new Error(`researchsmoke exited early: ${code}`)));
  });
  const { url, password } = JSON.parse(firstLine);
  console.log(`researchsmoke serving ${url}`);
  return { helper, url, password };
}

async function stopHelper(helper) {
  if (!helper || helper.exitCode !== null || helper.signalCode) return;
  await new Promise((done) => {
    helper.on('exit', () => done(undefined));
    helper.kill('SIGTERM');
    setTimeout(() => {
      helper.kill('SIGKILL');
      done(undefined);
    }, 5000);
  });
}

function researchTools(page, pageErrors) {
  const research = page.getByRole('region', { name: 'Autonomous research run' });
  const failOnErrors = () => {
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);
  };
  // Activity pages oldest-first in 25s; page forward until the expected
  // line renders (this also covers the Load-more path). The 5s status
  // poller can replace the button mid-click, so paging tolerates
  // detachment and re-queries every attempt.
  async function pageActivityForward() {
    const more = research.getByRole('button', { name: 'Load more activity' });
    if ((await more.count()) === 0) return false;
    try {
      await more.first().click({ timeout: 3000 });
    } catch {
      /* Detached by a poll refresh; the page may still have applied. */
    }
    await new Promise((done) => setTimeout(done, 400));
    return true;
  }
  async function revealActivity(text) {
    for (let turn = 0; turn < 8; turn += 1) {
      const line = research.getByText(text);
      if ((await line.count()) > 0) {
        await line.first().scrollIntoViewIfNeeded();
        return;
      }
      if (!(await pageActivityForward())) break;
    }
    await research.getByText(text).waitFor();
  }
  return { research, failOnErrors, pageActivityForward, revealActivity };
}

async function login(page, url, password) {
  await page.goto(url, { waitUntil: 'domcontentloaded' });
  await page.getByLabel('Administrator password').fill(password);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.getByRole('heading', { name: 'Your campaign' }).waitFor();
}

// Universe 1: results run — Start/Steer/Stop/Resume, acknowledged
// corrections, reconnect, true counts/costs, evidence and retained
// workflow handoff.
async function universeResults(browser, url, password, pageErrors) {
  const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  page.on('pageerror', (cause) => pageErrors.push(String(cause?.message || cause)));
  const { research, failOnErrors, pageActivityForward, revealActivity } = researchTools(
    page,
    pageErrors,
  );
  await login(page, url, password);
  await research.getByRole('button', { name: 'Start research run' }).waitFor();

  // Product flow: conversational intent only — no structured company,
  // role, source or query form, and no source toggles.
  assert.match(await research.innerText(), /allowance is finite/);
  assert.equal(await research.locator('input[type="text"], input:not([type]), select').count(), 0);
  assert.equal(await research.locator('input[type="checkbox"], input[type="radio"]').count(), 0);
  assert.equal(await research.locator('textarea').count(), 1);

  await research.getByLabel('Recruitment intent (optional)').fill('Find backend roles in Berlin.');
  await research.getByRole('button', { name: 'Start research run' }).click();
  await research.getByText('Running — research is underway.').waitFor();
  assert.match(await research.innerText(), /Brief v\d+ \(.+\)/);
  const runId = await page.evaluate(() => localStorage.getItem('jobseek.research-run-id'));
  assert.ok(runId && runId.length > 0, 'run id persisted for reload recovery');
  let state = await testState(url, runId);
  assert.equal(state.commissioned, 1, 'exactly one commission');
  assert.equal(state.state, 'running');

  // Agent work through the real backends: fetch x2, assess, match,
  // converging saves, read-only API POST save, 404 negative, exact reuse.
  const agent = await testPost(url, '/test/agent/research', { runId });
  assert.ok(agent.opportunity1 && agent.opportunity2 && agent.opportunity1 !== agent.opportunity2);
  assert.equal(agent.reused, true);
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  await research.getByText('Captured').first().waitFor();
  state = await testState(url, runId);
  const activityText = await research.locator('.research-activity').innerText();
  for (const line of [
    'Run commissioned for agent',
    'Claimed exact request',
    'Captured',
    'Observed',
    'Saved 2 records to the run checkpoint',
    'Reused prior result',
  ]) {
    assert.match(
      activityText,
      new RegExp(line.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')),
      `activity shows: ${line}`,
    );
  }
  // True counts and costs: the UI projections equal backend truth.
  const countsText = await research.locator('.research-counts').innerText();
  assert.match(countsText, new RegExp(`Saved records: ${state.savedIds.length}`));
  assert.match(countsText, new RegExp(`Unresolved findings: ${state.unresolved}`));
  assert.match(
    countsText,
    new RegExp(`Actions observed ${state.observedActions}, reserved ${state.reservedActions}`),
  );
  assert.equal(countsText.includes('plus unknown usage'), state.unknown === true);
  const runView = await apiJson(page, `/api/v1/research/runs/${runId}`);
  assert.equal(runView.savedIds.length, state.savedIds.length);
  assert.ok(runView.savedIds.includes(agent.opportunity1));
  assert.ok(runView.savedIds.includes(agent.opportunity2));
  assert.equal(runView.usage.observed.actions, state.observedActions);
  const report = await apiJson(page, `/api/v1/research/runs/${runId}/report`);
  assert.equal(report.outcomes.length, state.savedIds.length);
  assert.ok(report.searched.length > 0, 'report names searched operations');
  assert.ok(report.reused.length > 0, 'report names reused results');
  failOnErrors();

  // Evidence: the saved roles carry cited facts, fit/unknowns, source
  // history and live identity explanations — no second funnel. The saved
  // list loads on mount, so reload to pick up the agent's saves.
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Your campaign' }).waitFor();
  await page.getByRole('button', { name: 'Senior Backend Engineer' }).first().waitFor();
  assert.equal(await page.getByRole('button', { name: 'Support Engineer' }).count(), 1);
  // Only the assessment-free save is badged unassessed (D stage rule:
  // assessment-backed saves are discovered).
  assert.equal(await page.locator('.op-list .op-fit-badge').count(), 1);
  await page.getByRole('button', { name: 'Senior Backend Engineer' }).first().click();
  await page.getByRole('heading', { name: 'Senior Backend Engineer' }).waitFor();
  assert.match(await page.getByText('REQ-NW-101').first().innerText(), /REQ-NW-101/);
  const identity = page.getByRole('region', { name: 'Research identity and sightings' });
  await identity.getByText('Same record — matched to a saved role').waitFor();
  assert.match(await identity.innerText(), new RegExp(agent.assessment1));
  assert.match(await identity.innerText(), new RegExp(`${agent.sightings1} research sightings recorded\\.`));
  await identity.getByRole('button', { name: agent.opportunity1 }).click();
  await page.getByRole('heading', { name: 'Senior Backend Engineer' }).waitFor();
  const evidence = page.getByRole('region', { name: 'Qualification and evidence' });
  assert.match(await evidence.innerText(), /Fit judgment: Assessed/);
  assert.match(await evidence.innerText(), /Overall: unresolved/);
  assert.match(await evidence.innerText(), /Open unknowns:/);
  assert.equal(await page.locator('.op-fit-badge').count(), 0, 'assessed role carries no badge');
  failOnErrors();

  // The assessment-free save stays explicitly unassessed: badged on the
  // card and the detail, with an unresolved identity explanation and no
  // supporting assessment.
  await page.getByRole('button', { name: '← Back to opportunities' }).click();
  await page.getByRole('button', { name: 'Support Engineer' }).click();
  await page.getByRole('heading', { name: 'Support Engineer' }).waitFor();
  assert.equal(await page.locator('.op-fit-badge').count() > 0, true, 'unassessed badge shown');
  const identity2 = page.getByRole('region', { name: 'Research identity and sightings' });
  await identity2.getByText('Unresolved — needs owner judgment').waitFor();
  assert.match(await identity2.innerText(), /no recorded identity decision for this subject/);
  assert.match(await identity2.innerText(), /No supporting assessment was recorded\./);
  assert.match(await identity2.innerText(), /2 research sightings recorded\./);
  failOnErrors();

  // Steering is an acknowledged correction: ack in the UI, durable
  // journal event, idempotent under the same key.
  await page.getByRole('button', { name: '← Back to opportunities' }).click();
  await research.getByLabel('Steer the run').fill('Prefer remote-friendly roles.');
  await research.getByRole('button', { name: 'Send steering message' }).click();
  await research.getByText(/Message (applied|acknowledged|pending)\./).waitFor();
  console.log(`steering ack: ${await research.getByText(/Message (applied|acknowledged|pending)\./).innerText()}`);
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  await revealActivity('Steering message received (revision 1)');
  const steerReplay = await page.evaluate(async (id) => {
    const session = await (await fetch('/api/v1/auth/session')).json();
    const send = (key) =>
      fetch(`/api/v1/research/runs/${id}/steer`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': session.csrfToken },
        body: JSON.stringify({ body: 'Prefer remote-friendly roles.', idempotencyKey: key }),
      }).then((r) => r.json());
    const first = await send('smoke-steer-replay');
    const second = await send('smoke-steer-replay');
    return { first, second };
  }, runId);
  assert.equal(steerReplay.second.revision, steerReplay.first.revision);
  assert.equal(steerReplay.second.messageId, steerReplay.first.messageId);

  // Stop fences through the supervisor: paused state, stop reason,
  // journaled stop, and a stale generation can no longer dispatch.
  const genBeforeStop = (await testState(url, runId)).generation;
  await research.getByRole('button', { name: 'Stop', exact: true }).click();
  await research.getByText('Paused — resume continues with the remaining allowance.').waitFor();
  assert.match(await research.innerText(), /stopped:/);
  state = await testState(url, runId);
  assert.equal(state.state, 'paused');
  assert.ok(state.generation !== genBeforeStop, 'stop rotated the control generation');
  assert.equal(state.journalKinds['run.stopped'], 1);
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  await revealActivity('Run stopped (generation');
  const fenceResponse = await fetch(`${url}/test/agent/dispatch`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      runId,
      generation: genBeforeStop,
      key: 'smoke-fence-1',
      path: '/roles/1-cross',
    }),
  });
  const fenced = await fenceResponse.json();
  assert.equal(fenced.outcome, 'error');
  assert.match(fenced.error, /stopped|fenced|paused|generation/i);
  failOnErrors();

  // Resume continues on remaining allowance with a fresh generation.
  await research.getByRole('button', { name: 'Resume', exact: true }).click();
  await research.getByText('Running — research is underway.').waitFor();
  state = await testState(url, runId);
  assert.equal(state.state, 'running');
  const resumed = await testPost(url, '/test/agent/dispatch', {
    runId,
    generation: state.generation,
    key: 'smoke-resume-1',
    path: '/roles/1-cross',
  });
  assert.ok(['ok', 'reused'].includes(resumed.outcome), `post-resume dispatch: ${resumed.outcome}`);
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  await revealActivity('Stored conversation resumed');

  // Reconnect: reload recovers the run without commissioning work,
  // without duplicating activity, and polling stays read-only.
  await page.reload({ waitUntil: 'domcontentloaded' });
  await research.getByText('Running — research is underway.').waitFor();
  assert.equal(await page.evaluate(() => localStorage.getItem('jobseek.research-run-id')), runId);
  for (let turn = 0; turn < 8; turn += 1) {
    if (!(await pageActivityForward())) break;
  }
  const activityPage = await apiJson(page, `/api/v1/research/runs/${runId}/activity?limit=100`);
  assert.equal(activityPage.nextCursor ?? '', '', 'test journal fits one page');
  assert.equal(
    await research.locator('.research-activity li').count(),
    activityPage.events.length,
    'activity rendered exactly once per journaled event',
  );
  state = await testState(url, runId);
  assert.equal(state.commissioned, 1, 'reload commissioned no work');
  await new Promise((done) => setTimeout(done, 6500));
  state = await testState(url, runId);
  assert.equal(state.commissioned, 1, 'status polling commissioned no work');
  failOnErrors();

  // Retained workflow handoff: the research-saved role flows directly
  // into selection and pack preparation — the opportunity list stays the
  // only surface. The single active slot means preparation explicitly
  // replaces the paused results run; the replaced run then renders its
  // honest report in the research panel.
  await research.getByRole('button', { name: 'Stop', exact: true }).click();
  await research.getByText('Paused — resume continues with the remaining allowance.').waitFor();
  await page.getByRole('button', { name: 'Senior Backend Engineer' }).first().click();
  await page.locator('.owner-decision').getByRole('button', { name: 'Select', exact: true }).click();
  await page.locator('.owner-decision').getByText('Owner decision: selected').waitFor();
  const preparation = page.getByRole('region', { name: 'Application preparation' });
  await preparation
    .getByRole('button', { name: 'End paused research run round and prepare this application' })
    .click();
  await preparation.getByRole('button', { name: 'Stop preparation' }).waitFor();
  const activePrep = await apiJson(page, '/api/v1/rounds/active');
  assert.equal(activePrep.outcome, 'prepare');
  const replacedRun = await apiJson(page, `/api/v1/rounds/${runId}`);
  assert.equal(replacedRun.state, 'failed');
  assert.equal(replacedRun.stopReason, 'replaced_by_owner_input');
  await page.getByRole('button', { name: '← Back to opportunities' }).click();
  await research.getByText('Failed — see the report for what is known.').waitFor();
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  assert.match(await research.innerText(), /Senior Backend Engineer/);
  assert.match(await research.innerText(), /Support Engineer/);
  assert.match(await research.innerText(), /Searched 2 sources, reused \d+ results\./);
  assert.match(await research.innerText(), /Stop reason: replaced_by_owner_input\./);
  failOnErrors();

  // Owner corrections: gated while preparation is active, then
  // acknowledged through the same replace-paused slot, durable across
  // reload, and stopped cleanly.
  await page.getByRole('button', { name: 'Senior Backend Engineer' }).first().click();
  const identityRegion = page.getByRole('region', { name: 'Research identity and sightings' });
  await identityRegion.getByRole('button', { name: 'Question this identity decision' }).click();
  const correction = identityRegion.getByRole('region', { name: 'Owner instruction' });
  await correction.getByRole('textbox').fill('Northwind is actually two companies.');
  await correction.getByText(/Agency work: prepare · /).waitFor();
  const correctionSubmit = correction.getByRole('button', { name: 'Update this opportunity' });
  assert.equal(await correctionSubmit.isDisabled(), true, 'correction gated while work is active');
  assert.match(
    await correction.innerText(),
    /Current work must finish or be stopped before starting different work\./,
  );
  await preparation.getByRole('button', { name: 'Stop preparation' }).click();
  await preparation.getByRole('button', { name: 'Resume and check uncertain work' }).waitFor();
  await correction.getByRole('button', { name: 'End paused work and update this opportunity' }).click();
  await correction.getByText('Request accepted. The agency is handling this input.').waitFor();
  const correctionRound = await apiJson(page, '/api/v1/rounds/active');
  assert.equal(correctionRound.outcome, 'process_input');
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.getByRole('heading', { name: 'Senior Backend Engineer' }).waitFor();
  const identityRegionAfter = page.getByRole('region', { name: 'Research identity and sightings' });
  const correctionAfter = identityRegionAfter.getByRole('region', { name: 'Owner instruction' });
  await identityRegionAfter.getByRole('button', { name: 'Question this identity decision' }).click();
  await correctionAfter.getByText(/Agency work: process input · (queued|running)/).waitFor();
  await correctionAfter.getByRole('button', { name: 'Stop current work' }).click();
  await correctionAfter.getByText(/Agency work: process input · paused/).waitFor();
  failOnErrors();
  await context.close();
}

// Universe 2: zero-result reporting — a run whose investigation yields
// nothing reports honest zeros with the searched work still visible.
async function universeZero(browser, url, password, pageErrors) {
  const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  page.on('pageerror', (cause) => pageErrors.push(String(cause?.message || cause)));
  const { research, failOnErrors } = researchTools(page, pageErrors);
  await login(page, url, password);
  await research.getByRole('button', { name: 'Start research run' }).waitFor();
  await research.getByLabel('Recruitment intent (optional)').fill('Find support roles in Amsterdam.');
  await research.getByRole('button', { name: 'Start research run' }).click();
  await research.getByText('Running — research is underway.').waitFor();
  const runId = await page.evaluate(() => localStorage.getItem('jobseek.research-run-id'));
  assert.ok(runId && runId.length > 0);
  const zero = await testPost(url, '/test/agent/zero-result', { runId });
  assert.deepEqual(zero.saved, []);
  await research.getByRole('button', { name: 'Refresh', exact: true }).click();
  await research.getByText('Observed').first().waitFor();
  const zeroCounts = await research.locator('.research-counts').innerText();
  assert.match(zeroCounts, /Saved records: 0/);
  assert.match(zeroCounts, /Unresolved findings: 0/);
  const zeroReport = await apiJson(page, `/api/v1/research/runs/${runId}/report`);
  assert.equal(zeroReport.outcomes.length, 0);
  assert.ok(zeroReport.searched.length > 0, 'zero-result report still names searched work');
  await research.getByRole('button', { name: 'Stop', exact: true }).click();
  await research.getByText('Paused — resume continues with the remaining allowance.').waitFor();
  failOnErrors();

  await page.setViewportSize({ width: 390, height: 844 });
  const width = await page.evaluate(() => ({
    client: document.documentElement.clientWidth,
    scroll: document.documentElement.scrollWidth,
  }));
  assert.equal(width.client, 390);
  assert.ok(width.scroll <= width.client, `horizontal overflow: ${width.scroll} > ${width.client}`);
  await context.close();
}

async function run() {
  const helperBin = join(mkdtempSync(join(tmpdir(), 'researchsmoke-')), 'researchsmoke');
  console.log('Building researchsmoke helper…');
  execFileSync('go', ['build', '-o', helperBin, './cmd/researchsmoke'], {
    cwd: apiDir,
    stdio: 'inherit',
  });
  const keychainsBefore = snapshotKeychains();
  const pageErrors = [];
  let browser;
  let first;
  let second;
  try {
    browser = await launchSilentBrowser();
    first = await startHelper(helperBin);
    await universeResults(browser, first.url, first.password, pageErrors);
    await stopHelper(first.helper);
    first = null;
    second = await startHelper(helperBin);
    await universeZero(browser, second.url, second.password, pageErrors);
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);
    assertKeychainsUntouched(keychainsBefore);
    console.log(
      'Research smoke passed: Start/Steer/Stop/Resume, acknowledged corrections, reconnect, ' +
        'true counts/costs, evidence, zero-result reporting and retained workflow handoff against ' +
        'the real wired backend. Keychain silent.',
    );
  } finally {
    const cleanup = await Promise.allSettled([
      browser?.close(),
      (async () => {
        await stopHelper(first?.helper);
        await stopHelper(second?.helper);
      })(),
    ]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('Research smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('Research smoke failed:', cause);
  process.exitCode = 1;
});
