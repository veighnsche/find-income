import { describe, expect, it } from "vitest"
import { pickRestorableRunId, type RunHistoryItem } from "./run-history"

function item(runId: string, state: string): RunHistoryItem {
  return {
    runId,
    requestKey: `key-${runId}`,
    intent: "find-jobs",
    outcome: "research_run",
    state,
    stopReason: "",
    createdAt: "2026-09-24T10:00:00Z",
    updatedAt: "2026-09-24T10:00:00Z",
  }
}

describe("pickRestorableRunId (G2/D3)", () => {
  it("restores nothing without history", () => {
    expect(pickRestorableRunId([])).toBeNull()
  })

  it("prefers the newest resumable run over newer terminal ones", () => {
    expect(
      pickRestorableRunId([
        item("run-new-done", "completed"),
        item("run-old-paused", "paused"),
      ])
    ).toBe("run-old-paused")
    expect(
      pickRestorableRunId([
        item("run-new-failed", "failed"),
        item("run-old-running", "running"),
      ])
    ).toBe("run-old-running")
  })

  it("falls back to the newest terminal run", () => {
    expect(
      pickRestorableRunId([item("run-b", "failed"), item("run-a", "completed")])
    ).toBe("run-b")
  })

  it("never auto-restores an unknown state", () => {
    expect(pickRestorableRunId([item("run-x", "mystery")])).toBeNull()
    expect(
      pickRestorableRunId([item("run-x", "mystery"), item("run-done", "completed")])
    ).toBe("run-done")
  })
})
