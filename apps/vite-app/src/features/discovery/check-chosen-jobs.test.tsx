// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react"
import type { CheckStatusView } from "@/api/client"
import { SessionProvider, useSession } from "@/api/session"
import {
  CheckChosenJobs,
  type ChosenRoleInput,
} from "@/features/discovery/check-chosen-jobs"
import { sessionFixture } from "@/pages/fixtures"

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

const chosenRoles: ChosenRoleInput[] = [
  {
    jobId: "job-a",
    title: "Backend Engineer",
    opportunityRevision: 2,
    workflowRevision: 1,
  },
  {
    jobId: "job-b",
    title: "Support Engineer",
    opportunityRevision: 3,
    workflowRevision: 4,
  },
]

function stubCheckFetch(
  postCheck: (jobId: string, body: unknown) => Response = () =>
    jsonResponse(201, { status: "checking" } satisfies CheckStatusView)
): { calls: FetchCall[] } {
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
      const match = path.match(
        /^\/api\/v1\/opportunities\/([^/]+)\/checks$/
      )
      if (match?.[1] !== undefined && method === "POST") {
        const body =
          typeof init?.body === "string" ? JSON.parse(init.body) : null
        return postCheck(decodeURIComponent(match[1]), body)
      }
      return jsonResponse(404, { error: { message: "Not found." } })
    }
  )
  vi.stubGlobal("fetch", fetchMock)
  return { calls }
}

function renderAction(roles: ChosenRoleInput[] = chosenRoles) {
  return render(
    <SessionProvider>
      <CheckChosenJobs roles={roles} />
    </SessionProvider>
  )
}

// GroupedJobs only mounts the action once a session exists; tests that click
// must wait for the same gate or the D2 handler correctly refuses to start.
function SessionProbe() {
  const { session } = useSession()
  return (
    <p>
      {session === undefined
        ? "session loading"
        : session === null
          ? "signed out"
          : "signed in"}
    </p>
  )
}

