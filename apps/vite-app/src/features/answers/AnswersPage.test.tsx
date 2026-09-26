// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type {
  AnswerMatchView,
  CheckStatusView,
  CheckView,
  OpportunityView,
  QuestionAnswerList,
  QuestionAnswerValue,
  RoleWorkflowState,
  SavedAnswer,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { clearAnswerDraftsForJob } from "@/components/shared"
import { AnswersPage } from "@/features/answers/AnswersPage"
import { buildAnswerValueSave } from "@/features/answers/useAnswerSave"
import { roleWorkflowFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  // Drafts survive unmounts by design; clear the jobs this file uses so
  // one test's dirty text never leaks into the next.
  clearAnswerDraftsForJob("job-1")
  clearAnswerDraftsForJob("job-2")
})

const SUGGESTED_TEXT = "I work remotely from Example City."

function opportunityFixture(id: string): OpportunityView {
  return {
    opportunity: {
      id,
      companyId: "company-1",
      title: "Backend Engineer",
      kind: "employment",
      sourceUrl: `https://example.com/jobs/${id}`,
      originalText: `Original posting text for ${id}.`,
      notes: "",
      stage: "new",
      workPattern: "remote",
      locationText: "Berlin",
      postedOn: "2026-09-01",
      deadlineOn: "",
      revision: 2,
      createdAt: "2026-09-10T09:00:00Z",
      updatedAt: "2026-09-12T09:00:00Z",
      compensation: {},
    },
    likelyDuplicates: [],
  }
}

function checkFixture(
  opportunityId: string,
  status: CheckView["status"],
  overall: CheckStatusView["status"] = "checked"
): CheckStatusView {
  const check: CheckView = {
    id: `check-${opportunityId}`,
    opportunityId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status,
    vacancy: {
      captureIds: ["cap-1"],
      evidenceSourceIds: ["src-1"],
      completeness: "complete",
      sourceUrl: `https://example.com/jobs/${opportunityId}`,
      retrievedAt: "2026-09-20T10:00:00Z",
    },
    requestedDocuments: [],
    route: {
      judgment: "application_route",
      sourceExcerpt: "Apply through the portal.",
      observedAt: "2026-09-20T10:00:00Z",
    },
    gaps: [],
    questions: [
      {
        id: "q-remote",
        checkId: `check-${opportunityId}`,
        ordinal: 0,
        text: "Can you work remotely?",
        required: "required",
        kind: "free_text",
        sourceSpan: { captureId: "cap-1", start: 30, end: 58 },
        sourceExcerpt: "Can you work remotely?",
        textSha256: "sha-remote",
      },
      {
        id: "q-start",
        checkId: `check-${opportunityId}`,
        ordinal: 1,
        text: "When can you start?",
        required: "optional",
        kind: "free_text",
        sourceSpan: { captureId: "cap-1", start: 60, end: 80 },
        sourceExcerpt: "When can you start?",
        textSha256: "sha-start",
      },
    ],
    questionSetSha256: "set-sha",
    questionSetVersion: 1,
    createdAt: "2026-09-20T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "codex" },
  }
  if (overall === "blocked") {
    check.blockedReason = {
      code: "source_unavailable",
      detail: "Portal went down.",
    }
  }
  return { status: overall, check }
}

function matchFixture(
  checkId: string,
  status: AnswerMatchView["status"] = "matched"
): AnswerMatchView {
  return {
    status,
    checkId,
    questionSetSha256: "set-sha",
    answerCatalog: {
      digest: "catalog-digest",
      matchedAt: "2026-09-21T10:00:00Z",
    },
    matches: [
      {
        questionId: "q-remote",
        questionTextSha256: "sha-remote",
        choice: {
          answerId: "answer-remote",
          answerVersion: 1,
          textSha256: "text-sha",
        },
        candidateSetHash: "candidates",
        jevAttemptId: "jev-1",
        confidence: 0.8,
        model: "jev-test-1",
        matchedAt: "2026-09-21T10:00:00Z",
      },
      {
        questionId: "q-start",
        questionTextSha256: "sha-start",
        choice: { noneFits: true },
        candidateSetHash: "candidates",
        jevAttemptId: "jev-1",
        confidence: 0.7,
        matchedAt: "2026-09-21T10:00:00Z",
      },
    ],
  }
}

function savedAnswerFixture(id: string, text: string): SavedAnswer {
  return {
    id,
    currentVersion: 1,
    scopeTags: ["remote"],
    versions: [
      {
        version: 1,
        text,
        textSha256: "text-sha",
        approvedAt: "2026-09-19T10:00:00Z",
        approvedBy: { actorKind: "administrator", actorId: "owner" },
        approvalRequestKey: "approve-1",
      },
    ],
  }
}

