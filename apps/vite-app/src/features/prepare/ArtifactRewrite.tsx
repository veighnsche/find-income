import { useState } from "react"
import {
  isUnauthenticated,
  rewriteOpportunityArtifact,
} from "@/api/client"
import { useSession } from "@/api/session"
import { notifyAccepted } from "@/components/shared"
import { Button } from "@/components/ui/button"
import { Textarea } from "@/components/ui/textarea"
import {
  buildMaterialRewriteRequest,
  countRunes,
  newPrepareRequestKey,
  type StoredArtifactType,
} from "@/features/prepare/artifactsApi"
import { mutationMessage } from "@/features/prepare/useDraftArtifacts"

export const REWRITE_INSTRUCTION_RUNE_LIMIT = 2000

// RewritePanel is the E2 separate explicit rewrite for one stored artifact:
// a Standard turn against the same grounded basis, fenced on the displayed
// current version, saved as a new inspectable version. It never runs on
// read, reload or editor opening — only this button issues the POST. The
// requestKey is stable per failed attempt so an uncertain acknowledgment
// retries as an exact idempotent replay, and resets after success.
export function RewritePanel({
  jobId,
  artifactType,
  expectedVersion,
  disabledReason,
}: {
  jobId: string
  artifactType: StoredArtifactType
  expectedVersion: number
  disabledReason: string | null
}) {
  const { session, loseSession } = useSession()
  const [instruction, setInstruction] = useState("")
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pendingKey, setPendingKey] = useState<string | null>(null)
  const tooLong =
    countRunes(instruction) > REWRITE_INSTRUCTION_RUNE_LIMIT
  const boxId = `artifact-rewrite-${artifactType}`

  async function rewrite() {
    if (
      session === undefined ||
      session === null ||
      running ||
      tooLong ||
      disabledReason !== null
    )
      return
    const requestKey = pendingKey ?? newPrepareRequestKey()
    setPendingKey(requestKey)
    setRunning(true)
    setError(null)
    try {
      await rewriteOpportunityArtifact(
        jobId,
        artifactType,
        buildMaterialRewriteRequest(requestKey, expectedVersion, instruction),
        session.csrfToken
      )
      setRunning(false)
      setPendingKey(null)
      setInstruction("")
      notifyAccepted("materials", "workflows")
    } catch (cause: unknown) {
      setRunning(false)
      if (isUnauthenticated(cause)) {
        loseSession()
        return
      }
      setError(mutationMessage(cause))
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-2">
      <label htmlFor={boxId} className="text-sm font-medium wrap-break-word">
        {`Request rewrite (Standard drafts v${expectedVersion + 1} from the same facts and answers)`}
      </label>
      <Textarea
        id={boxId}
        value={instruction}
        onChange={(event) => setInstruction(event.target.value)}
        rows={2}
        placeholder="Optional instruction, e.g. shorter and more formal."
      />
      <div className="flex min-w-0 flex-wrap items-center gap-3">
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={
            running ||
            tooLong ||
            disabledReason !== null ||
            session === undefined ||
            session === null
          }
          onClick={() => void rewrite()}
        >
          {running ? "Rewriting…" : "Request rewrite"}
        </Button>
        <p className="text-sm wrap-break-word text-muted-foreground">
          {disabledReason ??
            (tooLong
              ? `Over the ${REWRITE_INSTRUCTION_RUNE_LIMIT.toLocaleString()} character limit.`
              : "Separate from exact edits; the new version stays inspectable below.")}
        </p>
      </div>
      {error === null ? null : (
        <p role="alert" className="text-sm wrap-break-word text-destructive">
          {error}
        </p>
      )}
    </div>
  )
}
