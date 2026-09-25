// Per-role attempted-material history. Re-reads the exact immutable
// delivery reviews this role attempted (via the stored review identities)
// and shows the attempted snapshot and actual service result for each.
// Reads only; newer material versions never change what is shown here.

import { getDeliveryReview, isUnauthenticated, type DeliveryItem, type DeliveryReview } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { useRead } from "@/pages/useRead"
import { AttemptCard } from "@/features/attempts/AttemptCard"
import { listAttemptReviews } from "@/features/attempts/attempt-store"

interface AttemptHistoryData {
  reviews: DeliveryReview[]
  failedIds: string[]
}

async function loadHistory(
  jobId: string,
  signal: AbortSignal
): Promise<AttemptHistoryData> {
  const ids = listAttemptReviews(jobId)
  const reviews: DeliveryReview[] = []
  const failedIds: string[] = []
  for (const id of ids) {
    try {
      reviews.push(await getDeliveryReview(id, signal))
    } catch (cause) {
      if (isUnauthenticated(cause) || signal.aborted) throw cause
      failedIds.push(id)
    }
  }
  return { reviews, failedIds }
}

function itemsForRole(reviews: DeliveryReview[], jobId: string): DeliveryItem[] {
  return reviews.flatMap((review) =>
    review.items.filter((item) => item.opportunityId === jobId)
  )
}

export function AttemptHistory({ jobId }: { jobId: string }) {
  const history = useRead(`attempts:${jobId}:history`, (signal) =>
    loadHistory(jobId, signal)
  )

  if (history.status === "loading")
    return <LoadingBlock label="Loading attempted submissions…" />
  if (history.status === "error")
    return (
      <ErrorBlock
        title="Could not load attempted submissions"
        message={history.error}
        onRetry={history.retry}
      />
    )

  const items = itemsForRole(history.data.reviews, jobId)
  if (items.length === 0) {
    if (history.data.failedIds.length > 0)
      return (
        <ErrorBlock
          title="Could not re-read stored attempts"
          message={`${history.data.failedIds.length} stored attempt${history.data.failedIds.length === 1 ? " could" : "s could"} not be re-read from the server. The stored identities are kept for another read.`}
          onRetry={history.retry}
        />
      )
    return (
      <EmptyBlock
        title="No submission attempts recorded for this role"
        description="Attempts appear here with their exact attempted material and outcome after the reviewed application is sent."
      />
    )
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      {history.data.failedIds.length > 0 ? (
        <p role="status" className="text-xs text-muted-foreground wrap-break-word">
          {history.data.failedIds.length} stored attempt
          {history.data.failedIds.length === 1 ? " could" : "s could"} not be
          re-read; the attempts below were re-read exactly.
        </p>
      ) : null}
      <ol className="flex min-w-0 flex-col gap-2">
        {items.map((item) => (
          <li key={item.id}>
            <AttemptCard item={item} />
          </li>
        ))}
      </ol>
    </div>
  )
}
