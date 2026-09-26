import { afterEach, describe, expect, it, vi } from "vitest"
import {
  answerClarification,
  CareerSourceNotConnected,
  commitRoleAnswers,
  exportFilename,
  exportFileExtension,
  exportOpportunityArtifact,
  getHealth,
  getOpportunityHandoff,
  getSession,
  getSourcedOwnerContext,
  isCareerSourceNotConnected,
  isUnauthenticated,
  listArtifactVersions,
  listClarifications,
  listResearchRuns,
  listSavedJobs,
  login,
  logout,
  pickRestorableRunId,
  RequestError,
  researchRunOutcome,
  rewriteOpportunityArtifact,
  saveOpportunityHandoff,
  type RunHistoryItem,
  type Session,
} from "./client"
import { sessionExpired } from "./session"

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const session: Session = {
  actorKind: "administrator",
  actorId: "owner",
  csrfToken: "csrf-token",
  expiresAt: "2099-01-01T00:00:00Z",
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe("session restore", () => {
  it("returns the session when authenticated", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, session))
    vi.stubGlobal("fetch", fetch)
    await expect(getSession()).resolves.toEqual(session)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/session",
      expect.objectContaining({ credentials: "same-origin" })
    )
  })

  it("returns null on expired session (401)", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(401, {})))
    await expect(getSession()).resolves.toBeNull()
  })

  it("throws a RequestError when the session read fails", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(500, {})))
    const cause = await getSession().catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(500)
  })
})

describe("authentication", () => {
  it("signs in with the password payload", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, session))
    vi.stubGlobal("fetch", fetch)
    await expect(login("secret")).resolves.toEqual(session)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/login",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ password: "secret" }),
      })
    )
  })

  it("surfaces the server message on invalid credentials", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(401, { error: { message: "Invalid credentials." } }))
    )
    const cause = await login("wrong").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(401)
    expect((cause as RequestError).message).toBe("Invalid credentials.")
    expect(isUnauthenticated(cause)).toBe(true)
  })

  it("signs out with the CSRF token", async () => {
    const fetch = vi.fn(async () => new Response(null, { status: 204 }))
    vi.stubGlobal("fetch", fetch)
    await expect(logout("csrf-token")).resolves.toBeUndefined()
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/auth/logout",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      })
    )
  })
})

describe("failed requests", () => {
  it("keeps envelope details on errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(409, { error: { message: "Conflict.", details: { rev: 2 } } })
      )
    )
    const cause = await login("x").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).message).toBe("Conflict.")
    expect((cause as RequestError).details).toEqual({ rev: 2 })
    expect(isUnauthenticated(cause)).toBe(false)
  })

  it("falls back when the server returns no JSON envelope", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("<html>down</html>", { status: 502 }))
    )
    const cause = await login("x").catch((error: unknown) => error)
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(502)
    expect((cause as RequestError).message).toContain("502")
  })

  it("rejects unexpected health responses", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => jsonResponse(200, { status: "ok", service: "other", version: "1" }))
    )
    await expect(getHealth()).rejects.toThrow("unexpected health response")
  })
})

describe("answer commit", () => {
  it("commits saved answers with the CSRF token and no invented body", async () => {
    const committed = { opportunityId: "job-1", stage: "answered" }
    let seenInit: RequestInit | undefined
    const fetch = vi.fn(
      async (_input: string | URL | Request, init?: RequestInit) => {
        seenInit = init
        return jsonResponse(200, committed)
      }
    )
    vi.stubGlobal("fetch", fetch)
    await expect(commitRoleAnswers("job 1", "csrf-token")).resolves.toEqual(
      committed
    )
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job%201/answers/commit",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      })
    )
    expect(seenInit).not.toHaveProperty("body")
  })

  it("surfaces commit conflicts with the server message", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(409, {
          error: { message: "Required answers are missing." },
        })
      )
    )
    const cause = await commitRoleAnswers("job-1", "csrf-token").catch(
      (error: unknown) => error
    )
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(409)
    expect((cause as RequestError).message).toBe(
      "Required answers are missing."
    )
  })
})

