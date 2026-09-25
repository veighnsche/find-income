import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// Slice 3 gate (G3): connected select -> explicit check -> actual questions ->
// saved-answer flow through the UI against a deterministic in-file API double.
// Covers an unselected role, one blocked role, a checking role, an outdated
// role, a no-matching-answer question, and exact edits/blanks. Nothing here is
// a live model run: checks, Jev matches and saved answers are canned double
// data; the handoff distinguishes this deterministic coverage from real-run
// evidence.
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
  roleCriteria: [],
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
  role('job-ready', 'Backend Engineer'),
  role('job-todo', 'Support Engineer'),
  role('job-checking', 'Contract Review'),
  role('job-outdated', 'Weekend Project'),
  role('job-blocked', 'Night Shift Ops'),
  role('job-open', 'Mystery Role'),
];

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
    'job-ready': findingBase('job-ready', 'recommended'),
    'job-todo': findingBase('job-todo', 'could_be_recommended'),
    'job-checking': findingBase('job-checking', 'could_be_recommended'),
    'job-outdated': findingBase('job-outdated', 'probably_not_recommended'),
    'job-blocked': findingBase('job-blocked', 'not_recommended'),
  }),
);

function question(checkId, id, ordinal, text, required, excerpt, sha) {
  return {
    id,
    checkId,
    ordinal,
    text,
    required,
    kind: 'free_text',
    sourceSpan: { captureId: 'cap-1', start: 30 + ordinal * 40, end: 58 + ordinal * 40 },
    sourceExcerpt: excerpt,
    textSha256: sha,
  };
}

function checkFixture(jobId, status, questions, setSha, overrides = {}) {
  return {
    id: `check-${jobId}`,
    opportunityId: jobId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status,
    vacancy: {
      captureIds: ['cap-1'],
      evidenceSourceIds: ['src-1'],
      completeness: 'complete',
      sourceUrl: `https://example.invalid/jobs/${jobId}`,
      retrievedAt: time,
    },
    requestedDocuments: [
      {
        label: 'CV',
        required: true,
        sourceExcerpt: 'Please attach your CV.',
        sourceSpan: { captureId: 'cap-1', start: 0, end: 22 },
      },
    ],
    route: {
      judgment: 'application_route',
      kind: 'direct',
      destinationText: 'Apply through the portal.',
      sourceExcerpt: 'Apply through the portal.',
      observedAt: time,
    },
    gaps: [
      {
        id: `gap-${jobId}`,
        description: 'Salary band unconfirmed.',
        consequential: true,
        kind: 'unverified_claim',
      },
    ],
    questions,
    questionSetSha256: setSha,
    questionSetVersion: 1,
    createdAt: time,
    createdBy: { actorKind: 'codex', actorId: 'codex' },
    ...overrides,
  };
}

const readyCheck = checkFixture(
  'job-ready',
  'checked',
  [
    question(
      'check-job-ready',
      'q-ready-1',
      0,
      'Can you work remotely from Berlin?',
      'required',
      'Can you work remotely from Berlin? (posting paragraph 2)',
      'sha-ready-1',
    ),
    question(
      'check-job-ready',
      'q-ready-2',
      1,
      'When can you start?',
      'optional',
      'When can you start? (application form Q2)',
      'sha-ready-2',
    ),
  ],
  'set-ready',
);

const blockedCheck = checkFixture(
  'job-blocked',
  'blocked',
  [
    question(
      'check-job-blocked',
      'q-blocked-1',
      0,
      'Why this role?',
      'required',
      'Why this role? (posting footer)',
      'sha-blocked-1',
    ),
  ],
  'set-blocked',
  { blockedReason: { code: 'source_unavailable', detail: 'The posting returned HTTP 404.' } },
);

const outdatedCheck = checkFixture(
  'job-outdated',
  'checked',
  [
    question(
      'check-job-outdated',
      'q-outdated-1',
      0,
      'Describe your weekend availability.',
      'required',
      'Describe your weekend availability. (posting line 12)',
      'sha-outdated-1',
    ),
  ],
  'set-outdated',
);

