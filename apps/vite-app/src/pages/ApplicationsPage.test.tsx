// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import App from "@/App"
import {
  roleWorkflowFixture,
  savedJobFixture,
  stubFetch,
} from "@/pages/fixtures"

beforeEach(() => {
  window.location.hash = "#/applications"
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ""
})

describe("Applications list", () => {
  it("lists only chosen roles with their current work and detail route", async () => {
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
    const detail = screen.getByRole("link", {
      name: "Application details",
    }) as HTMLAnchorElement
    expect(detail.getAttribute("href")).toBe("#/applications/job-1")
    expect(
      screen.queryByRole("link", { name: "Open handoff" })
    ).toBeNull()
  })

  it.each([
    ["selected", "#/jobs/job-1"],
    ["checking", "#/jobs/job-1/check"],
    ["checked", "#/jobs/job-1/check"],
    ["answered", "#/jobs/job-1/answers"],
    ["preparing", "#/jobs/job-1/prepare"],
    ["prepared", "#/jobs/job-1/handoff"],
    ["handoff_saved", "#/jobs/job-1/handoff"],
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

  it("shows saved item types, versions and status with a handoff link", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "prepared"),
      },
      savedJobs: [
        savedJobFixture("job-1", {
          items: [
            {
              type: "cv",
              required: true,
              state: "ready",
              reason: "Drafted from verified facts.",
              version: 2,
            },
            {
              type: "cover_letter",
              required: false,
              state: "held",
              reason: "Waiting on an owner fact.",
              version: 1,
            },
          ],
        }),
      ],
    })
    render(<App />)

    expect(
      await screen.findByText("cv · version 2 · ready — Drafted from verified facts.")
    ).toBeDefined()
    expect(
      screen.getByText(
        "cover_letter · version 1 · held · optional — Waiting on an owner fact."
      )
    ).toBeDefined()
    const handoff = screen.getByRole("link", {
      name: "Open handoff",
    }) as HTMLAnchorElement
    expect(handoff.getAttribute("href")).toBe("#/jobs/job-1/handoff")
  })

  it("shows an empty state when no role has been chosen", async () => {
    stubFetch({ workflowsByOpportunity: {} })
    render(<App />)

    expect(await screen.findByText("No chosen roles yet")).toBeDefined()
    expect(screen.queryByText("Backend Engineer")).toBeNull()
    expect(screen.queryByText("Weekend Project")).toBeNull()
  })
})

describe("Application detail saved materials (F3)", () => {
  it("lists partial saved items and links to the fill-manually handoff", async () => {
    window.location.hash = "#/applications/job-1"
    stubFetch({
      savedJobs: [
        savedJobFixture("job-1", {
          items: [
            {
              type: "cv",
              required: true,
              state: "ready",
              reason: "",
              version: 3,
            },
            {
              type: "email_body",
              required: true,
              state: "held",
              reason: "Check is outdated.",
              version: 1,
            },
          ],
        }),
      ],
    })
    render(<App />)

    expect(await screen.findByText("Saved materials")).toBeDefined()
    expect(
      await screen.findByText("cv · version 3 · ready")
    ).toBeDefined()
    expect(
      screen.getByText("email_body · version 1 · held — Check is outdated.")
    ).toBeDefined()
    const handoff = screen.getByRole("link", {
      name: "Open handoff — you fill in manually",
    }) as HTMLAnchorElement
    expect(handoff.getAttribute("href")).toBe("#/jobs/job-1/handoff")
    expect(
      screen.getByText(/Opening it never means applied\./)
    ).toBeDefined()
  })

  it("names the empty state when nothing is saved yet", async () => {
    window.location.hash = "#/applications/job-1"
    stubFetch()
    render(<App />)

    expect(await screen.findByText("Saved materials")).toBeDefined()
    expect(await screen.findByText("No saved materials yet")).toBeDefined()
  })
})
