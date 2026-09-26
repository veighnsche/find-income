// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react"
import App from "@/App"
import type { Round } from "@/api/client"
import {
  museReadinessFixture,
  preferencesFixture,
  researchRunFixture,
  roleWorkflowFixture,
  runHistoryFixture,
  sessionFixture,
  sourcedContextFixture,
  stubFetch,
} from "@/pages/fixtures"
import { newestRunState, searchStageView } from "@/pages/SearchPage"

beforeEach(() => {
  window.location.hash = ""
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
  window.location.hash = ""
})

describe("read-only shell", () => {
  it("opens the bare root on My search, the saved-before-run state", async () => {
    stubFetch()
    window.location.hash = ""
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Let's find your next role" })
    ).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(
      nav.querySelector('a[href="#/search"]')?.getAttribute("aria-current")
    ).toBe("page")
  })

  it("renders Today from real preference, opportunity and runtime reads", async () => {
    stubFetch()
    window.location.hash = "#/today"
    render(<App />)

    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()
    expect(
      await screen.findByText("1 active of 2 tracked roles.")
    ).toBeDefined()
    expect(
      await screen.findByText(/Search profile version 3 · Berlin/)
    ).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(nav.querySelector('a[href="#/jobs"]')).not.toBeNull()
  })

  it("links back to the server-restored research run without starting work", async () => {
    const { calls } = stubFetch({
      runHistory: [
        runHistoryFixture("run-1", "paused"),
        runHistoryFixture("run-0", "completed", "2026-09-25T10:00:00Z"),
      ],
      researchRunById: {
        "run-1": researchRunFixture("run-1", "paused", {
          savedIds: ["job-1"],
          unresolvedCount: 1,
        }),
      },
    })
    window.location.hash = "#/today"
    render(<App />)

    expect(
      await screen.findByText(
        "Paused — resume continues with the remaining allowance."
      )
    ).toBeDefined()
    expect(screen.getByText("1 saved role · 1 unresolved.")).toBeDefined()
    const link = screen.getByRole("link", {
      name: "Resume this run",
    }) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/search?run=run-1")
    expect(await screen.findByText("Recent runs (2)")).toBeDefined()
    expect(
      screen.getByRole("link", { name: "run-0 · completed" })
    ).toBeDefined()
    expect(window.localStorage.length).toBe(0)
    expect(calls.every((call) => call.method === "GET")).toBe(true)
  })

  it("surfaces a failed run with its reason and valid next action", async () => {
    stubFetch({
      runHistory: [runHistoryFixture("run-9", "failed")],
      researchRunById: {
        "run-9": researchRunFixture("run-9", "failed", {
          stopReason: "Contributor CLI exited.",
        }),
      },
    })
    window.location.hash = "#/today"
    render(<App />)

    expect(
      await screen.findByText("Failed — see the report for what is known.")
    ).toBeDefined()
    expect(await screen.findByText("Contributor CLI exited.")).toBeDefined()
    const saved = await screen.findByRole("region", { name: "Saved research" })
    const link = within(saved).getByRole("link", {
      name: "Review this failed run",
    }) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/search?run=run-9")
    expect(
      await screen.findByText(/cannot resume; Find more on My search/)
    ).toBeDefined()
  })

  it("renders My search with the saved criteria", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Let's find your next role" })
    ).toBeDefined()
    expect(await screen.findByText(/Saved profile version 3/)).toBeDefined()
    expect(await screen.findByText("Backend engineer")).toBeDefined()
    expect(await screen.findByText("Role · Must have")).toBeDefined()
    expect(await screen.findByText(/4,500\.00 EUR\/mo/)).toBeDefined()
  })

  it("renders grouped Jobs with deep links", async () => {
    stubFetch()
    window.location.hash = "#/jobs"
    render(<App />)

    fireEvent.click(
      await screen.findByRole("tab", { name: /Not yet classified/ })
    )
    const link = (await screen.findByRole("link", {
      name: "Backend Engineer",
    })) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/jobs/job-1")
    expect(await screen.findByText("Not yet classified (2)")).toBeDefined()
  })

  it("renders a job detail with decision, pay and live stage", async () => {
    stubFetch()
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(
      await screen.findByText("EUR 4000.00 – EUR 6000.00, per month, base")
    ).toBeDefined()
    expect(await screen.findByText("selected")).toBeDefined()
    expect(
      await screen.findByText("Current stage: Select jobs (selected)")
    ).toBeDefined()
    await waitFor(() => {
      const current = document.querySelector('li[aria-current="step"]')
      expect(current?.textContent).toContain("Select jobs")
    })
    expect(
      screen.queryByText("Per-role stage actions are not available yet")
    ).toBeNull()
  })

  it("renders the application detail without packs", async () => {
    stubFetch()
    window.location.hash = "#/applications/job-1"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(
      await screen.findByRole("heading", { name: "Continue this application" })
    ).toBeDefined()
    expect(screen.queryByText(/packs saved/)).toBeNull()
  })

  it("shows the server message when a job does not exist", async () => {
    stubFetch()
    window.location.hash = "#/jobs/missing"
    render(<App />)

    expect(await screen.findByText("Opportunity not found.")).toBeDefined()
  })

  it("shows the sign-in panel when the session is null and signs in", async () => {
    stubFetch({ session: null })
    window.location.hash = "#/today"
    render(<App />)

    const password = (await screen.findByLabelText(
      "Password"
    )) as HTMLInputElement
    fireEvent.change(password, { target: { value: "secret" } })
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }))

    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()
  })

  it("commissions no work while reading and navigating every surface", async () => {
    const { calls } = stubFetch()
    window.location.hash = "#/today"
    render(<App />)
    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()

    const go = (hash: string) => {
      window.location.hash = hash
      window.dispatchEvent(new HashChangeEvent("hashchange"))
    }

    go("#/search")
    expect(
      await screen.findByRole("heading", { name: "Let's find your next role" })
    ).toBeDefined()
    go("#/jobs")
    expect(await screen.findByRole("heading", { name: "Jobs" })).toBeDefined()
    go("#/jobs/job-1")
    expect(
      await screen.findByRole("heading", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(await screen.findByText("selected")).toBeDefined()
    go("#/applications")
    expect(
      await screen.findByRole("heading", { name: "Applications" })
    ).toBeDefined()
    go("#/applications/job-1")
    expect(
      await screen.findByRole("heading", { name: "Continue this application" })
    ).toBeDefined()
    go("#/today")
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Today" })).toBeDefined()
    )

    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
      // The muse commission count is a GET-only read, not a commission.
      if (call.url === "/api/v1/muse/commissions") continue
      expect(call.url).not.toMatch(
        /\/commission|\/prepare|\/process-input|\/connect/
      )
    }
    const posts = calls.filter((call) => call.method === "POST")
    expect(posts).toEqual([])
  })
})