function valueFixture(
  questionId: string,
  overrides: Partial<QuestionAnswerValue> = {}
): QuestionAnswerValue {
  return {
    questionId,
    questionTextSha256: `sha-${questionId}`,
    required: "required",
    version: 1,
    state: "answered",
    text: SUGGESTED_TEXT,
    textSha256: "text-sha",
    provenance: {
      origin: "jev_suggestion",
      matchId: "run-1",
      matchChoice: {
        answerId: "answer-remote",
        answerVersion: 1,
        textSha256: "text-sha",
      },
      editedAt: "2026-09-22T10:00:00Z",
      editedBy: { actorKind: "administrator", actorId: "owner" },
    },
    updatedAt: "2026-09-22T10:00:00Z",
    ...overrides,
  }
}

interface FetchCall {
  url: string
  method: string
  body: string | null
}

interface AnswersStubOptions {
  opportunities?: Record<string, OpportunityView | null>
  workflows?: Record<string, RoleWorkflowState | null>
  checks?: Record<string, CheckStatusView | null>
  matches?: Record<string, AnswerMatchView | null>
  values?: Record<string, QuestionAnswerList | null>
  answers?: Record<string, SavedAnswer | null>
  putAnswer?: (
    jobId: string,
    questionId: string,
    body: unknown
  ) => Response | Promise<Response>
  postCommit?: (jobId: string) => Response
  matchConflict?: boolean
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function notFound(message: string): Response {
  return jsonResponse(404, { error: { message } })
}

function stubAnswersFetch(options: AnswersStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  const liveMatches = new Map<string, AnswerMatchView>()
  for (const [id, entry] of Object.entries(options.matches ?? {})) {
    if (entry !== null) liveMatches.set(id, entry)
  }
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      const body = typeof init?.body === "string" ? init.body : null
      calls.push({ url, method, body })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname

      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)

      const answerMatch = path.match(/^\/api\/v1\/answers\/([^/]+)$/)
      if (answerMatch?.[1] !== undefined) {
        const entry =
          options.answers?.[decodeURIComponent(answerMatch[1])] ?? null
        return entry === null
          ? notFound("Answer not found.")
          : jsonResponse(200, entry)
      }

