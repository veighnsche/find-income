import assert from 'node:assert/strict';
import { startFixture } from './fixture.mjs';

async function openInterview(browser, fixture, width = 1180) {
  const context = await browser.newContext({ viewport: { width, height: 844 } });
  const page = await context.newPage();
  page.setDefaultTimeout(20_000);
  await page.goto(fixture.url, { waitUntil: 'networkidle' });
  await page.getByRole('button', { name: 'Synthetic Research Role' }).click();
  const panel = page.getByRole('region', { name: 'Interview preparation' });
  await panel.getByRole('heading', { name: 'Prepare for an interview' }).waitFor();
  return { context, page, panel };
}

export async function runInterviewSmoke(browser) {
  const fixture = await startFixture();
  try {
    const { context, page, panel } = await openInterview(browser, fixture, 390);
    const invitation =
      'Invitation: research work interview on 2026-10-02 at 11:00 by video. Please discuss the research prototype.';
    await panel.getByRole('textbox', { name: 'Invitation and context' }).fill(invitation);
    fixture.state.failFirstInterviewPrepareResponse = true;
    await panel.getByRole('button', { name: 'Prepare interview brief' }).click();
    await panel.getByRole('button', { name: 'Recover same interview Prepare request' }).waitFor();
    const prepares = () =>
      fixture.state.requests.filter(
        (request) => request.path === '/api/v1/interviews/prepare' && request.method === 'POST',
      );
    assert.equal(prepares().length, 1);
    assert.deepEqual(Object.keys(prepares()[0].payload).sort(), [
      'context',
      'opportunityId',
      'requestKey',
    ]);
    assert.equal(prepares()[0].payload.context, invitation);
    assert.equal(prepares()[0].payload.opportunityId, 'synthetic-role-1');
    await page.reload({ waitUntil: 'networkidle' });
    const recovered = page.getByRole('region', { name: 'Interview preparation' });
    assert.equal(
      await recovered.getByRole('textbox', { name: 'Invitation and context' }).inputValue(),
      invitation,
    );
    assert.equal(prepares().length, 1, 'reload must not commission work');
    await recovered.getByRole('button', { name: 'Recover same interview Prepare request' }).click();
    await recovered.getByText(/Interview preparation commissioned/).waitFor();
    assert.equal(prepares().length, 2);
    assert.deepEqual(prepares()[1].payload, prepares()[0].payload);
    assert.equal(fixture.state.interviews.size, 1);
    assert.deepEqual(fixture.state.round.scope.inputRefs, ['interview:synthetic-interview-1']);

    fixture.completeInterviewBrief();
    await recovered.getByRole('button', { name: 'Refresh interviews' }).click();
    await recovered.getByRole('heading', { name: 'Prepared interview brief' }).waitFor();
    assert.match(await recovered.innerText(), /Focus unresolved by Jev/);
    assert.match(await recovered.innerText(), /How will this role use research/);
    assert.match(await recovered.innerText(), /Approved experience examples/);
    assert.match(await recovered.innerText(), /Meeting link and interviewer names are unconfirmed/);
    assert.match(await recovered.innerText(), /Schedule claims from supplied context/);
    await recovered.getByText('Source snapshots and brief audit').click();
    assert.match(await recovered.innerText(), /Approved personal project/);

    const notes =
      'We discussed the research prototype. I was clear on the design. The hiring decision was not reported.';
    await recovered.getByRole('textbox', { name: 'Debrief notes' }).fill(notes);
    fixture.state.failFirstInterviewDebriefResponse = true;
    await recovered.getByRole('button', { name: 'Record interview debrief' }).click();
    await recovered.getByRole('button', { name: 'Recover same debrief request' }).waitFor();
    const debriefs = () =>
      fixture.state.requests.filter(
        (request) =>
          request.path === '/api/v1/interviews/synthetic-interview-1/debrief' &&
          request.method === 'POST',
      );
    assert.equal(debriefs().length, 1);
    assert.deepEqual(Object.keys(debriefs()[0].payload).sort(), ['notes', 'requestKey']);
    assert.equal(debriefs()[0].payload.notes, notes);
    await page.reload({ waitUntil: 'networkidle' });
    const afterReload = page.getByRole('region', { name: 'Interview preparation' });
    assert.equal(
      await afterReload.getByRole('textbox', { name: 'Debrief notes' }).inputValue(),
      notes,
    );
    assert.equal(debriefs().length, 1);
    await afterReload.getByRole('button', { name: 'Recover same debrief request' }).click();
    await afterReload.getByText(/Debrief commissioned/).waitFor();
    assert.equal(debriefs().length, 2);
    assert.deepEqual(debriefs()[1].payload, debriefs()[0].payload);
    fixture.completeInterviewDebrief();
    await afterReload.getByRole('button', { name: 'Refresh interviews' }).click();
    await afterReload.getByText(/Observations are attributed to your reported notes/).waitFor();
    assert.match(
      await afterReload.innerText(),
      /Observations are attributed to your reported notes/,
    );
    assert.match(await afterReload.innerText(), /Discussed the research prototype/);
    assert.match(await afterReload.innerText(), /Hiring decision not reported/);
    await afterReload.getByText('Original debrief notes and audit').click();
    assert.match(await afterReload.innerText(), new RegExp(notes.replaceAll('.', '\\.')));

    // The list/detail route is the record lookup, even after local request IDs are gone.
    await page.evaluate(() => {
      localStorage.removeItem('jobseek.interview-prepare.synthetic-role-1');
      localStorage.removeItem('jobseek.interview-debrief-request.synthetic-interview-1');
    });
    await page.reload({ waitUntil: 'networkidle' });
    const persisted = page.getByRole('region', { name: 'Interview preparation' });
    await persisted.getByRole('heading', { name: 'Prepared interview brief' }).waitFor();
    assert.match(await persisted.innerText(), /Discussed the research prototype/);
    assert.equal(prepares().length, 2);
    assert.equal(debriefs().length, 2);
    fixture.state.interviews.get('synthetic-interview-1').interview.current = false;
    await persisted.getByRole('button', { name: 'Refresh interviews' }).click();
    await persisted.getByText(/Historical brief: the role or campaign brief changed/).waitFor();
    assert.equal(
      await persisted.getByRole('button', { name: 'Record interview debrief' }).isDisabled(),
      true,
    );
    const width = await page.evaluate(() => ({
      client: document.documentElement.clientWidth,
      scroll: document.documentElement.scrollWidth,
    }));
    assert.equal(width.client, 390);
    assert.ok(
      width.scroll <= width.client,
      `interview horizontal overflow: ${width.scroll} > ${width.client}`,
    );
    await context.close();
  } finally {
    await fixture.close();
  }

  // The server's Jev decision hash binds a different input from the brief hash.
  // Selection must resolve by the server-validated candidate ID.
  const selectedFocus = await startFixture();
  try {
    const { context, panel } = await openInterview(browser, selectedFocus);
    await panel
      .getByRole('textbox', { name: 'Invitation and context' })
      .fill('Complete invitation for selected-focus interview preparation.');
    await panel.getByRole('button', { name: 'Prepare interview brief' }).click();
    await panel.getByText(/Interview preparation commissioned/).waitFor();
    selectedFocus.completeInterviewBrief({ disposition: 'selected' });
    const saved = [...selectedFocus.state.interviews.values()][0].interview;
    assert.notEqual(saved.focus.inputSha256, saved.brief.inputSha256);
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByText(/Selected focus:/).waitFor();
    assert.match(await panel.innerText(), /Prepare to discuss the research prototype/);

    saved.focus.selectedId = 'missing-candidate';
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByText(/Saved focus selection is unavailable in this brief/).waitFor();
    saved.focus = undefined;
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByText(/Focus not available yet/).waitFor();
    await context.close();
  } finally {
    await selectedFocus.close();
  }

  // A committed round remains discoverable and stoppable while the original
  // commission POST has not returned. The exact saved key survives Stop.
  const heldResponse = await startFixture();
  try {
    const { context, page, panel } = await openInterview(browser, heldResponse);
    await panel
      .getByRole('textbox', { name: 'Invitation and context' })
      .fill('Complete invitation while Prepare response remains open.');
    heldResponse.state.deferInterviewPrepareResponse = true;
    heldResponse.state.failFirstInterviewPrepareResponse = true;
    await panel.getByRole('button', { name: 'Prepare interview brief' }).click();
    const heldPrepare = heldResponse.state.requests.find(
      (request) => request.path === '/api/v1/interviews/prepare',
    ).payload;
    heldResponse.state.round.requestKey = 'another-interview-request';
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByText(/Another interview prepare round is running/).waitFor();
    assert.equal(await panel.getByRole('button', { name: 'Stop interview work' }).count(), 0);
    heldResponse.state.round.requestKey = heldPrepare.requestKey;
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByRole('button', { name: 'Stop interview work' }).waitFor();
    assert.equal(heldResponse.state.interviewPrepareResponsePending, true);
    const prepareRequest = heldResponse.state.requests.find(
      (request) => request.path === '/api/v1/interviews/prepare',
    ).payload;
    assert.deepEqual(
      await page.evaluate(() =>
        JSON.parse(localStorage.getItem('jobseek.interview-prepare.synthetic-role-1')),
      ),
      prepareRequest,
    );
    await panel.getByRole('button', { name: 'Stop interview work' }).click();
    assert.equal(heldResponse.state.round.state, 'paused');
    assert.equal(heldResponse.state.interviewPrepareResponsePending, true);
    heldResponse.state.releaseInterviewPrepare();
    await panel.getByRole('button', { name: 'Recover same interview Prepare request' }).waitFor();
    await panel.getByRole('button', { name: 'Recover same interview Prepare request' }).click();
    await panel.getByText(/Interview preparation commissioned/).waitFor();
    const prepareCalls = heldResponse.state.requests.filter(
      (request) => request.path === '/api/v1/interviews/prepare',
    );
    assert.equal(prepareCalls.length, 2);
    assert.deepEqual(prepareCalls[1].payload, prepareCalls[0].payload);

    heldResponse.completeInterviewBrief();
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByRole('heading', { name: 'Prepared interview brief' }).waitFor();
    await panel
      .getByRole('textbox', { name: 'Debrief notes' })
      .fill('Complete reported notes while Debrief response remains open.');
    heldResponse.state.deferInterviewDebriefResponse = true;
    heldResponse.state.failFirstInterviewDebriefResponse = true;
    await panel.getByRole('button', { name: 'Record interview debrief' }).click();
    await panel.getByRole('button', { name: 'Refresh interviews' }).click();
    await panel.getByRole('button', { name: 'Stop interview work' }).waitFor();
    assert.equal(heldResponse.state.interviewDebriefResponsePending, true);
    const debriefRequest = heldResponse.state.requests.find(
      (request) => request.path === '/api/v1/interviews/synthetic-interview-1/debrief',
    ).payload;
    assert.deepEqual(
      await page.evaluate(() =>
        JSON.parse(localStorage.getItem('jobseek.interview-debrief-request.synthetic-interview-1')),
      ),
      debriefRequest,
    );
    await panel.getByRole('button', { name: 'Stop interview work' }).click();
    assert.equal(heldResponse.state.round.state, 'paused');
    assert.equal(heldResponse.state.interviewDebriefResponsePending, true);
    heldResponse.state.releaseInterviewDebrief();
    await panel.getByRole('button', { name: 'Recover same debrief request' }).waitFor();
    await panel.getByRole('button', { name: 'Recover same debrief request' }).click();
    await panel.getByText(/Debrief commissioned/).waitFor();
    const debriefCalls = heldResponse.state.requests.filter(
      (request) => request.path === '/api/v1/interviews/synthetic-interview-1/debrief',
    );
    assert.equal(debriefCalls.length, 2);
    assert.deepEqual(debriefCalls[1].payload, debriefCalls[0].payload);
    await context.close();
  } finally {
    heldResponse.state.releaseInterviewPrepare?.();
    heldResponse.state.releaseInterviewDebrief?.();
    await heldResponse.close();
  }

  // A paused unrelated round keeps the invitation draft and directs the owner
  // to existing work controls. A definite rejection can be explicitly revised.
  const conflict = await startFixture();
  try {
    conflict.state.round = { ...conflict.state.round, state: 'paused', outcome: 'discover' };
    const { context, page, panel } = await openInterview(browser, conflict);
    await panel
      .getByRole('textbox', { name: 'Invitation and context' })
      .fill('Complete interview invitation for the same role.');
    await panel.getByText(/Another discover round is paused/).waitFor();
    assert.equal(
      await panel.getByRole('button', { name: 'Prepare interview brief' }).isDisabled(),
      true,
    );
    await page.reload({ waitUntil: 'networkidle' });
    assert.equal(
      await page
        .getByRole('region', { name: 'Interview preparation' })
        .getByRole('textbox', { name: 'Invitation and context' })
        .inputValue(),
      'Complete interview invitation for the same role.',
    );
    await page
      .getByRole('region', { name: 'Interview preparation' })
      .getByRole('button', { name: 'Open campaign work' })
      .click();
    await page
      .getByRole('region', { name: 'Agency work' })
      .getByRole('button', { name: 'Resume this round' })
      .waitFor();
    await context.close();
  } finally {
    await conflict.close();
  }
  const rejected = await startFixture();
  try {
    const { context, page, panel } = await openInterview(browser, rejected);
    await panel
      .getByRole('textbox', { name: 'Invitation and context' })
      .fill('Complete invitation before the rejected request.');
    rejected.state.rejectNextInterviewPrepare = true;
    await panel.getByRole('button', { name: 'Prepare interview brief' }).click();
    await panel.getByRole('button', { name: 'Review rejection and revise context' }).waitFor();
    const firstKey = rejected.state.requests
      .filter((request) => request.path === '/api/v1/interviews/prepare')
      .at(-1).payload.requestKey;
    await page.reload({ waitUntil: 'networkidle' });
    const same = page.getByRole('region', { name: 'Interview preparation' });
    await same.getByRole('button', { name: 'Review rejection and revise context' }).click();
    await same
      .getByRole('textbox', { name: 'Invitation and context' })
      .fill('Revised complete invitation after reviewing the rejection.');
    await same.getByRole('button', { name: 'Prepare interview brief' }).click();
    const next = rejected.state.requests
      .filter((request) => request.path === '/api/v1/interviews/prepare')
      .at(-1).payload;
    assert.notEqual(next.requestKey, firstKey);
    assert.match(next.context, /^Revised complete invitation/);
    await context.close();
  } finally {
    await rejected.close();
  }
  console.log(
    'Interview UI smoke passed: sourced selected/unresolved/missing focus, owner-reported debrief, held-response Stop, same-key recovery, persisted list/detail reload, conflict and rejection handling, stale context, 390px.',
  );
}
