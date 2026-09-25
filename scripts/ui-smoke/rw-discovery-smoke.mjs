import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// RW-E2 gate: saved-requirements discovery against a deterministic in-file API
// double. Covers wrong/empty saved preferences, contextual Change my search,
// accepted-vs-saved honesty, pending/paused blocks, stale-brief refresh,
// Find jobs commissioned from the NEW saved version with a labeled basis,
// retained results (incl. Unknown with staleBasis) across refresh/return,
// no-result and setup-failure honesty, passive-read silence, and the
// regression that Change-my-search input or a generic prompt alone never
// triggers real search work. Nothing here is a live model run: the correction
// round, brief authoring and research runs are canned state machines, and the
// handoff labels this coverage fixture-verified only.
const webDist = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/vite-app/dist');
const time = '2026-09-24T12:00:00Z';
const session = {
  actorKind: 'administrator',
  actorId: 'synthetic-owner',
  csrfToken: 'synthetic-csrf',
  expiresAt: '2099-01-01T00:00:00Z',
};
const WORDING = 'I want remote-only roles in Berlin, at least 32 hours a week';

// Wrong/empty starting point: Oslo instead of Berlin, 60h instead of 32h,
// remote disallowed, no timezone recorded, and a wrong saved criterion.
const prefsV3 = {
  version: 3,
  preferredLocation: 'Oslo',
  allowRemote: false,
  allowHybrid: false,
  targetHours: '60',
  minMonthlyBaseCents: 200000,
  salaryCurrency: 'EUR',
  timezone: '',
  roleCriteria: [
    {
      id: 'criterion-nights',
      kind: 'hours',
      mode: 'required',
      label: 'Night shifts are fine',
      description: 'The owner supposedly accepts permanent night shifts.',
    },
  ],
};
const prefsV4 = {
  version: 4,
  preferredLocation: 'Berlin',
  allowRemote: true,
  allowHybrid: false,
  targetHours: '32',
  minMonthlyBaseCents: 480000,
  salaryCurrency: 'EUR',
  timezone: 'Europe/Berlin',
  roleCriteria: [
    {
      id: 'criterion-remote',
      kind: 'work_pattern',
      mode: 'required',
      label: 'Remote-first work',
      description: 'The role must allow fully remote work from Berlin.',
    },
    {
      id: 'criterion-hours',
      kind: 'hours',
      mode: 'preferred',
      label: '32-hour week',
      description: 'A four-day or reduced-hours schedule is preferred.',
    },
  ],
};
const briefV3 = {
  profileVersion: 3,
  rubricVersion: 'rubric-1',
  rubricSource: 'codex',
  catalogVersion: 'cat-7',
  requirements: [],
  facts: [{ key: 'timezone', value: '' }],
};
const briefV4 = {
  profileVersion: 4,
  rubricVersion: 'rubric-2',
  rubricSource: 'codex',
  catalogVersion: 'cat-8',
  requirements: [],
  facts: [{ key: 'timezone', value: 'Europe/Berlin' }],
};

function role(id, title, extra = {}) {
  return {
    id,
    companyId: 'synthetic-company-1',
    title,
    kind: 'employment',
    sourceUrl: `https://example.invalid/jobs/${id}`,
    originalText: `Synthetic vacancy text for ${title}; pay and hours unconfirmed.`,
    notes: '',
    stage: 'new',
    workPattern: 'remote',
    locationText: 'Berlin',
    postedOn: '2026-09-01',
    deadlineOn: '',
    revision: 2,
    createdAt: '2026-09-10T09:00:00Z',
    updatedAt: '2026-09-12T09:00:00Z',
    compensation: {},
    ...extra,
  };
}
const roles = [
  role('job-rec', 'Backend Engineer'),
  role('job-old', 'Weekend Project'),
  role('job-unknown', 'Mystery Role'),
];