      const match = path.match(/^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/)
      if (match?.[1] !== undefined) {
        const id = decodeURIComponent(match[1])
        const suffix = match[2] ?? ""
        const view = options.opportunities?.[id] ?? null
        if (suffix === "")
          return view === null
            ? notFound("Opportunity not found.")
            : jsonResponse(200, view)
        if (view === null) return notFound("Opportunity not found.")
        if (suffix === "/workflow") {
          const entry = options.workflows?.[id] ?? null
          return entry === null
            ? notFound("Role is not selected.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks/current") {
          const entry = options.checks?.[id] ?? null
          return entry === null
            ? notFound("Check not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/answers/match/current") {
          const entry = liveMatches.get(id) ?? null
          return entry === null
            ? notFound("Match not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/answers/match" && method === "POST") {
          if (options.matchConflict === true)
            return jsonResponse(409, {
              error: { message: "Question set moved; recheck first." },
            })
          const entry = matchFixture(`check-${id}`)
          liveMatches.set(id, entry)
          return jsonResponse(200, entry)
        }
        if (suffix === "/answers/current") {
          const entry = options.values?.[id] ?? null
          return entry === null
            ? notFound("Answers not found.")
            : jsonResponse(200, entry)
        }
        const putMatch = suffix.match(/^\/questions\/([^/]+)\/answer$/)
        if (putMatch?.[1] !== undefined && method === "PUT") {
          if (options.putAnswer !== undefined)
            return await options.putAnswer(
              id,
              decodeURIComponent(putMatch[1]),
              body === null ? null : JSON.parse(body)
            )
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
        if (suffix === "/answers/commit" && method === "POST") {
          if (options.postCommit !== undefined)
            return options.postCommit(id)
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderAnswersPage(jobId: string) {
  return render(
    <SessionProvider>
      <AnswersPage jobId={jobId} />
    </SessionProvider>
  )
}

function answeredOptions(
  overrides: Partial<AnswersStubOptions> = {}
): AnswersStubOptions {
  return {
    opportunities: { "job-1": opportunityFixture("job-1") },
    workflows: {
      "job-1": roleWorkflowFixture("job-1", "checked", { revision: 1 }),
    },
    checks: { "job-1": checkFixture("job-1", "checked") },
    matches: { "job-1": matchFixture("check-job-1") },
    values: {
      "job-1": {
        checkId: "check-job-1",
        questionSetSha256: "set-sha",
        values: [],
      },
    },
    answers: {
      "answer-remote": savedAnswerFixture("answer-remote", SUGGESTED_TEXT),
    },
    ...overrides,
  }
}

function unexpectedPosts(calls: FetchCall[]): FetchCall[] {
  return calls.filter((call) => call.method !== "GET" && call.method !== "PUT")
}

describe("answer value payloads", () => {
  it("builds the exact save payload without touching the text", () => {
    expect(buildAnswerValueSave(0, "  spaced ✅ \n")).toEqual({
      expectedAnswerVersion: 0,
      text: "  spaced ✅ \n",
    })
  })

  it("carries a draft request on explicit blanks only", () => {
    expect(buildAnswerValueSave(0, "", true)).toEqual({
      expectedAnswerVersion: 0,
      text: "",
      draftRequested: true,
    })
    expect(buildAnswerValueSave(1, "", false)).toEqual({
      expectedAnswerVersion: 1,
      text: "",
    })
    expect(buildAnswerValueSave(1, "text", true)).toEqual({
      expectedAnswerVersion: 1,
      text: "text",
    })
  })
})

describe("answers page reads", () => {
  it("mounts GET-only: no PUT, POST, or match commission", async () => {
    const { calls } = stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
    expect(
      calls.some(
        (call) => call.url.includes("/answers/match") && call.method !== "GET"
      )
    ).toBe(false)
  })

  it("prefills the suggested match and leaves unanswered questions blank", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    expect(boxes).toHaveLength(2)
    expect(boxes[0]?.value).toBe(SUGGESTED_TEXT)
    expect(boxes[1]?.value).toBe("")
    expect(
      await screen.findByText(
        "No saved answer fit this question; the box starts blank."
      )
    ).toBeDefined()
  })

  it("shows stored values over suggestions with provenance", async () => {
    stubAnswersFetch(
      answeredOptions({
        values: {
          "job-1": {
            checkId: "check-job-1",
            questionSetSha256: "set-sha",
            values: [
              valueFixture("q-remote", {
                text: "Owner edited text.",
                provenance: {
                  origin: "owner_edited",
                  matchId: "run-1",
                  editedAt: "2026-09-22T10:00:00Z",
                  editedBy: { actorKind: "administrator", actorId: "owner" },
                },
              }),
            ],
          },
        },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue("Owner edited text.")).toBeDefined()
    expect(
      await screen.findByText(/Suggested match, edited by owner/)
    ).toBeDefined()
  })

  it("names the exact saved answer behind suggestions and stored values", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(
      await screen.findByText(
        "Suggested from saved answer answer-remote v1; the box is unsaved until you save or continue."
      )
    ).toBeDefined()
  })

  it("shows the exact match behind a kept suggestion with its version", async () => {
    stubAnswersFetch(
      answeredOptions({
        values: {
          "job-1": {
            checkId: "check-job-1",
            questionSetSha256: "set-sha",
            values: [valueFixture("q-remote")],
          },
        },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(
      await screen.findByText(/Suggested match, saved unchanged/)
    ).toBeDefined()
    expect(
      await screen.findByText(/based on saved answer answer-remote v1/)
    ).toBeDefined()
  })

  it("shows an explicit blank as saved, distinct from unset", async () => {
    const { calls } = stubAnswersFetch(
      answeredOptions({
        values: {
          "job-1": {
            checkId: "check-job-1",
            questionSetSha256: "set-sha",
            values: [
              valueFixture("q-start", {
                required: "optional",
                version: 2,
                state: "blank",
                text: "",
                textSha256: "",
                provenance: {
                  origin: "carried_blank",
                  editedAt: "2026-09-22T10:00:00Z",
                  editedBy: { actorKind: "administrator", actorId: "owner" },
                },
              }),
            ],
          },
        },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(await screen.findByText("Blank saved · v2.")).toBeDefined()
    expect(await screen.findByText(/Explicitly left blank/)).toBeDefined()
    for (const call of calls) expect(call.method).toBe("GET")
  })
})

describe("answers page saves", () => {
  it("saves exact bytes with the observed version, then guards on the new row", async () => {
    const seen: unknown[] = []
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          seen.push(body)
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: payload.expectedAnswerVersion + 1,
              state: "answered",
              text: payload.text,
              provenance: {
                origin: "owner_written",
                editedAt: "2026-09-22T11:00:00Z",
                editedBy: { actorKind: "administrator", actorId: "owner" },
              },
            })
          )
        },
      })
    )
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    const exact = "  Hybrid: 2–3 days ✅\nSecond line.  "
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: exact },
    })
    const saveButtons = await screen.findAllByRole("button", {
      name: "Save answer",
    })
    fireEvent.click(saveButtons[1] as HTMLElement)

    await waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0]).toEqual({ expectedAnswerVersion: 0, text: exact })
    expect(await screen.findByText("Saved · v1.")).toBeDefined()

    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: `${exact}!` },
    })
    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[1] as HTMLElement
    )
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[1]).toEqual({ expectedAnswerVersion: 1, text: `${exact}!` })
  })

  it("clears a suggestion to an explicit blank", async () => {
    const seen: unknown[] = []
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          seen.push(body)
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: 1,
              state: "blank",
              text: "",
              provenance: {
                origin: "carried_blank",
                matchId: "run-1",
                editedAt: "2026-09-22T11:00:00Z",
                editedBy: { actorKind: "administrator", actorId: "owner" },
              },
            })
          )
        },
      })
    )
    renderAnswersPage("job-1")

    const box = (await screen.findByDisplayValue(
      SUGGESTED_TEXT
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "" } })
    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[0] as HTMLElement
    )

    await waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0]).toEqual({ expectedAnswerVersion: 0, text: "" })
    expect(await screen.findByText("Blank saved · v1.")).toBeDefined()
  })

  it("saves a required blank with an explicit draft request", async () => {
    const seen: unknown[] = []
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          seen.push(body)
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: 1,
              state: "blank",
              text: "",
              textSha256: "",
              draftRequested: true,
              provenance: {
                origin: "carried_blank",
                matchId: "run-1",
                editedAt: "2026-09-22T11:00:00Z",
                editedBy: { actorKind: "administrator", actorId: "owner" },
              },
            })
          )
        },
      })
    )
    renderAnswersPage("job-1")

    const box = (await screen.findByDisplayValue(
      SUGGESTED_TEXT
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "" } })
    fireEvent.click(
      await screen.findByRole("checkbox", {
        name: "Leave blank and request drafting during Prepare from verified facts",
      })
    )
    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[0] as HTMLElement
    )

    await waitFor(() => expect(seen).toHaveLength(1))
    expect(seen[0]).toEqual({
      expectedAnswerVersion: 0,
      text: "",
      draftRequested: true,
    })
    expect(
      await screen.findByText("Blank saved · v1 · draft requested.")
    ).toBeDefined()
    expect(
      await screen.findByText(/draft requested for Prepare/)
    ).toBeDefined()
  })

  it("offers no draft request on optional blanks or filled boxes", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    expect(boxes).toHaveLength(2)
    // q-remote is required but prefilled; q-start is optional and blank.
    expect(screen.queryByRole("checkbox")).toBeNull()
  })

  it("reports a version conflict without losing the box text", async () => {
    const { calls } = stubAnswersFetch(
      answeredOptions({
        putAnswer: () => jsonResponse(409, { error: { message: "Conflict." } }),
      })
    )
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Conflicted text." },
    })
    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[1] as HTMLElement
    )

    expect(
      await screen.findByText(/This answer changed elsewhere\./)
    ).toBeDefined()
    expect((boxes[1] as HTMLTextAreaElement).value).toBe("Conflicted text.")
    expect(unexpectedPosts(calls)).toHaveLength(0)
  })
})