describe("session expiry", () => {
  it("detects expired and live sessions", () => {
    expect(sessionExpired(session, Date.parse("2099-01-02T00:00:00Z"))).toBe(true)
    expect(sessionExpired(session, Date.parse("2026-01-01T00:00:00Z"))).toBe(false)
  })
})

describe("run history (F2 adoption)", () => {
  it("lists research runs scoped to the research outcome", async () => {
    const page = { items: [{ runId: "run-1", state: "paused" }] }
    const fetch = vi.fn(async () => jsonResponse(200, page))
    vi.stubGlobal("fetch", fetch)
    await expect(listResearchRuns({ limit: 5 })).resolves.toEqual(page)
    expect(fetch).toHaveBeenCalledWith(
      `/api/v1/research/runs?outcome=${researchRunOutcome}&limit=5`,
      expect.objectContaining({ credentials: "same-origin" })
    )
  })

  it("passes an explicit cursor through", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, { items: [] }))
    vi.stubGlobal("fetch", fetch)
    await listResearchRuns({ cursor: "cur sor" })
    expect(fetch).toHaveBeenCalledWith(
      `/api/v1/research/runs?outcome=${researchRunOutcome}&cursor=cur+sor`,
      expect.anything()
    )
  })

  it("restores the newest resumable run, else the newest terminal run", () => {
    const item = (runId: string, state: string): RunHistoryItem =>
      ({
        runId,
        requestKey: `key-${runId}`,
        intent: "find_jobs",
        outcome: researchRunOutcome,
        state,
        stopReason: "",
        createdAt: "2026-09-26T09:00:00Z",
        updatedAt: "2026-09-26T10:00:00Z",
      }) as RunHistoryItem
    const items = [item("run-old", "completed"), item("run-new", "paused")]
    expect(pickRestorableRunId(items)).toBe("run-new")
    expect(pickRestorableRunId([items[0]!])).toBe("run-old")
    expect(pickRestorableRunId([])).toBeNull()
    expect(pickRestorableRunId([item("run-x", "mystery")])).toBeNull()
  })
})

describe("sourced owner context (F2 adoption)", () => {
  const context = {
    owner: { kind: "administrator", id: "owner" },
    sourcesConnected: true,
    sources: [],
  }

  it("reads identity plus career sources verbatim", async () => {
    const fetch = vi.fn(async () => jsonResponse(200, context))
    vi.stubGlobal("fetch", fetch)
    await expect(getSourcedOwnerContext()).resolves.toEqual(context)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/research/owner-context",
      expect.objectContaining({ credentials: "same-origin" })
    )
  })

  it("maps a missing endpoint to neutral absence, not a request error", async () => {
    vi.stubGlobal("fetch", vi.fn(async () => jsonResponse(404, {})))
    const cause = await getSourcedOwnerContext().catch(
      (error: unknown) => error
    )
    expect(cause).toBeInstanceOf(CareerSourceNotConnected)
    expect(isCareerSourceNotConnected(cause)).toBe(true)
    expect(isCareerSourceNotConnected(new Error("other"))).toBe(false)
  })

  it("keeps other failures as request errors", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(503, { error: { message: "Sources failed to load." } })
      )
    )
    const cause = await getSourcedOwnerContext().catch(
      (error: unknown) => error
    )
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).status).toBe(503)
  })
})

