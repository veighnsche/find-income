// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import {
  isRunNotFound,
  restoreRunFromServer,
  RUN_NOT_FOUND,
  RunNotFoundError,
  useServerRun,
} from "@/components/shared/run-restore"
import { sessionFixture } from "@/pages/fixtures"

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubRunFetch() {
  const calls: { url: string; method: string }[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url =
        typeof input === "string"
          ? input
          : input instanceof URL
            ? input.toString()
            : input.url
      const method = (init?.method ?? "GET").toUpperCase()
      calls.push({ url, method })
      const path = new URL(url, "http://localhost").pathname
      if (path === "/api/v1/auth/session")
        return jsonResponse(200, sessionFixture)
      if (path === "/api/v1/research/runs/run-1")
        return jsonResponse(200, {
          runId: "run-1",
          state: "paused",
          savedIds: ["job-1"],
          unresolvedCount: 0,
        })
      if (path.startsWith("/api/v1/research/runs/"))
        return jsonResponse(404, { error: { message: "No such run." } })
      return jsonResponse(404, { error: { message: "Not stubbed." } })
    })
  )
  return calls
}

function RunProbe({ runId }: { runId: string | null }) {
  const run = useServerRun(runId)
  if (run.status === "idle") return <p>run: idle</p>
  if (run.status === "loading") return <p>run: loading</p>
  if (run.status === "error")
    return (
      <p>
        run: error notFound={run.notFound ? "yes" : "no"} {run.error}
      </p>
    )
  return <p>run: ready {run.data.runId}</p>
}

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

describe("server-backed run restore", () => {
  it("restores a deep-linked run from the server with GET reads only", async () => {
    const calls = stubRunFetch()
    render(
      <SessionProvider>
        <RunProbe runId="run-1" />
      </SessionProvider>
    )
    await screen.findByText("run: ready run-1")
    expect(
      calls.filter((call) => call.url.includes("/research/runs/run-1"))
    ).toEqual([{ url: "/api/v1/research/runs/run-1", method: "GET" }])
  })

  it("stays idle without a run id and issues no run request", async () => {
    const calls = stubRunFetch()
    render(
      <SessionProvider>
        <RunProbe runId={null} />
      </SessionProvider>
    )
    await screen.findByText("run: idle")
    expect(calls.some((call) => call.url.includes("/research/runs/"))).toBe(
      false
    )
  })

  it("reports unknown deep links as run_not_found", async () => {
    stubRunFetch()
    render(
      <SessionProvider>
        <RunProbe runId="missing" />
      </SessionProvider>
    )
    const error = await screen.findByText(/run: error notFound=yes/)
    expect(error.textContent).toContain(RUN_NOT_FOUND)
  })

  it("throws RunNotFoundError for direct restores of unknown ids", async () => {
    stubRunFetch()
    const cause = await restoreRunFromServer("missing").catch(
      (error: unknown) => error
    )
    expect(cause).toBeInstanceOf(RunNotFoundError)
    expect(isRunNotFound(cause)).toBe(true)

    const run = await restoreRunFromServer("run-1")
    expect(run.runId).toBe("run-1")
    expect(isRunNotFound(new Error("boom"))).toBe(false)
  })
})
