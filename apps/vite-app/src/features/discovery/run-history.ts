import type { components } from "@jobseek/contracts"
import { RequestError } from "@/api/client"

/**
 * PROVISIONAL (G2/D3): handwritten GET for `GET /research/runs` (list).
 * `api/client.ts` is F-owned and has no run-history call yet; this module
 * mirrors its JSON-envelope error semantics (including the 401
 * `RequestError` that `useRead` turns into a session drop) so discovery
 * can restore runs from the server instead of a browser pointer. Proposed
 * F consolidation: move `RunHistoryItem`, `RunHistoryPage` and
 * `listResearchRuns(options, signal)` into `api/client.ts` and delete this
 * module. Types come from the generated contract.
 */
export type RunHistoryItem = components["schemas"]["RunHistoryItem"]
export type RunHistoryPage = components["schemas"]["RunHistoryPage"]

/** Research commissions carry outcome `research_run` (rounds supervisor). */
export const researchRunOutcome = "research_run"

/** States worth resuming: active work plus stopped-but-resumable paused. */
const resumableRunStates: ReadonlySet<string> = new Set([
  "queued",
  "running",
  "awaiting_input",
  "stopping",
  "paused",
])

/**
 * Newest-first run history for server-backed restoration. Always scoped to
 * the research outcome so profile-correction rounds never surface here.
 * Pure GET, zero model calls, commissions nothing.
 */
export async function listResearchRuns(
  options: { limit?: number; cursor?: string } = {},
  signal?: AbortSignal
): Promise<RunHistoryPage> {
  const query = new URLSearchParams({ outcome: researchRunOutcome })
  if (options.limit !== undefined) query.set("limit", String(options.limit))
  if (options.cursor !== undefined && options.cursor !== "")
    query.set("cursor", options.cursor)
  const response = await fetch(`/api/v1/research/runs?${query.toString()}`, {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  })
  if (!response.ok) {
    let message = `The API returned HTTP ${response.status}.`
    let details: Record<string, unknown> | undefined
    try {
      const body = (await response.json()) as {
        error?: { message?: string; details?: Record<string, unknown> }
      }
      if (body.error?.message) message = body.error.message
      details = body.error?.details
    } catch {
      // An unavailable server may not return a JSON envelope.
    }
    throw new RequestError(response.status, message, details)
  }
  return (await response.json()) as RunHistoryPage
}

/**
 * Pick the run a fresh context restores: the newest resumable
 * (active/paused) run when one exists, else the newest terminal run, else
 * null (idle commission). The page is newest-first, so the first match
 * wins. Unknown states never match: they stay visible in history but are
 * never auto-restored.
 */
const terminalRunStates: ReadonlySet<string> = new Set([
  "completed",
  "failed",
])

export function pickRestorableRunId(items: RunHistoryItem[]): string | null {
  const resumable = items.find((item) => resumableRunStates.has(item.state))
  if (resumable !== undefined) return resumable.runId
  const terminal = items.find((item) => terminalRunStates.has(item.state))
  return terminal?.runId ?? null
}
