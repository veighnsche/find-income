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
import type { RoleWorkflowState } from "@/api/client"
import { roleWorkflowFixture, stubFetch } from "@/pages/fixtures"
import {
  compactStageLabel,
  journeyViewFor,
  stageStatusText,
  type RoleStage,
} from "@/pages/role-stages"

type Stage = RoleWorkflowState["stage"]

const ACTIVE_JOURNEY: Record<Stage, string | null> = {
  selected: "Select jobs",
  checking: "Check job details",
  checked: "Check job details",
  answering: "Answer questions",
  answered: "Answer questions",
  preparing: "Prepare materials",
  prepared: "Prepare materials",
  handoff_saved: null,
  blocked: null,
}

beforeEach(() => {
  window.location.hash = ""
})

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.location.hash = ""
})

function go(hash: string) {
  window.location.hash = hash
  window.dispatchEvent(new HashChangeEvent("hashchange"))
}

function currentStepLabel(): string | null {
  const current = document.querySelectorAll('li[aria-current="step"]')
  if (current.length !== 1) return null
  return current[0]?.textContent ?? null
}

describe("role-stage mapping", () => {
  it("maps every server stage to its documented journey step", () => {
    const cases: [Stage, string | null][] = [
      ["selected", "select"],
      ["checking", "check"],
      ["checked", "check"],
      ["answering", "answer"],
      ["answered", "answer"],
      ["preparing", "prepare"],
      ["prepared", "prepare"],
      ["handoff_saved", null],
      ["blocked", null],
    ]
    for (const [stage, active] of cases) {
      expect(journeyViewFor(stage).activeStageId).toBe(active)
      expect(journeyViewFor(stage).stages).toHaveLength(7)
    }
  })

  it("completes the whole journey on handoff_saved and marks nothing on blocked", () => {
    expect(
      journeyViewFor("handoff_saved").stages.every(
        (item) => item.state === "complete"
      )
    ).toBe(true)
    expect(
      journeyViewFor("blocked").stages.every(
        (item) => item.state === "upcoming"
      )
    ).toBe(true)
  })

  it("labels every stage with its capitalized server name", () => {
    const stages: RoleStage[] = [
      "selected",
      "checking",
      "checked",
      "answering",
      "answered",
      "preparing",
      "prepared",
      "handoff_saved",
      "blocked",
    ]
    for (const stage of stages)
      expect(compactStageLabel(stage)).toBe(
        stage.charAt(0).toUpperCase() + stage.slice(1)
      )
  })

  it("reports the blocked reason instead of inventing a step", () => {
    expect(
      stageStatusText(
        roleWorkflowFixture("job-1", "blocked", {
          blockedReason: "Waiting on the hiring manager.",
        })
      )
    ).toBe("Blocked — Waiting on the hiring manager.")
    expect(
      stageStatusText(roleWorkflowFixture("job-1", "blocked"))
    ).toBe("Blocked — the server recorded no reason.")
  })
})

describe.each(Object.entries(ACTIVE_JOURNEY))(
  "job detail at role stage %s",
  (stage, journey) => {
    it(`shows ${journey ?? "no current step"} for ${stage}`, async () => {
      stubFetch({
        workflowsByOpportunity: {
          "job-1": roleWorkflowFixture("job-1", stage as Stage),
        },
      })
      window.location.hash = "#/jobs/job-1"
      render(<App />)

      expect(
        await screen.findByRole("heading", { name: "Backend Engineer" })
      ).toBeDefined()
      if (journey === null) {
        if (stage === "handoff_saved") {
          expect(
            await screen.findByText(
              "Saved for handoff — all seven stages complete."
            )
          ).toBeDefined()
          await waitFor(() =>
            expect(
              document.querySelectorAll('li[aria-current="step"]')
            ).toHaveLength(0)
          )
          expect(document.body.textContent).toContain("(completed)")
        } else {
          expect(await screen.findByText(/Blocked —/)).toBeDefined()
          await waitFor(() =>
            expect(
              document.querySelectorAll('li[aria-current="step"]')
            ).toHaveLength(0)
          )
        }
      } else {
        expect(
          await screen.findByText(`Current stage: ${journey} (${stage})`)
        ).toBeDefined()
        await waitFor(() =>
          expect(currentStepLabel()).toContain(journey)
        )
      }
      expect(
        screen.queryByText("Per-role stage actions are not available yet")
      ).toBeNull()
    })
  }
)

