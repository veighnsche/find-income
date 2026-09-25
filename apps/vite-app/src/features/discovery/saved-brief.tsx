import type { Preferences } from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"
import type { EffectiveSavedContext } from "@/features/owner-context/useOwnerContext"
import { formatCents } from "@/pages/format"

// summarizeSavedBriefTerms renders the concise owner-visible terms of the
// saved profile. Pure formatting over saved values; no inference.
export function summarizeSavedBriefTerms(preferences: Preferences): string {
  const location =
    preferences.preferredLocation === ""
      ? "location not recorded"
      : preferences.preferredLocation
  const presence =
    preferences.allowRemote && preferences.allowHybrid
      ? "remote and hybrid ok"
      : preferences.allowRemote
        ? "remote ok"
        : preferences.allowHybrid
          ? "hybrid ok"
          : "onsite only"
  const pay =
    preferences.minMonthlyBaseCents > 0
      ? `at least ${formatCents(preferences.minMonthlyBaseCents, preferences.salaryCurrency)}/mo`
      : "no pay floor"
  const count = preferences.roleCriteria.length
  return [
    location,
    presence,
    `${preferences.targetHours} h/week`,
    pay,
    `${count} role ${count === 1 ? "criterion" : "criteria"}`,
  ].join(" · ")
}

// changeSearchCorrectionBoxId is the Lane B correction textarea on My search.
// Change-my-search links route to #/search and then move focus here so the
// owner lands in the correction flow, not at the top of a long page.
export const changeSearchCorrectionBoxId = "change-search-text"

function focusCorrectionBox(remaining: number): void {
  const target = document.getElementById(changeSearchCorrectionBoxId)
  if (target !== null) {
    if (typeof target.scrollIntoView === "function")
      target.scrollIntoView({ block: "nearest" })
    const focusable = target as HTMLElement
    if (typeof focusable.focus === "function")
      focusable.focus({ preventScroll: true })
    return
  }
  if (remaining > 0)
    window.setTimeout(() => focusCorrectionBox(remaining - 1), 50)
}

function ChangeSearchLink({ href }: { href: string }) {
  return (
    <p className="text-sm">
      <a
        className="underline underline-offset-4"
        href={href}
        onClick={() => {
          // Plain-anchor navigation still applies; the deferred focus lands
          // the owner in Lane B's correction box once the route renders.
          window.setTimeout(() => focusCorrectionBox(10), 0)
        }}
      >
        Change my search
      </a>
    </p>
  )
}

export interface SavedBriefPanelProps {
  context: EffectiveSavedContext | null
  contextState: "loading" | "ready" | "error"
  contextError: string | null
  retryContext: () => void
  // Profile version the visible run was commissioned against, when a run is
  // shown. Compared honestly against the current saved context.
  runBriefProfileVersion?: number | null
  changeSearchHref?: string
}

// SavedBriefPanel renders the saved search basis from Lane B's saved context:
// version identity, concise terms, catalog state and staleness. It performs no
// reads of its own; the parent owns the single useOwnerContext instance per
// surface and passes the slice down. It never commissions work.
export function SavedBriefPanel({
  context,
  contextState,
  contextError,
  retryContext,
  runBriefProfileVersion = null,
  changeSearchHref = "#/search",
}: SavedBriefPanelProps) {
  if (contextState === "loading" || (contextState === "ready" && context === null)) {
    return <LoadingBlock label="Loading saved search brief…" />
  }
  if (contextState === "error" || context === null) {
    return (
      <ErrorBlock
        title="Could not load the saved search brief"
        message={contextError ?? "The request could not be completed."}
        onRetry={retryContext}
      />
    )
  }

  if (context.rubricVersion === null) {
    return (
      <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
        <EmptyBlock
          title="No saved search brief yet"
          description={`Profile v${context.profileVersion} is saved, but the server has no search brief for it yet. The first Find jobs run authors one.`}
        />
        <ChangeSearchLink href={changeSearchHref} />
      </div>
    )
  }

  const shownRequirements = context.requirements.slice(0, 3)
  const hiddenCount = context.requirements.length - shownRequirements.length

  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-2xl border bg-card px-4 py-4">
      <h3 className="font-heading text-base font-medium">Saved search brief</h3>
      <p className="text-sm wrap-break-word">
        Profile v{context.profileVersion} · {context.rubricVersion}
      </p>
      {context.rubricSource === null ? null : (
        <p className="text-xs text-muted-foreground wrap-break-word">
          {context.rubricSource}
        </p>
      )}
      <p className="text-sm text-muted-foreground wrap-break-word">
        {summarizeSavedBriefTerms(context.preferences)}
      </p>
      {context.requirements.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No role criteria in this brief.
        </p>
      ) : (
        <ul className="flex min-w-0 flex-col gap-1 text-sm">
          {shownRequirements.map((requirement) => (
            <li key={requirement.id} className="wrap-break-word">
              {requirement.label} — {requirement.kind} · {requirement.mode}
            </li>
          ))}
          {hiddenCount > 0 ? (
            <li className="text-muted-foreground">
              +{hiddenCount} more {hiddenCount === 1 ? "criterion" : "criteria"}
            </li>
          ) : null}
        </ul>
      )}
      {context.catalogVersion === null ? (
        <p className="text-xs text-muted-foreground">
          No reason catalog yet — the first Find jobs run authors one for this
          brief version.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          Reason catalog {context.catalogVersion} ready.
        </p>
      )}
      {context.briefStale ? (
        <div className="flex min-w-0 flex-col gap-2">
          <p role="alert" className="text-sm wrap-break-word text-destructive">
            The saved brief is behind profile v{context.profileVersion}.
            Refresh before starting a run.
          </p>
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={retryContext}
            >
              Refresh saved brief
            </Button>
          </div>
        </div>
      ) : null}
      {runBriefProfileVersion !== null &&
      runBriefProfileVersion !== context.profileVersion ? (
        <p className="text-xs text-muted-foreground wrap-break-word">
          The run below used brief profile v{runBriefProfileVersion}; the saved
          brief is now profile v{context.profileVersion}.
        </p>
      ) : null}
      <ChangeSearchLink href={changeSearchHref} />
    </div>
  )
}
