import {
  getPreferences,
  getSearchBrief,
  RequestError,
  type Preferences,
  type SearchBriefView,
} from "@/api/client"
import { EmptyBlock, ErrorBlock, LoadingBlock } from "@/components/shared"
import { Button } from "@/components/ui/button"
import { formatCents } from "@/pages/format"
import { useRead, type ReadResult } from "@/pages/useRead"

// SavedBriefData is the effective saved search basis for discovery: the
// current saved profile plus the server-derived brief for it. A null brief
// is the honest pre-first-save state (GET /research/brief 404s), not an
// error. All reads here are GET-only; nothing commissions work.
export interface SavedBriefData {
  preferences: Preferences
  brief: SearchBriefView | null
}

export type SavedBriefRead = ReadResult<SavedBriefData>

async function loadSavedBrief(signal: AbortSignal): Promise<SavedBriefData> {
  const [preferences, brief] = await Promise.all([
    getPreferences(signal),
    getSearchBrief(signal).catch((cause: unknown) => {
      if (cause instanceof RequestError && cause.status === 404) return null
      throw cause
    }),
  ])
  return { preferences, brief }
}

export function useSavedBrief(): SavedBriefRead {
  return useRead("discovery:saved-brief", loadSavedBrief)
}

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

export interface SavedBriefReadiness {
  ready: boolean
  reason: string | null
}

// savedBriefReadiness is the Find jobs gate: the run basis must be a loaded,
// fresh saved brief. A missing reason catalog is a normal pre-first-run
// state and never blocks.
export function savedBriefReadiness(read: SavedBriefRead): SavedBriefReadiness {
  if (read.status === "loading")
    return { ready: false, reason: "Loading the saved search brief…" }
  if (read.status === "error")
    return {
      ready: false,
      reason: "The saved search brief could not be loaded. Fix the error above first.",
    }
  const { preferences, brief } = read.data
  if (brief === null)
    return {
      ready: false,
      reason: "No saved search brief yet. Save your search before starting a run.",
    }
  if (brief.profileVersion !== preferences.version)
    return {
      ready: false,
      reason: `The saved brief (profile v${brief.profileVersion}) is behind profile v${preferences.version}. Refresh before starting a run.`,
    }
  return { ready: true, reason: null }
}

export interface SavedBriefPanelProps {
  read: SavedBriefRead
  // Profile version the visible run was commissioned against, when a run is
  // shown. Compared honestly against the current saved brief.
  runBriefProfileVersion?: number | null
  changeSearchHref?: string
}

// SavedBriefPanel renders the saved search basis: version identity, concise
// terms, catalog state and staleness. It never commissions work. Lane B owns
// the correction flow behind the Change my search link; RW-C2 wires it.
export function SavedBriefPanel({
  read,
  runBriefProfileVersion = null,
  changeSearchHref = "#/search",
}: SavedBriefPanelProps) {
  if (read.status === "loading") {
    return <LoadingBlock label="Loading saved search brief…" />
  }
  if (read.status === "error") {
    return (
      <ErrorBlock
        title="Could not load the saved search brief"
        message={read.error}
        onRetry={read.retry}
      />
    )
  }

  const { preferences, brief } = read.data
  if (brief === null) {
    return (
      <div className="flex min-w-0 flex-col gap-3 rounded-2xl border bg-card px-4 py-4">
        <EmptyBlock
          title="No saved search brief yet"
          description={`Profile v${preferences.version} is saved, but the server has no search brief for it yet. Save your search before starting a run.`}
        />
        <p className="text-sm">
          <a className="underline underline-offset-4" href={changeSearchHref}>
            Change my search
          </a>
        </p>
      </div>
    )
  }

  const stale = brief.profileVersion !== preferences.version
  const shownRequirements = brief.requirements.slice(0, 3)
  const hiddenCount = brief.requirements.length - shownRequirements.length

  return (
    <div className="flex min-w-0 flex-col gap-2 rounded-2xl border bg-card px-4 py-4">
      <h3 className="font-heading text-base font-medium">Saved search brief</h3>
      <p className="text-sm wrap-break-word">
        Profile v{brief.profileVersion} · {brief.rubricVersion}
      </p>
      <p className="text-xs text-muted-foreground wrap-break-word">
        {brief.rubricSource}
      </p>
      <p className="text-sm text-muted-foreground wrap-break-word">
        {summarizeSavedBriefTerms(preferences)}
      </p>
      {brief.requirements.length === 0 ? (
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
      {brief.catalogVersion === undefined ? (
        <p className="text-xs text-muted-foreground">
          No reason catalog yet — the first Find jobs run authors one for this
          brief version.
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          Reason catalog {brief.catalogVersion} ready.
        </p>
      )}
      {stale ? (
        <div className="flex min-w-0 flex-col gap-2">
          <p role="alert" className="text-sm wrap-break-word text-destructive">
            The saved brief (profile v{brief.profileVersion}) is behind profile
            v{preferences.version}. Refresh before starting a run.
          </p>
          <div>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={read.retry}
            >
              Refresh saved brief
            </Button>
          </div>
        </div>
      ) : null}
      {runBriefProfileVersion !== null &&
      runBriefProfileVersion !== brief.profileVersion ? (
        <p className="text-xs text-muted-foreground wrap-break-word">
          The run below used brief profile v{runBriefProfileVersion}; the saved
          brief is now profile v{brief.profileVersion}.
        </p>
      ) : null}
      <p className="text-sm">
        <a className="underline underline-offset-4" href={changeSearchHref}>
          Change my search
        </a>
      </p>
    </div>
  )
}