function findingBase(id, group, basis) {
  return {
    opportunityId: id,
    opportunityRevision: 2,
    group,
    assessmentId: `asmt-${id}`,
    profileVersion: basis.profileVersion,
    rubricVersion: basis.rubricVersion,
    catalogVersion: basis.catalogVersion,
    reasons: [],
    evidenceLinks: [],
    stale: false,
  };
}
const basisV3 = { profileVersion: 3, rubricVersion: 'rubric-1', catalogVersion: 'cat-7' };
const basisV4 = { profileVersion: 4, rubricVersion: 'rubric-2', catalogVersion: 'cat-8' };
const recV3 = {
  ...findingBase('job-rec', 'recommended', basisV3),
  reasons: [
    {
      reasonId: 'r-pos-1',
      kind: 'positive',
      label: 'Remote overlap with the brief',
      detail: 'Posting confirms fully remote work from Berlin.',
      jevSupport: 0.85,
    },
  ],
  evidenceLinks: [
    { captureId: 'cap-rw-1', spanStart: 10, spanEnd: 40, excerptSha256: 'a'.repeat(64) },
  ],
  sourceRef: {
    sourceId: 'career-site',
    sourceRevision: '2026-09-20',
    observedUrl: 'https://example.invalid/posting/backend',
  },
};
const recV4 = {
  ...findingBase('job-rec', 'recommended', basisV4),
  reasons: [
    {
      reasonId: 'r-pos-9',
      kind: 'positive',
      label: 'Remote overlap confirmed again',
      detail: 'New pass confirms fully remote work from Berlin.',
      jevSupport: 0.88,
    },
  ],
  evidenceLinks: [
    { captureId: 'cap-rw-1', spanStart: 10, spanEnd: 40, excerptSha256: 'a'.repeat(64) },
  ],
  sourceRef: {
    sourceId: 'career-site',
    sourceRevision: '2026-09-24',
    observedUrl: 'https://example.invalid/posting/backend',
  },
};
const oldFinding = {
  ...findingBase('job-old', 'probably_not_recommended', basisV3),
  reasons: [
    {
      reasonId: 'r-neg-2',
      kind: 'negative',
      label: 'Weekend hours',
      detail: 'Posting requires weekends.',
      jevSupport: 0.71,
    },
  ],
  stale: true,
  staleBasis: 'brief_changed',
};
const unknownFinding = {
  ...findingBase('job-unknown', 'unknown', basisV3),
  unknownBasis: 'The capture contradicted the listing route; neither source proved the work pattern.',
  stale: true,
  staleBasis: 'catalog_changed',
};
const perRoleFindings = new Map([
  ['job-rec', recV3],
  ['job-old', oldFinding],
  ['job-unknown', unknownFinding],
]);
const runFindings = [recV4, oldFinding, unknownFinding];

const captures = {
  'cap-rw-1': {
    captureId: 'cap-rw-1',
    observedUrl: 'https://example.invalid/posting/backend',
    finalUrl: 'https://example.invalid/posting/backend',
    retrievedAt: time,
    status: 'ok',
    mediaType: 'text/html',
    contentHash: 'abc123',
    extent: { bytes: 1234, complete: true },
  },
};

const allowance = { timeMs: 900000, maxActions: 60, maxJev: 12, maxTurns: 8, maxConcurrent: 2 };
function usageFor(observed) {
  return {
    enforced: { ...allowance },
    reserved: { actions: 0, jev: 0, turns: 0 },
    observed,
    unknown: false,
  };
}
function runView(run) {
  return {
    runId: run.id,
    state: run.state,
    briefVersion: run.briefVersion,
    allowance: { ...allowance },
    usage: usageFor(run.observed),
    investigations:
      run.state === 'completed' ? [] : [{ id: 'inv-1', intent: 'Collect vacancy text', status: 'active' }],
    savedIds: run.savedIds,
    unresolvedCount: run.unresolved,
    ...(run.stopReason === undefined ? {} : { stopReason: run.stopReason }),
  };
}
function reportFor(run) {
  return { runId: run.id, ...run.report, budget: { observed: usageFor(run.observed), unknown: false } };
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': body.length });
  res.end(body);
}
function error(res, status) {
  sendJson(res, status, { error: { message: `RW-discovery fixture HTTP ${status}` } });
}
async function bodyJson(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 100_000) throw new Error('Fixture request too large');
    chunks.push(chunk);
  }
  return JSON.parse(Buffer.concat(chunks).toString() || '{}');
}
async function serveStatic(res, pathname) {
  const file = resolve(webDist, pathname === '/' ? 'index.html' : `.${pathname}`);
  if (!file.startsWith(`${webDist}/`)) return error(res, 404);
  try {
    const bytes = await readFile(file);
    const mime =
      { '.js': 'text/javascript', '.css': 'text/css', '.html': 'text/html', '.svg': 'image/svg+xml' }[
        extname(file)
      ] || 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': mime, 'Content-Length': bytes.length });
    res.end(bytes);
  } catch {
    error(res, 404);
  }
}

