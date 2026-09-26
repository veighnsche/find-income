import { useEffect, useRef, useState } from "react"
import {
  RequestError,
  updatePreferences,
  type Preferences,
} from "@/api/client"
import { formatCentsAsUnits, parseUnitsToCents } from "./salary"
import type { RoleCriterion } from "@/features/owner-context/useOwnerContext"
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  NativeSelect,
  NativeSelectOption,
} from "@/components/ui/native-select"
import { Textarea } from "@/components/ui/textarea"
import { ErrorBlock } from "@/components/shared"

export const KIND_LABELS: Record<string, string> = {
  role: "Role",
  responsibility: "Responsibility",
  technology: "Technology",
}

export const MODE_LABELS: Record<string, string> = {
  require: "Must have",
  prefer: "Nice to have",
  avoid: "Don't want",
}

const KINDS = ["role", "responsibility", "technology"]
const MODES = ["require", "prefer", "avoid"]

export type CriterionDraft = {
  key: string
  id: string
  label: string
  description: string
  kind: string
  mode: string
}

export function slugCriterionId(label: string, taken: Set<string>): string {
  const base =
    label
      .toLowerCase()
      .replace(/[^a-z0-9]+/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 60) || "criterion"
  if (!taken.has(base)) return base
  for (let counter = 2; counter < 1000; counter += 1) {
    const candidate = `${base}-${counter}`.slice(0, 80)
    if (!taken.has(candidate)) return candidate
  }
  return `${base}-${Date.now().toString(36)}`.slice(0, 80)
}

function draftFromSaved(
  criteria: RoleCriterion[],
  counterStart: number
): { rows: CriterionDraft[]; counter: number } {
  let counter = counterStart
  const rows = criteria.map((criterion) => {
    counter += 1
    return {
      key: `saved-${counter}`,
      id: criterion.id,
      label: criterion.label,
      description: criterion.description,
      kind: criterion.kind,
      mode: criterion.mode,
    }
  })
  return { rows, counter }
}

function validHours(value: string): boolean {
  if (!/^\d{1,3}(\.\d{1,2})?$/.test(value.trim())) return false
  const hours = Number(value.trim())
  return hours >= 1 && hours <= 168
}

/**
 * Deterministic wants/don't-wants form (B2). Every saved choice is an
 * ordinary labeled control; saving PUTs the complete profile against the
 * loaded version, so a 409 names a real concurrent change instead of a
 * chat-approval ceremony. No research starts from this form.
 */
