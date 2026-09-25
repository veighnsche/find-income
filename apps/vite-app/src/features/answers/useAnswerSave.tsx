import { useCallback, useState } from "react"
import {
  RequestError,
  isUnauthenticated,
  saveQuestionAnswer,
  type AnswerValueSave,
  type QuestionAnswerValue,
} from "@/api/client"
import { useSession } from "@/api/session"

export type AnswerBoxState = "unset" | "answered" | "blank"

// Exact PUT payload: the guarded version plus the box text verbatim.
// Exported so tests build byte-identical payloads without duplicating the
// shape. No trimming, no normalization: the server stores text exactly.
export function buildAnswerValueSave(
  expectedAnswerVersion: number,
  text: string
): AnswerValueSave {
  return { expectedAnswerVersion, text }
}

export interface AnswerSaveSnapshot {
  version: number
  state: AnswerBoxState
  provenance: QuestionAnswerValue["provenance"] | null
}

export interface UseAnswerSaveOptions {
  jobId: string
  questionId: string
  initialText: string
  initial: AnswerSaveSnapshot
}

export interface UseAnswerSaveResult {
  text: string
  setText: (text: string) => void
  dirty: boolean
  saved: AnswerSaveSnapshot
  saving: boolean
  error: string | null
  save: () => void
  canSave: boolean
  unavailableReason: string | null
}

// Explicit per-question save for the E3 answers surface. Mount, reads and
// reloads never call save; only the returned save() issues the PUT, and only
// with the version already observed from the values read (0 when unset).
// Success adopts the response version so the next save guards on the new
// row; a 409 keeps the box text and reports the conflict honestly.
export function useAnswerSave({
  jobId,
  questionId,
  initialText,
  initial,
}: UseAnswerSaveOptions): UseAnswerSaveResult {
  const { session, loseSession } = useSession()
  const [text, setText] = useState(initialText)
  const [saved, setSaved] = useState<AnswerSaveSnapshot>(initial)
  const [savedText, setSavedText] = useState(initialText)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const csrfToken =
    session === undefined || session === null ? null : session.csrfToken
  const dirty = text !== savedText
  const tooLong = text.length > 20000
  const unavailableReason =
    session === undefined
      ? "Checking dashboard session…"
      : session === null
        ? "Sign in to save answers."
        : tooLong
          ? "Answer text is over the 20,000 character limit."
          : !dirty
            ? "No changes to save."
            : null
  const canSave = unavailableReason === null && !saving

  const save = useCallback(() => {
    if (csrfToken === null || saving || !dirty || tooLong) return
    setSaving(true)
    setError(null)
    void saveQuestionAnswer(
      jobId,
      questionId,
      buildAnswerValueSave(saved.version, text),
      csrfToken
    ).then(
      (value) => {
        setSaving(false)
        setSavedText(value.text)
        setSaved({
          version: value.version,
          state: value.state,
          provenance: value.provenance,
        })
      },
      (cause: unknown) => {
        setSaving(false)
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        if (cause instanceof RequestError && cause.status === 409) {
          setError(
            "This answer changed elsewhere. Reload the page and reconcile your edits before saving again."
          )
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
    saving,
    dirty,
    tooLong,
    jobId,
    questionId,
    saved.version,
    text,
    loseSession,
  ])

  return {
    text,
    setText,
    dirty,
    saved,
    saving,
    error,
    save,
    canSave,
    unavailableReason,
  }
}
