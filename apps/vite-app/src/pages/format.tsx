import type { Opportunity } from "@/api/client"

export type AdvertisedCompensation = Opportunity["compensation"]

/** Deterministic cents rendering; avoids locale-dependent formatting. */
export function formatCents(cents: number, currency: string): string {
  return `${currency} ${(cents / 100).toFixed(2)}`
}

/** Date part of an RFC3339 instant; the full value stays in `title`. */
export function formatDate(iso: string): string {
  return iso.length >= 10 ? iso.slice(0, 10) : iso
}

export function describeCompensation(
  compensation: AdvertisedCompensation
): string | null {
  const currency =
    compensation.currency === undefined || compensation.currency === ""
      ? "unknown currency"
      : compensation.currency
  const min =
    compensation.minAmountCents === undefined
      ? null
      : formatCents(compensation.minAmountCents, currency)
  const max =
    compensation.maxAmountCents === undefined
      ? null
      : formatCents(compensation.maxAmountCents, currency)
  let amount: string | null = min ?? max
  if (min !== null && max !== null && min !== max)
    amount = `${min} – ${max}`
  if (amount === null) return null
  const parts = [amount]
  if (compensation.period !== undefined && compensation.period !== "unknown")
    parts.push(`per ${compensation.period}`)
  if (compensation.basis !== undefined && compensation.basis !== "unknown")
    parts.push(compensation.basis)
  return parts.join(", ")
}
