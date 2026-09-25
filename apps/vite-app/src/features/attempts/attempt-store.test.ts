import { describe, expect, it } from "vitest"
import type { DeliveryReview } from "@/api/client"
import {
  ATTEMPT_REVIEWS_KEY,
  MAX_ATTEMPT_REVIEWS_PER_ROLE,
  listAttemptReviews,
  recordAttemptReview,
  recordReviewAttempts,
} from "@/features/attempts/attempt-store"

function memoryStorage(initial: Record<string, string> = {}) {
  const backing = new Map<string, string>(Object.entries(initial))
  return {
    getItem: (key: string) => backing.get(key) ?? null,
    setItem: (key: string, value: string) => {
      backing.set(key, value)
    },
    removeItem: (key: string) => {
      backing.delete(key)
    },
    raw: () => backing.get(ATTEMPT_REVIEWS_KEY),
  }
}

function reviewFixture(id: string, opportunityIds: string[]): DeliveryReview {
  return {
    id,
    materialSha256: "m".repeat(64),
    items: opportunityIds.map((opportunityId, index) => ({
      id: `${id}-item-${index}`,
      reviewId: id,
      packId: `pack-${index}`,
      opportunityId,
      opportunityRevision: 2,
      sourceSha256: "s".repeat(64),
      profileRevision: 3,
      packContentSha256: "p".repeat(64),
      routeId: "route-1",
      routeRevision: 1,
      routeSha256: "r".repeat(64),
      title: "Role",
      companyName: "Example",
      routeExcerpt: "Send here.",
      recipient: "jobs@example.invalid",
      sender: "owner@example.invalid",
      subject: "Application",
      body: "Exact body.",
      attachmentSha256: "a".repeat(64),
      mimeSha256: "m".repeat(64),
      messageId: `message-${index}`,
      state: "accepted_by_smtp" as const,
      current: true,
    })),
  }
}

describe("attempt-store", () => {
  it("returns an empty history for unknown roles", () => {
    expect(listAttemptReviews("job-1", memoryStorage())).toEqual([])
    expect(listAttemptReviews("", memoryStorage())).toEqual([])
  })

  it("records reviews most-recent-first per role", () => {
    const storage = memoryStorage()
    recordAttemptReview("job-1", "review-1", storage)
    recordAttemptReview("job-1", "review-2", storage)
    recordAttemptReview("job-2", "review-3", storage)
    expect(listAttemptReviews("job-1", storage)).toEqual([
      "review-2",
      "review-1",
    ])
    expect(listAttemptReviews("job-2", storage)).toEqual(["review-3"])
  })

  it("re-recording is idempotent and moves the review first", () => {
    const storage = memoryStorage()
    recordAttemptReview("job-1", "review-1", storage)
    recordAttemptReview("job-1", "review-2", storage)
    recordAttemptReview("job-1", "review-1", storage)
    expect(listAttemptReviews("job-1", storage)).toEqual([
      "review-1",
      "review-2",
    ])
  })

  it("caps the per-role history", () => {
    const storage = memoryStorage()
    for (let n = 0; n < MAX_ATTEMPT_REVIEWS_PER_ROLE + 5; n += 1)
      recordAttemptReview("job-1", `review-${n}`, storage)
    const ids = listAttemptReviews("job-1", storage)
    expect(ids).toHaveLength(MAX_ATTEMPT_REVIEWS_PER_ROLE)
    expect(ids[0]).toBe(`review-${MAX_ATTEMPT_REVIEWS_PER_ROLE + 4}`)
  })

  it("tolerates corrupt or foreign stored values", () => {
    expect(
      listAttemptReviews("job-1", memoryStorage({ [ATTEMPT_REVIEWS_KEY]: "{" }))
    ).toEqual([])
    expect(
      listAttemptReviews("job-1", memoryStorage({ [ATTEMPT_REVIEWS_KEY]: "[]" }))
    ).toEqual([])
    const mixed = memoryStorage({
      [ATTEMPT_REVIEWS_KEY]: JSON.stringify({
        "job-1": ["review-1", 7, "", null, "review-2"],
        "": ["review-9"],
        "job-2": "review-3",
      }),
    })
    expect(listAttemptReviews("job-1", mixed)).toEqual([
      "review-1",
      "review-2",
    ])
    expect(listAttemptReviews("job-2", mixed)).toEqual([])
  })

  it("ignores blank identities", () => {
    const storage = memoryStorage()
    recordAttemptReview("", "review-1", storage)
    recordAttemptReview("job-1", "", storage)
    expect(storage.raw()).toBeUndefined()
  })

  it("records every batched role once per review", () => {
    const storage = memoryStorage()
    recordReviewAttempts(
      reviewFixture("review-1", ["job-1", "job-2", "job-1"]),
      storage
    )
    expect(listAttemptReviews("job-1", storage)).toEqual(["review-1"])
    expect(listAttemptReviews("job-2", storage)).toEqual(["review-1"])
  })
})
