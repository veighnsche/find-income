// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react"
import type {
  CheckActivityPage,
  CheckStatusView,
  CheckView,
  OpportunityView,
  ResearchActivityEvent,
  RoleWorkflowState,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { CheckPage } from "@/features/check/CheckPage"
import {
  buildCheckStartRequest,
  newCheckRequestKey,
} from "@/features/check/useCheckStart"
import { roleWorkflowFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function opportunityFixture(
  id: string,
  revision: number,
  title: string
): OpportunityView {
  return {
    opportunity: {
      id,
      companyId: "company-1",
      title,
      kind: "employment",
      sourceUrl: `https://example.com/jobs/${id}`,
      originalText: `Original posting text for ${id}.`,
      notes: "",
      stage: "new",
      workPattern: "remote",
      locationText: "Berlin",
      postedOn: "2026-09-01",
      deadlineOn: "",
      revision,
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
  overrides: Partial<CheckView> = {}
): CheckView {
  return {
    id: `check-${opportunityId}`,
    opportunityId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status,
    vacancy: {
      captureIds: ["cap-1"],
      evidenceSourceIds: ["src-1", "src-2"],
      completeness: "complete",
      sourceUrl: `https://example.com/jobs/${opportunityId}`,
      retrievedAt: "2026-09-20T10:00:00Z",
    },
    requestedDocuments: [
      {
        label: "CV",
        required: true,
        sourceExcerpt: "Please attach your CV.",
        sourceSpan: { captureId: "cap-1", start: 0, end: 22 },
      },
    ],
    route: {
      judgment: "application_route",
      kind: "direct",
      destinationText: "Apply through the portal.",
      sourceExcerpt: "Apply through the portal.",
      observedAt: "2026-09-20T10:00:00Z",
    },
    gaps: [
      {
        id: "gap-1",
        description: "Salary band unconfirmed.",
        consequential: true,
        kind: "unverified_claim",
      },
    ],
    questions: [
      {
        id: "q-1",
        checkId: `check-${opportunityId}`,
        ordinal: 0,
        text: "Why do you want this role?",
        required: "required",
        kind: "free_text",
        sourceSpan: { captureId: "cap-1", start: 30, end: 58 },
        sourceExcerpt: "Why do you want this role?",
        textSha256: "abc123",
      },
    ],
    questionSetSha256: "set-sha",
    questionSetVersion: 1,
    createdAt: "2026-09-20T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "codex" },
    ...overrides,
  }
}

function activityFixture(
  eventId: string,
  kind: string,
  summary: string
): ResearchActivityEvent {
  return {
    eventId,
    at: "2026-09-20T10:00:00Z",
    kind,
    phase: "collect",
    summary,
  }
}

interface FetchCall {
  url: string
  method: string
  body: string | null
}

interface CheckStubOptions {
  opportunities?: Record<string, OpportunityView | null>
  workflows?: Record<string, RoleWorkflowState | null>
  checks?: Record<string, CheckStatusView | null>
  activityPages?: Record<string, CheckActivityPage[]>
  postCheck?: (jobId: string, body: unknown) => Response
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

function stubCheckFetch(options: CheckStubOptions = {}): {
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
      const body =
        typeof init?.body === "string" ? init.body : null
      calls.push({ url, method, body })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname

      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)

      const match = path.match(
        /^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/
      )
      if (match?.[1] !== undefined) {
        const id = decodeURIComponent(match[1])
        const suffix = match[2] ?? ""
        const view = options.opportunities?.[id] ?? null
        if (view === null && suffix !== "/checks") return notFound("Opportunity not found.")
        if (suffix === "") return jsonResponse(200, view)
        if (suffix === "/workflow") {
          const entry = options.workflows?.[id] ?? null
          return entry === null
            ? notFound("Role is not selected.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks/current/activity") {
          const pages = options.activityPages?.[id] ?? [{ events: [] }]
          const cursor = parsed.searchParams.get("cursor") ?? ""
          const page = cursor === "" ? pages[0] : pages[1]
          return jsonResponse(200, page ?? { events: [] })
        }
        if (suffix === "/checks/current") {
          const entry = options.checks?.[id] ?? null
          return entry === null
            ? notFound("Check not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks" && method === "POST") {
          if (options.postCheck !== undefined)
            return options.postCheck(id, body === null ? null : JSON.parse(body))
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderCheckPage(jobId: string) {
  return render(
    <SessionProvider>
      <CheckPage jobId={jobId} />
    </SessionProvider>
  )
}

const jobOne = opportunityFixture("job-1", 2, "Backend Engineer")
const jobTwo = opportunityFixture("job-2", 5, "Weekend Project")

describe("check request keys and payloads", () => {
  it("generates unique non-empty keys within the schema limit", () => {
    const keys = new Set([
      newCheckRequestKey(),
      newCheckRequestKey(),
      newCheckRequestKey(),
    ])
    expect(keys.size).toBe(3)
    for (const key of keys) {
      expect(key.length).toBeGreaterThan(0)
      expect(key.length).toBeLessThanOrEqual(200)
    }
  })

  it("builds the exact start payload from the key and revisions", () => {
    expect(
      buildCheckStartRequest("key-1", {
        expectedOpportunityRevision: 2,
        expectedWorkflowRevision: 0,
      })
    ).toEqual({
      requestKey: "key-1",
      expectedOpportunityRevision: 2,
      expectedWorkflowRevision: 0,
    })
  })
})

describe("check page states", () => {
  it("starts a check from not_checked with the revisions from reads", async () => {
    const seen: unknown[] = []
    const { calls } = stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "selected", {
          revision: 0,
          opportunityRevision: 2,
        }),
      },
      checks: { "job-1": { status: "not_checked" } },
      postCheck: (_jobId, body) => {
        seen.push(body)
        return jsonResponse(201, { status: "checking" })
      },
    })
    renderCheckPage("job-1")

    expect(await screen.findByText("No check yet")).toBeDefined()
    expect(
      await screen.findByText(
        "Current stage: Select jobs (selected)"
      )
    ).toBeDefined()

    fireEvent.click(
      await screen.findByRole("button", { name: "Start check" })
    )
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "POST" &&
            call.url.endsWith("/api/v1/opportunities/job-1/checks")
        )
      ).toBe(true)
    )
    expect(seen).toHaveLength(1)
    const payload = seen[0] as Record<string, unknown>
    expect(typeof payload["requestKey"]).toBe("string")
    expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
    expect(payload["expectedOpportunityRevision"]).toBe(2)
    expect(payload["expectedWorkflowRevision"]).toBe(0)
    expect(Object.keys(payload).sort()).toEqual([
      "expectedOpportunityRevision",
      "expectedWorkflowRevision",
      "requestKey",
    ])
  })

  it("replays the same requestKey when a failed start is retried", async () => {
    const seen: string[] = []
    let attempts = 0
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "selected", {
          revision: 0,
          opportunityRevision: 2,
        }),
      },
      checks: { "job-1": { status: "not_checked" } },
      postCheck: (_jobId, body) => {
        attempts += 1
        seen.push((body as { requestKey: string }).requestKey)
        return attempts === 1
          ? jsonResponse(500, { error: { message: "Codex unavailable." } })
          : jsonResponse(201, { status: "checking" })
      },
    })
    renderCheckPage("job-1")

    fireEvent.click(
      await screen.findByRole("button", { name: "Start check" })
    )
    expect(await screen.findByText("Codex unavailable.")).toBeDefined()

    fireEvent.click(
      await screen.findByRole("button", { name: "Start check" })
    )
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[0]).toBe(seen[1])
  })

  it("shows the checking state with no start action", async () => {
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checking", {
          revision: 1,
          opportunityRevision: 2,
        }),
      },
      checks: { "job-1": { status: "checking" } },
    })
    renderCheckPage("job-1")

    expect(
      await screen.findByText("Check in progress…")
    ).toBeDefined()
    expect(
      screen.queryByRole("button", { name: "Start check" })
    ).toBeNull()
    expect(
      screen.queryByRole("button", { name: "Retry check" })
    ).toBeNull()
    expect(
      await screen.findByRole("button", { name: "Refresh saved check" })
    ).toBeDefined()
  })

  it("shows saved evidence, questions and pending answers when checked", async () => {
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checked", {
          revision: 1,
          opportunityRevision: 2,
        }),
      },
      checks: {
        "job-1": {
          status: "checked",
          check: checkFixture("job-1", "checked"),
        },
      },
    })
    renderCheckPage("job-1")

    expect(await screen.findByText("Saved vacancy")).toBeDefined()
    expect(
      await screen.findByText("Why do you want this role?")
    ).toBeDefined()
    expect(await screen.findByText("CV · required")).toBeDefined()
    expect(
      await screen.findByText("Salary band unconfirmed.")
    ).toBeDefined()
    expect(
      await screen.findByText("application_route (direct)")
    ).toBeDefined()
    const answersLink = await screen.findByRole("link", {
      name: "Answer questions",
    })
    expect(answersLink.getAttribute("href")).toBe("#/jobs/job-1/answers")
    expect(
      screen.queryByRole("button", { name: "Start check" })
    ).toBeNull()
  })

  it("shows the blocker and a retry action when blocked", async () => {
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "blocked", {
          revision: 1,
          opportunityRevision: 2,
          blockedReason: "Posting unreachable.",
        }),
      },
      checks: {
        "job-1": {
          status: "blocked",
          check: checkFixture("job-1", "blocked", {
            blockedReason: {
              code: "source_unavailable",
              detail: "The posting returned HTTP 404.",
            },
          }),
        },
      },
      postCheck: () => jsonResponse(201, { status: "checking" }),
    })
    renderCheckPage("job-1")

    expect(await screen.findByText("Check blocked")).toBeDefined()
    expect(
      await screen.findByText(
        "Blocked — source_unavailable: The posting returned HTTP 404."
      )
    ).toBeDefined()
    expect(
      await screen.findByRole("button", { name: "Retry check" })
    ).toBeDefined()
  })

  it("shows the last saved check and a new-check action when outdated", async () => {
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checked", {
          revision: 1,
          opportunityRevision: 2,
        }),
      },
      checks: {
        "job-1": {
          status: "outdated",
          check: checkFixture("job-1", "checked"),
        },
      },
      postCheck: () => jsonResponse(201, { status: "checking" }),
    })
    renderCheckPage("job-1")

    expect(
      await screen.findByText("Saved check is outdated")
    ).toBeDefined()
    expect(
      await screen.findByText("Why do you want this role?")
    ).toBeDefined()
    expect(
      await screen.findByRole("button", { name: "Run a new check" })
    ).toBeDefined()
  })

  it("reports an honest error when checked arrives without details", async () => {
    stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checked", {
          revision: 1,
          opportunityRevision: 2,
        }),
      },
      checks: { "job-1": { status: "checked" } },
    })
    renderCheckPage("job-1")

    expect(
      await screen.findByText("Saved check details missing")
    ).toBeDefined()
  })

  it("shows role-not-selected without touching check endpoints", async () => {
    const { calls } = stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: { "job-1": null },
    })
    renderCheckPage("job-1")

    expect(await screen.findByText("Role not selected")).toBeDefined()
    expect(
      await screen.findByText(
        "This role is not selected, so the server keeps no check state for it. Only chosen roles can be checked."
      )
    ).toBeDefined()
    await waitFor(() =>
      expect(
        calls.some((call) => call.url.endsWith("/workflow"))
      ).toBe(true)
    )
    expect(
      calls.some((call) => call.url.includes("/checks"))
    ).toBe(false)
  })

  it("paginates check activity without starting work", async () => {
    const { calls } = stubCheckFetch({
      opportunities: { "job-1": jobOne },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checking", {
          revision: 1,
          opportunityRevision: 2,
        }),
      },
      checks: { "job-1": { status: "checking" } },
      activityPages: {
        "job-1": [
          {
            events: [
              activityFixture("e1", "run.dispatched", "Dispatched check"),
            ],
            nextCursor: "cursor-2",
          },
          {
            events: [
              activityFixture("e2", "capture", "Captured posting"),
            ],
          },
        ],
      },
    })
    renderCheckPage("job-1")

    fireEvent.click(
      await screen.findByText("Check job details · activity")
    )
    expect(await screen.findByText("Dispatched check")).toBeDefined()
    fireEvent.click(
      await screen.findByRole("button", { name: "Load more activity" })
    )
    expect(await screen.findByText("Captured posting")).toBeDefined()
    expect(
      await screen.findByText("Dispatched check")
    ).toBeDefined()
    for (const call of calls) expect(call.method).toBe("GET")
  })
})

