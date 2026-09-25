import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// Slice 4 gate (G4): connected prepare -> exact edit -> explicit rewrite ->
// review-authorize flow through the UI against a deterministic in-file API
// double. Covers grounded drafting with optional blanks left blank,
// missing-required held (no review link), exact-edit round-trip with v2
// adoption, a concurrent-revision 409 through the UI, reload persistence,
// one explicit rewrite (never implicit), provenance/readiness surviving every
// version change, zero automatic employer contact, and stale-approval reuse
// refused after a post-approval edit. Nothing here is a live model run:
// preparation, packs and reviews are canned double data; the handoff
// distinguishes this deterministic coverage from real-run evidence.
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

function role(id, title) {
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
  };
}

const roles = [role('job-prep', 'Backend Engineer'), role('job-held', 'Support Engineer'), role('job-open', 'Mystery Role')];
const titles = new Map(roles.map((item) => [item.id, item.title]));

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

function checkFixture(jobId, questions, setSha) {
  return {
    id: `check-${jobId}`,
    opportunityId: jobId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status: 'checked',
    vacancy: {
      captureIds: ['cap-1'],
      evidenceSourceIds: ['src-1'],
      completeness: 'complete',
      sourceUrl: `https://example.invalid/jobs/${jobId}`,
      retrievedAt: time,
    },
    requestedDocuments: [],
    route: {
      judgment: 'application_route',
      kind: 'direct',
      destinationText: 'Apply through the portal.',
      sourceExcerpt: 'Apply through the portal.',
      observedAt: time,
    },
    gaps: [],
    questions,
    questionSetSha256: setSha,
    questionSetVersion: 1,
    createdAt: time,
    createdBy: { actorKind: 'codex', actorId: 'codex' },
  };
}

const prepCheck = checkFixture(
  'job-prep',
  [
    question(
      'check-job-prep',
      'q-prep-req',
      0,
      'Can you work remotely from Berlin?',
      'required',
      'Can you work remotely from Berlin? (posting paragraph 2)',
      'sha-prep-req',
    ),
    question(
      'check-job-prep',
      'q-prep-opt',
      1,
      'When can you start?',
      'optional',
      'When can you start? (application form Q2)',
      'sha-prep-opt',
    ),
  ],
  'set-prep',
);

const heldCheck = checkFixture(
  'job-held',
  [
    question(
      'check-job-held',
      'q-held-req',
      0,
      'Describe your on-call experience.',
      'required',
      'Describe your on-call experience. (posting line 9)',
      'sha-held-req',
    ),
    question(
      'check-job-held',
      'q-held-opt',
      1,
      'Anything else to add?',
      'optional',
      'Anything else to add? (form Q3)',
      'sha-held-opt',
    ),
  ],
  'set-held',
);

const PREP_FOCUS = 'Application focus: remote backend work from Berlin.';
const PREP_COVER = 'Cover: async-first backend engineer with 6 years of experience.';
const PREP_REQ_TEXT = 'Can you work remotely from Berlin?';
const PREP_REQ_ANSWER = 'I work remotely from Berlin, async-first.';
const PREP_OPT_TEXT = 'When can you start?';
const HELD_REQ_TEXT = 'Describe your on-call experience.';

function materialVersion({ jobId, version, checkId, setSha, answers, readiness, origin, rewriteOf, createdBy }) {
  return {
    packId: `pack-${jobId}-${version}`,
    version,
    opportunityId: jobId,
    opportunityRevision: 2,
    profileRevision: 3,
    checkId,
    questionSetSha256: setSha,
    answers,
    readiness,
    provenance: {
      origin,
      sourceShas: ['career-sha-1', 'role-sha-2'],
      ...(rewriteOf === undefined ? {} : { rewriteOf }),
    },
    createdAt: time,
    createdBy,
  };
}

function packDetail({ id, jobId, version, focus, cover, answers, unknowns }) {
  return {
    id,
    opportunityId: jobId,
    opportunityRevision: 2,
    profileRevision: 3,
    version,
    contentSha256: `pack-sha-${id}`,
    createdAt: time,
    manifest: {
      role: {
        opportunityId: jobId,
        opportunityRevision: 2,
        profileRevision: 3,
        title: titles.get(jobId) ?? jobId,
        company: 'Example Corp',
        sourceUrl: `https://example.invalid/jobs/${jobId}`,
        description: `Synthetic role description for ${titles.get(jobId) ?? jobId}.`,
        destination: 'portal',
      },
      sources: [],
      draft: {
        focus: { text: focus, citations: [] },
        cover: cover.map((text) => ({ text, citations: [] })),
        answers: answers.map((answer) => ({
          question: answer.question,
          lines: answer.lines.map((text) => ({ text, citations: [] })),
        })),
        materialUnknowns: unknowns,
        relevance: [],
      },
      templateSha256: 'template-sha',
    },
  };
}

