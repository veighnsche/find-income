// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type { Preferences, SearchBriefView } from "@/api/client"
import { SessionProvider } from "@/api/session"
import {
  SavedBriefPanel,
  summarizeSavedBriefTerms,
  useSavedBrief,
} from "@/features/discovery/saved-brief"
import { sessionFixture } from "@/pages/fixtures"

interface FetchCall {
  url: string
  method: string
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const preferencesFixture: Preferences = {
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

function briefFixture(overrides: Partial<SearchBriefView> = {}): SearchBriefView {
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

interface StubOptions {
  preferences?: Preferences
  preferencesStatus?: number
  brief?: SearchBriefView | null
  briefStatus?: number
  failBriefAttempts?: number
}

function stubBriefFetch(options: StubOptions = {}): { calls: FetchCall[] } {
  const calls: FetchCall[] = []
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
      calls.push({ url, method })
      const parsed = new URL(url, "http://localhost")
      const path = parsed.pathname

      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/preferences") {
        const status = options.preferencesStatus ?? 200
        if (status !== 200)
          return jsonResponse(status, { error: { message: "Prefs down." } })
        return jsonResponse(200, options.preferences ?? preferencesFixture)
      }
      if (path === "/api/v1/research/brief") {
        briefAttempts += 1
        if (
          options.failBriefAttempts !== undefined &&
          briefAttempts <= options.failBriefAttempts
        )
          return jsonResponse(500, { error: { message: "Brief down." } })
        const status = options.briefStatus ?? 200
        if (status !== 200)
          return jsonResponse(status, {
            error: { message: status === 404 ? "No brief." : "Brief down." },
          })
        const brief = options.brief === undefined ? briefFixture() : options.brief
        if (brief === null)
          return jsonResponse(404, { error: { message: "No brief." } })
        return jsonResponse(200, brief)
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function BriefHarness({
  runBriefProfileVersion = null,
}: {
  runBriefProfileVersion?: number | null
}) {
  const read = useSavedBrief()
  return (
    <SavedBriefPanel
      read={read}
      runBriefProfileVersion={runBriefProfileVersion}
    />
  )
}

function renderBrief(options: StubOptions = {}, panelProps = {}) {
  const stub = stubBriefFetch(options)
  const rendered = render(
    <SessionProvider>
      <BriefHarness {...panelProps} />
    </SessionProvider>
  )
  return { ...stub, ...rendered }
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("saved brief", () => {
  it("summarizes concise saved terms without inference", () => {
    expect(summarizeSavedBriefTerms(preferencesFixture)).toBe(
      "Berlin · remote ok · 32 h/week · at least EUR 4500.00/mo · 1 role criterion"
    )
    expect(
      summarizeSavedBriefTerms({
        ...preferencesFixture,
        preferredLocation: "",
        allowRemote: false,
        allowHybrid: false,
        minMonthlyBaseCents: 0,
        roleCriteria: [],
      })
    ).toBe(
      "location not recorded · onsite only · 32 h/week · no pay floor · 0 role criteria"
    )
  })

  it("shows version identity and concise terms, reading only", async () => {
    const { calls } = renderBrief()

    await screen.findByText("Saved search brief")
    await screen.findByText("Profile v3 · criteria-v3-abc123def456")
    await screen.findByText("preferences_versions:current:v3")
    await screen.findByText(
      "Berlin · remote ok · 32 h/week · at least EUR 4500.00/mo · 1 role criterion"
    )
    await screen.findByText("Backend engineer — role · require")
    const changeLink = (await screen.findByRole("link", {
      name: "Change my search",
    })) as HTMLAnchorElement
    expect(changeLink.getAttribute("href")).toBe("#/search")
    await waitFor(() =>
      expect(
        calls.some((call) => call.url === "/api/v1/research/brief")
      ).toBe(true)
    )
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("treats a missing catalog as normal pre-first-run state", async () => {
    renderBrief()
    await screen.findByText(/No reason catalog yet/)
    expect(
      screen.queryByRole("alert")
    ).toBeNull()
  })

  it("shows the catalog version when one is authored", async () => {
    renderBrief({ brief: briefFixture({ catalogVersion: "catalog-v3-1" }) })
    await screen.findByText("Reason catalog catalog-v3-1 ready.")
  })

  it("shows an empty state when no brief exists yet", async () => {
    const { calls } = renderBrief({ brief: null })

    await screen.findByText("No saved search brief yet")
    await screen.findByText(/Profile v3 is saved/)
    await screen.findByRole("link", { name: "Change my search" })
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("shows a stale brief honestly with a refresh action", async () => {
    renderBrief({
      preferences: { ...preferencesFixture, version: 4 },
      brief: briefFixture({ profileVersion: 3 }),
    })

    const alert = await screen.findByRole("alert")
    expect(alert.textContent).toContain("profile v4")
    expect(alert.textContent).toContain("profile v3")
    await screen.findByRole("button", { name: "Refresh saved brief" })
  })

  it("reports a run commissioned against an older brief", async () => {
    renderBrief({}, { runBriefProfileVersion: 2 })
    await screen.findByText(
      "The run below used brief profile v2; the saved brief is now profile v3."
    )
  })

  it("stays silent about the run basis when versions agree", async () => {
    renderBrief({}, { runBriefProfileVersion: 3 })
    await screen.findByText("Saved search brief")
    expect(
      screen.queryByText(/The run below used brief/)
    ).toBeNull()
  })

  it("retries a failed brief read", async () => {
    renderBrief({ failBriefAttempts: 1 })

    await screen.findByText("Could not load the saved search brief")
    fireEvent.click(await screen.findByRole("button", { name: "Try again" }))
    await screen.findByText("Profile v3 · criteria-v3-abc123def456")
  })

  it("surfaces a preferences read failure", async () => {
    renderBrief({ preferencesStatus: 500 })
    await screen.findByText("Could not load the saved search brief")
    await screen.findByText("Prefs down.")
  })
})
