import { useEffect, useRef, useState } from "react"
import {
  RequestError,
  getDeliveryCapability,
  getDeliveryReview,
  isUnauthenticated,
  reconcileDeliveryReview,
  sendDeliveryReview,
  type DeliveryCapability,
  type DeliveryItem,
  type DeliveryReview,
  type Round,
} from "@/api/client"
import { useSession } from "@/api/session"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { ErrorBlock } from "@/components/shared"
import { attemptOutcomeHash, recordAttemptReview } from "@/features/attempts"

// Persisted send intent, mirroring the apps/web delivery panel. The server
// dedupes sends on `delivery:<reviewId>`, so the review id is the stable
// request identity: every replay of a send intent reuses it, and a lost
// response never invents a fresh identity.
const SEND_REQUESTED_KEY = "jobseek.delivery-send-requested"

function readSendRequested(): string | null {
  try {
    return window.localStorage.getItem(SEND_REQUESTED_KEY)
  } catch {
    return null
  }
}

function saveSendRequested(reviewId: string): void {
  try {
    window.localStorage.setItem(SEND_REQUESTED_KEY, reviewId)
  } catch {
    // The in-memory guard still prevents duplicate submissions.
  }
}

function requestMessage(cause: unknown): string {
  return cause instanceof Error ? cause.message : "The request could not be completed."
}

// SMTP acceptance is never described as confirmed receipt, and per-item
// outcomes stay visible so partial results are never collapsed away.
export function sendOutcomeText(state: DeliveryItem["state"]): string {
  switch (state) {
    case "accepted_by_smtp":
      return "Accepted by the mail server, not confirmed receipt."
    case "failed":
      return "Submission failed."
    case "uncertain":
      return "Submission outcome unknown. It may have been accepted; do not send again."
    case "sending":
      return "Submission in progress. Its outcome is not yet known."
    default:
      return "Prepared; no submission attempted."
  }
}

function smtpDetail(item: DeliveryItem): string | null {
  const parts: string[] = []
  if (item.smtpStage !== undefined && item.smtpStage !== "") {
    parts.push(`stage ${item.smtpStage}`)
  }
  if (item.smtpCode !== undefined) {
    parts.push(`code ${item.smtpCode}`)
  }
  if (item.outcomeDetail !== undefined && item.outcomeDetail !== "") {
    parts.push(item.outcomeDetail)
  }
  return parts.length > 0 ? parts.join(" · ") : null
}

function isApproved(review: DeliveryReview): boolean {
  return review.approvedAt !== undefined && review.approvedAt !== null
}

