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
  Preferences,
  ResearchActivityEvent,
  ResearchCaptureView,
  ResearchReportView,
  ResearchRunView,
  Round,
  SearchBriefView,
  SteeringMessage,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { SavedGoalsProvider } from "@/components/shared/saved-goals"
import { registerGoalEditorOpener } from "@/components/shared/goal-editor"
import { DiscoverySection } from "@/features/discovery/discovery-section"
import type { RunHistoryItem } from "@/features/discovery/run-history"
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

const allowanceFixture = {
  timeMs: 900000,
  maxActions: 60,
  maxJev: 12,
  maxTurns: 8,
  maxConcurrent: 2,
}

const usageFixture = {
  enforced: allowanceFixture,
  reserved: { actions: 2, jev: 1, turns: 1 },
  observed: { actions: 5, jev: 3, turns: 2, bytes: 1234 },
  unknown: false,
}

function runFixture(
  runId: string,
  state: ResearchRunView["state"]
): ResearchRunView {
  return {
    runId,
    state,
    briefVersion: { profileVersion: 3, rubricVersion: "rubric-1" },
    allowance: allowanceFixture,
    usage: usageFixture,
    investigations: [{ id: "inv-1", intent: "collect", status: "active" }],
    savedIds: ["job-1"],
    unresolvedCount: 1,
  }
}

const activityFixture: ResearchActivityEvent[] = [
  {
    eventId: "e1",
    at: "2026-09-24T10:00:00Z",
    kind: "run.dispatched",
    phase: "collect",
    summary: "Dispatched collect (collect:example)",
  },
  {
    eventId: "e2",
    at: "2026-09-24T10:01:00Z",
    kind: "capture",
    phase: "collect",
    summary: "Captured 1234 bytes (complete: true)",
    refs: { captureId: "cap-1" },
  },
  {
    eventId: "e3",
    at: "2026-09-24T10:02:00Z",
    kind: "run.uncertain",
    phase: "collect",
    summary: "Outcome uncertain (timeout); reconcile before retrying",
  },
  {
    eventId: "e4",
    at: "2026-09-24T10:03:00Z",
    kind: "observation",
    phase: "classify",
    summary: "Observed obs-1 (ok)",
    refs: { assessmentId: "asmt-1" },
  },
]

const captureFixture: ResearchCaptureView = {
  captureId: "cap-1",
  observedUrl: "https://example.com/careers",
  retrievedAt: "2026-09-24T10:01:00Z",
  status: "ok",
  mediaType: "text/html",
  contentHash: "abc123",
  extent: { bytes: 1234, complete: true },
}

const steerAckFixture: SteeringMessage = {
  messageId: "msg-1",
  revision: 2,
  body: "Focus on remote roles.",
  ack: "acknowledged",
}

function reportFixture(runId: string): ResearchReportView {
  return {
    runId,
    outcomes: ['opportunity "Backend Engineer" (job-1 rev 2)'],
    searched: ["collect x 4"],
    reused: [],
    uncertainty: ["attempt a1: capture incomplete"],
    budget: { observed: usageFixture, unknown: false },
    nextWork: ["Keep collecting"],
  }
}