interface CorrectionCall {
  url: string
  method: string
  body: string | null
}

function correctionJson(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

// Local scripted stub for the correction journey. The shared stubFetch stays
// untouched; this one adds the brief/process-input/round endpoints the
// correction flow needs.
function stubCorrectionFetch(handlers: {
  preferences: () => unknown
  brief: () => Response
  processInput: (body: Record<string, unknown>) => Response
  round: (id: string) => Response
}): { calls: CorrectionCall[] } {
  const calls: CorrectionCall[] = []
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
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/auth/session")
        return correctionJson(200, sessionFixture)
      if (path === "/api/v1/preferences")
        return correctionJson(200, handlers.preferences())
      if (path === "/api/v1/research/brief") return handlers.brief()
      if (path === "/api/v1/rounds/process-input" && method === "POST")
        return handlers.processInput(
          JSON.parse(body ?? "{}") as Record<string, unknown>
        )
      const roundMatch = path.match(/^\/api\/v1\/rounds\/([^/]+)$/)
      if (roundMatch?.[1] !== undefined)
        return handlers.round(decodeURIComponent(roundMatch[1]))
      return correctionJson(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function correctionRound(overrides: Partial<Round> = {}): Round {
  return {
    id: "round-1",
    requestKey: "profile-correction-v3-00000000",
    state: "queued",
    profileVersion: 3,
    originalProfileVersion: 3,
    ...overrides,
  } as Round
}

describe("frame sidebar and work-status strip (B1/F2)", () => {
  it("shows product identity, strip status and chosen-work navigation", async () => {
    stubFetch()
    render(<App />)

    expect(await screen.findByText("Your personal recruitment agency"))
      .toBeDefined()
    expect(await screen.findByText("Your job search")).toBeDefined()
    expect(await screen.findByText("Select jobs · 1 role")).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(nav.querySelector('a[href="#/applications"]')).not.toBeNull()
    expect(nav.querySelector('a[href="#/search"]')).not.toBeNull()
  })

  it("hides Applications and reports readiness with no chosen work", async () => {
    stubFetch({ workflowsByOpportunity: {} })
    render(<App />)

    expect(await screen.findByText("Ready to find jobs")).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(nav.querySelector('a[href="#/applications"]')).toBeNull()
  })

  it("reports a paused search from the active round", async () => {
    stubFetch({
      activeRound: correctionRound({ id: "round-9", state: "paused" }),
    })
    render(<App />)

    expect(await screen.findByText("Find jobs · paused")).toBeDefined()
  })

  it("never claims Ready when no search goals are saved", async () => {
    stubFetch({
      workflowsByOpportunity: {},
      preferences: { ...preferencesFixture, roleCriteria: [] },
    })
    render(<App />)

    expect(
      await screen.findByText("No search goals saved yet")
    ).toBeDefined()
    expect(screen.queryByText("Ready to find jobs")).toBeNull()
  })

  it("reports a known Contributor blocker instead of Ready", async () => {
    stubFetch({
      workflowsByOpportunity: {},
      museReadinessByTier: {
        contributor: museReadinessFixture(
          "contributor",
          "unavailable",
          "Muse CLI is not installed."
        ),
      },
    })
    render(<App />)

    expect(
      await screen.findByText(
        "Find jobs unavailable: Muse CLI is not installed."
      )
    ).toBeDefined()
    expect(screen.queryByText("Ready to find jobs")).toBeNull()
  })

  it("reports per-stage chosen work and terminal Handoff", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "checking"),
        "job-2": roleWorkflowFixture("job-2", "answering"),
      },
    })
    render(<App />)

    expect(
      await screen.findByText("Check job details · 1 role; Answer questions · 1 role")
    ).toBeDefined()
  })

  it("reports terminal Handoff when every role is saved", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "handoff_saved"),
      },
    })
    render(<App />)

    expect(
      await screen.findByText("Handoff · 1 saved role")
    ).toBeDefined()
  })

  it("shows the supported owner identity at the sidebar foot", async () => {
    stubFetch({ sourcedContext: sourcedContextFixture() })
    render(<App />)

    expect(await screen.findByText("Signed in as owner")).toBeDefined()
  })

  it("falls back to session text when owner context is absent", async () => {
    stubFetch()
    render(<App />)

    expect(
      await screen.findByText("Signed in · session ends 2099-01-01")
    ).toBeDefined()
    expect(screen.queryByText(/Signed in as /)).toBeNull()
  })

  it("opens My search with the seven-stage row and Handoff last", async () => {
    window.location.hash = "#/search"
    stubFetch()
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Let's find your next role" })
    ).toBeDefined()
    const journey = await screen.findByRole("list", {
      name: "Seven-step journey",
    })
    const labels = Array.from(journey.querySelectorAll("li")).map(
      (item) => item.textContent ?? ""
    )
    expect(labels).toHaveLength(7)
    expect(labels[0]).toContain("Your goals")
    expect(labels[6]).toContain("Handoff")
    expect(labels[1]).toContain("Contributor")
    expect(labels[1]).toContain("Jev")
    expect(labels[5]).toContain("Standard")
    // Saved goals with no known run rest on Find jobs, the next step.
    await waitFor(() =>
      expect(
        journey.querySelector('[aria-current="step"]')?.textContent
      ).toContain("Find jobs")
    )
    expect(screen.queryByText(/Review & send/)).toBeNull()
  })

  it("marks Your goals active when no goals are saved yet", async () => {
    window.location.hash = "#/search"
    stubFetch({
      preferences: { ...preferencesFixture, roleCriteria: [] },
    })
    render(<App />)

    const journey = await screen.findByRole("list", {
      name: "Seven-step journey",
    })
    await waitFor(() =>
      expect(
        journey.querySelector('[aria-current="step"]')?.textContent
      ).toContain("Your goals")
    )
  })

  it("marks Select jobs active once results are saved", async () => {
    window.location.hash = "#/search"
    stubFetch({ runHistory: [runHistoryFixture("run-7", "completed")] })
    render(<App />)

    const journey = await screen.findByRole("list", {
      name: "Seven-step journey",
    })
    await waitFor(() =>
      expect(
        journey.querySelector('[aria-current="step"]')?.textContent
      ).toContain("Select jobs")
    )
  })
})

