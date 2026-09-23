import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { dirname, extname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const webDist = resolve(dirname(fileURLToPath(import.meta.url)), '../../apps/web/dist');
const time = '2026-09-23T12:00:00Z';
const allowance = { requests: 7, items: 1, tools: 3, turns: 1 };
const opportunity = {
  id: 'synthetic-role-1',
  companyId: 'synthetic-company-1',
  title: 'Synthetic Research Role',
  kind: 'employment',
  sourceUrl: 'https://example.invalid/jobs/research',
  originalText: 'Synthetic vacancy: research role, remote; pay and hours unconfirmed.',
  notes: '',
  stage: 'new',
  workPattern: 'remote',
  locationText: 'Example City',
  postedOn: '',
  deadlineOn: '',
  revision: 3,
  createdAt: time,
  updatedAt: time,
  compensation: {},
};
const profile = {
  version: 7,
  preferredLocation: 'Example City',
  allowRemote: true,
  allowHybrid: true,
  targetHours: '32',
  minMonthlyBaseCents: 500000,
  salaryCurrency: 'EUR',
  timezone: 'Europe/Brussels',
  roleCriteria: [],
};
const company = {
  id: 'synthetic-company-1',
  name: 'Example Organisation',
  website: '',
  notes: '',
  revision: 1,
  createdAt: time,
  updatedAt: time,
};
const round = {
  id: 'synthetic-prepare-1',
  intent: 'Prepare a private application pack for the selected sourced opportunity.',
  outcome: 'prepare',
  profileVersion: 7,
  scope: {
    inputRefs: ['profile:current', 'opportunity:synthetic-role-1'],
    resources: ['opportunity:synthetic-role-1'],
    operations: ['application_pack.prepare'],
    delegates: ['codex-runner'],
  },
  state: 'completed',
  revision: 1,
  generation: 1,
  deadline: '2026-09-24T00:00:00Z',
  limits: allowance,
  used: { requests: 0, items: 0, tools: 0, turns: 0 },
  step: 'done',
  cursor: {},
  unresolved: [],
  report: {},
  stopReason: '',
  deliverableStatus: 'saved',
  reconciliationRequired: false,
  createdAt: time,
  updatedAt: time,
};
const pack2 = {
  id: 'synthetic-pack-2',
  opportunityId: opportunity.id,
  opportunityRevision: 3,
  profileRevision: 7,
  version: 2,
  contentSha256: 'b'.repeat(64),
  createdAt: time,
};
const pack1 = {
  id: 'synthetic-pack-1',
  opportunityId: opportunity.id,
  opportunityRevision: 2,
  profileRevision: 6,
  version: 1,
  contentSha256: 'a'.repeat(64),
  createdAt: '2026-09-22T12:00:00Z',
};
const deliveryRoute = {
  id: 'synthetic-mail-route',
  opportunityId: opportunity.id,
  kind: 'direct',
  destinationText: 'jobs@example.invalid',
  sourceExcerpt: 'Send your application to jobs@example.invalid',
  revision: 1,
  createdAt: time,
  updatedAt: time,
};
function deliveryReview(id, packIds) {
  return {
    id,
    materialSha256: '8'.repeat(64),
    items: packIds.map((packId, index) => ({
      id: `${id}-item-${index + 1}`,
      reviewId: id,
      packId,
      opportunityId: opportunity.id,
      opportunityRevision: opportunity.revision,
      sourceSha256: '1'.repeat(64),
      profileRevision: profile.version,
      packContentSha256: '2'.repeat(64),
      routeId: deliveryRoute.id,
      routeRevision: deliveryRoute.revision,
      routeSha256: '3'.repeat(64),
      title: opportunity.title,
      companyName: company.name,
      routeExcerpt: deliveryRoute.sourceExcerpt,
      recipient: deliveryRoute.destinationText,
      sender: 'owner@example.invalid',
      subject: `Application: ${opportunity.title}`,
      body: 'This synthetic research opening interests me.',
      attachmentSha256: '4'.repeat(64),
      mimeSha256: `${index + 5}`.repeat(64),
      messageId: `<synthetic-${index + 1}@example.invalid>`,
      state: 'prepared',
      current: true,
    })),
  };
}
function manifest(pack) {
  return {
    role: {
      opportunityId: opportunity.id,
      opportunityRevision: pack.opportunityRevision,
      profileRevision: pack.profileRevision,
      title: opportunity.title,
      company: company.name,
      sourceUrl: opportunity.sourceUrl,
      description: opportunity.originalText,
      destination: '',
    },
    sources: [
      {
        id: 'synthetic-vacancy',
        name: 'Synthetic vacancy snapshot',
        sha256: 'c'.repeat(64),
        approved: true,
        body: 'Synthetic vacancy: research role, remote.',
      },
      {
        id: 'synthetic-note',
        name: 'Synthetic approved career note',
        sha256: 'd'.repeat(64),
        approved: true,
        body: 'A personal research prototype was built as a learning project.',
      },
    ],
    draft: {
      focus: {
        text: 'Personal research prototype experience.',
        citations: [{ sourceId: 'synthetic-note', excerpt: 'personal research prototype' }],
      },
      cover: [
        {
          text: 'This synthetic research opening interests me.',
          citations: [{ sourceId: 'synthetic-vacancy', excerpt: 'research role' }],
        },
      ],
      answers: [
        {
          question: 'Why this role?',
          lines: [
            {
              text: 'It relates to my learning project.',
              citations: [{ sourceId: 'synthetic-note', excerpt: 'learning project' }],
            },
          ],
        },
      ],
      materialUnknowns: [
        'Actual weekly hours remain unconfirmed.',
        'Application destination is unknown.',
      ],
      relevance: [
        {
          requirement: 'Research work',
          sourceId: 'synthetic-note',
          scope: 'uncertain',
          confidence: 0.5,
          inputSha256: 'e'.repeat(64),
          model: 'synthetic-classifier',
        },
      ],
    },
    templateSha256: 'f'.repeat(64),
    preparationRequestSha256: '1'.repeat(64),
  };
}
function inputChange(operation, entityKind, revisionAfter) {
  return {
    auditId: `synthetic-${operation}`,
    attemptId: 'synthetic-attempt',
    operation,
    entityKind,
    entityId: `synthetic-${entityKind}`,
    revisionAfter,
    occurredAt: time,
  };
}
function sendJson(res, status, value) {
  const body = Buffer.from(JSON.stringify(value));
  res.writeHead(status, { 'Content-Type': 'application/json', 'Content-Length': body.length });
  res.end(body);
}
function error(res, status) {
  sendJson(res, status, { error: { message: `Synthetic fixture HTTP ${status}` } });
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
      {
        '.js': 'text/javascript',
        '.css': 'text/css',
        '.html': 'text/html',
        '.svg': 'image/svg+xml',
      }[extname(file)] || 'application/octet-stream';
    res.writeHead(200, { 'Content-Type': mime, 'Content-Length': bytes.length });
    res.end(bytes);
  } catch {
    error(res, 404);
  }
}

