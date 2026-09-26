// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import type {
  FindingEntry,
  OpportunityView,
  OwnerDecision,
  Preferences,
  ResearchCaptureView,
  RoleWorkflowState,
  SearchBriefView,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { GroupedJobs } from "@/features/discovery/grouped-jobs"
import { sessionFixture } from "@/pages/fixtures"

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

function jobView(id: string, title: string, revision = 2): OpportunityView {
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

const jobsFixture: OpportunityView[] = [
  jobView("job-rec", "Backend Engineer"),
  jobView("job-could", "Support Engineer"),
  jobView("job-prob", "Weekend Project"),
  jobView("job-not", "Night Shift Ops"),
  jobView("job-unknown", "Mystery Role"),
  jobView("job-new", "Fresh Listing"),
]

function findingBase(id: string, group: FindingEntry["group"]): FindingEntry {
  return {
    opportunityId: id,
    opportunityRevision: 2,
    group,
    assessmentId: `asmt-${id}`,
    profileVersion: 3,
    rubricVersion: "rubric-1",
    catalogVersion: "cat-7",
    reasons: [],
    evidenceLinks: [],
    stale: false,
  }
}

const recFinding: FindingEntry = {
  ...findingBase("job-rec", "recommended"),
  reasons: [
    {
      reasonId: "r-pos-1",
      kind: "positive",
      label: "Loves “quoted” labelling ✓",
      detail: "Detail with “quotes”, emoji ✓, and\nnewline.",
      jevSupport: 0.85,
    },
    {
      reasonId: "r-neg-1",
      kind: "negative",
      label: "On-call load",
      detail: "Rota includes weekends.",
      jevSupport: 0.4,
    },
  ],
  conflict: {
    id: "c-1",
    label: "Hybrid expectation",
    detail: "Two days onsite expected.",
  },
  missingFact: {
    id: "m-1",
    label: "Salary band",
    detail: "No band published.",
  },
  evidenceLinks: [
    {
      captureId: "cap-1",
      spanStart: 10,
      spanEnd: 40,
      excerptSha256: "a".repeat(64),
    },
  ],
  sourceRef: {
    sourceId: "career-site",
    sourceRevision: "2026-09-20",
    observedUrl: "https://example.com/posting",
  },
}

const couldFinding: FindingEntry = {
  ...findingBase("job-could", "could_be_recommended"),
  reasons: [
    {
      reasonId: "r-pos-2",
      kind: "positive",
      label: "Remote-first team",
      detail: "Docs say remote-first.",
      jevSupport: 0.62,
    },
  ],
  evidenceLinks: [
    {
      captureId: "cap-missing",
      spanStart: 0,
      spanEnd: 12,
      excerptSha256: "b".repeat(64),
    },
  ],
}

const probFinding: FindingEntry = {
  ...findingBase("job-prob", "probably_not_recommended"),
  reasons: [
    {
      reasonId: "r-neg-2",
      kind: "negative",
      label: "Weekend hours",
      detail: "Posting requires weekends.",
      jevSupport: 0.71,
    },
  ],
  stale: true,
  staleBasis: "opportunity_revised",
}

const notFinding: FindingEntry = {
  ...findingBase("job-not", "not_recommended"),
  reasons: [
    {
      reasonId: "r-neg-3",
      kind: "negative",
      label: "Night shifts",
      detail: "Permanent nights.",
      jevSupport: 0.9,
    },
  ],
}

const unknownFinding: FindingEntry = {
  ...findingBase("job-unknown", "unknown"),
  unknownBasis: "Evidence conflicted across sources.",
  stale: true,
  staleBasis: "brief_changed,catalog_changed",
}

const findingsByOpportunity: Record<string, FindingEntry> = {
  "job-rec": recFinding,
  "job-could": couldFinding,
  "job-prob": probFinding,
  "job-not": notFinding,
  "job-unknown": unknownFinding,
}

const briefFixture: SearchBriefView = {
  profileVersion: 3,
  rubricVersion: "rubric-1",
  rubricSource: "codex",
  catalogVersion: "cat-7",
  requirements: [],
  facts: [],
}

const captureFixture: ResearchCaptureView = {
  captureId: "cap-1",
  observedUrl: "https://example.com/posting",
  retrievedAt: "2026-09-24T10:01:00Z",
  status: "ok",
  mediaType: "text/html",
  contentHash: "abc123",
  extent: { bytes: 1234, complete: true },
}

function workflowFixture(id: string): RoleWorkflowState {
  return {
    opportunityId: id,
    stage: "selected",
    revision: 1,
    updatedAt: "2026-09-18T10:00:00Z",
    decisionAt: "2026-09-18T10:00:00Z",
    opportunityRevision: 2,
  }
}

const groupedPreferencesFixture: Preferences = {
  version: 3,
  preferredLocation: "Berlin",
  allowRemote: true,
  allowHybrid: false,
  targetHours: "32",
  minMonthlyBaseCents: 450000,
  salaryCurrency: "EUR",
  timezone: "Europe/Berlin",
  roleCriteria: [],
}

interface StubOptions {
  existingDecisions?: Record<string, OwnerDecision>
  workflowStages?: Record<string, RoleWorkflowState["stage"]>
  decisionConflict?: boolean
  preferences?: Preferences
  // null serves a 404 (no saved brief yet); "error" serves a 500.
  brief?: SearchBriefView | null | "error"
  failBriefAttempts?: number
}

function stubGroupedFetch(options: StubOptions = {}): { calls: FetchCall[] } {
  const calls: FetchCall[] = []
  const selected = new Set<string>(["job-rec"])
  const decisions = new Map<string, OwnerDecision>(
    Object.entries(options.existingDecisions ?? {})
  )
  decisions.set("job-rec", {
    id: "decision-rec",
    opportunityId: "job-rec",
    decision: "selected",
    revision: 1,
    opportunityRevision: 2,
    auditId: "audit-rec",
    createdAt: "2026-09-18T10:00:00Z",
  })
  let briefAttempts = 0
  const fetchMock = vi.fn(
    async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({
        url,
        method,
        body: typeof init?.body === "string" ? init.body : null,
      })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname

      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/opportunities" && method === "GET")
        return jsonResponse(200, { items: jobsFixture })
      if (path === "/api/v1/preferences" && method === "GET")
        return jsonResponse(
          200,
          options.preferences ?? groupedPreferencesFixture
        )
      if (path === "/api/v1/research/brief" && method === "GET") {
        briefAttempts += 1
        if (
          options.failBriefAttempts !== undefined &&
          briefAttempts <= options.failBriefAttempts
        )
          return jsonResponse(500, { error: { message: "Brief down." } })
        const brief = options.brief === undefined ? briefFixture : options.brief
        if (brief === "error")
          return jsonResponse(500, { error: { message: "Brief down." } })
        if (brief === null)
          return jsonResponse(404, { error: { message: "No brief." } })
        return jsonResponse(200, brief)
      }
      if (path === "/api/v1/workflow/roles" && method === "GET")
        return jsonResponse(200, {
          items: [...selected].map((id) => ({
            ...workflowFixture(id),
            stage: options.workflowStages?.[id] ?? "selected",
          })),
        })

      const runFindings = path.match(
        /^\/api\/v1\/research\/runs\/([^/]+)\/findings$/
      )
      if (runFindings?.[1] !== undefined && method === "GET") {
        return jsonResponse(200, { items: [recFinding, unknownFinding] })
      }

      const captureMatch = path.match(
        /^\/api\/v1\/research\/captures\/([^/]+)$/
      )
      if (captureMatch?.[1] !== undefined && method === "GET") {
        if (decodeURIComponent(captureMatch[1]) === "cap-1")
          return jsonResponse(200, captureFixture)
        return notFound("Unknown capture.")
      }

      const oppMatch = path.match(
        /^\/api\/v1\/opportunities\/([^/]+)\/(finding|decision|workflow)$/
      )
      if (oppMatch?.[1] !== undefined && oppMatch[2] !== undefined) {
        const id = decodeURIComponent(oppMatch[1])
        const leaf = oppMatch[2]
        if (leaf === "finding" && method === "GET") {
          const entry = findingsByOpportunity[id]
          if (entry === undefined) return notFound("No saved finding.")
          return jsonResponse(200, entry)
        }
        if (leaf === "decision" && method === "GET") {
          const existing = decisions.get(id)
          if (existing === undefined) return notFound("No decision recorded.")
          return jsonResponse(200, existing)
        }
        if (leaf === "decision" && method === "POST") {
          if (options.decisionConflict === true)
            return jsonResponse(409, {
              error: { message: "Decision revision conflict; refresh first." },
            })
          const payload = JSON.parse(
            typeof init?.body === "string" ? init.body : "{}"
          ) as {
            expectedOpportunityRevision?: number
            decision: OwnerDecision["decision"]
          }
          if (payload.decision === "selected") selected.add(id)
          else selected.delete(id)
          const saved: OwnerDecision = {
            id: "decision-9",
            opportunityId: id,
            decision: payload.decision,
            revision: (decisions.get(id)?.revision ?? 0) + 1,
            opportunityRevision: payload.expectedOpportunityRevision ?? 2,
            auditId: "audit-9",
            createdAt: "2026-09-24T10:00:00Z",
          }
          decisions.set(id, saved)
          return jsonResponse(201, saved)
        }
        if (leaf === "workflow" && method === "GET") {
          if (!selected.has(id)) return notFound("Role is not selected.")
          return jsonResponse(200, {
            ...workflowFixture(id),
            stage: options.workflowStages?.[id] ?? "selected",
          })
        }
      }

      const checksMatch = path.match(
        /^\/api\/v1\/opportunities\/([^/]+)\/checks$/
      )
      if (checksMatch?.[1] !== undefined && method === "POST") {
        if (!selected.has(decodeURIComponent(checksMatch[1])))
          return jsonResponse(422, {
            error: { message: "Role is not selected." },
          })
        return jsonResponse(201, { status: "checking" })
      }
      return notFound("Not found.")
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderJobs() {
  return render(
    <SessionProvider>
      <GroupedJobs />
    </SessionProvider>
  )
}

function cardFor(title: string): HTMLElement {
  const card = screen.getByRole("link", { name: title }).closest("li")
  if (card === null) throw new Error(`No card for ${title}.`)
  return card as HTMLElement
}

function switchTab(label: string): void {
  const tab = screen.getByRole("tab", {
    name: new RegExp(`^${label} \\(`),
  })
  fireEvent.click(tab)
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe("GroupedJobs", () => {
  it("shows the saved selection after a fresh mount", async () => {
    stubGroupedFetch()
    const first = renderJobs()
    await screen.findByRole("button", { name: "Check chosen jobs (1)" })
    expect(
      within(cardFor("Backend Engineer")).getByText(
        "Selected — decision rev 1."
      )
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Select Backend Engineer" })
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Shortlist Backend Engineer" })
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Pass on Backend Engineer" })
    ).toBeDefined()

    first.unmount()
    renderJobs()
    await screen.findByRole("button", { name: "Check chosen jobs (1)" })
    expect(
      within(cardFor("Backend Engineer")).getByText(
        "Selected — decision rev 1."
      )
    ).toBeDefined()
  })

  it("dismisses a chosen job and updates the sticky count without starting a check", async () => {
    const { calls } = stubGroupedFetch()
    renderJobs()
    await screen.findByRole("button", { name: "Check chosen jobs (1)" })

    fireEvent.click(
      screen.getByRole("button", {
        name: "Pass on Backend Engineer",
      })
    )
    await screen.findByRole("button", { name: "Check chosen jobs (0)" })
    expect(
      within(cardFor("Backend Engineer")).getByText(
        "Passed — decision rev 2."
      )
    ).toBeDefined()
    expect(
      screen.getByRole("button", {
        name: "Select Backend Engineer",
      })
    ).toBeDefined()
    const post = calls.find(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-rec/decision"
    )
    const body = JSON.parse(post?.body ?? "{}") as Record<string, unknown>
    expect(body["decision"]).toBe("dismissed")
    expect(body["expectedDecisionRevision"]).toBe(1)
    expect(calls.filter((call) => call.url.includes("/checks"))).toEqual([])
  })

  it("offers the decision triple during review but not after a completed send", async () => {
    stubGroupedFetch({ workflowStages: { "job-rec": "reviewing" } })
    const first = renderJobs()
    await screen.findByRole("button", { name: "Check chosen jobs (1)" })
    fireEvent.click(
      screen.getByRole("button", {
        name: "Pass on Backend Engineer",
      })
    )
    await screen.findByRole("button", { name: "Check chosen jobs (0)" })

    first.unmount()
    stubGroupedFetch({ workflowStages: { "job-rec": "sent" } })
    renderJobs()
    await screen.findByRole("button", { name: "Check chosen jobs (1)" })
    expect(
      screen.queryByRole("button", {
        name: "Select Backend Engineer",
      })
    ).toBeNull()
    expect(
      screen.queryByRole("button", {
        name: "Shortlist Backend Engineer",
      })
    ).toBeNull()
    expect(
      screen.queryByRole("button", {
        name: "Pass on Backend Engineer",
      })
    ).toBeNull()
    expect(
      within(cardFor("Backend Engineer")).getByText(
        "Selected — decision rev 1."
      )
    ).toBeDefined()
  })

  it("groups listings by saved Jev group, including exceptional Unknown", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    // Tabs carry total and chosen counts; only Backend Engineer is chosen.
    screen.getByRole("tab", { name: "Recommended (1 · 1 chosen)" })
    screen.getByRole("tab", { name: "Might recommend (1 · 0 chosen)" })
    screen.getByRole("tab", { name: "Might not recommend (1 · 0 chosen)" })
    screen.getByRole("tab", { name: "Not recommended (1 · 0 chosen)" })
    screen.getByRole("tab", {
      name: "Unknown — exceptional, needs a basis (1 · 0 chosen)",
    })
    screen.getByRole("tab", { name: "Not yet classified (1 · 0 chosen)" })

    switchTab("Might recommend")
    await screen.findByText("Might recommend (1)")
    switchTab("Might not recommend")
    await screen.findByText("Might not recommend (1)")
    switchTab("Not recommended")
    await screen.findByText("Not recommended (1)")
    switchTab("Unknown — exceptional, needs a basis")
    await screen.findByText("Unknown — exceptional, needs a basis (1)")
    switchTab("Not yet classified")
    await screen.findByText("Not yet classified (1)")

    switchTab("Unknown — exceptional, needs a basis")
    const unknownCard = cardFor("Mystery Role")
    fireEvent.click(
      within(unknownCard).getByRole("button", { name: "Why this job" })
    )
    await within(unknownCard).findByText(
      "Jev could not place this role: Evidence conflicted across sources."
    )

    // Run findings covered two roles; the rest resolved per-role.
    switchTab("Might recommend")
    await screen.findByText(
      "Latest saved finding from outside the tracked run."
    )
    switchTab("Might not recommend")
    await screen.findByText(
      "Latest saved finding from outside the tracked run."
    )
    switchTab("Not recommended")
    await screen.findByText(
      "Latest saved finding from outside the tracked run."
    )
    // B3 stage pill still shows for the chosen role.
    switchTab("Recommended")
    expect(
      within(cardFor("Backend Engineer")).getByText("Selected")
    ).toBeDefined()
    // Unclassified roles get no explanation toggle.
    switchTab("Not yet classified")
    expect(
      within(cardFor("Fresh Listing")).queryByRole("button", {
        name: "Why this job",
      })
    ).toBeNull()
  })

  it("keeps choices visible while switching views", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Might not recommend")
    fireEvent.click(
      screen.getByRole("button", { name: "Select Weekend Project" })
    )
    await screen.findByText("Selected — decision rev 1.")
    screen.getByRole("tab", { name: "Might not recommend (1 · 1 chosen)" })

    switchTab("Recommended")
    await screen.findByText("Recommended (1)")
    switchTab("Might not recommend")
    await screen.findByText("Selected — decision rev 1.")
  })

  it("leads cards with the principal saved reason, conflict and unknown basis", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    const card = cardFor("Backend Engineer")
    within(card).getByText("Strength: Loves “quoted” labelling ✓")
    within(card).getByText("Conflicting consideration: Hybrid expectation")

    switchTab("Unknown — exceptional, needs a basis")
    const unknownCard = cardFor("Mystery Role")
    await within(unknownCard).findByText(
      "Jev could not place this role: Evidence conflicted across sources."
    )
  })

  it("renders saved reason text verbatim with signal, conflict and missing info", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    const card = cardFor("Backend Engineer")
    fireEvent.click(within(card).getByRole("button", { name: "Why this job" }))
    await within(card).findByText("Strength: Loves “quoted” labelling ✓")
    within(card).getByText(
      (_, element) =>
        element?.textContent === "Detail with “quotes”, emoji ✓, and\nnewline."
    )
    within(card).getByText("Jev support signal: 0.85")
    within(card).getByText("Concern: On-call load")
    within(card).getByText(
      "Conflicting consideration: Hybrid expectation — Two days onsite expected."
    )
    within(card).getByText(
      "Missing information: Salary band — No band published."
    )
    within(card).getByText(
      "Support signals record Jev's assessment strength; they are not verified correctness."
    )
  })

  it("opens explanations with zero model calls and zero new fetches", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
    const before = calls.length

    for (const label of [
      "Recommended",
      "Might recommend",
      "Might not recommend",
      "Not recommended",
      "Unknown — exceptional, needs a basis",
      "Not yet classified",
    ]) {
      switchTab(label)
      const buttons = screen.queryAllByRole("button", {
        name: "Why this job",
      })
      for (const button of buttons) {
        fireEvent.click(button)
      }
      if (buttons.length > 0) {
        await screen.findByText(
          "Support signals record Jev's assessment strength; they are not verified correctness."
        )
      }
    }
    const after = calls.slice(before)
    expect(after).toEqual([])
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("posts a guarded select decision and never starts a check", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Might not recommend")
    fireEvent.click(
      screen.getByRole("button", {
        name: "Select Weekend Project",
      })
    )
    await screen.findByText("Selected — decision rev 1.")

    const decisionGets = calls.filter(
      (call) =>
        call.method === "GET" &&
        call.url === "/api/v1/opportunities/job-prob/decision"
    )
    const decisionPosts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-prob/decision"
    )
    expect(decisionGets).toHaveLength(1)
    expect(decisionPosts).toHaveLength(1)
    expect(calls.indexOf(decisionGets[0]!)).toBeLessThan(
      calls.indexOf(decisionPosts[0]!)
    )
    const payload = JSON.parse(decisionPosts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["decision"]).toBe("selected")
    expect(payload["expectedOpportunityRevision"]).toBe(2)
    expect(payload["expectedDecisionRevision"]).toBe(0)
    expect(typeof payload["requestKey"]).toBe("string")
    expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
    expect(calls.filter((call) => call.url.includes("/checks"))).toEqual([])
  })

  it("sends the existing decision revision as the guard", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch({
      existingDecisions: {
        "job-could": {
          id: "decision-4",
          opportunityId: "job-could",
          decision: "acknowledged",
          revision: 4,
          opportunityRevision: 2,
          auditId: "audit-4",
          createdAt: "2026-09-18T10:00:00Z",
        },
      },
    })
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Might recommend")
    fireEvent.click(
      screen.getByRole("button", {
        name: "Select Support Engineer",
      })
    )
    await screen.findByText("Selected — decision rev 5.")

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-could/decision"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["expectedDecisionRevision"]).toBe(4)
    expect(calls.filter((call) => call.url.includes("/checks"))).toEqual([])
  })

  it("shows the loaded shortlist label before any click", async () => {
    stubGroupedFetch({
      existingDecisions: {
        "job-could": {
          id: "decision-4",
          opportunityId: "job-could",
          decision: "acknowledged",
          revision: 4,
          opportunityRevision: 2,
          auditId: "audit-4",
          createdAt: "2026-09-18T10:00:00Z",
        },
      },
    })
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Might recommend")
    expect(
      within(cardFor("Support Engineer")).getByText(
        "Shortlisted — decision rev 4."
      )
    ).toBeDefined()
  })

  it("shortlists a job without touching the chosen count or starting a check", async () => {
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByRole("button", { name: "Check chosen jobs (1)" })
    switchTab("Might not recommend")
    fireEvent.click(
      screen.getByRole("button", { name: "Shortlist Weekend Project" })
    )
    await screen.findByText("Shortlisted — decision rev 1.")
    expect(
      screen.getByRole("button", { name: "Check chosen jobs (1)" })
    ).toBeDefined()
    const post = calls.find(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-prob/decision"
    )
    const body = JSON.parse(post?.body ?? "{}") as Record<string, unknown>
    expect(body["decision"]).toBe("acknowledged")
    expect(body["expectedDecisionRevision"]).toBe(0)
    expect(calls.filter((call) => call.url.includes("/checks"))).toEqual([])
  })

  it("shows staleness per listing", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    expect(screen.getByText("Current")).toBeDefined()
    switchTab("Might recommend")
    await screen.findByText("Current")
    switchTab("Might not recommend")
    await screen.findByText("Stale — opportunity_revised")
    switchTab("Not recommended")
    await screen.findByText("Current")
    switchTab("Unknown — exceptional, needs a basis")
    await screen.findByText("Stale — brief_changed,catalog_changed")
  })

  it("shows brief and catalog versions", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText(
      "Search brief: profile v3 · rubric rubric-1 · catalog cat-7"
    )
    for (const label of [
      "Recommended",
      "Might recommend",
      "Might not recommend",
      "Not recommended",
      "Unknown — exceptional, needs a basis",
    ]) {
      switchTab(label)
      await screen.findByText(
        "Brief profile v3 · rubric rubric-1 · catalog cat-7"
      )
    }
    await screen.findByText("Tracked research run: run-7")
  })

  it("reads the saved context once per surface", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    await screen.findByText(
      "Search brief: profile v3 · rubric rubric-1 · catalog cat-7"
    )
    expect(
      calls.filter(
        (call) => call.method === "GET" && call.url === "/api/v1/preferences"
      )
    ).toHaveLength(1)
    expect(
      calls.filter(
        (call) => call.method === "GET" && call.url === "/api/v1/research/brief"
      )
    ).toHaveLength(1)
  })

  it("retains and labels stale findings after the brief moves on", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch({
      preferences: { ...groupedPreferencesFixture, version: 5 },
      brief: {
        ...briefFixture,
        profileVersion: 4,
        rubricVersion: "rubric-2",
        catalogVersion: "cat-8",
      },
    })
    renderJobs()

    await screen.findByText("Recommended (1)")
    // Every saved group still renders: nothing is erased by the version move.
    for (const label of [
      "Might recommend",
      "Might not recommend",
      "Not recommended",
      "Unknown — exceptional, needs a basis",
      "Not yet classified",
    ]) {
      switchTab(label)
      await screen.findByText(`${label} (1)`)
    }
    // Stale findings keep their saved basis labels.
    switchTab("Might not recommend")
    await screen.findByText("Stale — opportunity_revised")
    switchTab("Unknown — exceptional, needs a basis")
    await screen.findByText("Stale — brief_changed,catalog_changed")
    switchTab("Recommended")
    await screen.findByText(
      "Brief profile v3 · rubric rubric-1 · catalog cat-7"
    )
    // The header shows the current identity plus the stale-brief note.
    await screen.findByText(
      "Search brief: profile v5 · rubric rubric-2 · catalog cat-8"
    )
    await screen.findByText(
      "The saved brief is behind profile v5; findings below keep their saved basis until the next Find jobs run."
    )
  })

  it("shows an honest empty state when no brief exists yet", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch({ brief: null })
    renderJobs()

    await screen.findByText(
      "No saved search brief yet — the first Find jobs run authors one from profile v3."
    )
    await screen.findByText("Recommended (1)")
    switchTab("Unknown — exceptional, needs a basis")
    await screen.findByText("Unknown — exceptional, needs a basis (1)")
  })

  it("keeps saved findings visible when the context read fails, with a retry", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch({ failBriefAttempts: 1 })
    renderJobs()

    await screen.findByText(/Search brief versions unavailable: /)
    await screen.findByText("Recommended (1)")
    fireEvent.click(
      await screen.findByRole("button", { name: "Retry saved context" })
    )
    await screen.findByText(
      "Search brief: profile v3 · rubric rubric-1 · catalog cat-7"
    )
  })

  it("falls back to per-opportunity findings without a tracked run", async () => {
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Unknown — exceptional, needs a basis")
    await screen.findByText("Unknown — exceptional, needs a basis (1)")
    expect(screen.getByText(/No tracked research run/)).toBeDefined()
    expect(
      calls.some(
        (call) =>
          call.method === "GET" &&
          call.url === "/api/v1/opportunities/job-rec/finding"
      )
    ).toBe(true)
    expect(
      calls.filter((call) => call.url.includes("/research/runs/"))
    ).toEqual([])
  })

  it("shows evidence links and source refs for new-source discovery", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    const card = cardFor("Backend Engineer")
    await within(card).findByText("Evidence (1)")
    const evidenceLink = within(card).getByRole("link", {
      name: "cap-1",
    }) as HTMLAnchorElement
    expect(evidenceLink.getAttribute("href")).toBe(
      "https://example.com/posting"
    )
    within(card).getByText("a".repeat(64))
    within(card).getByText(
      (_, element) =>
        element?.tagName === "P" &&
        element.textContent ===
          "Source: career-site (rev 2026-09-20) · Open source"
    )
    const sourceLink = within(card).getByRole("link", {
      name: "Open source",
    }) as HTMLAnchorElement
    expect(sourceLink.getAttribute("href")).toBe("https://example.com/posting")

    // Unresolvable captures stay visible as bare ids.
    switchTab("Might recommend")
    const couldCard = cardFor("Support Engineer")
    await within(couldCard).findByText("Evidence (1)")
    expect(
      within(couldCard).queryByRole("link", { name: "cap-missing" })
    ).toBeNull()
    within(couldCard).getByText("cap-missing")
  })

  it("wires the fixed Check chosen jobs action to chosen roles only", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch()
    renderJobs()

    await screen.findByText("Recommended (1)")
    // job-rec is the only preselected role (saved workflow in the stub).
    expect(
      screen.getByRole("button", { name: "Check chosen jobs (1)" })
    ).toBeDefined()
    expect(calls.filter((call) => call.url.includes("/checks"))).toEqual([])

    // Selecting another role updates the count but starts no check.
    switchTab("Might not recommend")
    fireEvent.click(
      screen.getByRole("button", {
        name: "Select Weekend Project",
      })
    )
    await screen.findByText("Selected — decision rev 1.")
    await screen.findByRole("button", { name: "Check chosen jobs (2)" })
    expect(
      calls.filter(
        (call) => call.method === "POST" && call.url.includes("/checks")
      )
    ).toEqual([])

    // An explicit click starts checks only for the two chosen roles.
    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    )
    await screen.findAllByText(/Check pending/)
    const posts = calls.filter(
      (call) => call.method === "POST" && call.url.includes("/checks")
    )
    expect(new Set(posts.map((post) => post.url))).toEqual(
      new Set([
        "/api/v1/opportunities/job-rec/checks",
        "/api/v1/opportunities/job-prob/checks",
      ])
    )
    for (const post of posts) {
      const payload = JSON.parse(post.body ?? "{}") as Record<string, unknown>
      expect(payload["expectedOpportunityRevision"]).toBe(2)
      expect(payload["expectedWorkflowRevision"]).toBe(1)
      expect(typeof payload["requestKey"]).toBe("string")
      expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
    }

    // Each chosen role links to its own check page.
    const link = screen.getByRole("link", {
      name: "Open check page for Backend Engineer",
    }) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/jobs/job-rec/check")
    const probLink = screen.getByRole("link", {
      name: "Open check page for Weekend Project",
    }) as HTMLAnchorElement
    expect(probLink.getAttribute("href")).toBe("#/jobs/job-prob/check")
  })

  it("surfaces decision conflicts without starting a check", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-7")
    const { calls } = stubGroupedFetch({ decisionConflict: true })
    renderJobs()

    await screen.findByText("Recommended (1)")
    switchTab("Not recommended")
    fireEvent.click(
      screen.getByRole("button", {
        name: "Select Night Shift Ops",
      })
    )
    const card = cardFor("Night Shift Ops")
    await within(card).findByText("Decision revision conflict; refresh first.")
    expect(
      calls.filter(
        (call) => call.method === "POST" && call.url.includes("/checks")
      )
    ).toEqual([])
  })
})
