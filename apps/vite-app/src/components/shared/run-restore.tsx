import {
  getResearchRun,
  RequestError,
  type ResearchRunView,
} from "@/api/client"
import { useRead, type ReadResult } from "@/pages/useRead"

/** Stable code for an unknown run id (C1 error shape). */
export const RUN_NOT_FOUND = "run_not_found"

export class RunNotFoundError extends Error {
  readonly runId: string
  readonly code = RUN_NOT_FOUND
  constructor(runId: string) {
    super(`Research run ${runId} was not found on the server (${RUN_NOT_FOUND}).`)
    this.name = "RunNotFoundError"
    this.runId = runId
  }
}

export function isRunNotFound(cause: unknown): boolean {
  return (
    cause instanceof RunNotFoundError ||
    (cause instanceof Error && cause.message.includes(RUN_NOT_FOUND))
  )
}

/**
 * Server-backed run restore (F1 shared seam, C1). GET-only: reading never
 * commissions work. Unknown ids throw `RunNotFoundError`; localStorage may
 * hold a convenience pointer but is never the recovery source.
 */
export function restoreRunFromServer(
  runId: string,
  signal?: AbortSignal
): Promise<ResearchRunView> {
  return getResearchRun(runId, signal).catch((cause: unknown) => {
    if (cause instanceof RequestError && cause.status === 404)
      throw new RunNotFoundError(runId)
    throw cause
  })
}

export type ServerRunResult =
  | (ReadResult<ResearchRunView> & { notFound: boolean })
  | {
      status: "idle"
      data: null
      error: null
      retry: () => void
      notFound: false
    }

/**
 * Reactive server restore for `#/search?run=<id>` deep links. A null run id
 * stays idle and issues no request; discovery adopts this hook (or
 * `restoreRunFromServer`) instead of trusting a stored pointer.
 */
export function useServerRun(runId: string | null): ServerRunResult {
  const read = useRead<ResearchRunView | null>(
    `run-restore:${runId ?? "none"}`,
    (signal) =>
      runId === null
        ? Promise.resolve(null)
        : restoreRunFromServer(runId, signal),
    { scopes: ["run"] }
  )
  if (read.status === "ready" && read.data === null)
    return { status: "idle", data: null, error: null, retry: read.retry, notFound: false }
  if (read.status === "ready")
    return {
      status: "ready",
      data: read.data as ResearchRunView,
      error: null,
      retry: read.retry,
      notFound: false,
    }
  if (read.status === "error")
    return {
      status: "error",
      data: null,
      error: read.error,
      retry: read.retry,
      notFound: read.error.includes(RUN_NOT_FOUND),
    }
  return {
    status: "loading",
    data: null,
    error: null,
    retry: read.retry,
    notFound: false,
  }
}
