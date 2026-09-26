// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, within } from "@testing-library/react"
import App from "@/App"
import type { Round } from "@/api/client"
import {
  preferencesFixture,
  roleWorkflowFixture,
  runHistoryFixture,
  stubFetch,
} from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  window.location.hash = ""
  vi.unstubAllGlobals()
})

function activeRound(state: Round["state"]): Round {
  return {
    id: "round-1",
    requestKey: "muse:run-1",
    state,
    profileVersion: 3,
  } as Round
}

async function continueRegion(): Promise<HTMLElement> {
  return screen.findByRole("region", { name: "Continue" })
}

describe("Today resume hub (B3)", () => {
  it("leads a paused search back to My search", async () => {
    stubFetch({ activeRound: activeRound("paused") })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    const link = await within(region).findByRole("link", {
      name: "Resume search on My search",
    })
    expect(link.getAttribute("href")).toBe("#/search")
  })

  it("leads a running search back to My search", async () => {
    stubFetch({ activeRound: activeRound("running") })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    expect(
      await within(region).findByRole("link", {
        name: "Open the running search",
      })
    ).toBeDefined()
  })

  it("continues the most recent chosen role in Applications", async () => {
    stubFetch()
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    expect(
      await within(region).findByText(/Most recent role: Backend Engineer/)
    ).toBeDefined()
    const link = await within(region).findByRole("link", {
      name: "Continue this application",
    })
    expect(link.getAttribute("href")).toBe("#/applications/job-1")
  })

  it("leads a failed run back to its review link", async () => {
    stubFetch({ runHistory: [runHistoryFixture("run-9", "failed")] })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    expect(
      await within(region).findByText(/latest search failed/)
    ).toBeDefined()
    const link = await within(region).findByRole("link", {
      name: "Review this failed run",
    })
    expect(link.getAttribute("href")).toBe("#/search?run=run-9")
  })

  it("opens the per-job handoff for prepared and saved roles", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "prepared"),
      },
    })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    const link = await within(region).findByRole("link", {
      name: "Open materials and handoff",
    })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/handoff")
  })

  it("reopens the saved handoff for handoff_saved roles", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "handoff_saved"),
      },
    })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    const link = await within(region).findByRole("link", {
      name: "Open the saved handoff",
    })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/handoff")
  })

  it("sends an empty search to the goals form", async () => {
    stubFetch({
      opportunities: [],
      workflowsByOpportunity: {},
      preferences: { ...preferencesFixture, roleCriteria: [] },
    })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    const link = await within(region).findByRole("link", {
      name: "Set your goals",
    })
    expect(link.getAttribute("href")).toBe("#/search")
  })

  it("offers Find jobs when goals exist but nothing runs", async () => {
    stubFetch({ opportunities: [], workflowsByOpportunity: {} })
    window.location.hash = "#/today"
    render(<App />)

    const region = await continueRegion()
    expect(
      await within(region).findByRole("link", { name: "Find jobs on My search" })
    ).toBeDefined()
  })
})