describe("search stage derivation (F2)", () => {
  it("prefers active work, else the newest terminal run", () => {
    expect(
      newestRunState([
        runHistoryFixture("run-2", "completed"),
        runHistoryFixture("run-1", "paused"),
      ])
    ).toBe("paused")
    expect(newestRunState([runHistoryFixture("run-2", "failed")])).toBe(
      "failed"
    )
    expect(newestRunState([])).toBeNull()
    expect(
      newestRunState([runHistoryFixture("run-x", "mystery")])
    ).toBeNull()
  })

  it("derives goals, find and select markers from actual state", () => {
    expect(
      searchStageView({ goalsSaved: false, runState: null }).activeStageId
    ).toBe("goals")
    expect(
      searchStageView({ goalsSaved: null, runState: null }).activeStageId
    ).toBe("goals")
    const find = searchStageView({ goalsSaved: true, runState: "running" })
    expect(find.activeStageId).toBe("find")
    expect(find.stages[0]?.state).toBe("complete")
    expect(find.stages[1]?.state).toBe("upcoming")
    const failed = searchStageView({ goalsSaved: true, runState: "failed" })
    expect(failed.activeStageId).toBe("find")
    const select = searchStageView({ goalsSaved: true, runState: "completed" })
    expect(select.activeStageId).toBe("select")
    expect(select.stages[0]?.state).toBe("complete")
    expect(select.stages[1]?.state).toBe("complete")
    expect(select.stages[2]?.state).toBe("upcoming")
  })
})