async function renderReadyAction(roles: ChosenRoleInput[] = chosenRoles) {
  const rendered = render(
    <SessionProvider>
      <SessionProbe />
      <CheckChosenJobs roles={roles} />
    </SessionProvider>
  )
  await screen.findByText("signed in")
  return rendered
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("CheckChosenJobs", () => {
  it("stays fixed at the list bottom and starts nothing on render", () => {
    const { calls } = stubCheckFetch()
    const { container } = renderAction()

    const bar = screen.getByRole("region", { name: "Check chosen jobs" })
    expect(bar.className).toContain("sticky")
    expect(bar.className).toContain("bottom-0")
    expect(container).toBeDefined()
    expect(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    ).toBeDefined()
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
  })

  it("links each chosen role to its check page", () => {
    stubCheckFetch()
    renderAction()

    for (const role of chosenRoles) {
      const link = screen.getByRole("link", {
        name: `Open check page for ${role.title}`,
      }) as HTMLAnchorElement
      expect(link.getAttribute("href")).toBe(
        `#/jobs/${encodeURIComponent(role.jobId)}/check`
      )
    }
  })

  it("starts checks only for chosen roles on explicit click", async () => {
    const { calls } = stubCheckFetch()
    await renderReadyAction()

    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    )
    await screen.findAllByText(/Check pending/)

    const posts = calls.filter((call) => call.method === "POST")
    expect(posts).toHaveLength(2)
    expect(new Set(posts.map((call) => call.url))).toEqual(
      new Set([
        "/api/v1/opportunities/job-a/checks",
        "/api/v1/opportunities/job-b/checks",
      ])
    )
    for (const post of posts) {
      const payload = JSON.parse(post.body ?? "{}") as Record<string, unknown>
      expect(typeof payload["requestKey"]).toBe("string")
      expect((payload["requestKey"] as string).length).toBeGreaterThan(0)
    }
    const payloadA = JSON.parse(
      posts.find((call) => call.url.includes("job-a"))?.body ?? "{}"
    ) as Record<string, unknown>
    expect(payloadA["expectedOpportunityRevision"]).toBe(2)
    expect(payloadA["expectedWorkflowRevision"]).toBe(1)
    const payloadB = JSON.parse(
      posts.find((call) => call.url.includes("job-b"))?.body ?? "{}"
    ) as Record<string, unknown>
    expect(payloadB["expectedOpportunityRevision"]).toBe(3)
    expect(payloadB["expectedWorkflowRevision"]).toBe(4)
  })

  it("keeps per-role outcomes independent across blocked and pending results", async () => {
    const { calls } = stubCheckFetch((jobId) =>
      jobId === "job-b"
        ? jsonResponse(409, {
            error: { message: "Check revision conflict; refresh first." },
          })
        : jsonResponse(201, { status: "checking" } satisfies CheckStatusView)
    )
    await renderReadyAction()

    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    )

    await screen.findByText("Check revision conflict; refresh first.")
    // The pending role still resolves independently of the blocked one.
    await screen.findByText(/Check pending/)
    expect(
      calls.filter(
        (call) =>
          call.method === "POST" &&
          call.url === "/api/v1/opportunities/job-a/checks"
      )
    ).toHaveLength(1)
    expect(
      calls.filter(
        (call) =>
          call.method === "POST" &&
          call.url === "/api/v1/opportunities/job-b/checks"
      )
    ).toHaveLength(1)

    // Per-role retry replays only the failed role.
    fireEvent.click(
      screen.getByRole("button", { name: "Retry check for Support Engineer" })
    )
    await waitFor(() => {
      expect(
        calls.filter(
          (call) =>
            call.method === "POST" &&
            call.url === "/api/v1/opportunities/job-b/checks"
        )
      ).toHaveLength(2)
    })
    expect(
      calls.filter(
        (call) =>
          call.method === "POST" &&
          call.url === "/api/v1/opportunities/job-a/checks"
      )
    ).toHaveLength(1)
  })

  it("shows a blocked outcome with a check-page link instead of failing the batch", async () => {
    stubCheckFetch(() =>
      jsonResponse(201, { status: "blocked" } satisfies CheckStatusView)
    )
    await renderReadyAction()

    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    )

    const labels = await screen.findAllByText(/Check blocked/)
    expect(labels).toHaveLength(2)
    expect(screen.getAllByRole("link", { name: "Open check" })).toHaveLength(
      2
    )
  })

  it("disables the action with no chosen roles and posts nothing", () => {
    const { calls } = stubCheckFetch()
    renderAction([])

    const button = screen.getByRole("button", {
      name: "Check chosen jobs (0)",
    }) as HTMLButtonElement
    expect(button.disabled).toBe(true)
    fireEvent.click(button)
    expect(calls.filter((call) => call.method === "POST")).toEqual([])
    expect(
      screen.getByText("No chosen roles yet. Select a role above to enable checks.")
    ).toBeDefined()
  })

  it("keeps the action keyboard reachable", () => {
    stubCheckFetch()
    renderAction()

    const button = screen.getByRole("button", {
      name: "Check chosen jobs (2)",
    }) as HTMLButtonElement
    expect(button.disabled).toBe(false)
    button.focus()
    expect(document.activeElement).toBe(button)
    const link = screen.getByRole("link", {
      name: "Open check page for Backend Engineer",
    }) as HTMLAnchorElement
    link.focus()
    expect(document.activeElement).toBe(link)
  })

  it("does not auto-start a role selected after an earlier click", async () => {
    const { calls } = stubCheckFetch()
    const { rerender } = render(
      <SessionProvider>
        <SessionProbe />
        <CheckChosenJobs roles={[chosenRoles[0]!]} />
      </SessionProvider>
    )
    await screen.findByText("signed in")

    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (1)" })
    )
    await screen.findByText(/Check pending/)
    expect(
      calls.filter((call) => call.method === "POST")
    ).toHaveLength(1)

    // Selecting another role afterwards only lists it; no new check starts.
    rerender(
      <SessionProvider>
        <SessionProbe />
        <CheckChosenJobs roles={chosenRoles} />
      </SessionProvider>
    )
    await screen.findByRole("button", { name: "Check chosen jobs (2)" })
    expect(
      screen.getByText("Not started — selection alone starts nothing.")
    ).toBeDefined()
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(1)

    // The next explicit click starts the newly chosen role too.
    fireEvent.click(
      screen.getByRole("button", { name: "Check chosen jobs (2)" })
    )
    await screen.findAllByText(/Check pending/)
    expect(calls.filter((call) => call.method === "POST")).toHaveLength(3)
  })
})
