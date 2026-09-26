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
  ArtifactReadinessSet,
  CheckStatusView,
  OpportunityView,
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

function readinessFixture(): ArtifactReadinessSet {
  return {
    opportunityId: "job-1",
    checkId: "check-job-1",
    checkStatus: "checked",
    entries: [
      {
        type: "cv",
        required: true,
        state: "ready",
        reason: "Drafted from verified facts.",
        current: {
          id: "artifact-cv-v2",
          opportunityId: "job-1",
          type: "cv",
          version: 2,
          content: "Jane Doe\nSenior Backend Engineer",
          basis: { factIds: ["fact-1"], answerRefs: [], checkSpans: [] },
          createdAt: "2026-09-22T10:00:00Z",
          createdBy: { actorKind: "codex", actorId: "standard" },
        },
      },
      {
        type: "email_subject",
        required: true,
        state: "ready",
        reason: "Drafted from the vacancy title.",
        current: {
          id: "artifact-subject-v1",
          opportunityId: "job-1",
          type: "email_subject",
          version: 1,
          content: "Application: Backend Engineer (Jane Doe)",
          basis: { factIds: [], answerRefs: [], checkSpans: [] },
          createdAt: "2026-09-22T10:00:00Z",
          createdBy: { actorKind: "codex", actorId: "standard" },
        },
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
  }
}

interface HandoffStubOptions {
  workflow?: boolean
  check?: CheckStatusView | null
  set?: ArtifactReadinessSet
}

function stubHandoffFetch(options: HandoffStubOptions = {}): {
  calls: FetchCall[]
} {
  const calls: FetchCall[] = []
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
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/opportunities/job-1" && method === "GET")
        return jsonResponse(200, opportunityFixture)
      if (path === "/api/v1/opportunities/job-1/workflow" && method === "GET")
        return options.workflow === false
          ? jsonResponse(404, { error: { message: "Role is not selected." } })
          : jsonResponse(200, roleWorkflowFixture("job-1", "handoff_saved"))
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
      if (
        path === "/api/v1/opportunities/job-1/artifacts" &&
        method === "GET"
      )
        return jsonResponse(200, options.set ?? readinessFixture())
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
      "Download tailored CV v2 from Prepare and attach the file yourself."
    )
    within(checklist).getByText(
      "Fill the portal form fields yourself with the 1 ready value below."
    )
    within(checklist).getByText(
      "Motivation email: Held: motivation email needs a saved answer."
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

  it("shows unselected roles without check or artifact state", async () => {
    stubHandoffFetch({ workflow: false })
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
})
