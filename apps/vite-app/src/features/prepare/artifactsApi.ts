import type {
  ClarificationAnswer,
  HandoffSave,
  MaterialPrepareRequest,
  MaterialRewriteRequest,
} from "@/api/client"

// Pure request helpers for the E-lane Prepare/Handoff UI: opaque request
// keys, rune/byte counts, and exact payload builders for the pins already
// observed from GET reads. All endpoint wrappers and contract types live in
// the F-owned api/client.ts; this module issues no fetch and commissions
// nothing.

export type StoredArtifactType =
  | "cv"
  | "cover_letter"
  | "email_subject"
  | "email_body"

// Request-key generator for explicit prepare/edit/rewrite/answer calls.
// UUIDs keep the key opaque and well under the 200-char schema limit; the
// fallback only serves runtimes without crypto.randomUUID.
export function newPrepareRequestKey(): string {
  const cryptoRef =
    typeof globalThis.crypto === "object" &&
    globalThis.crypto !== null &&
    "randomUUID" in globalThis.crypto
      ? (globalThis.crypto as { randomUUID?: () => string })
      : null
  if (cryptoRef?.randomUUID !== undefined) {
    try {
      return cryptoRef.randomUUID()
    } catch {
      // Fall through to the insecure generator below.
    }
  }
  return `prepare-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 12)}`
}

export function countRunes(text: string): number {
  return Array.from(text).length
}

export function countBytes(text: string): number {
  return new TextEncoder().encode(text).length
}

// Exact POST payload: the requestKey plus the pins already observed from GET
// reads (completed check identity plus workflow revision). Exported so tests
// build byte-identical payloads without duplicating the shape.
export function buildMaterialPrepareRequest(
  requestKey: string,
  expectedCheckId: string,
  expectedQuestionSetSha256: string,
  expectedWorkflowRevision: number
): MaterialPrepareRequest {
  return {
    requestKey,
    expectedCheckId,
    expectedQuestionSetSha256,
    expectedWorkflowRevision,
  }
}

// Exact rewrite POST payload. A blank instruction is omitted so the server
// sees the explicit no-instruction case; anything else travels verbatim.
export function buildMaterialRewriteRequest(
  requestKey: string,
  expectedVersion: number,
  instruction: string
): MaterialRewriteRequest {
  return instruction === ""
    ? { requestKey, expectedVersion }
    : { requestKey, expectedVersion, instruction }
}

export function buildClarificationAnswerRequest(
  requestKey: string,
  text: string
): ClarificationAnswer {
  return { requestKey, text }
}

export function buildHandoffSaveRequest(
  expectedWorkflowRevision: number
): HandoffSave {
  return { expectedWorkflowRevision }
}
