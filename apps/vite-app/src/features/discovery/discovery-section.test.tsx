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
  ResearchActivityEvent,
  ResearchCaptureView,
  ResearchReportView,
  ResearchRunView,
  SteeringMessage,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { DiscoverySection } from "@/features/discovery/discovery-section"
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

interface StubOptions {
  runState?: ResearchRunView["state"]
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
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderSection() {
  return render(
    <SessionProvider>
      <DiscoverySection />
    </SessionProvider>
  )
}

async function startRun(brief = "") {
  if (brief !== "") {
    const briefInput = await screen.findByLabelText(
      "Recruitment intent (optional)"
    )
    fireEvent.change(briefInput, { target: { value: brief } })
  }
  fireEvent.click(
    await screen.findByRole("button", { name: "Start research run" })
  )
  await screen.findByText("Running — research is underway.")
}

beforeEach(() => {
  window.localStorage.clear()
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

describe("DiscoverySection", () => {
  it("commissions nothing on mount", async () => {
    const { calls } = stubResearchFetch()
    renderSection()

    await screen.findByRole("button", { name: "Start research run" })
    await waitFor(() =>
      expect(
        calls.some((call) => call.url === "/api/v1/auth/session")
      ).toBe(true)
    )
    expect(calls.length).toBeGreaterThan(0)
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("restores the saved run with reads only", async () => {
    window.localStorage.setItem("jobseek.research-run-id", "run-9")
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
    expect(window.localStorage.getItem("jobseek.research-run-id")).toBe("run-1")
  })

  it("stops and resumes through round control, then refreshes", async () => {
    const { calls } = stubResearchFetch()
    renderSection()
    await startRun()

    fireEvent.click(await screen.findByRole("button", { name: "Stop" }))
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "POST" && call.url === "/api/v1/rounds/run-1/stop"
        )
      ).toBe(true)
    )
    fireEvent.click(await screen.findByRole("button", { name: "Resume" }))
    await waitFor(() =>
      expect(
        calls.some(
          (call) =>
            call.method === "POST" &&
            call.url === "/api/v1/rounds/run-1/resume"
        )
      ).toBe(true)
    )
    const runReads = calls.filter(
      (call) =>
        call.method === "GET" && call.url === "/api/v1/research/runs/run-1"
    )
    expect(runReads.length).toBeGreaterThanOrEqual(3)
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
    const { calls } = stubResearchFetch()
    renderSection()
    await startRun("remote backend roles")

    fireEvent.click(
      await screen.findByRole("button", { name: "Find more jobs" })
    )
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
    expect(window.localStorage.getItem("jobseek.research-run-id")).toBe("run-2")
  })

  it("loads the report for terminal runs", async () => {
    stubResearchFetch({ runState: "completed" })
    renderSection()

    fireEvent.click(
      await screen.findByRole("button", { name: "Start research run" })
    )
    await screen.findByText("Completed — outcomes and report are saved.")
    await screen.findByText('opportunity "Backend Engineer" (job-1 rev 2)')
    await screen.findByText("Searched 1 source")
    await screen.findByText("attempt a1: capture incomplete")
  })
})