async function startFixture() {
  const state = {
    expireSession: true,
    // Correction state machine, advanced by the test: none -> running ->
    // paused -> completed. Preferences read back v4 only at completed.
    correctionPhase: 'none',
    correctionRound: null,
    // The v4 brief is authored only after the test flips this, so the stale
    // window (prefs v4, brief v3) is observable and refreshable.
    briefReady: false,
    briefFails: false,
    emptyResults: false,
    runs: new Map(),
    commissionsByKey: new Map(),
    runSeq: 0,
    requests: [],
  };
  const currentPrefs = () => (state.correctionPhase === 'completed' ? prefsV4 : prefsV3);
  const currentBrief = () => {
    if (currentPrefs().version === 4 && state.briefReady) return briefV4;
    return briefV3;
  };
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    const path = url.pathname;
    try {
      if (!path.startsWith('/api/v1/')) return await serveStatic(res, path);
      const payload = req.method === 'POST' ? await bodyJson(req) : undefined;
      state.requests.push({ method: req.method, path, query: url.search, payload });
      if (path === '/api/v1/auth/session') {
        if (state.expireSession) return error(res, 401);
        return sendJson(res, 200, session);
      }
      if (path === '/api/v1/auth/login' && req.method === 'POST') {
        if (payload?.password !== 'synthetic-owner-password') return error(res, 401);
        return sendJson(res, 200, session);
      }
      if (path === '/api/v1/auth/logout' && req.method === 'POST') {
        res.writeHead(204);
        return res.end();
      }
      if (path === '/api/v1/health')
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'rw-discovery-fixture' });
      if (path === '/api/v1/preferences') return sendJson(res, 200, currentPrefs());
      if (path === '/api/v1/runtime-status')
        return sendJson(res, 200, { ingestionAvailable: false, organisationAvailable: false });
      if (path === '/api/v1/research/brief') {
        if (state.briefFails) return error(res, 500);
        return sendJson(res, 200, currentBrief());
      }
      if (path === '/api/v1/workflow/roles') return sendJson(res, 200, { items: [] });
      if (path === '/api/v1/opportunities') {
        if (state.emptyResults) return sendJson(res, 200, { items: [] });
        return sendJson(res, 200, { items: roles.map((opportunity) => ({ opportunity, likelyDuplicates: [] })) });
      }
      const findingLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/finding$/);
      if (findingLeaf?.[1] !== undefined && req.method === 'GET') {
        if (state.emptyResults) return error(res, 404);
        const entry = perRoleFindings.get(decodeURIComponent(findingLeaf[1]));
        return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
      }
      if (path === '/api/v1/rounds/process-input' && req.method === 'POST') {
        const prefs = currentPrefs();
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          payload.targetKind !== 'profile' ||
          payload.targetId !== 'current' ||
          typeof payload.text !== 'string' ||
          payload.text.trim() === ''
        )
          return error(res, 400);
        if (payload.expectedRevision !== prefs.version) return error(res, 409);
        state.correctionPhase = 'running';
        state.correctionRound = {
          id: 'rw-corr-1',
          requestKey: payload.requestKey,
          baseVersion: prefs.version,
        };
        return sendJson(res, 201, {
          round: {
            id: 'rw-corr-1',
            requestKey: payload.requestKey,
            state: 'running',
            profileVersion: prefs.version,
            originalProfileVersion: prefs.version,
          },
        });
      }
      const roundLeaf = path.match(/^\/api\/v1\/rounds\/([^/]+)$/);
      if (roundLeaf?.[1] !== undefined && req.method === 'GET') {
        if (state.correctionRound === null || decodeURIComponent(roundLeaf[1]) !== state.correctionRound.id)
          return error(res, 404);
        const done = state.correctionPhase === 'completed';
        return sendJson(res, 200, {
          id: state.correctionRound.id,
          requestKey: state.correctionRound.requestKey,
          state: done ? 'completed' : state.correctionPhase,
          profileVersion: done ? 4 : state.correctionRound.baseVersion,
          originalProfileVersion: state.correctionRound.baseVersion,
        });
      }
      if (path === '/api/v1/research/runs' && req.method === 'POST') {
        if (typeof payload?.idempotencyKey === 'string' && state.commissionsByKey.has(payload.idempotencyKey)) {
          return sendJson(res, 200, state.commissionsByKey.get(payload.idempotencyKey));
        }
        state.runSeq += 1;
        const id = `rw-run-${state.runSeq}`;
        const prefs = currentPrefs();
        const brief = currentBrief();
        const empty = state.emptyResults;
        const run = {
          id,
          state: 'running',
          briefVersion: { profileVersion: prefs.version, rubricVersion: brief.rubricVersion },
          savedIds: [],
          unresolved: 0,
          observed: { actions: 1, jev: 0, turns: 1, bytes: 1200 },
          // The commission response plus the mount refresh both read running;
          // the test's explicit Refresh read completes the run.
          readsToComplete: 2,
          empty,
          events: [
            {
              eventId: `ev-${id}-1`,
              at: time,
              kind: 'observation',
              phase: 'collect',
              summary: 'Codex recorded vacancy text from the company career page.',
              refs: { captureId: 'cap-rw-1' },
            },
          ],
          report: null,
        };
        state.runs.set(id, run);
        const view = runView(run);
        if (typeof payload?.idempotencyKey === 'string') state.commissionsByKey.set(payload.idempotencyKey, view);
        return sendJson(res, 201, view);
      }
      const runLeaf = path.match(/^\/api\/v1\/research\/runs\/([^/]+)(\/(activity|report|findings|steer))?$/);
      if (runLeaf?.[1] !== undefined) {
        const run = state.runs.get(decodeURIComponent(runLeaf[1]));
        if (run === undefined) return error(res, 404);
        const leaf = runLeaf[3];
        if (leaf === undefined && req.method === 'GET') {
          if (run.readsToComplete > 0) run.readsToComplete -= 1;
          if (run.readsToComplete === 0 && run.state !== 'completed') {
            run.state = 'completed';
            run.savedIds = run.empty ? [] : roles.map((item) => item.id);
            run.unresolved = run.empty ? 0 : 1;
            run.observed = { actions: 9, jev: 5, turns: 3, bytes: 42100 };
            run.events.push(
              {
                eventId: `ev-${run.id}-2`,
                at: time,
                kind: 'run.saved',
                phase: 'collect',
                summary: run.empty ? 'Saved 0 roles; no findings persisted.' : 'Saved 3 tracked roles.',
                refs: run.empty ? {} : { recordId: 'job-rec' },
              },
              {
                eventId: `ev-${run.id}-3`,
                at: time,
                kind: 'run.observed',
                phase: 'classify',
                summary: run.empty
                  ? 'Jev classified 0 roles: nothing was collected.'
                  : 'Jev classified 1 role against catalog cat-8; 2 retained.',
                refs: { assessmentId: 'asmt-job-rec' },
              },
            );
            run.report = run.empty
              ? {
                  outcomes: ['No roles matched the saved brief; nothing is presented as a result.'],
                  searched: ['company career page'],
                  reused: ['0 prior results reused'],
                  uncertainty: ['The searched source lists no current vacancy.'],
                  nextWork: ['Change the search, then run Find jobs again.'],
                }
              : {
                  outcomes: ['Saved 3 tracked roles for Jev classification.'],
                  searched: ['company career page'],
                  reused: ['2 prior results reused'],
                  uncertainty: ['One role lacks a published salary band.'],
                  nextWork: ['Select a role to start its check.'],
                };
          }
          return sendJson(res, 200, runView(run));
        }
        if (leaf === 'activity' && req.method === 'GET') return sendJson(res, 200, { events: run.events });
        if (leaf === 'report' && req.method === 'GET') {
          if (run.report === null) return error(res, 409);
          return sendJson(res, 200, reportFor(run));
        }
        if (leaf === 'findings' && req.method === 'GET') {
          if (run.empty || state.emptyResults) return sendJson(res, 200, { items: [] });
          return sendJson(res, 200, { items: runFindings });
        }
        if (leaf === 'steer' && req.method === 'POST') return error(res, 400);
        return error(res, 404);
      }
      const capture = path.match(/^\/api\/v1\/research\/captures\/([^/]+)$/);
      if (capture?.[1] !== undefined && req.method === 'GET') {
        const view = captures[decodeURIComponent(capture[1])];
        return view === undefined ? error(res, 404) : sendJson(res, 200, view);
      }
      return error(res, 503);
    } catch (cause) {
      sendJson(res, 500, { error: { message: `RW-discovery fixture error: ${cause.message}` } });
    }
  });
  await new Promise((resolvePromise, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolvePromise);
  });
  const address = server.address();
  return {
    url: `http://127.0.0.1:${address.port}`,
    state,
    close: () =>
      new Promise((resolvePromise, reject) =>
        server.close((err) => (err ? reject(err) : resolvePromise())),
      ),
  };
}

