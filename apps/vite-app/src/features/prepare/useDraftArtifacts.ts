import { useCallback, useRef, useState } from "react"
import {
  RequestError,
  draftOpportunityArtifacts,
  isUnauthenticated,
} from "@/api/client"
import { useSession } from "@/api/session"
import { notifyAccepted } from "@/components/shared"
import {
  buildMaterialPrepareRequest,
  newPrepareRequestKey,
} from "@/features/prepare/artifactsApi"

export function mutationMessage(cause: unknown): string {
  if (cause instanceof RequestError && cause.status === 409)
    return "These artifacts changed elsewhere. Reload this section, then try again."
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

export interface UseDraftArtifactsOptions {
  jobId: string
  checkId: string
  questionSetSha256: string
  workflowRevision: number
  /** Non-model reason drafting is unavailable (stale check, held pins). */
  disabledReason?: string | null
  onDone?: () => void
}

export interface UseDraftArtifactsResult {
  draft: () => void
  drafting: boolean
  error: string | null
  canDraft: boolean
  unavailableReason: string | null
}

// The ONE explicit Prepare operation for a role: a single bounded Standard
// turn drafts the held types the verified route requires. Mount, reads,
// reloads and editor openings never call draft; only the returned draft()
// issues the POST, and only with pins already observed from GET reads. The
// requestKey is stable per failed attempt so an uncertain acknowledgment
// retries as an exact idempotent replay, and resets after success so the
// next distinct prepare cannot replay the old one. Accepted work notifies
// the materials/workflows scopes; scoped reads refetch from the server.
export function useDraftArtifacts({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  disabledReason,
  onDone,
}: UseDraftArtifactsOptions): UseDraftArtifactsResult {
  const { session, loseSession } = useSession()
  const [drafting, setDrafting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const pendingKeyRef = useRef<string | null>(null)

  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to prepare materials."
        : (disabledReason ?? null)
  const canDraft = unavailableReason === null && !drafting

  const draft = useCallback(() => {
    if (
      session === undefined ||
      session === null ||
      drafting ||
      (disabledReason ?? null) !== null
    )
      return
    const requestKey = pendingKeyRef.current ?? newPrepareRequestKey()
    pendingKeyRef.current = requestKey
    setDrafting(true)
    setError(null)
    void draftOpportunityArtifacts(
      jobId,
      buildMaterialPrepareRequest(
        requestKey,
        checkId,
        questionSetSha256,
        workflowRevision
      ),
      session.csrfToken
    ).then(
      () => {
        pendingKeyRef.current = null
        setDrafting(false)
        notifyAccepted("materials", "workflows")
        onDone?.()
      },
      (cause: unknown) => {
        setDrafting(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setError(mutationMessage(cause))
      }
    )
  }, [
    session,
    drafting,
    disabledReason,
    jobId,
    checkId,
    questionSetSha256,
    workflowRevision,
    onDone,
    loseSession,
  ])

  return { draft, drafting, error, canDraft, unavailableReason }
}
