// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import type { DeliveryItem, DeliveryReview } from "@/api/client"
import {
  AttemptOutcomeView,
  attemptOutcomeHash,
} from "@/features/attempts/AttemptOutcome"
import {
  ATTEMPT_REVIEWS_KEY,
  listAttemptReviews,
} from "@/features/attempts/attempt-store"
import { sessionFixture } from "@/pages/fixtures"

afterEach(() => {
  cleanup()
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

const item: DeliveryItem = {
  id: "item-1",
  reviewId: "review-1",
  packId: "pack-1",
  opportunityId: "job-1",
  opportunityRevision: 2,
  sourceSha256: "s".repeat(64),
  profileRevision: 3,
  packContentSha256: "attempted-pack-sha-256",
  routeId: "route-1",
  routeRevision: 1,
  routeSha256: "r".repeat(64),
  title: "Backend Engineer",
  companyName: "Example",
  routeExcerpt: "Send to jobs@example.invalid",
  recipient: "jobs@example.invalid",
  sender: "owner@example.invalid",
  subject: "Application: Backend Engineer",
  body: "Exact attempted body.",
  attachmentSha256: "a".repeat(64),
  mimeSha256: "m".repeat(64),
  messageId: "message-1",
  state: "failed",
  attemptId: "attempt-1",
  outcomeDetail: "connection refused",
  current: true,
}

const review: DeliveryReview = {
  id: "review-1",
  materialSha256: "m".repeat(64),
  items: [item],
}

function stubApi(status: number, body: unknown) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith("/auth/session")) return json(200, sessionFixture)
      if (url.endsWith("/delivery/reviews/review-1")) return json(status, body)
      if (url.includes("/application-packs/"))
        return json(200, {
          id: "pack-1",
          version: 2,
          createdAt: "2026-09-20T10:00:00Z",
        })
      return json(404, { error: { message: "unexpected request" } })
    })
  )
}

function renderOutcome(jobId = "job-1", reviewId = "review-1") {
  render(
    <SessionProvider>
      <AttemptOutcomeView jobId={jobId} reviewId={reviewId} />
    </SessionProvider>
  )
}

describe("AttemptOutcomeView", () => {
  it("builds the stable post-send hash", () => {
    expect(attemptOutcomeHash("job-1", "review-1")).toBe(
      "#/applications/job-1/attempts/review-1"
    )
  })

  it("shows the immediate outcome and remembers the attempt for the role", async () => {
    stubApi(200, review)
    renderOutcome()

    await waitFor(() =>
      expect(screen.getByText("Submission outcome")).toBeDefined()
    )
    expect(
      screen.getByText("Submission failed. This review will not be sent again.")
    ).toBeDefined()
    expect(screen.getByText("attempted-pack-sha-256")).toBeDefined()
    expect(screen.getByText("jobs@example.invalid")).toBeDefined()
    await waitFor(() =>
      expect(listAttemptReviews("job-1")).toEqual(["review-1"])
    )
    expect(window.localStorage.getItem(ATTEMPT_REVIEWS_KEY)).toContain(
      "review-1"
    )
  })

  it("reports when the review attempted nothing for this role", async () => {
    stubApi(200, review)
    renderOutcome("job-9")

    await waitFor(() =>
      expect(
        screen.getByText("This review attempted nothing for this role")
      ).toBeDefined()
    )
  })

  it("retries a failed outcome read", async () => {
    stubApi(404, { error: { message: "Delivery review not found." } })
    renderOutcome()

    await waitFor(() =>
      expect(
        screen.getByText("Could not read the submission outcome")
      ).toBeDefined()
    )
    expect(
      screen.getByRole("link", { name: "Back to application" })
    ).toBeDefined()
  })
})
