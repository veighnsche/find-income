// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import type { RoleWorkflowState } from "@/api/client"
import { SessionProvider } from "@/api/session"
import { roleWorkflowFixture, stubFetch } from "@/pages/fixtures"
import { ApplicationContinue } from "@/pages/application-continue"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function renderContinue(jobId: string) {
  return render(
    <SessionProvider>
      <ApplicationContinue jobId={jobId} />
    </SessionProvider>
  )
}

function stubWorkflow(stage: RoleWorkflowState["stage"]) {
  stubFetch({
    workflowsByOpportunity: {
      "job-1": roleWorkflowFixture("job-1", stage),
    },
  })
}

describe("ApplicationContinue", () => {
  it("routes a checked role to its answer boxes", async () => {
    stubWorkflow("checked")
    renderContinue("job-1")

    const link = await screen.findByRole("link", { name: "Answer questions" })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/answers")
  })

  it("routes an answered role to preparation", async () => {
    stubWorkflow("answered")
    renderContinue("job-1")

    const link = await screen.findByRole("link", {
      name: "Prepare materials",
    })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/prepare")
  })

  it("reopens the saved handoff for handoff_saved roles", async () => {
    stubWorkflow("handoff_saved")
    renderContinue("job-1")

    const link = await screen.findByRole("link", { name: "Saved handoff" })
    expect(link.getAttribute("href")).toBe("#/jobs/job-1/handoff")
  })

  it("reports blocked roles without a next step", async () => {
    stubWorkflow("blocked")
    renderContinue("job-1")
    expect(await screen.findByText(/no next step/i)).toBeDefined()
  })
})