function nonAuthPosts(requests) {
  return requests.filter((item) => item.method === 'POST' && !item.path.startsWith('/api/v1/auth/'));
}
function postsTo(requests, path) {
  return requests.filter((item) => item.method === 'POST' && item.path === path);
}

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
    const verdicts = [];
    const note = (scenario, detail) => verdicts.push(`${scenario}: ${detail}`);
    const requests = () => fixture.state.requests;
    const mySearch = () =>
      page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'My search' });
    const jobsLink = () =>
      page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' });
    const findJobs = () => page.getByRole('button', { name: 'Find jobs', exact: true });

    await page.goto(fixture.url, { waitUntil: 'networkidle' });
    await page.getByLabel('Password').waitFor();
    await page.getByLabel('Password').fill('wrong');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('alert').waitFor();
    fixture.state.expireSession = false;
    await page.getByLabel('Password').fill('synthetic-owner-password');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('navigation', { name: 'Primary' }).waitFor();

    // Wrong/empty saved preferences render read-only; passive mount starts
    // nothing and offers no generic prompt as the primary entry.
    await mySearch().click();
    await page.getByRole('heading', { name: 'My search', exact: true }).waitFor();
    await page.getByText('Oslo').first().waitFor();
    await page.getByText('Profile version 3').first().waitFor();
    await page.getByText('Not recorded').first().waitFor();
    await page.getByText('Night shifts are fine').first().waitFor();
    await page.getByText('Profile v3 · rubric-1').waitFor();
    await page.getByText('Searches with profile v3 (rubric-1).').waitFor();
    assert.equal(await page.getByText('Recruitment intent (optional)').count(), 0);
    assert.equal(await page.getByText('What should the agent look for?').count(), 0);
    assert.equal(
      nonAuthPosts(requests()).length,
      0,
      `mount must commission no work: ${JSON.stringify(nonAuthPosts(requests()))}`,
    );
    note('wrong/empty-read', 'v3 Oslo/60h/unrecorded-timezone render, zero POSTs');

    // Setup failure: a 500 brief blocks both surfaces honestly without
    // erasing saved findings, and retry recovers.
    fixture.state.briefFails = true;
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Could not load search preferences').waitFor();
    await jobsLink().click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Search brief versions unavailable:').waitFor();
    await page.getByText('Saved findings below are unaffected.').waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Brief profile v3 · rubric rubric-1 · catalog cat-7').first().waitFor();
    assert.equal(await page.getByText('Brief profile v3 · rubric rubric-1 · catalog cat-7').count(), 3);
    await mySearch().click();
    await page.getByText('Could not load search preferences').waitFor();
    assert.equal(nonAuthPosts(requests()).length, 0, 'setup failure must commission no work');
    fixture.state.briefFails = false;
    const retries = page.getByRole('button', { name: 'Try again' });
    assert.equal(await retries.count(), 2);
    await retries.first().click();
    await retries.last().click();
    await page.getByText('Profile version 3').first().waitFor();
    await page.getByText('Searches with profile v3 (rubric-1).').waitFor();
    note('setup-failure', 'brief 500 blocks honestly on both surfaces, retry recovers');

    // Contextual correction: a field Correct button prefills the draft and
    // the Change my search link lands focus in the correction box.
    await page.getByRole('button', { name: 'Correct preferred location' }).click();
    assert.match(
      await page.getByLabel('Describe the correction in your own words').inputValue(),
      /About preferred location \(currently "Oslo"\): /,
    );
    await page.getByRole('link', { name: 'Change my search' }).click();
    await page.waitForFunction(() => document.activeElement?.id === 'change-search-text');
    note('contextual-correction', 'field prefill + Change-my-search focus land in the box');

    // REGRESSION: typed Change-my-search input and a filled generic run note,
    // without any explicit action, commission nothing.
    await page.getByLabel('Describe the correction in your own words').fill(WORDING);
    await page.locator('summary', { hasText: 'Extra note for this run only' }).click();
    await page.getByLabel('Extra note for this run only (optional)').fill('Generic prompt: find me anything remote');
    await page.waitForTimeout(600);
    assert.equal(
      nonAuthPosts(requests()).length,
      0,
      `typed input alone must commission no work: ${JSON.stringify(nonAuthPosts(requests()))}`,
    );
    await page.getByLabel('Extra note for this run only (optional)').fill('');
    note('regression/no-prompt-search', 'draft + generic note alone commission zero POSTs');

    // Accepted-vs-saved honesty: sending shows accepted, never saved; the
    // correction commissions no search work either.
    await page.getByRole('button', { name: 'Send correction' }).click();
    await page.getByText('Accepted — saving your change…').waitFor();
    await page.getByText('Accepted, not yet saved.').waitFor();
    assert.equal(await page.getByText('Saved as profile version').count(), 0);
    const corrections = postsTo(requests(), '/api/v1/rounds/process-input');
    assert.equal(corrections.length, 1);
    assert.equal(corrections[0].payload.targetKind, 'profile');
    assert.equal(corrections[0].payload.targetId, 'current');
    assert.equal(corrections[0].payload.expectedRevision, 3);
    assert.equal(corrections[0].payload.text, WORDING);
    assert.match(corrections[0].payload.requestKey, /^profile-correction-v3-/);
    assert.equal(postsTo(requests(), '/api/v1/research/runs').length, 0);
    const pendingKey = await page.evaluate(() => window.localStorage.getItem('jobseek.owner-context-pending'));
    assert.match(pendingKey ?? '', /rw-corr-1/);
    note('accepted-not-saved', 'send shows accepted; 1 process-input POST, 0 research POSTs');

    // Pending blocks Find jobs after the pending state is restored (reload):
    // restore polls with GETs only and commissions nothing new.
    const postsBeforePendingReload = nonAuthPosts(requests()).length;
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Accepted — saving your change…').waitFor();
    await page.getByText(/Saving your change \(running\)/).waitFor();
    assert.equal(await findJobs().isDisabled(), true);
    assert.equal(nonAuthPosts(requests()).length, postsBeforePendingReload, 'pending restore must be read-only');
    note('pending-blocks', 'reloaded pending blocks Find jobs, read-only restore');

    // Paused blocks too: the manual check surfaces paused, and the block
    // survives reload.
    fixture.state.correctionPhase = 'paused';
    await page.getByRole('button', { name: 'Check again' }).click();
    await page.getByText('Paused — your accepted change is waiting').waitFor();
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Paused — your accepted change is waiting').waitFor();
    await page.getByText('Your accepted change is paused. Check its status on My search before finding jobs.').waitFor();
    assert.equal(await findJobs().isDisabled(), true);
    assert.equal(postsTo(requests(), '/api/v1/research/runs').length, 0);
    note('paused-blocks', 'paused correction blocks Find jobs across reload');

    // Completion saves: the banner names the read-back version, the saved
    // context updates, and the now-stale brief blocks with a refresh action.
    fixture.state.correctionPhase = 'completed';
    await page.getByRole('button', { name: 'Check again' }).click();
    await page.getByText('Saved as profile version 4.').waitFor();
    await page.getByText('Profile version 4').first().waitFor();
    await page.getByText('Berlin').first().waitFor();
    await page.getByText('Remote-first work').first().waitFor();
    await page.getByText('The saved brief is behind profile v4. Refresh before starting a run.').waitFor();
    assert.equal(await findJobs().isDisabled(), true);
    assert.equal(postsTo(requests(), '/api/v1/research/runs').length, 0);
    const pendingAfterSave = await page.evaluate(() =>
      window.localStorage.getItem('jobseek.owner-context-pending'),
    );
    assert.equal(pendingAfterSave, null);
    note('saved-stale', 'saved v4 read back; stale brief blocks with refresh');

    // Saved reload shows the updated context and commissions nothing.
    const postsBeforeSavedReload = nonAuthPosts(requests()).length;
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Profile version 4').first().waitFor();
    await page.getByText('Berlin').first().waitFor();
    await page.getByText('The saved brief is behind profile v4. Refresh before starting a run.').waitFor();
    assert.equal(await page.getByText('Saved as profile version').count(), 0);
    assert.equal(await findJobs().isDisabled(), true);
    assert.equal(nonAuthPosts(requests()).length, postsBeforeSavedReload, 'saved reload must commission no work');
    note('saved-reload', 'v4 context persists across reload, zero new POSTs');

    // Refresh authorizes the new brief: Find jobs unlocks with the new basis
    // labeled, then commissions exactly once from that version.
    fixture.state.briefReady = true;
    await page.getByRole('button', { name: 'Refresh saved brief' }).click();
    await page.getByText('Profile v4 · rubric-2').waitFor();
    await page.getByText('Reason catalog cat-8 ready.').waitFor();
    await page.getByText('Searches with profile v4 (rubric-2).').waitFor();
    assert.equal(await findJobs().isEnabled(), true);
    await findJobs().click();
    await page.getByText('Running — research is underway.').waitFor();
    const commissions = postsTo(requests(), '/api/v1/research/runs');
    assert.equal(commissions.length, 1);
    assert.ok(
      typeof commissions[0].payload?.idempotencyKey === 'string' && commissions[0].payload.idempotencyKey !== '',
      'commission carries an idempotency key',
    );
    assert.ok(commissions[0].payload?.allowance !== undefined, 'commission carries the allowance');
    assert.equal(commissions[0].payload?.briefText, undefined, 'saved version is the basis, not a prompt');
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.getByText('Completed — outcomes and report are saved.').waitFor();
    await page.getByText('Saved 3 tracked roles for Jev classification.').waitFor();
    await page.getByText('Brief v4 (rubric-2)').waitFor();
    assert.equal(
      requests().filter((item) => item.path.includes('/checks')).length,
      0,
      'discovery must start no selected-job check',
    );
    note('find-jobs-new-version', '1 commission from v4/rubric-2, basis labeled, zero check traffic');

    // Jobs: the new-basis result plus retained stale findings incl. Unknown;
    // explanations open with zero new requests.
    await jobsLink().click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Search brief: profile v4 · rubric rubric-2 · catalog cat-8').waitFor();
    await page.getByText('Tracked research run: rw-run-1').waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Probably not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Unknown — exceptional, needs a basis (1)', { exact: true }).waitFor();
    await page.getByText('Brief profile v4 · rubric rubric-2 · catalog cat-8').waitFor();
    await page.getByText('Stale — brief_changed').waitFor();
    await page.getByText('Stale — catalog_changed').waitFor();
    const requestsBeforeWhy = requests().length;
    const whyButtons = page.getByRole('button', { name: 'Why this job' });
    assert.equal(await whyButtons.count(), 3);
    for (let index = 0; index < 3; index += 1) {
      await page.getByRole('button', { name: 'Why this job' }).first().click();
    }
    await page.getByText('Strength: Remote overlap confirmed again').waitFor();
    await page.getByText('Jev could not place this role: The capture contradicted the listing route; neither source proved the work pattern.').waitFor();
    assert.equal(requests().length, requestsBeforeWhy, 'explanation opens must make no request');
    note('retained-results', 'v4 result + retained stale/Unknown, zero-request explanations');

    // Retention across refresh and return: groups, bases and stale labels
    // survive a reload and a navigation round-trip.
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Unknown — exceptional, needs a basis (1)', { exact: true }).waitFor();
    await page.getByText('Stale — brief_changed').waitFor();
    await page.getByText('Stale — catalog_changed').waitFor();
    await mySearch().click();
    await page.getByText('Completed — outcomes and report are saved.').waitFor();
    await jobsLink().click();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Brief profile v4 · rubric rubric-2 · catalog cat-8').waitFor();
    await page.getByText('Stale — brief_changed').waitFor();
    await page.getByText('Stale — catalog_changed').waitFor();
    note('refresh-return', 'groups + stale labels retained across refresh/return');

    // Find more jobs commissions a second tracked run and retains everything.
    await mySearch().click();
    await page.getByRole('button', { name: 'Find more jobs' }).click();
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.getByText('Completed — outcomes and report are saved.').waitFor();
    const trackedAfterMore = await page.evaluate(() => window.localStorage.getItem('jobseek.research-run-id'));
    assert.equal(trackedAfterMore, 'rw-run-2');
    const commissionsAfterMore = postsTo(requests(), '/api/v1/research/runs');
    assert.equal(commissionsAfterMore.length, 2);
    assert.notEqual(
      commissionsAfterMore[0].payload.idempotencyKey,
      commissionsAfterMore[1].payload.idempotencyKey,
    );
    await jobsLink().click();
    await page.getByText('Tracked research run: rw-run-2').waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Probably not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Unknown — exceptional, needs a basis (1)', { exact: true }).waitFor();
    await page.getByText('Stale — brief_changed').waitFor();
    await page.getByText('Stale — catalog_changed').waitFor();
    note('find-more', 'second commission with a fresh key, all groups retained');

    // No-result honesty in a fresh context: an explicit run that collects
    // nothing reports exactly that, with an honest empty Jobs surface.
    fixture.state.emptyResults = true;
    const emptyContext = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const emptyPage = await emptyContext.newPage();
    emptyPage.setDefaultTimeout(20_000);
    emptyPage.on('pageerror', (cause) => pageErrors.push(cause.message));
    const emptyPostsBefore = nonAuthPosts(requests()).length;
    const researchBefore = postsTo(requests(), '/api/v1/research/runs').length;
    await emptyPage.goto(`${fixture.url}#/search`, { waitUntil: 'networkidle' });
    // The double's session is already live, so the fresh context lands
    // signed in with no login form.
    await emptyPage.getByText('Searches with profile v4 (rubric-2).').waitFor();
    assert.equal(nonAuthPosts(requests()).length, emptyPostsBefore, 'empty-context reads commission no work');
    await emptyPage.getByRole('button', { name: 'Find jobs', exact: true }).click();
    await emptyPage.getByText('Running — research is underway.').waitFor();
    await emptyPage.getByRole('button', { name: 'Refresh', exact: true }).click();
    await emptyPage.getByText('No roles matched the saved brief; nothing is presented as a result.').waitFor();
    assert.equal(postsTo(requests(), '/api/v1/research/runs').length, researchBefore + 1);
    await emptyPage
      .getByRole('navigation', { name: 'Primary' })
      .getByRole('link', { name: 'Jobs' })
      .click();
    await emptyPage.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await emptyPage.getByText('No jobs tracked yet').waitFor();
    await emptyContext.close();
    note('no-result', 'empty run reports honestly, empty Jobs surface, 1 commission');

    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);
    console.log(`rw-discovery smoke OK: ${verdicts.join(' | ')}`);
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('rw-discovery smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('rw-discovery smoke failed:', cause);
  process.exitCode = 1;
});