// Checking states carry a partial check object so the answers surface can
// distinguish "Check in progress" (detail present) from "No check yet".
const checkingCheck = checkFixture('job-checking', 'checking', [], 'set-checking');
const todoCheckingCheck = checkFixture('job-todo', 'checking', [], 'set-todo-checking');
const outdatedCheckingCheck = checkFixture('job-outdated', 'checking', [], 'set-outdated-checking');

const todoCheck = checkFixture(
  'job-todo',
  'checked',
  [
    question(
      'check-job-todo',
      'q-todo-1',
      0,
      'Describe your remote experience.',
      'required',
      'Describe your remote experience. (form Q1)',
      'sha-todo-1',
    ),
    question(
      'check-job-todo',
      'q-todo-2',
      1,
      'What are your salary expectations?',
      'optional',
      'What are your salary expectations? (form Q2)',
      'sha-todo-2',
    ),
  ],
  'set-todo',
);

function matchFixture(checkId, setSha, entries) {
  return {
    status: 'matched',
    checkId,
    questionSetSha256: setSha,
    answerCatalog: { digest: 'catalog-digest', matchedAt: time },
    matches: entries,
  };
}

function concreteMatch(questionId, questionSha, answerId, answerVersion, textSha) {
  return {
    questionId,
    questionTextSha256: questionSha,
    choice: { answerId, answerVersion, textSha256: textSha },
    candidateSetHash: 'candidates',
    jevAttemptId: 'jev-1',
    confidence: 0.8,
    model: 'jev-test-1',
    matchedAt: time,
  };
}

function noneFitsMatch(questionId, questionSha) {
  return {
    questionId,
    questionTextSha256: questionSha,
    choice: { noneFits: true },
    candidateSetHash: 'candidates',
    jevAttemptId: 'jev-1',
    confidence: 0.7,
    matchedAt: time,
  };
}

const readyMatch = matchFixture('check-job-ready', 'set-ready', [
  concreteMatch('q-ready-1', 'sha-ready-1', 'answer-remote', 1, 'text-sha-remote'),
  noneFitsMatch('q-ready-2', 'sha-ready-2'),
]);

const todoMatch = matchFixture('check-job-todo', 'set-todo', [
  concreteMatch('q-todo-1', 'sha-todo-1', 'answer-todo', 1, 'text-sha-todo'),
  noneFitsMatch('q-todo-2', 'sha-todo-2'),
]);

function savedAnswer(id, text, textSha) {
  return {
    id,
    currentVersion: 1,
    scopeTags: ['remote'],
    versions: [
      {
        version: 1,
        text,
        textSha256: textSha,
        approvedAt: time,
        approvedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
        approvalRequestKey: `approve-${id}`,
      },
    ],
  };
}

const savedAnswers = new Map(
  Object.entries({
    'answer-remote': savedAnswer(
      'answer-remote',
      'I work remotely from Berlin, async-first.',
      'text-sha-remote',
    ),
    'answer-todo': savedAnswer(
      'answer-todo',
      'I have 4 years of remote product work.',
      'text-sha-todo',
    ),
  }),
);

function workflowFixture(jobId, stage, extra = {}) {
  return {
    opportunityId: jobId,
    stage,
    revision: 1,
    updatedAt: time,
    decisionAt: time,
    opportunityRevision: 2,
    ...extra,
  };
}

