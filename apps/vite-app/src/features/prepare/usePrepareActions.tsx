import { useCallback, useRef, useState } from "react"
import {
  RequestError,
  editOpportunityMaterials,
  isUnauthenticated,
  prepareOpportunityMaterials,
  rewriteOpportunityMaterials,
  type MaterialEditRequest,
  type MaterialPrepareRequest,
  type MaterialRewriteRequest,
  type MaterialStatusView,
  type MaterialVersion,
} from "@/api/client"
import { useSession } from "@/api/session"

export const MATERIAL_EDIT_BYTE_LIMIT = 100000
export const REWRITE_INSTRUCTION_RUNE_LIMIT = 2000

// Request-key generator for explicit prepare/edit/rewrite calls. UUIDs keep
// the key opaque and well under the 200-char schema limit; the fallback only
// serves runtimes without crypto.randomUUID.
export function newPrepareRequestKey(): string {
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
  return `prepare-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

export function countRunes(text: string): number {
  return Array.from(text).length
}

export function countBytes(text: string): number {
  return new TextEncoder().encode(text).length
}

// Exact POST payload: the requestKey plus the pins already observed from GET
// reads (completed check identity plus workflow revision). Exported so tests
// build byte-identical payloads without duplicating the shape.
export function buildMaterialPrepareRequest(
  requestKey: string,
  expectedCheckId: string,
  expectedQuestionSetSha256: string,
  expectedWorkflowRevision: number
): MaterialPrepareRequest {
  return {
    requestKey,
    expectedCheckId,
    expectedQuestionSetSha256,
    expectedWorkflowRevision,
  }
}

// Exact PUT payload: the guarded version plus the box text verbatim. No
// trimming, no normalization: the server stores text exactly.
export function buildMaterialEditRequest(
  requestKey: string,
  expectedVersion: number,
  text: string
): MaterialEditRequest {
  return { requestKey, expectedVersion, text }
}

// Exact rewrite POST payload. A blank instruction is omitted so the server
// sees the explicit no-instruction case; anything else travels verbatim.
export function buildMaterialRewriteRequest(
  requestKey: string,
  expectedVersion: number,
  instruction: string
): MaterialRewriteRequest {
  return instruction === ""
    ? { requestKey, expectedVersion }
    : { requestKey, expectedVersion, instruction }
}

export const MATERIAL_CONFLICT_MESSAGE =
  "These materials changed elsewhere. Reload the page to see the current version, then try again."

function mutationErrorMessage(cause: unknown): string {
  if (cause instanceof RequestError && cause.status === 409)
    return MATERIAL_CONFLICT_MESSAGE
  return cause instanceof Error
    ? cause.message
    : "The request could not be completed."
}

function sessionToken(
  session: ReturnType<typeof useSession>["session"]
): string | null {
  return session === undefined || session === null ? null : session.csrfToken
}

export interface UsePrepareStartOptions {
  jobId: string
  checkId: string | null
  questionSetSha256: string | null
  workflowRevision: number | null
  onDone?: (view: MaterialStatusView) => void
}

export interface UsePrepareStartResult {
  start: () => void
  starting: boolean
  error: string | null
  canStart: boolean
  unavailableReason: string | null
}

// Explicit prepare action. Mount, reads and reloads never call start; only
// the returned start() issues the POST, and only with pins already observed
// from GET reads. The requestKey is stable per (role, pins) intent so a
// failed attempt retries as an exact idempotent replay, and resets after
// success so the next distinct prepare cannot replay the old one.
export function usePrepareStart({
  jobId,
  checkId,
  questionSetSha256,
  workflowRevision,
  onDone,
}: UsePrepareStartOptions): UsePrepareStartResult {
  const { session, loseSession } = useSession()
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const pendingKeyRef = useRef<{ key: string; intent: string } | null>(null)

  const csrfToken = sessionToken(session)
  const pinsReady =
    checkId !== null &&
    questionSetSha256 !== null &&
    workflowRevision !== null
  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to prepare materials."
        : !pinsReady
          ? "Saved check details are still loading."
          : null
  const canStart = unavailableReason === null && !starting

  const start = useCallback(() => {
    if (
      csrfToken === null ||
      checkId === null ||
      questionSetSha256 === null ||
      workflowRevision === null ||
      starting
    )
      return
    const intent = `${jobId}:${checkId}:${questionSetSha256}:${workflowRevision}`
    const pending = pendingKeyRef.current
    const requestKey =
      pending !== null && pending.intent === intent
        ? pending.key
        : newPrepareRequestKey()
    pendingKeyRef.current = { key: requestKey, intent }
    setStarting(true)
    setError(null)
    void prepareOpportunityMaterials(
      jobId,
      buildMaterialPrepareRequest(
        requestKey,
        checkId,
        questionSetSha256,
        workflowRevision
      ),
      csrfToken
    ).then(
      (view) => {
        pendingKeyRef.current = null
        setStarting(false)
        onDone?.(view)
      },
      (cause: unknown) => {
        setStarting(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setError(mutationErrorMessage(cause))
      }
    )
  }, [
    csrfToken,
    jobId,
    checkId,
    questionSetSha256,
    workflowRevision,
    starting,
    onDone,
    loseSession,
  ])

  return { start, starting, error, canStart, unavailableReason }
}

export interface UseMaterialEditOptions {
  jobId: string
  expectedVersion: number
  initialText: string
  onSaved?: (version: MaterialVersion) => void
}

export interface UseMaterialEditResult {
  text: string
  setText: (text: string) => void
  dirty: boolean
  saving: boolean
  error: string | null
  savedVersion: number | null
  save: () => void
  canSave: boolean
  unavailableReason: string | null
}

// Explicit direct edit for one material version. No model is involved: save
// PUTs the box bytes verbatim with the expectedVersion observed from the
// current-version read. Success adopts the response version so the box shows
// the new row; a 409 keeps the box text and reports the conflict honestly.
export function useMaterialEdit({
  jobId,
  expectedVersion,
  initialText,
  onSaved,
}: UseMaterialEditOptions): UseMaterialEditResult {
  const { session, loseSession } = useSession()
  const [text, setText] = useState(initialText)
  const [savedText, setSavedText] = useState(initialText)
  const [savedVersion, setSavedVersion] = useState<number | null>(null)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const pendingKeyRef = useRef<string | null>(null)

  const csrfToken = sessionToken(session)
  const dirty = text !== savedText
  const byteLength = countBytes(text)
  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to save edits."
        : byteLength === 0
          ? "The material text cannot be empty."
          : byteLength > MATERIAL_EDIT_BYTE_LIMIT
            ? `The material text is over the ${MATERIAL_EDIT_BYTE_LIMIT.toLocaleString("en-US")}-byte limit.`
            : !dirty
              ? "No changes to save."
              : null
  const canSave = unavailableReason === null && !saving

  const save = useCallback(() => {
    if (
      csrfToken === null ||
      saving ||
      !dirty ||
      byteLength === 0 ||
      byteLength > MATERIAL_EDIT_BYTE_LIMIT
    )
      return
    const requestKey = pendingKeyRef.current ?? newPrepareRequestKey()
    pendingKeyRef.current = requestKey
    setSaving(true)
    setError(null)
    void editOpportunityMaterials(
      jobId,
      buildMaterialEditRequest(requestKey, expectedVersion, text),
      csrfToken
    ).then(
      (version) => {
        pendingKeyRef.current = null
        setSaving(false)
        setSavedText(text)
        setSavedVersion(version.version)
        onSaved?.(version)
      },
      (cause: unknown) => {
        setSaving(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setError(mutationErrorMessage(cause))
      }
    )
  }, [
    csrfToken,
    saving,
    dirty,
    byteLength,
    jobId,
    expectedVersion,
    text,
    onSaved,
    loseSession,
  ])

  return {
    text,
    setText,
    dirty,
    saving,
    error,
    savedVersion,
    save,
    canSave,
    unavailableReason,
  }
}

export interface UseMaterialRewriteOptions {
  jobId: string
  expectedVersion: number
  onRewritten?: (version: MaterialVersion) => void
}

export interface UseMaterialRewriteResult {
  instruction: string
  setInstruction: (instruction: string) => void
  running: boolean
  error: string | null
  rewrittenVersion: number | null
  rewrite: () => void
  canRewrite: boolean
  unavailableReason: string | null
}

// Explicit rewrite action. Only the returned rewrite() issues the POST, and
// each call mints a fresh request key: a rewrite is never an idempotent
// replay of an earlier one, and a retry after a failure is a deliberate new
// turn. The instruction is optional and capped at 2000 characters.
export function useMaterialRewrite({
  jobId,
  expectedVersion,
  onRewritten,
}: UseMaterialRewriteOptions): UseMaterialRewriteResult {
  const { session, loseSession } = useSession()
  const [instruction, setInstruction] = useState("")
  const [rewrittenVersion, setRewrittenVersion] = useState<number | null>(null)
  const [running, setRunning] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const csrfToken = sessionToken(session)
  const tooLong = countRunes(instruction) > REWRITE_INSTRUCTION_RUNE_LIMIT
  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to request a rewrite."
        : tooLong
          ? `The instruction is over the ${REWRITE_INSTRUCTION_RUNE_LIMIT.toLocaleString("en-US")}-character limit.`
          : null
  const canRewrite = unavailableReason === null && !running

  const rewrite = useCallback(() => {
    if (csrfToken === null || running || tooLong) return
    setRunning(true)
    setError(null)
    void rewriteOpportunityMaterials(
      jobId,
      buildMaterialRewriteRequest(
        newPrepareRequestKey(),
        expectedVersion,
        instruction
      ),
      csrfToken
    ).then(
      (version) => {
        setRunning(false)
        setRewrittenVersion(version.version)
        onRewritten?.(version)
      },
      (cause: unknown) => {
        setRunning(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setError(mutationErrorMessage(cause))
      }
    )
  }, [
    csrfToken,
    running,
    tooLong,
    jobId,
    expectedVersion,
    instruction,
    onRewritten,
    loseSession,
  ])

  return {
    instruction,
    setInstruction,
    running,
    error,
    rewrittenVersion,
    rewrite,
    canRewrite,
    unavailableReason,
  }
}
