import { useCallback, useRef, useState } from "react"
import {
  isUnauthenticated,
  startOpportunityCheck,
  type CheckStartRequest,
  type CheckStatusView,
} from "@/api/client"
import { useSession } from "@/api/session"
import { notifyAccepted } from "@/components/shared/invalidation"

// Request-key generator for explicit check starts. UUIDs keep the key
// opaque and well under the 200-char schema limit; the fallback only
// serves runtimes without crypto.randomUUID.
export function newCheckRequestKey(): string {
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
      // Fall through to the insecure generator below.
    }
  }
  return `check-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

export interface CheckStartRevisions {
  expectedOpportunityRevision: number
  expectedWorkflowRevision: number
}

// Exact POST payload: the requestKey plus the expected revisions read from
// the saved opportunity and workflow records. Exported so C5 (and tests)
// build byte-identical payloads without duplicating the shape.
export function buildCheckStartRequest(
  requestKey: string,
  revisions: CheckStartRevisions
): CheckStartRequest {
  return {
    requestKey,
    expectedOpportunityRevision: revisions.expectedOpportunityRevision,
    expectedWorkflowRevision: revisions.expectedWorkflowRevision,
  }
}

export interface UseCheckStartOptions {
  jobId: string
  opportunityRevision: number | null
  workflowRevision: number | null
  onStarted?: (view: CheckStatusView) => void
}

export interface UseCheckStartResult {
  start: () => void
  starting: boolean
  error: string | null
  canStart: boolean
  unavailableReason: string | null
}

// Explicit check-start action shared by the D2 check page and C5. Mount,
// reads and reloads never call start; only the returned start() issues the
// POST, and only with revisions already observed from GET reads. The
// requestKey is stable per (role, revisions) intent so a failed attempt
// retries as an exact idempotent replay, and resets after success so the
// next distinct start cannot replay the old one.
export function useCheckStart({
  jobId,
  opportunityRevision,
  workflowRevision,
  onStarted,
}: UseCheckStartOptions): UseCheckStartResult {
  const { session, loseSession } = useSession()
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const pendingKeyRef = useRef<{ key: string; intent: string } | null>(null)

  const csrfToken =
    session === undefined || session === null ? null : session.csrfToken
  const revisionsReady =
    opportunityRevision !== null && workflowRevision !== null
  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to start a check."
        : !revisionsReady
          ? "Saved revisions are still loading."
          : null
  const canStart = unavailableReason === null && !starting

  const start = useCallback(() => {
    if (
      csrfToken === null ||
      opportunityRevision === null ||
      workflowRevision === null ||
      starting
    )
      return
    const intent = `${jobId}:${opportunityRevision}:${workflowRevision}`
    const pending = pendingKeyRef.current
    const requestKey =
      pending !== null && pending.intent === intent
        ? pending.key
        : newCheckRequestKey()
    pendingKeyRef.current = { key: requestKey, intent }
    setStarting(true)
    setError(null)
    void startOpportunityCheck(
      jobId,
      buildCheckStartRequest(requestKey, {
        expectedOpportunityRevision: opportunityRevision,
        expectedWorkflowRevision: workflowRevision,
      }),
      csrfToken
    ).then(
      (view) => {
        pendingKeyRef.current = null
        setStarting(false)
        // Accepted write: refresh check/workflow projections everywhere.
        notifyAccepted("check", "workflows")
        onStarted?.(view)
      },
      (cause: unknown) => {
        setStarting(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setError(
          cause instanceof Error
            ? cause.message
            : "The request could not be completed."
        )
      }
    )
  }, [
    csrfToken,
    jobId,
    opportunityRevision,
    workflowRevision,
    starting,
    onStarted,
    loseSession,
  ])

  return { start, starting, error, canStart, unavailableReason }
}