describe("read-only mount and per-role independence", () => {
  function baseOptions(): CheckStubOptions {
    return {
      opportunities: { "job-1": jobOne, "job-2": jobTwo },
      workflows: {
        "job-1": roleWorkflowFixture("job-1", "checked", {
          revision: 1,
          opportunityRevision: 2,
        }),
        "job-2": roleWorkflowFixture("job-2", "selected", {
          revision: 0,
          opportunityRevision: 5,
        }),
      },
      checks: {
        "job-1": {
          status: "checked",
          check: checkFixture("job-1", "checked"),
        },
        "job-2": { status: "not_checked" },
      },
    }
  }

  it("issues GET reads only on mount and on reload", async () => {
    const options = baseOptions()
    const first = stubCheckFetch(options)
    const rendered = renderCheckPage("job-1")
    expect(
      await screen.findByText("Why do you want this role?")
    ).toBeDefined()
    expect(first.calls.length).toBeGreaterThan(0)
    for (const call of first.calls) expect(call.method).toBe("GET")
    rendered.unmount()
    cleanup()

    const second = stubCheckFetch(options)
    renderCheckPage("job-1")
    expect(
      await screen.findByText("Why do you want this role?")
    ).toBeDefined()
    expect(second.calls.length).toBeGreaterThan(0)
    for (const call of second.calls) expect(call.method).toBe("GET")
  })

  it("keeps two roles independent across reads and starts", async () => {
    const posts: { url: string; body: unknown }[] = []
    const options = baseOptions()
    options.postCheck = (jobId, body) => {
      posts.push({ url: jobId, body })
      return jsonResponse(201, { status: "checking" })
    }
    stubCheckFetch(options)

    const first = renderCheckPage("job-1")
    expect(
      await screen.findByText("Why do you want this role?")
    ).toBeDefined()
    expect(
      screen.queryByRole("button", { name: "Start check" })
    ).toBeNull()
    first.unmount()
    cleanup()

    stubCheckFetch(options)
    renderCheckPage("job-2")
    expect(await screen.findByText("No check yet")).toBeDefined()
    fireEvent.click(
      await screen.findByRole("button", { name: "Start check" })
    )
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]?.url).toBe("job-2")
    const payload = posts[0]?.body as Record<string, unknown>
    expect(payload["expectedOpportunityRevision"]).toBe(5)
    expect(payload["expectedWorkflowRevision"]).toBe(0)
  })
})
