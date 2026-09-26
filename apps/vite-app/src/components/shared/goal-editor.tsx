import { useEffect } from "react"

/**
 * Goal-editor open/focus callback (F1 shared seam, C9).
 *
 * The deterministic goals editor (G-owned) registers its opener; "Change
 * goals"/"Edit goals" controls (G-owned discovery) request it. F owns only
 * this registry so neither feature imports the other. Registration is
 * single-owner: the mounted editor wins, unmounting clears it.
 */
export type GoalEditorOpener = (reason: string) => void

let opener: GoalEditorOpener | null = null

/** Register the mounted editor's open/focus handler; returns an unregister. */
export function registerGoalEditorOpener(
  next: GoalEditorOpener | null
): () => void {
  opener = next
  return () => {
    if (opener === next) opener = null
  }
}

/** React helper for the editor: register on mount, clear on unmount. */
export function useRegisterGoalEditorOpener(open: GoalEditorOpener): void {
  useEffect(() => registerGoalEditorOpener(open), [open])
}

/**
 * Ask the editor to open and take focus. Returns false when no editor is
 * mounted, so callers can fall back to navigating to `#/search`.
 */
export function requestGoalEditorOpen(reason = "edit-goals"): boolean {
  if (opener === null) return false
  opener(reason)
  return true
}

export function hasGoalEditorOpener(): boolean {
  return opener !== null
}
