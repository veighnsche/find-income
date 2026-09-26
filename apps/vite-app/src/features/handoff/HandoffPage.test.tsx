// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  within,
} from "@testing-library/react"
import type {
  CheckStatusView,
  HandoffView,
  OpportunityView,
  RoleWorkflowState,
} from "@/api/client"
import { SessionProvider } from "@/api/session"
import { HandoffPage } from "@/features/handoff/HandoffPage"
import {
  roleWorkflowFixture,
  sessionFixture,
} from "@/pages/fixtures"

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

const opportunityFixture: OpportunityView = {
  opportunity: {
    id: "job-1",
    companyId: "company-1",
    title: "Backend Engineer",
    kind: "employment",
    sourceUrl: "https://example.com/jobs/job-1",
    originalText: "Original posting text.",
    notes: "",
    stage: "new",
    workPattern: "remote",
    locationText: "Berlin",
    postedOn: "2026-09-01",
    deadlineOn: "",
    revision: 2,
    createdAt: "2026-09-10T09:00:00Z",
    updatedAt: "2026-09-12T09:00:00Z",
    compensation: {},
  },
  likelyDuplicates: [],
}

type CheckRoute = NonNullable<CheckStatusView["check"]>["route"]

function checkFixture(
  route: CheckRoute,
  status: CheckStatusView["status"] = "checked"
): CheckStatusView {
  return {
    status,
    check: {
      id: "check-job-1",
      opportunityId: "job-1",
      opportunityRevision: 2,
      workflowRevision: 1,
      status: "checked",
      vacancy: {
        captureIds: ["cap-1"],
        evidenceSourceIds: ["src-1"],
        completeness: "complete",
        sourceUrl: "https://example.com/jobs/job-1",
        retrievedAt: "2026-09-20T10:00:00Z",
      },
      requestedDocuments: [],
      route,
      gaps: [],
      questions: [],
      questionSetSha256: "set-sha",
      questionSetVersion: 1,
      createdAt: "2026-09-20T10:00:00Z",
      createdBy: { actorKind: "codex", actorId: "codex" },
    },
  }
}

const portalRoute = {
  judgment: "application_route" as const,
  kind: "direct" as const,
  destinationText: "https://example.com/apply/job-1",
  sourceExcerpt: "Apply through the portal.",
  observedAt: "2026-09-20T10:00:00Z",
}

const emailRoute = {
  judgment: "application_route" as const,
  kind: "direct" as const,
  destinationText: "jobs@example.com",
  sourceExcerpt: "Send your CV to jobs@example.com.",
  observedAt: "2026-09-20T10:00:00Z",
}

const unresolvedRoute = {
  judgment: "unresolved" as const,
  sourceExcerpt: "No application channel found.",
  observedAt: "2026-09-20T10:00:00Z",
}

function handoffFixture(): HandoffView {
  return {
    opportunityId: "job-1",
    title: "Backend Engineer",
    companyName: "Example",
    checkId: "check-job-1",
    checkStatus: "checked",
    workflowStage: "prepared",
    routeKind: "direct",
    routeDestination: "https://example.com/apply/job-1",
    routeExcerpt: "Apply through the portal.",
    items: [
      {
        type: "cv",
        required: true,
        state: "ready",
        reason: "Drafted from verified facts.",
        version: 2,
        content: "Jane Doe\nSenior Backend Engineer",
        contentSha256: "abc123def4567890abc123def4567890",
      },
      {
        type: "email_subject",
        required: true,
        state: "ready",
        reason: "Drafted from the vacancy title.",
        version: 1,
        content: "Application: Backend Engineer (Jane Doe)",
        contentSha256: "subjectsha256subjectsha256subject12",
      },
      {
        type: "email_body",
        required: true,
        state: "held",
        reason: "Held: motivation email needs a saved answer.",
      },
      {
        type: "cover_letter",
        required: false,
        state: "not_required",
        reason: "Email route needs no letter.",
      },
      {
        type: "form_values",
        required: false,
        state: "ready",
        reason: "Derived from saved answers.",
        formValues: [
          {
            questionId: "q-remote",
            questionText: "Can you work remotely?",
            required: "required",
            kind: "free_text",
            state: "answered",
            text: "Yes — remote-first.",
          },
        ],
      },
    ],
    uploads: [
      {
        questionId: "q-cv",
        questionText: "Upload your CV",
        required: "required",
        artifactType: "cv",
        state: "ready",
        version: 2,
        contentSha256: "abc123def4567890abc123def4567890",
      },
      {
        questionId: "q-portfolio",
        questionText: "Portfolio upload",
        required: "optional",
        state: "unresolved",
      },
    ],
  }
}

interface HandoffStubOptions {
  workflow?: RoleWorkflowState | null
  check?: CheckStatusView | null
  handoff?: HandoffView
  handoffConflict?: boolean
}

