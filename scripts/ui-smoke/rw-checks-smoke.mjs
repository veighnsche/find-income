import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// RW-E3 gate: deterministic acceptance for chosen-role checks and answers.
// Covers explicit check commissioning (selection alone starts nothing,
// unselected roles never start), actual-vs-invented employer questions (every
// displayed question must trace verbatim to the capture spans this double
// serves), Jev prefill vs approved answers plus no-fit honesty, exact
// edits/blanks round-trips, blocked/outdated honesty, long-list keyboard
// reachability of the bulk action, and zero Codex/LLM calls on Answer
// surfaces. All data is canned; .invalid URLs only; no paid calls, no sends.
const webDist = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/vite-app/dist');
const time = '2026-09-25T12:00:00Z';
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

// Served capture bodies: the provenance ground truth. Every employer
// question below is a verbatim slice of one of these bodies; the
// actual-vs-invented assertions fetch these captures over HTTP and check
// each displayed question against its span.
const captureBodies = new Map([
  [
    'cap-rw-rec',
    'Posting cap-rw-rec. Role: Backend Engineer in Berlin. Can you work remotely from Berlin? We are async-first. When can you start? Applications close soon.',
  ],
  [
    'cap-rw-could',
    'Posting cap-rw-could. Role: Support Engineer, remote EU. Describe your remote experience. Support hours vary. What are your salary expectations? State monthly gross.',
  ],
  ['cap-rw-prob', 'Posting cap-rw-prob. Weekend Project, on-call rota. Why this role? Short shifts only.'],
  [
    'cap-rw-not',
    'Posting cap-rw-not. Night Shift Ops, Berlin. Describe your weekend availability. Night differential paid.',
  ],
]);

function spanOf(captureId, sentence) {
  const body = captureBodies.get(captureId);
  assert.ok(body !== undefined, `capture ${captureId} must be served`);
  const start = body.indexOf(sentence);
  assert.ok(start >= 0, `sentence must occur in ${captureId}: ${sentence}`);
  assert.equal(body.indexOf(sentence, start + 1), -1, `sentence must be unique in ${captureId}`);
  return { captureId, start, end: start + sentence.length };
}

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

const fillers = Array.from({ length: 12 }, (_, index) =>
  role(`rw-fill-${String(index + 1).padStart(2, '0')}`, `Filler Contract ${String(index + 1).padStart(2, '0')}`),
);

const roles = [
  role('rw-rec', 'Backend Engineer'),
  role('rw-could', 'Support Engineer'),
  role('rw-checking', 'Contract Review'),
  role('rw-prob', 'Weekend Project'),
  role('rw-not', 'Night Shift Ops'),
  role('rw-open', 'Mystery Role'),
  ...fillers,
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
    'rw-rec': findingBase('rw-rec', 'recommended'),
    'rw-could': findingBase('rw-could', 'could_be_recommended'),
    'rw-checking': findingBase('rw-checking', 'could_be_recommended'),
    'rw-prob': findingBase('rw-prob', 'probably_not_recommended'),
    'rw-not': findingBase('rw-not', 'not_recommended'),
  }),
);

function question(checkId, id, ordinal, text, required, captureId, contextNote, sha) {
  return {
    id,
    checkId,
    ordinal,
    text,
    required,
    kind: 'free_text',
    sourceSpan: spanOf(captureId, text),
    sourceExcerpt: `${text} (${contextNote})`,
    textSha256: sha,
  };
}