const discoveryPreferencesFixture: Preferences = {
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

function discoveryBriefFixture(
  overrides: Partial<SearchBriefView> = {}
): SearchBriefView {
  return {
    profileVersion: 3,
    rubricVersion: "criteria-v3-abc123def456",
    rubricSource: "preferences_versions:current:v3",
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

function historyItem(runId: string, state: string): RunHistoryItem {
  return {
    runId,
    requestKey: `key-${runId}`,
    intent: "find-jobs",
    outcome: "research_run",
    state,
    stopReason: "",
    createdAt: "2026-09-24T10:00:00Z",
    updatedAt: "2026-09-24T11:00:00Z",
  }
}

interface StubOptions {
  runState?: ResearchRunView["state"]
  preferences?: Preferences
  // null serves a 404 (no saved brief yet); "error" serves a 500.
  brief?: SearchBriefView | null | "error"
  round?: Round
  // "missing" serves a 404 (server predates D3); "error" serves a 500.
  runHistory?: RunHistoryItem[] | "missing" | "error"
}

function stubResearchFetch(options: StubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let commissions = 0
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
      if (path === "/api/v1/preferences" && method === "GET")
        return jsonResponse(200, options.preferences ?? discoveryPreferencesFixture)
      if (path === "/api/v1/research/brief" && method === "GET") {
        const brief =
          options.brief === undefined ? discoveryBriefFixture() : options.brief
        if (brief === "error")
          return jsonResponse(500, { error: { message: "Brief down." } })
        if (brief === null)
          return jsonResponse(404, { error: { message: "No brief." } })
        return jsonResponse(200, brief)
      }
      if (path === "/api/v1/research/runs" && method === "GET") {
        const history = options.runHistory ?? "missing"
        if (history === "missing")
          return jsonResponse(404, { error: { message: "No run history." } })
        if (history === "error")
          return jsonResponse(500, { error: { message: "History down." } })
        return jsonResponse(200, { items: history })
      }
      if (path === "/api/v1/research/runs" && method === "POST") {
        commissions += 1
        return jsonResponse(
          201,
          runFixture(`run-${commissions}`, options.runState ?? "running")
        )
      }
      const runMatch = path.match(/^\/api\/v1\/research\/runs\/([^/]+)(\/.*)?$/)
      if (runMatch?.[1] !== undefined) {
        const id = decodeURIComponent(runMatch[1])
        const suffix = runMatch[2] ?? ""
        if (suffix === "")
          return jsonResponse(200, runFixture(id, options.runState ?? "running"))
        if (suffix === "/steer" && method === "POST")
          return jsonResponse(200, steerAckFixture)
        if (suffix === "/activity")
          return jsonResponse(200, { events: activityFixture })
        if (suffix === "/report") return jsonResponse(200, reportFixture(id))
      }
      const captureMatch = path.match(/^\/api\/v1\/research\/captures\/([^/]+)$/)
      if (captureMatch?.[1] !== undefined) return jsonResponse(200, captureFixture)
      const roundMatch = path.match(/^\/api\/v1\/rounds\/([^/]+)\/(stop|resume)$/)
      if (roundMatch !== null && method === "POST")
        return jsonResponse(200, { ok: true })
      const roundRead = path.match(/^\/api\/v1\/rounds\/([^/]+)$/)
      if (roundRead?.[1] !== undefined && method === "GET" && options.round !== undefined)
        return jsonResponse(200, options.round)
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderSection(runId?: string | null) {
  return render(
    <SessionProvider>
      <SavedGoalsProvider>
        <DiscoverySection runId={runId ?? null} />
      </SavedGoalsProvider>
    </SessionProvider>
  )
}

async function startRun(
  note = "",
  doneText = "Running — research is underway."
) {
  if (note !== "") {
    const noteInput = await screen.findByLabelText(
      "Extra note for this run only (optional)"
    )
    fireEvent.change(noteInput, { target: { value: note } })
  }
  fireEvent.click(await screen.findByRole("button", { name: "Find jobs" }))
  await screen.findByText(doneText)
}

beforeEach(() => {
  window.localStorage.clear()
  window.location.hash = ""
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  window.location.hash = ""
})

describe("DiscoverySection", () => {
  it("commissions nothing on mount", async () => {
    const { calls } = stubResearchFetch()
    renderSection()

    await screen.findByRole("button", { name: "Find jobs" })
    await waitFor(() =>
      expect(
        calls.some((call) => call.url === "/api/v1/auth/session")
      ).toBe(true)
    )
    await waitFor(() =>
      expect(
        calls.some((call) => call.url === "/api/v1/research/brief")
      ).toBe(true)
    )
    expect(calls.length).toBeGreaterThan(0)
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("restores the linked run with reads only", async () => {
    window.location.hash = "#/search?run=run-9"
    const { calls } = stubResearchFetch()
    renderSection()

    await screen.findByText("Running — research is underway.")
    expect(
      calls.some((call) =>
        call.url.startsWith("/api/v1/research/runs/run-9")
      )
    ).toBe(true)
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("ignores the retired browser pointer and restores from the server", async () => {
    // A stale pointer from before G2 must not shadow server recovery.
    window.localStorage.setItem("jobseek.research-run-id", "run-stale")
    const { calls } = stubResearchFetch({ runHistory: [historyItem("run-9", "paused")] })
    renderSection()

    await screen.findByText("Running — research is underway.")
    expect(
      calls.some((call) =>
        call.url.startsWith("/api/v1/research/runs/run-9")
      )
    ).toBe(true)
    expect(
      calls.some((call) => call.url.includes("run-stale"))
    ).toBe(false)
  })

  it("restores the newest resumable server run without a link", async () => {
    const { calls } = stubResearchFetch({
      runHistory: [
        historyItem("run-new-done", "completed"),
        historyItem("run-old-paused", "paused"),
      ],
    })
    renderSection()

    await screen.findByText("Running — research is underway.")
    expect(
      calls.some((call) =>
        call.url.startsWith("/api/v1/research/runs/run-old-paused")
      )
    ).toBe(true)
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
    // The settled run stays reachable from history.
    expect(
      await screen.findByRole("link", { name: /run-new-done · completed/ })
    ).toBeDefined()
  })

  it("restores an unlisted run explicitly from its history link", async () => {
    stubResearchFetch({ runHistory: [historyItem("run-x", "mystery")] })
    renderSection()

    // Unknown states never auto-restore: the idle panel stays.
    await screen.findByRole("button", { name: "Find jobs" })
    fireEvent.click(
      await screen.findByRole("link", { name: /run-x · mystery/ })
    )
    await screen.findByText("Running — research is underway.")
  })

  it("commissions with an explicit idempotent payload", async () => {
    const { calls } = stubResearchFetch()
    renderSection()
    await startRun("remote backend roles")

    const posts = calls.filter(
      (call) =>
        call.method === "POST" && call.url === "/api/v1/research/runs"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["briefText"]).toBe("remote backend roles")
    expect(payload["allowance"]).toEqual(allowanceFixture)
    expect(typeof payload["idempotencyKey"]).toBe("string")
    expect((payload["idempotencyKey"] as string).length).toBeGreaterThan(0)
    // The run link is the recovery pointer; no browser pointer is written.
    expect(window.location.hash).toBe("#/search?run=run-1")
    expect(window.localStorage.getItem("jobseek.research-run-id")).toBeNull()
  })

  it("stops a running run through round control, then refreshes", async () => {
    window.location.hash = "#/search?run=run-9"
    const { calls } = stubResearchFetch()
    renderSection()
    await screen.findByText("Running — research is underway.")

    fireEvent.click(await screen.findByRole("button", { name: "Stop" }))
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "POST" && call.url === "/api/v1/rounds/run-9/stop"
        )
      ).toBe(true)
    )
    const runReads = calls.filter(
      (call) =>
        call.method === "GET" && call.url === "/api/v1/research/runs/run-9"
    )
    expect(runReads.length).toBeGreaterThanOrEqual(2)
    // Resume is state-invalid while running and says so.
    expect(
      (await screen.findByRole("button", { name: "Resume" }) as HTMLButtonElement)
        .disabled
    ).toBe(true)
    expect(
      await screen.findByText(/Resume applies to a paused run/)
    ).toBeDefined()
  })

  it("resumes a paused run through round control", async () => {
    window.location.hash = "#/search?run=run-9"
    const { calls } = stubResearchFetch({ runState: "paused" })
    renderSection()
    await screen.findByText("Paused — resume continues with the remaining allowance.")

    expect(
      (await screen.findByRole("button", { name: "Resume" }) as HTMLButtonElement)
        .disabled
    ).toBe(false)
    fireEvent.click(screen.getByRole("button", { name: "Resume" }))
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "POST" &&
            call.url === "/api/v1/rounds/run-9/resume"
        )
      ).toBe(true)
    )
    expect(
      (screen.getByRole("button", { name: "Stop" }) as HTMLButtonElement)
        .disabled
    ).toBe(true)
  })

  it("steers with a body payload and shows the server ack", async () => {
    const { calls } = stubResearchFetch()
    renderSection()
    await startRun()

    const steerInput = await screen.findByLabelText("Steer the run")
    fireEvent.change(steerInput, { target: { value: "Focus on remote roles." } })
    fireEvent.click(
      await screen.findByRole("button", { name: "Send steering message" })
    )
    await screen.findByText("Message acknowledged.")
    const steerPosts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/research/runs/run-1/steer"
    )
    expect(steerPosts).toHaveLength(1)
    const payload = JSON.parse(steerPosts[0]?.body ?? "{}") as Record<
      string,
      unknown
    >
    expect(payload["body"]).toBe("Focus on remote roles.")
    expect(typeof payload["idempotencyKey"]).toBe("string")
  })

  it("renders activity with blockers, source links and a Jev split", async () => {
    stubResearchFetch()
    renderSection()
    await startRun()

    fireEvent.click(
      await screen.findByRole("button", { name: /Find jobs · collection/ })
    )
    await screen.findByText(
      "Outcome uncertain (timeout); reconcile before retrying"
    )
    const sourceLink = (await screen.findByRole("link", {
      name: "Captured 1234 bytes (complete: true)",
    })) as HTMLAnchorElement
    expect(sourceLink.getAttribute("href")).toBe("https://example.com/careers")

    fireEvent.click(
      await screen.findByRole("button", { name: /Find jobs · classification/ })
    )
    await screen.findByText("Observed obs-1 (ok) (assessment asmt-1)")
  })

  it("finds more jobs as a fresh pass with a new idempotency key", async () => {
    const { calls } = stubResearchFetch({ runState: "completed" })
    renderSection()
    await startRun("remote backend roles", "Completed — outcomes and report are saved.")

    const findMore = (await screen.findByRole("button", {
      name: "Find more jobs",
    })) as HTMLButtonElement
    expect(findMore.disabled).toBe(false)
    fireEvent.click(findMore)
    await waitFor(() =>
      expect(
        calls.filter(
          (call) =>
            call.method === "POST" && call.url === "/api/v1/research/runs"
        )
      ).toHaveLength(2)
    )
    const posts = calls.filter(
      (call) => call.method === "POST" && call.url === "/api/v1/research/runs"
    )
    const first = JSON.parse(posts[0]?.body ?? "{}") as Record<string, unknown>
    const second = JSON.parse(posts[1]?.body ?? "{}") as Record<string, unknown>
    expect(second["idempotencyKey"]).not.toBe(first["idempotencyKey"])
    expect(second).not.toHaveProperty("briefText")
    expect(window.location.hash).toBe("#/search?run=run-2")
  })

  it("disables Find more while a run is active and names the reason", async () => {
    stubResearchFetch()
    renderSection()
    await startRun()

    expect(
      (await screen.findByRole("button", {
        name: "Find more jobs",
      }) as HTMLButtonElement).disabled
    ).toBe(true)
    expect(
      await screen.findByText("Find more starts after this run settles.")
    ).toBeDefined()
  })

  it("continues a settled run to Select jobs beside Find more and Edit goals", async () => {
    stubResearchFetch({ runState: "completed" })
    renderSection()
    await startRun("", "Completed — outcomes and report are saved.")

    const selectJobs = (await screen.findByRole("link", {
      name: "Select jobs",
    })) as HTMLAnchorElement
    expect(selectJobs.getAttribute("href")).toBe("#/jobs")
    expect(
      (screen.getByRole("button", { name: "Find more jobs" }) as HTMLButtonElement)
        .disabled
    ).toBe(false)
    expect(
      (screen.getByRole("button", { name: "Edit goals" }) as HTMLButtonElement)
        .disabled
    ).toBe(false)
    expect(
      (screen.getByRole("button", { name: "Stop" }) as HTMLButtonElement)
        .disabled
    ).toBe(true)
    expect(
      await screen.findByText(/already completed; there is nothing to stop/)
    ).toBeDefined()
  })

  it("opens Change goals through the goal-editor registry", async () => {
    const opened: string[] = []
    const unregister = registerGoalEditorOpener((reason) => {
      opened.push(reason)
    })
    try {
      stubResearchFetch()
      renderSection()
      fireEvent.click(
        await screen.findByRole("button", { name: "Change goals" })
      )
      expect(opened).toEqual(["discovery:change-goals"])
      // The registry open navigates nowhere.
      expect(window.location.hash).toBe("")
    } finally {
      unregister()
    }
  })

  it("loads the report for terminal runs", async () => {
    stubResearchFetch({ runState: "completed" })
    renderSection()

    fireEvent.click(await screen.findByRole("button", { name: "Find jobs" }))
    await screen.findByText("Completed — outcomes and report are saved.")
    await screen.findByText('opportunity "Backend Engineer" (job-1 rev 2)')
    await screen.findByText("Searched 1 source")
    await screen.findByText("attempt a1: capture incomplete")
  })

  it("foregrounds the saved brief as the primary search basis", async () => {
    stubResearchFetch()
    renderSection()

    await screen.findByText("Saved search brief")
    await screen.findByText("Profile v3 · criteria-v3-abc123def456")
    await screen.findByText(/Berlin · remote ok/)
    await screen.findByText(/Searches with profile v3/)
    const findJobs = (await screen.findByRole("button", {
      name: "Find jobs",
    })) as HTMLButtonElement
    expect(findJobs.disabled).toBe(false)
    // The generic prompt is demoted to a clearly-labeled secondary input.
    await screen.findByLabelText("Extra note for this run only (optional)")
    expect(
      screen.queryByLabelText("Recruitment intent (optional)")
    ).toBeNull()
  })

  it("reads the saved context once per surface and commissions nothing", async () => {
    const { calls } = stubResearchFetch()
    renderSection()

    await screen.findByText("Saved search brief")
    await screen.findByText(/Searches with profile v3/)
    await waitFor(() =>
      expect(
        calls.some((call) => call.url === "/api/v1/research/brief")
      ).toBe(true)
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
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("lets the first Find jobs run author the brief when none exists yet", async () => {
    const { calls } = stubResearchFetch({ brief: null })
    renderSection()

    await screen.findByText("No saved search brief yet")
    await screen.findByText(
      "Searches with profile v3 — the first run authors the search brief."
    )
    const findJobs = (await screen.findByRole("button", {
      name: "Find jobs",
    })) as HTMLButtonElement
    expect(findJobs.disabled).toBe(false)
    fireEvent.click(findJobs)
    await screen.findByText("Running — research is underway.")
    const posts = calls.filter(
      (call) => call.method === "POST" && call.url === "/api/v1/research/runs"
    )
    expect(posts).toHaveLength(1)
    expect(JSON.parse(posts[0]?.body ?? "{}")).not.toHaveProperty("briefText")
  })

  it("blocks Find jobs when the brief is stale", async () => {
    stubResearchFetch({
      preferences: { ...discoveryPreferencesFixture, version: 4 },
      brief: discoveryBriefFixture({ profileVersion: 3 }),
    })
    renderSection()

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain(
      "The saved brief is behind profile v4."
    )
    await screen.findByText(
      "The search brief is behind profile version 4. Reload the saved context before finding jobs."
    )
    const findJobs = (await screen.findByRole("button", {
      name: "Find jobs",
    })) as HTMLButtonElement
    expect(findJobs.disabled).toBe(true)
    fireEvent.click(findJobs)
    await screen.findByRole("button", { name: "Refresh saved brief" })
  })

  it("blocks Find jobs while a correction is still saving", async () => {
    window.localStorage.setItem(
      "jobseek.owner-context-pending",
      JSON.stringify({
        roundId: "round-9",
        requestKey: "profile-correction-v3-deadbeef",
        baseVersion: 3,
        targetText: "Prefer Amsterdam.",
      })
    )
    const { calls } = stubResearchFetch({
      round: {
        id: "round-9",
        requestKey: "profile-correction-v3-deadbeef",
        state: "running",
        profileVersion: 3,
        originalProfileVersion: 3,
      } as Round,
    })
    renderSection()

    await screen.findByText(/Saving your change \(running\)…/)
    const findJobs = (await screen.findByRole("button", {
      name: "Find jobs",
    })) as HTMLButtonElement
    expect(findJobs.disabled).toBe(true)
    fireEvent.click(findJobs)
    expect(
      calls.filter(
        (call) => call.method === "POST" && call.url === "/api/v1/research/runs"
      )
    ).toEqual([])
  })

  it("blocks Find jobs when the brief read fails", async () => {
    stubResearchFetch({ brief: "error" })
    renderSection()

    await screen.findByText("Could not load the saved search brief")
    const findJobs = (await screen.findByRole("button", {
      name: "Find jobs",
    })) as HTMLButtonElement
    expect(findJobs.disabled).toBe(true)
  })

  it("omits briefText when the one-off note is empty", async () => {
    const { calls } = stubResearchFetch()
    renderSection()
    await startRun()

    const posts = calls.filter(
      (call) => call.method === "POST" && call.url === "/api/v1/research/runs"
    )
    expect(posts).toHaveLength(1)
    const payload = JSON.parse(posts[0]?.body ?? "{}") as Record<string, unknown>
    expect(payload).not.toHaveProperty("briefText")
    expect(payload["allowance"]).toEqual(allowanceFixture)
  })

  it("reports a run commissioned against an older brief", async () => {
    stubResearchFetch({
      preferences: { ...discoveryPreferencesFixture, version: 4 },
      brief: discoveryBriefFixture({
        profileVersion: 4,
        rubricVersion: "criteria-v4-new",
        rubricSource: "preferences_versions:current:v4",
      }),
    })
    renderSection()
    await startRun()

    await screen.findByText(
      "The run below used brief profile v3; the saved brief is now profile v4."
    )
  })
})
