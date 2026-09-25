// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type {
  CheckStatusView,
  CheckView,
  OpportunityView,
  Preferences,
  ResearchActivityEvent,
  ResearchReportView,
  ResearchRunView,
  RoleWorkflowState,
  SearchBriefView,
} from "@/api/client"
import { SessionProvider, useSession } from "@/api/session"
import { AnswersPage } from "@/features/answers"
import { CheckPage } from "@/features/check/CheckPage"
import {
  CheckChosenJobs,
  type ChosenRoleInput,
} from "@/features/discovery/check-chosen-jobs"
import {
  discoveryRunStorageKey,
  DiscoverySection,
} from "@/features/discovery/discovery-section"
import {
  fixtureMuseState,
  type MuseFixtureScenario,
} from "@/features/discovery/muse-state"
import { PreparePage } from "@/features/prepare/PreparePage"
import {
  roleWorkflowFixture,
  sessionFixture,
} from "@/pages/fixtures"

interface FetchCall {
  url: string
  method: string
  body: string | null
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

function recordCall(
  calls: FetchCall[],
  input: string | URL | Request,
  init?: RequestInit
): { path: string; method: string; body: unknown } {
  const url =
    typeof input === "string"
      ? input
      : input instanceof URL
        ? input.toString()
        : input.url
  const method = (init?.method ?? "GET").toUpperCase()
  const rawBody = typeof init?.body === "string" ? init.body : null
  calls.push({ url, method, body: rawBody })
  return {
    path: new URL(url, "http://localhost").pathname,
    method,
    body: rawBody === null ? null : JSON.parse(rawBody),
  }
}

// --- Discovery fixtures -------------------------------------------------

const journeyPreferences: Preferences = {
  version: 3,
  preferredLocation: "Berlin",
  allowRemote: true,
  allowHybrid: false,
  targetHours: "32",
  minMonthlyBaseCents: 450000,
  salaryCurrency: "EUR",
  timezone: "Europe/Berlin",
  roleCriteria: [
    {
      id: "criterion-1",
      label: "Backend engineer",
      description: "Server-side product work.",
      kind: "role",
      mode: "require",
      definitionHash: "hash-1",
    },
  ],
}

function journeyBrief(overrides: Partial<SearchBriefView> = {}): SearchBriefView {
  return {
    profileVersion: 3,
    rubricVersion: "criteria-v3-abc123",
    rubricSource: "preferences_versions:current:v3",
    catalogVersion: "catalog-v3",
    requirements: [
      {
        id: "criterion-1",
        label: "Backend engineer",
        description: "Server-side product work.",
        kind: "role",
        mode: "require",
        definitionHash: "hash-1",
      },
    ],
    facts: [{ key: "preferredLocation", value: "Berlin" }],
    ...overrides,
  }
}

const journeyAllowance = {
  timeMs: 900000,
  maxActions: 60,
  maxJev: 12,
  maxTurns: 8,
  maxConcurrent: 2,
}

const journeyUsage = {
  enforced: journeyAllowance,
  reserved: { actions: 2, jev: 1, turns: 1 },
  observed: { actions: 5, jev: 3, turns: 2, bytes: 1234 },
  unknown: false,
}

function journeyRun(runId: string, state: ResearchRunView["state"]): ResearchRunView {
  return {
    runId,
    state,
    briefVersion: { profileVersion: 3, rubricVersion: "criteria-v3-abc123" },
    allowance: journeyAllowance,
    usage: journeyUsage,
    investigations: [{ id: "inv-1", intent: "collect", status: "active" }],
    savedIds: ["job-1"],
    unresolvedCount: 1,
  }
}

function journeyReport(runId: string): ResearchReportView {
  return {
    runId,
    outcomes: ['opportunity "Backend Engineer" (job-1 rev 2)'],
    searched: ["collect x 4"],
    reused: [],
    uncertainty: [],
    budget: { observed: journeyUsage, unknown: false },
    nextWork: ["Keep collecting"],
  }
}

interface DiscoveryStubOptions {
  runState?: ResearchRunView["state"]
  brief?: SearchBriefView | null | "error"
}

function stubDiscoveryFetch(options: DiscoveryStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let commissions = 0
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const { path, method } = recordCall(calls, input, init)
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/preferences" && method === "GET")
        return jsonResponse(200, journeyPreferences)
      if (path === "/api/v1/research/brief" && method === "GET") {
        const brief = options.brief === undefined ? journeyBrief() : options.brief
        if (brief === "error")
          return jsonResponse(500, { error: { message: "Brief down." } })
        if (brief === null) return notFound("No brief.")
        return jsonResponse(200, brief)
      }
      if (path === "/api/v1/research/runs" && method === "POST") {
        commissions += 1
        return jsonResponse(
          201,
          journeyRun(`run-${commissions}`, options.runState ?? "running")
        )
      }
      const runMatch = path.match(/^\/api\/v1\/research\/runs\/([^/]+)(\/.*)?$/)
      if (runMatch?.[1] !== undefined) {
        const id = decodeURIComponent(runMatch[1])
        const suffix = runMatch[2] ?? ""
        if (suffix === "")
          return jsonResponse(200, journeyRun(id, options.runState ?? "running"))
        if (suffix === "/activity") return jsonResponse(200, { events: [] })
        if (suffix === "/report") return jsonResponse(200, journeyReport(id))
      }
      const roundMatch = path.match(/^\/api\/v1\/rounds\/([^/]+)\/(stop|resume)$/)
      if (roundMatch !== null && method === "POST")
        return jsonResponse(200, { ok: true })
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderDiscovery(museScenario?: MuseFixtureScenario) {
  return render(
    <SessionProvider>
      <DiscoverySection museScenario={museScenario} />
    </SessionProvider>
  )
}

function SessionProbe() {
  const { session } = useSession()
  return <p>{session === undefined ? "session loading" : "signed in"}</p>
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("muse readiness on discovery", () => {
  it("surfaces ready Contributor state with zero muse calls on read", async () => {
    const { calls } = stubDiscoveryFetch()
    renderDiscovery()

    expect(
      await screen.findByText("Contributor: ready (muse_ready).")
    ).toBeDefined()
    expect(fixtureMuseState("ready").commissionedCalls).toBe(0)
    await waitFor(() => {
      expect(
        calls.some((call) => call.url === "/api/v1/research/brief")
      ).toBe(true)
    })
    // Passive reads commission nothing and never touch a muse endpoint.
    expect(calls.every((call) => call.method === "GET")).toBe(true)
    expect(calls.some((call) => call.url.includes("muse"))).toBe(false)
  })

  it("runs Find jobs explicitly only when Contributor is ready", async () => {
    const { calls } = stubDiscoveryFetch()
    renderDiscovery()

    const findJobs = await screen.findByRole("button", { name: "Find jobs" })
    expect((findJobs as HTMLButtonElement).disabled).toBe(false)
    expect(
      calls.filter(
        (call) => call.method === "POST" && call.url === "/api/v1/research/runs"
      )
    ).toHaveLength(0)
    fireEvent.click(findJobs)
    await waitFor(() => {
      expect(
        calls.filter(
          (call) =>
            call.method === "POST" && call.url === "/api/v1/research/runs"
        )
      ).toHaveLength(1)
    })
  })

  it("keeps Find jobs disabled with the blocking code while unavailable", async () => {
    const { calls } = stubDiscoveryFetch()
    renderDiscovery("blocked")

    expect(
      await screen.findByText(
        "Contributor: unavailable (muse_lane_unverified) — effective model/subscription lane is not verified."
      )
    ).toBeDefined()
    const findJobs = await screen.findByRole("button", { name: "Find jobs" })
    expect((findJobs as HTMLButtonElement).disabled).toBe(true)
    expect(await screen.findByRole("alert")).toBeDefined()
    fireEvent.click(findJobs)
    expect(
      calls.filter((call) => call.method === "POST")
    ).toHaveLength(0)
  })

  it("reports needs-setup plainly", async () => {
    stubDiscoveryFetch()
    renderDiscovery("needs-setup")

    expect(
      await screen.findByText(
        "Contributor: needs setup (muse_not_configured) — muse CLI path is not configured."
      )
    ).toBeDefined()
    const findJobs = await screen.findByRole("button", { name: "Find jobs" })
    expect((findJobs as HTMLButtonElement).disabled).toBe(true)
  })

  it("keeps a wrong brief honest and Find jobs closed", async () => {
    stubDiscoveryFetch({ brief: journeyBrief({ profileVersion: 2 }) })
    renderDiscovery()

    expect(
      await screen.findByText(
        "The saved brief is behind profile v3. Refresh before starting a run."
      )
    ).toBeDefined()
    const findJobs = await screen.findByRole("button", { name: "Find jobs" })
    expect((findJobs as HTMLButtonElement).disabled).toBe(true)
  })

  it("reloads the tracked run through reads only", async () => {
    window.localStorage.setItem(discoveryRunStorageKey, "run-9")
    const { calls } = stubDiscoveryFetch({ runState: "completed" })
    renderDiscovery()

    expect(await screen.findByText(/Completed — /)).toBeDefined()
    expect(
      await screen.findByText("No Muse checkpoints saved yet")
    ).toBeDefined()
    await waitFor(() => {
      expect(
        calls.some((call) => call.url === "/api/v1/research/runs/run-9")
      ).toBe(true)
    })
    expect(calls.every((call) => call.method === "GET")).toBe(true)
  })
})

describe("muse checkpoints and reports", () => {
  it("shows partial-run checkpoints, gaps and the next action", async () => {
    stubDiscoveryFetch()
    renderDiscovery("partial")

    expect(
      await screen.findByText("Run checkpoints (2) · 3 saved")
    ).toBeDefined()
    expect(await screen.findByText("Gaps")).toBeDefined()
    expect(
      await screen.findByText(/Review the 2 saved roles below/)
    ).toBeDefined()
  })

  it("reports no-result runs without inventing vacancies", async () => {
    stubDiscoveryFetch()
    renderDiscovery("no-result")

    expect(
      await screen.findByText("Run report — no suitable vacancy")
    ).toBeDefined()
    expect(await screen.findByText("Saved roles: none.")).toBeDefined()
    expect(
      await screen.findByText(/Change my search to widen the brief/)
    ).toBeDefined()
  })

  it("renders long checkpoint lists in full", async () => {
    stubDiscoveryFetch()
    renderDiscovery("long-list")

    expect(
      await screen.findByText("Run checkpoints (30) · 60 saved")
    ).toBeDefined()
    expect(await screen.findByText(/Checkpoint 30 · run run-long/)).toBeDefined()
  })
})

// --- Check + answer fixtures --------------------------------------------

const chosenRoles: ChosenRoleInput[] = [
  {
    jobId: "job-a",
    title: "Backend Engineer",
    opportunityRevision: 2,
    workflowRevision: 1,
  },
  {
    jobId: "job-b",
    title: "Support Engineer",
    opportunityRevision: 3,
    workflowRevision: 4,
  },
]

function journeyOpportunity(id: string, title: string): OpportunityView {
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
      revision: 2,
      createdAt: "2026-09-10T09:00:00Z",
      updatedAt: "2026-09-12T09:00:00Z",
      compensation: {},
    },
    likelyDuplicates: [],
  }
}

function journeyCheckView(
  opportunityId: string,
  status: CheckView["status"]
): CheckView {
  return {
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
      kind: "direct",
      destinationText: "Apply through the portal.",
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
    ],
    questionSetSha256: "set-sha",
    questionSetVersion: 1,
    createdAt: "2026-09-20T10:00:00Z",
    createdBy: { actorKind: "codex", actorId: "codex" },
  }
}

