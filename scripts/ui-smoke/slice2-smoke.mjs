import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// Slice 2 gate (G2): real discovery and selection through the UI against a
// deterministic in-file API double. The shared fixture.mjs only serves the
// slice-1 surfaces (one synthetic role, no research/findings/brief/workflow
// routes), so this smoke stands up its own server with representative
// vacancy evidence: seven roles across the four Jev groups plus Unknown and
// unclassified, saved reason text, a search brief with a catalog version,
// journaled Codex/Jev activity and guarded owner decisions. Nothing here is
// a live model run: commission/classification activity is canned journal
// data, and the handoff distinguishes this deterministic coverage from the
// real-run evidence required by C1.
const webDist = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/vite-app/dist');
const time = '2026-09-24T12:00:00Z';
const session = {
  actorKind: 'administrator',
  actorId: 'synthetic-owner',
  csrfToken: 'synthetic-csrf',
  expiresAt: '2099-01-01T00:00:00Z',
};
const profile = {
  version: 3,
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
const brief = {
  profileVersion: 3,
  rubricVersion: 'rubric-1',
  rubricSource: 'codex',
  catalogVersion: 'cat-7',
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
  role('job-could', 'Support Engineer'),
  role('job-prob', 'Weekend Project'),
  role('job-not', 'Night Shift Ops'),
  role('job-unknown', 'Mystery Role'),
  role('job-new', 'Fresh Listing'),
];
const freshRole = role('job-fresh', 'Fresh Contract Role');

function findingBase(id, group) {
  return {
    opportunityId: id,
    opportunityRevision: 2,
    group,
    assessmentId: `asmt-${id}`,
    profileVersion: 3,
    rubricVersion: 'rubric-1',
    catalogVersion: 'cat-7',
    reasons: [],
    evidenceLinks: [],
    stale: false,
  };
}

const findings = new Map(
  Object.entries({
    'job-rec': {
      ...findingBase('job-rec', 'recommended'),
      reasons: [
        {
          reasonId: 'r-pos-1',
          kind: 'positive',
          label: 'Remote overlap with the brief',
          detail: 'Posting confirms fully remote work from Berlin.',
          jevSupport: 0.85,
        },
        {
          reasonId: 'r-neg-1',
          kind: 'negative',
          label: 'On-call load',
          detail: 'Rota includes weekends.',
          jevSupport: 0.4,
        },
      ],
      conflict: {
        id: 'c-1',
        label: 'Hybrid expectation',
        detail: 'Two days onsite expected.',
      },
      missingFact: { id: 'm-1', label: 'Salary band', detail: 'No band published.' },
      evidenceLinks: [
        { captureId: 'cap-1', spanStart: 10, spanEnd: 40, excerptSha256: 'a'.repeat(64) },
      ],
      sourceRef: {
        sourceId: 'career-site',
        sourceRevision: '2026-09-20',
        observedUrl: 'https://example.invalid/posting/backend',
      },
    },
    'job-could': {
      ...findingBase('job-could', 'could_be_recommended'),
      reasons: [
        {
          reasonId: 'r-pos-2',
          kind: 'positive',
          label: 'Remote-first team',
          detail: 'Docs say remote-first.',
          jevSupport: 0.62,
        },
      ],
      evidenceLinks: [
        { captureId: 'cap-missing', spanStart: 0, spanEnd: 12, excerptSha256: 'b'.repeat(64) },
      ],
    },
    'job-prob': {
      ...findingBase('job-prob', 'probably_not_recommended'),
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
      staleBasis: 'opportunity_revised',
    },
    'job-not': {
      ...findingBase('job-not', 'not_recommended'),
      reasons: [
        {
          reasonId: 'r-neg-3',
          kind: 'negative',
          label: 'Night shifts',
          detail: 'Permanent nights.',
          jevSupport: 0.9,
        },
      ],
    },
    'job-unknown': {
      ...findingBase('job-unknown', 'unknown'),
      unknownBasis: 'Evidence conflicted across sources.',
      stale: true,
      staleBasis: 'brief_changed,catalog_changed',
    },
    'job-fresh': {
      ...findingBase('job-fresh', 'could_be_recommended'),
      reasons: [
        {
          reasonId: 'r-pos-3',
          kind: 'positive',
          label: 'Contract overlap',
          detail: 'New source confirms a remote contract role.',
          jevSupport: 0.58,
        },
      ],
      evidenceLinks: [
        { captureId: 'cap-2', spanStart: 4, spanEnd: 30, excerptSha256: 'c'.repeat(64) },
      ],
    },
  }),
);

const captures = {
  'cap-1': {
    captureId: 'cap-1',
    observedUrl: 'https://example.invalid/posting/backend',
    finalUrl: 'https://example.invalid/posting/backend',
    retrievedAt: time,
    status: 'ok',
    mediaType: 'text/html',
    contentHash: 'abc123',
    extent: { bytes: 1234, complete: true },
  },
  'cap-2': {
    captureId: 'cap-2',
    observedUrl: 'https://example.invalid/feed/fresh',
    retrievedAt: time,
    status: 'ok',
    mediaType: 'text/html',
    contentHash: 'def456',
    extent: { bytes: 980, complete: true },
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
    briefVersion: { profileVersion: 3, rubricVersion: 'rubric-1' },
    allowance: { ...allowance },
    usage: usageFor(run.observed),
    investigations:
      run.state === 'completed'
        ? []
        : [{ id: 'inv-1', intent: 'Collect vacancy text', status: 'active' }],
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
  sendJson(res, status, { error: { message: `Slice2 fixture HTTP ${status}` } });
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

async function startSlice2Fixture() {
  const state = {
    expireSession: true,
    decisions: new Map(),
    runs: new Map(),
    commissionsByKey: new Map(),
    runSeq: 0,
    requests: [],
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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'slice2-fixture' });
      if (path === '/api/v1/preferences') return sendJson(res, 200, profile);
      if (path === '/api/v1/runtime-status')
        return sendJson(res, 200, { ingestionAvailable: false, organisationAvailable: false });
      if (path === '/api/v1/muse/readiness' && req.method === 'GET') {
        const tier = url.searchParams.get('tier');
        if (tier !== 'contributor' && tier !== 'standard')
          return sendJson(res, 400, { error: { message: 'Tier must be contributor or standard.' } });
        return sendJson(res, 200, {
          state: 'ready',
          code: 'muse_ready',
          detail: `muse ${tier} session may be admitted`,
          tier,
        });
      }
      if (path === '/api/v1/muse/checkpoints' && req.method === 'GET')
        return sendJson(res, 200, []);
      if (path === '/api/v1/muse/report' && req.method === 'GET')
        return sendJson(res, 404, { error: { message: 'Muse run not found.' } });
      if (path === '/api/v1/muse/commissions' && req.method === 'GET')
        return sendJson(res, 200, { count: 0 });
      if (path === '/api/v1/research/brief') return sendJson(res, 200, brief);
      if (path === '/api/v1/workflow/roles') {
        const items = [...state.decisions.values()]
          .filter((decision) => decision.decision === 'selected')
          .map((decision) => ({
            opportunityId: decision.opportunityId,
            stage: 'selected',
            revision: decision.revision,
            updatedAt: time,
            decisionAt: time,
            opportunityRevision: decision.opportunityRevision,
          }));
        return sendJson(res, 200, { items });
      }
      if (path === '/api/v1/opportunities') {
        const withFresh = [...state.runs.values()].some((run) => run.id === 'slice2-run-2');
        const items = (withFresh ? [...roles, freshRole] : roles).map((opportunity) => ({
          opportunity,
          likelyDuplicates: [],
        }));
        return sendJson(res, 200, { items });
      }
      const oppLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/(decision|finding|workflow)$/);
      if (oppLeaf?.[1] !== undefined && oppLeaf[2] !== undefined) {
        const id = decodeURIComponent(oppLeaf[1]);
        const leaf = oppLeaf[2];
        const opp = [...roles, freshRole].find((item) => item.id === id);
        if (opp === undefined) return error(res, 404);
        if (leaf === 'decision' && req.method === 'GET') {
          const saved = state.decisions.get(id);
          return saved === undefined ? error(res, 404) : sendJson(res, 200, saved);
        }
        if (leaf === 'decision' && req.method === 'POST') {
          const saved = state.decisions.get(id);
          if (
            payload.expectedOpportunityRevision !== opp.revision ||
            payload.expectedDecisionRevision !== (saved?.revision || 0) ||
            payload.decision !== 'selected' ||
            typeof payload.requestKey !== 'string' ||
            payload.requestKey === ''
          )
            return error(res, 409);
          const next = {
            id: `synthetic-decision-${id}`,
            opportunityId: id,
            decision: 'selected',
            revision: (saved?.revision || 0) + 1,
            opportunityRevision: opp.revision,
            auditId: 'synthetic-audit',
            createdAt: time,
          };
          state.decisions.set(id, next);
          return sendJson(res, 201, next);
        }
        if (leaf === 'finding' && req.method === 'GET') {
          const entry = findings.get(id);
          return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
        }
        if (leaf === 'workflow' && req.method === 'GET') {
          const saved = state.decisions.get(id);
          if (saved?.decision !== 'selected') return error(res, 404);
          return sendJson(res, 200, {
            opportunityId: id,
            stage: 'selected',
            revision: saved.revision,
            updatedAt: time,
            decisionAt: time,
            opportunityRevision: saved.opportunityRevision,
          });
        }
        return error(res, 404);
      }
      const oppDetail = path.match(/^\/api\/v1\/opportunities\/([^/]+)$/);
      if (oppDetail?.[1] !== undefined && req.method === 'GET') {
        const opp = [...roles, freshRole].find((item) => item.id === decodeURIComponent(oppDetail[1]));
        return opp === undefined
          ? error(res, 404)
          : sendJson(res, 200, { opportunity: opp, likelyDuplicates: [] });
      }
      if (path === '/api/v1/research/runs' && req.method === 'POST') {
        if (typeof payload?.idempotencyKey === 'string' && state.commissionsByKey.has(payload.idempotencyKey)) {
          return sendJson(res, 200, state.commissionsByKey.get(payload.idempotencyKey));
        }
        state.runSeq += 1;
        const id = `slice2-run-${state.runSeq}`;
        const run =
          state.runSeq === 1
            ? {
                id,
                state: 'running',
                savedIds: [],
                unresolved: 0,
                observed: { actions: 1, jev: 0, turns: 1, bytes: 1200 },
                completeOnRead: false,
                events: [
                  {
                    eventId: 'ev-1',
                    at: time,
                    kind: 'observation',
                    phase: 'collect',
                    summary: 'Codex recorded vacancy text from the company career page.',
                    refs: { captureId: 'cap-1' },
                  },
                ],
                report: {
                  outcomes: ['Saved 6 tracked roles for Jev classification.'],
                  searched: ['company career page'],
                  reused: ['0 prior results reused'],
                  uncertainty: ['One role lacks a published salary band.'],
                  nextWork: ['Select a role to start its check.'],
                },
              }
            : {
                id,
                state: 'completed',
                savedIds: ['job-fresh'],
                unresolved: 0,
                observed: { actions: 3, jev: 1, turns: 1, bytes: 9800 },
                events: [
                  {
                    eventId: 'ev-4',
                    at: time,
                    kind: 'observation',
                    phase: 'collect',
                    summary: 'Found 1 additional role from a newly discovered source feed.',
                    refs: { captureId: 'cap-2', recordId: 'job-fresh' },
                  },
                  {
                    eventId: 'ev-5',
                    at: time,
                    kind: 'run.observed',
                    phase: 'classify',
                    summary: 'Jev classified the new role against catalog cat-7.',
                    refs: { assessmentId: 'asmt-job-fresh' },
                  },
                ],
                report: {
                  outcomes: ['Saved 1 additional role from a newly discovered source.'],
                  searched: ['company career page', 'new source feed'],
                  reused: [],
                  uncertainty: [],
                  nextWork: [],
                },
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
          if (run.completeOnRead === true) {
            run.completeOnRead = false;
            run.state = 'completed';
            run.savedIds = roles.map((item) => item.id);
            run.unresolved = 1;
            run.observed = { actions: 9, jev: 5, turns: 3, bytes: 42100 };
            run.events.push(
              {
                eventId: 'ev-2',
                at: time,
                kind: 'run.saved',
                phase: 'collect',
                summary: 'Saved 6 tracked roles.',
                refs: { recordId: 'job-rec' },
              },
              {
                eventId: 'ev-3',
                at: time,
                kind: 'run.observed',
                phase: 'classify',
                summary: 'Jev classified 5 roles against catalog cat-7.',
                refs: { assessmentId: 'asmt-job-rec' },
              },
            );
          }
          return sendJson(res, 200, runView(run));
        }
        if (leaf === 'activity' && req.method === 'GET')
          return sendJson(res, 200, { events: run.events });
        if (leaf === 'report' && req.method === 'GET') {
          if (run.state !== 'completed' && run.state !== 'failed') return error(res, 409);
          return sendJson(res, 200, reportFor(run));
        }
        if (leaf === 'findings' && req.method === 'GET') {
          const items =
            run.id === 'slice2-run-1'
              ? [findings.get('job-rec'), findings.get('job-unknown')]
              : [findings.get('job-rec'), findings.get('job-unknown'), findings.get('job-fresh')];
          return sendJson(res, 200, { items });
        }
        if (leaf === 'steer' && req.method === 'POST') {
          if (typeof payload?.body !== 'string' || payload.body.trim() === '') return error(res, 400);
          run.events.push({
            eventId: `ev-steer-${run.events.length + 1}`,
            at: time,
            kind: 'run.steered',
            phase: 'collect',
            summary: `Steering noted: ${payload.body.trim().slice(0, 120)}`,
          });
          return sendJson(res, 200, {
            messageId: `steer-${run.events.length}`,
            revision: 1,
            body: payload.body,
            ack: 'acknowledged',
          });
        }
        return error(res, 404);
      }
      const capture = path.match(/^\/api\/v1\/research\/captures\/([^/]+)$/);
      if (capture?.[1] !== undefined && req.method === 'GET') {
        const view = captures[decodeURIComponent(capture[1])];
        return view === undefined ? error(res, 404) : sendJson(res, 200, view);
      }
      const roundControl = path.match(/^\/api\/v1\/rounds\/([^/]+)\/(stop|resume)$/);
      if (roundControl?.[1] !== undefined && req.method === 'POST') {
        const run = state.runs.get(decodeURIComponent(roundControl[1]));
        if (run === undefined) return error(res, 404);
        if (roundControl[2] === 'stop') {
          run.state = 'paused';
          run.stopReason = 'owner stop';
        } else {
          run.state = 'running';
          run.stopReason = undefined;
          run.completeOnRead = true;
        }
        return sendJson(res, 200, { runId: run.id, state: run.state });
      }
      return error(res, 503);
    } catch (cause) {
      sendJson(res, 500, { error: { message: `Slice2 fixture error: ${cause.message}` } });
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
  return requests.filter(
    (item) => item.method === 'POST' && !item.path.startsWith('/api/v1/auth/'),
  );
}

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startSlice2Fixture();
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

    // My search: reading the profile and the idle discovery section starts
    // nothing.
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'My search' }).click();
    await page.getByRole('heading', { name: 'My search', exact: true }).waitFor();
    await page.getByText('Berlin').first().waitFor();
    await page.getByRole('button', { name: 'Find jobs', exact: true }).waitFor();
    assert.equal(
      nonAuthPosts(fixture.state.requests).length,
      0,
      `reads must commission no work: ${JSON.stringify(nonAuthPosts(fixture.state.requests))}`,
    );

    // Start / stop / resume / refresh a run through the UI.
    await page.getByText('Extra note for this run only (optional)').first().click();
    await page.getByLabel('Extra note for this run only (optional)').fill('Remote backend roles');
    await page.getByRole('button', { name: 'Find jobs', exact: true }).click();
    await page.getByText('Running — research is underway.').waitFor();
    await page.getByText('No classification recorded').waitFor();
    const trackedAfterStart = await page.evaluate(() =>
      window.localStorage.getItem('jobseek.research-run-id'),
    );
    assert.equal(trackedAfterStart, 'slice2-run-1');
    await page.getByRole('button', { name: 'Stop', exact: true }).click();
    await page.getByText('Paused — resume continues with the remaining allowance.').waitFor();
    await page.getByRole('button', { name: 'Resume', exact: true }).click();
    // The double's first post-resume read completes the run, so the resume
    // refresh lands on the completed view directly.
    await page.getByText('Completed — outcomes and report are saved.').waitFor();
    await page.getByRole('button', { name: 'Refresh', exact: true }).click();
    await page.getByText('Saved 6 tracked roles for Jev classification.').waitFor();
    await page.getByText('Searched 1 source').waitFor();
    await page.getByText('2 recorded').first().waitFor();
    await page.getByText('1 recorded').first().waitFor();
    const commissions = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/research/runs',
    );
    assert.equal(commissions.length, 1);
    assert.ok(
      typeof commissions[0].payload?.idempotencyKey === 'string' &&
        commissions[0].payload.idempotencyKey !== '',
      'commission carries an idempotency key',
    );
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path.includes('/checks'))
        .length,
      0,
      'initial research/classification must start no selected-job check',
    );

    // Revisit: a reload restores the tracked run through reads only.
    const postsBeforeReload = nonAuthPosts(fixture.state.requests).length;
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('Completed — outcomes and report are saved.').waitFor();
    assert.equal(
      nonAuthPosts(fixture.state.requests).length,
      postsBeforeReload,
      'run revisit must commission no work',
    );

    // New-source discovery: Find more jobs commissions a second tracked run
    // whose report and extra role appear in the UI.
    await page.getByRole('button', { name: 'Find more jobs' }).click();
    await page.getByText('Saved 1 additional role from a newly discovered source.').waitFor();
    const trackedAfterMore = await page.evaluate(() =>
      window.localStorage.getItem('jobseek.research-run-id'),
    );
    assert.equal(trackedAfterMore, 'slice2-run-2');

    // Steering reaches the current run only; the saved brief keeps catalog
    // cat-7 (asserted on the Jobs page below).
    await page.getByLabel('Steer the run').fill('Prefer contract roles');
    await page.getByRole('button', { name: 'Send steering message' }).click();
    await page.getByText('Message acknowledged.').waitFor();
    const steers = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path.endsWith('/steer'),
    );
    assert.equal(steers.length, 1);
    assert.equal(steers[0].path, '/api/v1/research/runs/slice2-run-2/steer');

    // Jobs: four-group coverage plus Unknown and unclassified, saved reason
    // display, brief/catalog line and the new-source role.
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' }).click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Could be recommended (2)', { exact: true }).waitFor();
    await page.getByText('Probably not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Unknown — exceptional, needs a basis (1)', { exact: true }).waitFor();
    await page.getByText('Not yet classified (1)', { exact: true }).waitFor();
    await page.getByText('Search brief: profile v3 · rubric rubric-1 · catalog cat-7').waitFor();
    await page.getByText('Tracked research run: slice2-run-2').waitFor();
    await page.getByText('Fresh Contract Role').first().waitFor();
    await page.getByText('Stale — opportunity_revised').waitFor();
    const postsBeforeWhy = nonAuthPosts(fixture.state.requests).length;

    // Explanation opens reread nothing: zero new requests of any kind.
    const requestsBeforeWhy = fixture.state.requests.length;
    const whyButtons = page.getByRole('button', { name: 'Why this job' });
    assert.equal(await whyButtons.count(), 6);
    // Each opened button flips to "Hide why", so re-resolve the first closed
    // one per iteration.
    for (let index = 0; index < 6; index += 1) {
      await page.getByRole('button', { name: 'Why this job' }).first().click();
    }
    await page.getByText('Strength: Remote overlap with the brief').waitFor();
    await page.getByText('Posting confirms fully remote work from Berlin.').waitFor();
    await page.getByText('Jev support signal: 0.85').waitFor();
    await page.getByText('Concern: On-call load').waitFor();
    await page
      .getByText('Conflicting consideration: Hybrid expectation — Two days onsite expected.')
      .waitFor();
    await page.getByText('Missing information: Salary band — No band published.').waitFor();
    await page.getByText('Jev could not place this role: Evidence conflicted across sources.').waitFor();
    await page
      .getByText("Support signals record Jev's assessment strength; they are not verified correctness.")
      .first()
      .waitFor();
    await page.getByText('Evidence (1)').first().waitFor();
    await page.getByText('Source: career-site (rev 2026-09-20)').waitFor();
    assert.equal(
      fixture.state.requests.length,
      requestsBeforeWhy,
      `explanation opens must make no request: ${JSON.stringify(fixture.state.requests.slice(requestsBeforeWhy))}`,
    );
    assert.equal(
      nonAuthPosts(fixture.state.requests).length,
      postsBeforeWhy,
      'explanation opens must commission no work',
    );

    // Selection posts a guarded owner decision and never starts a check.
    await page.getByRole('button', { name: 'Select Weekend Project for preparation' }).click();
    await page.getByText('Selected — decision rev 1.').waitFor();
    const decisionGets = fixture.state.requests.filter(
      (item) =>
        item.method === 'GET' && item.path === '/api/v1/opportunities/job-prob/decision',
    );
    const decisionPosts = fixture.state.requests.filter(
      (item) =>
        item.method === 'POST' && item.path === '/api/v1/opportunities/job-prob/decision',
    );
    assert.equal(decisionGets.length, 1);
    assert.equal(decisionPosts.length, 1);
    assert.ok(
      fixture.state.requests.indexOf(decisionGets[0]) <
        fixture.state.requests.indexOf(decisionPosts[0]),
      'select reads the existing decision before posting',
    );
    assert.equal(decisionPosts[0].payload.decision, 'selected');
    assert.equal(decisionPosts[0].payload.expectedOpportunityRevision, 2);
    assert.equal(decisionPosts[0].payload.expectedDecisionRevision, 0);
    assert.ok(
      typeof decisionPosts[0].payload.requestKey === 'string' &&
        decisionPosts[0].payload.requestKey !== '',
      'select carries a request key',
    );
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/checks')).length,
      0,
      'selection must start no check and read no check state',
    );

    // Selection persists across reload via the saved decision and workflow
    // pill; re-selecting guards on the saved revision.
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Could be recommended (2)', { exact: true }).waitFor();
    await page
      .getByRole('link', { name: 'Open Weekend Project at stage Selected' })
      .waitFor();
    await page.getByRole('button', { name: 'Select Weekend Project for preparation' }).click();
    await page.getByText('Selected — decision rev 2.').waitFor();
    const decisionPostsAfter = fixture.state.requests.filter(
      (item) =>
        item.method === 'POST' && item.path === '/api/v1/opportunities/job-prob/decision',
    );
    assert.equal(decisionPostsAfter.length, 2);
    assert.equal(decisionPostsAfter[1].payload.expectedDecisionRevision, 1);

    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/checks')).length,
      0,
      'no check traffic anywhere in the slice 2 journey',
    );
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log(
      'slice2 smoke: run start/stop/resume/revisit, find-more new source, steer ack, four-group coverage, saved reasons, zero-request explanations, guarded persistent selection, zero check traffic OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice2 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('slice2 smoke failed:', cause);
  process.exitCode = 1;
});
