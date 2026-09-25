// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from "vitest"
import { cleanup, render, screen, waitFor } from "@testing-library/react"
import { SessionProvider } from "@/api/session"
import type { DeliveryItem, DeliveryReview } from "@/api/client"
import { AttemptHistory } from "@/features/attempts/AttemptHistory"
import { ATTEMPT_REVIEWS_KEY } from "@/features/attempts/attempt-store"
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

function itemFixture(overrides: Partial<DeliveryItem> = {}): DeliveryItem {
  return {
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
    state: "accepted_by_smtp",
    attemptId: "attempt-1",
    smtpStage: "data",
    smtpCode: 250,
    outcomeDetail: "250 OK",
    current: true,
    ...overrides,
  }
}

function reviewFixture(items: DeliveryItem[]): DeliveryReview {
  return { id: "review-1", materialSha256: "m".repeat(64), items }
}

function stubApi(review: DeliveryReview | null, calls: string[]) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (url.endsWith("/auth/session")) return json(200, sessionFixture)
      if (url.endsWith("/delivery/reviews/review-1"))
        return review === null
          ? json(404, { error: { message: "not found" } })
          : json(200, review)
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

function storeReviews(index: Record<string, string[]>) {
  window.localStorage.setItem(ATTEMPT_REVIEWS_KEY, JSON.stringify(index))
}

function renderHistory(jobId: string) {
  render(
    <SessionProvider>
      <AttemptHistory jobId={jobId} />
    </SessionProvider>
  )
}

describe("AttemptHistory", () => {
  it("shows the exact attempted snapshot and actual service result", async () => {
    const calls: string[] = []
    stubApi(reviewFixture([itemFixture()]), calls)
    storeReviews({ "job-1": ["review-1"] })
    renderHistory("job-1")

    await waitFor(() =>
      expect(
        screen.getByText(
          "Accepted by the outgoing SMTP server. Employer receipt is unverified."
        )
      ).toBeDefined()
    )
    await waitFor(() => expect(screen.getByText("Pack v2")).toBeDefined())
    expect(screen.getByText("attempted-pack-sha-256")).toBeDefined()
    expect(screen.getByText("jobs@example.invalid")).toBeDefined()
    expect(screen.getByText(/250 OK/)).toBeDefined()
    expect(
      screen.getByRole("link", { name: "Open attempted pack PDF" })
    ).toBeDefined()
    // The frozen attempt is re-read from the immutable review and pack
    // records; current materials are never consulted.
    expect(
      calls.some((url) => url.includes("/materials/current"))
    ).toBe(false)
    expect(calls.some((url) => url.includes("/materials/"))).toBe(false)
  })

  it("marks the attempt unchanged when newer materials exist", async () => {
    const calls: string[] = []
    stubApi(
      reviewFixture([itemFixture({ current: false, state: "uncertain" })]),
      calls
    )
    storeReviews({ "job-1": ["review-1"] })
    renderHistory("job-1")

    await waitFor(() =>
      expect(
        screen.getByText(
          "Submission outcome unknown. It may have been accepted; do not resend."
        )
      ).toBeDefined()
    )
    expect(
      screen.getByText(
        /Newer materials exist since this attempt\. The snapshot below is the exact attempted version and is unchanged\./
      )
    ).toBeDefined()
  })

  it("shows only this role's items from a batched review", async () => {
    const calls: string[] = []
    stubApi(
      reviewFixture([
        itemFixture(),
        itemFixture({
          id: "item-2",
          packId: "pack-9",
          opportunityId: "job-2",
          recipient: "other@example.invalid",
          packContentSha256: "other-role-sha",
        }),
      ]),
      calls
    )
    storeReviews({ "job-1": ["review-1"] })
    renderHistory("job-1")

    await waitFor(() =>
      expect(screen.getByText("jobs@example.invalid")).toBeDefined()
    )
    expect(screen.queryByText("other@example.invalid")).toBeNull()
    expect(screen.queryByText("other-role-sha")).toBeNull()
  })

  it("reports an empty history before any send", async () => {
    const calls: string[] = []
    stubApi(reviewFixture([itemFixture()]), calls)
    renderHistory("job-1")

    await waitFor(() =>
      expect(
        screen.getByText("No submission attempts recorded for this role")
      ).toBeDefined()
    )
    expect(
      calls.some((url) => url.includes("/delivery/reviews/"))
    ).toBe(false)
  })

  it("keeps stored identities when the server cannot re-read them", async () => {
    const calls: string[] = []
    stubApi(null, calls)
    storeReviews({ "job-1": ["review-1"] })
    renderHistory("job-1")

    await waitFor(() =>
      expect(
        screen.getByText("Could not re-read stored attempts")
      ).toBeDefined()
    )
    expect(window.localStorage.getItem(ATTEMPT_REVIEWS_KEY)).toContain(
      "review-1"
    )
  })
})
