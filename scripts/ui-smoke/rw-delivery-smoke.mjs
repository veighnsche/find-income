import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// RW-E4 gate: deterministic material/review/delivery edge cases through the
// real prepare/review/send/attempt UI against a self-contained in-file API
// double. Covers grounded prepare with provenance/readiness display, held
// missing-required blocking review, exact-edit byte saves, explicit-rewrite
// versioning, stale-approve disabled with fresh-review recovery, tampered
// approval refused with 409 honesty, repeated clicks POSTing send once, lost
// send response recovered via status read without a duplicate, partial /
// failure / uncertain outcome honesty, read-only reconcile, send hiding while
// the capability is unavailable (and fail-open when the capability read
// fails), and attempt snapshots staying immutable under newer drafts with
// outcome/history inspection. Every employer destination is a .invalid test
// domain; the run fails if any request leaves the fixture origin. Nothing
// here contacts a real employer or spends on a model call: preparation,
// packs, reviews, sends and reconciliations are canned double behavior, and
// the handoff labels this coverage fixture-verified only. Live transport
// proof still needs the RW-D3 sink plus the RW-G4 explicit owner action.
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

const ROLE_DEFS = [
  ['job-ground', 'Backend Engineer'],
  ['job-held', 'Support Engineer'],
  ['job-out', 'Platform Engineer'],
  ['job-noprep', 'Mystery Role'],
  ['job-stale', 'QA Engineer'],
  ['job-tamp', 'Data Engineer'],
  ['job-fail', 'DevOps Engineer'],
  ['job-cap', 'Mobile Engineer'],
  ['job-bind', 'Security Engineer'],
  ['job-lost', 'Research Engineer'],
  ['job-partial', 'Support Lead'],
  ['job-aux', 'Docs Writer'],
  ['job-unc', 'Network Engineer'],
];

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

const roles = ROLE_DEFS.map(([id, title]) => role(id, title));
const titles = new Map(ROLE_DEFS);

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

const GROUND_REQ_TEXT = 'Can you work remotely from Berlin?';
const GROUND_REQ_ANSWER = 'I work remotely from Berlin, async-first.';
const GROUND_OPT_TEXT = 'When can you start?';
const GROUND_FOCUS = 'Application focus: remote backend work from Berlin.';
const GROUND_COVER = 'Cover: async-first backend engineer with 6 years of experience.';
const HELD_REQ_TEXT = 'Describe your on-call experience.';

const groundCheck = checkFixture(
  'job-ground',
  [
    question(
      'check-job-ground',
      'q-ground-req',
      0,
      GROUND_REQ_TEXT,
      'required',
      'Can you work remotely from Berlin? (posting paragraph 2)',
      'sha-ground-req',
    ),
    question(
      'check-job-ground',
      'q-ground-opt',
      1,
      GROUND_OPT_TEXT,
      'optional',
      'When can you start? (application form Q2)',
      'sha-ground-opt',
    ),
  ],
  'set-ground',
);

