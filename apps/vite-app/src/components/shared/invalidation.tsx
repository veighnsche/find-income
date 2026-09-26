import { useCallback, useSyncExternalStore } from "react"

/**
 * Scoped read invalidation (F1 shared seam, C9).
 *
 * Accepted writes bump exactly the scopes their projection depends on, and
 * `useRead` re-issues only the reads subscribed to those scopes. There is no
 * blind global polling or recommissioning: an unrelated scope bump causes a
 * cheap re-render but never a refetch.
 *
 * Scope meanings:
 * - goals: saved wants/don't-wants (GET /preferences) and derived readiness.
 * - selection: owner decisions and the chosen-role workflow list.
 * - check: per-job check status/views/activity.
 * - answers: Jev matches and saved question answers.
 * - materials: prepared artifacts, readiness and Handoff projections.
 * - run: research runs, findings and brief/catalog reads.
 * - workflows: per-role workflow stage projections.
 */
export type InvalidationScope =
  | "goals"
  | "selection"
  | "check"
  | "answers"
  | "materials"
  | "run"
  | "workflows"

const versions = new Map<InvalidationScope, number>()
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

/** Current version of one scope (non-reactive read for request keys). */
export function scopeVersion(scope: InvalidationScope): number {
  return versions.get(scope) ?? 0
}

/**
 * Record accepted writes. Call only after the server accepted the write
 * (never optimistically, never on read): subscribers refetch the bumped
 * scopes from the server, which stays the source of truth.
 */
export function notifyAccepted(...scopes: InvalidationScope[]): void {
  if (scopes.length === 0) return
  for (const scope of scopes)
    versions.set(scope, scopeVersion(scope) + 1)
  epoch += 1
  for (const listener of listeners) listener()
}

/** React subscription to the invalidation clock. See `useRead`. */
export function useScopeEpoch(): number {
  return useSyncExternalStore(subscribe, getEpoch, getEpoch)
}

/** Stable notifier for write handlers in G/C/A/E feature components. */
export function useInvalidate(): (...scopes: InvalidationScope[]) => void {
  return useCallback((...scopes: InvalidationScope[]) => {
    notifyAccepted(...scopes)
  }, [])
}
