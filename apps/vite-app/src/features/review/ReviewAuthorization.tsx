import { useRef, useState } from "react"
import {
  RequestError,
  approveDeliveryReview,
  isUnauthenticated,
  prepareDeliveryReview,
  type DeliveryReview,
} from "@/api/client"
import { useSession } from "@/api/session"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { EmptyBlock, ErrorBlock, UnsupportedBlock } from "@/components/shared"

export function newReviewRequestKey(): string {
  const cryptoRef =
    typeof globalThis.crypto === "object" &&
    globalThis.crypto !== null &&
    "randomUUID" in globalThis.crypto
      ? (globalThis.crypto as { randomUUID?: () => string })
      : null
  if (cryptoRef?.randomUUID !== undefined) {
    try {
      return cryptoRef.randomUUID()
    } catch {
      // Fall through to the insecure fallback below.
    }
  }
  return `review-${Date.now().toString(36)}-${Math.floor(Math.random() * 1e9).toString(36)}`
}

function requestMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : "The request could not be completed."
}

function notReadyMessage(materialStatus: string | null): string {
  switch (materialStatus) {
    case "outdated":
      return "This version is outdated. Re-prepare on the Prepare page, then return here to authorize the fresh version."
    case "not_prepared":
      return "No material version is prepared yet. Prepare on the Prepare page first, then return here."
    case "preparing":
      return "Preparation is still running. Reload once it completes, then authorize the new version here."
    case "held":
    default:
      return "Resolve the held or missing required items above. The service rejects held material even if review is requested."
  }
}

export function ReviewAuthorization({
  packId,
  materialReady,
  materialVersion,
  materialStatus = null,
  prepareHref = null,
  onReviewChange,
}: {
  packId: string | null
  materialReady: boolean
  materialVersion: number | null
  materialStatus?: string | null
  prepareHref?: string | null
  onReviewChange?: (review: DeliveryReview) => void
}) {
  const { session, loseSession } = useSession()
  const [review, setReview] = useState<DeliveryReview | null>(null)
  const [busy, setBusy] = useState<"prepare" | "approve" | null>(null)
  const [error, setError] = useState<string | null>(null)
  const keyRef = useRef<string | null>(null)

  if (packId === null) {
    return (
      <UnsupportedBlock
        title="Nothing to authorize yet"
        message="Prepare applications to create the first pack version, then return here to authorize it."
      />
    )
  }
  if (!materialReady) {
    return (
      <div className="mt-2 flex min-w-0 flex-col gap-2">
        <UnsupportedBlock
          title="Materials not ready"
          message={notReadyMessage(materialStatus)}
        />
        {prepareHref === null ? null : (
          <p>
            <a
              href={prepareHref}
              className="text-sm font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
            >
              Open Prepare applications
            </a>
          </p>
        )}
      </div>
    )
  }

  const csrfToken = session === undefined || session === null ? null : session.csrfToken
  const staleItems = review === null ? [] : review.items.filter((item) => !item.current)

  async function runPrepare() {
    if (busy !== null || csrfToken === null) return
    if (keyRef.current === null) keyRef.current = newReviewRequestKey()
    setBusy("prepare")
    setError(null)
    try {
      const prepared = await prepareDeliveryReview(
        { requestKey: keyRef.current, packIds: [packId as string] },
        csrfToken
      )
      keyRef.current = null
      setReview(prepared)
      onReviewChange?.(prepared)
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      if (cause instanceof RequestError && cause.status === 409) {
        setError("The material changed. Refresh the review and prepare again for the current version.")
      } else if (cause instanceof RequestError && cause.status === 400) {
        setError("The service refused this material as not ready. Resolve the held items first.")
      } else {
        setError(requestMessage(cause))
      }
    } finally {
      setBusy(null)
    }
  }

  async function runApprove() {
    if (busy !== null || csrfToken === null || review === null) return
    setBusy("approve")
    setError(null)
    try {
      const approved = await approveDeliveryReview(review.id, review.materialSha256, csrfToken)
      setReview(approved)
      onReviewChange?.(approved)
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      if (cause instanceof RequestError && cause.status === 409) {
        setError("The material changed after this review was prepared. Prepare a fresh review for the current version.")
      } else if (cause instanceof RequestError && cause.status === 400) {
        setError("The service refused this material as not ready. Resolve the held items first.")
      } else {
        setError(requestMessage(cause))
      }
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="mt-2 flex min-w-0 flex-col gap-3">
      {review === null ? (
        <>
          <p className="text-sm text-muted-foreground">
            {materialVersion === null
              ? "Authorization binds the current pack."
              : `Authorization binds material v${materialVersion} and its pack. A newer version needs a fresh review.`}
          </p>
          <div>
            <Button type="button" onClick={() => void runPrepare()} disabled={busy !== null || csrfToken === null}>
              {busy === "prepare" ? "Preparing review…" : "Prepare review"}
            </Button>
          </div>
        </>
      ) : (
        <div className="rounded-md border p-3 text-sm">
          <p>
            <span className="font-medium">Review {review.id}</span>
            <span className="text-muted-foreground block text-xs">
              material sha {review.materialSha256.slice(0, 16)}… ·{" "}
              {review.approvedAt === undefined || review.approvedAt === null
                ? "not approved"
                : `approved ${review.approvedAt}`}
            </span>
          </p>
          <ul className="mt-2 space-y-1">
            {review.items.map((item) => (
              <li key={item.id} className="text-xs">
                <span className="font-medium">{item.packId}</span>
                <span className="text-muted-foreground">
                  {" "}
                  · {item.recipient} · pack sha {item.packContentSha256.slice(0, 12)}… ·{" "}
                  {item.current ? "current" : `stale (${item.blockingReason ?? "superseded"})`}
                </span>
              </li>
            ))}
          </ul>
          {review.approvedAt === undefined || review.approvedAt === null ? (
            <div className="mt-2 flex min-w-0 flex-col gap-2">
              {staleItems.length > 0 ? (
                <p role="status" className="text-muted-foreground text-xs">
                  {staleItems.length} of {review.items.length}{" "}
                  {review.items.length === 1 ? "item is" : "items are"} stale
                  {staleItems.every(
                    (item) => item.blockingReason === staleItems[0]?.blockingReason
                  ) && staleItems[0]?.blockingReason !== undefined
                    ? ` (${staleItems[0].blockingReason})`
                    : ""}
                  . Approving is disabled; prepare a fresh review for the current
                  version instead.
                </p>
              ) : null}
              <div className="flex min-w-0 flex-wrap gap-2">
                <Button
                  type="button"
                  onClick={() => void runApprove()}
                  disabled={busy !== null || csrfToken === null || staleItems.length > 0}
                >
                  {busy === "approve" ? "Approving…" : "Approve this review"}
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => void runPrepare()}
                  disabled={busy !== null || csrfToken === null}
                >
                  {busy === "prepare" ? "Preparing fresh review…" : "Prepare fresh review"}
                </Button>
              </div>
            </div>
          ) : (
            <EmptyBlock
              title="Review approved"
              description="This authorization covers exactly the pack and digest above. Sending is a separate explicit step."
            />
          )}
        </div>
      )}
      {error === null ? null : (
        <Alert variant="destructive">
          <AlertTitle>Authorization failed</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {csrfToken === null ? (
        <ErrorBlock title="Session unavailable" message="Sign in again to authorize reviews." />
      ) : null}
    </div>
  )
}