function stubHandoffFetch(options: HandoffStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
  let liveWorkflow =
    options.workflow === undefined
      ? roleWorkflowFixture("job-1", "prepared", { revision: 3 })
      : options.workflow
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
      if (path === "/api/v1/opportunities/job-1" && method === "GET")
        return jsonResponse(200, opportunityFixture)
      if (path === "/api/v1/opportunities/job-1/workflow" && method === "GET")
        return liveWorkflow === null
          ? jsonResponse(404, { error: { message: "Role is not selected." } })
          : jsonResponse(200, liveWorkflow)
      if (
        path === "/api/v1/opportunities/job-1/checks/current" &&
        method === "GET"
      ) {
        const entry =
          options.check === undefined
            ? checkFixture(portalRoute)
            : options.check
        return entry === null
          ? jsonResponse(404, { error: { message: "Check not found." } })
          : jsonResponse(200, entry)
      }
      if (path === "/api/v1/opportunities/job-1/handoff" && method === "GET")
        return jsonResponse(200, options.handoff ?? handoffFixture())
      if (path === "/api/v1/opportunities/job-1/handoff" && method === "POST") {
        if (options.handoffConflict === true)
          return jsonResponse(409, {
            error: { message: "Workflow moved; reload first." },
          })
        liveWorkflow =
          liveWorkflow === null
            ? liveWorkflow
            : { ...liveWorkflow, stage: "handoff_saved", revision: 4 }
        return jsonResponse(201, liveWorkflow)
      }
      const exportMatch = path.match(
        /^\/api\/v1\/opportunities\/job-1\/artifacts\/([^/]+)\/export$/
      )
      if (exportMatch?.[1] !== undefined && method === "GET") {
        const version = parsed.searchParams.get("version") ?? "live"
        return new Response(`exported:${exportMatch[1]}:v${version}`, {
          status: 200,
          headers: { "Content-Type": "text/plain; charset=utf-8" },
        })
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderHandoff() {
  return render(
    <SessionProvider>
      <HandoffPage jobId="job-1" />
    </SessionProvider>
  )
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
})

describe("HandoffPage", () => {
  it("mounts GET-only with destination, checklist and saved versions", async () => {
    const { calls } = stubHandoffFetch()
    renderHandoff()

    const destination = await screen.findByRole("region", {
      name: "Verified destination",
    })
    const portal = within(destination).getByRole("link", {
      name: "https://example.com/apply/job-1",
    }) as HTMLAnchorElement
    expect(portal.getAttribute("href")).toBe("https://example.com/apply/job-1")
    expect(portal.getAttribute("target")).toBe("_blank")

    const checklist = await screen.findByRole("region", {
      name: "Manual checklist",
    })
    within(checklist).getByText(
      "Copy the email subject (v1) into your email's subject line."
    )
    within(checklist).getByText(
      "Download tailored CV v2 and attach the file yourself."
    )
    within(checklist).getByText(
      "Fill the portal form fields yourself with the 1 ready value below."
    )
    within(checklist).getByText(
      "Attach your downloaded Tailored CV (v2) to “Upload your CV” yourself."
    )
    within(checklist).getByText(
      "Motivation email: Held: motivation email needs a saved answer."
    )
    within(checklist).getByText(
      "Upload for “Portfolio upload”: no file is mapped — see Prepare."
    )
    within(checklist).getByText(
      "You press send or submit in your own email app or browser. The app cannot do this for you."
    )

    const saved = await screen.findByRole("region", {
      name: "Saved artifacts",
    })
    within(saved).getByText("Tailored CV · v2")
    within(saved).getByText("Email subject · v1")
    const full = within(saved).getByRole("link", {
      name: "Open full Tailored CV in Prepare",
    }) as HTMLAnchorElement
    expect(full.getAttribute("href")).toBe("#/jobs/job-1/prepare")

    for (const call of calls) {
      expect(call.method).toBe("GET")
    }
    expect(
      calls.some((call) => call.method !== "GET" || call.url.includes("send"))
    ).toBe(false)
    // No fill/attach/send/submit control exists anywhere on the page.
    for (const button of screen.getAllByRole("button")) {
      expect(button.textContent ?? "").not.toMatch(/fill|attach|send|submit/i)
    }
  })

  it("links an email destination as mailto for the owner to open", async () => {
    stubHandoffFetch({ check: checkFixture(emailRoute) })
    renderHandoff()

    const destination = await screen.findByRole("region", {
      name: "Verified destination",
    })
    const mail = within(destination).getByRole("link", {
      name: "jobs@example.com",
    }) as HTMLAnchorElement
    expect(mail.getAttribute("href")).toBe("mailto:jobs@example.com")
  })

  it("stays honest when no destination was verified", async () => {
    stubHandoffFetch({ check: checkFixture(unresolvedRoute) })
    renderHandoff()

    const destination = await screen.findByRole("region", {
      name: "Verified destination",
    })
    within(destination).getByText("No verified destination")
    within(destination).getByText(
      /The check could not verify an application destination\./
    )
    const listing = within(destination).getByRole("link", {
      name: "https://example.com/jobs/job-1",
    }) as HTMLAnchorElement
    expect(listing.getAttribute("href")).toBe("https://example.com/jobs/job-1")
  })

  it("copies a ready text exactly", async () => {
    stubHandoffFetch()
    const written: string[] = []
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: {
        writeText: vi.fn(async (text: string) => {
          written.push(text)
        }),
      },
    })
    renderHandoff()

    const checklist = await screen.findByRole("region", {
      name: "Manual checklist",
    })
    fireEvent.click(
      within(checklist).getByRole("button", {
        name: "Copy Email subject v1",
      })
    )
    await within(checklist).findByText("Copied.")
    expect(written).toEqual(["Application: Backend Engineer (Jane Doe)"])
  })

  it("downloads a ready file from the canonical export", async () => {
    stubHandoffFetch()
    const blobs: Blob[] = []
    vi.stubGlobal(
      "URL",
      class extends URL {
        static createObjectURL(blob: Blob): string {
          blobs.push(blob)
          return "blob:fake"
        }
        static revokeObjectURL(): void {}
      }
    )
    renderHandoff()

    const checklist = await screen.findByRole("region", {
      name: "Manual checklist",
    })
    fireEvent.click(
      within(checklist).getByRole("button", {
        name: "Download Email subject v1",
      })
    )
    await vi.waitFor(() => expect(blobs).toHaveLength(1))
    expect(await blobs[0]?.text()).toBe("exported:email_subject:v1")
  })

  it("saves the handoff record explicitly and reaches the terminal state", async () => {
    const { calls } = stubHandoffFetch()
    renderHandoff()

    const record = await screen.findByRole("region", {
      name: "Handoff record",
    })
    within(record).getByText(
      /This only records your manual list here so you can revisit it\./
    )
    fireEvent.click(
      within(record).getByRole("button", { name: "Save handoff record" })
    )
    await screen.findByText("Handoff saved")

    const posts = calls.filter(
      (call) =>
        call.method === "POST" &&
        call.url === "/api/v1/opportunities/job-1/handoff"
    )
    expect(posts).toHaveLength(1)
    expect(JSON.parse(posts[0]?.body ?? "{}")).toEqual({
      expectedWorkflowRevision: 3,
    })
    expect(
      screen.queryByRole("button", { name: "Save handoff record" })
    ).toBeNull()
  })

  it("reports a handoff conflict without claiming a save", async () => {
    stubHandoffFetch({ handoffConflict: true })
    renderHandoff()

    const record = await screen.findByRole("region", {
      name: "Handoff record",
    })
    fireEvent.click(
      within(record).getByRole("button", { name: "Save handoff record" })
    )
    await within(record).findByText(
      "These artifacts changed elsewhere. Reload this section, then try again."
    )
    expect(screen.queryByText("Handoff saved")).toBeNull()
  })

  it("holds the save until preparation completes", async () => {
    stubHandoffFetch({
      workflow: roleWorkflowFixture("job-1", "answered", { revision: 2 }),
    })
    renderHandoff()

    const record = await screen.findByRole("region", {
      name: "Handoff record",
    })
    within(record).getByText(/current stage: answered/)
    expect(
      within(record).queryByRole("button", { name: "Save handoff record" })
    ).toBeNull()
  })

  it("shows unselected roles without check or artifact state", async () => {
    stubHandoffFetch({ workflow: null })
    renderHandoff()

    await screen.findByText("Role not selected")
    expect(
      screen.queryByRole("region", { name: "Manual checklist" })
    ).toBeNull()
  })

  it("holds handoff until the check completes", async () => {
    stubHandoffFetch({
      check: { status: "not_checked" },
    })
    renderHandoff()

    await screen.findByText("No check yet")
  })

  it("keeps prior handoff content readable but outdated under a stale check", async () => {
    stubHandoffFetch({ check: checkFixture(portalRoute, "outdated") })
    renderHandoff()

    const saved = await screen.findByRole("region", {
      name: "Saved artifacts",
    })
    const banner = screen.getByRole("status")
    expect(banner.textContent).toContain("role changed after these materials")
    within(saved).getByText("Tailored CV · v2")
    expect(within(saved).getAllByText("Outdated").length).toBeGreaterThan(0)
    expect(within(saved).queryByText("Ready")).toBeNull()

    const checklist = await screen.findByRole("region", {
      name: "Manual checklist",
    })
    within(checklist).getByText(
      "Nothing is ready to send yet. Prepare the required items first."
    )
  })
})
