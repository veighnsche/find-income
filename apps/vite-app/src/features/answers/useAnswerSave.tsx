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
// draftRequested rides explicit blanks only and the key is omitted
// otherwise, so ordinary text and plain blanks never carry the flag.
export function buildAnswerValueSave(
  expectedAnswerVersion: number,
  text: string,
  draftRequested = false
): AnswerValueSave {
  if (draftRequested && text === "") {
    return { expectedAnswerVersion, text, draftRequested: true }
  }
  return { expectedAnswerVersion, text }
}

export interface AnswerSaveSnapshot {
  version: number
  state: AnswerBoxState
  provenance: QuestionAnswerValue["provenance"] | null
  draftRequested: boolean
}

export interface UseAnswerSaveOptions {
  jobId: string
  questionId: string
  initialText: string
  // Stored text the box is clean against. initialText may carry a
  // preserved draft or an unsaved suggestion on top of it.
  baseText: string
  initial: AnswerSaveSnapshot
  onSaved?: () => void
}

export interface UseAnswerSaveResult {
  text: string
  setText: (text: string) => void
  draftRequested: boolean
  setDraftRequested: (draftRequested: boolean) => void
  dirty: boolean
  saved: AnswerSaveSnapshot
  saving: boolean
  error: string | null
  save: () => void
  saveAsync: () => Promise<QuestionAnswerValue>
  saveExact: (
    exactText: string,
    exactDraftRequested: boolean
  ) => Promise<QuestionAnswerValue>
  canSave: boolean
  unavailableReason: string | null
}

// Explicit per-question save for the answers surface. Mount, reads and
// reloads never call save; only save()/saveAsync()/saveExact() issue the
// PUT, and only with the version already observed from the values read (0
// when unset). Success adopts the response version so the next save
// guards on the new row; a 409 keeps the box text and reports the
// conflict honestly. saveExact bypasses the dirty check so continuation
// can persist untouched kept suggestions and explicit blanks; it still
// guards on the accepted version and never normalizes the text.
export function useAnswerSave({
  jobId,
  questionId,
  initialText,
  baseText,
  initial,
  onSaved,
}: UseAnswerSaveOptions): UseAnswerSaveResult {
  const { session, loseSession } = useSession()
  const [text, setText] = useState(initialText)
  const [draftRequested, setDraftRequested] = useState(
    initial.draftRequested
  )
  const [saved, setSaved] = useState<AnswerSaveSnapshot>(initial)
  const [savedText, setSavedText] = useState(baseText)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const csrfToken =
    session === undefined || session === null ? null : session.csrfToken
  const textDirty = text !== savedText
  const flagDirty =
    text === "" && draftRequested !== saved.draftRequested
  const dirty = textDirty || flagDirty
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

  const saveExact = useCallback(
    (
      exactText: string,
      exactDraftRequested: boolean
    ): Promise<QuestionAnswerValue> => {
      if (csrfToken === null)
        return Promise.reject(new Error("Sign in to save answers."))
      if (saving)
        return Promise.reject(
          new Error("A save is already running for this question.")
        )
      if (exactText.length > 20000)
        return Promise.reject(
          new Error("Answer text is over the 20,000 character limit.")
        )
      setSaving(true)
      setError(null)
      return saveQuestionAnswer(
        jobId,
        questionId,
        buildAnswerValueSave(
          saved.version,
          exactText,
          exactDraftRequested && exactText === ""
        ),
        csrfToken
      ).then(
        (value) => {
          setSaving(false)
          setSavedText(value.text)
          setSaved({
            version: value.version,
            state: value.state === "blank" ? "blank" : "answered",
            provenance: value.provenance,
            draftRequested: value.draftRequested ?? false,
          })
          onSaved?.()
          return value
        },
        (cause: unknown) => {
          setSaving(false)
          if (isUnauthenticated(cause)) {
            loseSession()
            throw cause
          }
          if (cause instanceof RequestError && cause.status === 409) {
            const conflict = new Error(
              "This answer changed elsewhere. Reload the page and reconcile your edits before saving again."
            )
            setError(conflict.message)
            throw conflict
          }
          const failure =
            cause instanceof Error
              ? cause
              : new Error("The request could not be completed.")
          setError(failure.message)
          throw failure
        }
      )
    },
    [csrfToken, saving, jobId, questionId, saved.version, loseSession, onSaved]
  )

  const saveAsync = useCallback((): Promise<QuestionAnswerValue> => {
    if (!dirty) return Promise.reject(new Error("No changes to save."))
    return saveExact(text, draftRequested)
  }, [dirty, saveExact, text, draftRequested])

  const save = useCallback(() => {
    void saveAsync().catch(() => {
      // saveAsync already reports the failure on the box.
    })
  }, [saveAsync])

  return {
    text,
    setText,
    draftRequested,
    setDraftRequested,
    dirty,
    saved,
    saving,
    error,
    save,
    saveAsync,
    saveExact,
    canSave,
    unavailableReason,
  }
}