describe("owner corrections (RW-B1)", () => {
  beforeEach(() => {
    window.localStorage.clear()
  })

  afterEach(() => {
    window.localStorage.clear()
  })

  it("shows goals, facts and correction controls without commissioning work", async () => {
    const { calls } = stubCorrectionFetch({
      preferences: () => preferencesFixture,
      brief: () =>
        correctionJson(200, {
          profileVersion: 3,
          rubricVersion: "criteria-v3-abc123",
          rubricSource: "preferences_versions:current:v3",
          requirements: [],
          facts: [{ key: "location", value: "Berlin" }],
        }),
      processInput: () => correctionJson(201, { round: correctionRound() }),
      round: (id) => correctionJson(200, correctionRound({ id })),
    })
    window.location.hash = "#/search"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "Let's find your next role" })
    ).toBeDefined()
    expect(await screen.findByText(/Saved profile version 3/)).toBeDefined()
    expect(
      await screen.findByRole("heading", { name: "What you want next" })
    ).toBeDefined()
    expect(
      await screen.findByRole("heading", { name: "Change my search" })
    ).toBeDefined()
    expect(
      await screen.findByLabelText("Describe the correction in your own words")
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Edit goals" })
    ).toBeDefined()
    expect(
      screen.getByRole("button", {
        name: 'Correct criterion "Backend engineer"',
      })
    ).toBeDefined()
    expect(await screen.findByText("Profile facts")).toBeDefined()
    for (const call of calls) expect(call.method).toBe("GET")
  })

  it("saves a correction only after the readback confirms the new version", async () => {
    let serverVersion = 3
    let serverRound = correctionRound({ state: "queued", profileVersion: 3 })
    const { calls } = stubCorrectionFetch({
      preferences: () => ({ ...preferencesFixture, version: serverVersion }),
      brief: () =>
        correctionJson(200, {
          profileVersion: serverVersion,
          rubricVersion: `criteria-v${serverVersion}-abc123`,
          rubricSource: `preferences_versions:current:v${serverVersion}`,
          requirements: [],
          facts: [{ key: "location", value: "Berlin" }],
        }),
      processInput: () => correctionJson(201, { round: serverRound }),
      round: () => correctionJson(200, serverRound),
    })
    window.location.hash = "#/search"
    render(<App />)

    const box = (await screen.findByLabelText(
      "Describe the correction in your own words"
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "I want remote-only roles." } })
    fireEvent.click(screen.getByRole("button", { name: "Send correction" }))

    expect(
      await screen.findByText("Accepted — saving your change…")
    ).toBeDefined()
    expect(screen.queryByText(/Saved as profile version/)).toBeNull()

    const post = calls.find(
      (call) =>
        call.method === "POST" && call.url.endsWith("/rounds/process-input")
    )
    expect(post).toBeDefined()
    const payload = JSON.parse(post?.body ?? "{}") as Record<string, unknown>
    expect(payload).toMatchObject({
      targetKind: "profile",
      targetId: "current",
      expectedRevision: 3,
      text: "I want remote-only roles.",
    })
    expect(payload.requestKey).toMatch(/^profile-correction-v3-[0-9a-f]{8}$/)

    serverRound = correctionRound({ state: "completed", profileVersion: 4 })
    serverVersion = 4
    fireEvent.click(screen.getByRole("button", { name: "Check again" }))

    expect(await screen.findByText("Saved as profile version 4.")).toBeDefined()
    expect(await screen.findByText(/Saved profile version 4/)).toBeDefined()
    await waitFor(() =>
      expect(
        (
          screen.getByLabelText(
            "Describe the correction in your own words"
          ) as HTMLTextAreaElement
        ).value
      ).toBe("")
    )
  })

  it("routes an in-context Correct button into the correction box", async () => {
    stubCorrectionFetch({
      preferences: () => preferencesFixture,
      brief: () => correctionJson(404, { error: { message: "No brief yet." } }),
      processInput: () => correctionJson(201, { round: correctionRound() }),
      round: (id) => correctionJson(200, correctionRound({ id })),
    })
    window.location.hash = "#/search"
    render(<App />)

    fireEvent.click(
      await screen.findByRole("button", {
        name: 'Correct criterion "Backend engineer"',
      })
    )
    const box = (await screen.findByLabelText(
      "Describe the correction in your own words"
    )) as HTMLTextAreaElement
    expect(box.value).toContain('About role criterion "Backend engineer"')
    expect(document.activeElement).toBe(box)
    expect(await screen.findByText("No search brief yet")).toBeDefined()
  })

  it("reports a stale-revision conflict honestly", async () => {
    let serverVersion = 4
    stubCorrectionFetch({
      preferences: () => ({ ...preferencesFixture, version: serverVersion }),
      brief: () =>
        correctionJson(200, {
          profileVersion: serverVersion,
          rubricVersion: `criteria-v${serverVersion}-abc123`,
          rubricSource: `preferences_versions:current:v${serverVersion}`,
          requirements: [],
          facts: [],
        }),
      processInput: () => {
        // Another writer lands v5 between our read and our submit.
        serverVersion = 5
        return correctionJson(409, { error: { message: "Stale revision." } })
      },
      round: (id) => correctionJson(200, correctionRound({ id })),
    })
    window.location.hash = "#/search"
    render(<App />)

    const box = (await screen.findByLabelText(
      "Describe the correction in your own words"
    )) as HTMLTextAreaElement
    fireEvent.change(box, { target: { value: "Prefer Amsterdam." } })
    fireEvent.click(screen.getByRole("button", { name: "Send correction" }))

    expect(
      await screen.findByText(
        "The profile changed before your correction was saved"
      )
    ).toBeDefined()
    expect(screen.queryByText(/Saved as profile version/)).toBeNull()
    expect(await screen.findByText(/Saved profile version 5/)).toBeDefined()
  })
})
