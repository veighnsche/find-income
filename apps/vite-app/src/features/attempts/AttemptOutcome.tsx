// Immediate post-send outcome view. Reachable right after sending via
// #/applications/:jobId/attempts/:reviewId (see the coordinator route
// snippet in the F4 handoff); it reads the exact review once and shows the
// attempted snapshot and actual service result without waiting for any
// later correspondence/history slice. Reads only.

import { useEffect } from "react"
import { getDeliveryReview } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { AttemptCard } from "@/features/attempts/AttemptCard"
import { recordReviewAttempts } from "@/features/attempts/attempt-store"
import { useRead } from "@/pages/useRead"

/** Hash for the immediate post-send outcome view of one review attempt. */
export function attemptOutcomeHash(jobId: string, reviewId: string): string {
  return `#/applications/${encodeURIComponent(jobId)}/attempts/${encodeURIComponent(reviewId)}`
}

export function AttemptOutcomeView({
  jobId,
  reviewId,
}: {
  jobId: string
  reviewId: string
}) {
  const review = useRead(`attempt:${reviewId}:outcome`, (signal) =>
    getDeliveryReview(reviewId, signal)
  )

  useEffect(() => {
    if (review.status === "ready") recordReviewAttempts(review.data)
  }, [review])

  const backHref = `#/applications/${encodeURIComponent(jobId)}`

  if (review.status === "loading")
    return <LoadingBlock label="Reading submission outcome…" />
  if (review.status === "error")
    return (
      <div className="flex min-w-0 flex-col gap-6">
        <p>
          <a
            href={backHref}
            className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            Back to application
          </a>
        </p>
        <ErrorBlock
          title="Could not read the submission outcome"
          message={review.error}
          onRetry={review.retry}
        />
      </div>
    )

  const items = review.data.items.filter(
    (item) => item.opportunityId === jobId
  )

  return (
    <div className="flex min-w-0 flex-col gap-6">
      <p className="flex gap-4">
        <a
          href={backHref}
          className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
        >
          Back to application
        </a>
      </p>

      <div>
        <h1 className="font-heading text-2xl font-semibold wrap-break-word">
          Submission outcome
        </h1>
        <p className="mt-1 text-sm text-muted-foreground">
          The exact attempted material and the actual recorded service result.
        </p>
      </div>

      {items.length === 0 ? (
        <EmptyBlock
          title="This review attempted nothing for this role"
          description="The review was read successfully, but none of its items belong to this role."
        />
      ) : (
        <ol className="flex min-w-0 flex-col gap-2">
          {items.map((item) => (
            <li key={item.id}>
              <AttemptCard item={item} />
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}