export function GoalsForm({
  preferences,
  csrfToken,
  onSaved,
  onDirtyChange,
}: {
  preferences: Preferences
  csrfToken: string | null
  onSaved: () => void
  /** Reports unsaved edits so the panel can surface them (G1). */
  onDirtyChange?: (dirty: boolean) => void
}) {
  const [location, setLocation] = useState(preferences.preferredLocation)
  const [timezone, setTimezone] = useState(preferences.timezone)
  const [remote, setRemote] = useState(preferences.allowRemote)
  const [hybrid, setHybrid] = useState(preferences.allowHybrid)
  const [hours, setHours] = useState(preferences.targetHours)
  // Salary is edited in ordinary currency units; the exact conversion to
  // integer cents happens only at the save boundary (G1).
  const [baseUnits, setBaseUnits] = useState(() =>
    formatCentsAsUnits(preferences.minMonthlyBaseCents)
  )
  const [currency, setCurrency] = useState(preferences.salaryCurrency)
  const [rows, setRows] = useState<CriterionDraft[]>(
    () => draftFromSaved(preferences.roleCriteria, 0).rows
  )
  const [keyCounter, setKeyCounter] = useState(preferences.roleCriteria.length)
  // Dirty tracking compares the live draft against the last saved shape.
  // After an accepted save the baseline moves to the saved draft, so the
  // form reads clean without a remount; a parent remount resets both.
  const baselineRef = useRef<string | null>(null)
  const signature = JSON.stringify([
    location,
    timezone,
    remote,
    hybrid,
    hours,
    baseUnits,
    currency,
    rows.map((row) => [row.id, row.label, row.description, row.kind, row.mode]),
  ])
  if (baselineRef.current === null) baselineRef.current = signature
  const dirty = signature !== baselineRef.current
  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])
  const [saving, setSaving] = useState(false)
  const [status, setStatus] = useState<
    | { kind: "idle" }
    | { kind: "saved"; version: number }
    | { kind: "conflict"; message: string }
    | { kind: "error"; message: string }
    | { kind: "invalid"; message: string }
  >({ kind: "idle" })

  function updateRow(key: string, patch: Partial<CriterionDraft>) {
    setRows((current) =>
      current.map((row) => (row.key === key ? { ...row, ...patch } : row))
    )
  }

  function removeRow(key: string) {
    setRows((current) => current.filter((row) => row.key !== key))
  }

  function addRow() {
    if (rows.length >= 32) return
    const taken = new Set(rows.map((row) => row.id))
    const id = slugCriterionId(`criterion-${rows.length + 1}`, taken)
    const key = `new-${keyCounter + 1}`
    setKeyCounter((value) => value + 1)
    setRows((current) => [
      ...current,
      { key, id, label: "", description: "", kind: "role", mode: "require" },
    ])
  }

  async function save() {
    if (saving) return
    if (csrfToken === null) {
      setStatus({ kind: "error", message: "Sign in to save goals." })
      return
    }
    if (timezone.trim() === "") {
      setStatus({ kind: "invalid", message: "Timezone is required." })
      return
    }
    if (!validHours(hours)) {
      setStatus({
        kind: "invalid",
        message: "Target hours must be a number from 1 to 168 with at most two decimals.",
      })
      return
    }
    const parsedBase = parseUnitsToCents(baseUnits)
    if (!parsedBase.ok) {
      setStatus({ kind: "invalid", message: parsedBase.error })
      return
    }
    if (!/^[A-Za-z]{3}$/.test(currency.trim())) {
      setStatus({
        kind: "invalid",
        message: "Currency must be a three-letter code such as EUR.",
      })
      return
    }
    const criteria: RoleCriterion[] = []
    const seen = new Set<string>()
    for (const row of rows) {
      const label = row.label.trim()
      if (label === "" || label.length > 100) {
        setStatus({
          kind: "invalid",
          message: "Every want needs a label of 1 to 100 characters.",
        })
        return
      }
      if (row.description.length > 1000) {
        setStatus({
          kind: "invalid",
          message: "Descriptions hold at most 1000 characters.",
        })
        return
      }
      if (!KINDS.includes(row.kind) || !MODES.includes(row.mode)) {
        setStatus({ kind: "invalid", message: "Every want needs a kind and a want level." })
        return
      }
      let id = row.id
      if (id === "") {
        id = slugCriterionId(label, seen)
      }
      if (seen.has(id)) {
        setStatus({
          kind: "invalid",
          message: "Two wants share an identity; change a label.",
        })
        return
      }
      seen.add(id)
      criteria.push({
        id,
        label,
        description: row.description,
        kind: row.kind,
        mode: row.mode,
      } as RoleCriterion)
    }
    setSaving(true)
    setStatus({ kind: "idle" })
    try {
      const saved = await updatePreferences(
        {
          expectedVersion: preferences.version,
          preferredLocation: location,
          allowRemote: remote,
          allowHybrid: hybrid,
          targetHours: hours.trim(),
          minMonthlyBaseCents: parsedBase.cents,
          salaryCurrency: currency.trim().toUpperCase(),
          timezone: timezone.trim(),
          roleCriteria: criteria,
        },
        csrfToken
      )
      baselineRef.current = signature
      setStatus({ kind: "saved", version: saved.preferences.version })
      onSaved()
    } catch (cause) {
      if (cause instanceof RequestError && cause.status === 409) {
        setStatus({
          kind: "conflict",
          message:
            "The saved goals changed elsewhere. Reload them before saving again.",
        })
      } else {
        setStatus({
          kind: "error",
          message:
            cause instanceof Error
              ? cause.message
              : "Could not save goals.",
        })
      }
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="grid min-w-0 grid-cols-1 gap-3 sm:grid-cols-2">
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="goals-location">Preferred location</Label>
          <Input
            id="goals-location"
            value={location}
            onChange={(event) => setLocation(event.target.value)}
            disabled={saving}
            maxLength={200}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="goals-timezone">Timezone</Label>
          <Input
            id="goals-timezone"
            value={timezone}
            onChange={(event) => setTimezone(event.target.value)}
            disabled={saving}
            maxLength={100}
            placeholder="Europe/Amsterdam"
          />
        </div>
        <div className="flex min-w-0 flex-wrap items-center gap-x-6 gap-y-2">
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={remote}
              onCheckedChange={(value) => setRemote(value === true)}
              disabled={saving}
              aria-label="Remote work allowed"
            />
            Remote ok
          </label>
          <label className="flex cursor-pointer items-center gap-2 text-sm">
            <Checkbox
              checked={hybrid}
              onCheckedChange={(value) => setHybrid(value === true)}
              disabled={saving}
            />
            Hybrid ok
          </label>
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="goals-hours">Target hours per week</Label>
          <Input
            id="goals-hours"
            value={hours}
            onChange={(event) => setHours(event.target.value)}
            disabled={saving}
            inputMode="decimal"
            maxLength={6}
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="goals-base">
            {`Minimum monthly base (${currency.trim() === "" ? "currency units" : currency.trim().toUpperCase()})`}
          </Label>
          <Input
            id="goals-base"
            value={baseUnits}
            onChange={(event) => setBaseUnits(event.target.value)}
            disabled={saving}
            inputMode="decimal"
            maxLength={16}
            placeholder="4500.00"
          />
        </div>
        <div className="flex min-w-0 flex-col gap-1.5">
          <Label htmlFor="goals-currency">Salary currency</Label>
          <Input
            id="goals-currency"
            value={currency}
            onChange={(event) => setCurrency(event.target.value)}
            disabled={saving}
            maxLength={3}
          />
        </div>
      </div>

      <fieldset className="flex min-w-0 flex-col gap-3">
        <legend className="font-heading text-base font-medium">
          Wants and don&apos;t-wants
        </legend>
        {rows.length === 0 ? (
          <p className="text-sm text-muted-foreground">
            No explicit wants yet. Add at least one want or don&apos;t-want so
            the search has something to follow.
          </p>
        ) : null}
        {rows.map((row, index) => (
          <div
            key={row.key}
            className="flex min-w-0 flex-col gap-2 rounded-xl border px-3 py-3"
          >
            <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
              <p className="text-sm font-medium">Want {index + 1}</p>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                disabled={saving}
                onClick={() => removeRow(row.key)}
                aria-label={`Remove want ${index + 1}`}
              >
                Remove
              </Button>
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${row.key}-label`}>Label</Label>
              <Input
                id={`${row.key}-label`}
                value={row.label}
                onChange={(event) =>
                  updateRow(row.key, { label: event.target.value })
                }
                disabled={saving}
                maxLength={100}
                placeholder="e.g. Backend engineer"
              />
            </div>
            <div className="flex min-w-0 flex-col gap-1.5">
              <Label htmlFor={`${row.key}-description`}>
                Description (optional)
              </Label>
              <Textarea
                id={`${row.key}-description`}
                value={row.description}
                onChange={(event) =>
                  updateRow(row.key, { description: event.target.value })
                }
                disabled={saving}
                rows={2}
                maxLength={1000}
              />
            </div>
            <div className="grid min-w-0 grid-cols-2 gap-3">
              <div className="flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={`${row.key}-kind`}>Kind</Label>
                <NativeSelect
                  id={`${row.key}-kind`}
                  value={row.kind}
                  onChange={(event) =>
                    updateRow(row.key, { kind: event.target.value })
                  }
                  disabled={saving}
                >
                  {KINDS.map((kind) => (
                    <NativeSelectOption key={kind} value={kind}>
                      {KIND_LABELS[kind]}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
              <div className="flex min-w-0 flex-col gap-1.5">
                <Label htmlFor={`${row.key}-mode`}>Want level</Label>
                <NativeSelect
                  id={`${row.key}-mode`}
                  value={row.mode}
                  onChange={(event) =>
                    updateRow(row.key, { mode: event.target.value })
                  }
                  disabled={saving}
                >
                  {MODES.map((mode) => (
                    <NativeSelectOption key={mode} value={mode}>
                      {MODE_LABELS[mode]}
                    </NativeSelectOption>
                  ))}
                </NativeSelect>
              </div>
            </div>
          </div>
        ))}
        <div>
          <Button
            type="button"
            variant="outline"
            size="sm"
            disabled={saving || rows.length >= 32}
            onClick={addRow}
          >
            Add a want
          </Button>
        </div>
      </fieldset>

      <div aria-live="polite">
        {status.kind === "saved" ? (
          <Alert role="status">
            <AlertTitle>Saved as profile version {status.version}.</AlertTitle>
            <AlertDescription>
              The next Find jobs run searches with these choices.
            </AlertDescription>
          </Alert>
        ) : status.kind === "conflict" ? (
          <ErrorBlock
            title="The goals changed before your save"
            message={status.message}
            onRetry={onSaved}
            retryLabel="Reload saved goals"
          />
        ) : status.kind === "error" || status.kind === "invalid" ? (
          <ErrorBlock title="Could not save goals" message={status.message} />
        ) : null}
      </div>

      <div>
        <Button type="button" disabled={saving} onClick={() => void save()}>
          {saving ? "Saving…" : "Save goals"}
        </Button>
      </div>
    </div>
  )
}
