import { useCallback, useEffect, useRef, useState } from "react"
import { isUnauthenticated } from "@/api/client"
import { useSession } from "@/api/session"
import {
  scopeVersion,
  useScopeEpoch,
  type InvalidationScope,
} from "@/components/shared/invalidation"

export type ReadState<T> =
  | { status: "loading"; data: null; error: null }
  | { status: "ready"; data: T; error: null }
  | { status: "error"; data: null; error: string }

export type ReadResult<T> = ReadState<T> & { retry: () => void }

type Settled<T> =
  | { requestKey: string; data: T; error: null }
  | { requestKey: string; data: null; error: string }

export interface ReadOptions {
  /**
   * Invalidation scopes (F1 seam). After `notifyAccepted` bumps one of
   * these scopes, the read re-issues from the server; unrelated bumps
   * never refetch. Reads without scopes behave exactly as before.
   */
  scopes?: InvalidationScope[]
}

/**
 * GET-only server read. The visible state is derived from the latest settled
 * response for the current key, so retries re-issue the same read and a 401
 * drops the session back to the sign-in panel. Never commissions work.
 */
export function useRead<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>,
  options?: ReadOptions
): ReadResult<T> {
  const { loseSession } = useSession()
  const loadRef = useRef(load)
  useEffect(() => {
    loadRef.current = load
  })
  // Subscribe to the invalidation clock; the request key below moves only
  // when a subscribed scope's version moves, so unrelated writes refetch
  // nothing.
  const clock = useScopeEpoch()
  void clock
  const [attempt, setAttempt] = useState(0)
  const [settled, setSettled] = useState<Settled<T> | null>(null)
  const retry = useCallback(() => setAttempt((value) => value + 1), [])
  const scopeSuffix = (options?.scopes ?? [])
    .map((scope) => scopeVersion(scope))
    .join(",")
  const requestKey = `${key}#${attempt}#${scopeSuffix}`

  useEffect(() => {
    const controller = new AbortController()
    loadRef
      .current(controller.signal)
      .then((data) => {
        if (!controller.signal.aborted)
          setSettled({ requestKey, data, error: null })
      })
      .catch((cause: unknown) => {
        if (controller.signal.aborted) return
        if (isUnauthenticated(cause)) {
          loseSession()
          return
        }
        setSettled({
          requestKey,
          data: null,
          error:
            cause instanceof Error
              ? cause.message
              : "The request could not be completed.",
        })
      })
    return () => controller.abort()
  }, [requestKey, loseSession])

  if (settled !== null && settled.requestKey === requestKey) {
    if (settled.error === null)
      return { status: "ready", data: settled.data, error: null, retry }
    return { status: "error", data: null, error: settled.error, retry }
  }
  return { status: "loading", data: null, error: null, retry }
}
