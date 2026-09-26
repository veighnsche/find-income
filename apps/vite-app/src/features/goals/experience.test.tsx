// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import { ExperiencePanel } from "@/features/goals/ExperiencePanel"
import { sessionFixture } from "@/pages/fixtures"
import type { SourcedOwnerContext } from "@/features/owner-context/sourced-context"

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const sourcesFixture: SourcedOwnerContext = {
  owner: { kind: "administrator", id: "owner" },
  sourcesConnected: true,
  sources: [
    {
      id: "cv-main",
      name: "Vince Liem — CV",
      sha256: "abc123def4567890abc123def4567890",
      approved: true,
      body: "Six years of Go platform work, remote-first.",
    },
    {
      id: "cv-addendum",
      name: "Career addendum",
      sha256: "fff000111222333444555666777888999",
      approved: true,
      body: "Harbour logistics domain experience.",
    },
  ],
}

function stubExperienceFetch(options: {
  ownerContext?: SourcedOwnerContext | "missing" | "down"
  answers?: unknown
} = {}) {
  const calls: { url: string; method: string }[] = []
  const fetchMock = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url =
      typeof input === "string"
        ? input
        : input instanceof URL
          ? input.toString()
          : input.url
    calls.push({ url, method: (init?.method ?? "GET").toUpperCase() })
    const path = new URL(url, "http://localhost").pathname
    if (path === "/api/v1/auth/session") return jsonResponse(200, sessionFixture)
    if (path === "/api/v1/research/owner-context") {
      const mode = options.ownerContext ?? sourcesFixture
      if (mode === "missing")
        return jsonResponse(404, { error: { message: "No such route." } })
      if (mode === "down")
        return jsonResponse(503, { error: { message: "Sources failed to load." } })
      return jsonResponse(200, mode)
    }
    if (path === "/api/v1/answers") return jsonResponse(200, options.answers ?? { items: [] })
    return jsonResponse(404, { error: { message: "Not found." } })
  })
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderPanel() {
  return render(
    <SessionProvider>
      <ExperiencePanel />
    </SessionProvider>
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("ExperiencePanel sourced context (G1/D4)", () => {
  it("shows CV sources with provenance even with zero reusable answers", async () => {
    const { calls } = stubExperienceFetch()
    renderPanel()

    expect(await screen.findByText("Vince Liem — CV")).toBeDefined()
    expect(await screen.findByText("Career addendum")).toBeDefined()
    expect(
      await screen.findByText(/Source cv-main · sha abc123def456/)
    ).toBeDefined()
    // First source body is readable without retyping anything.
    expect(
      await screen.findByText("Six years of Go platform work, remote-first.")
    ).toBeDefined()
    expect(await screen.findByText(/Signed in as administrator/)).toBeDefined()
    expect(await screen.findByText("No approved answers yet")).toBeDefined()
    // GET-only: no model call, no write, no answer promotion.
    expect(calls.every((call) => call.method === "GET")).toBe(true)
  })

  it("keeps reusable answers in their own section", async () => {
    stubExperienceFetch({
      answers: {
        items: [
          {
            id: "ans-1",
            currentVersion: 1,
            scopeTags: ["backend"],
            contextNote: "",
            versions: [
              {
                version: 1,
                text: "Approved reusable fact.",
                textSha256: "x".repeat(64),
                approvedAt: "2026-09-20T10:00:00Z",
                approvedBy: { actorKind: "administrator", actorId: "owner" },
                approvalRequestKey: "req-1",
              },
            ],
          },
        ],
      },
    })
    renderPanel()

    expect(await screen.findByText("Approved reusable fact.")).toBeDefined()
    expect(await screen.findByText("Vince Liem — CV")).toBeDefined()
    expect(await screen.findByText("Career sources")).toBeDefined()
    expect(await screen.findByText("Reusable answers")).toBeDefined()
  })

  it("stays neutral when the server predates the endpoint", async () => {
    stubExperienceFetch({ ownerContext: "missing" })
    renderPanel()

    expect(
      await screen.findByText("Career sources are not available")
    ).toBeDefined()
    expect(await screen.findByText("No approved answers yet")).toBeDefined()
  })

  it("reports a source loader failure honestly with a retry", async () => {
    stubExperienceFetch({ ownerContext: "down" })
    renderPanel()

    expect(await screen.findByText("Could not load career sources")).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Try again" })
    ).toBeDefined()
  })

  it("says plainly when no loader is connected", async () => {
    stubExperienceFetch({
      ownerContext: { owner: { kind: "administrator", id: "owner" }, sourcesConnected: false, sources: [] },
    })
    renderPanel()

    expect(
      await screen.findByText("Career sources are not connected")
    ).toBeDefined()
    expect(
      screen.queryByText("Could not load career sources")
    ).toBeNull()
  })
})
