import type { components } from "@jobseek/contracts"
import { RequestError } from "@/api/client"

/**
 * PROVISIONAL (G1/D4): handwritten GET for `GET /research/owner-context`.
 * `api/client.ts` is F-owned and has no sourced-context call yet; this
 * module mirrors its JSON-envelope error semantics (including the 401
 * `RequestError` that `useRead` turns into a session drop) so the
 * ExperiencePanel can consume the D4 read now. Proposed F consolidation:
 * move `SourcedOwnerContext`, `CareerSourceNotConnected` and
 * `getSourcedOwnerContext(signal)` into `api/client.ts` and delete this
 * module. Types come from the generated contract.
 */
export type SourcedOwnerContext = components["schemas"]["SourcedOwnerContext"]
export type OwnerCareerSource =
  SourcedOwnerContext["sources"][number]

/** The server predates D4 or has no owner-context route: neutral absence. */
export class CareerSourceNotConnected extends Error {
  constructor() {
    super("Career sources are not available on this server.")
    this.name = "CareerSourceNotConnected"
  }
}

export function isCareerSourceNotConnected(cause: unknown): boolean {
  return cause instanceof CareerSourceNotConnected
}

/**
 * Read owner identity plus approved career sources with provenance (D4).
 * Pure GET, zero model calls; bodies serve verbatim. A 404 means the
 * endpoint is absent and maps to `CareerSourceNotConnected` (neutral);
 * every other failure keeps its `RequestError` status/message.
 */
export async function getSourcedOwnerContext(
  signal?: AbortSignal
): Promise<SourcedOwnerContext> {
  const response = await fetch("/api/v1/research/owner-context", {
    credentials: "same-origin",
    signal,
    headers: { Accept: "application/json" },
  })
  if (response.status === 404) throw new CareerSourceNotConnected()
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
  return (await response.json()) as SourcedOwnerContext
}
