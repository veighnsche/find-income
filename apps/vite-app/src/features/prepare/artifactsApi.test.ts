import { describe, expect, it } from "vitest"
import {
  buildClarificationAnswerRequest,
  buildHandoffSaveRequest,
  buildMaterialPrepareRequest,
  buildMaterialRewriteRequest,
  countBytes,
  countRunes,
  newPrepareRequestKey,
} from "@/features/prepare/artifactsApi"

describe("prepare request helpers", () => {
  it("builds the exact prepare payload from observed pins", () => {
    expect(
      buildMaterialPrepareRequest("key-1", "check-1", "set-sha", 4)
    ).toEqual({
      requestKey: "key-1",
      expectedCheckId: "check-1",
      expectedQuestionSetSha256: "set-sha",
      expectedWorkflowRevision: 4,
    })
  })

  it("omits a blank rewrite instruction and keeps a real one verbatim", () => {
    expect(buildMaterialRewriteRequest("key-1", 2, "")).toEqual({
      requestKey: "key-1",
      expectedVersion: 2,
    })
    expect(buildMaterialRewriteRequest("key-1", 2, "  Shorter.  ")).toEqual({
      requestKey: "key-1",
      expectedVersion: 2,
      instruction: "  Shorter.  ",
    })
  })

  it("builds clarification and handoff payloads exactly", () => {
    expect(buildClarificationAnswerRequest("key-2", "Green.")).toEqual({
      requestKey: "key-2",
      text: "Green.",
    })
    expect(buildHandoffSaveRequest(7)).toEqual({
      expectedWorkflowRevision: 7,
    })
  })

  it("counts runes and bytes for multibyte text", () => {
    expect(countRunes("✅")).toBe(1)
    expect(countBytes("✅")).toBe(3)
  })

  it("mints opaque request keys", () => {
    const key = newPrepareRequestKey()
    expect(typeof key).toBe("string")
    expect(key.length).toBeGreaterThan(0)
    expect(key.length).toBeLessThanOrEqual(200)
  })
})