describe("answers page honest states", () => {
  it("keeps boxes blank when matches are outdated and fetches no answers", async () => {
    const { calls } = stubAnswersFetch(
      answeredOptions({
        matches: { "job-1": matchFixture("check-job-1", "outdated") },
      })
    )
    renderAnswersPage("job-1")

    expect(
      await screen.findByText(/The saved matches are outdated/)
    ).toBeDefined()
    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    expect(boxes).toHaveLength(2)
    for (const box of boxes) expect(box.value).toBe("")
    expect(calls.some((call) => call.url.includes("/api/v1/answers/"))).toBe(
      false
    )
    expect(unexpectedPosts(calls)).toHaveLength(0)
  })

  it("stays honest with no match run at all", async () => {
    stubAnswersFetch(answeredOptions({ matches: {} }))
    renderAnswersPage("job-1")

    expect(
      await screen.findByText(/No suggested answers yet for this check\./)
    ).toBeDefined()
    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    expect(boxes).toHaveLength(2)
    for (const box of boxes) expect(box.value).toBe("")
  })

  it("shows blocked checks without any editable box", async () => {
    stubAnswersFetch(
      answeredOptions({
        checks: { "job-1": checkFixture("job-1", "blocked", "blocked") },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText("Check blocked")).toBeDefined()
    expect(await screen.findByText(/source_unavailable/)).toBeDefined()
    expect(screen.queryAllByLabelText("Your answer")).toHaveLength(0)
  })

  it("shows unselected roles without check state", async () => {
    stubAnswersFetch(answeredOptions({ workflows: { "job-1": null } }))
    renderAnswersPage("job-1")

    expect(await screen.findByText("Role not selected")).toBeDefined()
    expect(screen.queryAllByLabelText("Your answer")).toHaveLength(0)
  })

  it("shows checks still running without boxes", async () => {
    stubAnswersFetch(
      answeredOptions({
        checks: { "job-1": checkFixture("job-1", "checking", "checking") },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText("Check in progress")).toBeDefined()
    expect(screen.queryAllByLabelText("Your answer")).toHaveLength(0)
  })

  it("links every question to its source excerpt and span", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(
      await screen.findByRole("heading", { name: "1. Can you work remotely?" })
    ).toBeDefined()
    expect(
      await screen.findByText(
        (_content, element) =>
          element?.textContent ===
          "Source: Can you work remotely? · cap-1 · chars 30–58"
      )
    ).toBeDefined()
    expect(
      await screen.findByText(
        (_content, element) =>
          element?.textContent ===
          "Source: When can you start? · cap-1 · chars 60–80"
      )
    ).toBeDefined()
  })

  it("ignores stored values pinned to a different check", async () => {
    stubAnswersFetch(
      answeredOptions({
        values: {
          "job-1": {
            checkId: "check-stale",
            questionSetSha256: "old-sha",
            values: [
              valueFixture("q-remote", {
                text: "Stale text from a previous check.",
              }),
            ],
          },
        },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(screen.queryByDisplayValue("Stale text from a previous check.")).toBeNull()
  })

  it("holds answering on an outdated check without boxes or writes", async () => {
    const { calls } = stubAnswersFetch(
      answeredOptions({
        checks: { "job-1": checkFixture("job-1", "checked", "outdated") },
      })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText("Check outdated")).toBeDefined()
    expect(screen.queryAllByLabelText("Your answer")).toHaveLength(0)
    for (const call of calls) expect(call.method).toBe("GET")
    expect(unexpectedPosts(calls)).toHaveLength(0)
  })
})

describe("answers page model-call boundary", () => {
  it("mounts, saves, and conflicts with zero POSTs and zero model endpoints", async () => {
    let attempts = 0
    const { calls } = stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          attempts += 1
          if (attempts === 1)
            return jsonResponse(409, { error: { message: "Conflict." } })
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: payload.expectedAnswerVersion + 1,
              state: "answered",
              text: payload.text,
              provenance: {
                origin: "owner_written",
                editedAt: "2026-09-22T11:00:00Z",
                editedBy: { actorKind: "administrator", actorId: "owner" },
              },
            })
          )
        },
      })
    )
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Boundary probe." },
    })
    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[1] as HTMLElement
    )
    expect(
      await screen.findByText(/This answer changed elsewhere\./)
    ).toBeDefined()

    fireEvent.click(
      (
        await screen.findAllByRole("button", { name: "Save answer" })
      )[1] as HTMLElement
    )
    expect(await screen.findByText("Saved · v1.")).toBeDefined()

    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(["GET", "PUT"]).toContain(call.method)
      expect(call.url.toLowerCase()).not.toMatch(/codex|llm|openai|anthropic/)
      if (call.method === "PUT")
        expect(call.url).toMatch(/\/questions\/[^/]+\/answer$/)
    }
    expect(unexpectedPosts(calls)).toHaveLength(0)
  })
})