function packSummary(pack) {
  return {
    id: pack.id,
    opportunityId: pack.opportunityId,
    opportunityRevision: pack.opportunityRevision,
    profileRevision: pack.profileRevision,
    version: pack.version,
    contentSha256: pack.contentSha256,
    createdAt: pack.createdAt,
  };
}

function matSha(jobId, version) {
  return `matsha-${jobId}-v${version}`;
}

function workflowFixture(jobId, stage) {
  return {
    opportunityId: jobId,
    stage,
    revision: 1,
    updatedAt: time,
    decisionAt: time,
    opportunityRevision: 2,
  };
}

function decisionFixture(jobId) {
  return {
    id: `synthetic-decision-${jobId}`,
    opportunityId: jobId,
    decision: 'selected',
    revision: 1,
    opportunityRevision: 2,
    auditId: 'synthetic-audit',
    createdAt: time,
  };
}

function routeFixture(jobId) {
  return {
    sourceKind: 'posting',
    sourceExcerpt: 'Apply through the portal.',
    observedAt: time,
    id: `route-${jobId}`,
    opportunityId: jobId,
    kind: 'direct',
    destinationText: 'Apply through the portal.',
    revision: 1,
    createdAt: time,
    updatedAt: time,
  };
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': body.length });
  res.end(body);
}
function error(res, status) {
  sendJson(res, status, { error: { message: `Slice4 fixture HTTP ${status}` } });
}
async function bodyJson(req) {
  const chunks = [];
  let size = 0;
  for await (const chunk of req) {
    size += chunk.length;
    if (size > 200_000) throw new Error('Fixture request too large');
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

async function startSlice4Fixture() {
  const heldVersion = materialVersion({
    jobId: 'job-held',
    version: 1,
    checkId: 'check-job-held',
    setSha: 'set-held',
    answers: [
      { questionId: 'q-held-req', questionTextSha256: 'sha-held-req', answerVersion: 0, textSha256: 'draft-sha-held-req' },
      { questionId: 'q-held-opt', questionTextSha256: 'sha-held-opt', answerVersion: 0, textSha256: 'draft-sha-held-opt' },
    ],
    readiness: { ready: false, missingRequired: ['q-held-req'], held: ['q-held-req'] },
    origin: 'prepared',
    createdBy: { actorKind: 'codex', actorId: 'codex' },
  });
  const heldPack = packDetail({
    id: 'pack-job-held-1',
    jobId: 'job-held',
    version: 1,
    focus: 'Application focus: on-call support work.',
    cover: [],
    answers: [],
    unknowns: ['On-call experience'],
  });
  const state = {
    expireSession: true,
    decisions: new Map([
      ['job-prep', decisionFixture('job-prep')],
      ['job-held', decisionFixture('job-held')],
    ]),
    workflows: new Map([
      ['job-prep', workflowFixture('job-prep', 'checked')],
      ['job-held', workflowFixture('job-held', 'checked')],
    ]),
    checks: new Map([
      ['job-prep', { status: 'checked', check: prepCheck }],
      ['job-held', { status: 'checked', check: heldCheck }],
    ]),
    answerValues: new Map([
      [
        'job-prep',
        {
          checkId: 'check-job-prep',
          questionSetSha256: 'set-prep',
          values: [
            {
              questionId: 'q-prep-req',
              questionTextSha256: 'sha-prep-req',
              required: 'required',
              version: 1,
              state: 'answered',
              text: PREP_REQ_ANSWER,
              textSha256: 'saved-sha-prep-req',
              provenance: {
                origin: 'owner_written',
                editedAt: time,
                editedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
              },
              updatedAt: time,
            },
            {
              questionId: 'q-prep-opt',
              questionTextSha256: 'sha-prep-opt',
              required: 'optional',
              version: 1,
              state: 'blank',
              text: '',
              provenance: {
                origin: 'carried_blank',
                editedAt: time,
                editedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
              },
              updatedAt: time,
            },
          ],
        },
      ],
      ['job-held', { checkId: 'check-job-held', questionSetSha256: 'set-held', values: [] }],
    ]),
    materials: new Map([['job-held', { status: 'held', current: heldVersion }]]),
    packs: new Map([[heldPack.id, heldPack]]),
    reviews: new Map(),
    requests: [],
  };

  function currentMatSha(jobId) {
    const entry = state.materials.get(jobId);
    if (entry?.current === undefined) return null;
    return matSha(jobId, entry.current.version);
  }

  function storeVersion(jobId, status, version, pack) {
    state.materials.set(jobId, { status, current: version });
    state.packs.set(pack.id, pack);
    return version;
  }

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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'slice4-fixture' });
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
      const packDetailMatch = path.match(/^\/api\/v1\/application-packs\/([^/]+)$/);
      if (packDetailMatch?.[1] !== undefined && req.method === 'GET') {
        const pack = state.packs.get(decodeURIComponent(packDetailMatch[1]));
        return pack === undefined ? error(res, 404) : sendJson(res, 200, pack);
      }
      if (path === '/api/v1/delivery/reviews' && req.method === 'POST') {
        const packIds = payload?.packIds;
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          !Array.isArray(packIds) ||
          packIds.length !== 1 ||
          typeof packIds[0] !== 'string'
        )
          return error(res, 400);
        const pack = state.packs.get(packIds[0]);
        if (pack === undefined) return error(res, 404);
        const entry = state.materials.get(pack.opportunityId);
        if (entry?.current === undefined || entry.status !== 'prepared' || !entry.current.readiness.ready)
          return error(res, 400);
        const id = `review-${state.reviews.size + 1}`;
        const review = {
          id,
          materialSha256: matSha(pack.opportunityId, entry.current.version),
          jobId: pack.opportunityId,
          items: [
            {
              id: `item-${id}`,
              reviewId: id,
              packId: pack.id,
              opportunityId: pack.opportunityId,
              opportunityRevision: 2,
              sourceSha256: 'role-sha-2',
              profileRevision: 3,
              packContentSha256: pack.contentSha256,
              routeId: `route-${pack.opportunityId}`,
              routeRevision: 1,
              routeSha256: 'route-sha',
              title: titles.get(pack.opportunityId) ?? pack.opportunityId,
              companyName: 'Example Corp',
              routeExcerpt: 'Apply through the portal.',
              recipient: 'hiring@example.invalid',
              sender: 'owner@example.invalid',
              subject: 'Application',
              body: 'Synthetic review body.',
              attachmentSha256: 'attachment-sha',
              mimeSha256: 'mime-sha',
              messageId: `<${id}@example.invalid>`,
              state: 'prepared',
              current: true,
            },
          ],
        };
        state.reviews.set(id, review);
        const { jobId: _jobId, ...wire } = review;
        return sendJson(res, 201, wire);
      }
      const approveMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/approve$/);
      if (approveMatch?.[1] !== undefined && req.method === 'POST') {
        const review = state.reviews.get(decodeURIComponent(approveMatch[1]));
        if (review === undefined) return error(res, 404);
        if (payload?.materialSha256 !== review.materialSha256) return error(res, 409);
        if (review.materialSha256 !== currentMatSha(review.jobId)) return error(res, 409);
        review.approvedAt = time;
        review.approvedSha256 = review.materialSha256;
        const { jobId: _jobId, ...wire } = review;
        return sendJson(res, 200, wire);
      }
      const sendMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/send$/);
      if (sendMatch?.[1] !== undefined && req.method === 'POST') {
        const review = state.reviews.get(decodeURIComponent(sendMatch[1]));
        if (review === undefined) return error(res, 404);
        if (review.approvedAt === undefined || review.approvedSha256 !== currentMatSha(review.jobId))
          return error(res, 409);
        return error(res, 500);
      }
      const reconcileMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/(reconcile|close)$/);
      if (reconcileMatch?.[1] !== undefined && req.method === 'POST') return error(res, 501);
      const oppLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/(decision|workflow|routes|application-packs)$/);
      if (oppLeaf?.[1] !== undefined && oppLeaf[2] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(oppLeaf[1]);
        const leaf = oppLeaf[2];
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        if (leaf === 'decision') {
          const saved = state.decisions.get(id);
          return saved === undefined ? error(res, 404) : sendJson(res, 200, saved);
        }
        if (leaf === 'workflow') {
          const saved = state.workflows.get(id);
          return saved === undefined ? error(res, 404) : sendJson(res, 200, saved);
        }
        if (leaf === 'routes') {
          return sendJson(
            res,
            200,
            state.workflows.has(id) ? { items: [routeFixture(id)] } : { items: [] },
          );
        }
        const items = [...state.packs.values()]
          .filter((pack) => pack.opportunityId === id)
          .sort((a, b) => a.version - b.version)
          .map(packSummary);
        return sendJson(res, 200, { items });
      }
      const currentCheck = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/checks\/current$/);
      if (currentCheck?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(currentCheck[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, state.checks.get(id) ?? { status: 'not_checked' });
      }
      const valuesCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/answers\/current$/);
      if (valuesCurrent?.[1] !== undefined && req.method === 'GET') {
        const entry = state.answerValues.get(decodeURIComponent(valuesCurrent[1]));
        return entry === undefined ? error(res, 404) : sendJson(res, 200, entry);
      }
      const prepareMatch = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/materials\/prepare$/);
      if (prepareMatch?.[1] !== undefined && req.method === 'POST') {
        const id = decodeURIComponent(prepareMatch[1]);
        const workflow = state.workflows.get(id);
        const checked = state.checks.get(id);
        if (workflow === undefined || checked?.status !== 'checked' || checked.check === undefined)
          return error(res, 409);
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          payload.expectedCheckId !== checked.check.id ||
          payload.expectedQuestionSetSha256 !== checked.check.questionSetSha256 ||
          payload.expectedWorkflowRevision !== workflow.revision
        )
          return error(res, 409);
        const existing = state.materials.get(id);
        if (existing?.current !== undefined) return sendJson(res, 200, existing);
        const values = state.answerValues.get(id)?.values ?? [];
        const byQuestion = new Map(values.map((value) => [value.questionId, value]));
        const answers = checked.check.questions.map((item) => {
          const value = byQuestion.get(item.id);
          const answered = value !== undefined && value.state === 'answered';
          return {
            questionId: item.id,
            questionTextSha256: item.textSha256,
            answerVersion: answered ? value.version : 0,
            textSha256: answered && value.textSha256 !== undefined ? value.textSha256 : `draft-sha-${id}-${item.id}`,
          };
        });
        const missingRequired = checked.check.questions
          .filter((item) => item.required === 'required')
          .filter((item) => {
            const value = byQuestion.get(item.id);
            return value === undefined || value.state !== 'answered';
          })
          .map((item) => item.id);
        const ready = missingRequired.length === 0;
        const version = materialVersion({
          jobId: id,
          version: 1,
          checkId: checked.check.id,
          setSha: checked.check.questionSetSha256,
          answers,
          readiness: { ready, missingRequired, held: [...missingRequired] },
          origin: 'prepared',
          createdBy: { actorKind: 'codex', actorId: 'codex' },
        });
        const answeredLines = checked.check.questions
          .filter((item) => {
            const value = byQuestion.get(item.id);
            return value !== undefined && value.state === 'answered';
          })
          .map((item) => ({ question: item.text, lines: [byQuestion.get(item.id).text] }));
        const pack = packDetail({
          id: version.packId,
          jobId: id,
          version: 1,
          focus: id === 'job-prep' ? PREP_FOCUS : `Application focus for ${titles.get(id) ?? id}.`,
          cover: id === 'job-prep' ? [PREP_COVER] : [`Cover line for ${titles.get(id) ?? id}.`],
          answers: answeredLines,
          unknowns: missingRequired.map((qid) => checked.check.questions.find((item) => item.id === qid).text),
        });
        storeVersion(id, ready ? 'prepared' : 'held', version, pack);
        return sendJson(res, 201, state.materials.get(id));
      }
      const materialsCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/materials\/current$/);
      if (materialsCurrent?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(materialsCurrent[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, state.materials.get(id) ?? { status: 'not_prepared' });
      }
      if (materialsCurrent?.[1] !== undefined && req.method === 'PUT') {
        const id = decodeURIComponent(materialsCurrent[1]);
        const entry = state.materials.get(id);
        if (entry?.current === undefined) return error(res, 409);
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          typeof payload?.expectedVersion !== 'number' ||
          typeof payload?.text !== 'string'
        )
          return error(res, 400);
        if (payload.text === '' || Buffer.byteLength(payload.text, 'utf8') > 100_000) return error(res, 400);
        if (payload.expectedVersion !== entry.current.version) return error(res, 409);
        const version = materialVersion({
          jobId: id,
          version: entry.current.version + 1,
          checkId: entry.current.checkId,
          setSha: entry.current.questionSetSha256,
          answers: entry.current.answers,
          readiness: entry.current.readiness,
          origin: 'direct_edit',
          createdBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
        });
        const pack = packDetail({
          id: version.packId,
          jobId: id,
          version: version.version,
          focus: payload.text,
          cover: [],
          answers: [],
          unknowns: [],
        });
        storeVersion(id, entry.status, version, pack);
        return sendJson(res, 200, version);
      }
      const rewriteMatch = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/materials\/rewrite$/);
      if (rewriteMatch?.[1] !== undefined && req.method === 'POST') {
        const id = decodeURIComponent(rewriteMatch[1]);
        const entry = state.materials.get(id);
        if (entry?.current === undefined) return error(res, 409);
        if (
          typeof payload?.requestKey !== 'string' ||
          payload.requestKey === '' ||
          typeof payload?.expectedVersion !== 'number' ||
          (payload.instruction !== undefined && typeof payload.instruction !== 'string')
        )
          return error(res, 400);
        if (payload.expectedVersion !== entry.current.version) return error(res, 409);
        const base = entry.current;
        const instruction = payload.instruction ?? '';
        const version = materialVersion({
          jobId: id,
          version: base.version + 1,
          checkId: base.checkId,
          setSha: base.questionSetSha256,
          answers: base.answers,
          readiness: base.readiness,
          origin: 'rewrite',
          rewriteOf: base.version,
          createdBy: { actorKind: 'codex', actorId: 'codex' },
        });
        const firstQuestion = state.checks.get(id)?.check?.questions[0]?.text ?? 'Saved question';
        const pack = packDetail({
          id: version.packId,
          jobId: id,
          version: version.version,
          focus: instruction === '' ? `Straight rewrite of v${base.version}.` : `Rewrite of v${base.version} (${instruction})`,
          cover: ['Rewritten cover line.'],
          answers: [{ question: firstQuestion, lines: ['Rewritten answer line.'] }],
          unknowns: [],
        });
        storeVersion(id, entry.status, version, pack);
        return sendJson(res, 200, version);
      }
      const versionMatch = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/materials\/versions\/(\d+)$/);
      if (versionMatch?.[1] !== undefined && versionMatch[2] !== undefined && req.method === 'GET') {
        const entry = state.materials.get(decodeURIComponent(versionMatch[1]));
        if (entry?.current === undefined || entry.current.version !== Number(versionMatch[2]))
          return error(res, 404);
        return sendJson(res, 200, entry.current);
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
      sendJson(res, 500, { error: { message: `Slice4 fixture error: ${cause.message}` } });
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

function employerContactRequests(requests) {
  return requests.filter(
    (item) =>
      item.path.includes('/send') ||
      item.path.includes('/reconcile') ||
      item.path.includes('/close') ||
      item.path.includes('/rounds') ||
      item.path.includes('/codex'),
  );
}

async function fetchJson(page, url, method, body) {
  return page.evaluate(
    async ({ url: target, method: verb, body: data }) => {
      const res = await fetch(target, {
        method: verb,
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
      });
      const text = await res.text();
      let parsed = null;
      try {
        parsed = JSON.parse(text);
      } catch {
        parsed = null;
      }
      return { status: res.status, body: parsed };
    },
    { url, method, body },
  );
}

// Provenance + readiness must survive every version change: same check pins,
// revisions, source SHAs and Ready/Held badge on each new version, plus the
// matching review-link state.
async function expectVersionPanel(page, { version, originLabel, ready, checkId, setSha, packId }) {
  const panel = page.getByRole('region', { name: `Material version ${version}` });
  await panel.getByRole('heading', { name: `Version ${version}` }).waitFor();
  await panel.getByText(ready ? 'Ready' : 'Held', { exact: true }).first().waitFor();
  await panel.getByText(originLabel).first().waitFor();
  await panel.getByText(checkId).waitFor();
  await panel.getByText(new RegExp(`set ${setSha}`)).waitFor();
  await panel.getByText(/role r2/).waitFor();
  await panel.getByText(/profile r3/).waitFor();
  await panel.getByText(new RegExp(`pack ${packId}`)).waitFor();
  await panel.getByText('career-sha-1').waitFor();
  await panel.getByText('role-sha-2').waitFor();
  if (ready) {
    const link = page.getByRole('link', { name: `Continue to review v${version}` });
    await link.waitFor();
    assert.equal(await link.getAttribute('href'), '#/applications/job-prep/review');
  } else {
    assert.equal(await page.getByRole('link', { name: /Continue to review v/ }).count(), 0);
  }
}

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startSlice4Fixture();
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

    // Unselected role: honest empty state, no check/material traffic.
    const mutationsAtStart = nonAuthMutations(fixture.state.requests).length;
    await page.goto(`${fixture.url}#/jobs/job-open/prepare`, { waitUntil: 'networkidle' });
    await page.getByText('Role not selected').waitFor();
    await page
      .getByText('This role is not selected, so the server keeps no check or material state for it. Only chosen roles can be prepared.')
      .waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/opportunities/job-open/checks')).length,
      0,
      'unselected prepare page must not touch check endpoints',
    );
    assert.equal(
      fixture.state.requests.filter((item) => item.path.includes('/opportunities/job-open/materials')).length,
      0,
      'unselected prepare page must not touch material endpoints',
    );

    // Missing-required held role: Held badge, missing item listed, no review
    // link, mount commissions nothing.
    await page.goto(`${fixture.url}#/jobs/job-held/prepare`, { waitUntil: 'networkidle' });
    const heldPanel = page.getByRole('region', { name: 'Material version 1' });
    await heldPanel.getByRole('heading', { name: 'Version 1' }).waitFor();
    await heldPanel.getByText('Held', { exact: true }).first().waitFor();
    await page.getByText(/Held: 1 required item still needs an owner-known fact\./).waitFor();
    await page.getByRole('heading', { name: 'Unanswered required items' }).waitFor();
    await page.getByText(`Missing required: ${HELD_REQ_TEXT}`).waitFor();
    await page.getByText('This version is held: every required item needs an answer before review.').waitFor();
    assert.equal(await page.getByRole('link', { name: /Continue to review v/ }).count(), 0);
    await page.getByText(`1. ${HELD_REQ_TEXT}`).waitFor();
    await page.getByText('Unknown: On-call experience').waitFor();
    assert.equal(
      nonAuthMutations(fixture.state.requests).length,
      mutationsAtStart,
      'held prepare mount must commission no work',
    );

    // Prepare flow: grounded v1 from the completed check, optional blank left
    // blank in the drafted pack.
    await page.goto(`${fixture.url}#/jobs/job-prep/prepare`, { waitUntil: 'networkidle' });
    await page.getByText('No prepared application yet').waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path.endsWith('/materials/prepare')).length,
      0,
      'prepare mount must not prepare',
    );
    await page.getByRole('button', { name: 'Prepare application' }).click();
    await page.getByRole('heading', { name: 'Version 1' }).waitFor();
    await expectVersionPanel(page, {
      version: 1,
      originLabel: 'Prepared from verified facts',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-1',
    });
    const preparePosts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/opportunities/job-prep/materials/prepare',
    );
    assert.equal(preparePosts.length, 1);
    assert.deepEqual(Object.keys(preparePosts[0].payload).sort(), [
      'expectedCheckId',
      'expectedQuestionSetSha256',
      'expectedWorkflowRevision',
      'requestKey',
    ]);
    assert.equal(preparePosts[0].payload.expectedCheckId, 'check-job-prep');
    assert.equal(preparePosts[0].payload.expectedQuestionSetSha256, 'set-prep');
    assert.equal(preparePosts[0].payload.expectedWorkflowRevision, 1);
    assert.ok(typeof preparePosts[0].payload.requestKey === 'string' && preparePosts[0].payload.requestKey !== '');
    // Per-question refs: required row cites the saved answer, optional blank
    // row is drafted with no saved row.
    await page.getByText(`1. ${PREP_REQ_TEXT}`).waitFor();
    await page.getByText(`2. ${PREP_OPT_TEXT}`).waitFor();
    assert.ok((await page.getByText('Required', { exact: true }).count()) >= 1);
    assert.ok((await page.getByText('Optional', { exact: true }).count()) >= 1);
    await page.getByText(/Saved answer v1/).waitFor();
    await page.getByText('Drafted into this version (no saved answer row)').waitFor();
    // Pack carries focus, cover and the answered question; the optional blank
    // is left out of the drafted pack.
    const packRegion = page.getByRole('region', { name: 'Prepared materials' });
    await packRegion.getByText(PREP_FOCUS).waitFor();
    await packRegion.getByText(PREP_COVER).waitFor();
    await packRegion.getByText(PREP_REQ_ANSWER).waitFor();
    assert.equal(await packRegion.getByText(PREP_OPT_TEXT, { exact: false }).count(), 0);
    assert.equal(await packRegion.getByText(/^Unknown:/).count(), 0);

    // Exact edit round-trip: empty box, deterministic seed, byte-exact save,
    // v2 adoption with provenance/readiness intact.
    const editBoxV1 = page.getByLabel('Material text (replaces v1 byte-exact)');
    await editBoxV1.waitFor();
    assert.equal(await editBoxV1.inputValue(), '');
    const seedV1 = [PREP_FOCUS, PREP_COVER, PREP_REQ_TEXT, PREP_REQ_ANSWER].join('\n\n');
    await page.getByRole('button', { name: 'Start from current rendering' }).click();
    assert.equal(await editBoxV1.inputValue(), seedV1);
    const exactEdit = `${seedV1}\n\nOwner note: available 2–3 days ✅ "exact bytes"  `;
    await editBoxV1.fill(exactEdit);
    await page.getByRole('button', { name: 'Save exact edit' }).click();
    await page.getByRole('heading', { name: 'Version 2' }).waitFor();
    await expectVersionPanel(page, {
      version: 2,
      originLabel: 'Direct owner edit (no model)',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-2',
    });
    await page.getByRole('region', { name: 'Prepared materials' }).getByText(/Owner note: available 2–3 days ✅/).waitFor();
    const putsAfterEdit = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-prep/materials/current',
    );
    assert.equal(putsAfterEdit.length, 1);
    assert.deepEqual(putsAfterEdit[0].payload, { requestKey: putsAfterEdit[0].payload.requestKey, expectedVersion: 1, text: exactEdit });
    assert.deepEqual(Object.keys(putsAfterEdit[0].payload).sort(), ['expectedVersion', 'requestKey', 'text']);

    // Concurrent revision conflict through the UI: a second writer lands v3
    // before our v2 save, so the save 409s and keeps our text; the reload
    // afterwards shows the concurrent winner.
    const editBoxV2 = page.getByLabel('Material text (replaces v2 byte-exact)');
    await editBoxV2.waitFor();
    assert.equal(await editBoxV2.inputValue(), '');
    const staleAttempt = 'Stale-side edit that will conflict.';
    await editBoxV2.fill(staleAttempt);
    const concurrent = await fetchJson(page, '/api/v1/opportunities/job-prep/materials/current', 'PUT', {
      requestKey: 'concurrent-probe-1',
      expectedVersion: 2,
      text: 'Concurrent edit from elsewhere.',
    });
    assert.equal(concurrent.status, 200);
    assert.equal(concurrent.body.version, 3);
    await page.getByRole('button', { name: 'Save exact edit' }).click();
    await page
      .getByText('These materials changed elsewhere. Reload the page to see the current version, then try again.')
      .waitFor();
    assert.equal(await editBoxV2.inputValue(), staleAttempt);
    const putsAfterConflict = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-prep/materials/current',
    );
    assert.equal(putsAfterConflict.length, 3);
    assert.equal(putsAfterConflict[2].payload.expectedVersion, 2);
    assert.equal(putsAfterConflict[2].payload.text, staleAttempt);

    // Reload persistence: the concurrent v3 is what loads, with provenance
    // and readiness surviving the version change.
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Version 3' }).waitFor();
    await expectVersionPanel(page, {
      version: 3,
      originLabel: 'Direct owner edit (no model)',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-3',
    });
    await page.getByRole('region', { name: 'Prepared materials' }).getByText('Concurrent edit from elsewhere.').waitFor();
    const editBoxV3 = page.getByLabel('Material text (replaces v3 byte-exact)');
    assert.equal(await editBoxV3.inputValue(), '');
    await page.getByRole('button', { name: 'Start from current rendering' }).click();
    assert.equal(await editBoxV3.inputValue(), 'Concurrent edit from elsewhere.');

    // Explicit rewrite: zero rewrite POSTs until the explicit click, then one
    // new reviewable version carrying the verbatim instruction.
    const rewritePath = '/api/v1/opportunities/job-prep/materials/rewrite';
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path === rewritePath).length,
      0,
      'reads, prepare, edits and reloads must never rewrite implicitly',
    );
    const instruction = 'Emphasize async-first collaboration.';
    await page.getByLabel(/Instruction \(optional/).fill(instruction);
    await page.getByRole('button', { name: 'Request rewrite' }).click();
    await page.getByRole('heading', { name: 'Version 4' }).waitFor();
    await expectVersionPanel(page, {
      version: 4,
      originLabel: 'Explicit rewrite',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-4',
    });
    await page.getByRole('region', { name: 'Material version 4' }).getByText('Explicit rewrite of v3').waitFor();
    await page.getByRole('region', { name: 'Prepared materials' }).getByText(new RegExp(instruction.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'))).waitFor();
    const rewritePosts = fixture.state.requests.filter((item) => item.method === 'POST' && item.path === rewritePath);
    assert.equal(rewritePosts.length, 1);
    assert.deepEqual(rewritePosts[0].payload, {
      requestKey: rewritePosts[0].payload.requestKey,
      expectedVersion: 3,
      instruction,
    });
    assert.deepEqual(Object.keys(rewritePosts[0].payload).sort(), ['expectedVersion', 'instruction', 'requestKey']);
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Version 4' }).waitFor();
    await expectVersionPanel(page, {
      version: 4,
      originLabel: 'Explicit rewrite',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-4',
    });

    // Review + authorize the current version: explicit prepare then approve,
    // with exact answers (including the optional blank) on display.
    await page.goto(`${fixture.url}#/applications/job-prep/review`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Review application' }).waitFor();
    await page.getByText(PREP_REQ_ANSWER).waitFor();
    await page.getByText('left blank').waitFor();
    await page.getByText('Authorization binds material v4 and its pack. A newer version needs a fresh review.').waitFor();
    await page.getByRole('button', { name: 'Prepare review' }).click();
    await page.getByText(/Review review-1/).waitFor();
    await page.getByText('not approved').waitFor();
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    await page
      .getByText('This authorization covers exactly the pack and digest above. Sending is a separate explicit step.')
      .waitFor();
    const reviewPrepares = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/delivery/reviews',
    );
    assert.equal(reviewPrepares.length, 1);
    assert.deepEqual(reviewPrepares[0].payload, {
      requestKey: reviewPrepares[0].payload.requestKey,
      packIds: ['pack-job-prep-4'],
    });
    const approves = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/delivery/reviews/review-1/approve',
    );
    assert.equal(approves.length, 1);
    assert.deepEqual(approves[0].payload, { materialSha256: 'matsha-job-prep-v4' });

    // No employer contact happened on its own before the deliberate stale
    // probes below: preparing, editing, rewriting, reloading, reviewing and
    // approving commission no send/reconcile/round traffic.
    assert.equal(
      employerContactRequests(fixture.state.requests).length,
      0,
      `no automatic employer contact allowed: ${JSON.stringify(employerContactRequests(fixture.state.requests))}`,
    );

    // Stale approval reuse refused: approve-then-edit, then both a send
    // attempt and an approve-again conflict against the moved version.
    await page.goto(`${fixture.url}#/jobs/job-prep/prepare`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Version 4' }).waitFor();
    const editBoxV4 = page.getByLabel('Material text (replaces v4 byte-exact)');
    await page.getByRole('button', { name: 'Start from current rendering' }).click();
    const seededV4 = await editBoxV4.inputValue();
    assert.ok(seededV4.includes(instruction));
    await editBoxV4.fill(`${seededV4} Post-approval owner tweak.`);
    await page.getByRole('button', { name: 'Save exact edit' }).click();
    await page.getByRole('heading', { name: 'Version 5' }).waitFor();
    await expectVersionPanel(page, {
      version: 5,
      originLabel: 'Direct owner edit (no model)',
      ready: true,
      checkId: 'check-job-prep',
      setSha: 'set-prep',
      packId: 'pack-job-prep-5',
    });
    const staleSend = await fetchJson(page, '/api/v1/delivery/reviews/review-1/send', 'POST', {});
    assert.equal(staleSend.status, 409);
    const staleApprove = await fetchJson(page, '/api/v1/delivery/reviews/review-1/approve', 'POST', {
      materialSha256: 'matsha-job-prep-v4',
    });
    assert.equal(staleApprove.status, 409);
    await page.goto(`${fixture.url}#/applications/job-prep/review`, { waitUntil: 'networkidle' });
    await page.getByText('Authorization binds material v5 and its pack. A newer version needs a fresh review.').waitFor();
    await page.getByRole('button', { name: 'Prepare review' }).waitFor();

    // Final tallies: exactly one rewrite (the explicit click), one prepare,
    // no other employer contact beyond the refused stale-send probe.
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path === rewritePath).length,
      1,
      'rewrite must happen exactly once, from the explicit click',
    );
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path.endsWith('/materials/prepare')).length,
      1,
    );
    const employerHits = employerContactRequests(fixture.state.requests);
    assert.equal(employerHits.length, 1, `only the deliberate stale-send probe may touch employer paths: ${JSON.stringify(employerHits)}`);
    assert.equal(employerHits[0].method, 'POST');
    assert.equal(employerHits[0].path, '/api/v1/delivery/reviews/review-1/send');
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log(
      'slice4 smoke: prepare flow, optional blank, held missing-required, exact edit v2, 409 conflict, reload persistence, explicit rewrite v4, provenance/readiness across versions, approve then stale send/approve refused, zero automatic employer contact OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice4 smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('slice4 smoke failed:', cause);
  process.exitCode = 1;
});