function decisionFixture(jobId, revision = 1) {
  return {
    id: `synthetic-decision-${jobId}`,
    opportunityId: jobId,
    decision: 'selected',
    revision,
    opportunityRevision: 2,
    auditId: 'synthetic-audit',
    createdAt: time,
  };
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': body.length });
  res.end(body);
}
function error(res, status) {
  sendJson(res, status, { error: { message: `Slice3 fixture HTTP ${status}` } });
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

function suggestionFor(jobId, questionId, matches) {
  const match = matches.get(jobId);
  if (!match || match.status === 'outdated') return null;
  const entry = match.matches.find((item) => item.questionId === questionId);
  if (!entry || entry.choice.noneFits === true) return null;
  if (entry.choice.answerId === undefined || entry.choice.answerVersion === undefined) return null;
  const saved = savedAnswers.get(entry.choice.answerId);
  if (!saved) return null;
  const pinned = saved.versions.find((version) => version.version === entry.choice.answerVersion);
  if (!pinned) return null;
  return {
    text: pinned.text,
    ref: {
      answerId: entry.choice.answerId,
      answerVersion: entry.choice.answerVersion,
      textSha256: entry.choice.textSha256,
    },
  };
}

async function startSlice3Fixture() {
  const state = {
    expireSession: true,
    decisions: new Map([
      ['job-ready', decisionFixture('job-ready')],
      ['job-checking', decisionFixture('job-checking')],
      ['job-outdated', decisionFixture('job-outdated')],
      ['job-blocked', decisionFixture('job-blocked')],
    ]),
    workflows: new Map([
      ['job-ready', workflowFixture('job-ready', 'checked')],
      ['job-checking', workflowFixture('job-checking', 'checking')],
      ['job-outdated', workflowFixture('job-outdated', 'checked')],
      ['job-blocked', workflowFixture('job-blocked', 'blocked', { blockedReason: 'Posting unreachable.' })],
    ]),
    checks: new Map([
      ['job-ready', { status: 'checked', check: readyCheck }],
      ['job-checking', { status: 'checking', check: checkingCheck }],
      ['job-outdated', { status: 'outdated', check: outdatedCheck }],
      ['job-blocked', { status: 'blocked', check: blockedCheck }],
    ]),
    matches: new Map([['job-ready', readyMatch]]),
    answerValues: new Map([
      ['job-ready', { checkId: 'check-job-ready', questionSetSha256: 'set-ready', values: [] }],
    ]),
    todoReads: 0,
    requests: [],
  };
  const server = createServer(async (req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    const path = url.pathname;
    try {
      if (!path.startsWith('/api/v1/')) return await serveStatic(res, path);
      const payload = req.method === 'POST' || req.method === 'PUT' ? await bodyJson(req) : undefined;
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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'slice3-fixture' });
      if (path === '/api/v1/preferences') return sendJson(res, 200, profile);
      if (path === '/api/v1/runtime-status')
        return sendJson(res, 200, { ingestionAvailable: false, organisationAvailable: false });
      if (path === '/api/v1/research/brief') return sendJson(res, 200, brief);
      if (path === '/api/v1/workflow/roles') {
        return sendJson(res, 200, { items: [...state.workflows.values()] });
      }
      if (path === '/api/v1/opportunities' && req.method === 'GET') {
        return sendJson(res, 200, {
          items: roles.map((opportunity) => ({ opportunity, likelyDuplicates: [] })),
        });
      }
      const answerDetail = path.match(/^\/api\/v1\/answers\/([^/]+)$/);
      if (answerDetail?.[1] !== undefined && req.method === 'GET') {
        const saved = savedAnswers.get(decodeURIComponent(answerDetail[1]));
        return saved === undefined ? error(res, 404) : sendJson(res, 200, saved);
      }
      const capture = path.match(/^\/api\/v1\/research\/captures\/([^/]+)$/);
      if (capture?.[1] !== undefined && req.method === 'GET') return error(res, 404);
      const oppLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/(decision|finding|workflow)$/);
      if (oppLeaf?.[1] !== undefined && oppLeaf[2] !== undefined) {
        const id = decodeURIComponent(oppLeaf[1]);
        const leaf = oppLeaf[2];
        const opp = roles.find((item) => item.id === id);
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
          if (!state.workflows.has(id)) {
            state.workflows.set(id, workflowFixture(id, 'selected', { revision: 1 }));
          }
          return sendJson(res, 201, next);
        }
        if (leaf === 'finding' && req.method === 'GET') {
          const entry = findings.get(id);
          return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
        }
        if (leaf === 'workflow' && req.method === 'GET') {
          const entry = state.workflows.get(id);
          return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
        }
        return error(res, 404);
      }
      const activityLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/checks\/current\/activity$/);
      if (activityLeaf?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(activityLeaf[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, { events: [] });
      }
      const currentCheck = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/checks\/current$/);
      if (currentCheck?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(currentCheck[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        if (id === 'job-todo' && state.checks.get('job-todo')?.status === 'checking') {
          if (state.todoReads === 0) {
            state.todoReads += 1;
            return sendJson(res, 200, { status: 'checking', check: todoCheckingCheck });
          }
          const next = { status: 'checked', check: todoCheck };
          state.checks.set('job-todo', next);
          state.workflows.set('job-todo', workflowFixture('job-todo', 'checked'));
          state.matches.set('job-todo', todoMatch);
          state.answerValues.set('job-todo', {
            checkId: 'check-job-todo',
            questionSetSha256: 'set-todo',
            values: [],
          });
          return sendJson(res, 200, next);
        }
        const entry = state.checks.get(id);
        return sendJson(res, 200, entry ?? { status: 'not_checked' });
      }
      const postChecks = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/checks$/);
      if (postChecks?.[1] !== undefined && req.method === 'POST') {
        const id = decodeURIComponent(postChecks[1]);
        const opp = roles.find((item) => item.id === id);
        if (opp === undefined) return error(res, 404);
        const workflow = state.workflows.get(id);
        if (workflow === undefined) return error(res, 422);
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          payload.expectedOpportunityRevision !== opp.revision ||
          payload.expectedWorkflowRevision !== workflow.revision
        )
          return error(res, 409);
        if (id === 'job-ready') return sendJson(res, 200, { status: 'checked', check: readyCheck });
        if (id === 'job-blocked') return sendJson(res, 200, { status: 'blocked', check: blockedCheck });
        if (id === 'job-checking') return sendJson(res, 200, { status: 'checking', check: checkingCheck });
        if (id === 'job-outdated') {
          const next = { status: 'checking', check: outdatedCheckingCheck };
          state.checks.set('job-outdated', next);
          state.workflows.set('job-outdated', workflowFixture('job-outdated', 'checking'));
          return sendJson(res, 201, next);
        }
        if (id === 'job-todo') {
          const next = { status: 'checking', check: todoCheckingCheck };
          state.checks.set('job-todo', next);
          state.todoReads = 0;
          state.workflows.set('job-todo', workflowFixture('job-todo', 'checking'));
          return sendJson(res, 201, next);
        }
        return error(res, 404);
      }
      const matchCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/answers\/match\/current$/);
      if (matchCurrent?.[1] !== undefined && req.method === 'GET') {
        const entry = state.matches.get(decodeURIComponent(matchCurrent[1]));
        return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
      }
      const valuesCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/answers\/current$/);
      if (valuesCurrent?.[1] !== undefined && req.method === 'GET') {
        const entry = state.answerValues.get(decodeURIComponent(valuesCurrent[1]));
        return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
      }
      const putAnswer = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/questions\/([^/]+)\/answer$/);
      if (putAnswer?.[1] !== undefined && putAnswer[2] !== undefined && req.method === 'PUT') {
        const id = decodeURIComponent(putAnswer[1]);
        const questionId = decodeURIComponent(putAnswer[2]);
        if (!state.workflows.has(id)) return error(res, 404);
        const status = state.checks.get(id);
        if (status?.status !== 'checked' || status.check === undefined) return error(res, 409);
        const check = status.check;
        const asked = check.questions.find((item) => item.id === questionId);
        if (asked === undefined) return error(res, 404);
        if (
          typeof payload?.expectedAnswerVersion !== 'number' ||
          typeof payload?.text !== 'string' ||
          payload.text.length > 20000
        )
          return error(res, 400);
        let list = state.answerValues.get(id);
        if (list === undefined || list.checkId !== check.id) {
          list = { checkId: check.id, questionSetSha256: check.questionSetSha256, values: [] };
          state.answerValues.set(id, list);
        }
        const existing = list.values.find((item) => item.questionId === questionId);
        const currentVersion = existing?.version ?? 0;
        if (payload.expectedAnswerVersion !== currentVersion) return error(res, 409);
        const suggestion = suggestionFor(id, questionId, state.matches);
        const text = payload.text;
        let origin = 'owner_written';
        if (text === '') origin = 'carried_blank';
        else if (suggestion !== null && text === suggestion.text) origin = 'jev_suggestion';
        else if (suggestion !== null) origin = 'owner_edited';
        const value = {
          questionId,
          questionTextSha256: asked.textSha256,
          required: asked.required,
          version: currentVersion + 1,
          state: text === '' ? 'blank' : 'answered',
          text,
          ...(text === '' ? {} : { textSha256: `saved-sha-${id}-${questionId}-v${currentVersion + 1}` }),
          provenance: {
            origin,
            ...(suggestion === null
              ? {}
              : { matchId: `match-${id}`, matchChoice: { ...suggestion.ref } }),
            editedAt: time,
            editedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
          },
          updatedAt: time,
        };
        if (existing !== undefined) {
          list.values = list.values.map((item) => (item.questionId === questionId ? value : item));
        } else {
          list.values.push(value);
          list.values.sort((a, b) => {
            const order = new Map(check.questions.map((item, index) => [item.id, index]));
            return (order.get(a.questionId) ?? 0) - (order.get(b.questionId) ?? 0);
          });
        }
        return sendJson(res, 200, value);
      }
      const oppDetail = path.match(/^\/api\/v1\/opportunities\/([^/]+)$/);
      if (oppDetail?.[1] !== undefined && req.method === 'GET') {
        const opp = roles.find((item) => item.id === decodeURIComponent(oppDetail[1]));
        return opp === undefined
          ? error(res, 404)
          : sendJson(res, 200, { opportunity: opp, likelyDuplicates: [] });
      }
      return error(res, 503);
    } catch (cause) {
      sendJson(res, 500, { error: { message: `Slice3 fixture error: ${cause.message}` } });
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

function nonAuthMutations(requests) {
  return requests.filter(
    (item) =>
      (item.method === 'POST' || item.method === 'PUT' || item.method === 'PATCH' || item.method === 'DELETE') &&
      !item.path.startsWith('/api/v1/auth/'),
  );
}

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startSlice3Fixture();
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

    // Jobs: grouped list, brief line, no tracked run, four chosen roles.
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' }).click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Could be recommended (2)', { exact: true }).waitFor();
    await page.getByText('Probably not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Not yet classified (1)', { exact: true }).waitFor();
    await page.getByText('Search brief: profile v3 · rubric rubric-1 · catalog cat-7').waitFor();
    await page.getByText(/No tracked research run/).waitFor();
    await page.getByRole('button', { name: 'Check chosen jobs (4)' }).waitFor();
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      0,
      `reads must commission no work: ${JSON.stringify(nonAuthMutations(fixture.state.requests))}`,
    );

    // Select connects job-todo: guarded decision, starts no check, count 4->5.
    await page.getByRole('button', { name: 'Select Support Engineer for preparation' }).click();
    await page.getByText('Selected — decision rev 1.').waitFor();
    await page.getByRole('button', { name: 'Check chosen jobs (5)' }).waitFor();
    const decisionGets = fixture.state.requests.filter(
      (item) => item.method === 'GET' && item.path === '/api/v1/opportunities/job-todo/decision',
    );
    const decisionPosts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/opportunities/job-todo/decision',
    );
    assert.equal(decisionGets.length, 1);
    assert.equal(decisionPosts.length, 1);
    assert.ok(
      fixture.state.requests.indexOf(decisionGets[0]) < fixture.state.requests.indexOf(decisionPosts[0]),
      'select reads the existing decision before posting',
    );
    assert.equal(decisionPosts[0].payload.decision, 'selected');
    assert.equal(decisionPosts[0].payload.expectedOpportunityRevision, 2);
    assert.equal(decisionPosts[0].payload.expectedDecisionRevision, 0);
    assert.ok(
      typeof decisionPosts[0].payload.requestKey === 'string' && decisionPosts[0].payload.requestKey !== '',
      'select carries a request key',
    );
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/checks')).length,
      0,
      'selection must start no check and read no check state',
    );

    // Check pages before the bulk start: all five states, GET-only mounts.
    const mutationsBeforeChecks = nonAuthMutations(fixture.state.requests).length;

    await page.goto(`${fixture.url}#/jobs/job-todo/check`, { waitUntil: 'networkidle' });
    await page.getByText('No check yet').waitFor();
    await page.getByRole('button', { name: 'Start check' }).waitFor();

    await page.goto(`${fixture.url}#/jobs/job-checking/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check in progress…').waitFor();
    await page.getByRole('button', { name: 'Refresh saved check' }).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Start check' }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Retry check' }).count(), 0);

    await page.goto(`${fixture.url}#/jobs/job-ready/check`, { waitUntil: 'networkidle' });
    await page.getByText('Saved vacancy').waitFor();
    await page.getByText('Can you work remotely from Berlin?').first().waitFor();
    await page.getByText('When can you start?').first().waitFor();
    await page.getByText('Excerpt: Can you work remotely from Berlin? (posting paragraph 2)').waitFor();
    await page.getByText('Excerpt: When can you start? (application form Q2)').waitFor();
    await page.getByText('CV · required').waitFor();
    await page.getByText('Salary band unconfirmed.').waitFor();
    await page.getByText('application_route (direct)').waitFor();

    await page.goto(`${fixture.url}#/jobs/job-blocked/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check blocked').first().waitFor();
    await page.getByText('Blocked — source_unavailable: The posting returned HTTP 404.').waitFor();
    await page.getByText('Excerpt: Why this role? (posting footer)').waitFor();
    await page.getByRole('button', { name: 'Retry check' }).waitFor();

    await page.goto(`${fixture.url}#/jobs/job-outdated/check`, { waitUntil: 'networkidle' });
    await page.getByText('Saved check is outdated').waitFor();
    await page.getByText('Excerpt: Describe your weekend availability. (posting line 12)').waitFor();
    await page.getByRole('button', { name: 'Run a new check' }).waitFor();

    // Unselected role touches no check endpoints.
    const checkRequestsBeforeOpen = fixture.state.requests.filter((item) =>
      item.path.includes('/opportunities/job-open/checks'),
    ).length;
    await page.goto(`${fixture.url}#/jobs/job-open/check`, { waitUntil: 'networkidle' });
    await page.getByText('Role not selected').waitFor();
    await page
      .getByText('This role is not selected, so the server keeps no check state for it. Only chosen roles can be checked.')
      .waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/opportunities/job-open/checks')).length,
      checkRequestsBeforeOpen,
      'unselected check page must not touch check endpoints',
    );
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      mutationsBeforeChecks,
      'check page visits must commission no work',
    );

    // Answers honest states before the bulk start: no editable boxes.
    async function expectNoAnswerBoxes(jobId, heading) {
      await page.goto(`${fixture.url}#/jobs/${jobId}/answers`, { waitUntil: 'networkidle' });
      await page.getByText(heading).first().waitFor();
      assert.equal(
        await page.getByLabel('Your answer').count(),
        0,
        `${jobId} answers must show no editable box`,
      );
    }
    const mutationsBeforeAnswers = nonAuthMutations(fixture.state.requests).length;
    await expectNoAnswerBoxes('job-open', 'Role not selected');
    await expectNoAnswerBoxes('job-todo', 'No check yet');
    await expectNoAnswerBoxes('job-checking', 'Check in progress');
    await expectNoAnswerBoxes('job-blocked', 'Check blocked');
    await expectNoAnswerBoxes('job-outdated', 'Check outdated');
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      mutationsBeforeAnswers,
      'answers honest-state visits must commission no work',
    );

    // Answers checked flow for job-ready: Jev prefill, no-match blank.
    await page.goto(`${fixture.url}#/jobs/job-ready/answers`, { waitUntil: 'networkidle' });
    await page.getByText('1. Can you work remotely from Berlin?').waitFor();
    await page.getByText('2. When can you start?').waitFor();
    assert.equal(await page.getByLabel('Your answer').count(), 2);
    const readyBoxes = page.getByLabel('Your answer');
    assert.equal(await readyBoxes.nth(0).inputValue(), 'I work remotely from Berlin, async-first.');
    assert.equal(await readyBoxes.nth(1).inputValue(), '');
    await page.getByText('Prefilled from a saved suggestion; change it or clear the box before saving.').waitFor();
    await page.getByText('No saved answer fit this question; the box starts blank.').waitFor();
    const putsBeforeReady = fixture.state.requests.filter((item) => item.method === 'PUT').length;
    assert.equal(putsBeforeReady, 0, 'answers mount must issue no PUT');

    // Exact edit on the no-match question: byte-identical save, then version guard.
    const exact = '  Hybrid: 2–3 days ✅\nSecond line.  ';
    await readyBoxes.nth(1).fill(exact);
    const readySaves = page.getByRole('button', { name: 'Save answer' });
    await readySaves.nth(1).click();
    await page.getByText('Saved · v1.').waitFor();
    const readyPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-ready/questions/q-ready-2/answer',
    );
    assert.equal(readyPuts.length, 1);
    assert.deepEqual(readyPuts[0].payload, { expectedAnswerVersion: 0, text: exact });
    assert.deepEqual(Object.keys(readyPuts[0].payload).sort(), ['expectedAnswerVersion', 'text']);
    await readyBoxes.nth(1).fill(`${exact}!`);
    await page.getByRole('button', { name: 'Save answer' }).nth(1).click();
    await page.getByText('Saved · v2.').waitFor();
    const readyPutsAfter = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-ready/questions/q-ready-2/answer',
    );
    assert.equal(readyPutsAfter.length, 2);
    assert.deepEqual(readyPutsAfter[1].payload, { expectedAnswerVersion: 1, text: `${exact}!` });

    // Explicit blank: clear the suggestion, save empty.
    await readyBoxes.nth(0).fill('');
    await page.getByRole('button', { name: 'Save answer' }).nth(0).click();
    await page.getByText('Blank saved · v1.').waitFor();
    const blankPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-ready/questions/q-ready-1/answer',
    );
    assert.equal(blankPuts.length, 1);
    assert.deepEqual(blankPuts[0].payload, { expectedAnswerVersion: 0, text: '' });
    await page.getByText('Explicitly left blank').first().waitFor();

    // Reload persists saved values exactly.
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('1. Can you work remotely from Berlin?').waitFor();
    const reloadedBoxes = page.getByLabel('Your answer');
    assert.equal(await reloadedBoxes.nth(0).inputValue(), '');
    assert.equal(await reloadedBoxes.nth(1).inputValue(), `${exact}!`);

    // Bulk explicit start from the Jobs list: five chosen roles, none unselected.
    await page.goto(`${fixture.url}#/jobs`, { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Check chosen jobs (5)' }).waitFor();
    const checksBeforeBulk = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path.includes('/checks'),
    ).length;
    await page.getByRole('button', { name: 'Check chosen jobs (5)' }).click();
    await page.getByText('Check complete —').waitFor();
    await page.getByText('Check blocked —').waitFor();
    const pendingLabels = page.getByText('Check pending —');
    assert.equal(await pendingLabels.count(), 3);
    const bulkPosts = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path.includes('/checks'))
      .slice(checksBeforeBulk);
    assert.equal(bulkPosts.length, 5);
    assert.deepEqual(
      new Set(bulkPosts.map((item) => item.path)),
      new Set([
        '/api/v1/opportunities/job-ready/checks',
        '/api/v1/opportunities/job-todo/checks',
        '/api/v1/opportunities/job-checking/checks',
        '/api/v1/opportunities/job-outdated/checks',
        '/api/v1/opportunities/job-blocked/checks',
      ]),
    );
    for (const post of bulkPosts) {
      assert.equal(post.payload.expectedOpportunityRevision, 2);
      assert.equal(post.payload.expectedWorkflowRevision, 1);
      assert.ok(typeof post.payload.requestKey === 'string' && post.payload.requestKey !== '');
      assert.deepEqual(
        Object.keys(post.payload).sort(),
        ['expectedOpportunityRevision', 'expectedWorkflowRevision', 'requestKey'],
      );
    }
    assert.equal(
      new Set(bulkPosts.map((item) => item.payload.requestKey)).size,
      5,
      'each role carries an independent request key',
    );
    const openCheckLink = page.getByRole('link', { name: 'Open check page for Backend Engineer' });
    assert.equal(await openCheckLink.getAttribute('href'), '#/jobs/job-ready/check');

    // Connected downstream unlock for job-todo: checking -> refresh -> checked.
    await page.goto(`${fixture.url}#/jobs/job-todo/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check in progress…').waitFor();
    await page.getByRole('button', { name: 'Refresh saved check' }).click();
    await page.getByText('Describe your remote experience.').first().waitFor();
    await page.getByText('Excerpt: Describe your remote experience. (form Q1)').waitFor();
    await page.getByText('Excerpt: What are your salary expectations? (form Q2)').waitFor();

    // Same role now answers: prefill plus no-match, then saved answers.
    await page.goto(`${fixture.url}#/jobs/job-todo/answers`, { waitUntil: 'networkidle' });
    await page.getByText('1. Describe your remote experience.').waitFor();
    assert.equal(await page.getByLabel('Your answer').count(), 2);
    const todoBoxes = page.getByLabel('Your answer');
    assert.equal(await todoBoxes.nth(0).inputValue(), 'I have 4 years of remote product work.');
    assert.equal(await todoBoxes.nth(1).inputValue(), '');
    const todoExact = 'Range: €4.8k–€6k / month, 32h.';
    await todoBoxes.nth(1).fill(todoExact);
    await page.getByRole('button', { name: 'Save answer' }).nth(1).click();
    await page.getByText('Saved · v1.').first().waitFor();
    const todoPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-todo/questions/q-todo-2/answer',
    );
    assert.equal(todoPuts.length, 1);
    assert.deepEqual(todoPuts[0].payload, { expectedAnswerVersion: 0, text: todoExact });
    await todoBoxes.nth(0).fill('I have 4 years of remote product work. Edited.');
    await page.getByRole('button', { name: 'Save answer' }).nth(0).click();
    await page.getByText('Saved · v1.').first().waitFor();
    await page.getByText('Suggested match, edited by owner').first().waitFor();

    // Recorded calls prove Answer uses no Codex/LLM: no match POST, no
    // codex/research/material/round traffic anywhere in the journey.
    const forbidden = fixture.state.requests.filter(
      (item) =>
        (item.path.includes('/answers/match') && item.method !== 'GET') ||
        item.path.includes('/codex') ||
        item.path.includes('/research/runs') ||
        item.path.includes('/materials') ||
        item.path.includes('/rounds'),
    );
    assert.equal(forbidden.length, 0, `Answer must use no Codex/LLM calls: ${JSON.stringify(forbidden)}`);
    const posts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && !item.path.startsWith('/api/v1/auth/'),
    );
    assert.ok(
      posts.every((item) => item.path.endsWith('/decision') || item.path.endsWith('/checks')),
      `only decision and check POSTs allowed: ${JSON.stringify(posts.map((item) => item.path))}`,
    );
    const puts = fixture.state.requests.filter((item) => item.method === 'PUT');
    assert.ok(puts.length >= 4, `expected at least 4 answer PUTs, saw ${puts.length}`);
    assert.ok(
      puts.every((item) => item.path.includes('/questions/') && item.path.endsWith('/answer')),
      `PUTs must target only question answers: ${JSON.stringify(puts.map((item) => item.path))}`,
    );
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log(
      'slice3 smoke: select, five check states, sourced questions, blocked/unselected/outdated/checking honesty, Jev prefill, no-match, exact edits/blanks, bulk explicit start, connected todo unlock, zero Codex/LLM answer calls OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice3 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('slice3 smoke failed:', cause);
  process.exitCode = 1;
});