function checkFixture(jobId, status, questions, setSha, captureIds, overrides = {}) {
  return {
    id: `check-${jobId}`,
    opportunityId: jobId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status,
    vacancy: {
      captureIds,
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
        sourceSpan: { captureId: captureIds[0], start: 0, end: 22 },
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

const recCheck = checkFixture(
  'rw-rec',
  'checked',
  [
    question(
      'check-rw-rec',
      'q-rec-1',
      0,
      'Can you work remotely from Berlin?',
      'required',
      'cap-rw-rec',
      'posting paragraph 2',
      'sha-rec-1',
    ),
    question(
      'check-rw-rec',
      'q-rec-2',
      1,
      'When can you start?',
      'optional',
      'cap-rw-rec',
      'application form Q2',
      'sha-rec-2',
    ),
  ],
  'set-rec',
  ['cap-rw-rec'],
);

const probCheck = checkFixture(
  'rw-prob',
  'blocked',
  [
    question(
      'check-rw-prob',
      'q-prob-1',
      0,
      'Why this role?',
      'required',
      'cap-rw-prob',
      'posting footer',
      'sha-prob-1',
    ),
  ],
  'set-prob',
  ['cap-rw-prob'],
  { blockedReason: { code: 'source_unavailable', detail: 'The posting returned HTTP 404.' } },
);

const notCheck = checkFixture(
  'rw-not',
  'checked',
  [
    question(
      'check-rw-not',
      'q-not-1',
      0,
      'Describe your weekend availability.',
      'required',
      'cap-rw-not',
      'posting line 12',
      'sha-not-1',
    ),
  ],
  'set-not',
  ['cap-rw-not'],
);

const checkingCheck = checkFixture('rw-checking', 'checking', [], 'set-checking', ['cap-rw-rec']);
const couldCheckingCheck = checkFixture('rw-could', 'checking', [], 'set-could-checking', ['cap-rw-could']);
const notRecheckingCheck = checkFixture('rw-not', 'checking', [], 'set-not-rechecking', ['cap-rw-not']);

const couldCheck = checkFixture(
  'rw-could',
  'checked',
  [
    question(
      'check-rw-could',
      'q-could-1',
      0,
      'Describe your remote experience.',
      'required',
      'cap-rw-could',
      'form Q1',
      'sha-could-1',
    ),
    question(
      'check-rw-could',
      'q-could-2',
      1,
      'What are your salary expectations?',
      'optional',
      'cap-rw-could',
      'form Q2',
      'sha-could-2',
    ),
  ],
  'set-could',
  ['cap-rw-could'],
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

const recMatch = matchFixture('check-rw-rec', 'set-rec', [
  concreteMatch('q-rec-1', 'sha-rec-1', 'answer-rec', 1, 'text-sha-rec'),
  noneFitsMatch('q-rec-2', 'sha-rec-2'),
]);

const couldMatch = matchFixture('check-rw-could', 'set-could', [
  concreteMatch('q-could-1', 'sha-could-1', 'answer-could', 1, 'text-sha-could'),
  noneFitsMatch('q-could-2', 'sha-could-2'),
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
    'answer-rec': savedAnswer('answer-rec', 'I work remotely from Berlin, async-first.', 'text-sha-rec'),
    'answer-could': savedAnswer('answer-could', 'I have 4 years of remote product work.', 'text-sha-could'),
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
  sendJson(res, status, { error: { message: `RW-E3 fixture HTTP ${status}` } });
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

async function startFixture() {
  const state = {
    expireSession: true,
    decisions: new Map([
      ['rw-rec', decisionFixture('rw-rec')],
      ['rw-checking', decisionFixture('rw-checking')],
      ['rw-prob', decisionFixture('rw-prob')],
      ['rw-not', decisionFixture('rw-not')],
    ]),
    workflows: new Map([
      ['rw-rec', workflowFixture('rw-rec', 'checked')],
      ['rw-checking', workflowFixture('rw-checking', 'checking')],
      ['rw-prob', workflowFixture('rw-prob', 'blocked', { blockedReason: 'Posting unreachable.' })],
      ['rw-not', workflowFixture('rw-not', 'checked')],
    ]),
    checks: new Map([
      ['rw-rec', { status: 'checked', check: recCheck }],
      ['rw-checking', { status: 'checking', check: checkingCheck }],
      ['rw-prob', { status: 'blocked', check: probCheck }],
      ['rw-not', { status: 'outdated', check: notCheck }],
    ]),
    matches: new Map([['rw-rec', recMatch]]),
    answerValues: new Map([
      ['rw-rec', { checkId: 'check-rw-rec', questionSetSha256: 'set-rec', values: [] }],
    ]),
    couldReads: 0,
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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'rw-e3-fixture' });
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
      if (capture?.[1] !== undefined && req.method === 'GET') {
        const body = captureBodies.get(decodeURIComponent(capture[1]));
        return body === undefined
          ? error(res, 404)
          : sendJson(res, 200, { id: decodeURIComponent(capture[1]), body });
      }
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
        if (id === 'rw-could' && state.checks.get('rw-could')?.status === 'checking') {
          if (state.couldReads === 0) {
            state.couldReads += 1;
            return sendJson(res, 200, { status: 'checking', check: couldCheckingCheck });
          }
          const next = { status: 'checked', check: couldCheck };
          state.checks.set('rw-could', next);
          state.workflows.set('rw-could', workflowFixture('rw-could', 'checked'));
          state.matches.set('rw-could', couldMatch);
          state.answerValues.set('rw-could', {
            checkId: 'check-rw-could',
            questionSetSha256: 'set-could',
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
        if (id === 'rw-rec') return sendJson(res, 200, { status: 'checked', check: recCheck });
        if (id === 'rw-prob') return sendJson(res, 200, { status: 'blocked', check: probCheck });
        if (id === 'rw-checking') return sendJson(res, 200, { status: 'checking', check: checkingCheck });
        if (id === 'rw-not') {
          const next = { status: 'checking', check: notRecheckingCheck };
          state.checks.set('rw-not', next);
          state.workflows.set('rw-not', workflowFixture('rw-not', 'checking'));
          return sendJson(res, 201, next);
        }
        if (id === 'rw-could') {
          const next = { status: 'checking', check: couldCheckingCheck };
          state.checks.set('rw-could', next);
          state.couldReads = 0;
          state.workflows.set('rw-could', workflowFixture('rw-could', 'checking'));
          return sendJson(res, 201, next);
        }
        const next = {
          status: 'checking',
          check: checkFixture(id, 'checking', [], `set-${id}-checking`, ['cap-rw-rec']),
        };
        state.checks.set(id, next);
        state.workflows.set(id, workflowFixture(id, 'checking'));
        return sendJson(res, 201, next);
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
      sendJson(res, 500, { error: { message: `RW-E3 fixture error: ${cause.message}` } });
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

const SPAN_RE = /Source:\s*(\S+)\s*[·]\s*chars\s*(\d+)[–-](\d+)/;
const EXCERPT_RE = /Excerpt:\s*(.+)/s;

async function fetchCaptureBody(baseUrl, captureId) {
  const res = await fetch(`${baseUrl}/api/v1/research/captures/${encodeURIComponent(captureId)}`);
  assert.equal(res.status, 200, `capture ${captureId} must be served`);
  const data = await res.json();
  assert.equal(typeof data.body, 'string');
  return data.body;
}

// Actual-vs-invented: every displayed question must be a verbatim slice of
// the served capture at its displayed span, and its excerpt must contain it.
async function assertTracedToCapture(baseUrl, displayed) {
  const body = await fetchCaptureBody(baseUrl, displayed.captureId);
  const slice = body.slice(displayed.start, displayed.end);
  assert.equal(
    slice,
    displayed.text,
    `displayed question must equal capture slice [${displayed.start},${displayed.end}) of ${displayed.captureId}`,
  );
  assert.ok(
    displayed.excerpt.includes(displayed.text),
    `excerpt must contain the displayed question: ${displayed.excerpt}`,
  );
}

async function scrapeCheckQuestions(page) {
  const items = page.locator('section[aria-labelledby="check-questions-heading"] li');
  const count = await items.count();
  const out = [];
  for (let index = 0; index < count; index += 1) {
    const item = items.nth(index);
    const paragraphs = item.locator('p');
    assert.ok((await paragraphs.nth(0).innerText()).trim().startsWith('Question '));
    const text = (await paragraphs.nth(1).innerText()).trim();
    const sourceLine = (await item.getByText(/^Source:/).innerText()).trim();
    const span = sourceLine.match(SPAN_RE);
    assert.ok(span?.[1] !== undefined, `check item must show a source span: ${sourceLine}`);
    const excerptLine = (await item.getByText(/^Excerpt:/).innerText()).trim();
    const excerpt = excerptLine.match(EXCERPT_RE);
    assert.ok(excerpt?.[1] !== undefined, `check item must show an excerpt: ${excerptLine}`);
    out.push({
      text,
      captureId: span[1],
      start: Number(span[2]),
      end: Number(span[3]),
      excerpt: excerpt[1].trim(),
    });
  }
  return out;
}

async function scrapeAnswerQuestions(page) {
  const cards = page.locator('section[aria-label^="Question "]');
  const count = await cards.count();
  const out = [];
  for (let index = 0; index < count; index += 1) {
    const card = cards.nth(index);
    const heading = (await card.getByRole('heading').innerText()).trim();
    const text = heading.replace(/^\d+\.\s*/, '');
    const sourceLine = (await card.getByText(/^Source:/).innerText()).trim();
    const parts = sourceLine.split('·').map((part) => part.trim());
    assert.equal(parts.length, 3, `answer source line must have excerpt, capture, span: ${sourceLine}`);
    assert.ok(parts[0].startsWith('Source:'), `answer source line head: ${sourceLine}`);
    const chars = parts[2].match(/^chars\s*(\d+)[–-](\d+)$/);
    assert.ok(chars?.[1] !== undefined, `answer source span: ${sourceLine}`);
    out.push({
      text,
      excerpt: parts[0].replace(/^Source:\s*/, ''),
      captureId: parts[1],
      start: Number(chars[1]),
      end: Number(chars[2]),
    });
  }
  return out;
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
    await page.goto(fixture.url, { waitUntil: 'networkidle' });

    await page.getByLabel('Password').waitFor();
    await page.getByLabel('Password').fill('wrong');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('alert').waitFor();
    fixture.state.expireSession = false;
    await page.getByLabel('Password').fill('synthetic-owner-password');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('navigation', { name: 'Primary' }).waitFor();

    // Jobs: long multi-group list, four chosen roles, reads commission nothing.
    await page.getByRole('navigation', { name: 'Primary' }).getByRole('link', { name: 'Jobs' }).click();
    await page.getByRole('heading', { name: 'Jobs', exact: true }).waitFor();
    await page.getByText('Recommended (1)', { exact: true }).waitFor();
    await page.getByText('Could be recommended (2)', { exact: true }).waitFor();
    await page.getByText('Probably not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Not recommended (1)', { exact: true }).waitFor();
    await page.getByText('Not yet classified (13)', { exact: true }).waitFor();
    await page.getByText('Search brief: profile v3 · rubric rubric-1 · catalog cat-7').waitFor();
    await page.getByRole('button', { name: 'Check chosen jobs (4)' }).waitFor();
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      0,
      `reads must commission no work: ${JSON.stringify(nonAuthMutations(fixture.state.requests))}`,
    );

    // Selection from two groups (could-be + unclassified): guarded decision
    // POSTs, count 4 -> 6, and no check traffic of any kind.
    await page.getByRole('button', { name: 'Select Support Engineer for preparation' }).click();
    await page.getByText('Selected — decision rev 1.').first().waitFor();
    await page.getByRole('button', { name: 'Select Filler Contract 03 for preparation' }).click();
    await page.getByText('Selected — decision rev 1.').nth(1).waitFor();
    await page.getByRole('button', { name: 'Check chosen jobs (6)' }).waitFor();
    for (const id of ['rw-could', 'rw-fill-03']) {
      const posts = fixture.state.requests.filter(
        (item) => item.method === 'POST' && item.path === `/api/v1/opportunities/${id}/decision`,
      );
      assert.equal(posts.length, 1, `one guarded decision POST for ${id}`);
      assert.equal(posts[0].payload.decision, 'selected');
      assert.equal(posts[0].payload.expectedOpportunityRevision, 2);
      assert.equal(posts[0].payload.expectedDecisionRevision, 0);
      assert.ok(typeof posts[0].payload.requestKey === 'string' && posts[0].payload.requestKey !== '');
    }
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/checks')).length,
      0,
      'selection alone must start no check and read no check state',
    );

    // Check pages: every state, GET-only mounts, traced questions.
    const mutationsBeforeChecks = nonAuthMutations(fixture.state.requests).length;

    await page.goto(`${fixture.url}#/jobs/rw-could/check`, { waitUntil: 'networkidle' });
    await page.getByText('No check yet').waitFor();
    await page.getByRole('button', { name: 'Start check' }).waitFor();

    await page.goto(`${fixture.url}#/jobs/rw-checking/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check in progress…').waitFor();
    assert.equal(await page.getByRole('button', { name: 'Start check' }).count(), 0);
    assert.equal(await page.getByRole('button', { name: 'Retry check' }).count(), 0);

    await page.goto(`${fixture.url}#/jobs/rw-rec/check`, { waitUntil: 'networkidle' });
    await page.getByText('Saved vacancy').waitFor();
    await page.getByText('Can you work remotely from Berlin?').first().waitFor();
    const recQuestions = await scrapeCheckQuestions(page);
    assert.equal(recQuestions.length, 2);
    for (const displayed of recQuestions) await assertTracedToCapture(fixture.url, displayed);
    const recAnswerLink = page.getByRole('link', { name: 'Answer questions' });
    assert.equal(await recAnswerLink.count(), 1);
    assert.equal(await recAnswerLink.getAttribute('href'), '#/jobs/rw-rec/answers');

    await page.goto(`${fixture.url}#/jobs/rw-prob/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check blocked').first().waitFor();
    await page.getByText('Blocked — source_unavailable: The posting returned HTTP 404.').waitFor();
    const probQuestions = await scrapeCheckQuestions(page);
    assert.equal(probQuestions.length, 1);
    for (const displayed of probQuestions) await assertTracedToCapture(fixture.url, displayed);
    await page.getByRole('button', { name: 'Retry check' }).waitFor();

    await page.goto(`${fixture.url}#/jobs/rw-not/check`, { waitUntil: 'networkidle' });
    await page.getByText('Saved check is outdated').waitFor();
    const notQuestions = await scrapeCheckQuestions(page);
    assert.equal(notQuestions.length, 1);
    for (const displayed of notQuestions) await assertTracedToCapture(fixture.url, displayed);
    assert.equal(
      await page.getByRole('link', { name: 'Answer questions' }).count(),
      0,
      'outdated checks must not link into Answer questions',
    );
    await page.getByRole('button', { name: 'Run a new check' }).waitFor();

    const checkRequestsBeforeOpen = fixture.state.requests.filter((item) =>
      item.path.includes('/opportunities/rw-open/checks'),
    ).length;
    await page.goto(`${fixture.url}#/jobs/rw-open/check`, { waitUntil: 'networkidle' });
    await page.getByText('Role not selected').waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/opportunities/rw-open/checks')).length,
      checkRequestsBeforeOpen,
      'unselected check page must not touch check endpoints',
    );
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      mutationsBeforeChecks,
      'check page visits must commission no work',
    );

    // Answers honest states: no editable boxes, GET-only.
    async function expectNoAnswerBoxes(jobId, heading) {
      await page.goto(`${fixture.url}#/jobs/${jobId}/answers`, { waitUntil: 'networkidle' });
      await page.getByText(heading).first().waitFor();
      assert.equal(await page.getByLabel('Your answer').count(), 0, `${jobId} answers must show no editable box`);
    }
    const mutationsBeforeAnswers = nonAuthMutations(fixture.state.requests).length;
    await expectNoAnswerBoxes('rw-open', 'Role not selected');
    await expectNoAnswerBoxes('rw-could', 'No check yet');
    await expectNoAnswerBoxes('rw-checking', 'Check in progress');
    await expectNoAnswerBoxes('rw-prob', 'Check blocked');
    await expectNoAnswerBoxes('rw-not', 'Check outdated');
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      mutationsBeforeAnswers,
      'answers honest-state visits must commission no work',
    );

    // Answers checked flow: Jev prefill vs approved answer, no-fit blank.
    await page.goto(`${fixture.url}#/jobs/rw-rec/answers`, { waitUntil: 'networkidle' });
    await page.getByText('1. Can you work remotely from Berlin?').waitFor();
    await page.getByText('2. When can you start?').waitFor();
    assert.equal(await page.getByLabel('Your answer').count(), 2);
    const recBoxes = page.getByLabel('Your answer');
    assert.equal(await recBoxes.nth(0).inputValue(), 'I work remotely from Berlin, async-first.');
    assert.equal(await recBoxes.nth(1).inputValue(), '');
    await page.getByText('Prefilled from a saved suggestion; change it or clear the box before saving.').waitFor();
    await page.getByText('No saved answer fit this question; the box starts blank.').waitFor();
    const recAnswerQuestions = await scrapeAnswerQuestions(page);
    assert.equal(recAnswerQuestions.length, 2);
    for (const displayed of recAnswerQuestions) await assertTracedToCapture(fixture.url, displayed);
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'PUT').length,
      0,
      'answers mount must issue no PUT',
    );

    // Exact edit round-trip: byte-identical save, then version guard.
    const exact = '  Hybrid: 2–3 days ✅\nSecond line.  ';
    await recBoxes.nth(1).fill(exact);
    await page.getByRole('button', { name: 'Save answer' }).nth(1).click();
    await page.getByText('Saved · v1.').waitFor();
    const recPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/rw-rec/questions/q-rec-2/answer',
    );
    assert.equal(recPuts.length, 1);
    assert.deepEqual(recPuts[0].payload, { expectedAnswerVersion: 0, text: exact });
    assert.deepEqual(Object.keys(recPuts[0].payload).sort(), ['expectedAnswerVersion', 'text']);
    await recBoxes.nth(1).fill(`${exact}!`);
    await page.getByRole('button', { name: 'Save answer' }).nth(1).click();
    await page.getByText('Saved · v2.').waitFor();

    // Explicit blank: clear the suggestion, save empty, reload round-trip.
    await recBoxes.nth(0).fill('');
    await page.getByRole('button', { name: 'Save answer' }).nth(0).click();
    await page.getByText('Blank saved · v1.').waitFor();
    const blankPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/rw-rec/questions/q-rec-1/answer',
    );
    assert.equal(blankPuts.length, 1);
    assert.deepEqual(blankPuts[0].payload, { expectedAnswerVersion: 0, text: '' });
    await page.getByText('Explicitly left blank').first().waitFor();
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('1. Can you work remotely from Berlin?').waitFor();
    const reloadedBoxes = page.getByLabel('Your answer');
    assert.equal(await reloadedBoxes.nth(0).inputValue(), '');
    assert.equal(await reloadedBoxes.nth(1).inputValue(), `${exact}!`);

    // Back/forward across check <-> answers keeps per-role state.
    await page.goto(`${fixture.url}#/jobs/rw-rec/check`, { waitUntil: 'networkidle' });
    await page.getByText('Saved vacancy').waitFor();
    await page.goto(`${fixture.url}#/jobs/rw-rec/answers`, { waitUntil: 'networkidle' });
    await page.getByText('1. Can you work remotely from Berlin?').waitFor();
    await page.goBack({ waitUntil: 'networkidle' });
    await page.getByText('Saved vacancy').waitFor();
    await page.getByText('Can you work remotely from Berlin?').first().waitFor();
    await page.goForward({ waitUntil: 'networkidle' });
    await page.getByText('2. When can you start?').waitFor();
    assert.equal(await page.getByLabel('Your answer').count(), 2);

    // Long-list keyboard access: Tab from the top of the Jobs page must
    // reach the sticky bulk action through native tab order, then Enter
    // starts checks for exactly the six chosen roles.
    await page.goto(`${fixture.url}#/jobs`, { waitUntil: 'networkidle' });
    await page.getByRole('button', { name: 'Check chosen jobs (6)' }).waitFor();
    let keyboardReached = false;
    for (let tab = 0; tab < 600; tab += 1) {
      const focused = await page.evaluate(() =>
        (document.activeElement?.textContent ?? '').trim().slice(0, 32),
      );
      if (focused.startsWith('Check chosen jobs (')) {
        keyboardReached = true;
        break;
      }
      await page.keyboard.press('Tab');
    }
    assert.ok(keyboardReached, 'Tab must reach Check chosen jobs on the long list');
    const checksBeforeBulk = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path.includes('/checks'),
    ).length;
    await page.keyboard.press('Enter');
    await page.getByText('Check complete —').waitFor();
    await page.getByText('Check blocked —').waitFor();
    assert.equal(await page.getByText('Check pending —').count(), 4);
    const bulkPosts = fixture.state.requests
      .filter((item) => item.method === 'POST' && item.path.includes('/checks'))
      .slice(checksBeforeBulk);
    assert.equal(bulkPosts.length, 6);
    assert.deepEqual(
      new Set(bulkPosts.map((item) => item.path)),
      new Set([
        '/api/v1/opportunities/rw-rec/checks',
        '/api/v1/opportunities/rw-could/checks',
        '/api/v1/opportunities/rw-checking/checks',
        '/api/v1/opportunities/rw-prob/checks',
        '/api/v1/opportunities/rw-not/checks',
        '/api/v1/opportunities/rw-fill-03/checks',
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
      6,
      'each role carries an independent request key',
    );
    assert.equal(
      fixture.state.requests.filter(
        (item) =>
          item.method === 'POST' &&
          item.path.includes('/checks') &&
          (item.path.includes('rw-open') || item.path.includes('rw-fill-0') || item.path.includes('rw-fill-1')) &&
          !item.path.includes('rw-fill-03'),
      ).length,
      0,
      'unselected roles must never receive a check commission',
    );

    // Server-side guard probe: an unselected role cannot be checked directly.
    const guardProbe = await fetch(`${fixture.url}/api/v1/opportunities/rw-open/checks`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        requestKey: 'rw-e3-guard-probe',
        expectedOpportunityRevision: 2,
        expectedWorkflowRevision: 1,
      }),
    });
    assert.equal(guardProbe.status, 422);

    // Connected downstream unlock for rw-could: checking -> refresh -> checked.
    await page.goto(`${fixture.url}#/jobs/rw-could/check`, { waitUntil: 'networkidle' });
    await page.getByText('Check in progress…').waitFor();
    await page.getByRole('button', { name: 'Refresh saved check' }).click();
    await page.getByText('Describe your remote experience.').first().waitFor();
    const couldQuestions = await scrapeCheckQuestions(page);
    assert.equal(couldQuestions.length, 2);
    for (const displayed of couldQuestions) await assertTracedToCapture(fixture.url, displayed);
    await page.goto(`${fixture.url}#/jobs/rw-could/answers`, { waitUntil: 'networkidle' });
    await page.getByText('1. Describe your remote experience.').waitFor();
    const couldBoxes = page.getByLabel('Your answer');
    assert.equal(await couldBoxes.nth(0).inputValue(), 'I have 4 years of remote product work.');
    assert.equal(await couldBoxes.nth(1).inputValue(), '');
    const couldAnswerQuestions = await scrapeAnswerQuestions(page);
    assert.equal(couldAnswerQuestions.length, 2);
    for (const displayed of couldAnswerQuestions) await assertTracedToCapture(fixture.url, displayed);

    // Answer surfaces issue zero Codex/LLM calls: no match POST, no
    // codex/research-run/material/round traffic anywhere in the journey.
    const forbidden = fixture.state.requests.filter(
      (item) =>
        (item.path.includes('/answers/match') && item.method !== 'GET') ||
        item.path.includes('/codex') ||
        item.path.toLowerCase().includes('/llm') ||
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
    assert.ok(puts.length >= 3, `expected at least 3 answer PUTs, saw ${puts.length}`);
    assert.ok(
      puts.every((item) => item.path.includes('/questions/') && item.path.endsWith('/answer')),
      `PUTs must target only question answers: ${JSON.stringify(puts.map((item) => item.path))}`,
    );
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log(
      'rw-e3 smoke: multi-group selection starts nothing, unselected never starts (UI + 422 guard), every displayed question traces to served capture spans, checked/outdated Answer-link gating, blocked/outdated honesty, Jev prefill vs no-fit, exact edits/blanks round-trip, back/forward, long-list keyboard bulk start, zero Codex/LLM answer calls OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('rw-e3 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('rw-e3 smoke failed:', cause);
  process.exitCode = 1;
});
