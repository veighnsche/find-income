import assert from 'node:assert/strict';
import { startFixture } from './fixture.mjs';

const time = '2026-09-23T12:00:00Z';
const profileVersion = 7;
const roleId = 'synthetic-role-1';
const roundId = 'synthetic-saved-advice';
const currentness = { status: 'current', code: 'current', checkedAt: time };
const roleRef = (options = {}) => ({
  id: `opportunity:${roleId}`,
  kind: 'current_opportunity_state',
  revision: '3'.repeat(64),
  omittedBytes: 0,
  opportunityRevision: 3,
  ownerDecisionRevision: options.selected ? 1 : undefined,
  screeningAssessmentId: '',
  organisationAssessmentId: '',
  packId: options.pack ? 'synthetic-pack-2' : undefined,
  packVersion: options.pack ? 2 : undefined,
  packContentSha256: options.pack ? 'b'.repeat(64) : undefined,
});

function configure(fixture, recommendation) {
  fixture.state.round = {
    ...fixture.state.round,
    id: roundId,
    outcome: 'process_input',
    intent: 'Process saved owner input',
    state: 'completed',
    generation: 2,
    step: 'done',
    report: {
      summary: 'One sourced role was assessed and saved.',
      recommendation,
      recommendationCurrentness: { ...currentness },
    },
    completedAt: time,
  };
}

function choice(action, target, refs = []) {
  return {
    status: 'selected',
    action,
    target,
    profileVersion,
    roundId,
    roundGeneration: 1,
    reason: `Saved source evidence supports ${action.replaceAll('_', ' ')} for owner review.`,
    candidateId: `synthetic-${action}`,
    decisionAttemptId: 'synthetic-decision-attempt',
    decisionInputSha256: 'a'.repeat(64),
    sourceRefs: [
      { id: 'profile:0', kind: 'owner_profile', revision: '7', omittedBytes: 0 },
      {
        id: `round:${roundId}`,
        kind: 'commissioned_outcome_facts',
        revision: '2'.repeat(64),
        omittedBytes: 0,
      },
      ...refs,
    ],
    unavailableActions: [],
    omittedOpportunities: false,
  };
}

function outcomeChoice(action, target) {
  return {
    ...choice(action, target),
    sourceRefs: [
      { id: 'profile:0', kind: 'owner_profile', revision: '7', omittedBytes: 0 },
      {
        id: `round:${roundId}`,
        kind: 'commissioned_outcome_facts',
        revision: 'f'.repeat(64),
        omittedBytes: 0,
      },
    ],
  };
}

function outcomeRound(fixture, outcome, recommendation, report = {}, state = 'completed') {
  fixture.state.round = {
    ...fixture.state.round,
    id: roundId,
    outcome,
    state,
    generation: 2,
    report: { ...report, recommendation, recommendationCurrentness: { ...currentness } },
    completedAt: time,
  };
  fixture.state.latestCompletedAll = fixture.state.round;
}

function selectRole(fixture) {
  fixture.state.decision = {
    id: 'synthetic-decision',
    opportunityId: roleId,
    decision: 'selected',
    revision: 1,
    opportunityRevision: 3,
    auditId: 'synthetic-audit',
    createdAt: time,
  };
}

async function openHome(browser, fixture) {
  const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
  await context.addInitScript((id) => localStorage.setItem('jobseek.last-round', id), roundId);
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  await page.goto(fixture.url, { waitUntil: 'networkidle' });
  const agency = page.getByRole('region', { name: 'Agency work' });
  await agency.getByRole('heading', { name: /Your .* ready to review/ }).waitFor();
  return { context, page, agency };
}

function recommendationPosts(fixture) {
  return fixture.state.requests.filter(
    (request) =>
      request.method === 'POST' &&
      (request.path === '/api/v1/rounds' || request.path === '/api/v1/rounds/prepare'),
  );
}

