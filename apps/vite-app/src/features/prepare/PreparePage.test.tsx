// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import type {
  CheckStatusView,
  CheckView,
  OpportunityView,
  RoleWorkflowState,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { PreparePage } from "@/features/prepare/PreparePage"
import {
  buildMaterialPrepareRequest,
  countBytes,
  countRunes,
} from "@/features/prepare/usePrepareActions"
import { roleWorkflowFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

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
  overall: CheckStatusView["status"] = "checked"
): CheckStatusView {
  const check: CheckView = {
    id: `check-${opportunityId}`,
    opportunityId,
    opportunityRevision: 2,
    workflowRevision: 1,
    status:
      overall === "blocked"
        ? "blocked"
        : overall === "checking"
          ? "checking"
          : "checked",
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
  if (overall === "not_checked") return { status: overall }
  return { status: overall, check }
}

interface FetchCall {
  url: string
  method: string
  body: string | null
}

interface PrepareStubOptions {
  opportunities?: Record<string, OpportunityView | null>
  workflows?: Record<string, RoleWorkflowState | null>
  checks?: Record<string, CheckStatusView | null>
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

function stubPrepareFetch(options: PrepareStubOptions = {}): {
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
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderPreparePage(jobId: string) {
  return render(
    <SessionProvider>
      <PreparePage jobId={jobId} />
    </SessionProvider>
  )
}

function preparedOptions(
  overrides: Partial<PrepareStubOptions> = {}
): PrepareStubOptions {
  return {
    opportunities: { "job-1": opportunityFixture("job-1") },
    workflows: {
      "job-1": roleWorkflowFixture("job-1", "answered", { revision: 1 }),
    },
    checks: { "job-1": checkFixture("job-1") },
    ...overrides,
  }
}

describe("prepare payloads", () => {
  it("builds the exact prepare payload from observed pins", () => {
    expect(
      buildMaterialPrepareRequest("key-1", "check-1", "set-sha", 4)
    ).toEqual({
      requestKey: "key-1",
      expectedCheckId: "check-1",
      expectedQuestionSetSha256: "set-sha",
      expectedWorkflowRevision: 4,
    })
  })

  it("counts runes and bytes for multibyte text", () => {
    expect(countRunes("✅")).toBe(1)
    expect(countBytes("✅")).toBe(3)
  })
})

describe("prepare page reads", () => {
  it("mounts GET-only and reads no pack or material endpoint", async () => {
    const { calls } = stubPrepareFetch(preparedOptions())
    renderPreparePage("job-1")

    expect(
      await screen.findByRole("heading", { name: "Prepare materials" })
    ).toBeDefined()
    expect(
      screen.getByRole("heading", { name: "Putting your application together" })
    ).toBeDefined()
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
    expect(
      calls.some((call) => call.url.includes("/materials/"))
    ).toBe(false)
    expect(
      calls.some((call) => call.url.includes("/application-packs"))
    ).toBe(false)
  })

  it("shows the not-selected state without check reads", async () => {
    const { calls } = stubPrepareFetch(
      preparedOptions({ workflows: { "job-1": null } })
    )
    renderPreparePage("job-1")

    expect(await screen.findByText("Role not selected")).toBeDefined()
    expect(
      calls.some((call) => call.url.includes("/materials/"))
    ).toBe(false)
    expect(
      calls.some((call) => call.url.includes("/checks/"))
    ).toBe(false)
  })

  it("blocks preparation until a check completes", async () => {
    const { calls } = stubPrepareFetch(
      preparedOptions({
        checks: { "job-1": checkFixture("job-1", "not_checked") },
      })
    )
    renderPreparePage("job-1")

    expect(await screen.findByText("No check yet")).toBeDefined()
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })
})
