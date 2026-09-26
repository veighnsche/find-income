import { useCallback, useSyncExternalStore } from "react"

/**
 * Answer-draft preservation (F1 shared seam, C9/A2).
 *
 * Job/check/question-scoped dirty text survives Jev rematching and internal
 * navigation: the store lives outside React state, so unmounts and suggestion
 * refreshes never discard it. Scoping is exact — another job or check can
 * never receive a draft. Only explicit owner continuation persists drafts to
 * the server (K3); this module performs no writes and no model calls.
 */
export interface AnswerDraftKey {
  jobId: string
  checkId: string
  questionId: string
}

function keyOf(key: AnswerDraftKey): string {
  return JSON.stringify([key.jobId, key.checkId, key.questionId])
}

const drafts = new Map<string, string>()
const listeners = new Set<() => void>()
let epoch = 0

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

function getEpoch(): number {
  return epoch
}

function bump(): void {
  epoch += 1
  for (const listener of listeners) listener()
}

/** Non-reactive read; null means no dirty text is preserved. */
export function getAnswerDraft(key: AnswerDraftKey): string | null {
  return drafts.get(keyOf(key)) ?? null
}

export function hasAnswerDraft(key: AnswerDraftKey): boolean {
  return drafts.has(keyOf(key))
}

/** Preserve dirty text verbatim (including an explicit empty box). */
export function setAnswerDraft(key: AnswerDraftKey, text: string): void {
  if (drafts.get(keyOf(key)) === text) return
  drafts.set(keyOf(key), text)
  bump()
}

export function clearAnswerDraft(key: AnswerDraftKey): void {
  if (!drafts.has(keyOf(key))) return
  drafts.delete(keyOf(key))
  bump()
}

/** Drop every draft for one check (e.g. after its answers commit). */
export function clearAnswerDraftsForCheck(
  jobId: string,
  checkId: string
): void {
  let changed = false
  for (const raw of drafts.keys()) {
    const [job, check] = JSON.parse(raw) as [string, string, string]
    if (job === jobId && check === checkId) {
      drafts.delete(raw)
      changed = true
    }
  }
  if (changed) bump()
}

/** Drop every draft for one job across all its checks. */
export function clearAnswerDraftsForJob(jobId: string): void {
  let changed = false
  for (const raw of drafts.keys()) {
    const [job] = JSON.parse(raw) as [string, string, string]
    if (job === jobId) {
      drafts.delete(raw)
      changed = true
    }
  }
  if (changed) bump()
}

export interface AnswerDraftHandle {
  draft: string | null
  setDraft: (text: string) => void
  clearDraft: () => void
}

/** Reactive draft box state for one exact question. */
export function useAnswerDraft(key: AnswerDraftKey): AnswerDraftHandle {
  useSyncExternalStore(subscribe, getEpoch, getEpoch)
  const setDraft = useCallback(
    (text: string) => setAnswerDraft(key, text),
    // The key fields identify the box; callers keep them stable per box.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key.jobId, key.checkId, key.questionId]
  )
  const clearDraft = useCallback(
    () => clearAnswerDraft(key),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key.jobId, key.checkId, key.questionId]
  )
  return { draft: getAnswerDraft(key), setDraft, clearDraft }
}