const heldCheck = checkFixture(
  'job-held',
  [
    question(
      'check-job-held',
      'q-held-req',
      0,
      HELD_REQ_TEXT,
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

function workflowFixture(jobId) {
  return {
    opportunityId: jobId,
    stage: 'checked',
    revision: 1,
    updatedAt: time,
    decisionAt: time,
    opportunityRevision: 2,
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

function answeredValue(jobId, questionId, sha, text) {
  return {
    questionId,
    questionTextSha256: sha,
    required: 'required',
    version: 1,
    state: 'answered',
    text,
    textSha256: `saved-sha-${jobId}-${questionId}`,
    provenance: {
      origin: 'owner_written',
      editedAt: time,
      editedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
    },
    updatedAt: time,
  };
}

function blankValue(questionId, sha) {
  return {
    questionId,
    questionTextSha256: sha,
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
  };
}

function answersFixture(jobId) {
  const title = titles.get(jobId) ?? jobId;
  return {
    checkId: `check-${jobId}`,
    questionSetSha256: `set-${jobId}`,
    values: [
      answeredValue(jobId, `q-${jobId}-req`, `sha-${jobId}-req`, `Owner answer for ${title}: available promptly.`),
      blankValue(`q-${jobId}-opt`, `sha-${jobId}-opt`),
    ],
  };
}

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

function readyVersion(jobId, version) {
  return materialVersion({
    jobId,
    version,
    checkId: `check-${jobId}`,
    setSha: `set-${jobId}`,
    answers: [
      {
        questionId: `q-${jobId}-req`,
        questionTextSha256: `sha-${jobId}-req`,
        answerVersion: 1,
        textSha256: `saved-sha-${jobId}-req`,
      },
      {
        questionId: `q-${jobId}-opt`,
        questionTextSha256: `sha-${jobId}-opt`,
        answerVersion: 0,
        textSha256: `draft-sha-${jobId}-opt`,
      },
    ],
    readiness: { ready: true, missingRequired: [], held: [] },
    origin: 'prepared',
    createdBy: { actorKind: 'codex', actorId: 'codex' },
  });
}

function packDetail({ id, jobId, version, focus, cover, answers, unknowns }) {
  const title = titles.get(jobId) ?? jobId;
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
        title,
        company: 'Example Corp',
        sourceUrl: `https://example.invalid/jobs/${jobId}`,
        description: `Synthetic role description for ${title}.`,
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

function itemFixture({ reviewId, jobId, pack, state, extra }) {
  return {
    id: `item-${reviewId}-${jobId}`,
    reviewId,
    packId: pack.id,
    opportunityId: jobId,
    opportunityRevision: 2,
    sourceSha256: 'role-sha-2',
    profileRevision: 3,
    packContentSha256: pack.contentSha256,
    routeId: `route-${jobId}`,
    routeRevision: 1,
    routeSha256: 'route-sha',
    title: titles.get(jobId) ?? jobId,
    companyName: 'Example Corp',
    routeExcerpt: 'Apply through the portal.',
    recipient: `hiring-${jobId}@example.invalid`,
    sender: 'owner@example.invalid',
    subject: `Application for ${titles.get(jobId) ?? jobId}`,
    body: `Synthetic application body for ${titles.get(jobId) ?? jobId}.`,
    attachmentSha256: `attachment-sha-${pack.id}`,
    mimeSha256: `mime-sha-${pack.id}`,
    messageId: `<${reviewId}-${jobId}@example.invalid>`,
    state,
    current: true,
    ...(extra ?? {}),
  };
}

function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': body.length });
  res.end(body);
}
function error(res, status, message) {
  sendJson(res, status, { error: { message: message ?? `RW-E4 fixture HTTP ${status}` } });
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

const SEND_ROLES = ['job-stale', 'job-tamp', 'job-fail', 'job-cap', 'job-bind', 'job-lost', 'job-partial', 'job-aux', 'job-unc'];

async function startRwDeliveryFixture() {
  const state = {
    expireSession: true,
    // Delivery capability mode: 'ok' | 'unavailable' | 'error'. The run flips
    // it through the control plane to prove send hiding on an explicit
    // submissionAvailable:false and fail-open when the read itself fails.
    capabilityMode: 'ok',
    // One-shot: the next review prepare returns stale items, modelling a
    // concurrent supersede between the materials read and the prepare click.
    staleNextReview: false,
    workflows: new Map([
      ['job-ground', workflowFixture('job-ground')],
      ['job-held', workflowFixture('job-held')],
    ]),
    checks: new Map([
      ['job-ground', { status: 'checked', check: groundCheck }],
      ['job-held', { status: 'checked', check: heldCheck }],
    ]),
    answerValues: new Map([
      [
        'job-ground',
        {
          checkId: 'check-job-ground',
          questionSetSha256: 'set-ground',
          values: [
            answeredValue('job-ground', 'q-ground-req', 'sha-ground-req', GROUND_REQ_ANSWER),
            blankValue('q-ground-opt', 'sha-ground-opt'),
          ],
        },
      ],
      ['job-held', { checkId: 'check-job-held', questionSetSha256: 'set-held', values: [] }],
      ['job-out', { checkId: 'check-job-out', questionSetSha256: 'set-job-out', values: [] }],
      ['job-noprep', { checkId: 'check-job-noprep', questionSetSha256: 'set-job-noprep', values: [] }],
    ]),
    materials: new Map(),
    packs: new Map(),
    reviews: new Map(),
    reviewSeq: 0,
    attempts: new Map(),
    approveResponses: [],
    sendResponses: [],
    reconciles: [],
    requests: [],
  };
  for (const [jobId] of ROLE_DEFS) {
    if (!state.answerValues.has(jobId)) state.answerValues.set(jobId, answersFixture(jobId));
  }
  for (const jobId of SEND_ROLES) {
    const title = titles.get(jobId) ?? jobId;
    const v1 = packDetail({
      id: `pack-${jobId}-1`,
      jobId,
      version: 1,
      focus: `Initial focus for ${title}.`,
      cover: [],
      answers: [],
      unknowns: [],
    });
    const v2 = packDetail({
      id: `pack-${jobId}-2`,
      jobId,
      version: 2,
      focus: `Refined focus for ${title}.`,
      cover: [],
      answers: [],
      unknowns: [],
    });
    state.packs.set(v1.id, v1);
    state.packs.set(v2.id, v2);
    state.materials.set(jobId, { status: 'prepared', current: readyVersion(jobId, 2) });
  }
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
  state.materials.set('job-held', { status: 'held', current: heldVersion });
  state.packs.set(
    'pack-job-held-1',
    packDetail({
      id: 'pack-job-held-1',
      jobId: 'job-held',
      version: 1,
      focus: 'Application focus: on-call support work.',
      cover: [],
      answers: [],
      unknowns: ['On-call experience'],
    }),
  );
  state.materials.set('job-out', { status: 'outdated', current: readyVersion('job-out', 1) });
  state.packs.set(
    'pack-job-out-1',
    packDetail({
      id: 'pack-job-out-1',
      jobId: 'job-out',
      version: 1,
      focus: 'Initial focus for Platform Engineer.',
      cover: [],
      answers: [],
      unknowns: [],
    }),
  );
  // job-noprep deliberately pairs "not_prepared" materials with one listed
  // pack so the review page exercises the not_prepared not-ready message
  // branch (which needs a non-null pack id) instead of the no-pack branch.
  state.packs.set(
    'pack-job-noprep-1',
    packDetail({
      id: 'pack-job-noprep-1',
      jobId: 'job-noprep',
      version: 1,
      focus: 'Orphaned pack rendering for Mystery Role.',
      cover: [],
      answers: [],
      unknowns: [],
    }),
  );

  function currentMatSha(jobId) {
    const entry = state.materials.get(jobId);
    if (entry?.current === undefined) return null;
    return matSha(jobId, entry.current.version);
  }

  function wireReview(review) {
    const { primaryJobId: _p, ...wire } = review;
    return wire;
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
      // Fixture-only control plane (not part of the API): drives the
      // stale-review, tamper and capability scenarios between UI steps.
      if (path === '/__rw4/capability' && req.method === 'POST') {
        const payload = await bodyJson(req);
        if (!['ok', 'unavailable', 'error'].includes(payload?.mode)) return error(res, 400);
        state.capabilityMode = payload.mode;
        return sendJson(res, 200, { mode: state.capabilityMode });
      }
      if (path === '/__rw4/stale-next' && req.method === 'POST') {
        state.staleNextReview = true;
        return sendJson(res, 200, { armed: true });
      }
      if (path === '/__rw4/tamper' && req.method === 'POST') {
        const payload = await bodyJson(req);
        const review = state.reviews.get(payload?.reviewId);
        if (review === undefined) return error(res, 404);
        review.materialSha256 = 'tampered-sha256-not-matching-any-version';
        return sendJson(res, 200, { tampered: true });
      }
      if (path === '/__rw4/tamper-approved' && req.method === 'POST') {
        const payload = await bodyJson(req);
        const review = state.reviews.get(payload?.reviewId);
        if (review === undefined) return error(res, 404);
        review.approvedSha256 = 'tampered-approved-sha256';
        return sendJson(res, 200, { tampered: true });
      }
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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'rw-delivery-fixture' });
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
      if (path === '/api/v1/delivery/capability' && req.method === 'GET') {
        if (state.capabilityMode === 'error') return error(res, 500, 'capability probe forced to fail');
        if (state.capabilityMode === 'unavailable') {
          return sendJson(res, 200, {
            submissionAvailable: false,
            receiptLookup: false,
            reason: 'no sender configured in this test double',
          });
        }
        return sendJson(res, 200, { submissionAvailable: true, receiptLookup: false });
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
          return error(res, 400, 'material is not ready for review');
        if (pack.id !== entry.current.packId) return error(res, 409, 'pack is superseded by a newer version');
        state.reviewSeq += 1;
        const id = `review-${pack.opportunityId}-${state.reviewSeq}`;
        const stale = state.staleNextReview;
        state.staleNextReview = false;
        const items = [
          itemFixture({
            reviewId: id,
            jobId: pack.opportunityId,
            pack,
            state: 'prepared',
            extra: stale ? { current: false, blockingReason: 'superseded' } : {},
          }),
        ];
        if (pack.id === 'pack-job-partial-2') {
          // The double models server-side batching for this one review: the
          // contract allows multi-item reviews and the UI renders each item.
          const auxPack = state.packs.get('pack-job-aux-2');
          items.push(itemFixture({ reviewId: id, jobId: 'job-aux', pack: auxPack, state: 'prepared' }));
        }
        const review = {
          id,
          materialSha256: matSha(pack.opportunityId, entry.current.version),
          primaryJobId: pack.opportunityId,
          items,
        };
        state.reviews.set(id, review);
        return sendJson(res, 201, wireReview(review));
      }
      const reviewRead = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)$/);
      if (reviewRead?.[1] !== undefined && req.method === 'GET') {
        const review = state.reviews.get(decodeURIComponent(reviewRead[1]));
        return review === undefined ? error(res, 404) : sendJson(res, 200, wireReview(review));
      }
      const approveMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/approve$/);
      if (approveMatch?.[1] !== undefined && req.method === 'POST') {
        const review = state.reviews.get(decodeURIComponent(approveMatch[1]));
        if (review === undefined) return error(res, 404);
        const verdict = (status) => {
          state.approveResponses.push({ reviewId: review.id, status });
          return status;
        };
        if (review.items.some((item) => !item.current)) {
          verdict(409);
          return error(res, 409, 'review items are stale; prepare a fresh review');
        }
        if (payload?.materialSha256 !== review.materialSha256) {
          verdict(409);
          return error(res, 409);
        }
        if (review.materialSha256 !== currentMatSha(review.primaryJobId)) {
          verdict(409);
          return error(res, 409);
        }
        review.approvedAt = time;
        review.approvedSha256 = review.materialSha256;
        verdict(200);
        return sendJson(res, 200, wireReview(review));
      }
      const sendMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/send$/);
      if (sendMatch?.[1] !== undefined && req.method === 'POST') {
        const review = state.reviews.get(decodeURIComponent(sendMatch[1]));
        if (review === undefined) return error(res, 404);
        if (review.approvedAt === undefined || review.approvedSha256 !== currentMatSha(review.primaryJobId)) {
          state.sendResponses.push({ reviewId: review.id, status: 409 });
          return error(res, 409);
        }
        if (state.attempts.has(review.id)) {
          state.sendResponses.push({ reviewId: review.id, status: 409 });
          return error(res, 409, 'send already recorded for this review');
        }
        const jobId = review.primaryJobId;
        if (jobId === 'job-fail') {
          state.sendResponses.push({ reviewId: review.id, status: 503 });
          return error(res, 503, 'test mail adapter unavailable');
        }
        const attemptId = `attempt-${review.id}-1`;
        const roundId = `round-${review.id}`;
        const applyOutcome = () => {
          for (const item of review.items) {
            item.attemptId = attemptId;
            item.roundId = roundId;
            if (jobId === 'job-partial' && item.opportunityId === 'job-aux') {
              item.state = 'failed';
              item.smtpStage = 'connection-refused';
              item.outcomeDetail = 'test adapter refused the peer item';
            } else if (jobId === 'job-unc') {
              item.state = 'uncertain';
              item.smtpStage = 'data';
              item.outcomeDetail = 'link dropped before the test adapter verdict';
            } else {
              item.state = 'accepted_by_smtp';
              item.smtpStage = '250-queued';
              item.smtpCode = 250;
              item.outcomeDetail = 'queued by the test mail double';
            }
          }
          state.attempts.set(review.id, { attemptId, roundId, at: time });
        };
        if (jobId === 'job-lost') {
          // The send is recorded server-side but the response is cut off
          // mid-body: the UI sees a network failure (no transparent retry is
          // possible once response bytes arrived) and must recover through a
          // status read, never a duplicate send.
          applyOutcome();
          res.writeHead(200, { 'Content-Type': 'application/json', 'Content-Length': 4096 });
          res.flushHeaders();
          await new Promise((done) => res.write('{"review":', done));
          await new Promise((done) => setTimeout(done, 150));
          res.destroy();
          return;
        }
        if (jobId === 'job-bind') {
          // Hold the response briefly so the repeated-click probe lands while
          // the first send is still in flight.
          await new Promise((done) => setTimeout(done, 350));
        }
        applyOutcome();
        state.sendResponses.push({ reviewId: review.id, status: 200 });
        return sendJson(res, 200, {
          review: wireReview(review),
          round: { id: roundId, requestKey: `delivery:${review.id}`, state: 'completed' },
        });
      }
      const reconcileMatch = path.match(/^\/api\/v1\/delivery\/reviews\/([^/]+)\/reconcile$/);
      if (reconcileMatch?.[1] !== undefined && req.method === 'POST') {
        const review = state.reviews.get(decodeURIComponent(reconcileMatch[1]));
        if (review === undefined) return error(res, 404);
        // Read-only: report what the test adapter can verify, change nothing.
        state.reconciles.push({ reviewId: review.id });
        return sendJson(res, 200, {
          supported: false,
          reason: 'test adapter cannot verify external receipt',
        });
      }
      const oppLeaf = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/(decision|workflow|routes|application-packs)$/);
      if (oppLeaf?.[1] !== undefined && oppLeaf[2] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(oppLeaf[1]);
        const leaf = oppLeaf[2];
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        if (leaf === 'decision') return error(res, 404);
        if (leaf === 'workflow') {
          const saved = state.workflows.get(id);
          return saved === undefined ? error(res, 404) : sendJson(res, 200, saved);
        }
        if (leaf === 'routes') return sendJson(res, 200, { items: [routeFixture(id)] });
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
        const id = decodeURIComponent(valuesCurrent[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, state.answerValues.get(id) ?? { checkId: `check-${id}`, questionSetSha256: `set-${id}`, values: [] });
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
          focus: id === 'job-ground' ? GROUND_FOCUS : `Application focus for ${titles.get(id) ?? id}.`,
          cover: id === 'job-ground' ? [GROUND_COVER] : [`Cover line for ${titles.get(id) ?? id}.`],
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
        const firstQuestion = state.checks.get(id)?.check?.questions[0]?.text
          ?? state.answerValues.get(id)?.values.find((value) => value.state === 'answered')?.text
          ?? 'Saved question';
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
      sendJson(res, 500, { error: { message: `RW-E4 fixture error: ${cause.message}` } });
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

function escapeRegExp(text) {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function fetchJson(page, url, method, body) {
  return page.evaluate(
    async ({ url: target, method: verb, body: data }) => {
      const res = await fetch(target, {
        method: verb,
        headers: { 'Content-Type': 'application/json' },
        body: data === undefined ? undefined : JSON.stringify(data),
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

async function expectNoDeliveredLanguage(page, context) {
  const bodyText = await page.evaluate(() => document.body.textContent ?? '');
  const match = bodyText.match(/delivered|received/i);
  assert.equal(
    match,
    null,
    `${context}: UI must never claim delivered/received, saw: ${(match?.[0] ?? '').slice(0, 80)}`,
  );
}

async function expectAttemptCard(page, { packVersion, packSha, recipient, outcome, attemptId }) {
  const card = page.getByRole('article', { name: `Pack v${packVersion} attempt` });
  await card.getByText(outcome).waitFor();
  await card.getByText(packSha).waitFor();
  await card.getByText(recipient).waitFor();
  await card.getByText(new RegExp(escapeRegExp(attemptId))).waitFor();
  await card.getByText('Exact attempted message body').waitFor();
}

// Provenance + readiness must survive every version change: same check pins,
// revisions, source SHAs and Ready/Held badge on each new version, plus the
// matching review-link state.
async function expectVersionPanel(page, { jobId, version, originLabel, ready, checkId, setSha, packId }) {
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
    assert.equal(await link.getAttribute('href'), `#/applications/${jobId}/review`);
  } else {
    assert.equal(await page.getByRole('link', { name: /Continue to review v/ }).count(), 0);
  }
}

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startRwDeliveryFixture();
    fixture.state.expireSession = true;
    browser = await launchBrowser();
    const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const page = await context.newPage();
    page.setDefaultTimeout(20_000);
    const pageErrors = [];
    page.on('pageerror', (cause) => pageErrors.push(cause.message));
    const externalRequests = [];
    page.on('request', (req) => {
      if (!req.url().startsWith(fixture.url)) externalRequests.push(req.url());
    });
    const sendPosts = (reviewId) =>
      fixture.state.requests.filter(
        (item) => item.method === 'POST' && item.path === `/api/v1/delivery/reviews/${reviewId}/send`,
      );
    const reviewPreparesFor = (packId) =>
      fixture.state.requests.filter(
        (item) =>
          item.method === 'POST' &&
          item.path === '/api/v1/delivery/reviews' &&
          Array.isArray(item.payload?.packIds) &&
          item.payload.packIds[0] === packId,
      );

    await page.goto(fixture.url, { waitUntil: 'networkidle' });
    await page.getByLabel('Password').waitFor();
    await page.getByLabel('Password').fill('wrong');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('alert').waitFor();
    fixture.state.expireSession = false;
    await page.getByLabel('Password').fill('synthetic-owner-password');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('navigation', { name: 'Primary' }).waitFor();

    async function gotoPrepare(jobId) {
      await page.goto(`${fixture.url}#/jobs/${jobId}/prepare`, { waitUntil: 'networkidle' });
      await page.getByRole('heading', { name: 'Prepare applications' }).waitFor();
    }

    async function gotoReview(jobId) {
      // Reload for a fresh mount: the authorization panel keeps per-mount
      // review state, so each role starts from an untouched review page.
      await page.goto(`${fixture.url}#/applications/${jobId}/review`, { waitUntil: 'networkidle' });
      await page.reload({ waitUntil: 'networkidle' });
      await page.getByRole('heading', { name: 'Review application' }).waitFor();
    }

    async function readReviewId() {
      const label = page.getByText(/^Review review-/);
      await label.waitFor();
      const reviewId = ((await label.textContent()) ?? '').trim().replace(/^Review\s+/, '');
      assert.match(reviewId, /^review-[a-z0-9-]+$/);
      return reviewId;
    }

    async function prepareAndApprove(jobId, version) {
      await gotoReview(jobId);
      await page
        .getByText(`Authorization binds material v${version} and its pack. A newer version needs a fresh review.`)
        .waitFor();
      await page.getByRole('button', { name: 'Prepare review' }).click();
      const reviewId = await readReviewId();
      await page.getByText('not approved').waitFor();
      await page.getByRole('button', { name: 'Approve this review' }).click();
      await page.getByText('Review approved').waitFor();
      return reviewId;
    }

    // Grounded prepare: v1 from the completed check, optional blank left
    // blank, provenance/readiness on display, mount prepares nothing.
    await gotoPrepare('job-ground');
    await page.getByText('No prepared application yet').waitFor();
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path.endsWith('/materials/prepare')).length,
      0,
      'prepare mount must not prepare',
    );
    await page.getByRole('button', { name: 'Prepare application' }).click();
    await page.getByRole('heading', { name: 'Version 1' }).waitFor();
    await expectVersionPanel(page, {
      jobId: 'job-ground',
      version: 1,
      originLabel: 'Prepared from verified facts',
      ready: true,
      checkId: 'check-job-ground',
      setSha: 'set-ground',
      packId: 'pack-job-ground-1',
    });
    const groundPrepares = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === '/api/v1/opportunities/job-ground/materials/prepare',
    );
    assert.equal(groundPrepares.length, 1);
    assert.deepEqual(Object.keys(groundPrepares[0].payload).sort(), [
      'expectedCheckId',
      'expectedQuestionSetSha256',
      'expectedWorkflowRevision',
      'requestKey',
    ]);
    assert.equal(groundPrepares[0].payload.expectedCheckId, 'check-job-ground');
    assert.equal(groundPrepares[0].payload.expectedQuestionSetSha256, 'set-ground');
    assert.equal(groundPrepares[0].payload.expectedWorkflowRevision, 1);
    await page.getByText(`1. ${GROUND_REQ_TEXT}`).waitFor();
    await page.getByText(`2. ${GROUND_OPT_TEXT}`).waitFor();
    await page.getByText(/Saved answer v1/).waitFor();
    await page.getByText('Drafted into this version (no saved answer row)').waitFor();
    const groundPack = page.getByRole('region', { name: 'Prepared materials' });
    await groundPack.getByText(GROUND_FOCUS).waitFor();
    await groundPack.getByText(GROUND_COVER).waitFor();
    await groundPack.getByText(GROUND_REQ_ANSWER).waitFor();
    assert.equal(await groundPack.getByText(GROUND_OPT_TEXT, { exact: false }).count(), 0);
    assert.equal(await groundPack.getByText(/^Unknown:/).count(), 0);

    // Exact edit: empty box, deterministic seed, byte-exact save into v2.
    const editBoxV1 = page.getByLabel('Material text (replaces v1 byte-exact)');
    await editBoxV1.waitFor();
    assert.equal(await editBoxV1.inputValue(), '');
    const seedV1 = [GROUND_FOCUS, GROUND_COVER, GROUND_REQ_TEXT, GROUND_REQ_ANSWER].join('\n\n');
    await page.getByRole('button', { name: 'Start from current rendering' }).click();
    assert.equal(await editBoxV1.inputValue(), seedV1);
    const exactEdit = `${seedV1}\n\nOwner note: available 2–3 days ✅ "exact bytes"  `;
    await editBoxV1.fill(exactEdit);
    await page.getByRole('button', { name: 'Save exact edit' }).click();
    await page.getByRole('heading', { name: 'Version 2' }).waitFor();
    await expectVersionPanel(page, {
      jobId: 'job-ground',
      version: 2,
      originLabel: 'Direct owner edit (no model)',
      ready: true,
      checkId: 'check-job-ground',
      setSha: 'set-ground',
      packId: 'pack-job-ground-2',
    });
    const groundPuts = fixture.state.requests.filter(
      (item) => item.method === 'PUT' && item.path === '/api/v1/opportunities/job-ground/materials/current',
    );
    assert.equal(groundPuts.length, 1);
    assert.deepEqual(groundPuts[0].payload, {
      requestKey: groundPuts[0].payload.requestKey,
      expectedVersion: 1,
      text: exactEdit,
    });
    assert.deepEqual(Object.keys(groundPuts[0].payload).sort(), ['expectedVersion', 'requestKey', 'text']);

    // Explicit rewrite only: zero rewrite POSTs until the explicit click,
    // then one new reviewable version carrying the verbatim instruction.
    const rewritePath = '/api/v1/opportunities/job-ground/materials/rewrite';
    assert.equal(
      fixture.state.requests.filter((item) => item.method === 'POST' && item.path === rewritePath).length,
      0,
      'reads, prepare and edits must never rewrite implicitly',
    );
    const instruction = 'Emphasize async-first collaboration.';
    await page.getByLabel(/Instruction \(optional/).fill(instruction);
    await page.getByRole('button', { name: 'Request rewrite' }).click();
    await page.getByRole('heading', { name: 'Version 3' }).waitFor();
    await expectVersionPanel(page, {
      jobId: 'job-ground',
      version: 3,
      originLabel: 'Explicit rewrite',
      ready: true,
      checkId: 'check-job-ground',
      setSha: 'set-ground',
      packId: 'pack-job-ground-3',
    });
    await page.getByRole('region', { name: 'Material version 3' }).getByText('Explicit rewrite of v2').waitFor();
    await page
      .getByRole('region', { name: 'Prepared materials' })
      .getByText(new RegExp(escapeRegExp(instruction)))
      .waitFor();
    const rewritePosts = fixture.state.requests.filter((item) => item.method === 'POST' && item.path === rewritePath);
    assert.equal(rewritePosts.length, 1);
    assert.deepEqual(rewritePosts[0].payload, {
      requestKey: rewritePosts[0].payload.requestKey,
      expectedVersion: 2,
      instruction,
    });
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Version 3' }).waitFor();
    await expectVersionPanel(page, {
      jobId: 'job-ground',
      version: 3,
      originLabel: 'Explicit rewrite',
      ready: true,
      checkId: 'check-job-ground',
      setSha: 'set-ground',
      packId: 'pack-job-ground-3',
    });

    // Review page provenance/readiness display, destination, exact answers
    // (including the optional blank) and journey links for the rewritten v3.
    await gotoReview('job-ground');
    const destSection = page.getByRole('region', { name: 'Destination' });
    await destSection.getByText('direct').waitFor();
    await destSection.getByText('Apply through the portal.').waitFor();
    const exactSection = page.getByRole('region', { name: 'Exact answers' });
    await exactSection.getByText(GROUND_REQ_ANSWER).waitFor();
    await exactSection.getByText('left blank').waitFor();
    const matSection = page.getByRole('region', { name: 'Material version' });
    await matSection.getByText('v3').first().waitFor();
    await matSection.getByText('Explicit rewrite of v2').waitFor();
    await matSection.getByText('check-job-ground').waitFor();
    await matSection.getByText('pack-job-ground-3').waitFor();
    await matSection.getByText(/2 source SHAs/).waitFor();
    await matSection.getByText('career-sha-1').waitFor();
    const backLink = page.getByRole('link', { name: 'Back to prepare' });
    assert.equal(await backLink.getAttribute('href'), '#/jobs/job-ground/prepare');
    const attemptsLink = page.getByRole('link', { name: 'Application attempts' });
    assert.equal(await attemptsLink.getAttribute('href'), '#/applications/job-ground');
    await page
      .getByText('Authorization binds material v3 and its pack. A newer version needs a fresh review.')
      .waitFor();
    await page.getByText(/Approve the review above to enable the explicit send step/).waitFor();

    // Held missing-required blocks review: Held badge, missing item listed,
    // no Continue link on prepare; Materials-not-ready block with the held
    // next action and an Open-Prepare link on review, no Prepare button.
    await gotoPrepare('job-held');
    const heldPanel = page.getByRole('region', { name: 'Material version 1' });
    await heldPanel.getByRole('heading', { name: 'Version 1' }).waitFor();
    await heldPanel.getByText('Held', { exact: true }).first().waitFor();
    await page.getByText(/Held: 1 required item still needs an owner-known fact\./).waitFor();
    await page.getByRole('heading', { name: 'Unanswered required items' }).waitFor();
    await page.getByText(`Missing required: ${HELD_REQ_TEXT}`).waitFor();
    await page.getByText('This version is held: every required item needs an answer before review.').waitFor();
    assert.equal(await page.getByRole('link', { name: /Continue to review v/ }).count(), 0);
    await gotoReview('job-held');
    await page.getByText('Materials not ready').waitFor();
    await page.getByText(/Resolve the held or missing required items above/).waitFor();
    const heldOpenPrepare = page.getByRole('link', { name: 'Open Prepare applications' });
    await heldOpenPrepare.waitFor();
    assert.equal(await heldOpenPrepare.getAttribute('href'), '#/jobs/job-held/prepare');
    assert.equal(await page.getByRole('button', { name: 'Prepare review' }).count(), 0);
    assert.equal(reviewPreparesFor('pack-job-held-1').length, 0, 'held material must never reach review prepare');

    // Status-specific not-ready messaging: outdated names re-prepare,
    // not_prepared names preparing first.
    await gotoReview('job-out');
    await page.getByText('Materials not ready').waitFor();
    await page.getByText(/This version is outdated\. Re-prepare on the Prepare page/).waitFor();
    const outOpenPrepare = page.getByRole('link', { name: 'Open Prepare applications' });
    await outOpenPrepare.waitFor();
    assert.equal(await outOpenPrepare.getAttribute('href'), '#/jobs/job-out/prepare');
    await gotoReview('job-noprep');
    await page.getByText('Materials not ready').waitFor();
    await page.getByText(/No material version is prepared yet\. Prepare on the Prepare page first/).waitFor();
    await page.getByText('No material version', { exact: true }).waitFor();
    const noprepOpenPrepare = page.getByRole('link', { name: 'Open Prepare applications' });
    await noprepOpenPrepare.waitFor();
    assert.equal(await noprepOpenPrepare.getAttribute('href'), '#/jobs/job-noprep/prepare');

    // Stale-approve disabled: the prepared review arrives stale, approval is
    // disabled with the block explained, and a fresh review (new request
    // key) recovers to an approved review without a reload.
    const armed = await fetchJson(page, '/__rw4/stale-next', 'POST', {});
    assert.equal(armed.status, 200);
    await gotoReview('job-stale');
    await page
      .getByText('Authorization binds material v2 and its pack. A newer version needs a fresh review.')
      .waitFor();
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const staleReview1 = await readReviewId();
    await page.getByText(/1 of 1 item is stale/).waitFor();
    assert.equal(await page.getByText('stale (superseded)').count(), 2);
    await page.getByText(/Approving is disabled/).waitFor();
    const staleApprove = page.getByRole('button', { name: 'Approve this review' });
    await staleApprove.waitFor();
    assert.equal(await staleApprove.isDisabled(), true);
    assert.equal(
      fixture.state.approveResponses.filter((item) => item.reviewId === staleReview1).length,
      0,
      'disabled approve must POST nothing',
    );
    await page.getByRole('button', { name: 'Prepare fresh review' }).click();
    // The label still shows the old review while the fresh prepare is in
    // flight; wait for the id to change before reading it.
    await page.waitForFunction(
      (oldId) =>
        [...document.querySelectorAll('span')].some(
          (item) => (item.textContent ?? '').startsWith('Review review-') && !(item.textContent ?? '').includes(oldId),
        ),
      staleReview1,
    );
    const staleReview2 = await readReviewId();
    assert.notEqual(staleReview2, staleReview1);
    assert.equal(await page.getByText('stale (superseded)').count(), 0);
    const staleApproveFresh = page.getByRole('button', { name: 'Approve this review' });
    await staleApproveFresh.waitFor();
    assert.equal(await staleApproveFresh.isDisabled(), false);
    await staleApproveFresh.click();
    await page.getByText('Review approved').waitFor();
    const stalePrepares = reviewPreparesFor('pack-job-stale-2');
    assert.equal(stalePrepares.length, 2);
    assert.notEqual(stalePrepares[0].payload.requestKey, stalePrepares[1].payload.requestKey);
    assert.deepEqual(
      fixture.state.approveResponses.filter((item) => item.reviewId === staleReview2).map((item) => item.status),
      [200],
    );

    // Tampered approval refused: approve after the digest is tampered 409s
    // with honest stale-review messaging, and a fresh review recovers.
    await gotoReview('job-tamp');
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const tampReview1 = await readReviewId();
    const tampered = await fetchJson(page, '/__rw4/tamper', 'POST', { reviewId: tampReview1 });
    assert.equal(tampered.status, 200);
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('The material changed after this review was prepared. Prepare a fresh review for the current version.').waitFor();
    await page.getByText('not approved').waitFor();
    assert.deepEqual(
      fixture.state.approveResponses.filter((item) => item.reviewId === tampReview1).map((item) => item.status),
      [409],
    );
    await page.getByRole('button', { name: 'Prepare fresh review' }).click();
    await page.waitForFunction(
      (oldId) =>
        [...document.querySelectorAll('span')].some(
          (item) => (item.textContent ?? '').startsWith('Review review-') && !(item.textContent ?? '').includes(oldId),
        ),
      tampReview1,
    );
    const tampReview2 = await readReviewId();
    assert.notEqual(tampReview2, tampReview1);
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    const tampPrepares = reviewPreparesFor('pack-job-tamp-2');
    assert.equal(tampPrepares.length, 2);
    assert.notEqual(tampPrepares[0].payload.requestKey, tampPrepares[1].payload.requestKey);
    assert.deepEqual(
      fixture.state.approveResponses.filter((item) => item.reviewId === tampReview2).map((item) => item.status),
      [200],
    );
    // A tampered approval digest also blocks the send itself at the HTTP
    // layer: 409, no attempt recorded.
    const tampApproved = await fetchJson(page, '/__rw4/tamper-approved', 'POST', { reviewId: tampReview2 });
    assert.equal(tampApproved.status, 200);
    const tampSend = await fetchJson(page, `/api/v1/delivery/reviews/${tampReview2}/send`, 'POST', {});
    assert.equal(tampSend.status, 409);
    assert.equal(fixture.state.attempts.has(tampReview2), false, 'tampered send must record no attempt');

    // Fail-open capability read: the capability GET fails, so no banner
    // appears and the send stays offered; the send POST itself then refuses
    // honestly with 503 and nothing is sent.
    const capError = await fetchJson(page, '/__rw4/capability', 'POST', { mode: 'error' });
    assert.equal(capError.status, 200);
    const failReview = await prepareAndApprove('job-fail', 2);
    assert.ok(
      fixture.state.requests.some((item) => item.method === 'GET' && item.path === '/api/v1/delivery/capability'),
      'send section must attempt the capability read',
    );
    assert.equal(await page.getByText('Sending unavailable').count(), 0);
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText('The service refused this send (test mail adapter unavailable). Nothing was sent.')
      .waitFor();
    assert.equal(sendPosts(failReview).length, 1);
    assert.deepEqual(
      fixture.state.sendResponses.filter((item) => item.reviewId === failReview).map((item) => item.status),
      [503],
    );
    assert.equal(fixture.state.attempts.has(failReview), false, 'failed send must record no attempt');
    await page.getByText('Prepared; no submission attempted.').waitFor();
    await page.getByRole('button', { name: 'Send approved review' }).waitFor();
    await expectNoDeliveredLanguage(page, 'failed review after refusal');

    // Capability-unavailable send hiding: an explicit
    // submissionAvailable:false hides the send action with the server reason
    // and POSTs nothing; restoring the capability and remounting offers the
    // send again, and the explicit send then succeeds exactly once.
    const capDown = await fetchJson(page, '/__rw4/capability', 'POST', { mode: 'unavailable' });
    assert.equal(capDown.status, 200);
    await gotoReview('job-cap');
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const capReview1 = await readReviewId();
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    await page.getByText('Sending unavailable').waitFor();
    await page.getByText(/no sender configured in this test double/).waitFor();
    assert.equal(await page.getByRole('button', { name: 'Send approved review' }).count(), 0);
    assert.equal(sendPosts(capReview1).length, 0, 'hidden send must POST nothing');
    const capUp = await fetchJson(page, '/__rw4/capability', 'POST', { mode: 'ok' });
    assert.equal(capUp.status, 200);
    await gotoReview('job-cap');
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const capReview2 = await readReviewId();
    assert.notEqual(capReview2, capReview1);
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    assert.equal(await page.getByText('Sending unavailable').count(), 0);
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText(
        'Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt.',
      )
      .waitFor();
    assert.equal(sendPosts(capReview2).length, 1);
    assert.equal(sendPosts(capReview1).length, 0);
    await page.getByText('Accepted by the mail server, not confirmed receipt.').waitFor();
    await expectNoDeliveredLanguage(page, 'cap review after send');

    // Exact version/recipient binding + repeated clicks send once.
    const bindReview = await prepareAndApprove('job-bind', 2);
    const bindPrepares = reviewPreparesFor('pack-job-bind-2');
    assert.equal(bindPrepares.length, 1, 'bind prepare must bind exactly pack v2');
    assert.deepEqual(bindPrepares[0].payload.packIds, ['pack-job-bind-2']);
    const bindApproves = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === `/api/v1/delivery/reviews/${bindReview}/approve`,
    );
    assert.equal(bindApproves.length, 1);
    assert.deepEqual(bindApproves[0].payload, { materialSha256: 'matsha-job-bind-v2' });
    await page.getByText('pack-job-bind-2').first().waitFor();
    await page.getByText('hiring-job-bind@example.invalid').first().waitFor();
    const bindRead = await fetchJson(page, `/api/v1/delivery/reviews/${bindReview}`, 'GET');
    assert.equal(bindRead.status, 200);
    assert.equal(bindRead.body.materialSha256, 'matsha-job-bind-v2');
    assert.equal(bindRead.body.items.length, 1);
    assert.equal(bindRead.body.items[0].packId, 'pack-job-bind-2');
    assert.equal(bindRead.body.items[0].packContentSha256, 'pack-sha-pack-job-bind-2');
    assert.equal(bindRead.body.items[0].recipient, 'hiring-job-bind@example.invalid');
    await page.evaluate(() => {
      const btn = [...document.querySelectorAll('button')].find((item) =>
        (item.textContent ?? '').includes('Send approved review'),
      );
      if (btn === undefined) throw new Error('send button missing for the repeated-click probe');
      btn.click();
      btn.click();
      btn.click();
    });
    await page
      .getByText(
        'Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt.',
      )
      .waitFor();
    assert.equal(sendPosts(bindReview).length, 1, 'repeated clicks must POST send once');
    await page.getByText('Accepted by the mail server, not confirmed receipt.').waitFor();
    await page.getByText(/stage 250-queued/).waitFor();
    const bindLink = page.getByRole('link', { name: 'View attempt outcome' });
    assert.equal(await bindLink.getAttribute('href'), `#/applications/job-bind/attempts/${bindReview}`);
    await expectNoDeliveredLanguage(page, 'bind review after send');
    const bindAttemptId = `attempt-${bindReview}-1`;

    // Lost send response: the server recorded the attempt, so recovery is a
    // status read, never a duplicate send.
    const lostReview = await prepareAndApprove('job-lost', 2);
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page.getByText(/The send may have reached the server, so its outcome is uncertain/).waitFor();
    await page.getByText('Status read required').waitFor();
    assert.equal(sendPosts(lostReview).length, 1);
    assert.equal(fixture.state.attempts.has(lostReview), true, 'lost response still records server-side');
    const lostAttemptId = fixture.state.attempts.get(lostReview).attemptId;
    await page.getByRole('button', { name: 'Read current status' }).click();
    await page.getByText('Current status refreshed from the server. This read never sends.').waitFor();
    await page.getByText('Accepted by the mail server, not confirmed receipt.').waitFor();
    assert.equal(sendPosts(lostReview).length, 1, 'recovery read must not duplicate the send');
    assert.equal(fixture.state.attempts.get(lostReview).attemptId, lostAttemptId);
    await page.getByRole('link', { name: 'View attempt outcome' }).click();
    await page.getByRole('heading', { name: 'Submission outcome' }).waitFor();
    await expectAttemptCard(page, {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-lost-2',
      recipient: 'hiring-job-lost@example.invalid',
      outcome: 'Accepted by the outgoing SMTP server. Employer receipt is unverified.',
      attemptId: lostAttemptId,
    });
    assert.equal(sendPosts(lostReview).length, 1);
    await expectNoDeliveredLanguage(page, 'lost review after recovery');

    // Partial per-item outcomes stay visible on the batched review.
    const partialReview = await prepareAndApprove('job-partial', 2);
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText(
        'Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt.',
      )
      .waitFor();
    assert.equal(sendPosts(partialReview).length, 1);
    const outcomes = page.getByRole('list', { name: 'Per-item send outcomes' });
    assert.equal(await outcomes.getByRole('listitem').count(), 2);
    await outcomes.getByText('Accepted by the mail server, not confirmed receipt.').waitFor();
    await outcomes.getByText('Submission failed.').waitFor();
    await outcomes.getByText(/connection-refused/).waitFor();
    await outcomes.getByText('hiring-job-partial@example.invalid').waitFor();
    await outcomes.getByText('hiring-job-aux@example.invalid').waitFor();
    await expectNoDeliveredLanguage(page, 'partial review after send');

    // Uncertain outcome reconciles read-only: one reconcile, one refresh, no
    // resend, and the recorded attempt is unchanged.
    const uncReview = await prepareAndApprove('job-unc', 2);
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText(
        'Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt.',
      )
      .waitFor();
    await page
      .getByText('Submission outcome unknown. It may have been accepted; do not send again.')
      .waitFor();
    await page.getByText('Read-only: reports what the mail adapter can verify without resending.').waitFor();
    await page.getByText(/Receipt lookup is not supported on this server/).waitFor();
    assert.equal(sendPosts(uncReview).length, 1);
    const uncAttemptBefore = { ...fixture.state.attempts.get(uncReview) };
    await page.getByRole('button', { name: 'Reconcile uncertain outcome' }).click();
    await page.getByText('Read-only receipt check unsupported: test adapter cannot verify external receipt').waitFor();
    const uncReconciles = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === `/api/v1/delivery/reviews/${uncReview}/reconcile`,
    );
    assert.equal(uncReconciles.length, 1);
    assert.equal(sendPosts(uncReview).length, 1, 'reconcile must not resend');
    assert.deepEqual(fixture.state.attempts.get(uncReview), uncAttemptBefore, 'reconcile must not rewrite the attempt');
    const requestPaths = fixture.state.requests.map((item) => `${item.method} ${item.path}`);
    const reconcileIndex = requestPaths.lastIndexOf(`POST /api/v1/delivery/reviews/${uncReview}/reconcile`);
    const refreshIndex = requestPaths.lastIndexOf(`GET /api/v1/delivery/reviews/${uncReview}`);
    assert.ok(refreshIndex > reconcileIndex, 'reconcile must refresh the review with a read');
    await expectNoDeliveredLanguage(page, 'uncertain review after reconcile');

    // Attempt snapshots stay immutable under newer drafts: an exact edit plus
    // an explicit rewrite move the materials to v4 while the recorded
    // attempt still shows the attempted v2 snapshot everywhere.
    const postSendEdit = await fetchJson(page, '/api/v1/opportunities/job-bind/materials/current', 'PUT', {
      requestKey: 'rw4-post-send-edit',
      expectedVersion: 2,
      text: 'Post-send owner tweak for the immutability probe.',
    });
    assert.equal(postSendEdit.status, 200);
    assert.equal(postSendEdit.body.version, 3);
    assert.equal(postSendEdit.body.provenance.origin, 'direct_edit');
    const postSendRewrite = await fetchJson(page, '/api/v1/opportunities/job-bind/materials/rewrite', 'POST', {
      requestKey: 'rw4-post-send-rewrite',
      expectedVersion: 3,
      instruction: 'Post-send rewrite probe.',
    });
    assert.equal(postSendRewrite.status, 200);
    assert.equal(postSendRewrite.body.version, 4);
    assert.equal(postSendRewrite.body.provenance.origin, 'rewrite');
    assert.equal(postSendRewrite.body.provenance.rewriteOf, 3);
    const bindAttemptCard = {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-bind-2',
      recipient: 'hiring-job-bind@example.invalid',
      outcome: 'Accepted by the outgoing SMTP server. Employer receipt is unverified.',
      attemptId: bindAttemptId,
    };
    await page.goto(`${fixture.url}#/applications/job-bind/attempts/${bindReview}`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Submission outcome' }).waitFor();
    await expectAttemptCard(page, bindAttemptCard);
    await expectNoDeliveredLanguage(page, 'bind attempt outcome');
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Submission outcome' }).waitFor();
    await expectAttemptCard(page, bindAttemptCard);
    await page.goto(`${fixture.url}#/applications/job-bind`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Attempted submissions' }).waitFor();
    await expectAttemptCard(page, bindAttemptCard);
    await expectNoDeliveredLanguage(page, 'bind attempt history');
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Attempted submissions' }).waitFor();
    await expectAttemptCard(page, bindAttemptCard);
    const attemptIndex = JSON.parse(
      (await page.evaluate(() => window.localStorage.getItem('jobseek.attempt-reviews.v1'))) ?? '{}',
    );
    assert.deepEqual(attemptIndex['job-bind']?.[0], bindReview);
    const bindReviewAfter = await fetchJson(page, `/api/v1/delivery/reviews/${bindReview}`, 'GET');
    assert.equal(bindReviewAfter.status, 200);
    assert.equal(bindReviewAfter.body.items[0].packId, 'pack-job-bind-2');
    assert.equal(bindReviewAfter.body.items[0].state, 'accepted_by_smtp');
    await gotoReview('job-bind');
    await page
      .getByText('Authorization binds material v4 and its pack. A newer version needs a fresh review.')
      .waitFor();
    await page.getByRole('button', { name: 'Prepare review' }).waitFor();

    // Final tallies: exactly one send POST per sent review, none for stale,
    // tampered, unsent or blocked reviews, one reconcile total, attempts
    // only where the double recorded them.
    assert.equal(sendPosts(staleReview1).length, 0);
    assert.equal(sendPosts(staleReview2).length, 0);
    assert.equal(sendPosts(tampReview1).length, 0);
    assert.equal(sendPosts(tampReview2).length, 1, 'tampered-approved send probe must POST once and 409');
    assert.deepEqual(
      fixture.state.sendResponses.filter((item) => item.reviewId === tampReview2).map((item) => item.status),
      [409],
    );
    const allSendPosts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path.endsWith('/send'),
    );
    assert.equal(allSendPosts.length, 7, `one send per sent review plus the tamper probe expected: ${JSON.stringify(allSendPosts.map((item) => item.path))}`);
    assert.equal(fixture.state.reconciles.length, 1);
    assert.deepEqual(
      [...fixture.state.attempts.keys()].sort(),
      [bindReview, capReview2, lostReview, partialReview, uncReview].sort(),
    );
    const blockedPrepares = fixture.state.requests.filter(
      (item) =>
        item.method === 'POST' &&
        item.path === '/api/v1/delivery/reviews' &&
        ['pack-job-held-1', 'pack-job-out-1', 'pack-job-noprep-1'].includes(item.payload?.packIds?.[0]),
    );
    assert.equal(blockedPrepares.length, 0, 'held/outdated/unprepared material must never reach review prepare');

    // No request may target a real employer: same-origin doubles only, and
    // every recorded destination is a .invalid test domain.
    assert.equal(externalRequests.length, 0, `external requests forbidden: ${externalRequests.join(', ')}`);
    for (const review of fixture.state.reviews.values()) {
      for (const item of review.items) {
        assert.ok(
          item.recipient.endsWith('.invalid'),
          `recipient must be a test domain: ${item.recipient}`,
        );
        assert.ok(item.sender.endsWith('.invalid'), `sender must be a test domain: ${item.sender}`);
        assert.ok(!/https?:\/\//.test(item.body), `attempt body must not link out: ${item.body}`);
      }
    }
    assert.equal(pageErrors.length, 0, `page errors: ${pageErrors.join('; ')}`);

    console.log(
      'rw-delivery smoke: grounded prepare + provenance, held/outdated/not-prepared review blocks, exact edit + explicit rewrite versioning, stale-approve disabled + fresh-review recovery, tampered 409 honesty, fail-open capability + unavailable send hiding, repeat clicks single send, lost-response recovery, partial/failure/uncertain honesty, read-only reconcile, immutable attempt snapshot across reload, zero external requests OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('rw-delivery smoke cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('rw-delivery smoke failed:', cause);
  process.exitCode = 1;
});