export async function runRecommendationSmoke(browser) {
  // Prepare recommendation navigates to the exact selected role's existing
  // owner-controlled Prepare action; navigation itself performs no commission.
  const prepare = await startFixture();
  try {
    selectRole(prepare);
    prepare.state.packs = [];
    configure(
      prepare,
      choice(
        'prepare',
        {
          kind: 'opportunity',
          id: roleId,
          revision: 3,
          ownerDecisionRevision: 1,
        },
        [roleRef({ selected: true })],
      ),
    );
    const { context, page, agency } = await openHome(browser, prepare);
    await agency
      .getByRole('button', { name: 'Open selected role to prepare its application' })
      .click();
    const preparation = page.getByRole('region', { name: 'Application preparation' });
    await preparation.getByRole('button', { name: 'Prepare application' }).waitFor();
    assert.equal(recommendationPosts(prepare).length, 0);
    await preparation.getByRole('button', { name: 'Prepare application' }).click();
    assert.deepEqual(recommendationPosts(prepare).at(-1).payload.opportunityId, roleId);
    await context.close();
  } finally {
    await prepare.close();
  }

  // Review pack opens the exact saved pack in its opportunity, then a manual
  // older-version selection remains under owner control.
  const pack = await startFixture();
  try {
    selectRole(pack);
    configure(
      pack,
      choice(
        'review_pack',
        {
          kind: 'application_pack',
          id: 'synthetic-pack-2',
          revision: 2,
          contentSha256: 'b'.repeat(64),
          opportunityId: roleId,
          opportunityRevision: 3,
          ownerDecisionRevision: 1,
        },
        [roleRef({ selected: true, pack: true })],
      ),
    );
    const { context, page, agency } = await openHome(browser, pack);
    await agency.getByRole('button', { name: 'Open exact saved application pack' }).click();
    await page.getByRole('heading', { name: 'Version 2: Synthetic Research Role' }).waitFor();
    await page.getByRole('button', { name: /Previous · version 1/ }).click();
    await page.getByRole('heading', { name: 'Version 1: Synthetic Research Role' }).waitFor();
    assert.equal(recommendationPosts(pack).length, 0);
    await context.close();
  } finally {
    await pack.close();
  }

  // Stale currentness/profile suppresses the selected CTA while preserving the historical reason.
  const stale = await startFixture();
  try {
    configure(stale, choice('prepare', { kind: 'opportunity', id: roleId, revision: 3, ownerDecisionRevision: 1 }));
    stale.state.profileVersion = 8;
    const { context, page, agency } = await openHome(browser, stale);
    await agency.getByText(/Historical advice:/).waitFor();
    assert.equal(
      await agency.getByRole('button', { name: 'Open selected role to prepare its application' }).isDisabled(),
      true,
    );
    assert.equal(recommendationPosts(stale).length, 0);
    await context.close();
  } finally {
    await stale.close();
  }
  const serverStale = await startFixture();
  try {
    configure(serverStale, choice('prepare', { kind: 'opportunity', id: roleId, revision: 3, ownerDecisionRevision: 1 }));
    serverStale.state.round.report.recommendationCurrentness = {
      status: 'stale',
      code: 'round_result_changed',
      checkedAt: time,
    };
    const { context, agency } = await openHome(browser, serverStale);
    await agency.getByText(/Historical advice: round_result_changed/).waitFor();
    assert.equal(
      await agency.getByRole('button', { name: 'Open selected role to prepare its application' }).isDisabled(),
      true,
    );
    await context.close();
  } finally {
    await serverStale.close();
  }
  const staleTarget = await startFixture();
  try {
    selectRole(staleTarget);
    staleTarget.state.packs = [];
    configure(
      staleTarget,
      choice(
        'prepare',
        {
          kind: 'opportunity',
          id: roleId,
          revision: 3,
          ownerDecisionRevision: 1,
        },
        [roleRef({ selected: true })],
      ),
    );
    staleTarget.state.decision = { ...staleTarget.state.decision, revision: 2 };
    const { context, agency } = await openHome(browser, staleTarget);
    await agency.getByText(/The selected role or owner decision has changed/).waitFor();
    assert.equal(
      await agency
        .getByRole('button', { name: 'Open selected role to prepare its application' })
        .isDisabled(),
      true,
    );
    assert.equal(recommendationPosts(staleTarget).length, 0);
    await context.close();
  } finally {
    await staleTarget.close();
  }
  const unresolved = await startFixture();
  try {
    configure(unresolved, {
      status: 'unresolved',
      code: 'jev_abstained',
      profileVersion,
      roundId,
      roundGeneration: 1,
      sourceRefs: [],
    });
    unresolved.state.round.report.recommendationCurrentness = {
      status: 'unavailable',
      code: 'recommendation_not_selected',
      checkedAt: time,
    };
    const { context, page, agency } = await openHome(browser, unresolved);
    await agency.getByText(/The assessment did not choose/).waitFor();
    assert.equal(await agency.getByRole('button', { name: 'Find more sourced opportunities' }).count(), 0);
    assert.equal(recommendationPosts(unresolved).length, 0);
    await context.close();
  } finally {
    await unresolved.close();
  }

  const result = await startFixture();
  try {
    outcomeRound(
      result,
      'process_input',
      outcomeChoice('review_result', { kind: 'round', id: roundId }),
      {
        code: 'partial',
        appliedChanges: [{ operation: 'preferences.correct' }],
        unresolved: ['Source date remains unknown.'],
      },
    );
    const { context, page, agency } = await openHome(browser, result);
    await agency.getByRole('button', { name: 'Review this saved round result' }).click();
    await page
      .getByRole('region', { name: 'Saved round result' })
      .getByText('Source date remains unknown.')
      .waitFor();
    assert.equal(result.state.requests.filter((request) => request.method === 'POST').length, 0);
    await context.close();
  } finally {
    await result.close();
  }

  const comparison = await startFixture();
  try {
    comparison.state.offerInput = {
      offers: [
        'Example Labs employment gross EUR 5000 per month, 32 hours, holiday included.',
        'Research Studio employment gross EUR 6000 per month, 32 hours, holiday included.',
        'Project Client contract project USD 7000; holiday terms are unspecified.',
      ],
    };
    comparison.state.round = {
      ...comparison.state.round,
      id: roundId,
      outcome: 'compare_offers',
      state: 'running',
      scope: {
        ...comparison.state.round.scope,
        inputRefs: ['offer_intake:synthetic-offer-intake-1'],
      },
    };
    comparison.completeOfferComparison();
    const saved = comparison.state.offerComparison;
    outcomeRound(
      comparison,
      'compare_offers',
      outcomeChoice('review_comparison', {
        kind: 'offer_comparison',
        id: saved.id,
        revision: 1,
        contentSha256: saved.comparison.inputSha256,
      }),
      { code: 'comparison_saved', comparisonId: saved.id, tradeoffStatus: saved.tradeoffStatus },
    );
    const { context, page, agency } = await openHome(browser, comparison);
    await agency.getByRole('button', { name: 'Open exact saved offer comparison' }).click();
    await page
      .getByRole('region', { name: 'Whole-offer comparison' })
      .getByRole('heading', { name: 'Saved whole-offer comparison' })
      .waitFor();
    assert.ok(
      comparison.state.requests.some(
        (request) => request.path === `/api/v1/offer-comparisons/${saved.id}`,
      ),
    );
    assert.equal(
      comparison.state.requests.filter((request) => request.method === 'POST').length,
      0,
    );
    comparison.state.round.report.recommendationCurrentness = {
      status: 'stale',
      code: 'comparison_changed',
      checkedAt: time,
    };
    await page.reload({ waitUntil: 'networkidle' });
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByText(/Historical advice: comparison_changed/)
      .waitFor();
    assert.equal(
      await page
        .getByRole('region', { name: 'Agency work' })
        .getByRole('button', { name: 'Open exact saved offer comparison' })
        .isDisabled(),
      true,
    );
    assert.equal(
      comparison.state.requests.filter((request) => request.method === 'POST').length,
      0,
    );
    await context.close();
  } finally {
    await comparison.close();
  }

  const delivery = await startFixture();
  try {
    const review = delivery.seedDeliveryReview();
    review.items[0].roundId = roundId;
    review.items[0].state = 'failed';
    outcomeRound(
      delivery,
      'deliver',
      outcomeChoice('review_delivery', { kind: 'delivery_review', id: review.id, revision: 1 }),
      { summary: 'One delivery item failed.' },
      'failed',
    );
    delivery.state.round.scope = {
      ...delivery.state.round.scope,
      inputRefs: [`delivery_review:${review.id}`],
    };
    delivery.state.latestCompletedAll = delivery.state.round;
    const context = await browser.newContext({ viewport: { width: 1180, height: 900 } });
    const page = await context.newPage();
    await page.goto(delivery.url, { waitUntil: 'networkidle' });
    const agency = page.getByRole('region', { name: 'Agency work' });
    await agency.getByRole('button', { name: 'Open exact saved delivery review' }).click();
    await page
      .getByRole('region', { name: 'Application delivery' })
      .getByText(/Your Send action approves/)
      .waitFor();
    assert.ok(
      delivery.state.requests.some(
        (request) =>
          request.path === '/api/v1/rounds/latest-completed' && request.query === '?outcome=all',
      ),
    );
    assert.equal(delivery.state.requests.filter((request) => request.method === 'POST').length, 0);
    await context.close();
  } finally {
    await delivery.close();
  }

  for (const action of ['review_interview', 'review_debrief']) {
    const fixture = await startFixture();
    try {
      const interviewId = 'synthetic-interview-advice';
      fixture.state.interviews.set(interviewId, {
        interview: {
          id: interviewId,
          opportunityId: roleId,
          opportunityRevision: 3,
          profileVersion: 7,
          context: 'Interview research work by video on 2026-10-02.',
          contextSha256: '6'.repeat(64),
          current: true,
          roundId: roundId,
          createdAt: time,
          updatedAt: time,
        },
        debriefs: [],
      });
      fixture.state.round = {
        ...fixture.state.round,
        id: roundId,
        outcome: 'interview_prepare',
        state: 'running',
        scope: { ...fixture.state.round.scope, inputRefs: [`interview:${interviewId}`] },
      };
      fixture.completeInterviewBrief();
      const debriefId = 'synthetic-debrief-advice';
      fixture.state.interviews.get(interviewId).debriefs.push({
        id: debriefId,
        interviewId,
        notes: 'We discussed the research prototype.',
        roundId,
        attribution: 'owner_reported',
        observations: [],
        createdAt: time,
        updatedAt: time,
      });
      const isDebrief = action === 'review_debrief';
      outcomeRound(
        fixture,
        isDebrief ? 'interview_debrief' : 'interview_prepare',
        outcomeChoice(action, {
          kind: isDebrief ? 'interview_debrief' : 'interview',
          id: isDebrief ? debriefId : interviewId,
          updatedAt: time,
        }),
        { code: 'brief_saved', interviewId, ...(isDebrief ? { debriefId } : {}) },
      );
      fixture.state.round.scope = {
        ...fixture.state.round.scope,
        resources: [`interview:${interviewId}`],
      };
      const { context, page, agency } = await openHome(browser, fixture);
      await agency
        .getByRole('button', {
          name: isDebrief
            ? 'Open exact saved interview debrief'
            : 'Open exact saved interview brief',
        })
        .click();
      const panel = page.getByRole('region', { name: 'Interview preparation' });
      if (isDebrief) await panel.locator(`#interview-debrief-${debriefId}`).waitFor();
      else await panel.getByRole('heading', { name: 'Prepared interview brief' }).waitFor();
      assert.equal(fixture.state.requests.filter((request) => request.method === 'POST').length, 0);
      await context.close();
    } finally {
      await fixture.close();
    }
  }
  console.log(
    'Recommendation UI smoke passed: latest all and remembered recovery, saved actions, exact record navigation, failed delivery, currentness refusal, unresolved results, and read-only reload.',
  );
}
