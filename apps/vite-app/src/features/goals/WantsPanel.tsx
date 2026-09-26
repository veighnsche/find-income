import { useState } from "react"
import type { EffectiveSavedContext } from "@/features/owner-context/useOwnerContext"
import { Button } from "@/components/ui/button"
import { EmptyBlock } from "@/components/shared"
import { GoalsForm, KIND_LABELS, MODE_LABELS } from "./GoalsForm"

/**
 * What-you-want-next panel (B2/F03–F04). Reviews the saved wants and
 * don't-wants beside their level, and opens the deterministic editor.
 * The form is the active content when no explicit choices exist yet.
 */
export function WantsPanel({
  context,
  csrfToken,
  correctionBusy,
  onCorrect,
  onSaved,
}: {
  context: EffectiveSavedContext
  csrfToken: string | null
  correctionBusy: boolean
  onCorrect: (anchor: string) => void
  onSaved: () => void
}) {
  const [editing, setEditing] = useState(context.requirements.length === 0)
  const wants = context.requirements.filter(
    (criterion) => criterion.mode !== "avoid"
  )
  const dontWants = context.requirements.filter(
    (criterion) => criterion.mode === "avoid"
  )
  const { preferences } = context

  return (
    <section
      aria-labelledby="wants-heading"
      className="rounded-2xl border bg-card px-4 py-4"
    >
      <div className="flex min-w-0 flex-wrap items-center justify-between gap-2">
        <h2 id="wants-heading" className="font-heading text-lg font-medium">
          What you want next
        </h2>
        <Button
          type="button"
          variant={editing ? "ghost" : "outline"}
          size="sm"
          onClick={() => setEditing((value) => !value)}
        >
          {editing ? "Close editor" : "Edit goals"}
        </Button>
      </div>
      <p className="mt-1 text-sm text-muted-foreground">
        Saved profile version {context.profileVersion}. The next Find jobs
        run searches with these choices.
      </p>

      {editing ? (
        <div className="mt-4">
          <GoalsForm
            preferences={preferences}
            csrfToken={csrfToken}
            onSaved={onSaved}
          />
        </div>
      ) : (
        <div className="mt-3 flex min-w-0 flex-col gap-4">
          <dl className="grid min-w-0 grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
            <div className="min-w-0">
              <dt className="text-muted-foreground">Location</dt>
              <dd className="wrap-break-word">{preferences.preferredLocation || "—"}</dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">Timezone</dt>
              <dd className="wrap-break-word">{preferences.timezone}</dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">Work pattern</dt>
              <dd>
                {[
                  preferences.allowRemote ? "Remote" : null,
                  preferences.allowHybrid ? "Hybrid" : null,
                ]
                  .filter((value) => value !== null)
                  .join(" · ") || "On-site"}
              </dd>
            </div>
            <div className="min-w-0">
              <dt className="text-muted-foreground">Target</dt>
              <dd>
                {preferences.targetHours} h/week · min{" "}
                {(preferences.minMonthlyBaseCents / 100).toLocaleString("en-IE", {
                  minimumFractionDigits: 2,
                  maximumFractionDigits: 2,
                })}{" "}
                {preferences.salaryCurrency}/mo
              </dd>
            </div>
          </dl>

          <div>
            <h3 className="text-sm font-medium">
              Wants ({wants.length})
            </h3>
            {wants.length === 0 ? (
              <p className="mt-1 text-sm text-muted-foreground">
                No wants saved yet.
              </p>
            ) : (
              <ul className="mt-2 flex min-w-0 flex-col gap-2">
                {wants.map((criterion) => (
                  <CriterionRow
                    key={criterion.id}
                    label={criterion.label}
                    description={criterion.description}
                    kind={criterion.kind}
                    mode={criterion.mode}
                    correctionBusy={correctionBusy}
                    onCorrect={() =>
                      onCorrect(
                        `About role criterion "${criterion.label}" (${criterion.kind} · ${criterion.mode}): `
                      )
                    }
                  />
                ))}
              </ul>
            )}
          </div>

          <div>
            <h3 className="text-sm font-medium">
              Don&apos;t-wants ({dontWants.length})
            </h3>
            {dontWants.length === 0 ? (
              <p className="mt-1 text-sm text-muted-foreground">
                Nothing ruled out yet.
              </p>
            ) : (
              <ul className="mt-2 flex min-w-0 flex-col gap-2">
                {dontWants.map((criterion) => (
                  <CriterionRow
                    key={criterion.id}
                    label={criterion.label}
                    description={criterion.description}
                    kind={criterion.kind}
                    mode={criterion.mode}
                    correctionBusy={correctionBusy}
                    onCorrect={() =>
                      onCorrect(
                        `About role criterion "${criterion.label}" (${criterion.kind} · ${criterion.mode}): `
                      )
                    }
                  />
                ))}
              </ul>
            )}
          </div>

          {context.requirements.length === 0 ? (
            <EmptyBlock
              title="No explicit choices yet"
              description="The editor above is open: save at least one want or don't-want so the search has something to follow."
            />
          ) : null}
        </div>
      )}
    </section>
  )
}

function CriterionRow({
  label,
  description,
  kind,
  mode,
  correctionBusy,
  onCorrect,
}: {
  label: string
  description: string
  kind: string
  mode: string
  correctionBusy: boolean
  onCorrect: () => void
}) {
  return (
    <li className="rounded-md border px-3 py-2">
      <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-2">
        <p className="text-sm font-medium wrap-break-word">{label}</p>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={correctionBusy}
          onClick={onCorrect}
          aria-label={`Correct criterion "${label}"`}
        >
          Correct
        </Button>
      </div>
      <p className="mt-0.5 text-xs text-muted-foreground">
        {KIND_LABELS[kind] ?? kind} · {MODE_LABELS[mode] ?? mode}
      </p>
      {description !== "" ? (
        <p className="mt-1 text-sm wrap-break-word">{description}</p>
      ) : null}
    </li>
  )
}
