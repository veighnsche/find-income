/**
 * Exact salary-unit conversion at the API boundary (G1).
 *
 * The API stores `minMonthlyBaseCents` as integer cents; the owner reads and
 * writes ordinary currency units (e.g. EUR 4500.50). Conversion happens only
 * here, with integer arithmetic: no binary-float math touches the value, so
 * a round trip is exact.
 */

/** Render integer cents as ordinary units with exactly two decimals. */
export function formatCentsAsUnits(cents: number): string {
  if (!Number.isInteger(cents)) throw new Error("Cents must be an integer.")
  const negative = cents < 0
  const absolute = Math.abs(cents)
  const units = Math.trunc(absolute / 100)
  const remainder = absolute % 100
  return `${negative ? "-" : ""}${units}.${remainder.toString().padStart(2, "0")}`
}

export type ParseUnitsResult =
  | { ok: true; cents: number }
  | { ok: false; error: string }

/**
 * Parse an owner-typed amount ("4500", "4500.5", "4500.50") into exact
 * integer cents. Rejects negatives, empty input, more than two decimals and
 * unsafe integers.
 */
export function parseUnitsToCents(text: string): ParseUnitsResult {
  const trimmed = text.trim()
  if (trimmed === "")
    return { ok: false, error: "Minimum base must be an amount like 4500 or 4500.50." }
  const match = /^(\d{1,12})(?:\.(\d{1,2}))?$/.exec(trimmed)
  if (match?.[1] === undefined)
    return { ok: false, error: "Minimum base must be an amount like 4500 or 4500.50." }
  const units = Number(match[1])
  const fraction = match[2] === undefined ? 0 : Number(match[2].padEnd(2, "0"))
  const cents = units * 100 + fraction
  if (!Number.isSafeInteger(cents))
    return { ok: false, error: "Minimum base is too large." }
  return { ok: true, cents }
}