describe("answers journey flow", () => {
  it("explains the answer stage above the boxes", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(
      screen.getByRole("heading", { name: "Answer the employer's questions" })
    ).toBeDefined()
  })

  it("records the Jev match outcome per question", async () => {
    stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(screen.getByText("1 suggestion ready")).toBeDefined()
    fireEvent.click(
      screen.getByRole("button", { name: /Answer questions/ })
    )
    expect(
      await screen.findByText(/a saved answer matched and was placed/)
    ).toBeDefined()
    expect(screen.getByText(/no saved answer fit/)).toBeDefined()
  })

  it("runs Jev matching on explicit click and prefills the returned suggestion", async () => {
    const { calls } = stubAnswersFetch(
      answeredOptions({ matches: { "job-1": null } })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText(/No suggested answers yet/)).toBeDefined()
    expect(
      calls.filter(
        (call) =>
          call.method === "POST" &&
          call.url === "/api/v1/opportunities/job-1/answers/match"
      )
    ).toHaveLength(0)

    fireEvent.click(
      screen.getByRole("button", { name: "Match saved answers" })
    )
    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/answers/match"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["expectedCheckId"]).toBe("check-job-1")
    expect(payload["expectedQuestionSetSha256"]).toBe("set-sha")
    expect(typeof payload["requestKey"]).toBe("string")
    expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
    expect(
      calls.filter(
        (call) =>
          call.method === "POST" &&
          call.url !== "/api/v1/opportunities/job-1/answers/match"
      )
    ).toEqual([])
    expect(
      screen.getByRole("button", { name: "Re-run answer matching" })
    ).toBeDefined()
  })

  it("reports a match conflict without touching the boxes", async () => {
    stubAnswersFetch(
      answeredOptions({ matches: { "job-1": null }, matchConflict: true })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText(/No suggested answers yet/)).toBeDefined()
    fireEvent.click(
      screen.getByRole("button", { name: "Match saved answers" })
    )
    await screen.findByText("Question set moved; recheck first.")
    expect(screen.queryByDisplayValue(SUGGESTED_TEXT)).toBeNull()
  })

  it("persists untouched suggestions and blanks, commits, then continues", async () => {
    const puts: Array<{ questionId: string; body: unknown }> = []
    const commits: string[] = []
    const { calls } = stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          puts.push({ questionId, body })
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          const kept = payload.text === SUGGESTED_TEXT
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: payload.expectedAnswerVersion + 1,
              state: payload.text === "" ? "blank" : "answered",
              text: payload.text,
              provenance: kept
                ? {
                    origin: "jev_suggestion",
                    matchId: "run-1",
                    matchChoice: {
                      answerId: "answer-remote",
                      answerVersion: 1,
                      textSha256: "text-sha",
                    },
                    editedAt: "2026-09-22T11:00:00Z",
                    editedBy: { actorKind: "administrator", actorId: "owner" },
                  }
                : {
                    origin:
                      payload.text === "" ? "carried_blank" : "owner_written",
                    editedAt: "2026-09-22T11:00:00Z",
                    editedBy: { actorKind: "administrator", actorId: "owner" },
                  },
            })
          )
        },
        postCommit: (jobId) => {
          commits.push(jobId)
          return jsonResponse(
            200,
            roleWorkflowFixture("job-1", "answered", { revision: 3 })
          )
        },
      })
    )
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    // Untouched: q-remote holds the suggestion, q-start is blank.
    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    await waitFor(() =>
      expect(window.location.hash).toBe("#/jobs/job-1/prepare")
    )
    // The untouched suggestion persists byte-identically (keep) and the
    // empty optional box persists an explicit blank — nothing is dropped.
    expect(puts).toHaveLength(2)
    expect(puts[0]).toEqual({
      questionId: "q-remote",
      body: { expectedAnswerVersion: 0, text: SUGGESTED_TEXT },
    })
    expect(puts[1]).toEqual({
      questionId: "q-start",
      body: { expectedAnswerVersion: 0, text: "" },
    })
    expect(commits).toEqual(["job-1"])
    const commitPosts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/answers/commit"
    )
    expect(commitPosts).toHaveLength(1)
    // No library promotion and no Prepare start from this page.
    expect(
      calls.some(
        (call) => call.method === "POST" && call.url === "/api/v1/answers"
      )
    ).toBe(false)
    expect(
      calls.some(
        (call) =>
          call.url.includes("/materials/") || call.url.includes("/artifacts/")
      )
    ).toBe(false)
  })

  it("saves every dirty box, then commits, before continuing", async () => {
    const seen: Array<{ questionId: string; body: unknown }> = []
    const commits: string[] = []
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          seen.push({ questionId, body })
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: payload.expectedAnswerVersion + 1,
              state: payload.text === "" ? "blank" : "answered",
              text: payload.text,
              provenance: {
                origin: "owner_written",
                editedAt: "2026-09-22T11:00:00Z",
                editedBy: { actorKind: "administrator", actorId: "owner" },
              },
            })
          )
        },
        postCommit: (jobId) => {
          commits.push(jobId)
          return jsonResponse(
            200,
            roleWorkflowFixture("job-1", "answered", { revision: 3 })
          )
        },
      })
    )
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[0] as HTMLTextAreaElement, {
      target: { value: "Remote, async-first." },
    })
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Two weeks." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    await waitFor(() =>
      expect(window.location.hash).toBe("#/jobs/job-1/prepare")
    )
    expect(seen).toHaveLength(2)
    expect(seen[0]).toEqual({
      questionId: "q-remote",
      body: { expectedAnswerVersion: 0, text: "Remote, async-first." },
    })
    expect(seen[1]).toEqual({
      questionId: "q-start",
      body: { expectedAnswerVersion: 0, text: "Two weeks." },
    })
    expect(commits).toEqual(["job-1"])
  })

  it("holds the continuation on a partial save failure and keeps the text", async () => {
    const commits: string[] = []
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          if (questionId === "q-start")
            return jsonResponse(409, {
              error: { message: "Answer version conflict." },
            })
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          return jsonResponse(
            200,
            valueFixture(questionId, {
              version: payload.expectedAnswerVersion + 1,
              state: "answered",
              text: payload.text,
            })
          )
        },
        postCommit: (jobId) => {
          commits.push(jobId)
          return jsonResponse(
            200,
            roleWorkflowFixture("job-1", "answered", { revision: 3 })
          )
        },
      })
    )
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[0] as HTMLTextAreaElement, {
      target: { value: "Remote, async-first." },
    })
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Two weeks." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    await screen.findByText(/Could not save Question 2\./)
    expect(window.location.hash).toBe("#/jobs/job-1/answers")
    expect(
      (screen.getAllByLabelText("Your answer")[1] as HTMLTextAreaElement).value
    ).toBe("Two weeks.")
    // A partial save never reaches the commit.
    expect(commits).toHaveLength(0)
  })
})

