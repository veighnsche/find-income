import { useCallback, useEffect, useRef, useState } from "react"
import { isUnauthenticated } from "@/api/client"
import { useSession } from "@/api/session"

export type ReadState<T> =
  | { status: "loading"; data: null; error: null }
  | { status: "ready"; data: T; error: null }
  | { status: "error"; data: null; error: string }

export type ReadResult<T> = ReadState<T> & { retry: () => void }

type Settled<T> =
  | { requestKey: string; data: T; error: null }
  | { requestKey: string; data: null; error: string }

/**
 * GET-only server read. The visible state is derived from the latest settled
 * response for the current key, so retries re-issue the same read and a 401
 * drops the session back to the sign-in panel. Never commissions work.
 */
export function useRead<T>(
  key: string,
  load: (signal: AbortSignal) => Promise<T>
): ReadResult<T> {
  const { loseSession } = useSession()
  const loadRef = useRef(load)
  useEffect(() => {
    loadRef.current = load
  })
  const [attempt, setAttempt] = useState(0)
  const [settled, setSettled] = useState<Settled<T> | null>(null)
  const retry = useCallback(() => setAttempt((value) => value + 1), [])
  const requestKey = `${key}#${attempt}`

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