interface RoleStubOptions {
  opportunities?: Record<string, OpportunityView | null>
  workflows?: Record<string, RoleWorkflowState | null>
  checks?: Record<string, CheckStatusView | null>
  activity?: ResearchActivityEvent[]
  postCheck?: (jobId: string, body: unknown) => Response
}

function stubRoleFetch(options: RoleStubOptions = {}): { calls: FetchCall[] } {
  const calls: FetchCall[] = []
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const { path, method, body } = recordCall(calls, input, init)
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      const match = path.match(/^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/)
      if (match?.[1] !== undefined) {
        const id = decodeURIComponent(match[1])
        const suffix = match[2] ?? ""
        if (suffix === "") {
          const view = options.opportunities?.[id] ?? null
          return view === null
            ? notFound("Opportunity not found.")
            : jsonResponse(200, view)
        }
        if (suffix === "/workflow") {
          const entry = options.workflows?.[id] ?? null
          return entry === null
            ? notFound("Role is not selected.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks/current/activity")
          return jsonResponse(200, { events: options.activity ?? [] })
        if (suffix === "/checks/current") {
          const entry = options.checks?.[id] ?? null
          return entry === null
            ? notFound("Check not found.")
            : jsonResponse(200, entry)
        }
        if (suffix === "/checks" && method === "POST") {
          if (options.postCheck !== undefined)
            return options.postCheck(id, body)
          return jsonResponse(500, { error: { message: "No stub handler." } })
        }
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

describe("check chosen jobs with muse readiness", () => {
  it("selects without deeper work; one explicit click checks each role", async () => {
    const { calls } = stubRoleFetch({
      postCheck: () => jsonResponse(201, { status: "checking" } satisfies CheckStatusView),
    })
    render(
      <SessionProvider>
        <SessionProbe />
        <CheckChosenJobs
          roles={chosenRoles}
          contributor={fixtureMuseState("ready").contributor}
        />
      </SessionProvider>
    )
    await screen.findByText("signed in")

    // Mounting the chosen roles starts nothing.
    expect(
      await screen.findAllByText("Not started — selection alone starts nothing.")
    ).toHaveLength(2)
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0)

    fireEvent.click(
      await screen.findByRole("button", { name: "Check chosen jobs (2)" })
    )
    await waitFor(() => {
      expect(
        calls.filter(
          (call) =>
            call.method === "POST" &&
            /\/api\/v1\/opportunities\/[^/]+\/checks$/.test(call.url)
        )
      ).toHaveLength(2)
    })
  })

  it("disables checks while Contributor is blocked", async () => {
    const { calls } = stubRoleFetch()
    render(
      <SessionProvider>
        <SessionProbe />
        <CheckChosenJobs
          roles={chosenRoles}
          contributor={fixtureMuseState("blocked").contributor}
        />
      </SessionProvider>
    )
    await screen.findByText("signed in")

    const action = await screen.findByRole("button", {
      name: "Check chosen jobs (2)",
    })
    expect((action as HTMLButtonElement).disabled).toBe(true)
    expect(
      await screen.findByText(/Checks are blocked: Muse Contributor is unavailable/)
    ).toBeDefined()
    fireEvent.click(action)
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0)
  })
})

describe("check page with muse readiness", () => {
  function selectedOptions(checks: Record<string, CheckStatusView>): RoleStubOptions {
    return {
      opportunities: { "job-1": journeyOpportunity("job-1", "Backend Engineer") },
      workflows: { "job-1": roleWorkflowFixture("job-1", "selected") },
      checks,
    }
  }

  it("reads saved state with zero commissions, then starts explicitly", async () => {
    const { calls } = stubRoleFetch({
      ...selectedOptions({ "job-1": { status: "not_checked" } }),
      postCheck: () => jsonResponse(201, { status: "checking" } satisfies CheckStatusView),
    })
    render(
      <SessionProvider>
        <CheckPage jobId="job-1" contributor={fixtureMuseState("ready").contributor} />
      </SessionProvider>
    )

    const start = await screen.findByRole("button", { name: "Start check" })
    expect((start as HTMLButtonElement).disabled).toBe(false)
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0)

    fireEvent.click(start)
    await waitFor(() => {
      expect(
        calls.filter((call) => call.method === "POST")
      ).toHaveLength(1)
    })
  })

  it("blocks the start control with the muse code", async () => {
    const { calls } = stubRoleFetch(
      selectedOptions({ "job-1": { status: "not_checked" } })
    )
    render(
      <SessionProvider>
        <CheckPage
          jobId="job-1"
          contributor={fixtureMuseState("blocked").contributor}
        />
      </SessionProvider>
    )

    const start = await screen.findByRole("button", { name: "Start check" })
    expect((start as HTMLButtonElement).disabled).toBe(true)
    expect(
      await screen.findByText(/This check is blocked: Muse Contributor is unavailable/)
    ).toBeDefined()
    fireEvent.click(start)
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(0)
  })

  it("renders saved employer questions from reads only", async () => {
    const { calls } = stubRoleFetch(
      selectedOptions({
        "job-1": {
          status: "checked",
          check: journeyCheckView("job-1", "checked"),
        },
      })
    )
    render(
      <SessionProvider>
        <CheckPage jobId="job-1" />
      </SessionProvider>
    )

    expect(await screen.findByText("Can you work remotely?")).toBeDefined()
    expect(calls.every((call) => call.method === "GET")).toBe(true)
  })
})

