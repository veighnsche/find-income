import { describe, expect, it } from "vitest"
import type { ResearchRunView } from "@/api/client"
import { runActionAvailability } from "./research-controls"

const states: ResearchRunView["state"][] = [
  "queued",
  "running",
  "awaiting_input",
  "stopping",
  "paused",
  "completed",
  "failed",
]

describe("runActionAvailability (G2/R26)", () => {
  it("covers every run state with an explicit verdict per action", () => {
    for (const state of states) {
      const availability = runActionAvailability(state)
      for (const action of ["stop", "resume", "steer", "findMore"] as const) {
        const verdict = availability[action]
        // Disabled actions always name their state reason.
        expect(typeof verdict.enabled).toBe("boolean")
        if (!verdict.enabled) {
          expect(typeof verdict.reason).toBe("string")
          expect(verdict.reason!.length).toBeGreaterThan(0)
        }
      }
    }
  })

  it("stops active runs, resumes paused runs, and starts Find more once settled", () => {
    for (const state of ["queued", "running", "awaiting_input"] as const) {
      const availability = runActionAvailability(state)
      expect(availability.stop.enabled).toBe(true)
      expect(availability.resume.enabled).toBe(false)
      expect(availability.steer.enabled).toBe(true)
      expect(availability.findMore.enabled).toBe(false)
    }
    const stopping = runActionAvailability("stopping")
    expect(stopping.stop.enabled).toBe(false)
    expect(stopping.resume.enabled).toBe(false)

    const paused = runActionAvailability("paused")
    expect(paused.stop.enabled).toBe(false)
    expect(paused.resume.enabled).toBe(true)
    expect(paused.steer.enabled).toBe(true)
    expect(paused.findMore.enabled).toBe(true)

    for (const state of ["completed", "failed"] as const) {
      const availability = runActionAvailability(state)
      expect(availability.stop.enabled).toBe(false)
      expect(availability.resume.enabled).toBe(false)
      expect(availability.steer.enabled).toBe(false)
      expect(availability.findMore.enabled).toBe(true)
    }
  })
})
