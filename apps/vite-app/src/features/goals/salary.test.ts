import { describe, expect, it } from "vitest"
import { formatCentsAsUnits, parseUnitsToCents } from "./salary"

describe("salary unit conversion (G1)", () => {
  it("formats cents as ordinary units with two decimals", () => {
    expect(formatCentsAsUnits(450000)).toBe("4500.00")
    expect(formatCentsAsUnits(450050)).toBe("4500.50")
    expect(formatCentsAsUnits(5)).toBe("0.05")
    expect(formatCentsAsUnits(0)).toBe("0.00")
  })

  it("parses whole and fractional units into exact cents", () => {
    expect(parseUnitsToCents("4500")).toEqual({ ok: true, cents: 450000 })
    expect(parseUnitsToCents("4500.50")).toEqual({ ok: true, cents: 450050 })
    expect(parseUnitsToCents("4500.5")).toEqual({ ok: true, cents: 450050 })
    expect(parseUnitsToCents("  0.05 ")).toEqual({ ok: true, cents: 5 })
    expect(parseUnitsToCents("0")).toEqual({ ok: true, cents: 0 })
  })

  it("round-trips without float drift", () => {
    for (const cents of [0, 1, 99, 100, 450050, 123456789, 999999999999]) {
      const parsed = parseUnitsToCents(formatCentsAsUnits(cents))
      expect(parsed).toEqual({ ok: true, cents })
    }
    // 0.07-style values that binary floats cannot represent exactly.
    expect(parseUnitsToCents("4500.07")).toEqual({ ok: true, cents: 450007 })
  })

  it("rejects negatives, empties and over-precise input", () => {
    for (const text of ["", "   ", "-5", "-0.01", "45.505", "abc", "4,500", "45..5"]) {
      const parsed = parseUnitsToCents(text)
      expect(parsed.ok).toBe(false)
    }
  })
})
