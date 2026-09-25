// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import { ReviewPage } from "@/features/review/ReviewPage"
import { jobOneFixture, packOneFixture, sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
})

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const packDetail = {
  ...packOneFixture,
  manifest: {
    role: {
      opportunityId: "job-one",
      opportunityRevision: 3,
      profileRevision: 7,
      title: "Synthetic Research Role",
      company: "Harbor Analytics",
      sourceUrl: "https://example.invalid/jobs/1",
      description: "Research.",
      destination: "jobs@example.invalid",
    },
    sources: [],
    draft: { focus: { text: "Focus" }, cover: [], answers: [], materialUnknowns: [], relevance: [] },
    templateSha256: "t".repeat(64),
  },
}

function stubReview(overrides: { answers?: number; materials?: number } = {}) {
  const calls: string[] = []
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(`${url}`)
      if (url.endsWith("/auth/session")) return json(200, sessionFixture)
      if (url.endsWith("/opportunities/job-one")) return json(200, jobOneFixture)
      if (url.endsWith("/opportunities/job-one/routes"))
        return json(200, {
          items: [
            {
              id: "route-1",
              opportunityId: "job-one",
              kind: "direct",
              destinationText: "jobs@example.invalid",
              revision: 1,
              createdAt: "2026-09-24T00:00:00Z",
              updatedAt: "2026-09-24T00:00:00Z",
              sourceKind: "captured",
              sourceExcerpt: "Send to jobs@example.invalid",
              observedAt: "2026-09-24T00:00:00Z",
            },
          ],
        })
      if (url.endsWith("/opportunities/job-one/application-packs")) return json(200, { items: [packOneFixture] })
      if (url.includes("/application-packs/")) return json(200, packDetail)
      if (url.endsWith("/answers/current"))
        return overrides.answers === 503
          ? json(503, { error: { message: "unavailable" } })
          : json(200, {
              checkId: "check-1",
              questionSetSha256: "s",
              values: [
                {
                  questionId: "q-1",
                  questionTextSha256: "h",
                  required: "required",
                  version: 2,
                  state: "answered",
                  text: "Exact owner text.",
                  textSha256: "t",
                  provenance: {
                    origin: "owner_written",
                    editedAt: "2026-09-24T00:00:00Z",
                    editedBy: { actorKind: "administrator", actorId: "owner" },
                  },
                  updatedAt: "2026-09-24T00:00:00Z",
                },
              ],
            })
      if (url.endsWith("/materials/current"))
        return overrides.materials === 503
          ? json(503, { error: { message: "unavailable" } })
          : json(200, {
              status: "prepared",
              current: {
                packId: "pack-1",
                version: 1,
                opportunityId: "job-one",
                opportunityRevision: 3,
                profileRevision: 7,
                checkId: "check-1",
                questionSetSha256: "s",
                answers: [],
                readiness: { ready: true, missingRequired: [], held: [] },
                provenance: { origin: "prepared", sourceShas: [] },
                createdAt: "2026-09-24T00:00:00Z",
                createdBy: { actorKind: "agent", actorId: "codex" },
              },
            })
      return json(404, { error: { message: "not found" } })
    })
  )
  return calls
}

describe("ReviewPage", () => {
  it("renders destination, exact answers, pack, materials, and live review authorization", async () => {
    stubReview()
    render(
      <SessionProvider>
        <ReviewPage jobId="job-one" />
      </SessionProvider>
    )
    await waitFor(() => expect(screen.getByText(/jobs@example\.invalid/)).toBeTruthy())
    expect(screen.getByText("Exact owner text.")).toBeTruthy()
    await waitFor(() => expect(screen.getByText("Pack PDF")).toBeTruthy())
    expect(
      await screen.findByRole("button", { name: "Prepare review" })
    ).toBeTruthy()
    expect(screen.queryByText("Answers not ready")).toBeNull()
  })

  it("shows unavailable blocks for unimplemented answers and materials", async () => {
    stubReview({ answers: 503, materials: 503 })
    render(
      <SessionProvider>
        <ReviewPage jobId="job-one" />
      </SessionProvider>
    )
    await waitFor(() => expect(screen.getByText("Answers not ready")).toBeTruthy())
    expect(screen.getByText("Materials not prepared")).toBeTruthy()
  })
})
