import { describe, expect, it } from "vitest"
import {
  describeReadiness,
  fixtureMuseState,
  nextActionFor,
  readinessFor,
  readinessStateFor,
} from "@/features/discovery/muse-state"

describe("readinessStateFor", () => {
  it("maps only muse_ready to ready", () => {
    expect(readinessStateFor("muse_ready")).toBe("ready")
  })

  it("maps a missing configuration to needs-setup", () => {
    expect(readinessStateFor("muse_not_configured")).toBe("needs-setup")
  })

  it("maps every other frozen code to unavailable", () => {
    for (const code of [
      "muse_version_mismatch",
      "muse_lane_unverified",
      "muse_protocol_unverified",
      "muse_workspace_unverified",
    ]) {
      expect(readinessStateFor(code)).toBe("unavailable")
    }
  })

  it("fails closed on unknown codes", () => {
    expect(readinessStateFor("muse_future_code")).toBe("unavailable")
    expect(readinessStateFor("")).toBe("unavailable")
  })
})

describe("readinessFor", () => {
  it("preserves tier, code and detail", () => {
    expect(
      readinessFor("contributor", "muse_lane_unverified", "lane not proved")
    ).toEqual({
      state: "unavailable",
      code: "muse_lane_unverified",
      detail: "lane not proved",
      tier: "contributor",
    })
  })
})

describe("fixtureMuseState", () => {
  it("defaults to a ready journey that commissioned nothing", () => {
    const state = fixtureMuseState()
    expect(state.contributor.state).toBe("ready")
    expect(state.standard.state).toBe("ready")
    expect(state.commissionedCalls).toBe(0)
    expect(state.checkpoints).toEqual([])
    expect(state.report).toBeNull()
  })

  it("reports needs-setup for both tiers", () => {
    const state = fixtureMuseState("needs-setup")
    expect(state.contributor).toMatchObject({
      state: "needs-setup",
      code: "muse_not_configured",
    })
    expect(state.standard.state).toBe("needs-setup")
    expect(state.commissionedCalls).toBe(0)
  })

  it("reports blocked tiers with frozen codes", () => {
    const state = fixtureMuseState("blocked")
    expect(state.contributor).toMatchObject({
      state: "unavailable",
      code: "muse_lane_unverified",
    })
    expect(state.standard).toMatchObject({
      state: "unavailable",
      code: "muse_protocol_unverified",
    })
  })

  it("keeps partial reports honest: real saves plus gaps", () => {
    const state = fixtureMuseState("partial")
    expect(state.report?.outcome).toBe("stopped")
    expect(state.report?.savedRefs).toHaveLength(2)
    expect(state.checkpoints).toHaveLength(2)
    expect(state.report?.gaps.length).toBeGreaterThan(0)
    expect(state.report?.nextAction).not.toBe("")
  })

  it("keeps no-result reports honest: empty saves with coverage", () => {
    const state = fixtureMuseState("no-result")
    expect(state.report?.outcome).toBe("completed")
    expect(state.report?.savedRefs).toEqual([])
    expect(state.report?.searched.length).toBeGreaterThan(0)
    expect(state.report?.gaps.length).toBeGreaterThan(0)
  })

  it("builds a deterministic long list", () => {
    const first = fixtureMuseState("long-list")
    const second = fixtureMuseState("long-list")
    expect(first.checkpoints).toHaveLength(30)
    expect(first).toEqual(second)
  })
})

describe("describeReadiness", () => {
  it("renders each state with its frozen code", () => {
    expect(
      describeReadiness(fixtureMuseState("ready").contributor)
    ).toBe("Contributor: ready (muse_ready).")
    expect(
      describeReadiness(fixtureMuseState("needs-setup").standard)
    ).toContain("needs setup (muse_not_configured)")
    expect(describeReadiness(fixtureMuseState("blocked").contributor)).toContain(
      "unavailable (muse_lane_unverified)"
    )
  })
})

describe("nextActionFor", () => {
  it("names the block first", () => {
    expect(nextActionFor(fixtureMuseState("blocked"))).toContain("blocked")
  })

  it("reuses the saved report action", () => {
    const state = fixtureMuseState("no-result")
    expect(nextActionFor(state)).toBe(state.report?.nextAction)
  })

  it("points at checkpoints, then Find jobs", () => {
    expect(nextActionFor(fixtureMuseState("partial"))).toBe(
      fixtureMuseState("partial").report?.nextAction
    )
    expect(nextActionFor(fixtureMuseState("long-list"))).toContain(
      "Check chosen jobs"
    )
    expect(nextActionFor(fixtureMuseState("ready"))).toContain("Find jobs")
  })
})
