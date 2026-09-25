import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { launchSilentBrowser as launchBrowser } from './browser.mjs';

// Slice 5b gate (G5 connected journey): the slice5 smoke proves each
// delivery behavior as an isolated per-role probe (binding, revoked/stale
// refusal, single-POST clicks, lost-response recovery, partial outcomes,
// honest failure, read-only reconcile, snapshot agreement). It stops the
// uncertain probe at reconcile and the failed probe at the review page, so
// neither uncertain nor failed outcomes are carried through to
// attempted-material return as one journey. This file closes exactly that
// gap: one uncertain journey (review every item -> explicit send ->
// uncertain outcome -> read-only reconcile -> attempt outcome -> history
// return, across reloads) and one failed journey (review -> explicit send
// -> honest failure -> empty history -> return to the still-prepared
// review), each asserting a single send POST end to end with no accidental
// repeat contact. Test double only, .invalid destinations only; the run
// fails if any request leaves the fixture origin. Nothing here contacts a
// real employer. Employer contact still requires the owner's explicit
// reviewed send action outside this test.
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
  ['job-junc', 'Network Engineer'],
  ['job-jaux', 'Docs Writer'],
  ['job-jfail', 'DevOps Engineer'],
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

function answersFixture(jobId) {
  const title = titles.get(jobId) ?? jobId;
  return {
    checkId: `check-${jobId}`,
    questionSetSha256: `set-${jobId}`,
    values: [
      {
        questionId: `q-${jobId}-req`,
        questionTextSha256: `sha-${jobId}-req`,
        required: 'required',
        version: 1,
        state: 'answered',
        text: `Owner answer for ${title}: available promptly.`,
        textSha256: `saved-sha-${jobId}-req`,
        provenance: {
          origin: 'owner_written',
          editedAt: time,
          editedBy: { actorKind: 'administrator', actorId: 'synthetic-owner' },
        },
        updatedAt: time,
      },
      {
        questionId: `q-${jobId}-opt`,
        questionTextSha256: `sha-${jobId}-opt`,
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
  };
}

function materialVersion({ jobId, version }) {
  return {
    packId: `pack-${jobId}-${version}`,
    version,
    opportunityId: jobId,
    opportunityRevision: 2,
    profileRevision: 3,
    checkId: `check-${jobId}`,
    questionSetSha256: `set-${jobId}`,
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
    provenance: { origin: 'prepared', sourceShas: ['career-sha-1', 'role-sha-2'] },
    createdAt: time,
    createdBy: { actorKind: 'codex', actorId: 'codex' },
  };
}

function packDetail({ id, jobId, version, focus }) {
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
        cover: [{ text: `Cover line for ${title} v${version}.`, citations: [] }],
        answers: [
          {
            question: `Owner question for ${title}?`,
            lines: [{ text: `Owner answer for ${title}: available promptly.`, citations: [] }],
          },
        ],
        materialUnknowns: [],
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
  sendJson(res, status, { error: { message: message ?? `Slice5b fixture HTTP ${status}` } });
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

async function startSlice5bFixture() {
  const state = {
    expireSession: true,
    materials: new Map(),
    packs: new Map(),
    reviews: new Map(),
    reviewSeq: 0,
    attempts: new Map(),
    sendResponses: [],
    reconciles: [],
    requests: [],
  };
  for (const [jobId, title] of ROLE_DEFS) {
    const v1 = packDetail({ id: `pack-${jobId}-1`, jobId, version: 1, focus: `Initial focus for ${title}.` });
    const v2 = packDetail({ id: `pack-${jobId}-2`, jobId, version: 2, focus: `Refined focus for ${title}.` });
    state.packs.set(v1.id, v1);
    state.packs.set(v2.id, v2);
    state.materials.set(jobId, { status: 'prepared', current: materialVersion({ jobId, version: 2 }) });
  }

  function currentMatSha(jobId) {
    const entry = state.materials.get(jobId);
    if (entry?.current === undefined) return null;
    return matSha(jobId, entry.current.version);
  }

  function wireReview(review) {
    const { primaryJobId: _p, ...wire } = review;
    return wire;
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
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'slice5b-fixture' });
      if (path === '/api/v1/preferences') return sendJson(res, 200, profile);
      if (path === '/api/v1/runtime-status')
        return sendJson(res, 200, { ingestionAvailable: false, organisationAvailable: false });
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
        state.reviewSeq += 1;
        const id = `review-${pack.opportunityId}-${state.reviewSeq}`;
        const items = [itemFixture({ reviewId: id, jobId: pack.opportunityId, pack, state: 'prepared' })];
        if (pack.id === 'pack-job-junc-2') {
          // The double models server-side batching for this one review: the
          // contract allows multi-item reviews and the journey must review
          // every item before the explicit send.
          const auxPack = state.packs.get('pack-job-jaux-2');
          items.push(itemFixture({ reviewId: id, jobId: 'job-jaux', pack: auxPack, state: 'prepared' }));
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
        if (payload?.materialSha256 !== review.materialSha256) return error(res, 409);
        if (review.materialSha256 !== currentMatSha(review.primaryJobId)) return error(res, 409);
        review.approvedAt = time;
        review.approvedSha256 = review.materialSha256;
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
        if (review.primaryJobId === 'job-jfail') {
          state.sendResponses.push({ reviewId: review.id, status: 503 });
          return error(res, 503, 'test mail adapter unavailable');
        }
        const attemptId = `attempt-${review.id}-1`;
        const roundId = `round-${review.id}`;
        for (const item of review.items) {
          item.attemptId = attemptId;
          item.roundId = roundId;
          if (item.opportunityId === 'job-junc') {
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
        if (leaf === 'workflow') return error(res, 404);
        if (leaf === 'routes') return sendJson(res, 200, { items: [routeFixture(id)] });
        const items = [...state.packs.values()]
          .filter((pack) => pack.opportunityId === id)
          .sort((a, b) => a.version - b.version)
          .map(packSummary);
        return sendJson(res, 200, { items });
      }
      const valuesCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/answers\/current$/);
      if (valuesCurrent?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(valuesCurrent[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, answersFixture(id));
      }
      const materialsCurrent = path.match(/^\/api\/v1\/opportunities\/([^/]+)\/materials\/current$/);
      if (materialsCurrent?.[1] !== undefined && req.method === 'GET') {
        const id = decodeURIComponent(materialsCurrent[1]);
        if (roles.find((item) => item.id === id) === undefined) return error(res, 404);
        return sendJson(res, 200, state.materials.get(id) ?? { status: 'not_prepared' });
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
      sendJson(res, 500, { error: { message: `Slice5b fixture error: ${cause.message}` } });
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

async function run() {
  let fixture;
  let browser;
  try {
    fixture = await startSlice5bFixture();
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

    await page.goto(fixture.url, { waitUntil: 'networkidle' });
    await page.getByLabel('Password').waitFor();
    fixture.state.expireSession = false;
    await page.getByLabel('Password').fill('synthetic-owner-password');
    await page.getByRole('button', { name: 'Sign in', exact: true }).click();
    await page.getByRole('navigation', { name: 'Primary' }).waitFor();

    async function gotoReview(jobId) {
      await page.goto(`${fixture.url}#/applications/${jobId}/review`, { waitUntil: 'networkidle' });
      await page.reload({ waitUntil: 'networkidle' });
      await page.getByRole('heading', { name: 'Review application' }).waitFor();
    }

    async function expectFullMaterial(jobId, title) {
      // The owner reviews the complete material before any send step.
      await page.getByText('Apply through the portal.').first().waitFor();
      await page.getByText(`Owner answer for ${title}: available promptly.`).first().waitFor();
      await page.locator('section[aria-label="Application pack"]').getByText('v2').waitFor();
      await page.locator('section[aria-label="Material version"]').getByText('v2').waitFor();
      await page
        .getByText('Authorization binds material v2 and its pack. A newer version needs a fresh review.')
        .waitFor();
      void jobId;
    }

    // Journey U: batched uncertain review carried through to return.
    await gotoReview('job-junc');
    await expectFullMaterial('job-junc', 'Network Engineer');
    await expectNoDeliveredLanguage(page, 'uncertain journey before prepare');
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const uncLabel = page.getByText(/^Review review-/);
    await uncLabel.waitFor();
    const uncReview = ((await uncLabel.textContent()) ?? '').trim().replace(/^Review\s+/, '');
    assert.match(uncReview, /^review-[a-z0-9-]+$/);
    // Every item is reviewable before approval: both batched items list
    // their exact pack and recipient in the authorization panel.
    const authItems = page.locator('section[aria-label="Review state"] li');
    assert.equal(await authItems.count(), 2, 'batched review must list both items before approval');
    await page.locator('section[aria-label="Review state"]').getByText('pack-job-junc-2').waitFor();
    await page.locator('section[aria-label="Review state"]').getByText('hiring-job-junc@example.invalid').waitFor();
    await page.locator('section[aria-label="Review state"]').getByText('pack-job-jaux-2').waitFor();
    await page.locator('section[aria-label="Review state"]').getByText('hiring-job-jaux@example.invalid').waitFor();
    const uncPrepares = fixture.state.requests.filter(
      (item) =>
        item.method === 'POST' &&
        item.path === '/api/v1/delivery/reviews' &&
        Array.isArray(item.payload?.packIds) &&
        item.payload.packIds[0] === 'pack-job-junc-2',
    );
    assert.equal(uncPrepares.length, 1, 'uncertain prepare must bind exactly pack v2');
    await page.getByText('not approved').waitFor();
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    const uncApproves = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path === `/api/v1/delivery/reviews/${uncReview}/approve`,
    );
    assert.equal(uncApproves.length, 1);
    assert.deepEqual(uncApproves[0].payload, { materialSha256: 'matsha-job-junc-v2' });
    await expectNoDeliveredLanguage(page, 'uncertain journey after approve');

    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText(
        'Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt.',
      )
      .waitFor();
    assert.equal(sendPosts(uncReview).length, 1);
    const uncOutcomes = page.getByRole('list', { name: 'Per-item send outcomes' });
    assert.equal(await uncOutcomes.getByRole('listitem').count(), 2);
    await uncOutcomes.getByText('Submission outcome unknown. It may have been accepted; do not send again.').waitFor();
    await uncOutcomes.getByText('Accepted by the mail server, not confirmed receipt.').waitFor();
    await uncOutcomes.getByText(/stage data/).waitFor();
    const uncLink = page.getByRole('link', { name: 'View attempt outcome' });
    assert.equal(await uncLink.getAttribute('href'), `#/applications/job-junc/attempts/${uncReview}`);
    await expectNoDeliveredLanguage(page, 'uncertain journey after send');
    const uncAttemptId = `attempt-${uncReview}-1`;

    // Reconcile read-only, then carry the uncertain attempt to its outcome
    // page and back to history without another send.
    const uncAttemptBefore = { ...fixture.state.attempts.get(uncReview) };
    await page.getByRole('button', { name: 'Reconcile uncertain outcome' }).click();
    await page.getByText('Read-only receipt check unsupported: test adapter cannot verify external receipt').waitFor();
    assert.equal(sendPosts(uncReview).length, 1, 'reconcile must not resend');
    assert.deepEqual(fixture.state.attempts.get(uncReview), uncAttemptBefore, 'reconcile must not rewrite the attempt');
    await uncLink.click();
    await page.getByRole('heading', { name: 'Submission outcome' }).waitFor();
    await expectAttemptCard(page, {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-junc-2',
      recipient: 'hiring-job-junc@example.invalid',
      outcome: 'Submission outcome unknown. It may have been accepted; do not resend.',
      attemptId: uncAttemptId,
    });
    assert.equal(
      await page.getByRole('article').count(),
      1,
      'role-scoped outcome shows only this role item',
    );
    assert.equal(sendPosts(uncReview).length, 1);
    await expectNoDeliveredLanguage(page, 'uncertain outcome page');
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Submission outcome' }).waitFor();
    await expectAttemptCard(page, {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-junc-2',
      recipient: 'hiring-job-junc@example.invalid',
      outcome: 'Submission outcome unknown. It may have been accepted; do not resend.',
      attemptId: uncAttemptId,
    });
    await page.getByRole('link', { name: 'Back to application' }).click();
    await page.getByRole('heading', { name: 'Attempted submissions' }).waitFor();
    await expectAttemptCard(page, {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-junc-2',
      recipient: 'hiring-job-junc@example.invalid',
      outcome: 'Submission outcome unknown. It may have been accepted; do not resend.',
      attemptId: uncAttemptId,
    });
    assert.equal(sendPosts(uncReview).length, 1);
    await expectNoDeliveredLanguage(page, 'uncertain history return');
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Attempted submissions' }).waitFor();
    await expectAttemptCard(page, {
      packVersion: 2,
      packSha: 'pack-sha-pack-job-junc-2',
      recipient: 'hiring-job-junc@example.invalid',
      outcome: 'Submission outcome unknown. It may have been accepted; do not resend.',
      attemptId: uncAttemptId,
    });
    const attemptIndex = JSON.parse(
      (await page.evaluate(() => window.localStorage.getItem('jobseek.attempt-reviews.v1'))) ?? '{}',
    );
    assert.deepEqual(attemptIndex['job-junc']?.[0], uncReview);

    // Journey F: failed send returns honestly with no attempt and no
    // accidental repeat contact.
    await gotoReview('job-jfail');
    await expectFullMaterial('job-jfail', 'DevOps Engineer');
    await page.getByRole('button', { name: 'Prepare review' }).click();
    const failLabel = page.getByText(/^Review review-/);
    await failLabel.waitFor();
    const failReview = ((await failLabel.textContent()) ?? '').trim().replace(/^Review\s+/, '');
    assert.match(failReview, /^review-[a-z0-9-]+$/);
    assert.equal(
      await page.locator('section[aria-label="Review state"] li').count(),
      1,
      'failed journey reviews its single item before approval',
    );
    await page.getByRole('button', { name: 'Approve this review' }).click();
    await page.getByText('Review approved').waitFor();
    await page.getByRole('button', { name: 'Send approved review' }).click();
    await page
      .getByText('The service refused this send (test mail adapter unavailable). Nothing was sent.')
      .waitFor();
    assert.equal(sendPosts(failReview).length, 1);
    assert.equal(fixture.state.attempts.has(failReview), false, 'failed send must record no attempt');
    assert.equal(
      await page.getByRole('link', { name: 'View attempt outcome' }).count(),
      0,
      'failed send offers no attempt outcome link',
    );
    await page.getByText('Prepared; no submission attempted.').waitFor();
    await page.getByRole('button', { name: 'Send approved review' }).waitFor();
    await expectNoDeliveredLanguage(page, 'failed journey after refusal');

    await page.goto(`${fixture.url}#/applications/job-jfail`, { waitUntil: 'networkidle' });
    await page.getByRole('heading', { name: 'Attempted submissions' }).waitFor();
    await page.getByText('No submission attempts recorded for this role').waitFor();
    assert.equal(sendPosts(failReview).length, 1, 'history visit must not resend');
    await page.reload({ waitUntil: 'networkidle' });
    await page.getByText('No submission attempts recorded for this role').waitFor();
    const attemptIndexAfterFail = JSON.parse(
      (await page.evaluate(() => window.localStorage.getItem('jobseek.attempt-reviews.v1'))) ?? '{}',
    );
    assert.equal(attemptIndexAfterFail['job-jfail'], undefined, 'failed send stores no attempt identity');
    // Return to the review: still prepared, still an explicit decision, no
    // automatic contact after the failure.
    await gotoReview('job-jfail');
    await page.getByRole('button', { name: 'Prepare review' }).waitFor();
    assert.equal(sendPosts(failReview).length, 1, 'review return must not resend');
    await expectNoDeliveredLanguage(page, 'failed journey after return');

    // Final tallies: exactly one send POST per journey review, one
    // reconcile total, one recorded attempt (the uncertain one).
    const allSendPosts = fixture.state.requests.filter(
      (item) => item.method === 'POST' && item.path.endsWith('/send'),
    );
    assert.equal(allSendPosts.length, 2, `one send per journey expected: ${JSON.stringify(allSendPosts.map((item) => item.path))}`);
    assert.equal(fixture.state.reconciles.length, 1);
    assert.deepEqual([...fixture.state.attempts.keys()], [uncReview]);

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
      'slice5b journey: uncertain review->send->reconcile->outcome->history and failed send->empty history->prepared return, single send each, zero external requests OK',
    );
  } finally {
    const cleanup = await Promise.allSettled([browser?.close(), fixture?.close()]);
    for (const item of cleanup) {
      if (item.status === 'rejected') {
        console.error('slice5b journey cleanup failed:', item.reason);
        process.exitCode = 1;
      }
    }
  }
}

run().catch((cause) => {
  console.error('slice5b journey failed:', cause);
  process.exitCode = 1;
});