export async function startFixture() {
  const state = {
    decision: null,
    profileVersion: profile.version,
    currentInput: null,
    organisationLimitNext: false,
    round: { ...round },
    previousRounds: new Map(),
    packs: [pack2, pack1],
    details: new Map([
      [pack2.id, { ...pack2, manifest: manifest(pack2) }],
      [pack1.id, { ...pack1, manifest: manifest(pack1) }],
    ]),
    failDetail: false,
    failFirstProcessResponse: false,
    processResponses: new Map(),
    prepareResponses: new Map(),
    discoveryResponses: new Map(),
    failFirstPrepareResponse: false,
    failFirstDiscoveryResponse: false,
    staleNextPrepare: false,
    staleNextDiscovery: false,
    discoveryReady: false,
    latestCompletedDiscover: null,
    recommendationCards: [],
    interviews: new Map(),
    interviewPrepareByKey: new Map(),
    interviewDebriefByKey: new Map(),
    failFirstInterviewPrepareResponse: false,
    failFirstInterviewDebriefResponse: false,
    rejectNextInterviewPrepare: false,
    deferInterviewPrepareResponse: false,
    interviewPrepareResponsePending: false,
    releaseInterviewPrepare: null,
    deferInterviewDebriefResponse: false,
    interviewDebriefResponsePending: false,
    releaseInterviewDebrief: null,
    delivery: {
      reviews: new Map(),
      keys: new Map(),
      failFirstPrepareResponse: false,
      failFirstSendResponse: false,
      dropNextSendBeforeArrival: false,
      deferSendResponse: false,
      sendResponsePending: false,
      releaseSend: null,
      staleNextApproval: false,
      senderAvailable: true,
      routeSupported: true,
      outcome: 'accepted_by_smtp',
      round: null,
    },
    requests: [],
  };
  const server = createServer(async (req, res) => {
    const path = new URL(req.url, 'http://127.0.0.1').pathname;
    try {
      if (!path.startsWith('/api/v1/')) return await serveStatic(res, path);
      const payload = req.method === 'POST' ? await bodyJson(req) : undefined;
      state.requests.push({ method: req.method, path, payload });
      if (path === '/api/v1/auth/session')
        return sendJson(res, 200, {
          actorKind: 'administrator',
          actorId: 'synthetic-owner',
          csrfToken: 'synthetic-csrf',
          expiresAt: '2099-01-01T00:00:00Z',
        });
      if (path === '/api/v1/health')
        return sendJson(res, 200, { status: 'ok', service: 'jobseek-api', version: 'ui-fixture' });
      if (path === '/api/v1/preferences')
        return sendJson(res, 200, { ...profile, version: state.profileVersion });
      if (path === '/api/v1/interviews' && req.method === 'GET')
        return sendJson(res, 200, {
          items: [...state.interviews.values()].map((record) => record.interview).reverse(),
        });
      if (path === '/api/v1/interviews/prepare' && req.method === 'POST') {
        const previous = state.interviewPrepareByKey.get(payload.requestKey);
        if (previous) return sendJson(res, 200, previous);
        if (state.rejectNextInterviewPrepare) {
          state.rejectNextInterviewPrepare = false;
          return error(res, 409);
        }
        if (payload.opportunityId !== opportunity.id || !payload.context?.trim())
          return error(res, 400);
        if (
          ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(state.round.state)
        )
          return error(res, 409);
        const id = `synthetic-interview-${state.interviews.size + 1}`;
        const roundId = `synthetic-interview-round-${state.interviews.size + 1}`;
        const view = {
          id,
          opportunityId: opportunity.id,
          opportunityRevision: opportunity.revision,
          profileVersion: state.profileVersion,
          context: payload.context,
          contextSha256: '6'.repeat(64),
          roundId,
          current: true,
          createdAt: time,
          updatedAt: time,
        };
        state.round = {
          ...round,
          id: roundId,
          outcome: 'interview_prepare',
          intent: 'Prepare one sourced private interview brief',
          state: 'running',
          step: 'preparing',
          deliverableStatus: 'pending',
          scope: {
            inputRefs: [`interview:${id}`],
            resources: [`opportunity:${opportunity.id}`],
            operations: ['interview_brief_save', 'jev_request'],
            delegates: ['codex-runner'],
          },
          report: {},
        };
        state.interviews.set(id, { interview: view, debriefs: [] });
        const response = { interviewId: id, round: state.round };
        state.interviewPrepareByKey.set(payload.requestKey, response);
        if (state.deferInterviewPrepareResponse) {
          state.interviewPrepareResponsePending = true;
          await new Promise((resolve) => {
            state.releaseInterviewPrepare = resolve;
          });
          state.interviewPrepareResponsePending = false;
          state.releaseInterviewPrepare = null;
        }
        if (state.failFirstInterviewPrepareResponse) {
          state.failFirstInterviewPrepareResponse = false;
          return error(res, 503);
        }
        return sendJson(res, 201, response);
      }
      if (path.startsWith('/api/v1/interviews/')) {
        const parts = path.split('/');
        const id = parts[4];
        const record = state.interviews.get(id);
        if (!record) return error(res, 404);
        if (req.method === 'GET' && parts.length === 5) return sendJson(res, 200, record);
        if (req.method === 'POST' && parts[5] === 'debrief') {
          const previous = state.interviewDebriefByKey.get(payload.requestKey);
          if (previous) return sendJson(res, 200, previous);
          if (!record.interview.current || !payload.notes?.trim()) return error(res, 409);
          if (
            ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(
              state.round.state,
            )
          )
            return error(res, 409);
          const debriefId = `synthetic-debrief-${state.interviewDebriefByKey.size + 1}`;
          const roundId = `synthetic-debrief-round-${state.interviewDebriefByKey.size + 1}`;
          record.debriefs.unshift({
            id: debriefId,
            interviewId: id,
            notes: payload.notes,
            roundId,
            createdAt: time,
            updatedAt: time,
          });
          state.round = {
            ...round,
            id: roundId,
            outcome: 'interview_debrief',
            intent: 'Record owner-reported debrief',
            state: 'running',
            step: 'recording',
            deliverableStatus: 'pending',
            scope: {
              inputRefs: [`debrief:${debriefId}`],
              resources: [`interview:${id}`],
              operations: ['interview_debrief_save'],
              delegates: ['codex-runner'],
            },
            report: {},
          };
          const response = { debriefId, interviewId: id, round: state.round };
          state.interviewDebriefByKey.set(payload.requestKey, response);
          if (state.deferInterviewDebriefResponse) {
            state.interviewDebriefResponsePending = true;
            await new Promise((resolve) => {
              state.releaseInterviewDebrief = resolve;
            });
            state.interviewDebriefResponsePending = false;
            state.releaseInterviewDebrief = null;
          }
          if (state.failFirstInterviewDebriefResponse) {
            state.failFirstInterviewDebriefResponse = false;
            return error(res, 503);
          }
          return sendJson(res, 201, response);
        }
      }
      if (path === '/api/v1/delivery/capability')
        return sendJson(res, 200, {
          submissionAvailable: state.delivery.senderAvailable,
          receiptLookup: false,
          reason: 'SMTP submission has no verified employer receipt lookup.',
        });
      if (path === '/api/v1/delivery/reviews' && req.method === 'POST') {
        if (
          !payload.requestKey ||
          !Array.isArray(payload.packIds) ||
          payload.packIds.length < 1 ||
          payload.packIds.length > 3
        )
          return error(res, 400);
        const previous = state.delivery.keys.get(payload.requestKey);
        if (previous) {
          if (previous.packIds.join('|') !== payload.packIds.join('|')) return error(res, 409);
          return sendJson(res, 200, state.delivery.reviews.get(previous.id));
        }
        if (!state.delivery.senderAvailable) return error(res, 503);
        if (!state.delivery.routeSupported) return error(res, 422);
        if (!state.decision || state.decision.decision !== 'selected') return error(res, 409);
        if (payload.packIds.some((id) => !state.packs.some((pack) => pack.id === id)))
          return error(res, 422);
        const id = `synthetic-delivery-${state.delivery.reviews.size + 1}`;
        const review = deliveryReview(id, payload.packIds);
        state.delivery.reviews.set(id, review);
        state.delivery.keys.set(payload.requestKey, { id, packIds: payload.packIds });
        if (state.delivery.failFirstPrepareResponse) {
          state.delivery.failFirstPrepareResponse = false;
          return error(res, 503);
        }
        return sendJson(res, 201, review);
      }
      if (path.startsWith('/api/v1/delivery/reviews/')) {
        const parts = path.split('/');
        const review = state.delivery.reviews.get(parts[5]);
        if (!review) return error(res, 404);
        const action = parts[6];
        if (!action && req.method === 'GET') return sendJson(res, 200, review);
        if (action === 'approve' && req.method === 'POST') {
          if (state.delivery.staleNextApproval) {
            state.delivery.staleNextApproval = false;
            review.items[0].current = false;
            review.items[0].blockingReason = 'route_changed';
            return error(res, 409);
          }
          if (
            payload.materialSha256 !== review.materialSha256 ||
            review.items.some((item) => !item.current)
          )
            return error(res, 409);
          review.approvedSha256 = payload.materialSha256;
          review.approvedAt = time;
          return sendJson(res, 200, review);
        }
        if (action === 'send' && req.method === 'POST') {
          if (state.delivery.dropNextSendBeforeArrival) {
            state.delivery.dropNextSendBeforeArrival = false;
            return error(res, 503);
          }
          if (state.delivery.round?.scope.inputRefs.includes(`delivery_review:${review.id}`))
            return sendJson(res, 200, { review, round: state.delivery.round });
          if (
            review.approvedSha256 !== review.materialSha256 ||
            review.items.some((item) => !item.current || item.state !== 'prepared')
          )
            return error(res, 409);
          if (state.delivery.round) return error(res, 409);
          state.delivery.round = {
            ...round,
            id: `synthetic-delivery-round-${state.delivery.reviews.size}`,
            intent: 'Deliver exact owner-approved applications',
            outcome: 'deliver',
            state:
              state.delivery.outcome === 'sending'
                ? 'running'
                : state.delivery.outcome === 'uncertain'
                  ? 'paused'
                  : 'completed',
            scope: {
              inputRefs: [`delivery_review:${review.id}`],
              resources: review.items.map((item) => `delivery:${item.id}`),
              operations: ['delivery.submit'],
              delegates: [],
            },
            step: state.delivery.outcome,
            deliverableStatus:
              state.delivery.outcome === 'accepted_by_smtp'
                ? 'submission_recorded'
                : 'submission_unverified',
            reconciliationRequired: state.delivery.outcome === 'uncertain',
          };
          for (const [index, item] of review.items.entries()) {
            item.state =
              state.delivery.outcome === 'sending' && index > 0
                ? 'prepared'
                : state.delivery.outcome;
            item.roundId = state.delivery.round.id;
            if (item.state !== 'prepared') {
              item.smtpStage =
                state.delivery.outcome === 'accepted_by_smtp' ? 'data_reply' : 'data';
              item.smtpCode = state.delivery.outcome === 'accepted_by_smtp' ? 250 : undefined;
            }
          }
          if (state.delivery.deferSendResponse) {
            state.delivery.sendResponsePending = true;
            await new Promise((resolve) => {
              state.delivery.releaseSend = resolve;
            });
            state.delivery.sendResponsePending = false;
            state.delivery.releaseSend = null;
          }
          if (state.delivery.failFirstSendResponse) {
            state.delivery.failFirstSendResponse = false;
            return error(res, 503);
          }
          return sendJson(res, 200, { review, round: state.delivery.round });
        }
        if (action === 'reconcile' && req.method === 'POST')
          return sendJson(res, 200, {
            supported: false,
            reason: 'No authenticated read-only receipt query.',
          });
        if (action === 'close' && req.method === 'POST') {
          if (state.delivery.round?.state !== 'paused') return error(res, 409);
          state.delivery.round = {
            ...state.delivery.round,
            state: 'completed',
            deliverableStatus: 'submission_unverified',
          };
          return sendJson(res, 200, state.delivery.round);
        }
      }
      if (state.delivery.round && path === `/api/v1/rounds/${state.delivery.round.id}`)
        return sendJson(res, 200, state.delivery.round);
      if (
        state.delivery.round &&
        path === `/api/v1/rounds/${state.delivery.round.id}/stop` &&
        req.method === 'POST'
      ) {
        state.delivery.round = {
          ...state.delivery.round,
          state: 'paused',
          revision: state.delivery.round.revision + 1,
        };
        state.delivery.reviews.forEach((review) =>
          review.items.forEach((item) => {
            if (item.state === 'sending') item.state = 'uncertain';
          }),
        );
        state.delivery.releaseSend?.();
        return sendJson(res, 200, state.delivery.round);
      }
      if (path === '/api/v1/rounds/active')
        if (
          state.delivery.round &&
          ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(
            state.delivery.round.state,
          )
        )
          return sendJson(res, 200, state.delivery.round);
      if (path === '/api/v1/rounds/active')
        return ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(
          state.round.state,
        )
          ? sendJson(res, 200, state.round)
          : error(res, 404);
      if (path === '/api/v1/rounds/capability')
        return sendJson(res, 200, {
          canStart:
            state.discoveryReady &&
            !['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(
              state.round.state,
            ),
          reason: ['queued', 'running', 'awaiting_input', 'stopping', 'paused'].includes(
            state.round.state,
          )
            ? 'round_active'
            : state.discoveryReady
              ? ''
              : 'fixture',
          intent: 'Synthetic discovery unavailable',
          outcome: 'discover',
          limits: allowance,
          sourceCount: 0,
        });
      if (path === '/api/v1/rounds/latest-completed')
        return sendJson(res, 200, state.latestCompletedDiscover);
      if (path === `/api/v1/rounds/${state.round.id}`) return sendJson(res, 200, state.round);
      if (
        path.startsWith('/api/v1/rounds/') &&
        state.previousRounds.has(path.split('/')[4]) &&
        path.split('/').length === 5
      )
        return sendJson(res, 200, state.previousRounds.get(path.split('/')[4]));
      if (path === `/api/v1/rounds/${state.round.id}/cards` && state.round.outcome === 'discover')
        return sendJson(res, 200, { items: state.recommendationCards });
      if (
        path === `/api/v1/rounds/${state.round.id}/history` ||
        path === `/api/v1/rounds/${state.round.id}/cards` ||
        (state.previousRounds.has(path.split('/')[4]) &&
          ['/history', '/cards'].some((suffix) => path.endsWith(suffix)))
      )
        return sendJson(res, 200, { items: [] });
      if (path === '/api/v1/rounds/prepare' && req.method === 'POST') {
        const previous = state.prepareResponses.get(payload.requestKey);
        if (previous) return sendJson(res, 200, previous);
        if (state.staleNextPrepare) {
          state.staleNextPrepare = false;
          state.round = { ...state.round, revision: state.round.revision + 1 };
          return error(res, 409);
        }
        if (payload.opportunityId !== opportunity.id || state.decision?.decision !== 'selected')
          return error(res, 409);
        if (
          state.round.state === 'paused' &&
          (payload.replacePaused?.roundId !== state.round.id ||
            payload.replacePaused?.expectedRevision !== state.round.revision)
        )
          return error(res, 409);
        if (['queued', 'running', 'awaiting_input', 'stopping'].includes(state.round.state))
          return error(res, 409);
        state.previousRounds.set(state.round.id, state.round);
        state.round = {
          ...round,
          id: `synthetic-prepare-${state.prepareResponses.size + 1}`,
          state: 'running',
          revision: 2,
          step: 'research',
          deliverableStatus: 'pending',
          report: {},
        };
        state.prepareResponses.set(payload.requestKey, state.round);
        if (state.failFirstPrepareResponse) {
          state.failFirstPrepareResponse = false;
          return error(res, 503);
        }
        return sendJson(res, 201, state.round);
      }
      if (path === '/api/v1/rounds' && req.method === 'POST') {
        const previous = state.discoveryResponses.get(payload.requestKey);
        if (previous) return sendJson(res, 200, previous);
        if (state.staleNextDiscovery) {
          state.staleNextDiscovery = false;
          state.round = { ...state.round, revision: state.round.revision + 1 };
          return error(res, 409);
        }
        if (!state.discoveryReady) return error(res, 503);
        if (
          state.round.state === 'paused' &&
          (payload.replacePaused?.roundId !== state.round.id ||
            payload.replacePaused?.expectedRevision !== state.round.revision)
        )
          return error(res, 409);
        if (['queued', 'running', 'awaiting_input', 'stopping'].includes(state.round.state))
          return error(res, 409);
        state.previousRounds.set(state.round.id, state.round);
        state.round = {
          ...round,
          id: `synthetic-discover-${state.discoveryResponses.size + 1}`,
          outcome: 'discover',
          intent: 'Find sourced opportunities',
          state: 'running',
          revision: 1,
          scope: {
            inputRefs: [],
            resources: [],
            operations: ['opportunity.discover'],
            delegates: ['codex-runner'],
          },
          step: 'searching',
          deliverableStatus: 'pending',
        };
        state.discoveryResponses.set(payload.requestKey, state.round);
        if (state.failFirstDiscoveryResponse) {
          state.failFirstDiscoveryResponse = false;
          return error(res, 503);
        }
        return sendJson(res, 201, state.round);
      }
      if (path === '/api/v1/rounds/process-input' && req.method === 'POST') {
        let response = state.processResponses.get(payload.requestKey);
        if (response) return sendJson(res, 200, response);
        if (
          payload.targetKind === 'campaign' &&
          (payload.targetId !== 'active' ||
            payload.expectedRevision !== state.profileVersion ||
            Boolean(payload.text) ||
            !(payload.sourceUrl || payload.originalText))
        )
          return error(res, 400);
        if (
          payload.targetKind === 'application_pack' &&
          (!state.packs.some(
            (pack) =>
              pack.id === payload.targetId &&
              pack.version === payload.expectedRevision &&
              pack.opportunityRevision === opportunity.revision &&
              pack.profileRevision === profile.version,
          ) ||
            !payload.text)
        )
          return error(res, 409);
        if (
          payload.targetKind === 'profile' &&
          (payload.targetId !== 'current' ||
            payload.expectedRevision !== state.profileVersion ||
            !payload.text)
        )
          return error(res, 409);
        if (!response) {
          if (['queued', 'running', 'awaiting_input', 'stopping'].includes(state.round.state))
            return error(res, 409);
          if (
            state.round.state === 'paused' &&
            (payload.replacePaused?.roundId !== state.round.id ||
              payload.replacePaused?.expectedRevision !== state.round.revision)
          )
            return error(res, 409);
          response = {
            round: {
              ...round,
              id: `synthetic-input-${state.processResponses.size + 1}`,
              outcome: 'process_input',
              intent: 'Handle owner input',
              scope: {
                inputRefs: [`synthetic-input:${payload.requestKey}`],
                resources:
                  payload.targetKind === 'application_pack'
                    ? [`opportunity:${opportunity.id}`]
                    : [`${payload.targetKind}:${payload.targetId}`],
                operations:
                  payload.targetKind === 'application_pack'
                    ? ['application_pack.prepare']
                    : payload.targetKind === 'profile'
                      ? ['preferences.correct']
                      : payload.targetKind === 'campaign'
                        ? ['opportunity.source_save']
                        : [`${payload.targetKind}.correct`],
                delegates: ['codex-runner'],
              },
              state: 'running',
              revision: 1,
              step: 'processing',
              deliverableStatus: 'pending',
              report: {},
            },
            ...(payload.replacePaused ? { replacedRoundId: payload.replacePaused.roundId } : {}),
          };
          state.processResponses.set(payload.requestKey, response);
          state.currentInput = payload;
          state.previousRounds.set(state.round.id, state.round);
          state.round = response.round;
          if (payload.targetKind === 'profile') state.profileVersion += 1;
        }
        if (state.failFirstProcessResponse) {
          state.failFirstProcessResponse = false;
          return error(res, 503);
        }
        return sendJson(res, 201, response);
      }
      if (path === `/api/v1/rounds/${state.round.id}/stop` && req.method === 'POST') {
        state.round = { ...state.round, state: 'paused', revision: state.round.revision + 1 };
        return sendJson(res, 200, state.round);
      }
      if (path === `/api/v1/rounds/${state.round.id}/resume` && req.method === 'POST') {
        state.round = { ...state.round, state: 'running', revision: state.round.revision + 1 };
        return sendJson(res, 200, state.round);
      }
      if (path === '/api/v1/opportunities')
        return sendJson(res, 200, { items: [{ opportunity, likelyDuplicates: [] }] });
      if (path === `/api/v1/opportunities/${opportunity.id}`)
        return sendJson(res, 200, { opportunity, likelyDuplicates: [] });
      if (path === '/api/v1/companies')
        return sendJson(res, 200, { items: [{ company, likelyDuplicates: [] }] });
      if (path === '/api/v1/ingestions') return sendJson(res, 200, { items: [] });
      if (path === '/api/v1/runtime-status')
        return sendJson(res, 200, {
          ingestionAvailable: false,
          organisationAvailable: false,
          collectionAvailable: false,
        });
      if (path === '/api/v1/organisation/categories') return sendJson(res, 200, { categories: [] });
      if (path === '/api/v1/organisation/summaries') return sendJson(res, 200, { items: [] });
      if (path === `/api/v1/opportunities/${opportunity.id}/organisation`)
        return sendJson(res, 200, {
          status: 'not_assessed',
          current: null,
          latestHistorical: null,
        });
      if (path === `/api/v1/opportunities/${opportunity.id}/screening`)
        return sendJson(res, 200, { status: 'not_assessed', confirmed: false, current: null });
      if (path === `/api/v1/opportunities/${opportunity.id}/decision`) {
        if (req.method === 'GET')
          return state.decision ? sendJson(res, 200, state.decision) : error(res, 404);
        if (
          payload.expectedOpportunityRevision !== opportunity.revision ||
          payload.expectedDecisionRevision !== (state.decision?.revision || 0)
        )
          return error(res, 409);
        state.decision = {
          id: 'synthetic-decision',
          opportunityId: opportunity.id,
          decision: payload.decision,
          revision: (state.decision?.revision || 0) + 1,
          opportunityRevision: opportunity.revision,
          auditId: 'synthetic-audit',
          createdAt: time,
        };
        return sendJson(res, 201, state.decision);
      }
      if (path === `/api/v1/opportunities/${opportunity.id}/application-packs`)
        return sendJson(res, 200, { items: state.packs });
      if (path.startsWith('/api/v1/application-packs/') && path.endsWith('/pdf')) {
        const bytes = Buffer.from('%PDF-1.4\n% Synthetic UI fixture only\n%%EOF');
        res.writeHead(200, { 'Content-Type': 'application/pdf', 'Content-Length': bytes.length });
        return res.end(bytes);
      }
      if (path.startsWith('/api/v1/application-packs/') && path.endsWith('/source.zip')) {
        const bytes = Buffer.from('PK\x03\x04synthetic');
        res.writeHead(200, { 'Content-Type': 'application/zip', 'Content-Length': bytes.length });
        return res.end(bytes);
      }
      if (path.startsWith('/api/v1/application-packs/')) {
        const id = path.split('/').pop();
        return state.failDetail
          ? error(res, 503)
          : state.details.has(id)
            ? sendJson(res, 200, state.details.get(id))
            : error(res, 404);
      }
      if (
        path === '/api/v1/owner-instructions' ||
        path === '/api/v1/relationships/counterparties' ||
        path === '/api/v1/relationships/events' ||
        path === '/api/v1/changes'
      )
        return sendJson(res, 200, { items: [] });
      if (path === `/api/v1/opportunities/${opportunity.id}/routes`)
        return sendJson(res, 200, { items: state.delivery.routeSupported ? [deliveryRoute] : [] });
      return error(res, 503);
    } catch (cause) {
      sendJson(res, 500, { error: { message: `Synthetic fixture error: ${cause.message}` } });
    }
  });
  await new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolve);
  });
  const address = server.address();
  return {
    url: `http://127.0.0.1:${address.port}`,
    state,
    completePreparation() {
      state.round = {
        ...state.round,
        state: 'completed',
        revision: state.round.revision + 1,
        step: 'done',
        deliverableStatus: 'saved',
        report: { summary: 'Synthetic pack saved.' },
      };
      const newest = {
        ...pack2,
        id: 'synthetic-pack-3',
        version: 3,
        createdAt: '2026-09-23T13:00:00Z',
      };
      state.packs = [newest, ...state.packs];
      state.details.set(newest.id, { ...newest, manifest: manifest(newest) });
    },
    pauseRound() {
      state.round = {
        ...state.round,
        state: 'paused',
        revision: state.round.revision + 1,
        report: state.round.outcome === 'process_input' ? {} : { summary: 'Prior work paused.' },
      };
    },
    completeInput() {
      const input = state.currentInput;
      let report = { summary: 'Synthetic round completed.' };
      if (state.round.outcome === 'process_input' && input) {
        if (input.targetKind === 'application_pack') {
          const newest = {
            ...pack2,
            id: 'synthetic-pack-4',
            version: 4,
            createdAt: '2026-09-23T14:00:00Z',
          };
          state.packs = [newest, ...state.packs];
          state.details.set(newest.id, { ...newest, manifest: manifest(newest) });
          report = {
            code: 'pack_ready',
            opportunityId: opportunity.id,
            packId: newest.id,
            version: newest.version,
            materialUnknowns: ['Application destination remains unconfirmed.'],
            remaining: allowance,
          };
        } else {
          let code = 'input_applied';
          let appliedChanges = [];
          let unresolved = [];
          if (input.targetKind === 'profile') {
            appliedChanges = [
              inputChange('preferences.correct', 'preferences', state.profileVersion),
            ];
          } else if (
            input.targetKind === 'campaign' &&
            input.sourceUrl === 'https://example.invalid/jobs/second-role'
          ) {
            code = 'unsupported_source_url';
            unresolved = [
              'The saved vacancy URL could not be verified from a supported source. Paste the complete vacancy text to process it.',
            ];
          } else if (input.targetKind === 'campaign') {
            appliedChanges = [
              inputChange('opportunity.source_save', 'opportunity', opportunity.revision),
            ];
            if (state.organisationLimitNext) {
              code = 'organisation_unresolved';
              unresolved = [
                'The vacancy was saved, but semantic organisation could not be recorded.',
              ];
              state.organisationLimitNext = false;
            }
          }
          report = {
            code,
            originalProfileVersion:
              input.targetKind === 'profile' || input.targetKind === 'campaign'
                ? input.expectedRevision
                : state.profileVersion,
            effectiveProfileVersion: state.profileVersion,
            appliedChanges,
            unresolved,
          };
        }
      }
      state.round = {
        ...state.round,
        state: 'completed',
        revision: state.round.revision + 1,
        deliverableStatus:
          state.round.outcome !== 'process_input' ||
          report.code === 'pack_ready' ||
          (report.appliedChanges?.length && !report.unresolved?.length)
            ? 'complete'
            : 'partial',
        report,
      };
    },
    completeInterviewBrief({ disposition = 'unresolved', selectedId = 'research' } = {}) {
      const record = [...state.interviews.values()].at(-1);
      if (!record) throw new Error('No synthetic interview to complete');
      const id = record.interview.id;
      record.interview.brief = {
        inputSha256: '7'.repeat(64),
        input: {
          interviewId: id,
          opportunityId: opportunity.id,
          roleTitle: opportunity.title,
          employerName: company.name,
          context: [
            {
              id: 'owner-invitation',
              kind: 'invitation',
              revision: '1',
              sha256: '8'.repeat(64),
              body: record.interview.context,
            },
          ],
          careerSources: [
            {
              id: 'career-project',
              name: 'Approved personal project',
              sha256: '9'.repeat(64),
              approved: true,
              body: 'Built a personal research prototype as a learning project.',
            },
          ],
          draft: {
            focus: [
              {
                id: 'research',
                why: {
                  text: 'Prepare to discuss the research prototype.',
                  citations: [
                    { sourceId: 'career-project', excerpt: 'personal research prototype' },
                  ],
                },
              },
            ],
            questions: [
              {
                text: 'How will this role use research?',
                why: {
                  text: 'The invitation mentions research work.',
                  citations: [{ sourceId: 'owner-invitation', excerpt: 'research work' }],
                },
              },
            ],
            examples: [
              {
                title: 'Research prototype',
                experienceKind: 'personal_project',
                contextBasis: {
                  sourceId: 'career-project',
                  excerpt: 'personal research prototype',
                },
                situation: {
                  text: 'A personal learning project.',
                  citations: [{ sourceId: 'career-project', excerpt: 'learning project' }],
                },
                action: {
                  text: 'Built a research prototype.',
                  citations: [
                    { sourceId: 'career-project', excerpt: 'Built a personal research prototype' },
                  ],
                },
                unknownResult: 'Impact is not independently verified.',
              },
            ],
            scheduleClaims: [
              {
                startRfc3339: '2026-10-02T11:00:00+02:00',
                mode: 'video',
                citation: { sourceId: 'owner-invitation', excerpt: '2026-10-02 at 11:00 by video' },
              },
            ],
            unknowns: ['Meeting link and interviewer names are unconfirmed.'],
          },
        },
      };
      record.interview.focus = {
        disposition,
        inputSha256: 'b'.repeat(64),
        ...(disposition === 'selected' ? { selectedId } : {}),
      };
      state.round = {
        ...state.round,
        state: 'completed',
        revision: state.round.revision + 1,
        step: 'done',
        deliverableStatus: 'brief_saved',
        report: { interviewId: id, focus: disposition },
      };
    },
    completeInterviewDebrief() {
      const record = [...state.interviews.values()].at(-1);
      const debrief = record?.debriefs[0];
      if (!debrief) throw new Error('No synthetic debrief to complete');
      debrief.attribution = 'owner_reported';
      debrief.observations = [
        {
          kind: 'discussed',
          detail: {
            text: 'Discussed the research prototype.',
            citations: [
              { sourceId: 'owner_interview_notes', excerpt: 'discussed the research prototype' },
            ],
          },
        },
      ];
      debrief.unknowns = ['Hiring decision not reported.'];
      state.round = {
        ...state.round,
        state: 'completed',
        revision: state.round.revision + 1,
        step: 'done',
        deliverableStatus: 'debrief_saved',
        report: { debriefId: debrief.id },
      };
    },
    close: () =>
      new Promise((resolve, reject) => server.close((err) => (err ? reject(err) : resolve()))),
  };
}
