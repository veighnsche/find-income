import { createContext, useContext, type ReactNode } from "react"
import { getPreferences, type Preferences } from "@/api/client"
import { notifyAccepted } from "@/components/shared/invalidation"
import { useRead } from "@/pages/useRead"

export interface SavedGoalsValue {
  state: "loading" | "ready" | "error"
  goals: Preferences | null
  /** Saved profile version, null until the first accepted read. */
  version: number | null
  error: string | null
  retry: () => void
}

const SavedGoalsContext = createContext<SavedGoalsValue | null>(null)

/**
 * One saved-goal read shared by Search + discovery (F1 shared seam, C9).
 *
 * The provider issues a single GET /preferences subscribed to the "goals"
 * scope. Feature writers call `notifyGoalsAccepted()` after the server
 * accepts a goal write; the provider re-reads and every consumer (including
 * Find-jobs readiness) updates from the accepted version immediately.
 * Passive mounts only read; saving stays with the deterministic goal forms.
 */
export function SavedGoalsProvider({ children }: { children: ReactNode }) {
  const read = useRead("shared:saved-goals", (signal) => getPreferences(signal), {
    scopes: ["goals"],
  })
  const value: SavedGoalsValue =
    read.status === "ready"
      ? {
          state: "ready",
          goals: read.data,
          version: read.data.version,
          error: null,
          retry: read.retry,
        }
      : read.status === "error"
        ? {
            state: "error",
            goals: null,
            version: null,
            error: read.error,
            retry: read.retry,
          }
        : { state: "loading", goals: null, version: null, error: null, retry: read.retry }
  return (
    <SavedGoalsContext.Provider value={value}>
      {children}
    </SavedGoalsContext.Provider>
  )
}

/** Shared saved goals; must render under `SavedGoalsProvider`. */
export function useSavedGoals(): SavedGoalsValue {
  const value = useContext(SavedGoalsContext)
  if (value === null)
    throw new Error("useSavedGoals must be used inside SavedGoalsProvider")
  return value
}

/** Accepted goal writes call this so Find-jobs readiness updates at once. */
export function notifyGoalsAccepted(): void {
  notifyAccepted("goals")
}