describe("clarifications, artifacts and handoff (F2/F3 adoption)", () => {
  it("lists and answers clarifications with exact paths and payloads", async () => {
    const list = { items: [{ id: "c-1", status: "open" }] }
    const answered = { id: "c-1", status: "answered" }
    const fetch = vi.fn(async (input: string | URL | Request) => {
      const url = String(input)
      return url.endsWith("/answer")
        ? jsonResponse(200, answered)
        : jsonResponse(200, list)
    })
    vi.stubGlobal("fetch", fetch)
    await expect(listClarifications("job 1")).resolves.toEqual(list)
    await expect(
      answerClarification("job 1", "c-1", { requestKey: "k", text: "2030." }, "csrf-token")
    ).resolves.toEqual(answered)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job%201/clarifications",
      expect.objectContaining({ credentials: "same-origin" })
    )
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job%201/clarifications/c-1/answer",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ requestKey: "k", text: "2030." }),
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      })
    )
  })

  it("rewrites one artifact and lists its versions", async () => {
    const set = { items: [] }
    const versions = { items: [{ version: 1 }] }
    const fetch = vi.fn(async (input: string | URL | Request) => {
      const url = String(input)
      return url.endsWith("/rewrite")
        ? jsonResponse(201, set)
        : jsonResponse(200, versions)
    })
    vi.stubGlobal("fetch", fetch)
    await expect(
      rewriteOpportunityArtifact(
        "job-1",
        "cv",
        { requestKey: "k", expectedVersion: 1 },
        "csrf-token"
      )
    ).resolves.toEqual(set)
    await expect(listArtifactVersions("job-1", "cv")).resolves.toEqual(
      versions
    )
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job-1/artifacts/cv/rewrite",
      expect.objectContaining({
        method: "POST",
        headers: expect.objectContaining({ "X-CSRF-Token": "csrf-token" }),
      })
    )
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job-1/artifacts/cv/versions",
      expect.objectContaining({ credentials: "same-origin" })
    )
  })

  it("exports the shown version with an attributable filename", async () => {
    const fetch = vi.fn(
      async () =>
        new Response("# CV", {
          status: 200,
          headers: { "Content-Type": "text/markdown; charset=utf-8" },
        })
    )
    vi.stubGlobal("fetch", fetch)
    await expect(
      exportOpportunityArtifact("job-1", "cv", 2)
    ).resolves.toEqual({
      text: "# CV",
      mediaType: "text/markdown",
      filename: "job-1-cv-v2.md",
    })
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job-1/artifacts/cv/export?version=2",
      expect.objectContaining({ credentials: "same-origin" })
    )
    await expect(exportOpportunityArtifact("job-1", "form_values", null))
      .resolves.toMatchObject({ filename: "job-1-form_values-live.md" })
    expect(exportFileExtension("text/plain")).toBe("txt")
    expect(exportFileExtension("application/octet-stream")).toBe("txt")
    expect(exportFilename("job-1", "cv", "live", "text/plain")).toBe(
      "job-1-cv-live.txt"
    )
  })

  it("surfaces export failures with the server message", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        jsonResponse(404, { error: { message: "No such version." } })
      )
    )
    const cause = await exportOpportunityArtifact("job-1", "cv", 9).catch(
      (error: unknown) => error
    )
    expect(cause).toBeInstanceOf(RequestError)
    expect((cause as RequestError).message).toBe("No such version.")
  })

  it("reads and saves the per-job handoff basis", async () => {
    const view = { opportunityId: "job-1", items: [] }
    const saved = { opportunityId: "job-1", stage: "handoff_saved" }
    const fetch = vi.fn(async (_input: string | URL | Request, init?: RequestInit) =>
      jsonResponse((init?.method ?? "GET") === "POST" ? 201 : 200, (init?.method ?? "GET") === "POST" ? saved : view)
    )
    vi.stubGlobal("fetch", fetch)
    await expect(getOpportunityHandoff("job-1")).resolves.toEqual(view)
    await expect(
      saveOpportunityHandoff("job-1", { expectedWorkflowRevision: 3 }, "csrf-token")
    ).resolves.toEqual(saved)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job-1/handoff",
      expect.objectContaining({ credentials: "same-origin" })
    )
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/opportunities/job-1/handoff",
      expect.objectContaining({
        method: "POST",
        body: JSON.stringify({ expectedWorkflowRevision: 3 }),
      })
    )
  })

  it("reads the saved-job index passively", async () => {
    const index = {
      items: [
        {
          opportunityId: "job-1",
          title: "Backend Engineer",
          companyName: "Acme",
          checkStatus: "complete",
          items: [],
        },
      ],
    }
    const fetch = vi.fn(async () => jsonResponse(200, index))
    vi.stubGlobal("fetch", fetch)
    await expect(listSavedJobs()).resolves.toEqual(index)
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/saved-jobs",
      expect.objectContaining({ credentials: "same-origin" })
    )
  })
})
