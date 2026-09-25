// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import App from "@/App"
import type { Round } from "@/api/client"
import {
  preferencesFixture,
  sessionFixture,
  stubFetch,
} from "@/pages/fixtures"

beforeEach(() => {
  window.location.hash = ""
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ""
})

describe("read-only shell", () => {
  it("renders Today from real preference, opportunity and runtime reads", async () => {
    stubFetch()
    render(<App />)

    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()
    expect(await screen.findByText("1 active of 2 tracked roles.")).toBeDefined()
    expect(
      await screen.findByText(/Search profile version 3 · Berlin/)
    ).toBeDefined()
    const nav = screen.getByRole("navigation", { name: "Primary" })
    expect(nav.querySelector('a[href="#/jobs"]')).not.toBeNull()
  })

  it("renders My search with the saved criteria", async () => {
    stubFetch()
    window.location.hash = "#/search"
    render(<App />)

    expect(
      await screen.findByRole("heading", { name: "My search" })
    ).toBeDefined()
    expect(await screen.findByText("Profile version 3")).toBeDefined()
    expect(await screen.findByText("Backend engineer")).toBeDefined()
    expect(await screen.findByText("role · require")).toBeDefined()
    expect(await screen.findByText("EUR 4500.00")).toBeDefined()
  })

  it("renders grouped Jobs with deep links", async () => {
    stubFetch()
    window.location.hash = "#/jobs"
    render(<App />)

    const link = (await screen.findByRole("link", {
      name: "Backend Engineer",
    })) as HTMLAnchorElement
    expect(link.getAttribute("href")).toBe("#/jobs/job-1")
    expect(await screen.findByText(/Not yet classified \(2\)/)).toBeDefined()
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

  it("renders saved application packs per role", async () => {
    stubFetch()
    window.location.hash = "#/applications/job-1"
    render(<App />)

    expect(await screen.findByText("Version 1")).toBeDefined()
    expect(await screen.findByText("Version 2")).toBeDefined()
    expect(await screen.findByText(/2 packs saved/)).toBeDefined()
  })

  it("shows the server message when a job does not exist", async () => {
    stubFetch()
    window.location.hash = "#/jobs/missing"
    render(<App />)

    expect(
      await screen.findByText("Opportunity not found.")
    ).toBeDefined()
  })

  it("shows the sign-in panel when the session is null and signs in", async () => {
    stubFetch({ session: null })
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
    render(<App />)
    expect(await screen.findByRole("heading", { name: "Today" })).toBeDefined()

    const go = (hash: string) => {
      window.location.hash = hash
      window.dispatchEvent(new HashChangeEvent("hashchange"))
    }

    go("#/search")
    expect(
      await screen.findByRole("heading", { name: "My search" })
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
    expect(await screen.findByText("Version 2")).toBeDefined()
    go("#/today")
    await waitFor(() =>
      expect(screen.getByRole("heading", { name: "Today" })).toBeDefined()
    )

    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) {
      expect(call.method).toBe("GET")
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
        return handlers.processInput(JSON.parse(body ?? "{}") as Record<string, unknown>)
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

    expect(await screen.findByRole("heading", { name: "My search" })).toBeDefined()
    expect(await screen.findByText("Profile version 3")).toBeDefined()
    expect(
      await screen.findByRole("heading", { name: "Your goals" })
    ).toBeDefined()
    expect(
      await screen.findByRole("heading", { name: "Change my search" })
    ).toBeDefined()
    expect(
      await screen.findByLabelText("Describe the correction in your own words")
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Correct preferred location" })
    ).toBeDefined()
    expect(
      screen.getByRole("button", { name: 'Correct criterion "Backend engineer"' })
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
      (call) => call.method === "POST" && call.url.endsWith("/rounds/process-input")
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

    expect(
      await screen.findByText("Saved as profile version 4.")
    ).toBeDefined()
    expect(await screen.findByText("Profile version 4")).toBeDefined()
    await waitFor(() =>
      expect(
        (screen.getByLabelText(
          "Describe the correction in your own words"
        ) as HTMLTextAreaElement).value
      ).toBe("")
    )
  })

  it("routes an in-context Correct button into the correction box", async () => {
    stubCorrectionFetch({
      preferences: () => preferencesFixture,
      brief: () =>
        correctionJson(404, { error: { message: "No brief yet." } }),
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
    expect(await screen.findByText("Profile version 5")).toBeDefined()
  })
})