describe("answers draft preservation", () => {
  it("preserves dirty text across rematching", async () => {
    stubAnswersFetch(answeredOptions({ matches: { "job-1": null } }))
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    expect(boxes).toHaveLength(2)
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Typed before rematch." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Match saved answers" })
    )

    // The rematch prefills q-remote; the dirty q-start text survives the
    // refresh that unmounts and remounts the boxes.
    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(
      (screen.getAllByLabelText("Your answer")[1] as HTMLTextAreaElement).value
    ).toBe("Typed before rematch.")
  })

  it("preserves dirty text across navigation, scoped to the exact job", async () => {
    const options: AnswersStubOptions = {
      opportunities: {
        "job-1": opportunityFixture("job-1"),
        "job-2": opportunityFixture("job-2"),
      },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checked", { revision: 1 }),
        "job-2": roleWorkflowFixture("job-2", "checked", { revision: 1 }),
      },
      checks: {
        "job-1": checkFixture("job-1", "checked"),
        "job-2": checkFixture("job-2", "checked"),
      },
      matches: {
        "job-1": matchFixture("check-job-1"),
        "job-2": matchFixture("check-job-2"),
      },
      values: {
        "job-1": {
          checkId: "check-job-1",
          questionSetSha256: "set-sha",
          values: [],
        },
        "job-2": {
          checkId: "check-job-2",
          questionSetSha256: "set-sha",
          values: [],
        },
      },
      answers: {
        "answer-remote": savedAnswerFixture("answer-remote", SUGGESTED_TEXT),
      },
    }
    stubAnswersFetch(options)
    const first = renderAnswersPage("job-1")
    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Keep me." },
    })
    first.unmount()
    cleanup()

    stubAnswersFetch(options)
    const second = renderAnswersPage("job-2")
    const other = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    // Another job never receives job-1's draft.
    expect(other[1]?.value).toBe("")
    second.unmount()
    cleanup()

    stubAnswersFetch(options)
    renderAnswersPage("job-1")
    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(
      (screen.getAllByLabelText("Your answer")[1] as HTMLTextAreaElement).value
    ).toBe("Keep me.")
  })

  it("never applies one check's draft to a new check", async () => {
    stubAnswersFetch(answeredOptions())
    const first = renderAnswersPage("job-1")
    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[0] as HTMLTextAreaElement, {
      target: { value: "Old-check edit." },
    })
    first.unmount()
    cleanup()

    const next = checkFixture("job-1", "checked")
    next.check!.id = "check-job-1b"
    for (const question of next.check!.questions)
      question.checkId = "check-job-1b"
    stubAnswersFetch(
      answeredOptions({
        checks: { "job-1": next },
        matches: { "job-1": matchFixture("check-job-1b") },
        values: {
          "job-1": {
            checkId: "check-job-1b",
            questionSetSha256: "set-sha",
            values: [],
          },
        },
      })
    )
    renderAnswersPage("job-1")
    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    expect(screen.queryByDisplayValue("Old-check edit.")).toBeNull()
  })

  it("freezes editors until the save→commit sequence completes", async () => {
    let release!: (response: Response) => void
    const gate = new Promise<Response>((resolve) => {
      release = resolve
    })
    stubAnswersFetch(
      answeredOptions({
        putAnswer: (_jobId, questionId, body) => {
          const payload = body as {
            expectedAnswerVersion: number
            text: string
          }
          return gate.then(() =>
            jsonResponse(
              200,
              valueFixture(questionId, {
                version: payload.expectedAnswerVersion + 1,
                state: payload.text === "" ? "blank" : "answered",
                text: payload.text,
                provenance: {
                  origin: "owner_written",
                  editedAt: "2026-09-22T11:00:00Z",
                  editedBy: { actorKind: "administrator", actorId: "owner" },
                },
              })
            )
          )
        },
        postCommit: (jobId) =>
          jsonResponse(
            200,
            roleWorkflowFixture(jobId, "answered", { revision: 3 })
          ),
      })
    )
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Frozen input." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    // While the saves are delayed, every editor and match control blocks.
    await waitFor(() =>
      expect(
        (screen.getAllByLabelText("Your answer")[0] as HTMLTextAreaElement)
          .disabled
      ).toBe(true)
    )
    for (const box of screen.getAllByLabelText("Your answer"))
      expect((box as HTMLTextAreaElement).disabled).toBe(true)
    for (const button of screen.getAllByRole("button", { name: "Save answer" }))
      expect((button as HTMLButtonElement).disabled).toBe(true)
    expect(
      (
        screen.getByRole("button", {
          name: "Re-run answer matching",
        }) as HTMLButtonElement
      ).disabled
    ).toBe(true)

    release(jsonResponse(200, {}))
    await waitFor(() =>
      expect(window.location.hash).toBe("#/jobs/job-1/prepare")
    )
  })
})

