// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import App from "@/App"
import { roleWorkflowFixture, stubFetch } from "@/pages/fixtures"

beforeEach(() => {
  window.location.hash = "#/applications"
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ""
})

describe("Applications list", () => {
  it("lists only chosen roles with their current work and preserved history route", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "answering"),
      },
    })
    render(<App />)

    const role = (await screen.findByRole("link", {
      name: "Backend Engineer",
    })) as HTMLAnchorElement
    expect(role.getAttribute("href")).toBe("#/jobs/job-1/answers")
    expect(screen.getByText("Current stage: Answer questions (answering)"))
    expect(screen.queryByText("Weekend Project")).toBeNull()
    const history = screen.getByRole("link", {
      name: "Saved materials and handoff",
    }) as HTMLAnchorElement
    expect(history.getAttribute("href")).toBe("#/applications/job-1")
  })

  it.each([
    ["selected", "#/jobs/job-1"],
    ["checking", "#/jobs/job-1/check"],
    ["checked", "#/jobs/job-1/check"],
    ["answered", "#/jobs/job-1/answers"],
    ["preparing", "#/jobs/job-1/prepare"],
    ["prepared", "#/jobs/job-1/prepare"],
    ["handoff_saved", "#/applications/job-1"],
    ["blocked", "#/jobs/job-1"],
  ] as const)("routes %s to %s", async (stage, href) => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", stage),
      },
    })
    render(<App />)

    const role = (await screen.findByRole("link", {
      name: "Backend Engineer",
    })) as HTMLAnchorElement
    expect(role.getAttribute("href")).toBe(href)
  })

  it("shows an empty state when no role has been chosen", async () => {
    stubFetch({ workflowsByOpportunity: {} })
    render(<App />)

    expect(await screen.findByText("No chosen roles yet")).toBeDefined()
    expect(screen.queryByText("Backend Engineer")).toBeNull()
    expect(screen.queryByText("Weekend Project")).toBeNull()
  })
})
