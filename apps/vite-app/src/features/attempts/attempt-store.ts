// Stable per-role index of delivery review identities. The backend exposes
// exact immutable reviews only by review id (GET /delivery/reviews/{id});
// this index remembers which reviews attempted a given role so Applications
// can re-read the exact attempted snapshots after return or reload, even
// when newer materials exist. Reads only; the server remains the source of
// truth for review contents and outcomes.

import type { DeliveryReview } from "@/api/client"

export const ATTEMPT_REVIEWS_KEY = "jobseek.attempt-reviews.v1"
export const MAX_ATTEMPT_REVIEWS_PER_ROLE = 20

type StorageLike = Pick<Storage, "getItem" | "setItem" | "removeItem">

function activeStorage(override?: StorageLike | null): StorageLike | null {
  if (override !== undefined) return override
  try {
    if (typeof localStorage === "undefined") return null
    return localStorage
  } catch {
    return null
  }
}

function readIndex(storage: StorageLike | null): Record<string, string[]> {
  if (storage === null) return {}
  let raw: string | null
  try {
    raw = storage.getItem(ATTEMPT_REVIEWS_KEY)
  } catch {
    return {}
  }
  if (raw === null || raw === "") return {}
  try {
    const parsed: unknown = JSON.parse(raw)
    if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed))
      return {}
    const index: Record<string, string[]> = {}
    for (const [roleId, ids] of Object.entries(
      parsed as Record<string, unknown>
    )) {
      if (roleId === "" || !Array.isArray(ids)) continue
      const clean = ids.filter(
        (id): id is string => typeof id === "string" && id !== ""
      )
      if (clean.length > 0) index[roleId] = clean.slice(0, MAX_ATTEMPT_REVIEWS_PER_ROLE)
    }
    return index
  } catch {
    return {}
  }
}

function writeIndex(storage: StorageLike | null, index: Record<string, string[]>): void {
  if (storage === null) return
  try {
    storage.setItem(ATTEMPT_REVIEWS_KEY, JSON.stringify(index))
  } catch {
    // The in-memory read still works for this page view.
  }
}

/** Review ids that attempted this role, most recent first. Never throws. */
export function listAttemptReviews(
  opportunityId: string,
  storage?: StorageLike | null
): string[] {
  if (opportunityId === "") return []
  return readIndex(activeStorage(storage))[opportunityId] ?? []
}

/**
 * Remember that a review attempted a role. Idempotent: re-recording moves
 * the review to the front without duplicating it. Never throws.
 */
export function recordAttemptReview(
  opportunityId: string,
  reviewId: string,
  storage?: StorageLike | null
): string[] {
  if (opportunityId === "" || reviewId === "") return listAttemptReviews(opportunityId, storage)
  const backend = activeStorage(storage)
  const index = readIndex(backend)
  const next = [
    reviewId,
    ...(index[opportunityId] ?? []).filter((id) => id !== reviewId),
  ].slice(0, MAX_ATTEMPT_REVIEWS_PER_ROLE)
  index[opportunityId] = next
  writeIndex(backend, index)
  return next
}

/**
 * Remember every role a review attempted. Call after a send (or when the
 * immediate post-send outcome view loads) so each role's history can re-read
 * the exact attempt later. Reviews may batch up to three roles. Never throws.
 */
export function recordReviewAttempts(
  review: DeliveryReview,
  storage?: StorageLike | null
): void {
  const seen = new Set<string>()
  for (const item of review.items) {
    if (item.opportunityId === "" || seen.has(item.opportunityId)) continue
    seen.add(item.opportunityId)
    recordAttemptReview(item.opportunityId, review.id, storage)
  }
}