export function SendReview({
  jobId,
  review,
  onReviewChange,
}: {
  jobId: string
  review: DeliveryReview
  onReviewChange: (review: DeliveryReview) => void
}) {
  const { session, loseSession } = useSession()
  const [round, setRound] = useState<Round | null>(null)
  const [busy, setBusy] = useState<"send" | "reconcile" | "read" | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  // A previously requested send for this review id has an unknown outcome
  // until the current status is read back from the server.
  const [mustRefreshFirst, setMustRefreshFirst] = useState<boolean>(
    () => readSendRequested() === review.id
  )
  const sendInFlight = useRef(false)
  // Delivery capability is advisory only: a failed read fails open and the
  // send POST itself stays the enforcement point (its 503 path already
  // reports "nothing was sent"). Only an explicit submissionAvailable:false
  // hides the send action.
  const [capability, setCapability] = useState<DeliveryCapability | null>(null)

  useEffect(() => {
    const controller = new AbortController()
    getDeliveryCapability(controller.signal).then(
      (loaded) => {
        if (!controller.signal.aborted) setCapability(loaded)
      },
      () => {
        // Fail open: no banner, send stays offered, server decides.
      }
    )
    return () => controller.abort()
  }, [])

  const csrfToken = session === undefined || session === null ? null : session.csrfToken
  const approved = isApproved(review)
  const items = review.items
  const allPrepared =
    items.length > 0 && items.every((item) => item.current && item.state === "prepared")
  const attempted = items.some((item) => item.state !== "prepared")
  const anyUncertain = items.some((item) => item.state === "uncertain")
  const sendingUnavailable = capability !== null && !capability.submissionAvailable
  const receiptLookupUnsupported = capability !== null && !capability.receiptLookup
  const canSend =
    approved &&
    allPrepared &&
    !mustRefreshFirst &&
    !sendingUnavailable &&
    busy === null &&
    csrfToken !== null

  async function runSend() {
    if (busy !== null || sendInFlight.current) return
    if (csrfToken === null || !approved || !allPrepared || mustRefreshFirst) return
    sendInFlight.current = true
    setBusy("send")
    setError(null)
    setNotice(null)
    try {
      // Record the stable identity before the request so a lost response
      // still replays this exact review instead of preparing a fresh one.
      saveSendRequested(review.id)
      const result = await sendDeliveryReview(review.id, csrfToken)
      setRound(result.round)
      recordAttemptReview(jobId, result.review.id)
      onReviewChange(result.review)
      setNotice(
        "Submission attempt recorded. Read each item outcome below; SMTP acceptance is not confirmed receipt."
      )
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      if (cause instanceof RequestError && cause.status === 409) {
        // The material may have changed or a send may already be in
        // progress; either way the current status must be read first.
        setMustRefreshFirst(true)
        setError(
          "The service refused this send: the material may have changed or a send may already be in progress. Read the current status before deciding what to do next."
        )
      } else if (
        cause instanceof RequestError &&
        (cause.status === 400 || cause.status === 422 || cause.status === 503)
      ) {
        setError(`The service refused this send (${cause.message}). Nothing was sent.`)
      } else {
        // The response was lost or the server failed without a verdict: the
        // send may have been recorded, so replay is blocked until a read.
        setMustRefreshFirst(true)
        setError(
          `${requestMessage(cause)} The send may have reached the server, so its outcome is uncertain. Read the current status before deciding what to do next.`
        )
      }
    } finally {
      sendInFlight.current = false
      setBusy(null)
    }
  }

  async function runReadStatus() {
    if (busy !== null) return
    setBusy("read")
    setError(null)
    setNotice(null)
    try {
      const latest = await getDeliveryReview(review.id)
      onReviewChange(latest)
      setMustRefreshFirst(false)
      setNotice("Current status refreshed from the server. This read never sends.")
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(requestMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  async function runReconcile() {
    if (busy !== null || csrfToken === null) return
    setBusy("reconcile")
    setError(null)
    setNotice(null)
    try {
      // Reconciliation only reads: the adapter reports what it can verify
      // and this path never submits or retries a delivery.
      const result = await reconcileDeliveryReview(review.id, csrfToken)
      const latest = await getDeliveryReview(review.id)
      onReviewChange(latest)
      setNotice(
        result.supported ? result.reason : `Read-only receipt check unsupported: ${result.reason}`
      )
    } catch (cause) {
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(requestMessage(cause))
    } finally {
      setBusy(null)
    }
  }

  if (!approved) return null

  return (
    <div className="mt-2 flex min-w-0 flex-col gap-3">
      {round !== null && (
        <p className="text-sm">
          Attempt recorded as round {round.id} (state {round.state}).
        </p>
      )}
      {attempted ? (
        <p className="text-muted-foreground text-sm">
          A submission was attempted for this review. The per-item outcomes below are the
          record; sending again from this review is disabled.{" "}
          <a
            href={attemptOutcomeHash(jobId, review.id)}
            className="font-medium underline underline-offset-4 outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
          >
            View attempt outcome
          </a>
        </p>
      ) : (
        <p className="text-muted-foreground text-sm">
          One send intent per review. Repeated clicks reuse this same review and cannot
          submit twice.
        </p>
      )}

      <ul aria-label="Per-item send outcomes" className="space-y-2">
        {items.map((item) => {
          const detail = smtpDetail(item)
          return (
            <li key={item.id} className="rounded-md border p-3 text-sm">
              <span className="font-medium">
                {item.title === "" ? item.packId : item.title} · {item.companyName}
              </span>
              <span className="text-muted-foreground block text-xs">
                {item.recipient}
                {item.current ? "" : ` · stale (${item.blockingReason ?? "superseded"})`}
              </span>
              <span role="status" className="mt-1 block">
                {sendOutcomeText(item.state)}
              </span>
              {detail !== null && (
                <span className="text-muted-foreground block text-xs">{detail}</span>
              )}
            </li>
          )
        })}
      </ul>

      {sendingUnavailable ? (
        <ErrorBlock
          title="Sending unavailable"
          message={
            capability?.reason === undefined || capability.reason === ""
              ? "The server reports no sender is configured. Nothing can be sent from this review."
              : `The server reports sending is unavailable: ${capability.reason}`
          }
        />
      ) : null}
      {allPrepared && !mustRefreshFirst && !sendingUnavailable && (
        <div>
          <Button type="button" onClick={() => void runSend()} disabled={!canSend}>
            {busy === "send" ? "Sending…" : "Send approved review"}
          </Button>
        </div>
      )}
      {mustRefreshFirst && allPrepared && (
        <Alert>
          <AlertTitle>Status read required</AlertTitle>
          <AlertDescription>
            A send was requested for this review and its outcome is unknown. Read the
            current status before sending.
          </AlertDescription>
        </Alert>
      )}
      {(mustRefreshFirst || attempted) && (
        <div>
          <Button
            type="button"
            variant="secondary"
            onClick={() => void runReadStatus()}
            disabled={busy !== null}
          >
            {busy === "read" ? "Reading…" : "Read current status"}
          </Button>
        </div>
      )}
      {anyUncertain && (
        <div className="flex min-w-0 flex-col gap-2">
          <div>
            <Button
              type="button"
              variant="secondary"
              onClick={() => void runReconcile()}
              disabled={busy !== null || csrfToken === null}
            >
              {busy === "reconcile" ? "Reconciling…" : "Reconcile uncertain outcome"}
            </Button>
          </div>
          <p className="text-muted-foreground text-xs">
            Read-only: reports what the mail adapter can verify without resending.
            {receiptLookupUnsupported
              ? " Receipt lookup is not supported on this server, so verification may report unsupported."
              : ""}
          </p>
        </div>
      )}

      {notice === null ? null : (
        <Alert>
          <AlertTitle>Send status</AlertTitle>
          <AlertDescription>{notice}</AlertDescription>
        </Alert>
      )}
      {error === null ? null : (
        <Alert variant="destructive">
          <AlertTitle>Send failed</AlertTitle>
          <AlertDescription>{error}</AlertDescription>
        </Alert>
      )}
      {csrfToken === null ? (
        <ErrorBlock title="Session unavailable" message="Sign in again to send reviews." />
      ) : null}
    </div>
  )
}