describe("answers commit holds", () => {
  function savingOptions(
    postCommit: (jobId: string) => Response
  ): AnswersStubOptions {
    return answeredOptions({
      putAnswer: (_jobId, questionId, body) => {
        const payload = body as {
          expectedAnswerVersion: number
          text: string
          draftRequested?: boolean
        }
        return jsonResponse(
          200,
          valueFixture(questionId, {
            version: payload.expectedAnswerVersion + 1,
            state: payload.text === "" ? "blank" : "answered",
            text: payload.text,
            draftRequested: payload.draftRequested ?? false,
            provenance: {
              origin:
                payload.text === "" ? "carried_blank" : "owner_written",
              editedAt: "2026-09-22T11:00:00Z",
              editedBy: { actorKind: "administrator", actorId: "owner" },
            },
          })
        )
      },
      postCommit,
    })
  }

  it("holds on a required blank, then continues once drafting is requested", async () => {
    const puts: Array<{ questionId: string; body: unknown }> = []
    let commits = 0
    const options = savingOptions(() => {
      commits += 1
      return commits === 1
        ? jsonResponse(409, {
            error: {
              message:
                "Required answers are missing; answer them before committing.",
            },
          })
        : jsonResponse(
            200,
            roleWorkflowFixture("job-1", "answered", { revision: 3 })
          )
    })
    const innerPut = options.putAnswer!
    options.putAnswer = (_jobId, questionId, body) => {
      puts.push({ questionId, body })
      return innerPut(_jobId, questionId, body)
    }
    stubAnswersFetch(options)
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    const box = (await screen.findByDisplayValue(
      SUGGESTED_TEXT
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "" } })
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    expect(
      await screen.findByText(
        (_content, element) =>
          element?.textContent ===
          "The server held the commit: Required answers are missing; answer them before committing. Question 1 still needs an answer, or a draft request for Prepare."
      )
    ).toBeDefined()
    expect(window.location.hash).toBe("#/jobs/job-1/answers")
    expect(
      (screen.getAllByLabelText("Your answer")[0] as HTMLTextAreaElement).value
    ).toBe("")

    // Request drafting for the blank required question and continue again:
    // the retry guards on the accepted version, not the stale one.
    fireEvent.click(
      await screen.findByRole("checkbox", {
        name: "Leave blank and request drafting during Prepare from verified facts",
      })
    )
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )
    await waitFor(() =>
      expect(window.location.hash).toBe("#/jobs/job-1/prepare")
    )
    expect(commits).toBe(2)
    const remotePuts = puts.filter((put) => put.questionId === "q-remote")
    expect(remotePuts).toHaveLength(2)
    expect(remotePuts[0]?.body).toEqual({
      expectedAnswerVersion: 0,
      text: "",
    })
    expect(remotePuts[1]?.body).toEqual({
      expectedAnswerVersion: 1,
      text: "",
      draftRequested: true,
    })
  })

  it("reports a moved basis honestly when nothing is locally missing", async () => {
    stubAnswersFetch(
      savingOptions(() =>
        jsonResponse(409, {
          error: { message: "The answer changed. Refresh and reconcile." },
        })
      )
    )
    window.location.hash = "#/jobs/job-1/answers"
    renderAnswersPage("job-1")

    const boxes = (await screen.findAllByLabelText(
      "Your answer"
    )) as HTMLTextAreaElement[]
    fireEvent.change(boxes[1] as HTMLTextAreaElement, {
      target: { value: "Two weeks." },
    })
    fireEvent.click(
      screen.getByRole("button", { name: "Save, commit and continue" })
    )

    expect(
      await screen.findByText(
        (_content, element) =>
          element?.textContent ===
          "The server held the commit: The answer changed. Refresh and reconcile. The check or answers moved; reload the page and reconcile before continuing."
      )
    ).toBeDefined()
    expect(window.location.hash).toBe("#/jobs/job-1/answers")
    expect(
      (screen.getAllByLabelText("Your answer")[0] as HTMLTextAreaElement).value
    ).toBe(SUGGESTED_TEXT)
  })

  it("guides zero-question checks back to the check page without boxes", async () => {
    const empty = checkFixture("job-1", "checked")
    empty.check!.questions = []
    const { calls } = stubAnswersFetch(
      answeredOptions({ checks: { "job-1": empty } })
    )
    renderAnswersPage("job-1")

    expect(await screen.findByText("No questions found")).toBeDefined()
    const link = await screen.findByRole("link", {
      name: "Return to the check page",
    })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/check")
    expect(screen.queryAllByLabelText("Your answer")).toHaveLength(0)
    for (const call of calls) expect(call.method).toBe("GET")
  })
})
