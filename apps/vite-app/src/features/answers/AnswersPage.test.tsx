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
import { AnswersPage } from "@/features/answers/AnswersPage"
import { buildAnswerValueSave } from "@/features/answers/useAnswerSave"
import { roleWorkflowFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
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
  putAnswer?: (jobId: string, questionId: string, body: unknown) => Response
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
          const entry = options.matches?.[id] ?? null
          return entry === null
            ? notFound("Match not found.")
            : jsonResponse(200, entry)
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
            return options.putAnswer(
              id,
              decodeURIComponent(putMatch[1]),
              body === null ? null : JSON.parse(body)
            )
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

  it("continues to preparation without commissioning anything", async () => {
    const { calls } = stubAnswersFetch(answeredOptions())
    renderAnswersPage("job-1")

    expect(await screen.findByDisplayValue(SUGGESTED_TEXT)).toBeDefined()
    const continuation = screen.getByRole("link", {
      name: "Prepare materials",
    })
    expect(continuation.getAttribute("href")).toBe("#/jobs/job-1/prepare")
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })
})