describe("saved answers stay model-free on read", () => {
  function stubAnswersRead(): { calls: FetchCall[] } {
    const calls: FetchCall[] = []
    const fetchMock = vi.fn(
      async (input: string | URL | Request, init?: RequestInit) => {
        const { path, method } = recordCall(calls, input, init)
        if (path === "/api/v1/auth/session")
          return jsonResponse(200, sessionFixture)
        if (path === "/api/v1/answers/answer-remote" && method === "GET")
          return jsonResponse(200, {
            id: "answer-remote",
            currentVersion: 1,
            scopeTags: ["remote"],
            versions: [
              {
                version: 1,
                text: "I work remotely from Example City.",
                textSha256: "text-sha",
                approvedAt: "2026-09-19T10:00:00Z",
                approvedBy: { actorKind: "administrator", actorId: "owner" },
                approvalRequestKey: "approve-1",
              },
            ],
          })
        const match = path.match(/^\/api\/v1\/opportunities\/([^/]+)(\/.*)?$/)
        if (match?.[1] !== undefined) {
          const id = decodeURIComponent(match[1])
          const suffix = match[2] ?? ""
          if (id !== "job-1") return notFound("Opportunity not found.")
          if (suffix === "")
            return jsonResponse(200, journeyOpportunity("job-1", "Backend Engineer"))
          if (suffix === "/workflow")
            return jsonResponse(200, roleWorkflowFixture("job-1", "checked"))
          if (suffix === "/checks/current")
            return jsonResponse(200, {
              status: "checked",
              check: journeyCheckView("job-1", "checked"),
            } satisfies CheckStatusView)
          if (suffix === "/answers/match/current")
            return jsonResponse(200, {
              status: "matched",
              checkId: "check-job-1",
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
              ],
            })
          if (suffix === "/answers/current")
            return jsonResponse(200, {
              checkId: "check-job-1",
              questionSetSha256: "set-sha",
              values: [],
            })
        }
        return jsonResponse(404, { error: { message: "Not found." } })
      }
    )
    vi.stubGlobal("fetch", fetchMock)
    return { calls }
  }

  it("shows the saved question and matched answer with GET-only reads", async () => {
    const { calls } = stubAnswersRead()
    render(
      <SessionProvider>
        <AnswersPage jobId="job-1" />
      </SessionProvider>
    )

    expect(
      await screen.findAllByText(/Can you work remotely\?/)
    ).toHaveLength(2)
    expect(
      await screen.findByDisplayValue("I work remotely from Example City.")
    ).toBeDefined()
    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
  })
})

describe("prepare surfaces standard readiness", () => {
  it("shows a blocked Standard tier without touching saved material", async () => {
    const { calls } = stubRoleFetch({
      opportunities: { "job-1": journeyOpportunity("job-1", "Backend Engineer") },
      workflows: { "job-1": roleWorkflowFixture("job-1", "checked") },
    })
    render(
      <SessionProvider>
        <PreparePage
          jobId="job-1"
          standard={fixtureMuseState("blocked").standard}
        />
      </SessionProvider>
    )

    expect(
      await screen.findByText(
        "Standard: unavailable (muse_protocol_unverified) — initialize sessionMcp + config.mcpServers is not verified."
      )
    ).toBeDefined()
    expect(calls.every((call) => call.method === "GET")).toBe(true)
  })
})