describe("blocked and unselected roles", () => {
  it("shows the blocked reason on the stalled role", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "blocked", {
          blockedReason: "Reference check outstanding.",
        }),
      },
    })
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByText("Blocked — Reference check outstanding.")
    ).toBeDefined()
    await waitFor(() =>
      expect(
        document.querySelectorAll('li[aria-current="step"]')
      ).toHaveLength(0)
    )
  })

  it("shows the no-stage state — not an error — for an unselected role", async () => {
    stubFetch({ workflowsByOpportunity: { "job-1": null } })
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(await screen.findByText("No stage recorded")).toBeDefined()
    expect(
      await screen.findByText(
        "This role is not selected, so the server keeps no workflow stage for it. Only chosen roles show progress here."
      )
    ).toBeDefined()
    expect(screen.queryByText("Could not load the application stage")).toBeNull()
    expect(
      document.querySelectorAll('li[aria-current="step"]')
    ).toHaveLength(0)
  })

  it("issues GET reads only while showing live stages", async () => {
    const { calls } = stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "checking"),
      },
    })
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByText("Current stage: Check job details (checking)")
    ).toBeDefined()
    expect(calls.length).toBeGreaterThan(0)
    for (const call of calls) expect(call.method).toBe("GET")
  })
})

describe("chosen-role links", () => {
  it("links to other chosen roles with compact stage labels", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "checking"),
        "job-2": roleWorkflowFixture("job-2", "preparing"),
      },
    })
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByText("Current stage: Check job details (checking)")
    ).toBeDefined()
    const nav = await screen.findByRole("navigation", {
      name: "Other chosen roles",
    })
    const link = nav.querySelector(
      'a[href="#/jobs/job-2"]'
    ) as HTMLAnchorElement | null
    expect(link).not.toBeNull()
    expect(link?.textContent).toContain("Weekend Project")
    expect(link?.textContent).toContain("Preparing")
    expect(nav.querySelector('a[href="#/jobs/job-1"]')).toBeNull()
  })

  it("omits the other-roles nav when no other role is chosen", async () => {
    const { calls } = stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "selected"),
      },
    })
    window.location.hash = "#/jobs/job-1"
    render(<App />)

    expect(
      await screen.findByText("Current stage: Select jobs (selected)")
    ).toBeDefined()
    await waitFor(() =>
      expect(
        calls.some((call) => call.url.includes("/workflow/roles"))
      ).toBe(true)
    )
    await waitFor(() =>
      expect(
        calls.some((call) => call.url.includes("/opportunities?"))
      ).toBe(true)
    )
    expect(
      screen.queryByRole("navigation", { name: "Other chosen roles" })
    ).toBeNull()
  })
})

describe("reload persistence and record independence", () => {
  it("restores the same stage when the deep link reloads", async () => {
    const options = {
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "checking"),
        "job-2": roleWorkflowFixture("job-2", "prepared"),
      },
    }
    stubFetch(options)
    window.location.hash = "#/jobs/job-2"
    const first = render(<App />)
    expect(
      await screen.findByText("Current stage: Prepare materials (prepared)")
    ).toBeDefined()
    await waitFor(() =>
      expect(currentStepLabel()).toContain("Prepare materials")
    )
    first.unmount()
    cleanup()

    stubFetch(options)
    window.location.hash = "#/jobs/job-2"
    render(<App />)
    expect(
      await screen.findByText("Current stage: Prepare materials (prepared)")
    ).toBeDefined()
    await waitFor(() =>
      expect(currentStepLabel()).toContain("Prepare materials")
    )
  })

  it("keeps two records independent across list and detail", async () => {
    stubFetch({
      workflowsByOpportunity: {
        "job-1": roleWorkflowFixture("job-1", "checking"),
        "job-2": roleWorkflowFixture("job-2", "prepared"),
      },
    })
    window.location.hash = "#/jobs"
    render(<App />)

    fireEvent.click(
      await screen.findByRole("tab", { name: /Not yet classified/ })
    )
    const checking = (await screen.findByRole("link", {
      name: "Open Backend Engineer at stage Checking",
    })) as HTMLAnchorElement
    expect(checking.getAttribute("href")).toBe("#/jobs/job-1")
    const prepared = (await screen.findByRole("link", {
      name: "Open Weekend Project at stage Prepared",
    })) as HTMLAnchorElement
    expect(prepared.getAttribute("href")).toBe("#/jobs/job-2")

    go("#/jobs/job-1")
    expect(
      await screen.findByText("Current stage: Check job details (checking)")
    ).toBeDefined()

    go("#/jobs/job-2")
    expect(
      await screen.findByText(
        "Current stage: Prepare materials (prepared)"
      )
    ).toBeDefined()
    await waitFor(() =>
      expect(currentStepLabel()).toContain("Prepare materials")
    )
  })

  it("shows no stage label for unselected roles on the Jobs list", async () => {
    const { calls } = stubFetch({ workflowsByOpportunity: {} })
    window.location.hash = "#/jobs"
    render(<App />)

    fireEvent.click(
      await screen.findByRole("tab", { name: /Not yet classified/ })
    )
    expect(
      await screen.findByRole("link", { name: "Backend Engineer" })
    ).toBeDefined()
    expect(
      await screen.findByRole("link", { name: "Weekend Project" })
    ).toBeDefined()
    await waitFor(() =>
      expect(
        calls.some((call) => call.url.includes("/workflow/roles"))
      ).toBe(true)
    )
    expect(screen.queryByRole("link", { name: /at stage / })).toBeNull()
  })
})
